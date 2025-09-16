// Package main - github.go contains GitHub API integration functions.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

// extractOrgFromRepo extracts the organization name from a repository path like "org/repo".
func extractOrgFromRepo(repo string) string {
	parts := strings.Split(repo, "/")
	if len(parts) >= 1 {
		return parts[0]
	}
	return ""
}

// initClients initializes GitHub and Turn API clients.
func (app *App) initClients(ctx context.Context) error {
	token, err := app.token(ctx)
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

// token retrieves the GitHub token from GITHUB_TOKEN env var or gh CLI.
func (*App) token(ctx context.Context) (string, error) {
	// Check GITHUB_TOKEN environment variable first
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		token = strings.TrimSpace(token)
		// Validate token format inline
		if token == "" {
			return "", errors.New("GITHUB_TOKEN is empty")
		}
		if !githubTokenRegex.MatchString(token) {
			return "", errors.New("GITHUB_TOKEN has invalid format")
		}
		slog.Info("Using GitHub token from GITHUB_TOKEN environment variable")
		return token, nil
	}
	// Try to find gh in PATH first
	ghPath, err := exec.LookPath("gh")
	if err == nil {
		slog.Debug("Found gh in PATH", "path", ghPath)
		// Resolve any symlinks to get the real path
		if realPath, err := filepath.EvalSymlinks(ghPath); err == nil {
			ghPath = realPath
			slog.Debug("Resolved gh path", "path", ghPath)
		}
	} else {
		// Fall back to checking common installation paths
		slog.Debug("gh not found in PATH, checking common locations...")
		var commonPaths []string
		switch runtime.GOOS {
		case "windows":
			commonPaths = []string{
				`C:\Program Files\GitHub CLI\gh.exe`,
				`C:\Program Files (x86)\GitHub CLI\gh.exe`,
				filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "gh", "gh.exe"),
				filepath.Join(os.Getenv("LOCALAPPDATA"), "GitHub CLI", "gh.exe"),
			}
		case "darwin":
			commonPaths = []string{
				"/opt/homebrew/bin/gh",                 // Homebrew on Apple Silicon
				"/usr/local/bin/gh",                    // Homebrew on Intel / manual install
				"/usr/bin/gh",                          // System package managers
				"/opt/local/bin/gh",                    // MacPorts
				"/run/current-system/sw/bin/gh",        // Nix
				"/nix/var/nix/profiles/default/bin/gh", // Nix fallback
			}
		case "linux":
			homeDir := os.Getenv("HOME")
			commonPaths = []string{
				"/usr/local/bin/gh",                       // Manual install
				"/usr/bin/gh",                             // System package managers (apt, dnf, etc)
				"/home/linuxbrew/.linuxbrew/bin/gh",       // Linuxbrew
				"/snap/bin/gh",                            // Snap package
				"/run/current-system/sw/bin/gh",           // NixOS
				"/var/lib/flatpak/exports/bin/gh",         // Flatpak system
				"/usr/local/go/bin/gh",                    // Go install
				filepath.Join(homeDir, "go", "bin", "gh"), // Go install user
				"/opt/gh/bin/gh",                          // Custom installs
			}
		default:
			// BSD and other Unix-like systems
			commonPaths = []string{
				"/usr/local/bin/gh",
				"/usr/bin/gh",
				"/usr/pkg/bin/gh",   // NetBSD pkgsrc
				"/opt/local/bin/gh", // OpenBSD ports
			}
		}

		for _, path := range commonPaths {
			if path == "" {
				continue // Skip empty paths from unset env vars
			}
			if _, err := os.Stat(path); err == nil {
				slog.Debug("Found gh at common location", "path", path)
				ghPath = path
				break
			}
		}
	}

	if ghPath == "" {
		return "", errors.New("gh CLI not found in PATH or common locations, and GITHUB_TOKEN not set")
	}

	slog.Debug("Executing gh command", "command", ghPath+" auth token")

	// Use retry logic for gh CLI command as it may fail temporarily
	var token string
	retryErr := retry.Do(func() error {
		// Create timeout context for gh CLI call
		cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		cmd := exec.CommandContext(cmdCtx, ghPath, "auth", "token")
		output, cmdErr := cmd.CombinedOutput()
		if cmdErr != nil {
			slog.Warn("gh command failed (will retry)", "error", cmdErr)
			return fmt.Errorf("exec 'gh auth token': %w", cmdErr)
		}

		token = strings.TrimSpace(string(output))
		if validateErr := validateGitHubToken(token); validateErr != nil {
			// Don't retry on invalid token - it won't get better
			return retry.Unrecoverable(fmt.Errorf("invalid token from gh CLI: %w", validateErr))
		}
		return nil
	},
		retry.Attempts(3), // Fewer attempts for local command
		retry.Delay(time.Second),
		retry.OnRetry(func(n uint, err error) {
			slog.Warn("[GH CLI] Retry attempt", "attempt", n+1, "maxAttempts", 3, "error", err)
		}),
		retry.Context(ctx),
	)
	if retryErr != nil {
		return "", retryErr
	}

	slog.Info("Successfully obtained GitHub token from gh CLI")
	return token, nil
}

