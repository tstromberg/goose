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
// Returns (cachedData, cacheHit, hasRunningTests).
func (app *App) checkCache(cacheFile, url string, updatedAt time.Time) (cachedData *turn.CheckResponse, cacheHit bool, hasRunningTests bool) {
	fileData, err := os.ReadFile(cacheFile)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Debug("[CACHE] Cache file read error", "url", url, "error", err)
		}
		return nil, false, false
	}

	var entry cacheEntry
	if err := json.Unmarshal(fileData, &entry); err != nil {
		slog.Warn("Failed to unmarshal cache data", "url", url, "error", err)
		// Remove corrupted cache file
		if err := os.Remove(cacheFile); err != nil {
			slog.Error("Failed to remove corrupted cache file", "error", err)
		}
		return nil, false, false
	}

	// Determine TTL based on test state - use shorter TTL for incomplete tests
	testState := entry.Data.PullRequest.TestState
	isTestIncomplete := testState == "running" || testState == "queued" || testState == "pending"
	ttl := cacheTTL
	if isTestIncomplete {
		ttl = runningTestsCacheTTL
	}

	// Check if cache is expired or PR updated
	if time.Since(entry.CachedAt) >= ttl || !entry.UpdatedAt.Equal(updatedAt) {
		// Log why cache was invalid
		if !entry.UpdatedAt.Equal(updatedAt) {
			slog.Debug("[CACHE] Cache miss - PR updated",
				"url", url,
				"cached_pr_time", entry.UpdatedAt.Format(time.RFC3339),
				"current_pr_time", updatedAt.Format(time.RFC3339))
		} else {
			slog.Debug("[CACHE] Cache miss - TTL expired",
				"url", url,
				"cached_at", entry.CachedAt.Format(time.RFC3339),
				"cache_age", time.Since(entry.CachedAt).Round(time.Second),
				"ttl", ttl,
				"test_state", testState)
		}
		return nil, false, isTestIncomplete
	}

	// Check for incomplete tests that should invalidate cache and trigger Turn API cache bypass
	cacheAge := time.Since(entry.CachedAt)
	if entry.Data != nil && isTestIncomplete && cacheAge < runningTestsCacheBypass {
		slog.Debug("[CACHE] Cache invalidated - tests incomplete and cache entry is fresh",
			"url", url,
			"test_state", testState,
			"cache_age", cacheAge.Round(time.Minute),
			"cached_at", entry.CachedAt.Format(time.RFC3339))
		return nil, false, true
	}

	// Cache hit
	slog.Debug("[CACHE] Cache hit",
		"url", url,
		"cached_at", entry.CachedAt.Format(time.RFC3339),
		"cache_age", time.Since(entry.CachedAt).Round(time.Second),
		"pr_updated_at", entry.UpdatedAt.Format(time.RFC3339))
	if app.healthMonitor != nil {
		app.healthMonitor.recordCacheAccess(true)
	}
	return entry.Data, true, false
}

