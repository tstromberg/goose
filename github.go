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
		return "", errors.New("gh cli not found in trusted locations")
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

// fetchPRsInternal is the implementation for PR fetching.
// It returns GitHub data immediately and starts Turn API queries in the background (when waitForTurn=false),
// or waits for Turn data to complete (when waitForTurn=true).
func (app *App) fetchPRsInternal(ctx context.Context, waitForTurn bool) (incoming []PR, outgoing []PR, err error) {
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

	var result *github.IssuesSearchResult
	var resp *github.Response
	err = retry.Do(func() error {
		// Create timeout context for GitHub API call
		githubCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		var retryErr error
		result, resp, retryErr = app.client.Search.Issues(githubCtx, query, opts)
		if retryErr != nil {
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
						log.Printf("GitHub API rate limited, reset at: %s (will retry)", resetTime)
						return retryErr // Retry on rate limit
					}
					log.Print("GitHub API access forbidden (check token permissions)")
					return retry.Unrecoverable(fmt.Errorf("github API access forbidden: %w", retryErr))
				case httpStatusUnauthorized:
					log.Print("GitHub API authentication failed (check token)")
					return retry.Unrecoverable(fmt.Errorf("github API authentication failed: %w", retryErr))
				case httpStatusUnprocessable:
					log.Printf("GitHub API query invalid: %s", query)
					return retry.Unrecoverable(fmt.Errorf("github API query invalid: %w", retryErr))
				default:
					log.Printf("GitHub API error (status %d): %v (will retry)", resp.StatusCode, retryErr)
				}
			} else {
				// Likely network error - retry these
				log.Printf("GitHub API network error: %v (will retry)", retryErr)
			}
			return retryErr
		}
		return nil
	},
		retry.Attempts(maxRetries),
		retry.DelayType(retry.BackOffDelay),
		retry.MaxDelay(maxRetryDelay),
		retry.OnRetry(func(n uint, err error) {
			log.Printf("GitHub Search.Issues retry %d/%d: %v", n+1, maxRetries, err)
		}),
		retry.Context(ctx),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("search PRs after %d retries: %w", maxRetries, err)
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
	for i := range incoming {
		log.Printf("[GITHUB] Incoming PR: %s", incoming[i].URL)
	}
	for i := range outgoing {
		log.Printf("[GITHUB] Outgoing PR: %s", outgoing[i].URL)
	}

	// Fetch Turn API data
	if waitForTurn {
		// Synchronous - wait for Turn data
		log.Println("[TURN] Fetching Turn API data synchronously before building menu...")
		app.fetchTurnDataSync(ctx, result.Issues, user, &incoming, &outgoing)
	} else {
		// Asynchronous - start in background
		app.mu.Lock()
		app.loadingTurnData = true
		app.pendingTurnResults = make([]TurnResult, 0) // Reset buffer
		app.mu.Unlock()
		go app.fetchTurnDataAsync(ctx, result.Issues, user)
	}

	return incoming, outgoing, nil
}

// updatePRData updates PR data with Turn API results.
func (app *App) updatePRData(url string, needsReview bool, isOwner bool, actionReason string) (*PR, bool) {
	app.mu.Lock()
	defer app.mu.Unlock()

	if isOwner {
		// Update outgoing PRs
		for i := range app.outgoing {
			if app.outgoing[i].URL != url {
				continue
			}
			// Check if Turn data was already applied for this UpdatedAt
			now := time.Now()
			if app.outgoing[i].TurnDataAppliedAt.After(app.outgoing[i].UpdatedAt) {
				// Turn data already applied for this PR version, no change
				return &app.outgoing[i], false
			}
			changed := app.outgoing[i].NeedsReview != needsReview ||
				app.outgoing[i].IsBlocked != needsReview ||
				app.outgoing[i].ActionReason != actionReason
			app.outgoing[i].NeedsReview = needsReview
			app.outgoing[i].IsBlocked = needsReview
			app.outgoing[i].ActionReason = actionReason
			app.outgoing[i].TurnDataAppliedAt = now
			return &app.outgoing[i], changed
		}
	} else {
		// Update incoming PRs
		for i := range app.incoming {
			if app.incoming[i].URL != url {
				continue
			}
			// Check if Turn data was already applied for this UpdatedAt
			now := time.Now()
			if app.incoming[i].TurnDataAppliedAt.After(app.incoming[i].UpdatedAt) {
				// Turn data already applied for this PR version, no change
				return &app.incoming[i], false
			}
			changed := app.incoming[i].NeedsReview != needsReview ||
				app.incoming[i].ActionReason != actionReason
			app.incoming[i].NeedsReview = needsReview
			app.incoming[i].ActionReason = actionReason
			app.incoming[i].TurnDataAppliedAt = now
			return &app.incoming[i], changed
		}
	}
	return nil, false
}

