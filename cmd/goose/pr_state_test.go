package main

import (
	"testing"
	"time"
)

func TestPRStateManager(t *testing.T) {
	// Create a manager with a start time in the past (past grace period)
	mgr := NewPRStateManager(time.Now().Add(-60 * time.Second))

	// Test 1: New blocked PR after grace period should notify
	pr1 := PR{
		Repository:  "test/repo",
		Number:      1,
		URL:         "https://github.com/test/repo/pull/1",
		NeedsReview: true,
		UpdatedAt:   time.Now(), // Recently updated PR
	}

	toNotify := mgr.UpdatePRs([]PR{pr1}, []PR{}, map[string]bool{}, false)
	if len(toNotify) != 1 {
		t.Errorf("Expected 1 PR to notify, got %d", len(toNotify))
	}

	// Test 2: Same PR on next update should not notify again
	toNotify = mgr.UpdatePRs([]PR{pr1}, []PR{}, map[string]bool{}, false)
	if len(toNotify) != 0 {
		t.Errorf("Expected 0 PRs to notify (already notified), got %d", len(toNotify))
	}

	// Test 3: PR becomes unblocked
	pr1.NeedsReview = false
	toNotify = mgr.UpdatePRs([]PR{pr1}, []PR{}, map[string]bool{}, false)
	if len(toNotify) != 0 {
		t.Errorf("Expected 0 PRs to notify (unblocked), got %d", len(toNotify))
	}

	// Verify state was removed
	if _, exists := mgr.PRState(pr1.URL); exists {
		t.Error("Expected PR state to be removed when unblocked")
	}

	// Test 4: PR becomes blocked again - should notify
	pr1.NeedsReview = true
	toNotify = mgr.UpdatePRs([]PR{pr1}, []PR{}, map[string]bool{}, false)
	if len(toNotify) != 1 {
		t.Errorf("Expected 1 PR to notify (re-blocked), got %d", len(toNotify))
	}
}

func TestPRStateManagerGracePeriod(t *testing.T) {
	// Create a manager with recent start time (within grace period)
	mgr := NewPRStateManager(time.Now().Add(-5 * time.Second))

	// New blocked PR during grace period should NOT notify
	pr1 := PR{
		Repository:  "test/repo",
		Number:      1,
		URL:         "https://github.com/test/repo/pull/1",
		NeedsReview: true,
		UpdatedAt:   time.Now(), // Recently updated PR
	}

	toNotify := mgr.UpdatePRs([]PR{pr1}, []PR{}, map[string]bool{}, false)
	if len(toNotify) != 0 {
		t.Errorf("Expected 0 PRs to notify during grace period, got %d", len(toNotify))
	}

	// Verify state is still tracked
	if _, exists := mgr.PRState(pr1.URL); !exists {
		t.Error("Expected PR state to be tracked even during grace period")
	}

	// Simulate time passing past grace period
	mgr.startTime = time.Now().Add(-60 * time.Second)

	// Same PR should now notify since we're past grace period and haven't notified yet
	toNotify = mgr.UpdatePRs([]PR{pr1}, []PR{}, map[string]bool{}, false)
	if len(toNotify) != 1 {
		t.Errorf("Expected 1 PR to notify after grace period, got %d", len(toNotify))
	}
}

func TestPRStateManagerHiddenOrgs(t *testing.T) {
	mgr := NewPRStateManager(time.Now().Add(-60 * time.Second))

	pr1 := PR{
		Repository: "hidden-org/repo",
		Number:     1,
		URL:        "https://github.com/hidden-org/repo/pull/1",
		IsBlocked:  true,
		UpdatedAt:  time.Now(), // Recently updated PR
	}

	pr2 := PR{
		Repository: "visible-org/repo",
		Number:     2,
		URL:        "https://github.com/visible-org/repo/pull/2",
		IsBlocked:  true,
		UpdatedAt:  time.Now(), // Recently updated PR
	}

	hiddenOrgs := map[string]bool{
		"hidden-org": true,
	}

	toNotify := mgr.UpdatePRs([]PR{}, []PR{pr1, pr2}, hiddenOrgs, false)

	// Should only notify for visible org
	if len(toNotify) != 1 {
		t.Errorf("Expected 1 PR to notify (visible org only), got %d", len(toNotify))
	}
	if toNotify[0].URL != pr2.URL {
		t.Errorf("Expected visible PR to be notified, got %s", toNotify[0].URL)
	}
}

