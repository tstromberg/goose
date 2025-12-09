package main

import (
	"log/slog"
	"maps"
	"sync"
	"time"
)

// PRState tracks the complete state of a PR including blocking history.
type PRState struct {
	FirstBlockedAt     time.Time
	LastSeenBlocked    time.Time
	PR                 PR
	HasNotified        bool
	IsInitialDiscovery bool // True if this PR was discovered as already blocked during startup
}

// PRStateManager manages all PR states with proper synchronization.
type PRStateManager struct {
	startTime          time.Time
	states             map[string]*PRState
	gracePeriodSeconds int
	mu                 sync.RWMutex
}

// NewPRStateManager creates a new PR state manager.
func NewPRStateManager(startTime time.Time) *PRStateManager {
	return &PRStateManager{
		states:             make(map[string]*PRState),
		startTime:          startTime,
		gracePeriodSeconds: 30,
	}
}

// UpdatePRs updates the state with new PR data and returns which PRs need notifications.
// This function is thread-safe and handles all state transitions atomically.
// isInitialDiscovery should be true only on the very first poll to prevent notifications for already-blocked PRs.
func (m *PRStateManager) UpdatePRs(incoming, outgoing []PR, hiddenOrgs map[string]bool, isInitialDiscovery bool) (toNotify []PR) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	inGracePeriod := time.Since(m.startTime) < time.Duration(m.gracePeriodSeconds)*time.Second

	slog.Debug("[STATE] UpdatePRs called",
		"incoming", len(incoming), "outgoing", len(outgoing),
		"existing_states", len(m.states), "in_grace_period", inGracePeriod, "is_initial_discovery", isInitialDiscovery)

	// Track which PRs are currently blocked
	currentlyBlocked := make(map[string]bool)

	// Process all PRs (both incoming and outgoing)
	allPRs := make([]PR, 0, len(incoming)+len(outgoing))
	allPRs = append(allPRs, incoming...)
	allPRs = append(allPRs, outgoing...)

	for i := range allPRs {
		pr := allPRs[i]
		// Skip hidden orgs
		org := extractOrgFromRepo(pr.Repository)
		if org != "" && hiddenOrgs[org] {
			continue
		}

		// Check if PR is blocked
		isBlocked := pr.NeedsReview || pr.IsBlocked
		if !isBlocked {
			// PR is not blocked - remove from tracking if it was
			if state, exists := m.states[pr.URL]; exists && state != nil {
				slog.Info("[STATE] State transition: blocked -> unblocked",
					"repo", pr.Repository, "number", pr.Number, "url", pr.URL,
					"was_blocked_since", state.FirstBlockedAt.Format(time.RFC3339),
					"blocked_duration", time.Since(state.FirstBlockedAt).Round(time.Second))
				delete(m.states, pr.URL)
			}
			continue
		}

		currentlyBlocked[pr.URL] = true

		// Get or create state for this PR
		state, exists := m.states[pr.URL]
		if !exists {
			// This PR was not in our state before
			if isInitialDiscovery {
				// Initial discovery: PR was already blocked when we started, no state transition
				state = &PRState{
					PR:                 pr,
					FirstBlockedAt:     now,
					LastSeenBlocked:    now,
					HasNotified:        false, // Don't consider this as notified since no actual notification was sent
					IsInitialDiscovery: true,  // Mark as initial discovery to prevent notifications and party poppers
				}
				m.states[pr.URL] = state

				slog.Info("[STATE] Initial discovery: already blocked PR",
					"repo", pr.Repository,
					"number", pr.Number,
					"url", pr.URL,
					"pr_updated_at", pr.UpdatedAt.Format(time.RFC3339),
					"firstBlockedAt", state.FirstBlockedAt.Format(time.RFC3339))
			} else {
				// Actual state transition: unblocked -> blocked
				state = &PRState{
					PR:                 pr,
					FirstBlockedAt:     now,
					LastSeenBlocked:    now,
					HasNotified:        false,
					IsInitialDiscovery: false, // This is a real state transition
				}
				m.states[pr.URL] = state

				slog.Info("[STATE] State transition: unblocked -> blocked",
					"repo", pr.Repository,
					"number", pr.Number,
					"url", pr.URL,
					"pr_updated_at", pr.UpdatedAt.Format(time.RFC3339),
					"firstBlockedAt", state.FirstBlockedAt.Format(time.RFC3339),
					"inGracePeriod", inGracePeriod)

				// Should we notify for actual state transitions?
				if !inGracePeriod && !state.HasNotified {
					slog.Debug("[STATE] Will notify for newly blocked PR", "repo", pr.Repository, "number", pr.Number)
					toNotify = append(toNotify, pr)
					state.HasNotified = true
				} else if inGracePeriod {
					slog.Debug("[STATE] In grace period, not notifying", "repo", pr.Repository, "number", pr.Number)
				}
			}
		} else {
			// PR was already blocked in our state - just update data, preserve FirstBlockedAt
			originalFirstBlocked := state.FirstBlockedAt
			state.LastSeenBlocked = now
			state.PR = pr // Update PR data

			slog.Debug("[STATE] State transition: blocked -> blocked (no change)",
				"repo", pr.Repository, "number", pr.Number, "url", pr.URL,
				"original_first_blocked", originalFirstBlocked.Format(time.RFC3339),
				"time_since_first_blocked", time.Since(originalFirstBlocked).Round(time.Second),
				"has_notified", state.HasNotified)

			// If we haven't notified yet and we're past grace period, notify now
			// But don't notify for initial discovery PRs
			if !state.HasNotified && !inGracePeriod && !state.IsInitialDiscovery {
				slog.Info("[STATE] Past grace period, notifying for previously blocked PR",
					"repo", pr.Repository, "number", pr.Number)
				toNotify = append(toNotify, pr)
				state.HasNotified = true
			}
		}
	}

	// Clean up states for PRs that are no longer in our lists
	// Add more conservative cleanup with logging
	removedCount := 0
	for url, state := range m.states {
		if !currentlyBlocked[url] {
			timeSinceLastSeen := time.Since(state.LastSeenBlocked)
			slog.Info("[STATE] Removing stale PR state (no longer blocked)",
				"url", url, "repo", state.PR.Repository, "number", state.PR.Number,
				"first_blocked_at", state.FirstBlockedAt.Format(time.RFC3339),
				"last_seen_blocked", state.LastSeenBlocked.Format(time.RFC3339),
				"time_since_last_seen", timeSinceLastSeen.Round(time.Second),
				"was_notified", state.HasNotified)
			delete(m.states, url)
			removedCount++
		}
	}

	if removedCount > 0 {
		slog.Info("[STATE] State cleanup completed", "removed_states", removedCount, "remaining_states", len(m.states))
	}

	return toNotify
}

// BlockedPRs returns all currently blocked PRs with their states.
func (m *PRStateManager) BlockedPRs() map[string]*PRState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*PRState)
	maps.Copy(result, m.states)
	return result
}

// PRState returns the state for a specific PR.
func (m *PRStateManager) PRState(url string) (*PRState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state, exists := m.states[url]
	return state, exists
}

// ResetNotifications resets the notification flag for all PRs (useful for testing).
func (m *PRStateManager) ResetNotifications() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, state := range m.states {
		state.HasNotified = false
	}
	slog.Info("[STATE] Reset notification flags", "prCount", len(m.states))
}
