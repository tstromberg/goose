package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/codeGROOVE-dev/goose/pkg/prcache"
	"github.com/codeGROOVE-dev/goose/pkg/safebrowse"
	"github.com/codeGROOVE-dev/retry"
	"github.com/codeGROOVE-dev/turnclient/pkg/turn"
)

// checkCache checks the cache for a PR and returns the cached data if valid.
// Returns (data, hit, running) where running indicates incomplete tests.
func (app *App) checkCache(cacheManager *prcache.Manager, path, url string, updatedAt time.Time) (data *turn.CheckResponse, hit, running bool) {
	// State check function for incomplete tests
	stateCheck := func(d any) bool {
		if m, ok := d.(map[string]any); ok {
			if pr, ok := m["pull_request"].(map[string]any); ok {
				if state, ok := pr["test_state"].(string); ok {
					incomplete := state == "running" || state == "queued" || state == "pending"
					// Only bypass for recently updated PRs
					if incomplete && time.Since(updatedAt) < time.Hour {
						return true
					}
				}
			}
		}
		return false
	}

	// Determine TTL based on whether we expect tests to be running
	ttl := cacheTTL
	bypassTTL := runningTestsCacheBypass

	result, err := cacheManager.Get(path, updatedAt, ttl, bypassTTL, stateCheck)
	if err != nil {
		slog.Debug("[CACHE] Cache error", "url", url, "error", err)
		return nil, false, false
	}

	if !result.Hit {
		return nil, false, result.ShouldBypass
	}

	// Extract turn.CheckResponse from cached data
	if result.Entry == nil || result.Entry.Data == nil {
		return nil, false, false
	}

	// Convert map back to CheckResponse
	dataBytes, err := json.Marshal(result.Entry.Data)
	if err != nil {
		slog.Warn("Failed to marshal cached data", "url", url, "error", err)
		return nil, false, false
	}

	var response turn.CheckResponse
	if err := json.Unmarshal(dataBytes, &response); err != nil {
		slog.Warn("Failed to unmarshal cached data", "url", url, "error", err)
		return nil, false, false
	}

	slog.Debug("[CACHE] Cache hit",
		"url", url,
		"cached_at", result.Entry.CachedAt.Format(time.RFC3339),
		"cache_age", time.Since(result.Entry.CachedAt).Round(time.Second),
		"pr_updated_at", result.Entry.UpdatedAt.Format(time.RFC3339))

	if app.healthMonitor != nil {
		app.healthMonitor.recordCacheAccess(true)
	}

	return &response, true, false
}

// turnData fetches Turn API data with caching.
func (app *App) turnData(ctx context.Context, url string, updatedAt time.Time) (*turn.CheckResponse, bool, error) {
	if app.turnClient == nil {
		slog.Debug("[TURN] Turn API disabled, skipping", "url", url)
		return nil, false, nil
	}

	if err := safebrowse.ValidateURL(url); err != nil {
		return nil, false, fmt.Errorf("invalid URL: %w", err)
	}

	// Create cache manager and path
	cacheManager := prcache.NewManager(app.cacheDir)
	cacheKey := prcache.CacheKey(url, updatedAt)
	path := cacheManager.CachePath(cacheKey)

	slog.Debug("[CACHE] Checking cache",
		"url", url,
		"updated_at", updatedAt.Format(time.RFC3339),
		"cache_key", cacheKey)

	// Check cache unless --no-cache flag is set
	var running bool
	if !app.noCache {
		data, hit, r := app.checkCache(cacheManager, path, url, updatedAt)
		if hit {
			return data, true, nil
		}
		running = r
	}

	// Cache miss, fetch from API
	if app.noCache {
		slog.Debug("Cache bypassed (--no-cache), fetching from Turn API", "url", url)
	} else {
		slog.Info("[CACHE] Cache miss, fetching from Turn API",
			"url", url,
			"pr_updated_at", updatedAt.Format(time.RFC3339))
		if app.healthMonitor != nil {
			app.healthMonitor.recordCacheAccess(false)
		}
	}

	// Use exponential backoff with jitter for Turn API calls
	var data *turn.CheckResponse
	err := retry.Do(func() error {
		tctx, cancel := context.WithTimeout(ctx, turnAPITimeout)
		defer cancel()

		// For PRs with running tests, send current time to bypass Turn server cache
		ts := updatedAt
		if running {
			ts = time.Now()
			slog.Debug("[TURN] Using current timestamp for PR with running tests to bypass Turn server cache",
				"url", url,
				"pr_updated_at", updatedAt.Format(time.RFC3339),
				"timestamp_sent", ts.Format(time.RFC3339))
		}

		slog.Debug("[TURN] Making API call",
			"url", url,
			"user", app.currentUser.GetLogin(),
			"pr_updated_at", ts.Format(time.RFC3339))
		var err error
		data, err = app.turnClient.Check(tctx, url, app.currentUser.GetLogin(), ts)
		if err != nil {
			slog.Warn("Turn API error (will retry)", "error", err)
			return err
		}
		slog.Debug("[TURN] API call successful", "url", url)
		return nil
	},
		retry.Attempts(maxRetries),
		retry.DelayType(retry.CombineDelay(retry.BackOffDelay, retry.RandomDelay)),
		retry.MaxDelay(maxRetryDelay),
		retry.OnRetry(func(n uint, err error) {
			slog.Warn("[TURN] API retry", "attempt", n+1, "maxRetries", maxRetries, "url", url, "error", err)
		}),
		retry.Context(ctx),
	)
	if err != nil {
		slog.Error("Turn API error after retries (will use PR without metadata)", "maxRetries", maxRetries, "error", err)
		if app.healthMonitor != nil {
			app.healthMonitor.recordAPICall(false)
		}
		return nil, false, err
	}

	if app.healthMonitor != nil {
		app.healthMonitor.recordAPICall(true)
	}

	if data != nil {
		slog.Info("[TURN] API response details",
			"url", url,
			"test_state", data.PullRequest.TestState,
			"state", data.PullRequest.State,
			"merged", data.PullRequest.Merged,
			"pending_checks", len(data.PullRequest.CheckSummary.Pending))
	}

	// Save to cache (don't fail if caching fails)
	if !app.noCache && data != nil {
		if err := cacheManager.Put(path, data, updatedAt); err != nil {
			slog.Error("Failed to save cache", "url", url, "error", err)
		} else {
			slog.Debug("[CACHE] Saved to cache",
				"url", url,
				"test_state", data.PullRequest.TestState)
		}
	}

	return data, false, nil
}

// cleanupOldCache removes cache files older than the cleanup interval (15 days).
func (app *App) cleanupOldCache() {
	cacheManager := prcache.NewManager(app.cacheDir)
	cleaned, errs := cacheManager.CleanupOldFiles(cacheCleanupInterval)

	if cleaned > 0 || errs > 0 {
		slog.Info("Cache cleanup completed", "removed", cleaned, "errors", errs)
	}
}
