// Package main - cache.go provides caching functionality for Turn API responses.
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

	"github.com/codeGROOVE-dev/retry"
	"github.com/codeGROOVE-dev/turnclient/pkg/turn"
)

type cacheEntry struct {
	Data      *turn.CheckResponse `json:"data"`
	CachedAt  time.Time           `json:"cached_at"`
	UpdatedAt time.Time           `json:"updated_at"`
}

// turnData fetches Turn API data with caching.
func (app *App) turnData(ctx context.Context, url string, updatedAt time.Time) (*turn.CheckResponse, bool, error) {
	prAge := time.Since(updatedAt)
	hasRunningTests := false
	// Validate URL before processing
	if err := validateURL(url); err != nil {
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
		// Try to read from cache (gracefully handle all cache errors)
		if data, readErr := os.ReadFile(cacheFile); readErr == nil {
			var entry cacheEntry
			if unmarshalErr := json.Unmarshal(data, &entry); unmarshalErr != nil {
				slog.Warn("Failed to unmarshal cache data", "url", url, "error", unmarshalErr)
				// Remove corrupted cache file
				if removeErr := os.Remove(cacheFile); removeErr != nil {
					slog.Error("Failed to remove corrupted cache file", "error", removeErr)
				}
			} else if time.Since(entry.CachedAt) < cacheTTL && entry.UpdatedAt.Equal(updatedAt) {
				// Check if cache is still valid (10 day TTL, but PR UpdatedAt is primary check)
				// But invalidate cache for PRs with running tests if they're fresh (< 90 minutes old)
				if entry.Data != nil && entry.Data.PullRequest.TestState == "running" && prAge < runningTestsCacheBypass {
					hasRunningTests = true
					slog.Debug("[CACHE] Cache invalidated - PR has running tests and is fresh",
						"url", url,
						"test_state", entry.Data.PullRequest.TestState,
						"pr_age", prAge.Round(time.Minute),
						"cached_at", entry.CachedAt.Format(time.RFC3339))
					// Don't return cached data - fall through to fetch fresh data with current time
				} else {
					slog.Debug("[CACHE] Cache hit",
						"url", url,
						"cached_at", entry.CachedAt.Format(time.RFC3339),
						"cache_age", time.Since(entry.CachedAt).Round(time.Second),
						"pr_updated_at", entry.UpdatedAt.Format(time.RFC3339))
					if app.healthMonitor != nil {
						app.healthMonitor.recordCacheAccess(true)
					}
					return entry.Data, true, nil
				}
			} else {
				// Log why cache was invalid
				if !entry.UpdatedAt.Equal(updatedAt) {
					slog.Debug("[CACHE] Cache miss - PR updated",
						"url", url,
						"cached_pr_time", entry.UpdatedAt.Format(time.RFC3339),
						"current_pr_time", updatedAt.Format(time.RFC3339))
				} else if time.Since(entry.CachedAt) >= cacheTTL {
					slog.Debug("[CACHE] Cache miss - TTL expired",
						"url", url,
						"cached_at", entry.CachedAt.Format(time.RFC3339),
						"cache_age", time.Since(entry.CachedAt).Round(time.Second),
						"ttl", cacheTTL)
				}
			}
		} else if !os.IsNotExist(readErr) {
			slog.Debug("[CACHE] Cache file read error", "url", url, "error", readErr)
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

		var retryErr error
		slog.Debug("[TURN] Making API call",
			"url", url,
			"user", app.currentUser.GetLogin(),
			"pr_updated_at", timestampToSend.Format(time.RFC3339))
		data, retryErr = app.turnClient.Check(turnCtx, url, app.currentUser.GetLogin(), timestampToSend)
		if retryErr != nil {
			slog.Warn("Turn API error (will retry)", "error", retryErr)
			return retryErr
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

	// Save to cache (don't fail if caching fails) - skip if --no-cache is set
	// Also skip caching if tests are running and PR is fresh (updated in last 90 minutes)
	if !app.noCache {
		shouldCache := true
		prAge := time.Since(updatedAt)

		// Don't cache PRs with running tests unless they're older than 90 minutes
		if data != nil && data.PullRequest.TestState == "running" && prAge < runningTestsCacheBypass {
			shouldCache = false
			slog.Debug("[CACHE] Skipping cache for PR with running tests",
				"url", url,
				"test_state", data.PullRequest.TestState,
				"pr_age", prAge.Round(time.Minute),
				"pending_checks", len(data.PullRequest.CheckSummary.PendingStatuses))
		}

		if shouldCache {
			entry := cacheEntry{
				Data:      data,
				CachedAt:  time.Now(),
				UpdatedAt: updatedAt,
			}
			if cacheData, marshalErr := json.Marshal(entry); marshalErr != nil {
				slog.Error("Failed to marshal cache data", "url", url, "error", marshalErr)
			} else {
				// Ensure cache directory exists with secure permissions
				if dirErr := os.MkdirAll(filepath.Dir(cacheFile), 0o700); dirErr != nil {
					slog.Error("Failed to create cache directory", "error", dirErr)
				} else if writeErr := os.WriteFile(cacheFile, cacheData, 0o600); writeErr != nil {
					slog.Error("Failed to write cache file", "error", writeErr)
				} else {
					slog.Debug("[CACHE] Saved to cache",
						"url", url,
						"cached_at", entry.CachedAt.Format(time.RFC3339),
						"pr_updated_at", entry.UpdatedAt.Format(time.RFC3339),
						"cache_file", filepath.Base(cacheFile))
				}
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
			if removeErr := os.Remove(filePath); removeErr != nil {
				slog.Error("Failed to remove old cache file", "file", filePath, "error", removeErr)
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
