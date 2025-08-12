// Package main implements a cross-platform system tray application for monitoring GitHub pull requests.
// It displays incoming and outgoing PRs, highlighting those that are blocked and need attention.
// The app integrates with the Turn API to provide additional PR metadata and uses the GitHub API
// for fetching PR data.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/codeGROOVE-dev/retry"
	"github.com/energye/systray"
	"github.com/gen2brain/beeep"
	"github.com/google/go-github/v57/github"
	"github.com/ready-to-review/turnclient/pkg/turn"
)

// Version information - set during build with -ldflags.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

const (
	// Cache settings.
	cacheTTL             = 2 * time.Hour
	cacheCleanupInterval = 5 * 24 * time.Hour

	// PR settings.
	dailyInterval    = 24 * time.Hour
	stalePRThreshold = 90 * dailyInterval
	maxPRsToProcess  = 200 // Limit for performance

	// Update interval settings.
	minUpdateInterval     = 10 * time.Second
	defaultUpdateInterval = 1 * time.Minute

	// Retry settings for external API calls - exponential backoff with jitter up to 2 minutes.
	maxRetryDelay = 2 * time.Minute
	maxRetries    = 10 // Should reach 2 minutes with exponential backoff

	// Failure thresholds.
	minorFailureThreshold = 3
	majorFailureThreshold = 10
	panicFailureIncrement = 10

	// Notification settings.
	reminderInterval     = 24 * time.Hour
	historyRetentionDays = 30

	// Turn API settings.
	turnAPITimeout            = 10 * time.Second
	maxConcurrentTurnAPICalls = 10
)

// PR represents a pull request with metadata.
type PR struct {
	UpdatedAt         time.Time
	TurnDataAppliedAt time.Time
	Title             string
	URL               string
	Repository        string
	ActionReason      string
	Number            int
	IsBlocked         bool
	NeedsReview       bool
}

// MenuState represents the current state of menu items for comparison.
type MenuState struct {
	IncomingItems []MenuItemState `json:"incoming"`
	OutgoingItems []MenuItemState `json:"outgoing"`
	HideStale     bool            `json:"hide_stale"`
}

// MenuItemState represents a single menu item's display state.
type MenuItemState struct {
	URL          string `json:"url"`
	Title        string `json:"title"`
	Repository   string `json:"repository"`
	ActionReason string `json:"action_reason"`
	Number       int    `json:"number"`
	NeedsReview  bool   `json:"needs_review"`
	IsBlocked    bool   `json:"is_blocked"`
}

// TurnResult represents a Turn API result to be applied later.
type TurnResult struct {
	URL          string
	ActionReason string
	NeedsReview  bool
	IsOwner      bool
	WasFromCache bool // Track if this result came from cache
}

// NotificationState tracks the last known state and notification time for a PR.
type NotificationState struct {
	LastNotified time.Time
	WasBlocked   bool
}

// App holds the application state.
type App struct {
	lastSuccessfulFetch time.Time
	turnClient          *turn.Client
	currentUser         *github.User
	previousBlockedPRs  map[string]bool
	notificationHistory map[string]NotificationState // Track state and notification time per PR
	client              *github.Client
	lastMenuState       *MenuState
	targetUser          string
	cacheDir            string
	incoming            []PR
	outgoing            []PR
	pendingTurnResults  []TurnResult
	consecutiveFailures int
	updateInterval      time.Duration
	mu                  sync.RWMutex
	initialLoadComplete bool
	menuInitialized     bool
	noCache             bool
	hideStaleIncoming   bool
	loadingTurnData     bool
	enableReminders     bool // Whether to send daily reminder notifications
}

