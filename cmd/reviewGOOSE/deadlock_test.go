package main

import (
	"sync"
	"testing"
	"time"
)

// TestConcurrentMenuOperations tests that concurrent menu operations don't cause deadlocks
func TestConcurrentMenuOperations(t *testing.T) {
	app := &App{
		mu:                 sync.RWMutex{},
		stateManager:       NewPRStateManager(time.Now()),
		hiddenOrgs:         make(map[string]bool),
		seenOrgs:           make(map[string]bool),
		blockedPRTimes:     make(map[string]time.Time),
		browserRateLimiter: NewBrowserRateLimiter(startupGracePeriod, 5, defaultMaxBrowserOpensDay),
		systrayInterface:   &MockSystray{},
		incoming: []PR{
			{Repository: "org1/repo1", Number: 1, Title: "Fix bug", URL: "https://github.com/org1/repo1/pull/1"},
		},
		outgoing: []PR{
			{Repository: "org2/repo2", Number: 2, Title: "Add feature", URL: "https://github.com/org2/repo2/pull/2"},
		},
	}

	// Use a WaitGroup to coordinate goroutines
	var wg sync.WaitGroup

	// Use a channel to detect if we've deadlocked
	done := make(chan bool, 1)

	// Number of concurrent operations to test
	concurrentOps := 10

	wg.Add(concurrentOps * 3) // 3 types of operations

	// Start a goroutine that will signal completion
	go func() {
		wg.Wait()
		done <- true
	}()

	// Simulate concurrent menu clicks (write lock operations)
	for range concurrentOps {
		go func() {
			defer wg.Done()

			// This simulates the click handler storing menu titles
			menuTitles := app.generateMenuTitles()
			app.mu.Lock()
			app.lastMenuTitles = menuTitles
			app.mu.Unlock()
		}()
	}

	// Simulate concurrent menu generation (read lock operations)
	for range concurrentOps {
		go func() {
			defer wg.Done()

			// This simulates generating menu titles
			_ = app.generateMenuTitles()
		}()
	}

	// Simulate concurrent PR updates (write lock operations)
	for i := range concurrentOps {
		go func(iteration int) {
			defer wg.Done()

			app.mu.Lock()
			// Simulate updating PR data
			if iteration%2 == 0 {
				app.incoming = append(app.incoming, PR{
					Repository: "test/repo",
					Number:     iteration,
					Title:      "Test PR",
					URL:        "https://github.com/test/repo/pull/1",
				})
			}
			app.mu.Unlock()
		}(i)
	}

	// Wait for operations to complete or timeout
	select {
	case <-done:
		// Success - all operations completed without deadlock
		t.Log("All concurrent operations completed successfully")
	case <-time.After(5 * time.Second):
		t.Fatal("Deadlock detected: operations did not complete within 5 seconds")
	}
}

// TestMenuClickDeadlockScenario specifically tests the deadlock scenario that was fixed
func TestMenuClickDeadlockScenario(t *testing.T) {
	app := &App{
		mu:                 sync.RWMutex{},
		stateManager:       NewPRStateManager(time.Now()),
		hiddenOrgs:         make(map[string]bool),
		seenOrgs:           make(map[string]bool),
		blockedPRTimes:     make(map[string]time.Time),
		browserRateLimiter: NewBrowserRateLimiter(startupGracePeriod, 5, defaultMaxBrowserOpensDay),
		systrayInterface:   &MockSystray{},
		incoming: []PR{
			{Repository: "org1/repo1", Number: 1, Title: "Test PR", URL: "https://github.com/org1/repo1/pull/1"},
		},
	}

	// This exact sequence previously caused a deadlock:
	// 1. Click handler acquires write lock
	// 2. Click handler calls generateMenuTitles while holding lock
	// 3. generateMenuTitles tries to acquire read lock
	// 4. Deadlock!

	// The fix ensures we don't hold the lock when calling generateMenuTitles
	done := make(chan bool, 1)

	go func() {
		// Simulate the fixed click handler behavior
		menuTitles := app.generateMenuTitles() // Called WITHOUT holding lock
		app.mu.Lock()
		app.lastMenuTitles = menuTitles
		app.mu.Unlock()
		done <- true
	}()

	select {
	case <-done:
		t.Log("Click handler completed without deadlock")
	case <-time.After(1 * time.Second):
		t.Fatal("Click handler deadlocked")
	}
}

// TestRapidMenuClicks tests that rapid menu clicks don't cause issues
func TestRapidMenuClicks(t *testing.T) {
	app := &App{
		mu:                 sync.RWMutex{},
		stateManager:       NewPRStateManager(time.Now()),
		hiddenOrgs:         make(map[string]bool),
		seenOrgs:           make(map[string]bool),
		blockedPRTimes:     make(map[string]time.Time),
		browserRateLimiter: NewBrowserRateLimiter(startupGracePeriod, 5, defaultMaxBrowserOpensDay),
		systrayInterface:   &MockSystray{},
		lastSearchAttempt:  time.Now().Add(-15 * time.Second), // Allow first click
		incoming: []PR{
			{Repository: "org1/repo1", Number: 1, Title: "Test", URL: "https://github.com/org1/repo1/pull/1"},
		},
	}

	// Simulate 20 rapid clicks
	clickCount := 20
	successfulClicks := 0

	for range clickCount {
		// Check if enough time has passed for rate limiting
		app.mu.RLock()
		timeSince := time.Since(app.lastSearchAttempt)
		app.mu.RUnlock()

		if timeSince >= minUpdateInterval {
			// This click would trigger a refresh
			app.mu.Lock()
			app.lastSearchAttempt = time.Now()
			app.mu.Unlock()
			successfulClicks++

			// Also update menu titles as the real handler would
			menuTitles := app.generateMenuTitles()
			app.mu.Lock()
			app.lastMenuTitles = menuTitles
			app.mu.Unlock()
		}

		// Small delay between clicks to simulate human clicking
		time.Sleep(10 * time.Millisecond)
	}

	// Due to rate limiting, we should only have 1-2 successful clicks
	if successfulClicks > 3 {
		t.Errorf("Rate limiting not working: %d clicks succeeded out of %d rapid clicks", successfulClicks, clickCount)
	}

	t.Logf("Rate limiting working correctly: %d clicks succeeded out of %d rapid clicks", successfulClicks, clickCount)
}
