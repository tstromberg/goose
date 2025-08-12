// Package main - ratelimit.go provides rate limiting functionality.
package main

import (
	"sync"
	"time"
)

// RateLimiter implements a simple token bucket rate limiter.
type RateLimiter struct {
	lastRefill time.Time
	mu         sync.Mutex
	refillRate time.Duration
	tokens     int
	maxTokens  int
}

// NewRateLimiter creates a new rate limiter.
func NewRateLimiter(maxTokens int, refillRate time.Duration) *RateLimiter {
	return &RateLimiter{
		tokens:     maxTokens,
		maxTokens:  maxTokens,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

// Allow checks if an operation is allowed under the rate limit.
func (r *RateLimiter) Allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Refill tokens based on elapsed time
	now := time.Now()
	elapsed := now.Sub(r.lastRefill)
	tokensToAdd := int(elapsed / r.refillRate)

	if tokensToAdd > 0 {
		r.tokens += tokensToAdd
		if r.tokens > r.maxTokens {
			r.tokens = r.maxTokens
		}
		r.lastRefill = now
	}

	// Check if we have tokens available
	if r.tokens > 0 {
		r.tokens--
		return true
	}

	return false
}
