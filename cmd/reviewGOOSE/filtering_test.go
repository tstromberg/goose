package main

import (
	"strings"
	"testing"
	"time"
)

// TestCountPRsWithHiddenOrgs tests that PRs from hidden orgs are not counted
func TestCountPRsWithHiddenOrgs(t *testing.T) {
	app := &App{
		incoming: []PR{
			{Repository: "org1/repo1", NeedsReview: true, UpdatedAt: time.Now()},
			{Repository: "org2/repo2", NeedsReview: true, UpdatedAt: time.Now()},
			{Repository: "org3/repo3", NeedsReview: true, UpdatedAt: time.Now()},
		},
		outgoing: []PR{
			{Repository: "org1/repo4", IsBlocked: true, UpdatedAt: time.Now()},
			{Repository: "org2/repo5", IsBlocked: true, UpdatedAt: time.Now()},
		},
		hiddenOrgs: map[string]bool{
			"org2": true, // Hide org2
		},
		hideStaleIncoming: false,
		systrayInterface:  &MockSystray{}, // Use mock systray to avoid panics
	}

	counts := app.countPRs()

	// Should only count PRs from org1 and org3, not org2
	if counts.IncomingTotal != 2 {
		t.Errorf("IncomingTotal = %d, want 2 (org2 should be hidden)", counts.IncomingTotal)
	}
	if counts.IncomingBlocked != 2 {
		t.Errorf("IncomingBlocked = %d, want 2 (org2 should be hidden)", counts.IncomingBlocked)
	}
	if counts.OutgoingTotal != 1 {
		t.Errorf("OutgoingTotal = %d, want 1 (org2 should be hidden)", counts.OutgoingTotal)
	}
	if counts.OutgoingBlocked != 1 {
		t.Errorf("OutgoingBlocked = %d, want 1 (org2 should be hidden)", counts.OutgoingBlocked)
	}
}

// TestCountPRsWithStalePRs tests that stale PRs are not counted when hideStaleIncoming is true
func TestCountPRsWithStalePRs(t *testing.T) {
	now := time.Now()
	staleTime := now.Add(-100 * 24 * time.Hour) // 100 days ago
	recentTime := now.Add(-1 * time.Hour)       // 1 hour ago

	app := &App{
		incoming: []PR{
			{Repository: "org1/repo1", NeedsReview: true, UpdatedAt: staleTime},
			{Repository: "org1/repo2", NeedsReview: true, UpdatedAt: recentTime},
			{Repository: "org2/repo3", NeedsReview: false, UpdatedAt: staleTime},
		},
		outgoing: []PR{
			{Repository: "org1/repo4", IsBlocked: true, UpdatedAt: staleTime},
			{Repository: "org1/repo5", IsBlocked: true, UpdatedAt: recentTime},
		},
		hiddenOrgs:        map[string]bool{},
		hideStaleIncoming: true,           // Hide stale PRs
		systrayInterface:  &MockSystray{}, // Use mock systray to avoid panics
	}

	counts := app.countPRs()

	// Should only count recent PRs
	if counts.IncomingTotal != 1 {
		t.Errorf("IncomingTotal = %d, want 1 (stale PRs should be hidden)", counts.IncomingTotal)
	}
	if counts.IncomingBlocked != 1 {
		t.Errorf("IncomingBlocked = %d, want 1 (stale PRs should be hidden)", counts.IncomingBlocked)
	}
	if counts.OutgoingTotal != 1 {
		t.Errorf("OutgoingTotal = %d, want 1 (stale PRs should be hidden)", counts.OutgoingTotal)
	}
	if counts.OutgoingBlocked != 1 {
		t.Errorf("OutgoingBlocked = %d, want 1 (stale PRs should be hidden)", counts.OutgoingBlocked)
	}
}

