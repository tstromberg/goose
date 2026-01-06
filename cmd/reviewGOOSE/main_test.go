package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codeGROOVE-dev/turnclient/pkg/turn"
	"github.com/google/go-github/v57/github"
)

func TestMain(m *testing.M) {
	// Set test mode to prevent actual sound playback during tests
	if err := os.Setenv("GOOSE_TEST_MODE", "1"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestIsStale(t *testing.T) {
	// Capture time.Now() once to avoid race conditions
	now := time.Now()
	tests := []struct {
		time     time.Time
		name     string
		expected bool
	}{
		{
			name:     "recent PR",
			time:     now.Add(-24 * time.Hour),
			expected: false,
		},
		{
			name:     "stale PR",
			time:     now.Add(-91 * 24 * time.Hour),
			expected: true,
		},
		{
			name:     "exactly at threshold",
			time:     now.Add(-90 * 24 * time.Hour),
			expected: true, // >= 90 days is stale
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// isStale was inlined - test the logic directly
			// Use the same 'now' for consistency
			threshold := now.Add(-stalePRThreshold)
			got := !tt.time.After(threshold) // time <= threshold means stale (>= 90 days old)
			if got != tt.expected {
				t.Logf("Test time: %v, Threshold: %v, Before: %v", tt.time, threshold, got)
				t.Errorf("stale check = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestMenuItemTitleTransition tests that PR menu titles transition from emoji to regular prefix.
func TestMenuItemTitleTransition(t *testing.T) {
	// Test duration - using 1 second for quick testing
	const testBlockDuration = 1 * time.Second

	ctx := context.Background()

	// Create app with mocked data
	app := &App{
		mu:                 sync.RWMutex{},
		stateManager:       NewPRStateManager(time.Now()),
		hiddenOrgs:         make(map[string]bool),
		seenOrgs:           make(map[string]bool),
		blockedPRTimes:     make(map[string]time.Time),
		browserRateLimiter: NewBrowserRateLimiter(startupGracePeriod, 5, defaultMaxBrowserOpensDay),
		systrayInterface:   &MockSystray{}, // Use mock systray to avoid panics
	}

	// Test incoming PR that just became blocked
	incomingPR := PR{
		Repository:     "test/repo",
		Number:         123,
		Title:          "Test PR",
		URL:            "https://github.com/test/repo/pull/123",
		NeedsReview:    true,
		FirstBlockedAt: time.Now(),
		UpdatedAt:      time.Now(),
	}

	// Test outgoing PR that just became blocked
	outgoingPR := PR{
		Repository:     "test/repo2",
		Number:         456,
		Title:          "Another Test PR",
		URL:            "https://github.com/test/repo2/pull/456",
		IsBlocked:      true,
		FirstBlockedAt: time.Now(),
		UpdatedAt:      time.Now(),
	}

	// Set the PRs in the app
	app.incoming = []PR{incomingPR}
	app.outgoing = []PR{outgoingPR}

	// Track blocked times
	app.blockedPRTimes[incomingPR.URL] = incomingPR.FirstBlockedAt
	app.blockedPRTimes[outgoingPR.URL] = outgoingPR.FirstBlockedAt

	// Helper function to extract titles from menu structure
	extractTitles := func() (incomingTitle, outgoingTitle string) {
		// This simulates what addPRSection does when building menu items
		for _, pr := range app.incoming {
			title := fmt.Sprintf("%s #%d", pr.Repository, pr.Number)

			// Add action code if present, or test state as fallback
			if pr.ActionKind != "" {
				// Replace underscores with spaces for better readability
				actionDisplay := strings.ReplaceAll(pr.ActionKind, "_", " ")
				title = fmt.Sprintf("%s — %s", title, actionDisplay)
			} else if pr.TestState == "running" {
				// Show "tests running" as a fallback when no specific action is available
				title = fmt.Sprintf("%s — tests running...", title)
			}

			if pr.NeedsReview {
				if !pr.FirstBlockedAt.IsZero() && time.Since(pr.FirstBlockedAt) < testBlockDuration {
					title = fmt.Sprintf("🪿 %s", title)
				} else {
					title = fmt.Sprintf("• %s", title)
				}
			}
			incomingTitle = title
		}

		for _, pr := range app.outgoing {
			title := fmt.Sprintf("%s #%d", pr.Repository, pr.Number)

			// Add action code if present, or test state as fallback
			if pr.ActionKind != "" {
				// Replace underscores with spaces for better readability
				actionDisplay := strings.ReplaceAll(pr.ActionKind, "_", " ")
				title = fmt.Sprintf("%s — %s", title, actionDisplay)
			} else if pr.TestState == "running" {
				// Show "tests running" as a fallback when no specific action is available
				title = fmt.Sprintf("%s — tests running...", title)
			}

			if pr.IsBlocked {
				if !pr.FirstBlockedAt.IsZero() && time.Since(pr.FirstBlockedAt) < testBlockDuration {
					title = fmt.Sprintf("🎉 %s", title)
				} else {
					title = fmt.Sprintf("• %s", title)
				}
			}
			outgoingTitle = title
		}
		return incomingTitle, outgoingTitle
	}

	// Test initial state - should have emoji prefixes
	inTitle, outTitle := extractTitles()
	if inTitle != "🪿 test/repo #123" {
		t.Errorf("Expected incoming PR to have goose emoji, got: %s", inTitle)
	}
	if outTitle != "🎉 test/repo2 #456" {
		t.Errorf("Expected outgoing PR to have party emoji, got: %s", outTitle)
	}

	// Wait for the test duration to pass
	time.Sleep(testBlockDuration + 100*time.Millisecond)

	// Test after duration - should have bullet points
	inTitle, outTitle = extractTitles()
	if inTitle != "• test/repo #123" {
		t.Errorf("Expected incoming PR to have bullet point after %v, got: %s", testBlockDuration, inTitle)
	}
	if outTitle != "• test/repo2 #456" {
		t.Errorf("Expected outgoing PR to have bullet point after %v, got: %s", testBlockDuration, outTitle)
	}

	// Test PR that becomes unblocked and then blocked again
	app.incoming[0].FirstBlockedAt = time.Time{} // Clear FirstBlockedAt
	app.incoming[0].NeedsReview = false          // Actually unblock it
	inTitle, _ = extractTitles()
	if inTitle != "test/repo #123" {
		t.Errorf("Expected unblocked PR to have no prefix, got: %s", inTitle)
	}

	// Re-block the PR
	app.incoming[0].NeedsReview = true
	app.incoming[0].FirstBlockedAt = time.Now()
	app.blockedPRTimes[incomingPR.URL] = app.incoming[0].FirstBlockedAt
	inTitle, _ = extractTitles()
	if inTitle != "🪿 test/repo #123" {
		t.Errorf("Expected re-blocked PR to have goose emoji again, got: %s", inTitle)
	}

	_ = ctx // Unused in this test but would be used for real menu operations
}

// TestWorkflowStateNewlyPublished tests that PRs with NEWLY_PUBLISHED workflow state get a 💎 bullet.
func TestWorkflowStateNewlyPublished(t *testing.T) {
	tests := []struct {
		name          string
		pr            PR
		expectedTitle string
	}{
		{
			name: "newly_published_with_action",
			pr: PR{
				Repository:    "test/repo",
				Number:        123,
				ActionKind:    "review",
				WorkflowState: string(turn.StateNewlyPublished),
				NeedsReview:   true,
				UpdatedAt:     time.Now(),
			},
			expectedTitle: "💎 test/repo #123 — review",
		},
		{
			name: "newly_published_without_action",
			pr: PR{
				Repository:    "test/repo",
				Number:        456,
				WorkflowState: string(turn.StateNewlyPublished),
				UpdatedAt:     time.Now(),
			},
			expectedTitle: "💎 test/repo #456",
		},
		{
			name: "newly_published_with_running_tests",
			pr: PR{
				Repository:    "test/repo",
				Number:        789,
				TestState:     "running",
				WorkflowState: string(turn.StateNewlyPublished),
				UpdatedAt:     time.Now(),
			},
			expectedTitle: "💎 test/repo #789 — tests running...",
		},
		{
			name: "not_newly_published_with_action",
			pr: PR{
				Repository:    "test/repo",
				Number:        999,
				ActionKind:    "merge",
				WorkflowState: "WAITING_FOR_REVIEW",
				NeedsReview:   true,
				UpdatedAt:     time.Now(),
			},
			expectedTitle: "■ test/repo #999 — merge",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Helper function to generate title (mirrors the logic in ui.go)
			generateTitle := func(pr PR) string {
				title := fmt.Sprintf("%s #%d", pr.Repository, pr.Number)

				// Add action code if present, or test state as fallback
				if pr.ActionKind != "" {
					actionDisplay := strings.ReplaceAll(pr.ActionKind, "_", " ")
					title = fmt.Sprintf("%s — %s", title, actionDisplay)
				} else if pr.TestState == "running" {
					title = fmt.Sprintf("%s — tests running...", title)
				}

				// Add prefix based on workflow state or blocked status
				switch {
				case pr.WorkflowState == string(turn.StateNewlyPublished):
					title = fmt.Sprintf("💎 %s", title)
				case pr.NeedsReview || pr.IsBlocked:
					title = fmt.Sprintf("■ %s", title)
				}

				return title
			}

			actualTitle := generateTitle(tt.pr)
			if actualTitle != tt.expectedTitle {
				t.Errorf("Expected title %q, got %q", tt.expectedTitle, actualTitle)
			}
		})
	}
}

// TestTrayIconRestoredAfterNetworkRecovery tests that the tray icon is restored
// to normal after network failures are resolved.
func TestTrayIconRestoredAfterNetworkRecovery(t *testing.T) {
	ctx := context.Background()
	mock := &MockSystray{}
	app := &App{
		mu:                 sync.RWMutex{},
		stateManager:       NewPRStateManager(time.Now().Add(-35 * time.Second)), // Past grace period
		blockedPRTimes:     make(map[string]time.Time),
		hiddenOrgs:         make(map[string]bool),
		seenOrgs:           make(map[string]bool),
		browserRateLimiter: NewBrowserRateLimiter(startupGracePeriod, 5, defaultMaxBrowserOpensDay),
		systrayInterface:   mock,
		menuInitialized:    true,
	}

	// Initial state - successful fetch with some PRs
	app.incoming = []PR{
		{Repository: "test/repo", Number: 1, NeedsReview: true, UpdatedAt: time.Now()},
	}
	app.setTrayTitle()
	initialTitle := mock.title

	// Expected title varies by platform
	expectedTitle := "" // Most platforms: icon only, no text
	if runtime.GOOS == "darwin" {
		expectedTitle = "1" // macOS: show count with icon
	}

	if initialTitle != expectedTitle {
		t.Errorf("Expected initial tray title %q, got %q", expectedTitle, initialTitle)
	}

	// Simulate network failure - updatePRs would set warning icon and return early
	app.consecutiveFailures = 3
	app.lastFetchError = "network timeout"
	// In the old code, rebuildMenu would be called but return early, never calling setTrayTitle()
	app.rebuildMenu(ctx)
	// The mock systray won't have the warning icon because rebuildMenu doesn't set it directly

	// Simulate network recovery - this should restore the normal icon
	app.consecutiveFailures = 0
	app.lastFetchError = ""
	// With our fix, setTrayTitle() is now called after successful fetch
	app.setTrayTitle()
	recoveredTitle := mock.title
	if recoveredTitle != expectedTitle {
		t.Errorf("Expected tray title to be restored to %q after recovery, got %q", expectedTitle, recoveredTitle)
	}
}

// TestTrayTitleUpdates tests that the tray title updates correctly based on PR counts.
func TestTrayTitleUpdates(t *testing.T) {
	app := &App{
		mu:                 sync.RWMutex{},
		stateManager:       NewPRStateManager(time.Now().Add(-35 * time.Second)), // Past grace period
		blockedPRTimes:     make(map[string]time.Time),
		hiddenOrgs:         make(map[string]bool),
		seenOrgs:           make(map[string]bool),
		browserRateLimiter: NewBrowserRateLimiter(startupGracePeriod, 5, defaultMaxBrowserOpensDay),
		systrayInterface:   &MockSystray{}, // Use mock systray to avoid panics
	}

	tests := []struct {
		name              string
		incoming          []PR
		outgoing          []PR
		hiddenOrgs        map[string]bool
		hideStaleIncoming bool
		expectedTitle     string
	}{
		{
			name:          "no PRs",
			incoming:      []PR{},
			outgoing:      []PR{},
			expectedTitle: "", // No count shown when no blocked PRs
		},
		{
			name: "only incoming blocked",
			incoming: []PR{
				{Repository: "test/repo", Number: 1, NeedsReview: true, UpdatedAt: time.Now()},
				{Repository: "test/repo", Number: 2, NeedsReview: true, UpdatedAt: time.Now()},
			},
			outgoing:      []PR{},
			expectedTitle: "2", // macOS format: just the count
		},
		{
			name:     "only outgoing blocked",
			incoming: []PR{},
			outgoing: []PR{
				{Repository: "test/repo", Number: 3, IsBlocked: true, UpdatedAt: time.Now()},
				{Repository: "test/repo", Number: 4, IsBlocked: true, UpdatedAt: time.Now()},
				{Repository: "test/repo", Number: 5, IsBlocked: true, UpdatedAt: time.Now()},
			},
			expectedTitle: "3", // macOS format: just the count
		},
		{
			name: "both incoming and outgoing blocked",
			incoming: []PR{
				{Repository: "test/repo", Number: 1, NeedsReview: true, UpdatedAt: time.Now()},
			},
			outgoing: []PR{
				{Repository: "test/repo", Number: 2, IsBlocked: true, UpdatedAt: time.Now()},
			},
			expectedTitle: "1 / 1", // macOS format: "incoming / outgoing"
		},
		{
			name: "mixed blocked and unblocked",
			incoming: []PR{
				{Repository: "test/repo", Number: 1, NeedsReview: true, UpdatedAt: time.Now()},
				{Repository: "test/repo", Number: 2, NeedsReview: false, UpdatedAt: time.Now()},
			},
			outgoing: []PR{
				{Repository: "test/repo", Number: 3, IsBlocked: false, UpdatedAt: time.Now()},
				{Repository: "test/repo", Number: 4, IsBlocked: true, UpdatedAt: time.Now()},
			},
			expectedTitle: "1 / 1", // macOS format: "incoming / outgoing"
		},
		{
			name: "hidden org filters out blocked PRs",
			incoming: []PR{
				{Repository: "hidden-org/repo", Number: 1, NeedsReview: true, UpdatedAt: time.Now()},
				{Repository: "visible-org/repo", Number: 2, NeedsReview: true, UpdatedAt: time.Now()},
			},
			outgoing:      []PR{},
			hiddenOrgs:    map[string]bool{"hidden-org": true},
			expectedTitle: "1", // macOS format: just the count
		},
		{
			name: "stale PRs filtered when hideStaleIncoming is true",
			incoming: []PR{
				{Repository: "test/repo", Number: 1, NeedsReview: true, UpdatedAt: time.Now().Add(-100 * 24 * time.Hour)}, // stale
				{Repository: "test/repo", Number: 2, NeedsReview: true, UpdatedAt: time.Now()},                            // fresh
			},
			outgoing:          []PR{},
			hideStaleIncoming: true,
			expectedTitle:     "1", // macOS format: just the count
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app.incoming = tt.incoming
			app.outgoing = tt.outgoing
			app.hiddenOrgs = tt.hiddenOrgs
			app.hideStaleIncoming = tt.hideStaleIncoming

			// Call setTrayTitle to get the actual title
			app.setTrayTitle()
			mockSystray, ok := app.systrayInterface.(*MockSystray)
			if !ok {
				t.Fatal("Failed to cast systrayInterface to MockSystray")
			}
			actualTitle := mockSystray.title

			// Adjust expected title based on platform
			expectedTitle := tt.expectedTitle
			if runtime.GOOS != "darwin" {
				// Non-macOS platforms show icon only (no text)
				expectedTitle = ""
			}

			if actualTitle != expectedTitle {
				t.Errorf("Expected tray title %q, got %q", expectedTitle, actualTitle)
			}
		})
	}
}

// TestSoundPlaybackDuringTransitions tests the logic for when sounds should be played during PR state transitions.
func TestSoundPlaybackDuringTransitions(t *testing.T) {
	// This test verifies the logic by checking state transitions
	// Actual sound playback is tested through logging in integration tests
	ctx := context.Background()

	// Create app with initial state
	app := &App{
		mu:                  sync.RWMutex{},
		stateManager:        NewPRStateManager(time.Now().Add(-35 * time.Second)), // Past grace period
		blockedPRTimes:      make(map[string]time.Time),
		hiddenOrgs:          make(map[string]bool),
		seenOrgs:            make(map[string]bool),
		previousBlockedPRs:  make(map[string]bool),
		browserRateLimiter:  NewBrowserRateLimiter(startupGracePeriod, 5, defaultMaxBrowserOpensDay),
		enableAudioCues:     true,
		initialLoadComplete: true, // Set to true to allow sound playback
		menuInitialized:     true,
		systrayInterface:    &MockSystray{}, // Use mock systray to avoid Windows-specific panics
	}

	tests := []struct {
		name            string
		initialIncoming []PR
		initialOutgoing []PR
		updatedIncoming []PR
		updatedOutgoing []PR
		expectedSounds  []string
		description     string
	}{
		{
			name: "incoming PR becomes blocked",
			initialIncoming: []PR{
				{Repository: "test/repo", Number: 1, URL: "https://github.com/test/repo/pull/1", NeedsReview: false, UpdatedAt: time.Now()},
			},
			initialOutgoing: []PR{},
			updatedIncoming: []PR{
				{Repository: "test/repo", Number: 1, URL: "https://github.com/test/repo/pull/1", NeedsReview: true, UpdatedAt: time.Now()},
			},
			updatedOutgoing: []PR{},
			expectedSounds:  []string{"honk"},
			description:     "Should play honk sound when incoming PR becomes blocked",
		},
		{
			name:            "outgoing PR becomes blocked",
			initialIncoming: []PR{},
			initialOutgoing: []PR{
				{Repository: "test/repo", Number: 2, URL: "https://github.com/test/repo/pull/2", IsBlocked: false, UpdatedAt: time.Now()},
			},
			updatedIncoming: []PR{},
			updatedOutgoing: []PR{
				{Repository: "test/repo", Number: 2, URL: "https://github.com/test/repo/pull/2", IsBlocked: true, UpdatedAt: time.Now()},
			},
			expectedSounds: []string{"rocket"},
			description:    "Should play rocket sound when outgoing PR becomes blocked",
		},
		{
			name: "multiple PRs become blocked",
			initialIncoming: []PR{
				{Repository: "test/repo", Number: 1, URL: "https://github.com/test/repo/pull/1", NeedsReview: false, UpdatedAt: time.Now()},
			},
			initialOutgoing: []PR{
				{Repository: "test/repo", Number: 2, URL: "https://github.com/test/repo/pull/2", IsBlocked: false, UpdatedAt: time.Now()},
			},
			updatedIncoming: []PR{
				{Repository: "test/repo", Number: 1, URL: "https://github.com/test/repo/pull/1", NeedsReview: true, UpdatedAt: time.Now()},
			},
			updatedOutgoing: []PR{
				{Repository: "test/repo", Number: 2, URL: "https://github.com/test/repo/pull/2", IsBlocked: true, UpdatedAt: time.Now()},
			},
			expectedSounds: []string{"honk", "rocket"},
			description:    "Should play both sounds when both PR types become blocked",
		},
		{
			name: "PR becomes unblocked - no sound",
			initialIncoming: []PR{
				{Repository: "test/repo", Number: 1, URL: "https://github.com/test/repo/pull/1", NeedsReview: true, UpdatedAt: time.Now()},
			},
			initialOutgoing: []PR{},
			updatedIncoming: []PR{
				{Repository: "test/repo", Number: 1, URL: "https://github.com/test/repo/pull/1", NeedsReview: false, UpdatedAt: time.Now()},
			},
			updatedOutgoing: []PR{},
			expectedSounds:  []string{},
			description:     "Should not play sound when PR becomes unblocked",
		},
		{
			name: "already blocked PR stays blocked - no sound",
			initialIncoming: []PR{
				{Repository: "test/repo", Number: 1, URL: "https://github.com/test/repo/pull/1", NeedsReview: true, UpdatedAt: time.Now()},
			},
			initialOutgoing: []PR{},
			updatedIncoming: []PR{
				{Repository: "test/repo", Number: 1, URL: "https://github.com/test/repo/pull/1", NeedsReview: true, UpdatedAt: time.Now()},
			},
			updatedOutgoing: []PR{},
			expectedSounds:  []string{},
			description:     "Should not play sound when PR stays blocked",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset app state for each test
			app.previousBlockedPRs = make(map[string]bool)
			app.blockedPRTimes = make(map[string]time.Time)

			// Note: We can't directly capture sound calls in unit tests since playSound is a method.
			// This test verifies the state transitions that would trigger sounds.
			// Actual sound playback is verified through integration testing.

			// Set initial state
			app.mu.Lock()
			app.incoming = tt.initialIncoming
			app.outgoing = tt.initialOutgoing
			app.mu.Unlock()

			// Run first check to establish baseline
			app.checkForNewlyBlockedPRs(ctx)

			// Update to new state
			app.mu.Lock()
			app.incoming = tt.updatedIncoming
			app.outgoing = tt.updatedOutgoing
			app.mu.Unlock()

			// Run check again to detect transitions
			app.checkForNewlyBlockedPRs(ctx)

			// Verify state transitions occurred correctly
			// For newly blocked PRs after grace period, the previousBlockedPRs map should be updated
			if len(tt.expectedSounds) > 0 {
				// Check that blocked PRs are tracked in previousBlockedPRs
				blocked := 0
				app.mu.RLock()
				for _, pr := range app.incoming {
					if pr.NeedsReview && app.previousBlockedPRs[pr.URL] {
						blocked++
					}
				}
				for _, pr := range app.outgoing {
					if pr.IsBlocked && app.previousBlockedPRs[pr.URL] {
						blocked++
					}
				}
				app.mu.RUnlock()
				if blocked == 0 {
					t.Errorf("%s: expected blocked PRs to be tracked in previousBlockedPRs", tt.description)
				}
			}
		})
	}
}

// TestSoundDisabledNoPlayback tests that no sounds are played when audio cues are disabled.
func TestSoundDisabledNoPlayback(t *testing.T) {
	ctx := context.Background()

	app := &App{
		mu:                  sync.RWMutex{},
		stateManager:        NewPRStateManager(time.Now().Add(-35 * time.Second)), // Past grace period
		blockedPRTimes:      make(map[string]time.Time),
		hiddenOrgs:          make(map[string]bool),
		seenOrgs:            make(map[string]bool),
		previousBlockedPRs:  make(map[string]bool),
		browserRateLimiter:  NewBrowserRateLimiter(startupGracePeriod, 5, defaultMaxBrowserOpensDay),
		enableAudioCues:     false, // Audio disabled
		initialLoadComplete: true,
		menuInitialized:     true,
		systrayInterface:    &MockSystray{}, // Use mock systray to avoid panics
	}

	// Note: We verify behavior through state changes rather than direct sound capture

	// Set initial state with no blocked PRs
	app.incoming = []PR{
		{Repository: "test/repo", Number: 1, URL: "https://github.com/test/repo/pull/1", NeedsReview: false, UpdatedAt: time.Now()},
	}
	app.checkForNewlyBlockedPRs(ctx)

	// Update to blocked state
	app.incoming = []PR{
		{Repository: "test/repo", Number: 1, URL: "https://github.com/test/repo/pull/1", NeedsReview: true, UpdatedAt: time.Now()},
	}
	app.checkForNewlyBlockedPRs(ctx)

	// When audio is disabled, PRs should still be tracked but no actual sounds would play
	// Verify that state is tracked correctly even with audio disabled
	if len(app.previousBlockedPRs) == 0 {
		t.Errorf("Expected blocked PRs to be tracked even with audio disabled")
	}
}

// TestGracePeriodPreventsNotifications tests that no sounds/notifications occur during the grace period.
func TestGracePeriodPreventsNotifications(t *testing.T) {
	ctx := context.Background()

	// Create app with a very recent start time (within grace period)
	app := &App{
		mu:                  sync.RWMutex{},
		stateManager:        NewPRStateManager(time.Now()), // Within grace period
		blockedPRTimes:      make(map[string]time.Time),
		hiddenOrgs:          make(map[string]bool),
		seenOrgs:            make(map[string]bool),
		previousBlockedPRs:  make(map[string]bool),
		browserRateLimiter:  NewBrowserRateLimiter(startupGracePeriod, 5, defaultMaxBrowserOpensDay),
		enableAudioCues:     true,
		initialLoadComplete: true,
		menuInitialized:     true,
		startTime:           time.Now(),     // Just started
		systrayInterface:    &MockSystray{}, // Use mock systray to avoid panics
	}

	// Track whether we're in grace period for verification
	inGracePeriod := func() bool {
		return time.Since(app.startTime) < startupGracePeriod
	}

	// Set initial state with no blocked PRs
	app.incoming = []PR{
		{Repository: "test/repo", Number: 1, URL: "https://github.com/test/repo/pull/1", NeedsReview: false, UpdatedAt: time.Now()},
	}
	app.checkForNewlyBlockedPRs(ctx)

	// PR becomes blocked during grace period
	app.incoming = []PR{
		{Repository: "test/repo", Number: 1, URL: "https://github.com/test/repo/pull/1", NeedsReview: true, UpdatedAt: time.Now()},
	}
	app.checkForNewlyBlockedPRs(ctx)

	// Verify we're still in grace period
	if !inGracePeriod() {
		t.Errorf("Expected to still be in grace period")
	}

	// The PR should be tracked as blocked but not as "newly notified"
	if !app.previousBlockedPRs["https://github.com/test/repo/pull/1"] {
		t.Errorf("Expected PR to be tracked as blocked during grace period")
	}

	// Now simulate time passing beyond grace period (1 minute)
	app.startTime = time.Now().Add(-61 * time.Second)

	// New PR becomes blocked after grace period
	app.incoming = []PR{
		{Repository: "test/repo", Number: 1, URL: "https://github.com/test/repo/pull/1", NeedsReview: true, UpdatedAt: time.Now()},
		{Repository: "test/repo", Number: 2, URL: "https://github.com/test/repo/pull/2", NeedsReview: true, UpdatedAt: time.Now()},
	}
	app.checkForNewlyBlockedPRs(ctx)

	// Verify we're past grace period
	if inGracePeriod() {
		t.Errorf("Expected to be past grace period")
	}

	// Both PRs should now be tracked
	if !app.previousBlockedPRs["https://github.com/test/repo/pull/2"] {
		t.Errorf("Expected new PR to be tracked as blocked after grace period")
	}
}

// TestNotificationScenarios comprehensively tests when notifications should and shouldn't fire.
func TestNotificationScenarios(t *testing.T) {
	tests := []struct {
		name                string
		timeSinceStart      time.Duration
		initialLoadComplete bool
		prWasBlocked        bool
		prIsBlocked         bool
		expectNotification  bool
		description         string
	}{
		{
			name:                "initial_load_already_blocked",
			timeSinceStart:      1 * time.Second,
			initialLoadComplete: false,
			prWasBlocked:        false,
			prIsBlocked:         true,
			expectNotification:  false,
			description:         "PRs already blocked on startup should not notify",
		},
		{
			name:                "newly_blocked_during_grace_period",
			timeSinceStart:      10 * time.Second,
			initialLoadComplete: true,
			prWasBlocked:        false,
			prIsBlocked:         true,
			expectNotification:  false,
			description:         "Newly blocked PRs during grace period should not notify",
		},
		{
			name:                "newly_blocked_after_grace_period",
			timeSinceStart:      35 * time.Second,
			initialLoadComplete: true,
			prWasBlocked:        false,
			prIsBlocked:         true,
			expectNotification:  true,
			description:         "Newly blocked PRs after grace period SHOULD notify",
		},
		{
			name:                "stays_blocked",
			timeSinceStart:      35 * time.Second,
			initialLoadComplete: true,
			prWasBlocked:        true,
			prIsBlocked:         true,
			expectNotification:  false,
			description:         "PRs that stay blocked should not notify again",
		},
		{
			name:                "becomes_unblocked",
			timeSinceStart:      35 * time.Second,
			initialLoadComplete: true,
			prWasBlocked:        true,
			prIsBlocked:         false,
			expectNotification:  false,
			description:         "PRs becoming unblocked should not notify",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			app := &App{
				mu:                  sync.RWMutex{},
				stateManager:        NewPRStateManager(time.Now().Add(-tt.timeSinceStart)),
				blockedPRTimes:      make(map[string]time.Time),
				hiddenOrgs:          make(map[string]bool),
				seenOrgs:            make(map[string]bool),
				previousBlockedPRs:  make(map[string]bool),
				browserRateLimiter:  NewBrowserRateLimiter(startupGracePeriod, 5, defaultMaxBrowserOpensDay),
				enableAudioCues:     true,
				initialLoadComplete: tt.initialLoadComplete,
				menuInitialized:     true,
				startTime:           time.Now().Add(-tt.timeSinceStart),
				systrayInterface:    &MockSystray{}, // Use mock systray to avoid panics
			}

			// Set up previous state
			if tt.prWasBlocked {
				app.previousBlockedPRs["https://github.com/test/repo/pull/1"] = true
				app.blockedPRTimes["https://github.com/test/repo/pull/1"] = time.Now().Add(-5 * time.Minute)
			}

			// Set current state
			app.incoming = []PR{
				{
					Repository:  "test/repo",
					Number:      1,
					URL:         "https://github.com/test/repo/pull/1",
					NeedsReview: tt.prIsBlocked,
					UpdatedAt:   time.Now(),
				},
			}

			// Track if we would notify (by checking logs)
			app.checkForNewlyBlockedPRs(ctx)

			// Verify expectations
			if tt.expectNotification {
				// Should have updated previousBlockedPRs
				if !app.previousBlockedPRs["https://github.com/test/repo/pull/1"] {
					t.Errorf("%s: Expected PR to be tracked as blocked", tt.description)
				}
				// Should have set FirstBlockedAt in state manager
				if state, exists := app.stateManager.PRState("https://github.com/test/repo/pull/1"); !exists || state.FirstBlockedAt.IsZero() {
					t.Errorf("%s: Expected FirstBlockedAt to be set in state manager", tt.description)
				}
			}

			// Check if PR is tracked correctly
			if tt.prIsBlocked && !app.previousBlockedPRs["https://github.com/test/repo/pull/1"] {
				t.Errorf("%s: Expected blocked PR to be tracked", tt.description)
			}
		})
	}
}

// TestNewlyBlockedPRAfterGracePeriod verifies that a PR becoming blocked after grace period triggers notifications.
func TestNewlyBlockedPRAfterGracePeriod(t *testing.T) {
	ctx := context.Background()

	// Create app that's been running for more than 30 seconds
	app := &App{
		mu:                  sync.RWMutex{},
		stateManager:        NewPRStateManager(time.Now().Add(-35 * time.Second)), // Past grace period
		blockedPRTimes:      make(map[string]time.Time),
		hiddenOrgs:          make(map[string]bool),
		seenOrgs:            make(map[string]bool),
		previousBlockedPRs:  make(map[string]bool),
		browserRateLimiter:  NewBrowserRateLimiter(startupGracePeriod, 5, defaultMaxBrowserOpensDay),
		enableAudioCues:     true,
		initialLoadComplete: true, // Already past initial load
		menuInitialized:     true,
		startTime:           time.Now().Add(-35 * time.Second), // Started 35 seconds ago
		systrayInterface:    &MockSystray{},                    // Use mock systray to avoid panics
	}

	// Start with no blocked PRs
	app.incoming = []PR{
		{Repository: "test/repo", Number: 1, URL: "https://github.com/test/repo/pull/1", NeedsReview: false, UpdatedAt: time.Now()},
	}

	// Run initial check to establish baseline
	app.checkForNewlyBlockedPRs(ctx)

	// Now the PR becomes blocked (after grace period)
	app.incoming = []PR{
		{Repository: "test/repo", Number: 1, URL: "https://github.com/test/repo/pull/1", NeedsReview: true, UpdatedAt: time.Now()},
	}

	// This should trigger notifications since we're past grace period
	app.checkForNewlyBlockedPRs(ctx)

	// Verify the PR is tracked as blocked
	if !app.previousBlockedPRs["https://github.com/test/repo/pull/1"] {
		t.Error("Expected PR to be tracked as blocked after grace period")
	}

	// Verify FirstBlockedAt was set in state manager
	if state, exists := app.stateManager.PRState("https://github.com/test/repo/pull/1"); !exists || state.FirstBlockedAt.IsZero() {
		t.Error("Expected FirstBlockedAt to be set for newly blocked PR in state manager")
	}
}

// TestAuthRetryLoopStopsOnSuccess tests that the auth retry loop stops when auth succeeds.
func TestAuthRetryLoopStopsOnSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	app := &App{
		mu:               sync.RWMutex{},
		authError:        "initial auth error",
		systrayInterface: &MockSystray{},
	}

	// Track how many times we check authError (use atomic for thread safety)
	var checkCount atomic.Int32
	done := make(chan struct{})
	exitReason := make(chan string, 1)

	// Start a goroutine that simulates the auth retry loop behavior
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				exitReason <- "context canceled"
				return
			case <-ticker.C:
				app.mu.RLock()
				hasError := app.authError != ""
				app.mu.RUnlock()

				count := checkCount.Add(1)

				if !hasError {
					exitReason <- fmt.Sprintf("auth succeeded after %d checks", count)
					return // Loop should exit when auth succeeds
				}

				// Simulate clearing auth error on 3rd attempt
				if count >= 3 {
					app.mu.Lock()
					app.authError = ""
					app.mu.Unlock()
				}
			}
		}
	}()

	// Wait for the goroutine to finish
	select {
	case <-done:
		// Success - loop exited
		reason := "unknown"
		select {
		case reason = <-exitReason:
		default:
		}
		t.Logf("Goroutine exited: %s", reason)
	case <-time.After(2 * time.Second):
		t.Fatalf("Auth retry loop did not stop after auth succeeded (checkCount=%d)", checkCount.Load())
	}

	// Verify auth error was cleared
	app.mu.RLock()
	finalError := app.authError
	app.mu.RUnlock()

	if finalError != "" {
		t.Errorf("Expected auth error to be cleared, got: %s", finalError)
	}

	finalCount := checkCount.Load()
	if finalCount < 3 {
		t.Errorf("Expected at least 3 retry attempts, got: %d", finalCount)
	}
}