func main() {
	// Parse command line flags
	var targetUser string
	var noCache bool
	var updateInterval time.Duration
	flag.StringVar(&targetUser, "user", "", "GitHub user to query PRs for (defaults to authenticated user)")
	flag.BoolVar(&noCache, "no-cache", false, "Bypass cache for debugging")
	flag.DurationVar(&updateInterval, "interval", defaultUpdateInterval, "Update interval (e.g. 30s, 1m, 5m)")
	flag.Parse()

	// Validate target user if provided
	if targetUser != "" {
		if err := validateGitHubUsername(targetUser); err != nil {
			log.Fatalf("Invalid target user: %v", err)
		}
	}

	// Validate update interval
	if updateInterval < minUpdateInterval {
		log.Printf("Update interval %v too short, using minimum of %v", updateInterval, minUpdateInterval)
		updateInterval = minUpdateInterval
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Starting GitHub PR Monitor (version=%s, commit=%s, date=%s)", version, commit, date)
	log.Printf("Configuration: update_interval=%v, max_retries=%d, max_delay=%v", updateInterval, maxRetries, maxRetryDelay)

	ctx := context.Background()

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		log.Fatalf("Failed to get cache directory: %v", err)
	}
	cacheDir = filepath.Join(cacheDir, "ready-to-review")
	const dirPerm = 0o700 // Only owner can access cache directory
	if err := os.MkdirAll(cacheDir, dirPerm); err != nil {
		log.Fatalf("Failed to create cache directory: %v", err)
	}

	app := &App{
		cacheDir:            cacheDir,
		hideStaleIncoming:   true,
		previousBlockedPRs:  make(map[string]bool),
		notificationHistory: make(map[string]NotificationState),
		targetUser:          targetUser,
		noCache:             noCache,
		updateInterval:      updateInterval,
		pendingTurnResults:  make([]TurnResult, 0),
		enableReminders:     true,
	}

	log.Println("Initializing GitHub clients...")
	err = app.initClients(ctx)
	if err != nil {
		log.Fatalf("Failed to initialize clients: %v", err)
	}

	log.Println("Loading current user...")
	var user *github.User
	err = retry.Do(func() error {
		var retryErr error
		user, _, retryErr = app.client.Users.Get(ctx, "")
		if retryErr != nil {
			log.Printf("GitHub Users.Get failed (will retry): %v", retryErr)
			return retryErr
		}
		return nil
	},
		retry.Attempts(maxRetries),
		retry.DelayType(retry.BackOffDelay),
		retry.MaxDelay(maxRetryDelay),
		retry.OnRetry(func(n uint, err error) {
			log.Printf("GitHub Users.Get retry %d/%d: %v", n+1, maxRetries, err)
		}),
		retry.Context(ctx),
	)
	if err != nil {
		log.Fatalf("Failed to load current user after %d retries: %v", maxRetries, err)
	}
	if user == nil {
		log.Fatal("GitHub API returned nil user")
	}
	app.currentUser = user

	// Log if we're using a different target user (sanitized)
	if app.targetUser != "" && app.targetUser != user.GetLogin() {
		log.Printf("Querying PRs for user '%s' instead of authenticated user", sanitizeForLog(app.targetUser))
	}

	log.Println("Starting systray...")
	// Create a cancellable context for the application
	appCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	systray.Run(func() { app.onReady(appCtx) }, func() {
		log.Println("Shutting down application")
		cancel() // Cancel the context to stop goroutines
		app.cleanupOldCache()
	})
}

func (app *App) onReady(ctx context.Context) {
	log.Println("System tray ready")
	systray.SetTitle("Loading PRs...")

	// Set tooltip based on whether we're using a custom user
	tooltip := "GitHub PR Monitor"
	if app.targetUser != "" {
		tooltip = fmt.Sprintf("GitHub PR Monitor - @%s", app.targetUser)
	}
	systray.SetTooltip(tooltip)

	// Set up click handlers
	systray.SetOnClick(func(menu systray.IMenu) {
		log.Println("Icon clicked")
		if menu != nil {
			if err := menu.ShowMenu(); err != nil {
				log.Printf("Failed to show menu: %v", err)
			}
		}
	})

	systray.SetOnRClick(func(menu systray.IMenu) {
		log.Println("Right click detected")
		if menu != nil {
			if err := menu.ShowMenu(); err != nil {
				log.Printf("Failed to show menu: %v", err)
			}
		}
	})

	// Clean old cache on startup
	app.cleanupOldCache()

	// Start update loop - it will create the initial menu after loading data
	go app.updateLoop(ctx)
}

