// Package main implements browser rate limiting for PR auto-open feature.
package main

import (
	"log"
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
	log.Printf("[BROWSER] Initializing rate limiter: startup_delay=%v, max_per_minute=%d, max_per_day=%d",
		startupDelay, maxPerMinute, maxPerDay)
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

	// Check if we've already opened this PR
	if b.openedPRs[prURL] {
		log.Printf("[BROWSER] Skipping auto-open: PR already opened - %s", prURL)
		return false
	}

	// Check startup delay
	if time.Since(startTime) < b.startupDelay {
		log.Printf("[BROWSER] Skipping auto-open: within startup delay period (%v remaining)",
			b.startupDelay-time.Since(startTime))
		return false
	}

	now := time.Now()

	// Clean old entries
	b.cleanOldEntries(now)

	// Check per-minute limit
	if len(b.openedLastMinute) >= b.maxPerMinute {
		log.Printf("[BROWSER] Rate limit: already opened %d PRs in the last minute (max: %d)",
			len(b.openedLastMinute), b.maxPerMinute)
		return false
	}

	// Check per-day limit
	if len(b.openedToday) >= b.maxPerDay {
		log.Printf("[BROWSER] Rate limit: already opened %d PRs today (max: %d)",
			len(b.openedToday), b.maxPerDay)
		return false
	}

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

	log.Printf("[BROWSER] Recorded browser open for %s (minute: %d/%d, today: %d/%d)",
		prURL, len(b.openedLastMinute), b.maxPerMinute, len(b.openedToday), b.maxPerDay)
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
	log.Printf("[BROWSER] Rate limiter reset: cleared %d tracked PRs", previousCount)
}