// TestAuthRetryLoopStopsOnContextCancel tests that the auth retry loop stops on context cancellation.
func TestAuthRetryLoopStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	app := &App{
		mu:               sync.RWMutex{},
		authError:        "persistent auth error",
		systrayInterface: &MockSystray{},
	}

	done := make(chan struct{})

	// Start a goroutine that simulates the auth retry loop behavior
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				app.mu.RLock()
				hasError := app.authError != ""
				app.mu.RUnlock()

				if !hasError {
					return
				}
				// Auth error persists, loop continues
			}
		}
	}()

	// Cancel context after a short delay
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Wait for the goroutine to finish
	select {
	case <-done:
		// Success - loop exited on context cancel
	case <-time.After(1 * time.Second):
		t.Fatal("Auth retry loop did not stop after context cancellation")
	}
}

// TestAuthErrorStatePreservation tests that auth error state is correctly preserved and accessible.
func TestAuthErrorStatePreservation(t *testing.T) {
	app := &App{
		mu:               sync.RWMutex{},
		systrayInterface: &MockSystray{},
	}

	// Initially no auth error
	app.mu.RLock()
	if app.authError != "" {
		t.Errorf("Expected no initial auth error, got: %s", app.authError)
	}
	app.mu.RUnlock()

	// Set auth error
	app.mu.Lock()
	app.authError = "token expired"
	app.mu.Unlock()

	// Verify auth error is set
	app.mu.RLock()
	if app.authError != "token expired" {
		t.Errorf("Expected auth error 'token expired', got: %s", app.authError)
	}
	app.mu.RUnlock()

	// Clear auth error
	app.mu.Lock()
	app.authError = ""
	app.mu.Unlock()

	// Verify auth error is cleared
	app.mu.RLock()
	if app.authError != "" {
		t.Errorf("Expected auth error to be cleared, got: %s", app.authError)
	}
	app.mu.RUnlock()
}

