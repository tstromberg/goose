// Package main implements browser rate limiting for PR auto-open feature.
package main

import (
	"log/slog"
	"sync"
	"time"
)

// BrowserRateLimiter manages rate limiting for automatically opening browser windows.
type BrowserRateLimiter struct {
	openedPRs        map[string]bool
	openedLastMinute []time.Time
	openedToday      []time.Time
	startupDelay     time.Duration
	maxPerMinute     int
	maxPerDay        int
	mu               sync.Mutex
}

// NewBrowserRateLimiter creates a new browser rate limiter.
func NewBrowserRateLimiter(startupDelay time.Duration, maxPerMinute, maxPerDay int) *BrowserRateLimiter {
	slog.Info("[BROWSER] Initializing rate limiter",
		"startup_delay", startupDelay, "max_per_minute", maxPerMinute, "max_per_day", maxPerDay)
	return &BrowserRateLimiter{
		openedLastMinute: make([]time.Time, 0),
		openedToday:      make([]time.Time, 0),
		startupDelay:     startupDelay,
		maxPerMinute:     maxPerMinute,
		maxPerDay:        maxPerDay,
		openedPRs:        make(map[string]bool),
	}
}

// CanOpen checks if we can open a browser window according to rate limits.
func (b *BrowserRateLimiter) CanOpen(startTime time.Time, prURL string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	slog.Info("[BROWSER] CanOpen check",
		"url", prURL,
		"time_since_start", time.Since(startTime).Round(time.Second),
		"startup_delay", b.startupDelay)

	// Check if we've already opened this PR
	if b.openedPRs[prURL] {
		slog.Info("[BROWSER] Skipping auto-open: PR already opened", "url", prURL)
		return false
	}

	// Check startup delay
	if time.Since(startTime) < b.startupDelay {
		slog.Info("[BROWSER] Skipping auto-open: within startup delay period",
			"remaining", b.startupDelay-time.Since(startTime))
		return false
	}

	now := time.Now()

	// Clean old entries
	b.cleanOldEntries(now)

	// Check per-minute limit
	if len(b.openedLastMinute) >= b.maxPerMinute {
		slog.Info("[BROWSER] Rate limit: per-minute limit reached",
			"opened", len(b.openedLastMinute), "max", b.maxPerMinute)
		return false
	}

	// Check per-day limit
	if len(b.openedToday) >= b.maxPerDay {
		slog.Info("[BROWSER] Rate limit: daily limit reached",
			"opened", len(b.openedToday), "max", b.maxPerDay)
		return false
	}

	slog.Info("[BROWSER] CanOpen returning true", "url", prURL)
	return true
}

// RecordOpen records that a browser window was opened.
func (b *BrowserRateLimiter) RecordOpen(prURL string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	b.openedLastMinute = append(b.openedLastMinute, now)
	b.openedToday = append(b.openedToday, now)
	b.openedPRs[prURL] = true

	slog.Info("[BROWSER] Recorded browser open",
		"url", prURL, "minuteCount", len(b.openedLastMinute), "minuteMax", b.maxPerMinute,
		"todayCount", len(b.openedToday), "todayMax", b.maxPerDay)
}

// cleanOldEntries removes entries outside the time windows.
func (b *BrowserRateLimiter) cleanOldEntries(now time.Time) {
	// Clean entries older than 1 minute
	oneMinuteAgo := now.Add(-1 * time.Minute)
	newLastMinute := make([]time.Time, 0, len(b.openedLastMinute))
	for _, t := range b.openedLastMinute {
		if t.After(oneMinuteAgo) {
			newLastMinute = append(newLastMinute, t)
		}
	}
	b.openedLastMinute = newLastMinute

	// Clean entries older than 24 hours (1 day)
	oneDayAgo := now.Add(-24 * time.Hour)
	newToday := make([]time.Time, 0, len(b.openedToday))
	for _, t := range b.openedToday {
		if t.After(oneDayAgo) {
			newToday = append(newToday, t)
		}
	}
	b.openedToday = newToday
}

// Reset clears the opened PRs tracking - useful when toggling the feature.
func (b *BrowserRateLimiter) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	previousCount := len(b.openedPRs)
	b.openedPRs = make(map[string]bool)
	slog.Info("[BROWSER] Rate limiter reset", "clearedPRs", previousCount)
}
