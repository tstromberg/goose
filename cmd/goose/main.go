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
	"reflect"
	"strings"
	"sync"
	"time"

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
	cacheTTL                  = 2 * time.Hour
	cacheCleanupInterval      = 5 * 24 * time.Hour
	stalePRThreshold          = 90 * 24 * time.Hour
	maxPRsToProcess           = 200
	minUpdateInterval         = 10 * time.Second
	defaultUpdateInterval     = 1 * time.Minute
	maxRetryDelay             = 2 * time.Minute
	maxRetries                = 10
	minorFailureThreshold     = 3
	majorFailureThreshold     = 10
	panicFailureIncrement     = 10
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
	client              *github.Client
	turnClient          *turn.Client
	currentUser         *github.User
	previousBlockedPRs  map[string]bool
	targetUser          string
	cacheDir            string
	authError           string
	incoming            []PR
	outgoing            []PR
	lastMenuTitles      []string
	pendingTurnResults  []TurnResult
	updateInterval      time.Duration
	consecutiveFailures int
	mu                  sync.RWMutex
	noCache             bool
	hideStaleIncoming   bool
	loadingTurnData     bool
	menuInitialized     bool
	initialLoadComplete bool
	enableReminders     bool
	enableAudioCues     bool
}

func loadCurrentUser(ctx context.Context, app *App) {
	log.Println("Loading current user...")

	if app.client == nil {
		log.Println("Skipping user load - no GitHub client available")
		return
	}

	var user *github.User
	err := retry.Do(func() error {
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
		log.Printf("Warning: Failed to load current user after %d retries: %v", maxRetries, err)
		if app.authError == "" {
			app.authError = fmt.Sprintf("Failed to load user: %v", err)
		}
		return
	}

	if user == nil {
		log.Print("Warning: GitHub API returned nil user")
		return
	}

	app.currentUser = user
	// Log if we're using a different target user (sanitized)
	if app.targetUser != "" && app.targetUser != user.GetLogin() {
		log.Printf("Querying PRs for user '%s' instead of authenticated user", sanitizeForLog(app.targetUser))
	}
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
		cacheDir:           cacheDir,
		hideStaleIncoming:  true,
		previousBlockedPRs: make(map[string]bool),
		targetUser:         targetUser,
		noCache:            noCache,
		updateInterval:     updateInterval,
		pendingTurnResults: make([]TurnResult, 0),
		enableReminders:    true,
		enableAudioCues:    true,
	}

	// Load saved settings
	app.loadSettings()

	log.Println("Initializing GitHub clients...")
	err = app.initClients(ctx)
	if err != nil {
		log.Printf("Warning: Failed to initialize clients: %v", err)
		app.authError = err.Error()
		// Continue running with auth error - will show error in UI
	}

	// Load current user if we have a client
	loadCurrentUser(ctx, app)

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

	// Set up click handlers first (needed for both success and error states)
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

	// Check if we have an auth error
	if app.authError != "" {
		systray.SetTitle("⚠️")
		systray.SetTooltip("GitHub PR Monitor - Authentication Error")
		// Create initial error menu
		app.rebuildMenu(ctx)
		// Clean old cache on startup
		app.cleanupOldCache()
		return
	}

	systray.SetTitle("Loading PRs...")

	// Set tooltip based on whether we're using a custom user
	tooltip := "GitHub PR Monitor"
	if app.targetUser != "" {
		tooltip = fmt.Sprintf("GitHub PR Monitor - @%s", app.targetUser)
	}
	systray.SetTooltip(tooltip)

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

	app.updateMenu(ctx)
}

// updateMenu rebuilds the menu only when content actually changes.
func (app *App) updateMenu(ctx context.Context) {
	app.mu.RLock()
	// Skip menu updates while Turn data is still loading
	if app.loadingTurnData {
		app.mu.RUnlock()
		return
	}

	// Build current menu titles for comparison
	var currentTitles []string
	for i := range app.incoming {
		currentTitles = append(currentTitles, fmt.Sprintf("IN:%s #%d", app.incoming[i].Repository, app.incoming[i].Number))
	}
	for i := range app.outgoing {
		currentTitles = append(currentTitles, fmt.Sprintf("OUT:%s #%d", app.outgoing[i].Repository, app.outgoing[i].Number))
	}

	lastTitles := app.lastMenuTitles
	app.mu.RUnlock()

	// Only rebuild if titles changed
	if reflect.DeepEqual(lastTitles, currentTitles) {
		return
	}

	app.mu.Lock()
	app.lastMenuTitles = currentTitles
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
		app.updateMenu(ctx)
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

// notifyWithSound sends a notification and plays sound only once per cycle.
func (app *App) notifyWithSound(ctx context.Context, pr PR, isIncoming bool, playedSound *bool) {
	var title, soundType string
	if isIncoming {
		title = "PR Blocked on You 🪿"
		soundType = "honk"
	} else {
		title = "Your PR is Blocked 🚀"
		soundType = "rocket"
	}

	message := fmt.Sprintf("%s #%d: %s", pr.Repository, pr.Number, pr.Title)
	if err := beeep.Notify(title, message, ""); err != nil {
		log.Printf("Failed to send notification for %s: %v", pr.URL, err)
	}

	// Play sound only once per refresh cycle
	if !*playedSound {
		app.playSound(ctx, soundType)
		*playedSound = true
	}
}

// checkForNewlyBlockedPRs sends notifications for blocked PRs.
func (app *App) checkForNewlyBlockedPRs(ctx context.Context) {
	app.mu.RLock()
	incoming := app.incoming
	outgoing := app.outgoing
	previousBlocked := app.previousBlockedPRs
	app.mu.RUnlock()

	currentBlocked := make(map[string]bool)
	playedHonk := false
	playedJet := false

	// Check incoming PRs
	for i := range incoming {
		if incoming[i].NeedsReview {
			currentBlocked[incoming[i].URL] = true
			// Notify if newly blocked
			if !previousBlocked[incoming[i].URL] {
				app.notifyWithSound(ctx, incoming[i], true, &playedHonk)
			}
		}
	}

	// Check outgoing PRs
	for i := range outgoing {
		if outgoing[i].IsBlocked {
			currentBlocked[outgoing[i].URL] = true
			// Notify if newly blocked
			if !previousBlocked[outgoing[i].URL] {
				// Add delay if we already played honk sound
				if playedHonk && !playedJet {
					time.Sleep(2 * time.Second)
				}
				app.notifyWithSound(ctx, outgoing[i], false, &playedJet)
			}
		}
	}

	app.mu.Lock()
	app.previousBlockedPRs = currentBlocked
	app.mu.Unlock()
}
