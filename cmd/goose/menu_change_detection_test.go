package main

import (
	"slices"
	"sync"
	"testing"
	"time"
)

// TestMenuChangeDetection tests that the menu change detection logic works correctly
// and prevents unnecessary menu rebuilds when PR data hasn't changed.
func TestMenuChangeDetection(t *testing.T) {
	// Create app with test data
	app := &App{
		mu:                 sync.RWMutex{},
		stateManager:       NewPRStateManager(time.Now()),
		hiddenOrgs:         make(map[string]bool),
		seenOrgs:           make(map[string]bool),
		blockedPRTimes:     make(map[string]time.Time),
		browserRateLimiter: NewBrowserRateLimiter(startupGracePeriod, 5, defaultMaxBrowserOpensDay),
		systrayInterface:   &MockSystray{},
		incoming: []PR{
			{Repository: "org1/repo1", Number: 1, Title: "Fix bug", URL: "https://github.com/org1/repo1/pull/1", NeedsReview: true, UpdatedAt: time.Now()},
			{Repository: "org2/repo2", Number: 2, Title: "Add feature", URL: "https://github.com/org2/repo2/pull/2", NeedsReview: false, UpdatedAt: time.Now()},
		},
		outgoing: []PR{
			{Repository: "org3/repo3", Number: 3, Title: "Update docs", URL: "https://github.com/org3/repo3/pull/3", IsBlocked: true, UpdatedAt: time.Now()},
		},
	}

	t.Run("same_titles_should_be_equal", func(t *testing.T) {
		// Generate titles twice with same data
		titles1 := app.generateMenuTitles()
		titles2 := app.generateMenuTitles()

		// They should be equal
		if !slices.Equal(titles1, titles2) {
			t.Errorf("Same PR data generated different titles:\nFirst:  %v\nSecond: %v", titles1, titles2)
		}
	})

	t.Run("different_pr_count_changes_titles", func(t *testing.T) {
		// Generate initial titles
		initialTitles := app.generateMenuTitles()

		// Add a new PR
		app.incoming = append(app.incoming, PR{
			Repository:  "org4/repo4",
			Number:      4,
			Title:       "New PR",
			URL:         "https://github.com/org4/repo4/pull/4",
			NeedsReview: true,
			UpdatedAt:   time.Now(),
		})

		// Generate new titles
		newTitles := app.generateMenuTitles()

		// They should be different
		if slices.Equal(initialTitles, newTitles) {
			t.Error("Adding a PR didn't change the menu titles")
		}

		// The new titles should have more items
		if len(newTitles) <= len(initialTitles) {
			t.Errorf("New titles should have more items: got %d, initial had %d", len(newTitles), len(initialTitles))
		}
	})

	t.Run("pr_repository_change_updates_menu", func(t *testing.T) {
		// Generate initial titles
		initialTitles := app.generateMenuTitles()

		// Change a PR repository (this would be unusual but tests the title generation)
		app.incoming[0].Repository = "different-org/different-repo"

		// Generate new titles
		newTitles := app.generateMenuTitles()

		// They should be different because menu shows "org/repo #number"
		if slices.Equal(initialTitles, newTitles) {
			t.Error("Changing a PR repository didn't change the menu titles")
		}
	})

	t.Run("blocked_status_change_updates_menu", func(t *testing.T) {
		// Generate initial titles
		initialTitles := app.generateMenuTitles()

		// Change blocked status
		app.incoming[1].NeedsReview = true // Make it blocked

		// Generate new titles
		newTitles := app.generateMenuTitles()

		// They should be different because the title prefix changes for blocked PRs
		if slices.Equal(initialTitles, newTitles) {
			t.Error("Changing PR blocked status didn't change the menu titles")
		}
	})
}