func (app *App) updateLoop(ctx context.Context) {
	// Recover from panics to keep the update loop running
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC in update loop: %v", r)

			// Set error state in UI
			systray.SetTitle("💥")
			systray.SetTooltip("GitHub PR Monitor - Critical error")

			// Update failure count
			app.mu.Lock()
			app.consecutiveFailures += panicFailureIncrement // Treat panic as critical failure
			app.mu.Unlock()

			// Signal app to quit after panic
			log.Println("Update loop panic - signaling quit")
			systray.Quit()
		}
	}()

	ticker := time.NewTicker(app.updateInterval)
	defer ticker.Stop()
	log.Printf("[UPDATE] Update loop started with interval: %v", app.updateInterval)

	// Initial update with wait for Turn data
	app.updatePRsWithWait(ctx)

	for {
		select {
		case <-ticker.C:
			log.Println("Running scheduled PR update")
			app.updatePRs(ctx)
		case <-ctx.Done():
			log.Println("Update loop stopping due to context cancellation")
			return
		}
	}
}

func (app *App) updatePRs(ctx context.Context) {
	incoming, outgoing, err := app.fetchPRsInternal(ctx, false)
	if err != nil {
		log.Printf("Error fetching PRs: %v", err)
		app.mu.Lock()
		app.consecutiveFailures++
		failureCount := app.consecutiveFailures
		app.mu.Unlock()

		// Progressive degradation based on failure count
		var title, tooltip string
		switch {
		case failureCount == 1:
			title = "⚠️"
			tooltip = "GitHub PR Monitor - Temporary error, retrying..."
		case failureCount <= minorFailureThreshold:
			title = "⚠️"
			tooltip = fmt.Sprintf("GitHub PR Monitor - %d consecutive failures", failureCount)
		case failureCount <= majorFailureThreshold:
			title = "❌"
			tooltip = "GitHub PR Monitor - Multiple failures, check connection"
		default:
			title = "💀"
			tooltip = "GitHub PR Monitor - Service degraded, check authentication"
		}

		systray.SetTitle(title)

		// Include time since last success and user info
		timeSinceSuccess := "never"
		if !app.lastSuccessfulFetch.IsZero() {
			timeSinceSuccess = time.Since(app.lastSuccessfulFetch).Round(time.Minute).String()
		}

		userInfo := ""
		if app.targetUser != "" {
			userInfo = fmt.Sprintf(" - @%s", app.targetUser)
		}

		// Provide actionable error message based on error type
		var errorHint string
		errMsg := err.Error()
		switch {
		case strings.Contains(errMsg, "rate limited"):
			errorHint = "\nRate limited - wait before retrying"
		case strings.Contains(errMsg, "authentication"):
			errorHint = "\nCheck GitHub token with 'gh auth status'"
		case strings.Contains(errMsg, "network"):
			errorHint = "\nCheck internet connection"
		default:
			// No specific hint for this error type
		}

		fullTooltip := fmt.Sprintf("%s%s\nLast success: %s ago%s", tooltip, userInfo, timeSinceSuccess, errorHint)
		systray.SetTooltip(fullTooltip)
		return
	}

	// Update health status on success
	app.mu.Lock()
	app.lastSuccessfulFetch = time.Now()
	app.consecutiveFailures = 0
	app.mu.Unlock()

	// Update state atomically
	app.mu.Lock()
	app.incoming = incoming
	app.outgoing = outgoing
	// Mark initial load as complete after first successful update
	if !app.initialLoadComplete {
		app.initialLoadComplete = true
	}
	app.mu.Unlock()

	// Don't check for newly blocked PRs here - wait for Turn data
	// Turn data will be applied asynchronously and will trigger the check

	app.updateMenuIfChanged(ctx)
}

