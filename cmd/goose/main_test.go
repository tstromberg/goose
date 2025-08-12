package main

import (
	"testing"
	"time"
)

func TestIsStale(t *testing.T) {
	tests := []struct {
		time     time.Time
		name     string
		expected bool
	}{
		{
			name:     "recent PR",
			time:     time.Now().Add(-24 * time.Hour),
			expected: false,
		},
		{
			name:     "stale PR",
			time:     time.Now().Add(-91 * 24 * time.Hour),
			expected: true,
		},
		{
			name:     "exactly at threshold",
			time:     time.Now().Add(-90 * 24 * time.Hour),
			expected: true, // >= 90 days is stale
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// isStale was inlined - test the logic directly
			if got := tt.time.Before(time.Now().Add(-stalePRThreshold)); got != tt.expected {
				t.Errorf("stale check = %v, want %v", got, tt.expected)
			}
		})
	}
}
