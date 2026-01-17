package ratelimit

import (
	"testing"
	"time"
)

func TestNewBrowserRateLimiter(t *testing.T) {
	limiter := NewBrowserRateLimiter(1*time.Minute, 2, 10)

	if limiter == nil {
		t.Fatal("NewBrowserRateLimiter returned nil")
	}

	if limiter.startupDelay != 1*time.Minute {
		t.Errorf("startupDelay = %v, want %v", limiter.startupDelay, 1*time.Minute)
	}

	if limiter.maxPerMinute != 2 {
		t.Errorf("maxPerMinute = %d, want 2", limiter.maxPerMinute)
	}

	if limiter.maxPerDay != 10 {
		t.Errorf("maxPerDay = %d, want 10", limiter.maxPerDay)
	}
}

func TestBrowserRateLimiter_CanOpen_StartupDelay(t *testing.T) {
	startTime := time.Now()
	limiter := NewBrowserRateLimiter(1*time.Minute, 10, 100)

	// Should not allow opening during startup delay
	if limiter.CanOpen(startTime, "https://github.com/owner/repo/pull/1") {
		t.Error("CanOpen should return false during startup delay")
	}

	// Should allow opening after startup delay
	pastStartTime := time.Now().Add(-2 * time.Minute)
	if !limiter.CanOpen(pastStartTime, "https://github.com/owner/repo/pull/1") {
		t.Error("CanOpen should return true after startup delay")
	}
}

func TestBrowserRateLimiter_CanOpen_DuplicatePR(t *testing.T) {
	startTime := time.Now().Add(-2 * time.Minute) // Past startup delay
	limiter := NewBrowserRateLimiter(1*time.Minute, 10, 100)

	prURL := "https://github.com/owner/repo/pull/1"

	// First call should succeed
	if !limiter.CanOpen(startTime, prURL) {
		t.Error("CanOpen should return true for first call")
	}

	// Record the open
	limiter.RecordOpen(prURL)

	// Second call for same PR should fail
	if limiter.CanOpen(startTime, prURL) {
		t.Error("CanOpen should return false for duplicate PR")
	}
}

func TestBrowserRateLimiter_CanOpen_PerMinuteLimit(t *testing.T) {
	startTime := time.Now().Add(-2 * time.Minute)           // Past startup delay
	limiter := NewBrowserRateLimiter(1*time.Minute, 2, 100) // Max 2 per minute

	// Open first PR
	if !limiter.CanOpen(startTime, "https://github.com/owner/repo/pull/1") {
		t.Error("First CanOpen should succeed")
	}
	limiter.RecordOpen("https://github.com/owner/repo/pull/1")

	// Open second PR
	if !limiter.CanOpen(startTime, "https://github.com/owner/repo/pull/2") {
		t.Error("Second CanOpen should succeed")
	}
	limiter.RecordOpen("https://github.com/owner/repo/pull/2")

	// Third PR should fail per-minute limit
	if limiter.CanOpen(startTime, "https://github.com/owner/repo/pull/3") {
		t.Error("Third CanOpen should fail per-minute limit")
	}
}

func TestBrowserRateLimiter_CanOpen_PerDayLimit(t *testing.T) {
	startTime := time.Now().Add(-2 * time.Minute)           // Past startup delay
	limiter := NewBrowserRateLimiter(1*time.Minute, 100, 2) // Max 2 per day

	// Open first PR
	if !limiter.CanOpen(startTime, "https://github.com/owner/repo/pull/1") {
		t.Error("First CanOpen should succeed")
	}
	limiter.RecordOpen("https://github.com/owner/repo/pull/1")

	// Manually clear per-minute limit to test daily limit in isolation
	limiter.mu.Lock()
	limiter.openedLastMinute = []time.Time{}
	limiter.mu.Unlock()

	// Open second PR
	if !limiter.CanOpen(startTime, "https://github.com/owner/repo/pull/2") {
		t.Error("Second CanOpen should succeed")
	}
	limiter.RecordOpen("https://github.com/owner/repo/pull/2")

	// Manually clear per-minute limit again
	limiter.mu.Lock()
	limiter.openedLastMinute = []time.Time{}
	limiter.mu.Unlock()

	// Third PR should fail per-day limit
	if limiter.CanOpen(startTime, "https://github.com/owner/repo/pull/3") {
		t.Error("Third CanOpen should fail per-day limit")
	}
}

