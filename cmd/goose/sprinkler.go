// Package main - sprinkler.go contains real-time event monitoring via WebSocket.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/codeGROOVE-dev/retry"
	"github.com/codeGROOVE-dev/sprinkler/pkg/client"
	"github.com/codeGROOVE-dev/turnclient/pkg/turn"
	"github.com/gen2brain/beeep"
)

const (
	eventChannelSize    = 100              // Buffer size for event channel
	eventDedupWindow    = 5 * time.Second  // Time window for deduplicating events
	eventMapMaxSize     = 1000             // Maximum entries in event dedup map
	eventMapCleanupAge  = 1 * time.Hour    // Age threshold for cleaning up old entries
	sprinklerMaxRetries = 3                // Max retries for Turn API calls
	sprinklerMaxDelay   = 10 * time.Second // Max delay between retries
)

// sprinklerMonitor manages WebSocket event subscriptions for all user orgs.
type sprinklerMonitor struct {
	app          *App
	client       *client.Client
	cancel       context.CancelFunc
	eventChan    chan string          // Channel for PR URLs that need checking
	lastEventMap map[string]time.Time // Track last event per URL to dedupe
	token        string
	orgs         []string
	ctx          context.Context
	mu           sync.RWMutex
	isRunning    bool
}

// newSprinklerMonitor creates a new sprinkler monitor for real-time PR events.
func newSprinklerMonitor(app *App, token string) *sprinklerMonitor {
	ctx, cancel := context.WithCancel(context.Background())
	return &sprinklerMonitor{
		app:          app,
		token:        token,
		orgs:         make([]string, 0),
		ctx:          ctx,
		cancel:       cancel,
		eventChan:    make(chan string, eventChannelSize),
		lastEventMap: make(map[string]time.Time),
	}
}

// updateOrgs updates the list of organizations to monitor.
func (sm *sprinklerMonitor) updateOrgs(orgs []string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Check if orgs changed
	if len(orgs) == len(sm.orgs) {
		same := true
		for i := range orgs {
			if orgs[i] != sm.orgs[i] {
				same = false
				break
			}
		}
		if same {
			return // No change
		}
	}

	slog.Info("[SPRINKLER] Updating monitored organizations", "orgs", orgs)
	sm.orgs = make([]string, len(orgs))
	copy(sm.orgs, orgs)

	// Restart if running
	if sm.isRunning {
		slog.Info("[SPRINKLER] Restarting monitor with new org list")
		sm.stop()
		sm.ctx, sm.cancel = context.WithCancel(context.Background())
		if err := sm.start(); err != nil {
			slog.Error("[SPRINKLER] Failed to restart", "error", err)
		}
	}
}

// start begins monitoring for PR events across all user orgs.
func (sm *sprinklerMonitor) start() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.isRunning {
		slog.Debug("[SPRINKLER] Monitor already running")
		return nil // Already running
	}

	if len(sm.orgs) == 0 {
		slog.Debug("[SPRINKLER] No organizations to monitor, skipping start")
		return nil
	}

	slog.Info("[SPRINKLER] Starting event monitor",
		"orgs", sm.orgs,
		"org_count", len(sm.orgs))

	// Create logger that discards output unless debug mode
	var sprinklerLogger *slog.Logger
	if slog.Default().Enabled(sm.ctx, slog.LevelDebug) {
		sprinklerLogger = slog.Default()
	} else {
		// Use a handler that discards all logs
		sprinklerLogger = slog.New(slog.NewTextHandler(nil, &slog.HandlerOptions{
			Level: slog.LevelError + 1, // Level higher than any log level to discard all
		}))
	}

	config := client.Config{
		ServerURL:      "wss://" + client.DefaultServerAddress + "/ws",
		Token:          sm.token,
		Organization:   "*", // Monitor all orgs
		EventTypes:     []string{"pull_request"},
		UserEventsOnly: false,
		Verbose:        false,
		NoReconnect:    false,
		Logger:         sprinklerLogger,
		OnConnect: func() {
			slog.Info("[SPRINKLER] WebSocket connected")
		},
		OnDisconnect: func(err error) {
			if err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("[SPRINKLER] WebSocket disconnected", "error", err)
			}
		},
		OnEvent: func(event client.Event) {
			sm.handleEvent(event)
		},
	}

	wsClient, err := client.New(config)
	if err != nil {
		slog.Error("[SPRINKLER] Failed to create WebSocket client", "error", err)
		return fmt.Errorf("create sprinkler client: %w", err)
	}

	sm.client = wsClient
	sm.isRunning = true

	slog.Info("[SPRINKLER] Starting event processor goroutine")
	// Start event processor
	go sm.processEvents()

	slog.Info("[SPRINKLER] Starting WebSocket client goroutine")
	// Start WebSocket client with error recovery
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("[SPRINKLER] WebSocket goroutine panic",
					"panic", r)
				sm.mu.Lock()
				sm.isRunning = false
				sm.mu.Unlock()
			}
		}()

		startTime := time.Now()
		if err := wsClient.Start(sm.ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("[SPRINKLER] WebSocket client error",
				"error", err,
				"uptime", time.Since(startTime).Round(time.Second))
			sm.mu.Lock()
			sm.isRunning = false
			sm.mu.Unlock()
		} else {
			slog.Info("[SPRINKLER] WebSocket client stopped gracefully",
				"uptime", time.Since(startTime).Round(time.Second))
		}
	}()

	slog.Info("[SPRINKLER] Event monitor started successfully")
	return nil
}