// executeGitHubQuery executes a single GitHub search query with retry logic.
func (app *App) executeGitHubQuery(ctx context.Context, query string, opts *github.SearchOptions) (*github.IssuesSearchResult, error) {
	var result *github.IssuesSearchResult
	var resp *github.Response

	err := retry.Do(func() error {
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
						slog.Warn("GitHub API rate limited (will retry)", "resetTime", resetTime)
						return retryErr // Retry on rate limit
					}
					slog.Error("GitHub API access forbidden (check token permissions)")
					return retry.Unrecoverable(fmt.Errorf("github API access forbidden: %w", retryErr))
				case httpStatusUnauthorized:
					slog.Error("GitHub API authentication failed (check token)")
					return retry.Unrecoverable(fmt.Errorf("github API authentication failed: %w", retryErr))
				case httpStatusUnprocessable:
					slog.Error("GitHub API query invalid", "query", query)
					return retry.Unrecoverable(fmt.Errorf("github API query invalid: %w", retryErr))
				default:
					slog.Warn("GitHub API error (will retry)", "statusCode", resp.StatusCode, "error", retryErr)
				}
			} else {
				// Likely network error - retry these
				slog.Warn("GitHub API network error (will retry)", "error", retryErr)
			}
			return retryErr
		}
		return nil
	},
		retry.Attempts(maxRetries),
		retry.DelayType(retry.CombineDelay(retry.BackOffDelay, retry.RandomDelay)), // Add jitter for better backoff distribution
		retry.MaxDelay(maxRetryDelay),
		retry.OnRetry(func(n uint, err error) {
			slog.Warn("[GITHUB] Search.Issues retry", "attempt", n+1, "maxRetries", maxRetries, "error", err)
		}),
		retry.Context(ctx),
	)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// prResult holds the result of a Turn API query for a PR.
type prResult struct {
	err          error
	turnData     *turn.CheckResponse
	url          string
	isOwner      bool
	wasFromCache bool
}