// fetchTurnDataSync fetches Turn API data synchronously and updates PRs directly.
func (app *App) fetchTurnDataSync(ctx context.Context, issues []*github.Issue, user string, incoming *[]PR, outgoing *[]PR) {
	turnStart := time.Now()
	type prResult struct {
		err          error
		turnData     *turn.CheckResponse
		url          string
		isOwner      bool
		wasFromCache bool
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

			url := issue.GetHTMLURL()
			updatedAt := issue.GetUpdatedAt().Time

			// Call turnData - it now has proper exponential backoff with jitter
			turnData, wasFromCache, err := app.turnData(ctx, url, updatedAt)

			results <- prResult{
				url:          issue.GetHTMLURL(),
				turnData:     turnData,
				err:          err,
				isOwner:      issue.GetUser().GetLogin() == user,
				wasFromCache: wasFromCache,
			}
		}(issue)
	}

	// Close the results channel when all goroutines are done
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results and update PRs directly
	turnSuccesses := 0
	turnFailures := 0

	for result := range results {
		if result.err == nil && result.turnData != nil && result.turnData.PRState.UnblockAction != nil {
			turnSuccesses++

			// Check if user needs to review and get action reason
			needsReview := false
			actionReason := ""
			if action, exists := result.turnData.PRState.UnblockAction[user]; exists {
				needsReview = true
				actionReason = action.Reason
				log.Printf("[TURN] UnblockAction for %s: Reason=%q, Kind=%q", result.url, action.Reason, action.Kind)
			}

			// Update the PR in the slices directly
			if result.isOwner {
				for i := range *outgoing {
					if (*outgoing)[i].URL == result.url {
						(*outgoing)[i].NeedsReview = needsReview
						(*outgoing)[i].IsBlocked = needsReview
						(*outgoing)[i].ActionReason = actionReason
						break
					}
				}
			} else {
				for i := range *incoming {
					if (*incoming)[i].URL == result.url {
						(*incoming)[i].NeedsReview = needsReview
						(*incoming)[i].ActionReason = actionReason
						break
					}
				}
			}
		} else if result.err != nil {
			turnFailures++
		}
	}

	log.Printf("[TURN] Turn API queries completed in %v (%d/%d succeeded)",
		time.Since(turnStart), turnSuccesses, turnSuccesses+turnFailures)
}

// fetchTurnDataAsync fetches Turn API data in the background and updates PRs as results arrive.
func (app *App) fetchTurnDataAsync(ctx context.Context, issues []*github.Issue, user string) {
	// Log start of Turn API queries
	log.Print("[TURN] Starting Turn API queries in background")

	turnStart := time.Now()
	type prResult struct {
		err          error
		turnData     *turn.CheckResponse
		url          string
		isOwner      bool
		wasFromCache bool
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

			url := issue.GetHTMLURL()
			updatedAt := issue.GetUpdatedAt().Time

			// Call turnData - it now has proper exponential backoff with jitter
			turnData, wasFromCache, err := app.turnData(ctx, url, updatedAt)

			results <- prResult{
				url:          issue.GetHTMLURL(),
				turnData:     turnData,
				err:          err,
				isOwner:      issue.GetUser().GetLogin() == user,
				wasFromCache: wasFromCache,
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

	// Process results as they arrive and buffer them

	for result := range results {
		if result.err == nil && result.turnData != nil && result.turnData.PRState.UnblockAction != nil {
			turnSuccesses++

			// Check if user needs to review and get action reason
			needsReview := false
			actionReason := ""
			if action, exists := result.turnData.PRState.UnblockAction[user]; exists {
				needsReview = true
				actionReason = action.Reason
				log.Printf("[TURN] UnblockAction for %s: Reason=%q, Kind=%q", result.url, action.Reason, action.Kind)
			} else if !result.wasFromCache {
				// Only log "no action" for fresh API results, not cached ones
				log.Printf("[TURN] No UnblockAction found for user %s on %s", user, result.url)
			}

			// Buffer the Turn result instead of applying immediately
			turnResult := TurnResult{
				URL:          result.url,
				NeedsReview:  needsReview,
				IsOwner:      result.isOwner,
				ActionReason: actionReason,
				WasFromCache: result.wasFromCache,
			}

			app.mu.Lock()
			app.pendingTurnResults = append(app.pendingTurnResults, turnResult)
			app.mu.Unlock()

			updatesApplied++
			// Reduce verbosity - only log if not from cache or if blocked
			if !result.wasFromCache || needsReview {
				cacheStatus := "fresh"
				if result.wasFromCache {
					cacheStatus = "cached"
				}
				log.Printf("[TURN] %s data for %s (needsReview=%v, actionReason=%q)", cacheStatus, result.url, needsReview, actionReason)
			}
		} else if result.err != nil {
			turnFailures++
		}
	}

	log.Printf("[TURN] Turn API queries completed in %v (%d/%d succeeded, %d PRs updated)",
		time.Since(turnStart), turnSuccesses, turnSuccesses+turnFailures, updatesApplied)

	// Apply all buffered Turn results at once
	app.mu.Lock()
	pendingResults := app.pendingTurnResults
	app.pendingTurnResults = nil
	app.loadingTurnData = false
	app.mu.Unlock()

	// Check if any results came from fresh API calls (not cache)
	var cacheHits, freshResults int
	for _, result := range pendingResults {
		if result.WasFromCache {
			cacheHits++
		} else {
			freshResults++
		}
	}

	log.Printf("[TURN] Applying %d buffered Turn results (%d from cache, %d fresh)", len(pendingResults), cacheHits, freshResults)

	// Track how many PRs actually changed
	var actualChanges int
	for _, result := range pendingResults {
		_, changed := app.updatePRData(result.URL, result.NeedsReview, result.IsOwner, result.ActionReason)
		if changed {
			actualChanges++
		}
	}

	// Update tray title and menu with final Turn data if menu is already initialized
	app.setTrayTitle()
	if app.menuInitialized {
		// Only trigger menu update if PR data actually changed
		if actualChanges > 0 {
			log.Printf("[TURN] Turn data applied - %d PRs actually changed, checking if menu needs update", actualChanges)
			app.updateMenuIfChanged(ctx)
		} else {
			log.Print("[TURN] Turn data applied - no PR changes detected (cached data unchanged), skipping menu update")
		}
	}
}
