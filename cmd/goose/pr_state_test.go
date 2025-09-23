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

	toNotify := mgr.UpdatePRs([]PR{pr1}, []PR{}, map[string]bool{})
	if len(toNotify) != 1 {
		t.Errorf("Expected 1 PR to notify, got %d", len(toNotify))
	}

	// Test 2: Same PR on next update should not notify again
	toNotify = mgr.UpdatePRs([]PR{pr1}, []PR{}, map[string]bool{})
	if len(toNotify) != 0 {
		t.Errorf("Expected 0 PRs to notify (already notified), got %d", len(toNotify))
	}

	// Test 3: PR becomes unblocked
	pr1.NeedsReview = false
	toNotify = mgr.UpdatePRs([]PR{pr1}, []PR{}, map[string]bool{})
	if len(toNotify) != 0 {
		t.Errorf("Expected 0 PRs to notify (unblocked), got %d", len(toNotify))
	}

	// Verify state was removed
	if _, exists := mgr.PRState(pr1.URL); exists {
		t.Error("Expected PR state to be removed when unblocked")
	}

	// Test 4: PR becomes blocked again - should notify
	pr1.NeedsReview = true
	toNotify = mgr.UpdatePRs([]PR{pr1}, []PR{}, map[string]bool{})
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

	toNotify := mgr.UpdatePRs([]PR{pr1}, []PR{}, map[string]bool{})
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
	toNotify = mgr.UpdatePRs([]PR{pr1}, []PR{}, map[string]bool{})
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

	toNotify := mgr.UpdatePRs([]PR{}, []PR{pr1, pr2}, hiddenOrgs)

	// Should only notify for visible org
	if len(toNotify) != 1 {
		t.Errorf("Expected 1 PR to notify (visible org only), got %d", len(toNotify))
	}
	if toNotify[0].URL != pr2.URL {
		t.Errorf("Expected visible PR to be notified, got %s", toNotify[0].URL)
	}
}