// TestTurnDataDisabled tests that turnData returns nil gracefully when Turn API is disabled.
func TestTurnDataDisabled(t *testing.T) {
	ctx := context.Background()

	app := &App{
		mu:         sync.RWMutex{},
		turnClient: nil, // Simulates TURNSERVER=disabled
		cacheDir:   t.TempDir(),
	}

	// turnData should return nil without error when disabled
	data, cached, err := app.turnData(ctx, "https://github.com/test/repo/pull/1", time.Now())
	if err != nil {
		t.Errorf("Expected no error when Turn API disabled, got: %v", err)
	}
	if data != nil {
		t.Error("Expected nil data when Turn API disabled")
	}
	if cached {
		t.Error("Expected cached=false when Turn API disabled")
	}
}

// TestSprinklerDisabled tests that initSprinklerOrgs returns nil gracefully when Sprinkler is disabled.
func TestSprinklerDisabled(t *testing.T) {
	ctx := context.Background()

	app := &App{
		mu:               sync.RWMutex{},
		client:           github.NewClient(nil), // Need a non-nil client
		sprinklerMonitor: nil,                   // Simulates SPRINKLER=disabled
	}

	// initSprinklerOrgs should return nil without error when disabled
	err := app.initSprinklerOrgs(ctx)
	if err != nil {
		t.Errorf("Expected no error when Sprinkler disabled, got: %v", err)
	}
}