func TestBrowserRateLimiter_RecordOpen(t *testing.T) {
	limiter := NewBrowserRateLimiter(1*time.Minute, 10, 100)

	prURL := "https://github.com/owner/repo/pull/1"

	// Record an open
	limiter.RecordOpen(prURL)

	// Verify it's tracked in openedPRs
	limiter.mu.Lock()
	if !limiter.openedPRs[prURL] {
		t.Error("PR should be tracked in openedPRs")
	}

	// Verify it's tracked in time windows
	if len(limiter.openedLastMinute) != 1 {
		t.Errorf("openedLastMinute = %d, want 1", len(limiter.openedLastMinute))
	}

	if len(limiter.openedToday) != 1 {
		t.Errorf("openedToday = %d, want 1", len(limiter.openedToday))
	}
	limiter.mu.Unlock()
}

func TestBrowserRateLimiter_CleanOldEntries(t *testing.T) {
	limiter := NewBrowserRateLimiter(1*time.Minute, 10, 100)

	now := time.Now()

	// Add entries at different times
	limiter.mu.Lock()
	limiter.openedLastMinute = []time.Time{
		now.Add(-2 * time.Minute),  // Should be cleaned (>1 minute ago)
		now.Add(-30 * time.Second), // Should remain (<1 minute ago)
	}
	limiter.openedToday = []time.Time{
		now.Add(-25 * time.Hour), // Should be cleaned (>24 hours ago)
		now.Add(-1 * time.Hour),  // Should remain (<24 hours ago)
	}
	limiter.mu.Unlock()

	// Clean entries
	limiter.mu.Lock()
	limiter.cleanOldEntries(now)

	// Check per-minute entries
	if len(limiter.openedLastMinute) != 1 {
		t.Errorf("openedLastMinute after clean = %d, want 1", len(limiter.openedLastMinute))
	}

	// Check per-day entries
	if len(limiter.openedToday) != 1 {
		t.Errorf("openedToday after clean = %d, want 1", len(limiter.openedToday))
	}
	limiter.mu.Unlock()
}

func TestBrowserRateLimiter_Reset(t *testing.T) {
	limiter := NewBrowserRateLimiter(1*time.Minute, 10, 100)

	// Add some opened PRs
	limiter.RecordOpen("https://github.com/owner/repo/pull/1")
	limiter.RecordOpen("https://github.com/owner/repo/pull/2")
	limiter.RecordOpen("https://github.com/owner/repo/pull/3")

	// Verify they're tracked
	limiter.mu.Lock()
	if len(limiter.openedPRs) != 3 {
		t.Errorf("openedPRs before reset = %d, want 3", len(limiter.openedPRs))
	}
	limiter.mu.Unlock()

	// Reset
	limiter.Reset()

	// Verify they're cleared
	limiter.mu.Lock()
	if len(limiter.openedPRs) != 0 {
		t.Errorf("openedPRs after reset = %d, want 0", len(limiter.openedPRs))
	}
	limiter.mu.Unlock()

	// Time window entries should still exist (reset only clears PR tracking)
	limiter.mu.Lock()
	if len(limiter.openedLastMinute) == 0 {
		t.Error("openedLastMinute should not be cleared by Reset")
	}
	if len(limiter.openedToday) == 0 {
		t.Error("openedToday should not be cleared by Reset")
	}
	limiter.mu.Unlock()
}

func TestBrowserRateLimiter_Concurrent(t *testing.T) {
	startTime := time.Now().Add(-2 * time.Minute) // Past startup delay
	limiter := NewBrowserRateLimiter(1*time.Minute, 100, 1000)

	// Test concurrent access
	done := make(chan bool)
	for i := range 10 {
		go func(id int) {
			prURL := "https://github.com/owner/repo/pull/" + string(rune('1'+id))
			if limiter.CanOpen(startTime, prURL) {
				limiter.RecordOpen(prURL)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for range 10 {
		<-done
	}

	// Verify no race conditions (test runs with -race flag will catch issues)
	limiter.mu.Lock()
	totalOpened := len(limiter.openedPRs)
	limiter.mu.Unlock()

	if totalOpened == 0 {
		t.Error("No PRs were opened")
	}
}