// fetchPRsInternal fetches PRs and Turn data synchronously for simplicity.
func (app *App) fetchPRsInternal(ctx context.Context) (incoming []PR, outgoing []PR, _ error) {
	// Update search attempt time for rate limiting
	app.mu.Lock()
	app.lastSearchAttempt = time.Now()
	app.mu.Unlock()

	// Check if we have a client
	if app.client == nil {
		return nil, nil, fmt.Errorf("no GitHub client available: %s", app.authError)
	}

	// Use targetUser if specified, otherwise use authenticated user
	user := ""
	if app.currentUser != nil {
		user = app.currentUser.GetLogin()
	}
	if app.targetUser != "" {
		user = app.targetUser
	}
	if user == "" {
		return nil, nil, errors.New("no user specified and current user not loaded")
	}

	const perPage = 100
	opts := &github.SearchOptions{
		ListOptions: github.ListOptions{PerPage: perPage},
		Sort:        "updated",
		Order:       "desc",
	}

	searchStart := time.Now()

	// Run both queries in parallel
	type queryResult struct {
		err    error
		query  string
		issues []*github.Issue
	}

	queryResults := make(chan queryResult, 2)

	// Query 1: PRs involving the user
	go func() {
		query := fmt.Sprintf("is:open is:pr involves:%s archived:false", user)
		slog.Debug("[GITHUB] Searching for PRs", "query", query)

		result, err := app.executeGitHubQuery(ctx, query, opts)
		if err != nil {
			queryResults <- queryResult{err: err, query: query}
		} else {
			queryResults <- queryResult{issues: result.Issues, query: query}
		}
	}()

	// Query 2: PRs in user-owned repos with no reviewers
	go func() {
		query := fmt.Sprintf("is:open is:pr user:%s review:none archived:false", user)
		slog.Debug("[GITHUB] Searching for PRs", "query", query)

		result, err := app.executeGitHubQuery(ctx, query, opts)
		if err != nil {
			queryResults <- queryResult{err: err, query: query}
		} else {
			queryResults <- queryResult{issues: result.Issues, query: query}
		}
	}()

	// Collect results from both queries
	var allIssues []*github.Issue
	seenURLs := make(map[string]bool)
	var queryErrors []error

	for range 2 {
		result := <-queryResults
		if result.err != nil {
			slog.Error("[GITHUB] Query failed", "query", result.query, "error", result.err)
			queryErrors = append(queryErrors, result.err)
			// Continue processing other query results even if one fails
			continue
		}
		slog.Debug("[GITHUB] Query completed", "query", result.query, "prCount", len(result.issues))

		// Deduplicate PRs based on URL
		for _, issue := range result.issues {
			url := issue.GetHTMLURL()
			if !seenURLs[url] {
				seenURLs[url] = true
				allIssues = append(allIssues, issue)
			}
		}
	}
	slog.Info("[GITHUB] Both searches completed", "duration", time.Since(searchStart), "uniquePRs", len(allIssues))

	// If both queries failed, return an error
	if len(queryErrors) == 2 {
		return nil, nil, fmt.Errorf("all GitHub queries failed: %v", queryErrors)
	}

	// Limit PRs for performance
	if len(allIssues) > maxPRsToProcess {
		slog.Info("Limiting PRs for performance", "limit", maxPRsToProcess, "total", len(allIssues))
		allIssues = allIssues[:maxPRsToProcess]
	}

	// Process GitHub results immediately
	for _, issue := range allIssues {
		if !issue.IsPullRequest() {
			continue
		}
		repo := strings.TrimPrefix(issue.GetRepositoryURL(), "https://api.github.com/repos/")

		// Extract org and track it (but don't filter here)
		org := extractOrgFromRepo(repo)
		if org != "" {
			app.mu.Lock()
			if !app.seenOrgs[org] {
				slog.Info("[ORG] Discovered new organization", "org", org)
			}
			app.seenOrgs[org] = true
			app.mu.Unlock()
		}

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

	// Only log summary, not individual PRs
	slog.Info("[GITHUB] GitHub PR summary", "incoming", len(incoming), "outgoing", len(outgoing))

	// Fetch Turn API data
	// Always synchronous now for simplicity - Turn API calls are fast with caching
	app.fetchTurnDataSync(ctx, allIssues, user, &incoming, &outgoing)

	return incoming, outgoing, nil
}

// fetchTurnDataSync fetches Turn API data synchronously and updates PRs directly.
func (app *App) fetchTurnDataSync(ctx context.Context, issues []*github.Issue, user string, incoming *[]PR, outgoing *[]PR) {
	turnStart := time.Now()

	// Create a channel for results
	results := make(chan prResult, len(issues))

	// Use a WaitGroup to track goroutines
	var wg sync.WaitGroup

	// Create semaphore to limit concurrent Turn API calls
	sem := make(chan struct{}, maxConcurrentTurnAPICalls)

	// Process PRs in parallel with concurrency limit
	for _, issue := range issues {
		if !issue.IsPullRequest() {
			continue
		}

		wg.Add(1)
		go func(issue *github.Issue) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

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
	actualAPICalls := 0
	cacheHits := 0

	for result := range results {
		if result.err == nil && result.turnData != nil && result.turnData.PRState.UnblockAction != nil {
			turnSuccesses++
			if result.wasFromCache {
				cacheHits++
			} else {
				actualAPICalls++
			}

			// Check if user needs to review and get action reason
			needsReview := false
			actionReason := ""
			if action, exists := result.turnData.PRState.UnblockAction[user]; exists {
				needsReview = true
				actionReason = action.Reason
				// Only log fresh API calls
				if !result.wasFromCache {
					slog.Debug("[TURN] UnblockAction", "url", result.url, "reason", action.Reason, "kind", action.Kind)
				}
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

	// Only log if there were actual API calls or failures
	if actualAPICalls > 0 || turnFailures > 0 {
		slog.Info("[TURN] API queries completed",
			"duration", time.Since(turnStart),
			"api_calls", actualAPICalls,
			"cache_hits", cacheHits,
			"failures", turnFailures,
			"total", turnSuccesses+turnFailures)
	} else if cacheHits > 0 {
		slog.Debug("[TURN] All data served from cache",
			"cache_hits", cacheHits,
			"duration", time.Since(turnStart))
	}
}
