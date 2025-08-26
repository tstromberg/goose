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
		log.Println("Using GitHub token from GITHUB_TOKEN environment variable")
		return token, nil
	}
	// Try to find gh in PATH first
	ghPath, err := exec.LookPath("gh")
	if err == nil {
		log.Printf("Found gh in PATH at: %s", ghPath)
		// Resolve any symlinks to get the real path
		if realPath, err := filepath.EvalSymlinks(ghPath); err == nil {
			ghPath = realPath
			log.Printf("Resolved to: %s", ghPath)
		}
	} else {
		// Fall back to checking common installation paths
		log.Print("gh not found in PATH, checking common locations...")
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
				log.Printf("Found gh at common location: %s", path)
				ghPath = path
				break
			}
		}
	}

	if ghPath == "" {
		return "", errors.New("gh CLI not found in PATH or common locations, and GITHUB_TOKEN not set")
	}

	log.Printf("Executing command: %s auth token", ghPath)
	cmd := exec.CommandContext(ctx, ghPath, "auth", "token")
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("gh command failed: %v", err)
		return "", fmt.Errorf("exec 'gh auth token': %w", err)
	}
	token := strings.TrimSpace(string(output))
	if err := validateGitHubToken(token); err != nil {
		return "", fmt.Errorf("invalid token from gh CLI: %w", err)
	}
	log.Println("Successfully obtained GitHub token from gh CLI")
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

// fetchPRsInternal is the implementation for PR fetching.
// It returns GitHub data immediately and starts Turn API queries in the background (when waitForTurn=false),
// or waits for Turn data to complete (when waitForTurn=true).
func (app *App) fetchPRsInternal(ctx context.Context, waitForTurn bool) (incoming []PR, outgoing []PR, _ error) {
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
		log.Printf("[GITHUB] Searching for PRs with query: %s", query)

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
		log.Printf("[GITHUB] Searching for PRs with query: %s", query)

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
			log.Printf("[GITHUB] Query failed: %s - %v", result.query, result.err)
			queryErrors = append(queryErrors, result.err)
			// Continue processing other query results even if one fails
			continue
		}
		log.Printf("[GITHUB] Query completed: %s - found %d PRs", result.query, len(result.issues))

		// Deduplicate PRs based on URL
		for _, issue := range result.issues {
			url := issue.GetHTMLURL()
			if !seenURLs[url] {
				seenURLs[url] = true
				allIssues = append(allIssues, issue)
			}
		}
	}
	log.Printf("[GITHUB] Both searches completed in %v, found %d unique PRs", time.Since(searchStart), len(allIssues))

	// If both queries failed, return an error
	if len(queryErrors) == 2 {
		return nil, nil, fmt.Errorf("all GitHub queries failed: %v", queryErrors)
	}

	// Limit PRs for performance
	if len(allIssues) > maxPRsToProcess {
		log.Printf("Limiting to %d PRs for performance (total: %d)", maxPRsToProcess, len(allIssues))
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
				log.Printf("[ORG] Discovered new organization: %s", org)
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
	log.Printf("[GITHUB] Found %d incoming, %d outgoing PRs from GitHub", len(incoming), len(outgoing))

	// Fetch Turn API data
	if waitForTurn {
		// Synchronous - wait for Turn data
		// Fetch Turn API data synchronously before building menu
		app.fetchTurnDataSync(ctx, allIssues, user, &incoming, &outgoing)
	} else {
		// Asynchronous - start in background
		app.mu.Lock()
		app.loadingTurnData = true
		app.pendingTurnResults = make([]TurnResult, 0) // Reset buffer
		app.mu.Unlock()
		go app.fetchTurnDataAsync(ctx, allIssues, user)
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

	for result := range results {
		if result.err == nil && result.turnData != nil && result.turnData.PRState.UnblockAction != nil {
			turnSuccesses++

			// Check if user needs to review and get action reason
			needsReview := false
			actionReason := ""
			if action, exists := result.turnData.PRState.UnblockAction[user]; exists {
				needsReview = true
				actionReason = action.Reason
				// Only log fresh API calls
				if !result.wasFromCache {
					log.Printf("[TURN] UnblockAction for %s: Reason=%q, Kind=%q", result.url, action.Reason, action.Kind)
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

	log.Printf("[TURN] Turn API queries completed in %v (%d/%d succeeded)",
		time.Since(turnStart), turnSuccesses, turnSuccesses+turnFailures)
}

// fetchTurnDataAsync fetches Turn API data in the background and updates PRs as results arrive.
func (app *App) fetchTurnDataAsync(ctx context.Context, issues []*github.Issue, user string) {
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
				// Only log blocked PRs from fresh API calls
				if !result.wasFromCache {
					log.Printf("[TURN] UnblockAction for %s: Reason=%q, Kind=%q", result.url, action.Reason, action.Kind)
				}
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
			// Only log fresh API calls (not cached)
			if !result.wasFromCache {
				log.Printf("[TURN] Fresh API data for %s (needsReview=%v)", result.url, needsReview)
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

	// Only log if we have fresh results
	if freshResults > 0 {
		log.Printf("[TURN] Applying %d buffered Turn results (%d from cache, %d fresh)", len(pendingResults), cacheHits, freshResults)
	}

	// Track how many PRs actually changed
	var actualChanges int
	for _, result := range pendingResults {
		_, changed := app.updatePRData(result.URL, result.NeedsReview, result.IsOwner, result.ActionReason)
		if changed {
			actualChanges++
		}
	}

	// Only check for newly blocked PRs if there were actual changes
	// checkForNewlyBlockedPRs will handle UI updates internally if needed
	if actualChanges > 0 {
		app.checkForNewlyBlockedPRs(ctx)
		// UI updates are handled inside checkForNewlyBlockedPRs
	} else {
		// No changes, but still update tray title in case of initial load
		app.setTrayTitle()
	}
}
