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

	// Update intervals.
	updateInterval = 5 * time.Minute // Reduced frequency to avoid rate limits

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

// App holds the application state.
type App struct {
	lastSuccessfulFetch time.Time
	turnClient          *turn.Client
	currentUser         *github.User
	previousBlockedPRs  map[string]bool
	client              *github.Client
	targetUser          string
	cacheDir            string
	outgoing            []PR
	incoming            []PR
	lastMenuHashInt     uint64
	consecutiveFailures int
	mu                  sync.RWMutex
	initialLoadComplete bool
	menuInitialized     bool
	noCache             bool
	hideStaleIncoming   bool
}

func main() {
	// Parse command line flags
	var targetUser string
	var noCache bool
	flag.StringVar(&targetUser, "user", "", "GitHub user to query PRs for (defaults to authenticated user)")
	flag.BoolVar(&noCache, "no-cache", false, "Bypass cache for debugging")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Starting GitHub PR Monitor (version=%s, commit=%s, date=%s)", version, commit, date)
	log.Printf("Retry configuration: max_retries=%d, max_delay=%v", maxRetries, maxRetryDelay)

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

	ticker := time.NewTicker(updateInterval)
	defer ticker.Stop()

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
	incoming, outgoing, err := app.fetchPRs(ctx)
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

	// Check for newly blocked PRs and send notifications
	// Use a single lock for all operations on previousBlockedPRs and initialLoadComplete
	app.mu.Lock()
	oldBlockedPRs := app.previousBlockedPRs
	initialLoad := app.initialLoadComplete
	app.mu.Unlock()

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
			if initialLoad && !oldBlockedPRs[incoming[i].URL] {
				if err := beeep.Notify("PR Blocked on You",
					fmt.Sprintf("%s #%d: %s", incoming[i].Repository, incoming[i].Number, incoming[i].Title), ""); err != nil {
					log.Printf("Failed to send notification: %v", err)
				}
				app.playSound(ctx, "detective")
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
			if initialLoad && !oldBlockedPRs[outgoing[i].URL] {
				if err := beeep.Notify("PR Blocked on You",
					fmt.Sprintf("%s #%d: %s", outgoing[i].Repository, outgoing[i].Number, outgoing[i].Title), ""); err != nil {
					log.Printf("Failed to send notification: %v", err)
				}
				app.playSound(ctx, "rocket")
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

// updateMenuIfChanged only rebuilds the menu if the PR data has actually changed.
func (app *App) updateMenuIfChanged(ctx context.Context) {
	app.mu.RLock()
	// Calculate hash including blocking status - use efficient integer hash
	var incomingBlocked, outgoingBlocked int
	for i := range app.incoming {
		if app.incoming[i].NeedsReview {
			incomingBlocked++
		}
	}
	for i := range app.outgoing {
		if app.outgoing[i].IsBlocked {
			outgoingBlocked++
		}
	}

	// Build hash as integers to avoid string allocation
	const (
		hashPartsCount  = 5
		hideStaleIndex  = 4
		bitsPerHashPart = 8
	)
	hashParts := [hashPartsCount]int{
		len(app.incoming),
		len(app.outgoing),
		incomingBlocked,
		outgoingBlocked,
		0, // hideStaleIncoming
	}
	if app.hideStaleIncoming {
		hashParts[hideStaleIndex] = 1
	}

	// Simple hash function - combine values
	var currentHashInt uint64
	for i, part := range hashParts {
		// Safe conversion since we control the values
		if part >= 0 {
			currentHashInt ^= uint64(part) << (i * bitsPerHashPart)
		}
	}
	app.mu.RUnlock()

	if currentHashInt == app.lastMenuHashInt {
		log.Printf("[MENU] Menu hash unchanged (%d), skipping update", currentHashInt)
		return
	}

	log.Printf("[MENU] Menu hash changed from %d to %d, rebuilding entire menu", app.lastMenuHashInt, currentHashInt)
	app.lastMenuHashInt = currentHashInt
	app.rebuildMenu(ctx)
}

// updatePRsWithWait fetches PRs and waits for Turn data before building initial menu.
func (app *App) updatePRsWithWait(ctx context.Context) {
	incoming, outgoing, err := app.fetchPRsWithWait(ctx)
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
			app.initializeMenu(ctx)
		}
		return
	}

	// Update health status on success
	app.mu.Lock()
	app.lastSuccessfulFetch = time.Now()
	app.consecutiveFailures = 0
	app.mu.Unlock()

	// Check for newly blocked PRs and send notifications
	// Use a single lock for all operations on previousBlockedPRs and initialLoadComplete
	app.mu.Lock()
	oldBlockedPRs := app.previousBlockedPRs
	initialLoad := app.initialLoadComplete
	app.mu.Unlock()

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
			if initialLoad && !oldBlockedPRs[incoming[i].URL] {
				if err := beeep.Notify("PR Blocked on You",
					fmt.Sprintf("%s #%d: %s", incoming[i].Repository, incoming[i].Number, incoming[i].Title), ""); err != nil {
					log.Printf("Failed to send notification: %v", err)
				}
				app.playSound(ctx, "detective")
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
			if initialLoad && !oldBlockedPRs[outgoing[i].URL] {
				if err := beeep.Notify("PR Blocked on You",
					fmt.Sprintf("%s #%d: %s", outgoing[i].Repository, outgoing[i].Number, outgoing[i].Title), ""); err != nil {
					log.Printf("Failed to send notification: %v", err)
				}
				app.playSound(ctx, "rocket")
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
		app.initializeMenu(ctx)
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
