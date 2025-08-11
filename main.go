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
	stalePRThreshold = 90 * 24 * time.Hour
	maxPRsToProcess  = 200 // Limit for performance

	// Update interval settings.
	minUpdateInterval     = 10 * time.Second
	defaultUpdateInterval = 1 * time.Minute

	// Retry settings for external API calls - exponential backoff with jitter up to 2 minutes.
	maxRetryDelay = 2 * time.Minute
	maxRetries    = 10 // Should reach 2 minutes with exponential backoff
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

// App holds the application state.
type App struct {
	lastSuccessfulFetch time.Time
	turnClient          *turn.Client
	currentUser         *github.User
	previousBlockedPRs  map[string]bool
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
		cacheDir:           cacheDir,
		hideStaleIncoming:  true,
		previousBlockedPRs: make(map[string]bool),
		targetUser:         targetUser,
		noCache:            noCache,
		updateInterval:     updateInterval,
		pendingTurnResults: make([]TurnResult, 0),
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

	// Log if we're using a different target user
	if app.targetUser != "" && app.targetUser != user.GetLogin() {
		log.Printf("Querying PRs for user '%s' instead of authenticated user '%s'", app.targetUser, user.GetLogin())
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
			app.consecutiveFailures += 10 // Treat panic as critical failure
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
		const (
			minorFailureThreshold = 3
			majorFailureThreshold = 10
		)
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
	var filteredIncoming, filteredOutgoing []PR

	now := time.Now()
	staleThreshold := now.Add(-stalePRThreshold)

	for i := range app.incoming {
		if !app.hideStaleIncoming || app.incoming[i].UpdatedAt.After(staleThreshold) {
			filteredIncoming = append(filteredIncoming, app.incoming[i])
		}
	}

	for i := range app.outgoing {
		if !app.hideStaleIncoming || app.outgoing[i].UpdatedAt.After(staleThreshold) {
			filteredOutgoing = append(filteredOutgoing, app.outgoing[i])
		}
	}

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
		const (
			minorFailureThreshold = 3
			majorFailureThreshold = 10
		)
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

// checkForNewlyBlockedPRs checks for PRs that have become blocked and sends notifications.
func (app *App) checkForNewlyBlockedPRs(ctx context.Context) {
	app.mu.Lock()
	oldBlockedPRs := app.previousBlockedPRs
	if oldBlockedPRs == nil {
		oldBlockedPRs = make(map[string]bool)
	}
	initialLoadComplete := app.initialLoadComplete
	hideStaleIncoming := app.hideStaleIncoming
	incoming := make([]PR, len(app.incoming))
	copy(incoming, app.incoming)
	outgoing := make([]PR, len(app.outgoing))
	copy(outgoing, app.outgoing)
	app.mu.Unlock()

	// Only log when we're checking after initial load is complete
	if initialLoadComplete && len(oldBlockedPRs) > 0 {
		log.Printf("[NOTIFY] Checking for newly blocked PRs (oldBlockedCount=%d)", len(oldBlockedPRs))
	}

	// Calculate stale threshold
	now := time.Now()
	staleThreshold := now.Add(-stalePRThreshold)

	currentBlockedPRs := make(map[string]bool)
	playedIncomingSound := false
	playedOutgoingSound := false

	// Check incoming PRs for newly blocked ones
	for i := range incoming {
		if incoming[i].NeedsReview {
			currentBlockedPRs[incoming[i].URL] = true

			// Skip stale PRs for notifications if hideStaleIncoming is enabled
			if hideStaleIncoming && incoming[i].UpdatedAt.Before(staleThreshold) {
				continue
			}

			// Send notification and play sound if PR wasn't blocked before
			if !oldBlockedPRs[incoming[i].URL] {
				log.Printf("[NOTIFY] New blocked incoming PR: %s #%d - %s (reason: %s)",
					incoming[i].Repository, incoming[i].Number, incoming[i].Title, incoming[i].ActionReason)
				title := "PR Blocked on You 🪿"
				message := fmt.Sprintf("%s #%d: %s", incoming[i].Repository, incoming[i].Number, incoming[i].Title)
				if err := beeep.Notify(title, message, ""); err != nil {
					log.Printf("[NOTIFY] Failed to send desktop notification for %s: %v", incoming[i].URL, err)
				} else {
					log.Printf("[NOTIFY] Desktop notification sent for %s", incoming[i].URL)
				}
				// Only play sound once per polling period
				if !playedIncomingSound {
					app.playSound(ctx, "honk")
					playedIncomingSound = true
				}
			}
		}
	}

	// Check outgoing PRs for newly blocked ones
	for i := range outgoing {
		if outgoing[i].IsBlocked {
			currentBlockedPRs[outgoing[i].URL] = true

			// Skip stale PRs for notifications if hideStaleIncoming is enabled
			if hideStaleIncoming && outgoing[i].UpdatedAt.Before(staleThreshold) {
				continue
			}

			// Send notification and play sound if PR wasn't blocked before
			if !oldBlockedPRs[outgoing[i].URL] {
				log.Printf("[NOTIFY] New blocked outgoing PR: %s #%d - %s (reason: %s)",
					outgoing[i].Repository, outgoing[i].Number, outgoing[i].Title, outgoing[i].ActionReason)
				title := "Your PR is Blocked 🚀"
				message := fmt.Sprintf("%s #%d: %s", outgoing[i].Repository, outgoing[i].Number, outgoing[i].Title)
				if err := beeep.Notify(title, message, ""); err != nil {
					log.Printf("[NOTIFY] Failed to send desktop notification for %s: %v", outgoing[i].URL, err)
				} else {
					log.Printf("[NOTIFY] Desktop notification sent for %s", outgoing[i].URL)
				}
				// Only play sound once per polling period
				if !playedOutgoingSound {
					app.playSound(ctx, "rocket")
					playedOutgoingSound = true
				}
			}
		}
	}

	// Update the previous blocked PRs map
	app.mu.Lock()
	app.previousBlockedPRs = currentBlockedPRs
	app.mu.Unlock()
}