// handleEvent processes incoming PR events.
func (sm *sprinklerMonitor) handleEvent(event client.Event) {
	// Filter by event type
	if event.Type != "pull_request" {
		slog.Debug("[SPRINKLER] Ignoring non-PR event", "type", event.Type)
		return
	}

	if event.URL == "" {
		slog.Warn("[SPRINKLER] Received PR event with empty URL", "type", event.Type)
		return
	}

	// Extract org from URL (format: https://github.com/org/repo/pull/123)
	parts := strings.Split(event.URL, "/")
	const minParts = 5
	if len(parts) < minParts || parts[2] != "github.com" {
		slog.Warn("[SPRINKLER] Failed to extract org from URL", "url", event.URL)
		return
	}
	org := parts[3]

	// Check if this org is in our monitored list
	sm.mu.RLock()
	monitored := false
	for _, o := range sm.orgs {
		if o == org {
			monitored = true
			break
		}
	}
	orgCount := len(sm.orgs)
	sm.mu.RUnlock()

	if !monitored {
		slog.Debug("[SPRINKLER] Event from unmonitored org",
			"org", org,
			"url", event.URL,
			"monitored_orgs", orgCount)
		return
	}

	// Dedupe events - only process if we haven't seen this URL recently
	sm.mu.Lock()
	lastSeen, exists := sm.lastEventMap[event.URL]
	now := time.Now()
	if exists && now.Sub(lastSeen) < eventDedupWindow {
		sm.mu.Unlock()
		slog.Debug("[SPRINKLER] Skipping duplicate event",
			"url", event.URL,
			"last_seen", now.Sub(lastSeen).Round(time.Millisecond))
		return
	}
	sm.lastEventMap[event.URL] = now

	// Clean up old entries to prevent memory leak
	if len(sm.lastEventMap) > eventMapMaxSize {
		// Remove entries older than the cleanup age threshold
		cutoff := now.Add(-eventMapCleanupAge)
		for url, timestamp := range sm.lastEventMap {
			if timestamp.Before(cutoff) {
				delete(sm.lastEventMap, url)
			}
		}
		slog.Debug("[SPRINKLER] Cleaned up event map",
			"entries_remaining", len(sm.lastEventMap))
	}
	sm.mu.Unlock()

	slog.Info("[SPRINKLER] PR event received",
		"url", event.URL,
		"org", org)

	// Send to event channel for processing (non-blocking)
	select {
	case sm.eventChan <- event.URL:
		slog.Debug("[SPRINKLER] Event queued for processing", "url", event.URL)
	default:
		slog.Warn("[SPRINKLER] Event channel full, dropping event",
			"url", event.URL,
			"channel_size", cap(sm.eventChan))
	}
}

// processEvents handles PR events by checking if they're blocking and notifying.
func (sm *sprinklerMonitor) processEvents() {
	for {
		select {
		case <-sm.ctx.Done():
			return
		case prURL := <-sm.eventChan:
			sm.checkAndNotify(prURL)
		}
	}
}

