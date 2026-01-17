// Package prcache provides caching functionality for PR metadata with TTL support.
package prcache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Entry represents a cached item with metadata.
type Entry[T any] struct {
	Data      T         `json:"data"`
	CachedAt  time.Time `json:"cached_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Manager handles caching of PR metadata with TTL and invalidation logic.
type Manager struct {
	cacheDir string
}

// NewManager creates a new cache manager.
func NewManager(cacheDir string) *Manager {
	return &Manager{cacheDir: cacheDir}
}

// CacheKey generates a cache key from a URL and timestamp.
func CacheKey(url string, updatedAt time.Time) string {
	key := fmt.Sprintf("%s-%s", url, updatedAt.Format(time.RFC3339))
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])[:16]
}

// CachePath returns the file path for a cache key.
func (m *Manager) CachePath(cacheKey string) string {
	return filepath.Join(m.cacheDir, cacheKey+".json")
}

// CacheResult represents the result of a cache lookup.
type CacheResult struct {
	Entry        *Entry[any]
	Hit          bool // True if cache entry was found and valid
	ShouldBypass bool // True if cache should be bypassed (e.g., for running tests)
}

// Get retrieves cached data if valid according to TTL rules.
func (*Manager) Get(path string, updatedAt time.Time, ttl time.Duration, bypassTTL time.Duration, stateCheck func(any) bool) (*CacheResult, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &CacheResult{}, nil
		}
		return nil, fmt.Errorf("read cache file: %w", err)
	}

	var e Entry[any]
	if err := json.Unmarshal(b, &e); err != nil {
		// Corrupted cache file - try to remove it
		if removeErr := os.Remove(path); removeErr != nil {
			slog.Debug("Failed to remove corrupted cache file", "path", path, "error", removeErr)
		}
		return nil, fmt.Errorf("unmarshal cache: %w", err)
	}

	// Check if PR was updated since cache
	if !e.UpdatedAt.Equal(updatedAt) {
		return &CacheResult{}, nil
	}

	age := time.Since(e.CachedAt)

	// Check if should bypass cache for incomplete state (regardless of TTL)
	// This ensures we fetch fresh data when tests are still running
	if stateCheck != nil && stateCheck(e.Data) && age < bypassTTL {
		return &CacheResult{ShouldBypass: true}, nil
	}

	// Check TTL
	if age >= ttl {
		return &CacheResult{}, nil
	}

	return &CacheResult{Entry: &e, Hit: true}, nil
}

// Put stores data in the cache.
func (*Manager) Put(path string, data any, updatedAt time.Time) error {
	e := Entry[any]{
		Data:      data,
		CachedAt:  time.Now(),
		UpdatedAt: updatedAt,
	}

	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal cache data: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}

	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write cache file: %w", err)
	}

	return nil
}

// CleanupOldFiles removes cache files older than the specified interval.
func (m *Manager) CleanupOldFiles(maxAge time.Duration) (cleaned int, errs int) {
	entries, err := os.ReadDir(m.cacheDir)
	if err != nil {
		slog.Error("Failed to read cache directory for cleanup", "error", err)
		return 0, 1
	}

	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}

		info, err := e.Info()
		if err != nil {
			errs++
			continue
		}

		if time.Since(info.ModTime()) > maxAge {
			p := filepath.Join(m.cacheDir, e.Name())
			if err := os.Remove(p); err != nil {
				errs++
			} else {
				cleaned++
			}
		}
	}

	return cleaned, errs
}
