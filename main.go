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
	UpdatedAt    time.Time
	Title        string
	URL          string
	Repository   string
	ActionReason string // Action reason from Turn API when blocked
	Number       int
	IsBlocked    bool
	NeedsReview  bool
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
	log.Printf("[UPDATE] Successfully fetched %d incoming, %d outgoing PRs", len(incoming), len(outgoing))
	app.mu.Lock()
	app.lastSuccessfulFetch = time.Now()
	app.consecutiveFailures = 0
	app.mu.Unlock()

	// Check for newly blocked PRs and send notifications
	// Use a single lock for all operations on previousBlockedPRs and initialLoadComplete
	app.mu.Lock()
	oldBlockedPRs := app.previousBlockedPRs
	initialLoadComplete := app.initialLoadComplete
	app.mu.Unlock()

	log.Printf("[NOTIFY] Checking for newly blocked PRs (initialLoadComplete=%v, oldBlockedCount=%d)", initialLoadComplete, len(oldBlockedPRs))

	currentBlockedPRs := make(map[string]bool)
	var incomingBlocked, outgoingBlocked int

	// Count blocked PRs and send notifications
	for i := range incoming {
		if incoming[i].NeedsReview {
			currentBlockedPRs[incoming[i].URL] = true
			if !app.hideStaleIncoming || !incoming[i].UpdatedAt.Before(time.Now().Add(-stalePRThreshold)) {
				incomingBlocked++
			}
			// Send notification and play sound if PR wasn't blocked before
			// (only after initial load to avoid startup noise)
			if initialLoadComplete && !oldBlockedPRs[incoming[i].URL] {
				log.Printf("[NOTIFY] Sending notification for newly blocked incoming PR: %s #%d", incoming[i].Repository, incoming[i].Number)
				if err := beeep.Notify("PR Blocked on You",
					fmt.Sprintf("%s #%d: %s", incoming[i].Repository, incoming[i].Number, incoming[i].Title), ""); err != nil {
					log.Printf("Failed to send notification: %v", err)
				}
				app.playSound(ctx, "detective")
			} else {
				log.Printf("[NOTIFY] Skipping notification for incoming %s: initialLoadComplete=%v, wasBlocked=%v",
					incoming[i].Repository, initialLoadComplete, oldBlockedPRs[incoming[i].URL])
			}
		}
	}

	for i := range outgoing {
		if outgoing[i].IsBlocked {
			currentBlockedPRs[outgoing[i].URL] = true
			if !app.hideStaleIncoming || !outgoing[i].UpdatedAt.Before(time.Now().Add(-stalePRThreshold)) {
				outgoingBlocked++
			}
			// Send notification and play sound if PR wasn't blocked before
			// (only after initial load to avoid startup noise)
			if initialLoadComplete && !oldBlockedPRs[outgoing[i].URL] {
				log.Printf("[NOTIFY] Sending notification for newly blocked outgoing PR: %s #%d", outgoing[i].Repository, outgoing[i].Number)
				if err := beeep.Notify("PR Blocked on You",
					fmt.Sprintf("%s #%d: %s", outgoing[i].Repository, outgoing[i].Number, outgoing[i].Title), ""); err != nil {
					log.Printf("Failed to send notification: %v", err)
				}
				app.playSound(ctx, "rocket")
			} else {
				log.Printf("[NOTIFY] Skipping notification for outgoing %s: initialLoadComplete=%v, wasBlocked=%v",
					outgoing[i].Repository, initialLoadComplete, oldBlockedPRs[outgoing[i].URL])
			}
		}
	}

	// Update state atomically
	app.mu.Lock()
	app.previousBlockedPRs = currentBlockedPRs
	app.incoming = incoming
	app.outgoing = outgoing
	// Mark initial load as complete after first successful update
	if !app.initialLoadComplete {
		app.initialLoadComplete = true
	}
	app.mu.Unlock()

	app.updateMenuIfChanged(ctx)
}

