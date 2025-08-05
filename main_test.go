package main

import (
	"testing"
	"time"
)

func TestIsStale(t *testing.T) {
	tests := []struct {
		name     string
		time     time.Time
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
			if got := isStale(tt.time); got != tt.expected {
				t.Errorf("isStale() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestEscapeAppleScriptString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple string",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "string with quotes",
			input:    `hello "world"`,
			expected: `hello \"world\"`,
		},
		{
			name:     "string with backslashes",
			input:    `hello\world`,
			expected: `hello\\world`,
		},
		{
			name:     "complex string",
			input:    `path\to\"file"`,
			expected: `path\\to\\\"file\"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeAppleScriptString(tt.input); got != tt.expected {
				t.Errorf("escapeAppleScriptString() = %v, want %v", got, tt.expected)
			}
		})
	}
}
