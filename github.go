// Package main - github.go contains GitHub API integration functions.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/codeGROOVE-dev/retry"
	"github.com/google/go-github/v57/github"
	"github.com/ready-to-review/turnclient/pkg/turn"
	"golang.org/x/oauth2"
)

// initClients initializes GitHub and Turn API clients.
func (app *App) initClients(ctx context.Context) error {
	token, err := app.githubToken(ctx)
	if err != nil {
		return fmt.Errorf("get github token: %w", err)
	}

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
	app.client = github.NewClient(tc)

	// Initialize Turn client with base URL
	turnClient, err := turn.NewClient("https://turn.ready-to-review.dev")
	if err != nil {
		return fmt.Errorf("create turn client: %w", err)
	}
	turnClient.SetAuthToken(token)
	app.turnClient = turnClient

	return nil
}

// githubToken retrieves the GitHub token using gh CLI.
func (*App) githubToken(ctx context.Context) (string, error) {
	// Only check absolute paths for security - never use PATH
	var trustedPaths []string
	switch runtime.GOOS {
	case "windows":
		trustedPaths = []string{
			`C:\Program Files\GitHub CLI\gh.exe`,
			`C:\Program Files (x86)\GitHub CLI\gh.exe`,
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "gh", "gh.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "GitHub CLI", "gh.exe"),
		}
	case "darwin":
		trustedPaths = []string{
			"/opt/homebrew/bin/gh", // Homebrew on Apple Silicon
			"/usr/local/bin/gh",    // Homebrew on Intel / manual install
			"/usr/bin/gh",          // System package managers
		}
	case "linux":
		trustedPaths = []string{
			"/usr/local/bin/gh",                 // Manual install
			"/usr/bin/gh",                       // System package managers
			"/home/linuxbrew/.linuxbrew/bin/gh", // Linuxbrew
			"/snap/bin/gh",                      // Snap package
		}
	default:
		// BSD and other Unix-like systems
		trustedPaths = []string{
			"/usr/local/bin/gh",
			"/usr/bin/gh",
		}
	}

	var ghPath string
	for _, path := range trustedPaths {
		// Verify the file exists and is executable
		if info, err := os.Stat(path); err == nil {
			// Check if it's a regular file and executable
			const executableMask = 0o111
			if info.Mode().IsRegular() && info.Mode()&executableMask != 0 {
				// Verify it's actually the gh binary by running version command
				cmd := exec.Command(path, "version") //nolint:noctx // Quick version check doesn't need context
				output, err := cmd.Output()
				if err == nil && strings.Contains(string(output), "gh version") {
					log.Printf("Found and verified gh at: %s", path)
					ghPath = path
					break
				}
			}
		}
	}

	if ghPath == "" {
		return "", errors.New("gh cli not found in trusted locations, please install from https://cli.github.com")
	}

	log.Printf("Executing command: %s auth token", ghPath)
	cmd := exec.CommandContext(ctx, ghPath, "auth", "token")
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("gh command failed with output: %s", string(output))
		return "", fmt.Errorf("exec 'gh auth token': %w (output: %s)", err, string(output))
	}
	token := strings.TrimSpace(string(output))
	if token == "" {
		return "", errors.New("empty github token")
	}
	const minTokenLength = 20
	if len(token) < minTokenLength {
		return "", fmt.Errorf("invalid github token length: %d", len(token))
	}
	log.Println("Successfully obtained GitHub token")
	return token, nil
}

