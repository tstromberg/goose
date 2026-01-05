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
	"github.com/codeGROOVE-dev/turnclient/pkg/turn"
	"github.com/google/go-github/v57/github"
	"golang.org/x/oauth2"
)

// extractOrgFromRepo extracts the organization name from a repository path like "org/repo".
func extractOrgFromRepo(repo string) string {
	idx := strings.Index(repo, "/")
	if idx > 0 {
		return repo[:idx]
	}
	if idx == 0 {
		return "" // Invalid: starts with "/"
	}
	return repo // No slash: return as-is (single segment or empty)
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

	// Check for custom turn server hostname (for self-hosting)
	// Set TURNSERVER=disabled to run without Turn API
	turnServer := os.Getenv("TURNSERVER")
	if turnServer == "disabled" {
		slog.Info("Turn API disabled via TURNSERVER=disabled")
	} else {
		var turnClient *turn.Client
		if turnServer != "" {
			slog.Info("Using custom turn server", "hostname", turnServer)
			turnClient, err = turn.NewClient("https://" + turnServer)
		} else {
			turnClient, err = turn.NewDefaultClient()
		}
		if err != nil {
			return fmt.Errorf("create turn client: %w", err)
		}
		turnClient.SetAuthToken(token)
		app.turnClient = turnClient
	}

	// Initialize sprinkler monitor for real-time events
	// Check for custom sprinkler server hostname (for self-hosting)
	// Set SPRINKLER=disabled to run without real-time events
	sprinklerServer := os.Getenv("SPRINKLER")
	if sprinklerServer == "disabled" {
		slog.Info("Sprinkler disabled via SPRINKLER=disabled")
	} else {
		if sprinklerServer != "" {
			slog.Info("Using custom sprinkler server", "hostname", sprinklerServer)
		}
		app.sprinklerMonitor = newSprinklerMonitor(app, token, sprinklerServer)
	}

	return nil
}

