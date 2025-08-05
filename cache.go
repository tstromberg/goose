// Package main - cache.go provides caching functionality for Turn API responses.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ready-to-review/turnclient/pkg/turn"
)

type cacheEntry struct {
	Data      *turn.CheckResponse `json:"data"`
	CachedAt  time.Time           `json:"cached_at"`
	UpdatedAt time.Time           `json:"updated_at"`
}

// turnData fetches Turn API data with caching.
func (app *App) turnData(ctx context.Context, url string, updatedAt time.Time) (*turn.CheckResponse, error) {
	// Create cache key from URL and updated timestamp
	key := fmt.Sprintf("%s-%s", url, updatedAt.Format(time.RFC3339))
	hash := sha256.Sum256([]byte(key))
	cacheFile := filepath.Join(app.cacheDir, hex.EncodeToString(hash[:])[:16]+".json")

	// Try to read from cache
	if data, err := os.ReadFile(cacheFile); err == nil {
		var entry cacheEntry
		if err := json.Unmarshal(data, &entry); err == nil {
			// Check if cache is still valid (2 hour TTL)
			if time.Since(entry.CachedAt) < cacheTTL && entry.UpdatedAt.Equal(updatedAt) {
				return entry.Data, nil
			}
		}
	}

	// Cache miss, fetch from API
	log.Printf("Cache miss for %s, fetching from Turn API", url)

	// Just try once with timeout - if Turn API fails, it's not critical
	turnCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	data, err := app.turnClient.Check(turnCtx, url, app.currentUser.GetLogin(), updatedAt)
	if err != nil {
		log.Printf("Turn API error (will use PR without metadata): %v", err)
		return nil, err
	}

	// Save to cache
	entry := cacheEntry{
		Data:      data,
		CachedAt:  time.Now(),
		UpdatedAt: updatedAt,
	}
	cacheData, err := json.Marshal(entry)
	if err == nil {
		if err := os.WriteFile(cacheFile, cacheData, 0o600); err != nil {
			log.Printf("Failed to write cache for %s: %v", url, err)
		}
	}

	return data, nil
}

// cleanupOldCache removes cache files older than 5 days.
func (app *App) cleanupOldCache() {
	entries, err := os.ReadDir(app.cacheDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		// Remove cache files older than 5 days
		if time.Since(info.ModTime()) > cacheCleanupInterval {
			if err := os.Remove(filepath.Join(app.cacheDir, entry.Name())); err != nil {
				log.Printf("Failed to remove old cache: %v", err)
			}
		}
	}
}