// TestCountPRsWithBothFilters tests that both filters work together
func TestCountPRsWithBothFilters(t *testing.T) {
	now := time.Now()
	staleTime := now.Add(-100 * 24 * time.Hour)
	recentTime := now.Add(-1 * time.Hour)

	app := &App{
		incoming: []PR{
			{Repository: "org1/repo1", NeedsReview: true, UpdatedAt: recentTime},  // Should be counted
			{Repository: "org2/repo2", NeedsReview: true, UpdatedAt: recentTime},  // Hidden org
			{Repository: "org3/repo3", NeedsReview: true, UpdatedAt: staleTime},   // Stale
			{Repository: "org1/repo4", NeedsReview: false, UpdatedAt: recentTime}, // Not blocked
		},
		outgoing: []PR{
			{Repository: "org1/repo5", IsBlocked: true, UpdatedAt: recentTime}, // Should be counted
			{Repository: "org2/repo6", IsBlocked: true, UpdatedAt: recentTime}, // Hidden org
			{Repository: "org3/repo7", IsBlocked: true, UpdatedAt: staleTime},  // Stale
		},
		hiddenOrgs: map[string]bool{
			"org2": true,
		},
		hideStaleIncoming: true,
		systrayInterface:  &MockSystray{}, // Use mock systray to avoid panics
	}

	counts := app.countPRs()

	// Should only count org1/repo1 (incoming) and org1/repo5 (outgoing)
	if counts.IncomingTotal != 2 {
		t.Errorf("IncomingTotal = %d, want 2", counts.IncomingTotal)
	}
	if counts.IncomingBlocked != 1 {
		t.Errorf("IncomingBlocked = %d, want 1", counts.IncomingBlocked)
	}
	if counts.OutgoingTotal != 1 {
		t.Errorf("OutgoingTotal = %d, want 1", counts.OutgoingTotal)
	}
	if counts.OutgoingBlocked != 1 {
		t.Errorf("OutgoingBlocked = %d, want 1", counts.OutgoingBlocked)
	}
}

// TestExtractOrgFromRepo tests the org extraction function
func TestExtractOrgFromRepo(t *testing.T) {
	tests := []struct {
		repo string
		name string
		want string
	}{
		{
			name: "standard repo path",
			repo: "microsoft/vscode",
			want: "microsoft",
		},
		{
			name: "single segment",
			repo: "justarepo",
			want: "justarepo",
		},
		{
			name: "empty string",
			repo: "",
			want: "",
		},
		{
			name: "nested path",
			repo: "org/repo/subpath",
			want: "org",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractOrgFromRepo(tt.repo); got != tt.want {
				t.Errorf("extractOrgFromRepo(%q) = %q, want %q", tt.repo, got, tt.want)
			}
		})
	}
}

