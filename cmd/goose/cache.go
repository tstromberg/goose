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
	// Validate URL before processing
	if err := validateURL(url); err != nil {
		return nil, false, fmt.Errorf("invalid URL: %w", err)
	}

	// Create cache key from URL and updated timestamp
	key := fmt.Sprintf("%s-%s", url, updatedAt.Format(time.RFC3339))
	hash := sha256.Sum256([]byte(key))
	cacheFile := filepath.Join(app.cacheDir, hex.EncodeToString(hash[:])[:16]+".json")

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
				// Check if cache is still valid (2 hour TTL)
				return entry.Data, true, nil
			}
		}
	}

	// Cache miss, fetch from API
	if app.noCache {
		slog.Debug("Cache bypassed (--no-cache), fetching from Turn API", "url", url)
	} else {
		slog.Debug("Cache miss, fetching from Turn API", "url", url)
	}

	// Use exponential backoff with jitter for Turn API calls
	var data *turn.CheckResponse
	err := retry.Do(func() error {
		// Create timeout context for Turn API call
		turnCtx, cancel := context.WithTimeout(ctx, turnAPITimeout)
		defer cancel()

		var retryErr error
		data, retryErr = app.turnClient.Check(turnCtx, url, app.currentUser.GetLogin(), updatedAt)
		if retryErr != nil {
			slog.Warn("Turn API error (will retry)", "error", retryErr)
			return retryErr
		}
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
		return nil, false, err
	}

	// Save to cache (don't fail if caching fails) - skip if --no-cache is set
	if !app.noCache {
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
			}
		}
	}

	return data, false, nil
}

// cleanupOldCache removes cache files older than 5 days.
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

		// Remove cache files older than 5 days
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
