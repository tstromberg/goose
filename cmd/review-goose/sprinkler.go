package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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

// prEvent captures the essential details from a sprinkler event.
type prEvent struct {
	timestamp time.Time
	url       string
}

// sprinklerMonitor manages WebSocket event subscriptions for all user orgs.
type sprinklerMonitor struct {
	lastConnectedAt time.Time
	app             *App
	client          *client.Client
	cancel          context.CancelFunc
	eventChan       chan prEvent
	lastEventMap    map[string]time.Time
	token           string
	orgs            []string
	mu              sync.RWMutex
	isRunning       bool
	isConnected     bool
}

// newSprinklerMonitor creates a new sprinkler monitor for real-time PR events.
func newSprinklerMonitor(app *App, token string) *sprinklerMonitor {
	return &sprinklerMonitor{
		app:          app,
		token:        token,
		orgs:         make([]string, 0),
		eventChan:    make(chan prEvent, eventChannelSize),
		lastEventMap: make(map[string]time.Time),
	}
}

// updateOrgs sets the list of organizations to monitor.
func (sm *sprinklerMonitor) updateOrgs(orgs []string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if len(orgs) == 0 {
		slog.Debug("[SPRINKLER] No organizations provided")
		return
	}

	slog.Info("[SPRINKLER] Setting organizations", "orgs", orgs, "count", len(orgs))
	sm.orgs = make([]string, len(orgs))
	copy(sm.orgs, orgs)
}

// start begins monitoring for PR events across all user orgs.
func (sm *sprinklerMonitor) start(ctx context.Context) error {
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

	// Create context with cancel for shutdown
	monitorCtx, cancel := context.WithCancel(ctx)
	sm.cancel = cancel

	// Create logger that discards output unless debug mode
	var sprinklerLogger *slog.Logger
	if slog.Default().Enabled(ctx, slog.LevelDebug) {
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
			sm.mu.Lock()
			sm.isConnected = true
			sm.lastConnectedAt = time.Now()
			sm.mu.Unlock()
			slog.Info("[SPRINKLER] WebSocket connected")
		},
		OnDisconnect: func(err error) {
			sm.mu.Lock()
			sm.isConnected = false
			sm.mu.Unlock()
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
	go sm.processEvents(monitorCtx)

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
		if err := wsClient.Start(monitorCtx); err != nil && !errors.Is(err, context.Canceled) {
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
	monitored := slices.Contains(sm.orgs, org)
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
		"org", org,
		"timestamp", event.Timestamp.Format(time.RFC3339))

	// Send to event channel for processing (non-blocking)
	select {
	case sm.eventChan <- prEvent{timestamp: event.Timestamp, url: event.URL}:
		slog.Debug("[SPRINKLER] Event queued for processing",
			"url", event.URL,
			"timestamp", event.Timestamp.Format(time.RFC3339))
	default:
		slog.Warn("[SPRINKLER] Event channel full, dropping event",
			"url", event.URL,
			"channel_size", cap(sm.eventChan))
	}
}

// processEvents handles PR events by checking if they're blocking and notifying.
func (sm *sprinklerMonitor) processEvents(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("[SPRINKLER] Event processor panic", "panic", r)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-sm.eventChan:
			sm.checkAndNotify(ctx, evt)
		}
	}
}

// checkAndNotify checks if a PR is blocking and sends notification if needed.
func (sm *sprinklerMonitor) checkAndNotify(ctx context.Context, evt prEvent) {
	start := time.Now()

	user := sm.currentUser()
	if user == "" {
		slog.Debug("[SPRINKLER] Skipping check - no user configured", "url", evt.url)
		return
	}

	repo, n := parseRepoAndNumberFromURL(evt.url)
	if repo == "" || n == 0 {
		slog.Warn("[SPRINKLER] Failed to parse PR URL", "url", evt.url)
		return
	}

	data, cached := sm.fetchTurnData(ctx, evt, repo, n, start)
	if data == nil {
		return
	}

	if sm.handleClosedPR(ctx, data, evt.url, repo, n, cached) {
		return
	}

	act := validateUserAction(data, user, repo, n, cached)
	if act == nil {
		return
	}

	if sm.handleNewPR(ctx, evt.url, repo, n, act) {
		return
	}

	if sm.isAlreadyTrackedAsBlocked(evt.url, repo, n) {
		return
	}

	slog.Info("[SPRINKLER] Blocking PR detected via event",
		"repo", repo,
		"number", n,
		"action", act.Kind,
		"reason", act.Reason,
		"event_timestamp", evt.timestamp.Format(time.RFC3339),
		"elapsed", time.Since(start).Round(time.Millisecond))

	sm.sendNotifications(ctx, evt.url, repo, n, act)
}

