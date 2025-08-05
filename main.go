// Package main implements a cross-platform system tray application for monitoring GitHub pull requests.
// It displays incoming and outgoing PRs, highlighting those that are blocked and need attention.
// The app integrates with the Turn API to provide additional PR metadata and uses the GitHub API
// for fetching PR data.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	turnClient          *turn.Client
	currentUser         *github.User
	previousBlockedPRs  map[string]bool
	client              *github.Client
	cacheDir            string
	lastMenuHash        string
	incoming            []PR
	menuItems           []*systray.MenuItem
	outgoing            []PR
	consecutiveFailures int
	mu                  sync.RWMutex
	hideStaleIncoming   bool
	initialLoadComplete bool
}

func main() {
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
	systray.SetTooltip("GitHub PR Monitor")

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
	app.updateMenu(ctx)

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
			// Restart the update loop after a delay
			time.Sleep(10 * time.Second)
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
		app.mu.Unlock()

		// Show different icon based on failure count
		if app.consecutiveFailures > 3 {
			systray.SetTitle("❌") // Complete failure
		} else {
			systray.SetTitle("⚠️") // Warning
		}

		// Include time since last success in tooltip
		timeSinceSuccess := "never"
		if !app.lastSuccessfulFetch.IsZero() {
			timeSinceSuccess = time.Since(app.lastSuccessfulFetch).Round(time.Minute).String()
		}
		systray.SetTooltip(fmt.Sprintf("GitHub PR Monitor - Error: %v\nLast success: %s ago", err, timeSinceSuccess))
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

	// Set title based on PR state
	switch {
	case incomingBlocked == 0 && outgoingBlocked == 0:
		systray.SetTitle("😊")
	case incomingBlocked > 0 && outgoingBlocked > 0:
		systray.SetTitle(fmt.Sprintf("🕵️ %d / 🚀 %d", incomingBlocked, outgoingBlocked))
	case incomingBlocked > 0:
		systray.SetTitle(fmt.Sprintf("🕵️ %d", incomingBlocked))
	default:
		systray.SetTitle(fmt.Sprintf("🚀 %d", outgoingBlocked))
	}

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
	return time.Since(updatedAt) > stalePRThreshold
}

// updateMenuIfChanged only rebuilds the menu if the PR data has actually changed.
func (app *App) updateMenuIfChanged(ctx context.Context) {
	app.mu.RLock()
	// Simple hash: just count of PRs and hideStale setting
	currentHash := fmt.Sprintf("%d-%d-%t", len(app.incoming), len(app.outgoing), app.hideStaleIncoming)
	app.mu.RUnlock()

	if currentHash == app.lastMenuHash {
		return
	}

	app.lastMenuHash = currentHash
	app.updateMenu(ctx)
}
