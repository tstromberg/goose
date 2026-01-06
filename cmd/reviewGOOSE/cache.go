package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/codeGROOVE-dev/goose/pkg/safebrowse"
	"github.com/codeGROOVE-dev/retry"
	"github.com/codeGROOVE-dev/turnclient/pkg/turn"
)

type cacheEntry struct {
	Data      *turn.CheckResponse `json:"data"`
	CachedAt  time.Time           `json:"cached_at"`
	UpdatedAt time.Time           `json:"updated_at"`
}

// checkCache checks the cache for a PR and returns the cached data if valid.
// Returns (data, hit, running) where running indicates incomplete tests.
func (app *App) checkCache(path, url string, updatedAt time.Time) (data *turn.CheckResponse, hit, running bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Debug("[CACHE] Cache file read error", "url", url, "error", err)
		}
		return nil, false, false
	}

	var e cacheEntry
	if err := json.Unmarshal(b, &e); err != nil {
		slog.Warn("Failed to unmarshal cache data", "url", url, "error", err)
		if err := os.Remove(path); err != nil {
			slog.Debug("Failed to remove corrupted cache file", "error", err)
		}
		return nil, false, false
	}
	if e.Data == nil {
		slog.Warn("Cache entry missing data", "url", url)
		if err := os.Remove(path); err != nil {
			slog.Debug("Failed to remove corrupted cache file", "error", err)
		}
		return nil, false, false
	}

	// Determine TTL based on test state - use shorter TTL for incomplete tests
	state := e.Data.PullRequest.TestState
	incomplete := state == "running" || state == "queued" || state == "pending"
	ttl := cacheTTL
	if incomplete {
		ttl = runningTestsCacheTTL
	}

	// Check if cache is expired or PR updated
	if time.Since(e.CachedAt) >= ttl || !e.UpdatedAt.Equal(updatedAt) {
		if !e.UpdatedAt.Equal(updatedAt) {
			slog.Debug("[CACHE] Cache miss - PR updated",
				"url", url,
				"cached_pr_time", e.UpdatedAt.Format(time.RFC3339),
				"current_pr_time", updatedAt.Format(time.RFC3339))
		} else {
			slog.Debug("[CACHE] Cache miss - TTL expired",
				"url", url,
				"cached_at", e.CachedAt.Format(time.RFC3339),
				"cache_age", time.Since(e.CachedAt).Round(time.Second),
				"ttl", ttl,
				"test_state", state)
		}
		return nil, false, incomplete
	}

	// Invalidate cache for incomplete tests on recently-updated PRs to catch completion
	// Skip this for PRs not updated in over an hour - their pending tests are likely stale
	age := time.Since(e.CachedAt)
	if incomplete && age < runningTestsCacheBypass && time.Since(updatedAt) < time.Hour {
		slog.Debug("[CACHE] Cache invalidated - tests incomplete and cache entry is fresh",
			"url", url,
			"test_state", state,
			"cache_age", age.Round(time.Minute),
			"cached_at", e.CachedAt.Format(time.RFC3339))
		return nil, false, true
	}

	slog.Debug("[CACHE] Cache hit",
		"url", url,
		"cached_at", e.CachedAt.Format(time.RFC3339),
		"cache_age", time.Since(e.CachedAt).Round(time.Second),
		"pr_updated_at", e.UpdatedAt.Format(time.RFC3339))
	if app.healthMonitor != nil {
		app.healthMonitor.recordCacheAccess(true)
	}
	return e.Data, true, false
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

	// Create cache key from URL and updated timestamp
	key := fmt.Sprintf("%s-%s", url, updatedAt.Format(time.RFC3339))
	h := sha256.Sum256([]byte(key))
	path := filepath.Join(app.cacheDir, hex.EncodeToString(h[:])[:16]+".json")

	slog.Debug("[CACHE] Checking cache",
		"url", url,
		"updated_at", updatedAt.Format(time.RFC3339),
		"cache_key", key,
		"cache_file", filepath.Base(path))

	// Check cache unless --no-cache flag is set
	var running bool
	if !app.noCache {
		data, hit, r := app.checkCache(path, url, updatedAt)
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
		e := cacheEntry{Data: data, CachedAt: time.Now(), UpdatedAt: updatedAt}
		b, err := json.Marshal(e)
		if err != nil {
			slog.Error("Failed to marshal cache data", "url", url, "error", err)
		} else if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			slog.Error("Failed to create cache directory", "error", err)
		} else if err := os.WriteFile(path, b, 0o600); err != nil {
			slog.Error("Failed to write cache file", "error", err)
		} else {
			slog.Debug("[CACHE] Saved to cache",
				"url", url,
				"cache_file", filepath.Base(path),
				"test_state", data.PullRequest.TestState)
		}
	}

	return data, false, nil
}

// cleanupOldCache removes cache files older than the cleanup interval (15 days).
func (app *App) cleanupOldCache() {
	entries, err := os.ReadDir(app.cacheDir)
	if err != nil {
		slog.Error("Failed to read cache directory for cleanup", "error", err)
		return
	}

	var cleaned, errs int
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			slog.Error("Failed to get file info for cache entry", "entry", e.Name(), "error", err)
			errs++
			continue
		}
		if time.Since(info.ModTime()) > cacheCleanupInterval {
			p := filepath.Join(app.cacheDir, e.Name())
			if err := os.Remove(p); err != nil {
				slog.Error("Failed to remove old cache file", "file", p, "error", err)
				errs++
			} else {
				cleaned++
			}
		}
	}

	if cleaned > 0 || errs > 0 {
		slog.Info("Cache cleanup completed", "removed", cleaned, "errors", errs)
	}
}
