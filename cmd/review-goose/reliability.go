package main

import (
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// safeExecute runs a function with panic recovery and logging.
func safeExecute(operation string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			err = fmt.Errorf("panic in %s: %v\nStack: %s", operation, r, stack)
			slog.Error("[RELIABILITY] Panic recovered",
				"operation", operation,
				"panic", r,
				"stack", string(stack))
		}
	}()

	start := time.Now()
	err = fn()
	duration := time.Since(start)

	if err != nil {
		slog.Error("[RELIABILITY] Operation failed",
			"operation", operation,
			"error", err,
			"duration", duration)
	} else if duration > 5*time.Second {
		slog.Warn("[RELIABILITY] Slow operation",
			"operation", operation,
			"duration", duration)
	}

	return err
}

// circuitBreaker provides circuit breaker pattern for external API calls.
type circuitBreaker struct {
	lastFailureTime time.Time
	name            string
	state           string
	timeout         time.Duration
	failures        int
	threshold       int
	mu              sync.RWMutex
}

func newCircuitBreaker(name string, threshold int, timeout time.Duration) *circuitBreaker {
	return &circuitBreaker{
		name:      name,
		threshold: threshold,
		timeout:   timeout,
		state:     "closed",
	}
}

func (cb *circuitBreaker) call(fn func() error) error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	// Check if circuit is open
	if cb.state == "open" {
		if time.Since(cb.lastFailureTime) <= cb.timeout {
			return fmt.Errorf("circuit breaker open for %s", cb.name)
		}
		cb.state = "half-open"
		slog.Info("[CIRCUIT] Circuit breaker transitioning to half-open",
			"name", cb.name)
	}

	// Execute the function
	err := fn()
	if err != nil {
		cb.failures++
		cb.lastFailureTime = time.Now()

		if cb.failures >= cb.threshold {
			cb.state = "open"
			slog.Error("[CIRCUIT] Circuit breaker opened",
				"name", cb.name,
				"failures", cb.failures,
				"threshold", cb.threshold)
		}

		return err
	}

	// Success - reset on half-open or reduce failure count
	if cb.state == "half-open" {
		cb.state = "closed"
		cb.failures = 0
		slog.Info("[CIRCUIT] Circuit breaker closed after successful call",
			"name", cb.name)
	} else if cb.failures > 0 {
		cb.failures--
	}

	return nil
}

// healthMonitor tracks application health metrics.
type healthMonitor struct {
	lastCheckTime time.Time
	uptime        time.Time
	app           *App
	apiCalls      int64
	apiErrors     int64
	cacheHits     int64
	cacheMisses   int64
	mu            sync.RWMutex
}

func newHealthMonitor() *healthMonitor {
	return &healthMonitor{
		uptime:        time.Now(),
		lastCheckTime: time.Now(),
	}
}

func (hm *healthMonitor) recordAPICall(success bool) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	hm.apiCalls++
	if !success {
		hm.apiErrors++
	}
	hm.lastCheckTime = time.Now()
}

func (hm *healthMonitor) recordCacheAccess(hit bool) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	if hit {
		hm.cacheHits++
	} else {
		hm.cacheMisses++
	}
}

func (hm *healthMonitor) metrics() map[string]any {
	hm.mu.RLock()
	defer hm.mu.RUnlock()

	errorRate := float64(0)
	if hm.apiCalls > 0 {
		errorRate = float64(hm.apiErrors) / float64(hm.apiCalls) * 100
	}

	cacheHitRate := float64(0)
	totalCacheAccess := hm.cacheHits + hm.cacheMisses
	if totalCacheAccess > 0 {
		cacheHitRate = float64(hm.cacheHits) / float64(totalCacheAccess) * 100
	}

	return map[string]any{
		"uptime":         time.Since(hm.uptime),
		"api_calls":      hm.apiCalls,
		"api_errors":     hm.apiErrors,
		"error_rate":     errorRate,
		"cache_hits":     hm.cacheHits,
		"cache_misses":   hm.cacheMisses,
		"cache_hit_rate": cacheHitRate,
		"last_check":     hm.lastCheckTime,
	}
}

func (hm *healthMonitor) logMetrics() {
	m := hm.metrics()

	// Get sprinkler connection status
	sprinklerConnected := false
	sprinklerLastConnected := ""
	if hm.app.sprinklerMonitor != nil {
		connected, lastConnectedAt := hm.app.sprinklerMonitor.connectionStatus()
		sprinklerConnected = connected
		if !lastConnectedAt.IsZero() {
			sprinklerLastConnected = time.Since(lastConnectedAt).Round(time.Second).String() + " ago"
		}
	}

	slog.Info("[HEALTH] Application metrics",
		"uptime", m["uptime"],
		"api_calls", m["api_calls"],
		"api_errors", m["api_errors"],
		"error_rate_pct", fmt.Sprintf("%.1f", m["error_rate"]),
		"cache_hit_rate_pct", fmt.Sprintf("%.1f", m["cache_hit_rate"]),
		"sprinkler_connected", sprinklerConnected,
		"sprinkler_last_connected", sprinklerLastConnected)
}