// buildCurrentMenuState creates a MenuState representing the current menu items.
func (app *App) buildCurrentMenuState() *MenuState {
	// Apply the same filtering as the menu display (stale PR filtering)
	staleThreshold := time.Now().Add(-stalePRThreshold)

	filterStale := func(prs []PR) []PR {
		if !app.hideStaleIncoming {
			return prs
		}
		var filtered []PR
		for i := range prs {
			if prs[i].UpdatedAt.After(staleThreshold) {
				filtered = append(filtered, prs[i])
			}
		}
		return filtered
	}

	filteredIncoming := filterStale(app.incoming)
	filteredOutgoing := filterStale(app.outgoing)

	// Sort PRs the same way the menu does
	incomingSorted := sortPRsBlockedFirst(filteredIncoming)
	outgoingSorted := sortPRsBlockedFirst(filteredOutgoing)

	// Build menu item states
	incomingItems := make([]MenuItemState, len(incomingSorted))
	for i := range incomingSorted {
		incomingItems[i] = MenuItemState{
			URL:          incomingSorted[i].URL,
			Title:        incomingSorted[i].Title,
			Repository:   incomingSorted[i].Repository,
			Number:       incomingSorted[i].Number,
			NeedsReview:  incomingSorted[i].NeedsReview,
			IsBlocked:    false, // incoming PRs don't use IsBlocked
			ActionReason: incomingSorted[i].ActionReason,
		}
	}

	outgoingItems := make([]MenuItemState, len(outgoingSorted))
	for i := range outgoingSorted {
		outgoingItems[i] = MenuItemState{
			URL:          outgoingSorted[i].URL,
			Title:        outgoingSorted[i].Title,
			Repository:   outgoingSorted[i].Repository,
			Number:       outgoingSorted[i].Number,
			NeedsReview:  outgoingSorted[i].NeedsReview,
			IsBlocked:    outgoingSorted[i].IsBlocked,
			ActionReason: outgoingSorted[i].ActionReason,
		}
	}

	return &MenuState{
		IncomingItems: incomingItems,
		OutgoingItems: outgoingItems,
		HideStale:     app.hideStaleIncoming,
	}
}

// updateMenuIfChanged only rebuilds the menu if the PR data has actually changed.
func (app *App) updateMenuIfChanged(ctx context.Context) {
	app.mu.RLock()
	// Skip menu updates while Turn data is still loading to avoid excessive rebuilds
	if app.loadingTurnData {
		app.mu.RUnlock()
		log.Print("[MENU] Skipping menu update - Turn data still loading")
		return
	}
	currentMenuState := app.buildCurrentMenuState()
	lastMenuState := app.lastMenuState
	app.mu.RUnlock()

	// Only rebuild if menu changed
	if lastMenuState != nil && cmp.Diff(lastMenuState, currentMenuState) == "" {
		return
	}

	app.mu.Lock()
	app.lastMenuState = currentMenuState
	app.mu.Unlock()
	app.rebuildMenu(ctx)
}