// buildCurrentMenuState creates a MenuState representing the current menu items.
func (app *App) buildCurrentMenuState() *MenuState {
	// Apply the same filtering as the menu display (stale PR filtering)
	var filteredIncoming, filteredOutgoing []PR

	now := time.Now()
	staleThreshold := now.Add(-stalePRThreshold)

	for _, pr := range app.incoming {
		if !app.hideStaleIncoming || pr.UpdatedAt.After(staleThreshold) {
			filteredIncoming = append(filteredIncoming, pr)
		}
	}

	for _, pr := range app.outgoing {
		if !app.hideStaleIncoming || pr.UpdatedAt.After(staleThreshold) {
			filteredOutgoing = append(filteredOutgoing, pr)
		}
	}

	// Sort PRs the same way the menu does
	incomingSorted := sortPRsBlockedFirst(filteredIncoming)
	outgoingSorted := sortPRsBlockedFirst(filteredOutgoing)

	// Build menu item states
	incomingItems := make([]MenuItemState, len(incomingSorted))
	for i, pr := range incomingSorted {
		incomingItems[i] = MenuItemState{
			URL:          pr.URL,
			Title:        pr.Title,
			Repository:   pr.Repository,
			Number:       pr.Number,
			NeedsReview:  pr.NeedsReview,
			IsBlocked:    false, // incoming PRs don't use IsBlocked
			ActionReason: pr.ActionReason,
		}
	}

	outgoingItems := make([]MenuItemState, len(outgoingSorted))
	for i, pr := range outgoingSorted {
		outgoingItems[i] = MenuItemState{
			URL:          pr.URL,
			Title:        pr.Title,
			Repository:   pr.Repository,
			Number:       pr.Number,
			NeedsReview:  pr.NeedsReview,
			IsBlocked:    pr.IsBlocked,
			ActionReason: pr.ActionReason,
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
		// But still save the current state for future comparisons
		currentMenuState := app.buildCurrentMenuState()
		if app.lastMenuState == nil {
			log.Print("[MENU] Skipping menu update - Turn data still loading, but saving state for future comparison")
			app.mu.RUnlock()
			app.mu.Lock()
			app.lastMenuState = currentMenuState
			app.mu.Unlock()
		} else {
			app.mu.RUnlock()
			log.Print("[MENU] Skipping menu update - Turn data still loading")
		}
		return
	}
	log.Print("[MENU] *** updateMenuIfChanged called - calculating diff ***")
	currentMenuState := app.buildCurrentMenuState()
	lastMenuState := app.lastMenuState
	if lastMenuState == nil {
		log.Print("[MENU] *** lastMenuState is NIL - will do initial build ***")
	} else {
		log.Printf("[MENU] *** lastMenuState exists (incoming:%d, outgoing:%d) - will compare ***",
			len(lastMenuState.IncomingItems), len(lastMenuState.OutgoingItems))
	}
	app.mu.RUnlock()

	if lastMenuState != nil {
		diff := cmp.Diff(lastMenuState, currentMenuState)
		log.Printf("[MENU] *** DIFF CALCULATION RESULT ***:\n%s", diff)
		if diff == "" {
			log.Printf("[MENU] Menu state unchanged, skipping update (incoming:%d, outgoing:%d)",
				len(currentMenuState.IncomingItems), len(currentMenuState.OutgoingItems))
			return
		}
		log.Print("[MENU] *** DIFF DETECTED *** Menu state changed, rebuilding menu")
	} else {
		log.Printf("[MENU] Initial menu build (incoming:%d, outgoing:%d)",
			len(currentMenuState.IncomingItems), len(currentMenuState.OutgoingItems))
	}

	app.mu.Lock()
	log.Printf("[MENU] *** SAVING menu state (incoming:%d, outgoing:%d) ***",
		len(currentMenuState.IncomingItems), len(currentMenuState.OutgoingItems))
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
			log.Println("Creating initial menu despite error")
			log.Print("[MENU] Initializing menu structure")
			app.rebuildMenu(ctx)
			app.menuInitialized = true
			log.Print("[MENU] Menu initialization complete")
		}
		return
	}

	// Update health status on success
	log.Printf("[UPDATE] Successfully fetched %d incoming, %d outgoing PRs", len(incoming), len(outgoing))
	app.mu.Lock()
	app.lastSuccessfulFetch = time.Now()
	app.consecutiveFailures = 0
	app.mu.Unlock()

	// Check for newly blocked PRs and send notifications
	// Use a single lock for all operations on previousBlockedPRs and initialLoadComplete
	app.mu.Lock()
	oldBlockedPRs := app.previousBlockedPRs
	initialLoadComplete := app.initialLoadComplete
	app.mu.Unlock()

	log.Printf("[NOTIFY] Checking for newly blocked PRs (initialLoadComplete=%v, oldBlockedCount=%d)", initialLoadComplete, len(oldBlockedPRs))

	currentBlockedPRs := make(map[string]bool)
	var incomingBlocked, outgoingBlocked int

	// Count blocked PRs and send notifications
	for i := range incoming {
		if incoming[i].NeedsReview {
			currentBlockedPRs[incoming[i].URL] = true
			if !app.hideStaleIncoming || !incoming[i].UpdatedAt.Before(time.Now().Add(-stalePRThreshold)) {
				incomingBlocked++
			}
			// Send notification and play sound if PR wasn't blocked before
			// (only after initial load to avoid startup noise)
			if initialLoadComplete && !oldBlockedPRs[incoming[i].URL] {
				log.Printf("[NOTIFY] Sending notification for newly blocked incoming PR: %s #%d", incoming[i].Repository, incoming[i].Number)
				if err := beeep.Notify("PR Blocked on You",
					fmt.Sprintf("%s #%d: %s", incoming[i].Repository, incoming[i].Number, incoming[i].Title), ""); err != nil {
					log.Printf("Failed to send notification: %v", err)
				}
				app.playSound(ctx, "detective")
			} else {
				log.Printf("[NOTIFY] Skipping notification for incoming %s: initialLoadComplete=%v, wasBlocked=%v",
					incoming[i].Repository, initialLoadComplete, oldBlockedPRs[incoming[i].URL])
			}
		}
	}

	for i := range outgoing {
		if outgoing[i].IsBlocked {
			currentBlockedPRs[outgoing[i].URL] = true
			if !app.hideStaleIncoming || !outgoing[i].UpdatedAt.Before(time.Now().Add(-stalePRThreshold)) {
				outgoingBlocked++
			}
			// Send notification and play sound if PR wasn't blocked before
			// (only after initial load to avoid startup noise)
			if initialLoadComplete && !oldBlockedPRs[outgoing[i].URL] {
				log.Printf("[NOTIFY] Sending notification for newly blocked outgoing PR: %s #%d", outgoing[i].Repository, outgoing[i].Number)
				if err := beeep.Notify("PR Blocked on You",
					fmt.Sprintf("%s #%d: %s", outgoing[i].Repository, outgoing[i].Number, outgoing[i].Title), ""); err != nil {
					log.Printf("Failed to send notification: %v", err)
				}
				app.playSound(ctx, "rocket")
			} else {
				log.Printf("[NOTIFY] Skipping notification for outgoing %s: initialLoadComplete=%v, wasBlocked=%v",
					outgoing[i].Repository, initialLoadComplete, oldBlockedPRs[outgoing[i].URL])
			}
		}
	}

	// Update state
	app.mu.Lock()
	app.previousBlockedPRs = currentBlockedPRs
	app.incoming = incoming
	app.outgoing = outgoing
	app.mu.Unlock()

	// Create initial menu after first successful data load
	if !app.menuInitialized {
		log.Println("Creating initial menu with Turn data")
		log.Print("[MENU] Initializing menu structure")
		app.rebuildMenu(ctx)
		app.menuInitialized = true
		log.Print("[MENU] Menu initialization complete")
	} else {
		app.updateMenuIfChanged(ctx)
	}

	// Mark initial load as complete after first successful update
	if !app.initialLoadComplete {
		app.mu.Lock()
		app.initialLoadComplete = true
		app.mu.Unlock()
	}
}