// fetchPRs retrieves all PRs involving the current user.
// It returns GitHub data immediately and starts Turn API queries in the background.
func (app *App) fetchPRs(ctx context.Context) (incoming []PR, outgoing []PR, err error) {
	// Use targetUser if specified, otherwise use authenticated user
	user := app.currentUser.GetLogin()
	if app.targetUser != "" {
		user = app.targetUser
	}

	// Single query to get all PRs involving the user
	query := fmt.Sprintf("is:open is:pr involves:%s archived:false", user)

	const perPage = 100
	opts := &github.SearchOptions{
		ListOptions: github.ListOptions{PerPage: perPage},
		Sort:        "updated",
		Order:       "desc",
	}

	log.Printf("Searching for PRs with query: %s", query)
	searchStart := time.Now()

	// Create timeout context for GitHub API call
	githubCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	result, resp, err := app.client.Search.Issues(githubCtx, query, opts)
	if err != nil {
		// Enhanced error handling with specific cases
		if resp != nil {
			const (
				httpStatusUnauthorized  = 401
				httpStatusForbidden     = 403
				httpStatusUnprocessable = 422
			)
			switch resp.StatusCode {
			case httpStatusForbidden:
				if resp.Header.Get("X-Ratelimit-Remaining") == "0" {
					resetTime := resp.Header.Get("X-Ratelimit-Reset")
					log.Printf("GitHub API rate limited, reset at: %s", resetTime)
					return nil, nil, fmt.Errorf("github API rate limited, try again later: %w", err)
				}
				log.Print("GitHub API access forbidden (check token permissions)")
				return nil, nil, fmt.Errorf("github API access forbidden: %w", err)
			case httpStatusUnauthorized:
				log.Print("GitHub API authentication failed (check token)")
				return nil, nil, fmt.Errorf("github API authentication failed: %w", err)
			case httpStatusUnprocessable:
				log.Printf("GitHub API query invalid: %s", query)
				return nil, nil, fmt.Errorf("github API query invalid: %w", err)
			default:
				log.Printf("GitHub API error (status %d): %v", resp.StatusCode, err)
			}
		} else {
			// Likely network error
			log.Printf("GitHub API network error: %v", err)
		}
		return nil, nil, fmt.Errorf("search PRs: %w", err)
	}

	log.Printf("GitHub search completed in %v, found %d PRs", time.Since(searchStart), len(result.Issues))

	// Limit PRs for performance
	if len(result.Issues) > maxPRsToProcess {
		log.Printf("Limiting to %d PRs for performance (total: %d)", maxPRsToProcess, len(result.Issues))
		result.Issues = result.Issues[:maxPRsToProcess]
	}

	// Process GitHub results immediately
	for _, issue := range result.Issues {
		if !issue.IsPullRequest() {
			continue
		}
		repo := strings.TrimPrefix(issue.GetRepositoryURL(), "https://api.github.com/repos/")

		pr := PR{
			Title:      issue.GetTitle(),
			URL:        issue.GetHTMLURL(),
			Repository: repo,
			Number:     issue.GetNumber(),
			UpdatedAt:  issue.GetUpdatedAt().Time,
		}

		// Categorize as incoming or outgoing
		// When viewing another user's PRs, we're looking at it from their perspective
		if issue.GetUser().GetLogin() == user {
			outgoing = append(outgoing, pr)
		} else {
			incoming = append(incoming, pr)
		}
	}

	log.Printf("[GITHUB] Found %d incoming, %d outgoing PRs from GitHub", len(incoming), len(outgoing))
	for _, pr := range incoming {
		log.Printf("[GITHUB] Incoming PR: %s", pr.URL)
	}
	for _, pr := range outgoing {
		log.Printf("[GITHUB] Outgoing PR: %s", pr.URL)
	}

	// Start Turn API queries in background
	go app.fetchTurnDataAsync(ctx, result.Issues, user)

	return incoming, outgoing, nil
}

// updatePRData updates PR data with Turn API results.
func (app *App) updatePRData(url string, needsReview bool, isOwner bool) *PR {
	app.mu.Lock()
	defer app.mu.Unlock()

	if isOwner {
		// Update outgoing PRs
		for i := range app.outgoing {
			if app.outgoing[i].URL == url {
				app.outgoing[i].NeedsReview = needsReview
				app.outgoing[i].IsBlocked = needsReview
				return &app.outgoing[i]
			}
		}
	} else {
		// Update incoming PRs
		for i := range app.incoming {
			if app.incoming[i].URL == url {
				app.incoming[i].NeedsReview = needsReview
				return &app.incoming[i]
			}
		}
	}
	return nil
}