// TestInitialDiscoveryNoNotifications tests that PRs discovered as already blocked on startup don't notify
func TestInitialDiscoveryNoNotifications(t *testing.T) {
	mgr := NewPRStateManager(time.Now().Add(-60 * time.Second)) // Past grace period

	// Create some PRs that are already blocked
	pr1 := PR{
		Repository:  "test/repo1",
		Number:      1,
		URL:         "https://github.com/test/repo1/pull/1",
		NeedsReview: true,
		UpdatedAt:   time.Now(),
	}

	pr2 := PR{
		Repository: "test/repo2",
		Number:     2,
		URL:        "https://github.com/test/repo2/pull/2",
		IsBlocked:  true,
		UpdatedAt:  time.Now(),
	}

	// Initial discovery should NOT notify even though we're past grace period
	toNotify := mgr.UpdatePRs([]PR{pr1}, []PR{pr2}, map[string]bool{}, true)
	if len(toNotify) != 0 {
		t.Errorf("Expected 0 PRs to notify on initial discovery, got %d", len(toNotify))
	}

	// Verify states were created and marked as initial discovery
	state1, exists1 := mgr.PRState(pr1.URL)
	if !exists1 {
		t.Error("Expected state to exist for pr1")
	}
	if !state1.IsInitialDiscovery {
		t.Error("Expected pr1 state to be marked as initial discovery")
	}

	state2, exists2 := mgr.PRState(pr2.URL)
	if !exists2 {
		t.Error("Expected state to exist for pr2")
	}
	if !state2.IsInitialDiscovery {
		t.Error("Expected pr2 state to be marked as initial discovery")
	}

	// Now a subsequent update with the same PRs should still not notify
	toNotify = mgr.UpdatePRs([]PR{pr1}, []PR{pr2}, map[string]bool{}, false)
	if len(toNotify) != 0 {
		t.Errorf("Expected 0 PRs to notify on subsequent update (no state change), got %d", len(toNotify))
	}

	// But if a NEW blocked PR appears later, it should notify
	pr3 := PR{
		Repository:  "test/repo3",
		Number:      3,
		URL:         "https://github.com/test/repo3/pull/3",
		NeedsReview: true,
		UpdatedAt:   time.Now(),
	}

	toNotify = mgr.UpdatePRs([]PR{pr1, pr3}, []PR{pr2}, map[string]bool{}, false)
	if len(toNotify) != 1 {
		t.Errorf("Expected 1 PR to notify for newly blocked PR, got %d", len(toNotify))
	}
	if len(toNotify) > 0 && toNotify[0].URL != pr3.URL {
		t.Errorf("Expected pr3 to be notified, got %s", toNotify[0].URL)
	}

	// Verify that initial discovery states are marked correctly
	state3, exists3 := mgr.PRState(pr3.URL)
	if !exists3 {
		t.Error("Expected state to exist for pr3")
	}
	if state3.IsInitialDiscovery {
		t.Error("Expected pr3 to NOT be marked as initial discovery (it was a real transition)")
	}
}

// TestPRStateManagerPreservesFirstBlockedTime tests that FirstBlockedAt is not reset
// when the same blocked PR is processed on subsequent polls
func TestPRStateManagerPreservesFirstBlockedTime(t *testing.T) {
	mgr := NewPRStateManager(time.Now().Add(-60 * time.Second))

	// Create a blocked PR
	pr := PR{
		Repository:  "test/repo",
		Number:      1,
		URL:         "https://github.com/test/repo/pull/1",
		NeedsReview: true,
		UpdatedAt:   time.Now(),
	}

	// First call - should create state and notify (state transition: none -> blocked)
	toNotify := mgr.UpdatePRs([]PR{pr}, []PR{}, map[string]bool{}, false)
	if len(toNotify) != 1 {
		t.Fatalf("Expected 1 PR to notify on first call, got %d", len(toNotify))
	}

	// Get the initial state
	state1, exists := mgr.PRState(pr.URL)
	if !exists {
		t.Fatal("Expected state to exist after first call")
	}
	originalFirstBlocked := state1.FirstBlockedAt

	// Wait a small amount to ensure timestamps would be different
	time.Sleep(10 * time.Millisecond)

	// Second call with same PR - should NOT notify and should preserve FirstBlockedAt
	// (no state transition: blocked -> blocked)
	toNotify = mgr.UpdatePRs([]PR{pr}, []PR{}, map[string]bool{}, false)
	if len(toNotify) != 0 {
		t.Errorf("Expected 0 PRs to notify on second call (no state transition), got %d", len(toNotify))
	}

	// Get the state again
	state2, exists := mgr.PRState(pr.URL)
	if !exists {
		t.Fatal("Expected state to exist after second call")
	}

	// FirstBlockedAt should be exactly the same
	if !state2.FirstBlockedAt.Equal(originalFirstBlocked) {
		t.Errorf("FirstBlockedAt was changed! Original: %s, New: %s",
			originalFirstBlocked.Format(time.RFC3339Nano),
			state2.FirstBlockedAt.Format(time.RFC3339Nano))
	}

	// HasNotified should still be true
	if !state2.HasNotified {
		t.Error("HasNotified should remain true")
	}

	t.Logf("SUCCESS: FirstBlockedAt preserved across polls: %s", originalFirstBlocked.Format(time.RFC3339))
}

// TestPRStateTransitions tests the core state transition logic
func TestPRStateTransitions(t *testing.T) {
	mgr := NewPRStateManager(time.Now().Add(-60 * time.Second))

	pr := PR{
		Repository:  "test/repo",
		Number:      1,
		URL:         "https://github.com/test/repo/pull/1",
		NeedsReview: true,
		UpdatedAt:   time.Now(),
	}

	// Transition 1: none -> blocked (should notify)
	toNotify := mgr.UpdatePRs([]PR{pr}, []PR{}, map[string]bool{}, false)
	if len(toNotify) != 1 {
		t.Errorf("Expected notification for none->blocked transition, got %d", len(toNotify))
	}

	// Transition 2: blocked -> unblocked (should clean up state)
	pr.NeedsReview = false
	toNotify = mgr.UpdatePRs([]PR{pr}, []PR{}, map[string]bool{}, false)
	if len(toNotify) != 0 {
		t.Errorf("Expected no notification for blocked->unblocked transition, got %d", len(toNotify))
	}

	// Verify state was removed
	if _, exists := mgr.PRState(pr.URL); exists {
		t.Error("Expected state to be removed when PR becomes unblocked")
	}

	// Transition 3: unblocked -> blocked again (should notify again as new state)
	pr.NeedsReview = true
	toNotify = mgr.UpdatePRs([]PR{pr}, []PR{}, map[string]bool{}, false)
	if len(toNotify) != 1 {
		t.Errorf("Expected notification for unblocked->blocked transition, got %d", len(toNotify))
	}

	// Verify new state was created
	state, exists := mgr.PRState(pr.URL)
	if !exists {
		t.Error("Expected new state to be created for unblocked->blocked transition")
	}
	if state.HasNotified != true {
		t.Error("Expected new state to be marked as notified")
	}
}