// currentUser returns the configured user for the sprinkler monitor.
func (sm *sprinklerMonitor) currentUser() string {
	user := ""
	if sm.app.currentUser != nil {
		user = sm.app.currentUser.GetLogin()
	}
	if sm.app.targetUser != "" {
		user = sm.app.targetUser
	}
	return user
}

// fetchTurnData retrieves PR data from Turn API with retry logic.
func (sm *sprinklerMonitor) fetchTurnData(ctx context.Context, evt prEvent, repo string, n int, start time.Time) (*turn.CheckResponse, bool) {
	var data *turn.CheckResponse
	var cached bool

	err := retry.Do(func() error {
		var err error
		// Use event timestamp to bypass caching - this ensures we get fresh data for real-time events
		data, cached, err = sm.app.turnData(ctx, evt.url, evt.timestamp)
		if err != nil {
			slog.Debug("[SPRINKLER] Turn API call failed (will retry)",
				"repo", repo,
				"number", n,
				"event_timestamp", evt.timestamp.Format(time.RFC3339),
				"error", err)
			return err
		}
		return nil
	},
		retry.Attempts(sprinklerMaxRetries),
		retry.DelayType(retry.CombineDelay(retry.BackOffDelay, retry.RandomDelay)),
		retry.MaxDelay(sprinklerMaxDelay),
		retry.OnRetry(func(attempt uint, err error) {
			slog.Warn("[SPRINKLER] Retrying Turn API call",
				"attempt", attempt+1,
				"repo", repo,
				"number", n,
				"error", err)
		}),
		retry.Context(ctx),
	)
	if err != nil {
		slog.Warn("[SPRINKLER] Failed to get turn data after retries",
			"repo", repo,
			"number", n,
			"event_timestamp", evt.timestamp.Format(time.RFC3339),
			"elapsed", time.Since(start).Round(time.Millisecond),
			"error", err)
		return nil, false
	}

	return data, cached
}

// handleClosedPR processes closed or merged PRs and returns true if the PR was closed.
func (sm *sprinklerMonitor) handleClosedPR(
	ctx context.Context, data *turn.CheckResponse, url, repo string, n int, cached bool,
) bool {
	state := ""
	merged := false
	if data != nil {
		state = data.PullRequest.State
		merged = data.PullRequest.Merged
	}

	slog.Info("[SPRINKLER] Turn API response",
		"repo", repo,
		"number", n,
		"cached", cached,
		"state", state,
		"merged", merged,
		"has_data", data != nil,
		"has_analysis", data != nil && data.Analysis.NextAction != nil)

	if state == "closed" || merged {
		sm.removeClosedPR(ctx, url, repo, n, state, merged)
		return true
	}

	return false
}

// validateUserAction checks if the user needs to take action and returns the action if critical.
func validateUserAction(data *turn.CheckResponse, user, repo string, n int, cached bool) *turn.Action {
	if data == nil || data.Analysis.NextAction == nil {
		slog.Debug("[SPRINKLER] No turn data available",
			"repo", repo,
			"number", n,
			"cached", cached)
		return nil
	}

	act, exists := data.Analysis.NextAction[user]
	if !exists {
		slog.Debug("[SPRINKLER] No action required for user",
			"repo", repo,
			"number", n,
			"user", user,
			"state", data.PullRequest.State)
		return nil
	}

	if !act.Critical {
		slog.Debug("[SPRINKLER] Non-critical action, skipping notification",
			"repo", repo,
			"number", n,
			"action", act.Kind,
			"critical", act.Critical)
		return nil
	}

	return &act
}

// handleNewPR triggers a refresh for PRs not in our lists and returns true if handled.
func (sm *sprinklerMonitor) handleNewPR(ctx context.Context, url, repo string, n int, act *turn.Action) bool {
	sm.app.mu.RLock()
	inIncoming := findPRInList(sm.app.incoming, url)
	inOutgoing := false
	if !inIncoming {
		inOutgoing = findPRInList(sm.app.outgoing, url)
	}
	sm.app.mu.RUnlock()

	if !inIncoming && !inOutgoing {
		slog.Info("[SPRINKLER] New PR detected, triggering refresh",
			"repo", repo,
			"number", n,
			"action", act.Kind)
		go sm.app.updatePRs(ctx)
		return true
	}

	return false
}