// fetchTurnDataAsync fetches Turn API data in the background and updates PRs as results arrive.
func (app *App) fetchTurnDataAsync(ctx context.Context, issues []*github.Issue, user string) {
	// Set loading state
	app.mu.Lock()
	app.turnDataLoading = true
	app.mu.Unlock()

	// Update section headers to show loading state
	log.Print("[TURN] Starting Turn API queries, updating section headers to show loading state")
	app.updateSectionHeaders()

	turnStart := time.Now()
	type prResult struct {
		err      error
		turnData *turn.CheckResponse
		url      string
		isOwner  bool
	}

	// Create a channel for results
	results := make(chan prResult, len(issues))

	// Use a WaitGroup to track goroutines
	var wg sync.WaitGroup

	// Process PRs in parallel
	for _, issue := range issues {
		if !issue.IsPullRequest() {
			continue
		}

		wg.Add(1)
		go func(issue *github.Issue) {
			defer wg.Done()

			// Retry logic for Turn API with exponential backoff and jitter
			var turnData *turn.CheckResponse
			var err error

			turnData, err = retry.DoWithData(
				func() (*turn.CheckResponse, error) {
					data, apiErr := app.turnData(ctx, issue.GetHTMLURL(), issue.GetUpdatedAt().Time)
					if apiErr != nil {
						log.Printf("Turn API attempt failed for %s: %v", issue.GetHTMLURL(), apiErr)
					}
					return data, apiErr
				},
				retry.Context(ctx),
				retry.Attempts(5),                             // 5 attempts max
				retry.Delay(500*time.Millisecond),             // Start with 500ms
				retry.MaxDelay(30*time.Second),                // Cap at 30 seconds
				retry.DelayType(retry.FullJitterBackoffDelay), // Exponential backoff with jitter
				retry.OnRetry(func(attempt uint, err error) {
					log.Printf("Turn API retry attempt %d for %s: %v", attempt, issue.GetHTMLURL(), err)
				}),
			)
			if err != nil {
				log.Printf("Turn API failed after all retries for %s: %v", issue.GetHTMLURL(), err)
			}

			results <- prResult{
				url:      issue.GetHTMLURL(),
				turnData: turnData,
				err:      err,
				isOwner:  issue.GetUser().GetLogin() == user,
			}
		}(issue)
	}

	// Close the results channel when all goroutines are done
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results and update PRs incrementally
	turnSuccesses := 0
	turnFailures := 0
	updatesApplied := 0

	// Batch updates to reduce menu rebuilds
	updateBatch := 0
	const batchSize = 10
	lastUpdateTime := time.Now()
	const minUpdateInterval = 500 * time.Millisecond

	for result := range results {
		if result.err == nil && result.turnData != nil && result.turnData.PRState.UnblockAction != nil {
			turnSuccesses++

			// Check if user needs to review
			needsReview := false
			if _, exists := result.turnData.PRState.UnblockAction[user]; exists {
				needsReview = true
			}

			// Update the PR in our lists
			pr := app.updatePRData(result.url, needsReview, result.isOwner)

			if pr != nil {
				updatesApplied++
				updateBatch++
				log.Printf("[TURN] Turn data received for %s (needsReview=%v)", result.url, needsReview)
				// Update the specific menu item immediately
				app.updatePRMenuItem(*pr)

				// Periodically update section headers and tray title
				if updateBatch >= batchSize || time.Since(lastUpdateTime) >= minUpdateInterval {
					log.Printf("[TURN] Batch update threshold reached (%d updates), updating headers and title", updateBatch)
					app.updateSectionHeaders()
					app.setTrayTitle()
					updateBatch = 0
					lastUpdateTime = time.Now()
				}
			}
		} else if result.err != nil {
			turnFailures++
		}
	}

	// Clear loading state
	app.mu.Lock()
	app.turnDataLoading = false
	app.turnDataLoaded = true
	app.mu.Unlock()

	log.Printf("[TURN] Turn API queries completed in %v (%d/%d succeeded, %d PRs updated)",
		time.Since(turnStart), turnSuccesses, turnSuccesses+turnFailures, updatesApplied)

	// Update section headers with final counts
	log.Print("[TURN] Updating section headers and tray title with final counts")
	app.updateSectionHeaders()

	// Update tray title
	app.setTrayTitle()
}
