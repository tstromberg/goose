package prcache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewManager(t *testing.T) {
	m := NewManager("/tmp/test-cache")
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	if m.cacheDir != "/tmp/test-cache" {
		t.Errorf("cacheDir = %q, want %q", m.cacheDir, "/tmp/test-cache")
	}
}

func TestCacheKey(t *testing.T) {
	url := "https://github.com/owner/repo/pull/123"
	ts := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	key1 := CacheKey(url, ts)
	key2 := CacheKey(url, ts)

	// Same inputs should produce same key
	if key1 != key2 {
		t.Errorf("CacheKey not deterministic: %q != %q", key1, key2)
	}

	// Different timestamp should produce different key
	ts2 := ts.Add(1 * time.Second)
	key3 := CacheKey(url, ts2)
	if key1 == key3 {
		t.Error("CacheKey should differ for different timestamps")
	}

	// Key should be 16 characters (hex encoded)
	if len(key1) != 16 {
		t.Errorf("CacheKey length = %d, want 16", len(key1))
	}
}

func TestCachePath(t *testing.T) {
	cacheDir := t.TempDir()
	m := NewManager(cacheDir)
	path := m.CachePath("abcd1234")

	expected := filepath.Join(cacheDir, "abcd1234.json")
	if path != expected {
		t.Errorf("CachePath = %q, want %q", path, expected)
	}
}

func TestPutAndGet(t *testing.T) {
	// Create temporary cache directory
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)

	url := "https://github.com/owner/repo/pull/123"
	updatedAt := time.Now().Add(-1 * time.Hour)
	cacheKey := CacheKey(url, updatedAt)
	path := m.CachePath(cacheKey)

	// Test data
	data := map[string]string{
		"test": "value",
		"foo":  "bar",
	}

	// Put data in cache
	err := m.Put(path, data, updatedAt)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("Cache file was not created")
	}

	// Get data from cache with long TTL (should hit)
	ttl := 24 * time.Hour
	bypassTTL := 1 * time.Hour
	result, err := m.Get(path, updatedAt, ttl, bypassTTL, nil)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !result.Hit {
		t.Error("Expected cache hit")
	}
	if result.ShouldBypass {
		t.Error("Should not bypass cache")
	}
	if result.Entry == nil {
		t.Fatal("Entry is nil")
	}

	// Verify cached timestamp is recent
	if time.Since(result.Entry.CachedAt) > 5*time.Second {
		t.Errorf("CachedAt is too old: %v", result.Entry.CachedAt)
	}
}

func TestGet_CacheMiss_FileNotExists(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)

	path := filepath.Join(tmpDir, "nonexistent.json")
	updatedAt := time.Now()

	result, err := m.Get(path, updatedAt, 1*time.Hour, 1*time.Minute, nil)
	if err != nil {
		t.Errorf("Get returned error for nonexistent file: %v", err)
	}
	if result.Hit {
		t.Error("Should not have cache hit for nonexistent file")
	}
	if result.ShouldBypass {
		t.Error("Should not bypass for nonexistent file")
	}
}

func TestGet_CacheMiss_CorruptedFile(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)

	path := filepath.Join(tmpDir, "corrupted.json")

	// Write corrupted JSON
	err := os.WriteFile(path, []byte("not valid json {{{"), 0o600)
	if err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	updatedAt := time.Now()
	result, err := m.Get(path, updatedAt, 1*time.Hour, 1*time.Minute, nil)
	if err == nil {
		t.Error("Expected error for corrupted cache file")
	}
	if result != nil && result.Hit {
		t.Error("Should not have cache hit for corrupted file")
	}
	if result != nil && result.ShouldBypass {
		t.Error("Should not bypass for corrupted file")
	}

	// File should be removed
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Corrupted cache file should have been removed")
	}
}

func TestGet_CacheMiss_PRUpdated(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)

	url := "https://github.com/owner/repo/pull/123"
	oldUpdatedAt := time.Now().Add(-2 * time.Hour)
	newUpdatedAt := time.Now().Add(-1 * time.Hour)

	cacheKey := CacheKey(url, oldUpdatedAt)
	path := m.CachePath(cacheKey)

	// Put data with old timestamp
	data := map[string]string{"test": "value"}
	err := m.Put(path, data, oldUpdatedAt)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Try to get with new timestamp (PR was updated)
	result, err := m.Get(path, newUpdatedAt, 24*time.Hour, 1*time.Hour, nil)
	if err != nil {
		t.Errorf("Get failed: %v", err)
	}
	if result.Hit {
		t.Error("Should not have cache hit when PR was updated")
	}
}

