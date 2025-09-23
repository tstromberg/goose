// Package main - pr_state.go provides simplified PR state management.
package main

import (
	"log/slog"
	"sync"
	"time"
)

// PRState tracks the complete state of a PR including blocking history.
type PRState struct {
	FirstBlockedAt  time.Time
	LastSeenBlocked time.Time
	PR              PR
	HasNotified     bool
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
func (m *PRStateManager) UpdatePRs(incoming, outgoing []PR, hiddenOrgs map[string]bool) (toNotify []PR) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	inGracePeriod := time.Since(m.startTime) < time.Duration(m.gracePeriodSeconds)*time.Second

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
				slog.Info("[STATE] PR no longer blocked", "repo", pr.Repository, "number", pr.Number)
				delete(m.states, pr.URL)
			}
			continue
		}

		currentlyBlocked[pr.URL] = true

		// Get or create state for this PR
		state, exists := m.states[pr.URL]
		if !exists {
			// Check if PR is too old to be considered "newly blocked"
			// If the PR hasn't been updated in over an hour, don't treat it as new
			const maxAgeForNewlyBlocked = 1 * time.Hour
			prAge := time.Since(pr.UpdatedAt)

			if prAge > maxAgeForNewlyBlocked {
				// Old PR, track it but don't notify
				state = &PRState{
					PR:              pr,
					FirstBlockedAt:  now,
					LastSeenBlocked: now,
					HasNotified:     true, // Mark as already notified to prevent future notifications
				}
				m.states[pr.URL] = state
				slog.Debug("[STATE] Blocked PR detected but too old to notify",
					"repo", pr.Repository, "number", pr.Number,
					"age", prAge.Round(time.Minute), "maxAge", maxAgeForNewlyBlocked)
			} else {
				// New blocked PR that was recently updated!
				state = &PRState{
					PR:              pr,
					FirstBlockedAt:  now,
					LastSeenBlocked: now,
					HasNotified:     false,
				}
				m.states[pr.URL] = state

				slog.Info("[STATE] New blocked PR detected", "repo", pr.Repository, "number", pr.Number,
					"age", prAge.Round(time.Minute))

				// Should we notify?
				if !inGracePeriod && !state.HasNotified {
					slog.Debug("[STATE] Will notify for newly blocked PR", "repo", pr.Repository, "number", pr.Number)
					toNotify = append(toNotify, pr)
					state.HasNotified = true
				} else if inGracePeriod {
					slog.Debug("[STATE] In grace period, not notifying", "repo", pr.Repository, "number", pr.Number)
				}
			}
		} else {
			// PR was already blocked
			state.LastSeenBlocked = now
			state.PR = pr // Update PR data

			// If we haven't notified yet and we're past grace period, notify now
			if !state.HasNotified && !inGracePeriod {
				slog.Info("[STATE] Past grace period, notifying for previously blocked PR",
					"repo", pr.Repository, "number", pr.Number)
				toNotify = append(toNotify, pr)
				state.HasNotified = true
			}
		}
	}

	// Clean up states for PRs that are no longer in our lists
	for url := range m.states {
		if !currentlyBlocked[url] {
			slog.Debug("[STATE] Removing stale state for PR", "url", url)
			delete(m.states, url)
		}
	}

	return toNotify
}

// BlockedPRs returns all currently blocked PRs with their states.
func (m *PRStateManager) BlockedPRs() map[string]*PRState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*PRState)
	for url, state := range m.states {
		result[url] = state
	}
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
