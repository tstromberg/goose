// Package dedup provides time-based event deduplication.
package dedup

import (
	"sync"
	"time"
)

// Manager deduplicates events within a time window.
type Manager struct {
	last       map[string]time.Time
	mu         sync.Mutex
	window     time.Duration
	cleanupAge time.Duration
	maxSize    int
}

// New creates a deduplication manager.
func New(window, cleanupAge time.Duration, maxSize int) *Manager {
	return &Manager{
		last:       make(map[string]time.Time),
		window:     window,
		maxSize:    maxSize,
		cleanupAge: cleanupAge,
	}
}

// ShouldProcess returns true if the event should be processed.
// Returns false if it's a duplicate within the dedup window.
// Safe for concurrent use.
func (d *Manager) ShouldProcess(key string, t time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if last, ok := d.last[key]; ok && t.Sub(last) < d.window {
		return false
	}

	d.last[key] = t

	// Cleanup old entries if map is too large
	if len(d.last) > d.maxSize {
		cutoff := t.Add(-d.cleanupAge)
		for k, ts := range d.last {
			if ts.Before(cutoff) {
				delete(d.last, k)
			}
		}
	}

	return true
}

// Size returns the current number of tracked events.
// Safe for concurrent use.
func (d *Manager) Size() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.last)
}