// updatePRsWithWait fetches PRs and waits for Turn data before building initial menu.
func (app *App) updatePRsWithWait(ctx context.Context) {
	incoming, outgoing, err := app.fetchPRsInternal(ctx, true)
	if err != nil {
		log.Printf("Error fetching PRs: %v", err)
		app.mu.Lock()
		app.consecutiveFailures++
		failureCount := app.consecutiveFailures
		app.mu.Unlock()

		// Progressive degradation based on failure count
		var title, tooltip string
		switch {
		case failureCount == 1:
			title = "⚠️"
			tooltip = "GitHub PR Monitor - Temporary error, retrying..."
		case failureCount <= minorFailureThreshold:
			title = "⚠️"
			tooltip = fmt.Sprintf("GitHub PR Monitor - %d consecutive failures", failureCount)
		case failureCount <= majorFailureThreshold:
			title = "❌"
			tooltip = "GitHub PR Monitor - Multiple failures, check connection"
		default:
			title = "💀"
			tooltip = "GitHub PR Monitor - Service degraded, check authentication"
		}

		systray.SetTitle(title)
		systray.SetTooltip(tooltip)

		// Still create initial menu even on error
		if !app.menuInitialized {
			// Create initial menu despite error
			app.rebuildMenu(ctx)
			app.menuInitialized = true
			// Menu initialization complete
		}
		return
	}

	// Update health status on success
	app.mu.Lock()
	app.lastSuccessfulFetch = time.Now()
	app.consecutiveFailures = 0
	app.mu.Unlock()

	// Update state
	app.mu.Lock()
	app.incoming = incoming
	app.outgoing = outgoing
	app.mu.Unlock()

	// Create initial menu after first successful data load
	if !app.menuInitialized {
		// Create initial menu with Turn data
		// Initialize menu structure
		app.rebuildMenu(ctx)
		app.menuInitialized = true
		// Menu initialization complete
	} else {
		app.updateMenuIfChanged(ctx)
	}

	// Mark initial load as complete after first successful update
	if !app.initialLoadComplete {
		app.mu.Lock()
		app.initialLoadComplete = true
		app.mu.Unlock()
	}
	// Check for newly blocked PRs
	app.checkForNewlyBlockedPRs(ctx)
}

// processPRNotifications handles notification logic for a single PR.
func (app *App) processPRNotifications(
	ctx context.Context,
	pr PR,
	isBlocked bool,
	isIncoming bool,
	notificationHistory map[string]NotificationState,
	playedSound *bool,
	now time.Time,
	reminderInterval time.Duration,
) {
	prevState, hasHistory := notificationHistory[pr.URL]

	// Inline notification decision logic
	var shouldNotify bool
	var notifyReason string
	switch {
	case !hasHistory && isBlocked:
		shouldNotify = true
		notifyReason = "newly blocked"
	case !hasHistory:
		shouldNotify = false
		notifyReason = ""
	case isBlocked && !prevState.WasBlocked:
		shouldNotify = true
		notifyReason = "became blocked"
	case !isBlocked && prevState.WasBlocked:
		shouldNotify = false
		notifyReason = "unblocked"
	case isBlocked && prevState.WasBlocked && app.enableReminders && time.Since(prevState.LastNotified) > reminderInterval:
		shouldNotify = true
		notifyReason = "reminder"
	default:
		shouldNotify = false
		notifyReason = ""
	}

	// Update state for unblocked PRs
	if notifyReason == "unblocked" {
		notificationHistory[pr.URL] = NotificationState{
			LastNotified: prevState.LastNotified,
			WasBlocked:   false,
		}
		return
	}

	if !shouldNotify || !isBlocked {
		return
	}

	// Send notification
	var title, soundType string
	if isIncoming {
		title = "PR Blocked on You 🪿"
		soundType = "honk"
		if notifyReason == "reminder" {
			log.Printf("[NOTIFY] Incoming PR reminder (24hr): %s #%d - %s (reason: %s)",
				pr.Repository, pr.Number, pr.Title, pr.ActionReason)
		} else {
			log.Printf("[NOTIFY] Incoming PR notification (%s): %s #%d - %s (reason: %s)",
				notifyReason, pr.Repository, pr.Number, pr.Title, pr.ActionReason)
		}
	} else {
		title = "Your PR is Blocked 🚀"
		soundType = "rocket"
		if notifyReason == "reminder" {
			log.Printf("[NOTIFY] Outgoing PR reminder (24hr): %s #%d - %s (reason: %s)",
				pr.Repository, pr.Number, pr.Title, pr.ActionReason)
		} else {
			log.Printf("[NOTIFY] Outgoing PR notification (%s): %s #%d - %s (reason: %s)",
				notifyReason, pr.Repository, pr.Number, pr.Title, pr.ActionReason)
		}
	}

	message := fmt.Sprintf("%s #%d: %s", pr.Repository, pr.Number, pr.Title)
	if err := beeep.Notify(title, message, ""); err != nil {
		log.Printf("[NOTIFY] Failed to send desktop notification for %s: %v", pr.URL, err)
	} else {
		log.Printf("[NOTIFY] Desktop notification sent for %s", pr.URL)
		notificationHistory[pr.URL] = NotificationState{
			LastNotified: now,
			WasBlocked:   true,
		}
	}

	// Play sound once per polling period
	if !*playedSound {
		if notifyReason == "reminder" {
			log.Printf("[SOUND] Playing %s sound for daily reminder", soundType)
		}
		app.playSound(ctx, soundType)
		*playedSound = true
	}
}

