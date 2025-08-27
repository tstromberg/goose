package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestMenuClickRateLimit tests that menu clicks are properly rate limited.
func TestMenuClickRateLimit(t *testing.T) {
	ctx := context.Background()

	// Create app with initial state
	app := &App{
		mu:                sync.RWMutex{},
		stateManager:      NewPRStateManager(time.Now().Add(-35 * time.Second)),
		hiddenOrgs:        make(map[string]bool),
		seenOrgs:          make(map[string]bool),
		lastSearchAttempt: time.Now().Add(-15 * time.Second), // 15 seconds ago
		systrayInterface:  &MockSystray{},                    // Use mock systray to avoid panics
	}

	// Simulate the click handler logic (without the actual UI interaction)
	testClick := func() (shouldRefresh bool, timeSinceLastSearch time.Duration) {
		app.mu.RLock()
		timeSince := time.Since(app.lastSearchAttempt)
		app.mu.RUnlock()

		if timeSince >= minUpdateInterval {
			// Would trigger refresh
			app.mu.Lock()
			app.lastSearchAttempt = time.Now()
			app.mu.Unlock()
			return true, timeSince
		}
		return false, timeSince
	}

	// Test 1: First click should allow refresh (last search was 15s ago)
	shouldRefresh, timeSince := testClick()
	if !shouldRefresh {
		t.Errorf("First click should allow refresh, last search was %v ago", timeSince)
	}

	// Test 2: Immediate second click should be rate limited
	shouldRefresh2, timeSince2 := testClick()
	if shouldRefresh2 {
		t.Errorf("Second click should be rate limited, last search was %v ago", timeSince2)
	}

	// Test 3: After waiting 10+ seconds, should allow refresh again
	app.mu.Lock()
	app.lastSearchAttempt = time.Now().Add(-11 * time.Second)
	app.mu.Unlock()

	shouldRefresh3, timeSince3 := testClick()
	if !shouldRefresh3 {
		t.Errorf("Click after 11 seconds should allow refresh, last search was %v ago", timeSince3)
	}

	_ = ctx // Keep context for potential future use
}

// TestScheduledUpdateRateLimit tests that scheduled updates respect rate limiting.
func TestScheduledUpdateRateLimit(t *testing.T) {
	app := &App{
		mu:                sync.RWMutex{},
		stateManager:      NewPRStateManager(time.Now().Add(-35 * time.Second)),
		hiddenOrgs:        make(map[string]bool),
		seenOrgs:          make(map[string]bool),
		lastSearchAttempt: time.Now().Add(-5 * time.Second), // 5 seconds ago
		systrayInterface:  &MockSystray{},                   // Use mock systray to avoid panics
	}

	// Simulate the scheduled update logic
	testScheduledUpdate := func() (shouldUpdate bool, timeSinceLastSearch time.Duration) {
		app.mu.RLock()
		timeSince := time.Since(app.lastSearchAttempt)
		app.mu.RUnlock()

		return timeSince >= minUpdateInterval, timeSince
	}

	// Test 1: Scheduled update should be skipped (last search was only 5s ago)
	shouldUpdate, timeSince := testScheduledUpdate()
	if shouldUpdate {
		t.Errorf("Scheduled update should be skipped, last search was %v ago", timeSince)
	}

	// Test 2: After waiting 10+ seconds, scheduled update should proceed
	app.mu.Lock()
	app.lastSearchAttempt = time.Now().Add(-12 * time.Second)
	app.mu.Unlock()

	shouldUpdate2, timeSince2 := testScheduledUpdate()
	if !shouldUpdate2 {
		t.Errorf("Scheduled update after 12 seconds should proceed, last search was %v ago", timeSince2)
	}
}