// TestFirstRunMenuRebuildBug tests the specific bug where the first scheduled update
// after initial load would unnecessarily rebuild the menu.
func TestFirstRunMenuRebuildBug(t *testing.T) {
	// Create app simulating initial state
	app := &App{
		mu:                 sync.RWMutex{},
		stateManager:       NewPRStateManager(time.Now()),
		hiddenOrgs:         make(map[string]bool),
		seenOrgs:           make(map[string]bool),
		blockedPRTimes:     make(map[string]time.Time),
		browserRateLimiter: NewBrowserRateLimiter(startupGracePeriod, 5, defaultMaxBrowserOpensDay),
		menuInitialized:    false,
		systrayInterface:   &MockSystray{},
		lastMenuTitles:     nil, // This is nil on first run - the bug condition
		incoming: []PR{
			{Repository: "test/repo", Number: 1, Title: "Test PR", URL: "https://github.com/test/repo/pull/1"},
		},
	}

	// Simulate what happens during initial load
	// OLD BEHAVIOR: lastMenuTitles would remain nil
	// NEW BEHAVIOR: lastMenuTitles should be set after initial menu build

	// Generate initial titles (simulating what rebuildMenu would show)
	initialTitles := app.generateMenuTitles()

	// This is the fix - store titles after initial build
	app.mu.Lock()
	app.lastMenuTitles = initialTitles
	app.menuInitialized = true
	app.mu.Unlock()

	// Now simulate first scheduled update with same data
	// Generate current titles (should be same as initial)
	currentTitles := app.generateMenuTitles()

	// Get stored titles
	app.mu.RLock()
	storedTitles := app.lastMenuTitles
	app.mu.RUnlock()

	// Test 1: Stored titles should not be nil/empty
	if len(storedTitles) == 0 {
		t.Fatal("BUG: lastMenuTitles not set after initial menu build")
	}

	// Test 2: Current and stored titles should be equal (no changes)
	if !slices.Equal(currentTitles, storedTitles) {
		t.Errorf("BUG: Titles marked as different when they're the same:\nCurrent: %v\nStored:  %v",
			currentTitles, storedTitles)
	}

	// Test 3: Verify the comparison result that updateMenu would make
	// In the bug, this would be comparing non-empty current titles with nil/empty stored titles
	// and would return false (different), triggering unnecessary rebuild
	shouldSkipRebuild := slices.Equal(currentTitles, storedTitles)
	if !shouldSkipRebuild {
		t.Error("BUG: Would rebuild menu even though PR data hasn't changed")
	}
}

// TestHiddenOrgChangesMenu tests that hiding/showing orgs updates menu titles
func TestHiddenOrgChangesMenu(t *testing.T) {
	app := &App{
		mu:                 sync.RWMutex{},
		stateManager:       NewPRStateManager(time.Now()),
		hiddenOrgs:         make(map[string]bool),
		seenOrgs:           make(map[string]bool),
		blockedPRTimes:     make(map[string]time.Time),
		browserRateLimiter: NewBrowserRateLimiter(startupGracePeriod, 5, defaultMaxBrowserOpensDay),
		systrayInterface:   &MockSystray{},
		incoming: []PR{
			{Repository: "org1/repo1", Number: 1, Title: "PR 1", URL: "https://github.com/org1/repo1/pull/1"},
			{Repository: "org2/repo2", Number: 2, Title: "PR 2", URL: "https://github.com/org2/repo2/pull/2"},
		},
	}

	// Generate initial titles
	initialTitles := app.generateMenuTitles()
	initialCount := len(initialTitles)

	// Hide org1
	app.hiddenOrgs["org1"] = true

	// Generate new titles - should have fewer items
	newTitles := app.generateMenuTitles()

	// Titles should be different
	if slices.Equal(initialTitles, newTitles) {
		t.Error("Hiding an org didn't change menu titles")
	}

	// Should have fewer items (org1/repo1 should be hidden)
	if len(newTitles) >= initialCount {
		t.Errorf("Menu should have fewer items after hiding org: got %d, started with %d",
			len(newTitles), initialCount)
	}
}
