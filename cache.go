package main

import (
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

// getTurnData fetches Turn API data with caching
func (app *App) getTurnData(url string, updatedAt time.Time) (*turn.CheckResponse, error) {
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
	data, err := app.turnClient.Check(app.ctx, url, app.currentUser.GetLogin(), updatedAt)
	if err != nil {
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

// cleanupOldCache removes cache files older than 5 days
func (app *App) cleanupOldCache() {
	entries, err := os.ReadDir(app.cacheDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		filePath := filepath.Join(app.cacheDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		// Remove cache files older than 5 days
		if time.Since(info.ModTime()) > cacheCleanupInterval {
			log.Printf("Removing old cache file: %s", entry.Name())
			os.Remove(filePath)
		}
	}
}

// startCacheCleanup starts periodic cache cleanup
func (app *App) startCacheCleanup() {
	// Initial cleanup
	app.cleanupOldCache()

	// Schedule periodic cleanup
	ticker := time.NewTicker(24 * time.Hour)
	go func() {
		for range ticker.C {
			app.cleanupOldCache()
		}
	}()
}