// turnData fetches Turn API data with caching.
func (app *App) turnData(ctx context.Context, url string, updatedAt time.Time) (*turn.CheckResponse, bool, error) {
	// If Turn API is disabled, return nil without error
	if app.turnClient == nil {
		slog.Debug("[TURN] Turn API disabled, skipping", "url", url)
		return nil, false, nil
	}

	hasRunningTests := false
	// Validate URL before processing
	if err := safebrowse.ValidateURL(url); err != nil {
		return nil, false, fmt.Errorf("invalid URL: %w", err)
	}

	// Create cache key from URL and updated timestamp
	key := fmt.Sprintf("%s-%s", url, updatedAt.Format(time.RFC3339))
	hash := sha256.Sum256([]byte(key))
	cacheFile := filepath.Join(app.cacheDir, hex.EncodeToString(hash[:])[:16]+".json")

	// Log the cache key details
	slog.Debug("[CACHE] Checking cache",
		"url", url,
		"updated_at", updatedAt.Format(time.RFC3339),
		"cache_key", key,
		"cache_file", filepath.Base(cacheFile))

	// Skip cache if --no-cache flag is set
	if !app.noCache {
		if cachedData, cacheHit, runningTests := app.checkCache(cacheFile, url, updatedAt); cacheHit {
			return cachedData, true, nil
		} else if runningTests {
			hasRunningTests = true
		}
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
		// Create timeout context for Turn API call
		turnCtx, cancel := context.WithTimeout(ctx, turnAPITimeout)
		defer cancel()

		// For PRs with running tests, send current time to bypass Turn server cache
		timestampToSend := updatedAt
		if hasRunningTests {
			timestampToSend = time.Now()
			slog.Debug("[TURN] Using current timestamp for PR with running tests to bypass Turn server cache",
				"url", url,
				"pr_updated_at", updatedAt.Format(time.RFC3339),
				"timestamp_sent", timestampToSend.Format(time.RFC3339))
		}

		var err error
		slog.Debug("[TURN] Making API call",
			"url", url,
			"user", app.currentUser.GetLogin(),
			"pr_updated_at", timestampToSend.Format(time.RFC3339))
		data, err = app.turnClient.Check(turnCtx, url, app.currentUser.GetLogin(), timestampToSend)
		if err != nil {
			slog.Warn("Turn API error (will retry)", "error", err)
			return err
		}
		slog.Debug("[TURN] API call successful", "url", url)
		return nil
	},
		retry.Attempts(maxRetries),
		retry.DelayType(retry.CombineDelay(retry.BackOffDelay, retry.RandomDelay)), // Add jitter for better backoff distribution
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

	// Log Turn API response for debugging
	if data != nil {
		slog.Info("[TURN] API response details",
			"url", url,
			"test_state", data.PullRequest.TestState,
			"state", data.PullRequest.State,
			"merged", data.PullRequest.Merged,
			"pending_checks", len(data.PullRequest.CheckSummary.Pending))
	}

	// Save to cache (don't fail if caching fails) - skip if --no-cache is set
	// Cache PRs with incomplete tests using short TTL to catch completion quickly
	if !app.noCache && data != nil {
		testState := data.PullRequest.TestState
		isTestIncomplete := testState == "running" || testState == "queued" || testState == "pending"

		entry := cacheEntry{
			Data:      data,
			CachedAt:  time.Now(),
			UpdatedAt: updatedAt,
		}
		if cacheData, err := json.Marshal(entry); err != nil {
			slog.Error("Failed to marshal cache data", "url", url, "error", err)
		} else {
			// Ensure cache directory exists with secure permissions
			if err := os.MkdirAll(filepath.Dir(cacheFile), 0o700); err != nil {
				slog.Error("Failed to create cache directory", "error", err)
			} else if err := os.WriteFile(cacheFile, cacheData, 0o600); err != nil {
				slog.Error("Failed to write cache file", "error", err)
			} else {
				ttl := cacheTTL
				if isTestIncomplete {
					ttl = runningTestsCacheTTL
				}
				slog.Debug("[CACHE] Saved to cache",
					"url", url,
					"cached_at", entry.CachedAt.Format(time.RFC3339),
					"pr_updated_at", entry.UpdatedAt.Format(time.RFC3339),
					"ttl", ttl,
					"test_state", testState,
					"cache_file", filepath.Base(cacheFile))
			}
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

	var cleanupCount, errorCount int
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			slog.Error("Failed to get file info for cache entry", "entry", entry.Name(), "error", err)
			errorCount++
			continue
		}

		// Remove cache files older than cleanup interval (15 days)
		if time.Since(info.ModTime()) > cacheCleanupInterval {
			filePath := filepath.Join(app.cacheDir, entry.Name())
			if err := os.Remove(filePath); err != nil {
				slog.Error("Failed to remove old cache file", "file", filePath, "error", err)
				errorCount++
			} else {
				cleanupCount++
			}
		}
	}

	if cleanupCount > 0 || errorCount > 0 {
		slog.Info("Cache cleanup completed", "removed", cleanupCount, "errors", errorCount)
	}
}