// initSprinklerOrgs fetches the user's organizations and starts sprinkler monitoring.
func (app *App) initSprinklerOrgs(ctx context.Context) error {
	if app.client == nil {
		return errors.New("github client not initialized")
	}
	// If sprinkler is disabled, skip silently
	if app.sprinklerMonitor == nil {
		slog.Debug("[SPRINKLER] Sprinkler disabled, skipping org initialization")
		return nil
	}

	// Get current user
	user := ""
	if app.currentUser != nil {
		user = app.currentUser.GetLogin()
	}
	if app.targetUser != "" {
		user = app.targetUser
	}
	if user == "" {
		return errors.New("no user configured")
	}

	slog.Info("[SPRINKLER] Fetching user's organizations", "user", user)

	// Fetch all orgs the user is a member of with retry
	opts := &github.ListOptions{PerPage: 100}
	var orgs []string

	for {
		var page []*github.Organization
		var resp *github.Response

		err := retry.Do(func() error {
			apiCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			var err error
			page, resp, err = app.client.Organizations.List(apiCtx, user, opts)
			if err != nil {
				slog.Debug("[SPRINKLER] Organizations.List failed (will retry)", "error", err, "page", opts.Page)
				return err
			}
			return nil
		},
			retry.Attempts(maxRetries),
			retry.DelayType(retry.CombineDelay(retry.BackOffDelay, retry.RandomDelay)),
			retry.MaxDelay(maxRetryDelay),
			retry.OnRetry(func(n uint, err error) {
				slog.Warn("[SPRINKLER] Organizations.List retry", "attempt", n+1, "error", err, "page", opts.Page)
			}),
			retry.Context(ctx),
		)
		if err != nil {
			// Gracefully degrade - continue without sprinkler if org fetch fails
			slog.Warn("[SPRINKLER] Failed to fetch organizations after retries, sprinkler will not start",
				"error", err,
				"maxRetries", maxRetries)
			return nil // Return nil to avoid blocking startup
		}

		for _, o := range page {
			if o.Login != nil {
				orgs = append(orgs, *o.Login)
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	slog.Info("[SPRINKLER] Discovered user organizations",
		"user", user,
		"orgs", orgs,
		"count", len(orgs))

	// Update sprinkler with all orgs at once
	if len(orgs) > 0 {
		app.sprinklerMonitor.updateOrgs(orgs)
		if err := app.sprinklerMonitor.start(ctx); err != nil {
			return fmt.Errorf("start sprinkler: %w", err)
		}
	}

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

	// Use circuit breaker if available
	if app.githubCircuit != nil {
		err := app.githubCircuit.call(func() error {
			return app.executeGitHubQueryInternal(ctx, query, opts, &result, &resp)
		})
		if err != nil {
			return nil, err
		}
		return result, nil
	}

	// Fallback to direct execution
	err := app.executeGitHubQueryInternal(ctx, query, opts, &result, &resp)
	return result, err
}

func (app *App) executeGitHubQueryInternal(
	ctx context.Context,
	query string,
	opts *github.SearchOptions,
	result **github.IssuesSearchResult,
	resp **github.Response,
) error {
	return retry.Do(func() error {
		// Create timeout context for GitHub API call
		githubCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		var retryErr error
		*result, *resp, retryErr = app.client.Search.Issues(githubCtx, query, opts)
		if retryErr != nil {
			// Enhanced error handling with specific cases
			if *resp != nil {
				const (
					httpStatusUnauthorized  = 401
					httpStatusForbidden     = 403
					httpStatusUnprocessable = 422
				)
				switch (*resp).StatusCode {
				case httpStatusForbidden:
					if (*resp).Header.Get("X-Ratelimit-Remaining") == "0" {
						resetTime := (*resp).Header.Get("X-Ratelimit-Reset")
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
					slog.Warn("GitHub API error (will retry)", "statusCode", (*resp).StatusCode, "error", retryErr)
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
	type qResult struct {
		err    error
		query  string
		issues []*github.Issue
	}

	results := make(chan qResult, 2)

	// Query 1: PRs involving the user
	go func() {
		q := fmt.Sprintf("is:open is:pr involves:%s archived:false", user)
		slog.Debug("[GITHUB] Searching for PRs", "query", q)

		res, err := app.executeGitHubQuery(ctx, q, opts)
		if err != nil {
			results <- qResult{err: err, query: q}
		} else {
			results <- qResult{issues: res.Issues, query: q}
		}
	}()

	// Query 2: PRs in user-owned repos with no reviewers
	go func() {
		q := fmt.Sprintf("is:open is:pr user:%s review:none archived:false", user)
		slog.Debug("[GITHUB] Searching for PRs", "query", q)

		res, err := app.executeGitHubQuery(ctx, q, opts)
		if err != nil {
			results <- qResult{err: err, query: q}
		} else {
			results <- qResult{issues: res.Issues, query: q}
		}
	}()

	// Collect results from both queries
	var issues []*github.Issue
	seen := make(map[string]bool)
	var errs []error

	for range 2 {
		r := <-results
		if r.err != nil {
			slog.Error("[GITHUB] Query failed", "query", r.query, "error", r.err)
			errs = append(errs, r.err)
			continue
		}
		slog.Debug("[GITHUB] Query completed", "query", r.query, "prCount", len(r.issues))

		// Deduplicate PRs based on URL
		for _, issue := range r.issues {
			url := issue.GetHTMLURL()
			if !seen[url] {
				seen[url] = true
				issues = append(issues, issue)
			}
		}
	}
	slog.Info("[GITHUB] Both searches completed", "duration", time.Since(searchStart), "uniquePRs", len(issues))

	// If both queries failed, return an error
	if len(errs) == 2 {
		return nil, nil, fmt.Errorf("all GitHub queries failed: %v", errs)
	}

	// Limit PRs for performance
	if len(issues) > maxPRsToProcess {
		slog.Info("Limiting PRs for performance", "limit", maxPRsToProcess, "total", len(issues))
		issues = issues[:maxPRsToProcess]
	}

	// Process GitHub results immediately
	for _, issue := range issues {
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
			Author:     issue.GetUser().GetLogin(),
			Number:     issue.GetNumber(),
			CreatedAt:  issue.GetCreatedAt().Time,
			UpdatedAt:  issue.GetUpdatedAt().Time,
			IsDraft:    issue.GetDraft(),
		}

		// Categorize as incoming or outgoing
		// When viewing another user's PRs, we're looking at it from their perspective
		if issue.GetUser().GetLogin() == user {
			slog.Info("[GITHUB] Found outgoing PR", "repo", repo, "number", pr.Number, "author", pr.Author, "url", pr.URL)
			outgoing = append(outgoing, pr)
		} else {
			slog.Info("[GITHUB] Found incoming PR", "repo", repo, "number", pr.Number, "author", pr.Author, "url", pr.URL)
			incoming = append(incoming, pr)
		}
	}

	// Only log summary, not individual PRs
	slog.Info("[GITHUB] GitHub PR summary", "incoming", len(incoming), "outgoing", len(outgoing))

	// Fetch Turn API data
	// Always synchronous now for simplicity - Turn API calls are fast with caching
	app.fetchTurnDataSync(ctx, issues, user, &incoming, &outgoing)

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

		wg.Go(func() {
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
		})
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
		if result.err == nil && result.turnData != nil && result.turnData.Analysis.NextAction != nil {
			turnSuccesses++
			if result.wasFromCache {
				cacheHits++
			} else {
				actualAPICalls++
			}

			// Check if user needs to review and get action reason
			needsReview := false
			isBlocked := false
			actionReason := ""
			actionKind := ""
			testState := result.turnData.PullRequest.TestState
			workflowState := result.turnData.Analysis.WorkflowState
			if action, exists := result.turnData.Analysis.NextAction[user]; exists {
				needsReview = true
				isBlocked = action.Critical // Only critical actions are blocking
				actionReason = action.Reason
				actionKind = string(action.Kind)
				// Only log fresh API calls
				if !result.wasFromCache {
					slog.Debug("[TURN] NextAction", "url", result.url, "reason", action.Reason, "kind", action.Kind, "critical", action.Critical)
				}
			}

			// Update the PR in the slices directly
			authorBot := result.turnData.PullRequest.AuthorBot
			lastActivityAt := result.turnData.Analysis.LastActivity.Timestamp
			if result.isOwner {
				for i := range *outgoing {
					if (*outgoing)[i].URL != result.url {
						continue
					}
					(*outgoing)[i].NeedsReview = needsReview
					(*outgoing)[i].IsBlocked = isBlocked
					(*outgoing)[i].ActionReason = actionReason
					(*outgoing)[i].ActionKind = actionKind
					(*outgoing)[i].TestState = testState
					(*outgoing)[i].WorkflowState = workflowState
					(*outgoing)[i].AuthorBot = authorBot
					(*outgoing)[i].LastActivityAt = lastActivityAt
					break
				}
			} else {
				for i := range *incoming {
					if (*incoming)[i].URL != result.url {
						continue
					}
					(*incoming)[i].NeedsReview = needsReview
					(*incoming)[i].IsBlocked = isBlocked
					(*incoming)[i].ActionReason = actionReason
					(*incoming)[i].ActionKind = actionKind
					(*incoming)[i].TestState = testState
					(*incoming)[i].WorkflowState = workflowState
					(*incoming)[i].AuthorBot = authorBot
					(*incoming)[i].LastActivityAt = lastActivityAt
					break
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