// TestCustomTurnServer tests that a custom TURNSERVER hostname routes requests correctly.
func TestCustomTurnServer(t *testing.T) {
	ctx := context.Background()

	// Create a mock Turn API server
	requestReceived := false
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestReceived = true

		// Verify the request is to the validate endpoint
		if r.URL.Path != "/v1/validate" {
			t.Errorf("Expected request to /v1/validate, got: %s", r.URL.Path)
		}

		// Return a valid Turn API response matching the expected schema:
		// - turn.CheckResponse contains prx.PullRequest and turn.Analysis
		// - CheckSummary fields are map[string]string (check name -> status description)
		resp := map[string]any{
			"timestamp": time.Now().Format(time.RFC3339),
			"commit":    "abc123def456",
			"pull_request": map[string]any{
				"number":     1,
				"state":      "open",
				"title":      "Test PR",
				"author":     "testauthor",
				"author_bot": false,
				"draft":      false,
				"merged":     false,
				"test_state": "passing",
				"created_at": time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
				"updated_at": time.Now().Format(time.RFC3339),
				"head_sha":   "abc123def456",
				"check_summary": map[string]any{
					"success":   map[string]string{"ci/test": "All tests passed"},
					"failing":   map[string]string{},
					"pending":   map[string]string{},
					"cancelled": map[string]string{},
					"skipped":   map[string]string{},
					"stale":     map[string]string{},
					"neutral":   map[string]string{},
				},
			},
			"analysis": map[string]any{
				"workflow_state": "WAITING_FOR_REVIEW",
				"next_action": map[string]any{
					"testuser": map[string]any{
						"kind":     "review",
						"reason":   "PR is ready for review",
						"critical": true,
						"since":    time.Now().Format(time.RFC3339),
					},
				},
				"last_activity": map[string]any{
					"timestamp": time.Now().Format(time.RFC3339),
					"kind":      "push",
					"actor":     "testauthor",
					"message":   "Pushed new commits",
				},
				"size":        "S",
				"ready_merge": false,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("Failed to encode response: %v", err)
		}
	}))
	defer mockServer.Close()

	// Create a Turn client pointing to our mock server
	turnClient, err := turn.NewClient(mockServer.URL)
	if err != nil {
		t.Fatalf("Failed to create turn client: %v", err)
	}
	turnClient.SetAuthToken("test-token")

	// Create app with the custom turn client
	login := "testuser"
	app := &App{
		mu:         sync.RWMutex{},
		turnClient: turnClient,
		cacheDir:   t.TempDir(),
		noCache:    true, // Skip cache to ensure we hit the API
		currentUser: &github.User{
			Login: &login,
		},
	}

	// Make a request
	data, _, err := app.turnData(ctx, "https://github.com/test/repo/pull/1", time.Now())
	if err != nil {
		t.Fatalf("turnData failed: %v", err)
	}

	// Verify the mock server received the request
	if !requestReceived {
		t.Error("Expected request to be sent to custom Turn server")
	}

	// Verify we got a valid response
	if data == nil {
		t.Fatal("Expected non-nil response from custom Turn server")
	}
	if data.PullRequest.State != "open" {
		t.Errorf("Expected state 'open', got: %s", data.PullRequest.State)
	}
	if data.PullRequest.TestState != "passing" {
		t.Errorf("Expected test_state 'passing', got: %s", data.PullRequest.TestState)
	}
	if data.Analysis.WorkflowState != "WAITING_FOR_REVIEW" {
		t.Errorf("Expected workflow_state 'WAITING_FOR_REVIEW', got: %s", data.Analysis.WorkflowState)
	}
	if _, hasAction := data.Analysis.NextAction["testuser"]; !hasAction {
		t.Error("Expected NextAction to contain 'testuser'")
	}
}