// checkForNewlyBlockedPRs checks for PRs that have become blocked and sends notifications.
func (app *App) checkForNewlyBlockedPRs(ctx context.Context) {
	app.mu.Lock()
	notificationHistory := app.notificationHistory
	if notificationHistory == nil {
		notificationHistory = make(map[string]NotificationState)
		app.notificationHistory = notificationHistory
	}
	initialLoadComplete := app.initialLoadComplete
	hideStaleIncoming := app.hideStaleIncoming
	incoming := make([]PR, len(app.incoming))
	copy(incoming, app.incoming)
	outgoing := make([]PR, len(app.outgoing))
	copy(outgoing, app.outgoing)
	hasValidData := len(incoming) > 0 || len(outgoing) > 0
	app.mu.Unlock()

	// Skip if this looks like a transient GitHub API failure
	if !hasValidData && initialLoadComplete {
		log.Print("[NOTIFY] Skipping notification check - no PR data (likely transient API failure)")
		return
	}

	// Calculate stale threshold
	now := time.Now()
	staleThreshold := now.Add(-stalePRThreshold)

	// Use reminder interval constant from package level

	currentBlockedPRs := make(map[string]bool)
	playedIncomingSound := false
	playedOutgoingSound := false

	// Check incoming PRs for state changes
	for idx := range incoming {
		pr := incoming[idx]
		isBlocked := pr.NeedsReview

		if isBlocked {
			currentBlockedPRs[pr.URL] = true
		}

		// Skip stale PRs for notifications if hideStaleIncoming is enabled
		if hideStaleIncoming && pr.UpdatedAt.Before(staleThreshold) {
			continue
		}

		app.processPRNotifications(ctx, pr, isBlocked, true, notificationHistory,
			&playedIncomingSound, now, reminderInterval)
	}

	// Check outgoing PRs for state changes
	for idx := range outgoing {
		pr := outgoing[idx]
		isBlocked := pr.IsBlocked

		if isBlocked {
			currentBlockedPRs[pr.URL] = true
		}

		// Skip stale PRs for notifications if hideStaleIncoming is enabled
		if hideStaleIncoming && pr.UpdatedAt.Before(staleThreshold) {
			continue
		}

		app.processPRNotifications(ctx, pr, isBlocked, false, notificationHistory,
			&playedOutgoingSound, now, reminderInterval)
	}

	// Clean up old entries from notification history (older than 7 days)
	const historyRetentionDays = 7
	for url, state := range notificationHistory {
		if time.Since(state.LastNotified) > historyRetentionDays*24*time.Hour {
			delete(notificationHistory, url)
		}
	}

	// Update the notification history
	app.mu.Lock()
	app.notificationHistory = notificationHistory
	app.previousBlockedPRs = currentBlockedPRs
	app.mu.Unlock()
}