// TestIsAlreadyTrackedAsBlocked tests that sprinkler correctly identifies blocked PRs
func TestIsAlreadyTrackedAsBlocked(t *testing.T) {
	app := &App{
		incoming: []PR{
			{URL: "https://github.com/org1/repo1/pull/1", IsBlocked: true},
			{URL: "https://github.com/org1/repo1/pull/2", IsBlocked: false},
			{URL: "https://github.com/org1/repo1/pull/3", NeedsReview: true, IsBlocked: false}, // NeedsReview but not IsBlocked
		},
		outgoing: []PR{
			{URL: "https://github.com/org2/repo2/pull/10", IsBlocked: true},
			{URL: "https://github.com/org2/repo2/pull/11", IsBlocked: false},
		},
	}

	sm := &sprinklerMonitor{app: app}

	tests := []struct {
		name string
		url  string
		want bool
	}{
		{
			name: "incoming PR is blocked",
			url:  "https://github.com/org1/repo1/pull/1",
			want: true,
		},
		{
			name: "incoming PR is not blocked",
			url:  "https://github.com/org1/repo1/pull/2",
			want: false,
		},
		{
			name: "incoming PR needs review but is not blocked",
			url:  "https://github.com/org1/repo1/pull/3",
			want: false,
		},
		{
			name: "outgoing PR is blocked",
			url:  "https://github.com/org2/repo2/pull/10",
			want: true,
		},
		{
			name: "outgoing PR is not blocked",
			url:  "https://github.com/org2/repo2/pull/11",
			want: false,
		},
		{
			name: "unknown PR",
			url:  "https://github.com/org3/repo3/pull/99",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sm.isAlreadyTrackedAsBlocked(tt.url, "test", 1)
			if got != tt.want {
				t.Errorf("isAlreadyTrackedAsBlocked(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

// TestBotPRsSortedAfterHumans tests that human-authored PRs appear before bot-authored PRs
func TestBotPRsSortedAfterHumans(t *testing.T) {
	now := time.Now()

	app := &App{
		incoming: []PR{
			{Repository: "org/repo1", Number: 1, Author: "dependabot[bot]", AuthorBot: true, NeedsReview: true, UpdatedAt: now},
			{Repository: "org/repo2", Number: 2, Author: "human-dev", AuthorBot: false, NeedsReview: true, UpdatedAt: now.Add(-1 * time.Hour)},
			{Repository: "org/repo3", Number: 3, Author: "renovate[bot]", AuthorBot: true, NeedsReview: true, UpdatedAt: now.Add(-2 * time.Hour)},
			{Repository: "org/repo4", Number: 4, Author: "another-human", AuthorBot: false, NeedsReview: true, UpdatedAt: now.Add(-3 * time.Hour)},
		},
		hiddenOrgs:       map[string]bool{},
		stateManager:     NewPRStateManager(now),
		systrayInterface: &MockSystray{},
	}

	titles := app.generatePRSectionTitles(app.incoming, "Incoming", map[string]bool{}, false)

	if len(titles) != 4 {
		t.Fatalf("Expected 4 titles, got %d", len(titles))
	}

	// Human PRs should come first (repo2 and repo4), then bot PRs (repo1 and repo3)
	// Within each group, sorted by UpdatedAt (most recent first)
	expectedOrder := []string{"repo2", "repo4", "repo1", "repo3"}
	for i, expected := range expectedOrder {
		if !strings.Contains(titles[i], expected) {
			t.Errorf("Title %d: expected to contain %q, got %q", i, expected, titles[i])
		}
	}
}

// TestBotPRsGetSmallerIcon tests that bot PRs get a smaller dot icon instead of the block
func TestBotPRsGetSmallerIcon(t *testing.T) {
	now := time.Now()

	app := &App{
		incoming: []PR{
			{
				Repository:  "org/repo1",
				Number:      1,
				Author:      "dependabot[bot]",
				AuthorBot:   true,
				NeedsReview: true,
				IsBlocked:   true,
				UpdatedAt:   now,
			},
			{
				Repository:  "org/repo2",
				Number:      2,
				Author:      "human-dev",
				AuthorBot:   false,
				NeedsReview: true,
				IsBlocked:   true,
				UpdatedAt:   now,
			},
		},
		hiddenOrgs:       map[string]bool{},
		stateManager:     NewPRStateManager(now),
		systrayInterface: &MockSystray{},
	}

	titles := app.generatePRSectionTitles(app.incoming, "Incoming", map[string]bool{}, false)

	if len(titles) != 2 {
		t.Fatalf("Expected 2 titles, got %d", len(titles))
	}

	// Human PR should come first with block icon
	humanTitle := titles[0]
	if !strings.Contains(humanTitle, "repo2") {
		t.Errorf("Expected human PR (repo2) first, got: %s", humanTitle)
	}
	if !strings.HasPrefix(humanTitle, "■") {
		t.Errorf("Expected human PR to have block icon (■), got: %s", humanTitle)
	}

	// Bot PR should come second with smaller dot
	botTitle := titles[1]
	if !strings.Contains(botTitle, "repo1") {
		t.Errorf("Expected bot PR (repo1) second, got: %s", botTitle)
	}
	if !strings.HasPrefix(botTitle, "·") {
		t.Errorf("Expected bot PR to have smaller dot (·), got: %s", botTitle)
	}
}
