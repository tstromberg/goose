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
)

// PR represents a pull request with metadata.
type PR struct {
	UpdatedAt   time.Time
	Title       string
	URL         string
	Repository  string
	Number      int
	IsBlocked   bool
	NeedsReview bool
}

// App holds the application state.
type App struct {
	lastSuccessfulFetch time.Time
	prMenuItems         map[string]*systray.MenuItem
	turnClient          *turn.Client
	currentUser         *github.User
	previousBlockedPRs  map[string]bool
	client              *github.Client
	sectionHeaders      map[string]*systray.MenuItem
	targetUser          string
	cacheDir            string
	incoming            []PR
	outgoing            []PR
	menuItems           []*systray.MenuItem
	lastMenuHashInt     uint64
	consecutiveFailures int
	mu                  sync.RWMutex
	hideStaleIncoming   bool
	initialLoadComplete bool
	turnDataLoading     bool
	turnDataLoaded      bool
	menuInitialized     bool
}

func main() {
	// Parse command line flags
	var targetUser string
	flag.StringVar(&targetUser, "user", "", "GitHub user to query PRs for (defaults to authenticated user)")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Starting GitHub PR Monitor (version=%s, commit=%s, date=%s)", version, commit, date)

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
		prMenuItems:        make(map[string]*systray.MenuItem),
		sectionHeaders:     make(map[string]*systray.MenuItem),
	}

	log.Println("Initializing GitHub clients...")
	err = app.initClients(ctx)
	if err != nil {
		log.Fatalf("Failed to initialize clients: %v", err)
	}

	log.Println("Loading current user...")
	user, _, err := app.client.Users.Get(ctx, "")
	if err != nil {
		log.Fatalf("Failed to load current user: %v", err)
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

	// Create initial menu
	log.Println("Creating initial menu")
	app.initializeMenu(ctx)

	// Clean old cache on startup
	app.cleanupOldCache()

	// Start update loop
	go app.updateLoop(ctx)
}

func (app *App) updateLoop(ctx context.Context) {
	// Recover from panics to keep the update loop running
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC in update loop: %v", r)

			// Set error state in UI
			systray.SetTitle("💥")
			systray.SetTooltip("GitHub PR Monitor - Critical error, restarting...")

			// Update failure count
			app.mu.Lock()
			app.consecutiveFailures += 5 // Treat panic as multiple failures
			app.mu.Unlock()

			// Restart the update loop after a delay (with exponential backoff)
			const panicRestartDelay = 30 * time.Second
			log.Printf("Restarting update loop in %v", panicRestartDelay)
			time.Sleep(panicRestartDelay)
			go app.updateLoop(ctx)
		}
	}()

	ticker := time.NewTicker(updateInterval)
	defer ticker.Stop()

	// Initial update
	app.updatePRs(ctx)

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
	app.mu.RLock()
	oldBlockedPRs := app.previousBlockedPRs
	app.mu.RUnlock()

	currentBlockedPRs := make(map[string]bool)
	var incomingBlocked, outgoingBlocked int

	// Count blocked PRs and send notifications
	for i := range incoming {
		if incoming[i].NeedsReview {
			currentBlockedPRs[incoming[i].URL] = true
			if !app.hideStaleIncoming || !isStale(incoming[i].UpdatedAt) {
				incomingBlocked++
			}
			// Send notification and play sound if PR wasn't blocked before
			// (only after initial load to avoid startup noise)
			if app.initialLoadComplete && !oldBlockedPRs[incoming[i].URL] {
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
			if !app.hideStaleIncoming || !isStale(outgoing[i].UpdatedAt) {
				outgoingBlocked++
			}
			// Send notification and play sound if PR wasn't blocked before
			// (only after initial load to avoid startup noise)
			if app.initialLoadComplete && !oldBlockedPRs[outgoing[i].URL] {
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

	app.updateMenuIfChanged(ctx)

	// Mark initial load as complete after first successful update
	if !app.initialLoadComplete {
		app.mu.Lock()
		app.initialLoadComplete = true
		app.mu.Unlock()
	}
}

// isStale returns true if the PR hasn't been updated in over 90 days.
func isStale(updatedAt time.Time) bool {
	return updatedAt.Before(time.Now().Add(-stalePRThreshold))
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
		hashPartsCount   = 6
		hideStaleIndex   = 4
		turnLoadingIndex = 5
		bitsPerHashPart  = 8
	)
	hashParts := [hashPartsCount]int{
		len(app.incoming),
		len(app.outgoing),
		incomingBlocked,
		outgoingBlocked,
		0, // hideStaleIncoming
		0, // turnDataLoading
	}
	if app.hideStaleIncoming {
		hashParts[hideStaleIndex] = 1
	}
	if app.turnDataLoading {
		hashParts[turnLoadingIndex] = 1
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

	log.Printf("[MENU] Menu hash changed from %d to %d, updating menu", app.lastMenuHashInt, currentHashInt)
	app.lastMenuHashInt = currentHashInt
	app.updateMenu(ctx)
}