// findPRInList searches for a PR URL in the given list.
func findPRInList(prs []PR, url string) bool {
	for i := range prs {
		if prs[i].URL == url {
			return true
		}
	}
	return false
}

// isAlreadyTrackedAsBlocked checks if the PR is already tracked as blocked.
func (sm *sprinklerMonitor) isAlreadyTrackedAsBlocked(url, repo string, n int) bool {
	sm.app.mu.RLock()
	defer sm.app.mu.RUnlock()

	for i := range sm.app.incoming {
		if sm.app.incoming[i].URL == url && sm.app.incoming[i].IsBlocked {
			slog.Debug("[SPRINKLER] Found in incoming blocked PRs", "repo", repo, "number", n)
			return true
		}
	}

	for i := range sm.app.outgoing {
		if sm.app.outgoing[i].URL == url && sm.app.outgoing[i].IsBlocked {
			slog.Debug("[SPRINKLER] Found in outgoing blocked PRs", "repo", repo, "number", n)
			return true
		}
	}

	return false
}

// sendNotifications sends desktop notification, plays sound, and attempts auto-open.
func (sm *sprinklerMonitor) sendNotifications(ctx context.Context, url, repo string, n int, act *turn.Action) {
	title := fmt.Sprintf("PR Event: #%d needs %s", n, act.Kind)
	msg := fmt.Sprintf("%s #%d - %s", repo, n, act.Reason)

	go func() {
		if err := beeep.Notify(title, msg, ""); err != nil {
			slog.Warn("[SPRINKLER] Failed to send desktop notification",
				"repo", repo,
				"number", n,
				"error", err)
		} else {
			slog.Info("[SPRINKLER] Sent desktop notification",
				"repo", repo,
				"number", n)
		}
	}()

	if sm.app.enableAudioCues && time.Since(sm.app.startTime) > startupGracePeriod {
		slog.Debug("[SPRINKLER] Playing notification sound",
			"repo", repo,
			"number", n,
			"soundType", "honk")
		sm.app.playSound(ctx, "honk")
	}

	if sm.app.enableAutoBrowser {
		slog.Debug("[SPRINKLER] Attempting auto-open",
			"repo", repo,
			"number", n)
		sm.app.tryAutoOpenPR(ctx, &PR{
			URL:        url,
			Repository: repo,
			Number:     n,
			IsBlocked:  true,
			ActionKind: string(act.Kind),
		}, sm.app.enableAutoBrowser, sm.app.startTime)
	}
}

// removeClosedPR removes a closed or merged PR from the in-memory lists.
func (sm *sprinklerMonitor) removeClosedPR(ctx context.Context, url, repo string, n int, state string, merged bool) {
	slog.Info("[SPRINKLER] PR closed/merged, removing from lists",
		"repo", repo,
		"number", n,
		"state", state,
		"merged", merged,
		"url", url)

	// Remove from in-memory lists immediately
	sm.app.mu.Lock()
	inBefore := len(sm.app.incoming)
	outBefore := len(sm.app.outgoing)

	// Filter out this PR from incoming
	in := make([]PR, 0, len(sm.app.incoming))
	for i := range sm.app.incoming {
		if sm.app.incoming[i].URL != url {
			in = append(in, sm.app.incoming[i])
		}
	}
	sm.app.incoming = in

	// Filter out this PR from outgoing
	out := make([]PR, 0, len(sm.app.outgoing))
	for i := range sm.app.outgoing {
		if sm.app.outgoing[i].URL != url {
			out = append(out, sm.app.outgoing[i])
		}
	}
	sm.app.outgoing = out
	sm.app.mu.Unlock()

	slog.Info("[SPRINKLER] Removed PR from lists",
		"url", url,
		"incoming_before", inBefore,
		"incoming_after", len(sm.app.incoming),
		"outgoing_before", outBefore,
		"outgoing_after", len(sm.app.outgoing))

	// Update UI to reflect removal
	sm.app.updateMenu(ctx)
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

// connectionStatus returns the current WebSocket connection status.
func (sm *sprinklerMonitor) connectionStatus() (connected bool, lastConnectedAt time.Time) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.isConnected, sm.lastConnectedAt
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