// checkAndNotify checks if a PR is blocking and sends notification if needed.
func (sm *sprinklerMonitor) checkAndNotify(prURL string) {
	startTime := time.Now()

	// Get current user
	user := ""
	if sm.app.currentUser != nil {
		user = sm.app.currentUser.GetLogin()
	}
	if sm.app.targetUser != "" {
		user = sm.app.targetUser
	}
	if user == "" {
		slog.Debug("[SPRINKLER] Skipping check - no user configured", "url", prURL)
		return
	}

	// Extract repo and number early for better logging
	repo, number := parseRepoAndNumberFromURL(prURL)
	if repo == "" || number == 0 {
		slog.Warn("[SPRINKLER] Failed to parse PR URL", "url", prURL)
		return
	}

	// Check Turn server for PR status with retry logic
	var turnData *turn.CheckResponse
	var wasFromCache bool

	err := retry.Do(func() error {
		var retryErr error
		turnData, wasFromCache, retryErr = sm.app.turnData(sm.ctx, prURL, time.Now())
		if retryErr != nil {
			slog.Debug("[SPRINKLER] Turn API call failed (will retry)",
				"repo", repo, "number", number, "error", retryErr)
			return retryErr
		}
		return nil
	},
		retry.Attempts(sprinklerMaxRetries),
		retry.DelayType(retry.CombineDelay(retry.BackOffDelay, retry.RandomDelay)),
		retry.MaxDelay(sprinklerMaxDelay),
		retry.OnRetry(func(n uint, err error) {
			slog.Warn("[SPRINKLER] Retrying Turn API call",
				"attempt", n+1,
				"repo", repo,
				"number", number,
				"error", err)
		}),
		retry.Context(sm.ctx),
	)
	if err != nil {
		// Log error but don't block - the next polling cycle will catch it
		slog.Warn("[SPRINKLER] Failed to get turn data after retries",
			"repo", repo,
			"number", number,
			"elapsed", time.Since(startTime).Round(time.Millisecond),
			"error", err)
		return
	}

	// Log Turn API response details
	prState := ""
	prIsMerged := false
	if turnData != nil {
		prState = turnData.PullRequest.State
		prIsMerged = turnData.PullRequest.Merged
	}

	slog.Info("[SPRINKLER] Turn API response",
		"repo", repo,
		"number", number,
		"cached", wasFromCache,
		"state", prState,
		"merged", prIsMerged,
		"has_data", turnData != nil,
		"has_analysis", turnData != nil && turnData.Analysis.NextAction != nil)

	// Skip closed/merged PRs and remove from lists immediately
	if prState == "closed" || prIsMerged {
		slog.Info("[SPRINKLER] PR closed/merged, removing from lists",
			"repo", repo,
			"number", number,
			"state", prState,
			"merged", prIsMerged,
			"url", prURL)

		// Remove from in-memory lists immediately
		sm.app.mu.Lock()
		originalIncoming := len(sm.app.incoming)
		originalOutgoing := len(sm.app.outgoing)

		// Filter out this PR from incoming
		filteredIncoming := make([]PR, 0, len(sm.app.incoming))
		for _, pr := range sm.app.incoming {
			if pr.URL != prURL {
				filteredIncoming = append(filteredIncoming, pr)
			}
		}
		sm.app.incoming = filteredIncoming

		// Filter out this PR from outgoing
		filteredOutgoing := make([]PR, 0, len(sm.app.outgoing))
		for _, pr := range sm.app.outgoing {
			if pr.URL != prURL {
				filteredOutgoing = append(filteredOutgoing, pr)
			}
		}
		sm.app.outgoing = filteredOutgoing
		sm.app.mu.Unlock()

		slog.Info("[SPRINKLER] Removed PR from lists",
			"url", prURL,
			"incoming_before", originalIncoming,
			"incoming_after", len(sm.app.incoming),
			"outgoing_before", originalOutgoing,
			"outgoing_after", len(sm.app.outgoing))

		// Update UI to reflect removal
		sm.app.updateMenu(sm.ctx)
		return
	}

	if turnData == nil || turnData.Analysis.NextAction == nil {
		slog.Debug("[SPRINKLER] No turn data available",
			"repo", repo,
			"number", number,
			"cached", wasFromCache)
		return
	}

	// Check if user needs to take action
	action, exists := turnData.Analysis.NextAction[user]
	if !exists {
		slog.Debug("[SPRINKLER] No action required for user",
			"repo", repo,
			"number", number,
			"user", user,
			"state", prState)
		return
	}

	if !action.Critical {
		slog.Debug("[SPRINKLER] Non-critical action, skipping notification",
			"repo", repo,
			"number", number,
			"action", action.Kind,
			"critical", action.Critical)
		return
	}

	// Check if PR exists in our lists
	sm.app.mu.RLock()
	foundIncoming := false
	foundOutgoing := false
	for i := range sm.app.incoming {
		if sm.app.incoming[i].URL == prURL {
			foundIncoming = true
			break
		}
	}
	if !foundIncoming {
		for i := range sm.app.outgoing {
			if sm.app.outgoing[i].URL == prURL {
				foundOutgoing = true
				break
			}
		}
	}
	sm.app.mu.RUnlock()

	// If PR not found in our lists, trigger a refresh to fetch it
	if !foundIncoming && !foundOutgoing {
		slog.Info("[SPRINKLER] New PR detected, triggering refresh",
			"repo", repo,
			"number", number,
			"action", action.Kind)
		go sm.app.updatePRs(sm.ctx)
		return // Let the refresh handle everything
	}

	slog.Info("[SPRINKLER] Blocking PR detected via event",
		"repo", repo,
		"number", number,
		"action", action.Kind,
		"reason", action.Reason,
		"elapsed", time.Since(startTime).Round(time.Millisecond))

	// Check if we already know about this PR being blocked
	sm.app.mu.RLock()
	found := false
	for i := range sm.app.incoming {
		if sm.app.incoming[i].URL == prURL && sm.app.incoming[i].IsBlocked {
			found = true
			slog.Debug("[SPRINKLER] Found in incoming blocked PRs", "repo", repo, "number", number)
			break
		}
	}
	if !found {
		for i := range sm.app.outgoing {
			if sm.app.outgoing[i].URL == prURL && sm.app.outgoing[i].IsBlocked {
				found = true
				slog.Debug("[SPRINKLER] Found in outgoing blocked PRs", "repo", repo, "number", number)
				break
			}
		}
	}
	sm.app.mu.RUnlock()

	if found {
		slog.Debug("[SPRINKLER] Already tracking as blocked, skipping notification",
			"repo", repo,
			"number", number)
		return
	}

	// Send notification
	title := fmt.Sprintf("PR Event: #%d needs %s", number, action.Kind)
	message := fmt.Sprintf("%s #%d - %s", repo, number, action.Reason)

	// Send desktop notification
	go func() {
		if err := beeep.Notify(title, message, ""); err != nil {
			slog.Warn("[SPRINKLER] Failed to send desktop notification",
				"repo", repo,
				"number", number,
				"error", err)
		} else {
			slog.Info("[SPRINKLER] Sent desktop notification",
				"repo", repo,
				"number", number)
		}
	}()

	// Play sound if enabled
	if sm.app.enableAudioCues && time.Since(sm.app.startTime) > startupGracePeriod {
		slog.Debug("[SPRINKLER] Playing notification sound",
			"repo", repo,
			"number", number,
			"soundType", "honk")
		sm.app.playSound(sm.ctx, "honk")
	}

	// Try auto-open if enabled
	if sm.app.enableAutoBrowser {
		slog.Debug("[SPRINKLER] Attempting auto-open",
			"repo", repo,
			"number", number)
		sm.app.tryAutoOpenPR(sm.ctx, PR{
			URL:        prURL,
			Repository: repo,
			Number:     number,
			IsBlocked:  true,
			ActionKind: string(action.Kind),
		}, sm.app.enableAutoBrowser, sm.app.startTime)
	}
}

// stop stops the sprinkler monitor.
func (sm *sprinklerMonitor) stop() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.isRunning {
		return
	}

	slog.Info("[SPRINKLER] Stopping event monitor")
	sm.cancel()
	sm.isRunning = false
}

// parseRepoAndNumberFromURL extracts repo and PR number from URL.
func parseRepoAndNumberFromURL(url string) (repo string, number int) {
	// URL format: https://github.com/org/repo/pull/123
	const minParts = 7
	parts := strings.Split(url, "/")
	if len(parts) < minParts || parts[2] != "github.com" {
		return "", 0
	}

	repo = fmt.Sprintf("%s/%s", parts[3], parts[4])

	var n int
	_, err := fmt.Sscanf(parts[6], "%d", &n)
	if err != nil {
		return "", 0
	}

	return repo, n
}