func TestGet_CacheMiss_TTLExpired(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)

	url := "https://github.com/owner/repo/pull/123"
	updatedAt := time.Now().Add(-1 * time.Hour)
	cacheKey := CacheKey(url, updatedAt)
	path := m.CachePath(cacheKey)

	// Put data
	data := map[string]string{"test": "value"}
	err := m.Put(path, data, updatedAt)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Wait a bit to ensure cache age
	time.Sleep(100 * time.Millisecond)

	// Get with very short TTL (should miss)
	result, err := m.Get(path, updatedAt, 50*time.Millisecond, 1*time.Hour, nil)
	if err != nil {
		t.Errorf("Get failed: %v", err)
	}
	if result.Hit {
		t.Error("Should not have cache hit when TTL expired")
	}
	if result.ShouldBypass {
		t.Error("Should not bypass without state check")
	}
}

func TestGet_Bypass_WithStateCheck(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)

	url := "https://github.com/owner/repo/pull/123"
	updatedAt := time.Now().Add(-1 * time.Hour)
	cacheKey := CacheKey(url, updatedAt)
	path := m.CachePath(cacheKey)

	// Put data
	data := map[string]any{
		"state": "running",
	}
	err := m.Put(path, data, updatedAt)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Wait to ensure TTL expired
	time.Sleep(100 * time.Millisecond)

	// State check that returns true (incomplete state)
	stateCheck := func(d any) bool {
		if m, ok := d.(map[string]any); ok {
			if state, ok := m["state"].(string); ok {
				return state == "running"
			}
		}
		return false
	}

	// Get with expired TTL but bypass window still valid
	result, err := m.Get(path, updatedAt, 50*time.Millisecond, 1*time.Hour, stateCheck)
	if err != nil {
		t.Errorf("Get failed: %v", err)
	}
	if result.Hit {
		t.Error("Should not have cache hit when TTL expired")
	}
	if !result.ShouldBypass {
		t.Error("Should bypass cache for incomplete state within bypass window")
	}
}

func TestCleanupOldFiles(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)

	// Create some old files
	oldFile1 := filepath.Join(tmpDir, "old1.json")
	oldFile2 := filepath.Join(tmpDir, "old2.json")
	recentFile := filepath.Join(tmpDir, "recent.json")
	nonJSONFile := filepath.Join(tmpDir, "other.txt")

	// Write files
	for _, f := range []string{oldFile1, oldFile2, recentFile, nonJSONFile} {
		if err := os.WriteFile(f, []byte("{}"), 0o600); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}
	}

	// Make old files actually old by changing their modification time
	oldTime := time.Now().Add(-20 * 24 * time.Hour) // 20 days ago
	if err := os.Chtimes(oldFile1, oldTime, oldTime); err != nil {
		t.Fatalf("Failed to change file time: %v", err)
	}
	if err := os.Chtimes(oldFile2, oldTime, oldTime); err != nil {
		t.Fatalf("Failed to change file time: %v", err)
	}

	// Cleanup files older than 15 days
	cleaned, errs := m.CleanupOldFiles(15 * 24 * time.Hour)

	if errs != 0 {
		t.Errorf("Cleanup had errors: %d", errs)
	}

	if cleaned != 2 {
		t.Errorf("Cleaned %d files, want 2", cleaned)
	}

	// Verify old files are gone
	for _, f := range []string{oldFile1, oldFile2} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("Old file %q should have been removed", f)
		}
	}

	// Verify recent file and non-JSON file still exist
	for _, f := range []string{recentFile, nonJSONFile} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("File %q should still exist: %v", f, err)
		}
	}
}

func TestCleanupOldFiles_NoFiles(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)

	cleaned, errs := m.CleanupOldFiles(15 * 24 * time.Hour)

	if cleaned != 0 {
		t.Errorf("Cleaned %d files, want 0", cleaned)
	}
	if errs != 0 {
		t.Errorf("Had %d errors, want 0", errs)
	}
}

func TestCleanupOldFiles_NonexistentDir(t *testing.T) {
	m := NewManager("/nonexistent/directory")

	cleaned, errs := m.CleanupOldFiles(15 * 24 * time.Hour)

	if cleaned != 0 {
		t.Errorf("Cleaned %d files, want 0", cleaned)
	}
	if errs != 1 {
		t.Errorf("Had %d errors, want 1", errs)
	}
}

func TestPut_CreateDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	nestedDir := filepath.Join(tmpDir, "nested", "deep")
	m := NewManager(nestedDir)

	path := filepath.Join(nestedDir, "test.json")
	data := map[string]string{"test": "value"}
	updatedAt := time.Now()

	err := m.Put(path, data, updatedAt)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Verify directory was created
	if _, err := os.Stat(nestedDir); os.IsNotExist(err) {
		t.Error("Nested directory should have been created")
	}

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("Cache file should have been created")
	}
}
