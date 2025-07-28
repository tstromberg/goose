// Package main implements a cross-platform system tray application for monitoring GitHub pull requests.
// It displays incoming and outgoing PRs, highlighting those that are blocked and need attention.
// The app integrates with the Turn API to provide additional PR metadata and uses the GitHub API
// for fetching PR data.
package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
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

//go:embed menubar-icon.png
var embeddedIcon []byte

const (
	// Cache settings
	cacheTTL             = 2 * time.Hour
	cacheCleanupInterval = 5 * 24 * time.Hour

	// PR settings
	stalePRThreshold = 90 * 24 * time.Hour

	// Update intervals
	updateInterval = 2 * time.Minute
)

// PR represents a pull request with metadata
type PR struct {
	ID          int64  `json:"id"`
	Number      int    `json:"number"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	User        *github.User
	Repository  string
	UpdatedAt   time.Time
	IsBlocked   bool
	NeedsReview bool
	Size        string
	Tags        []string
}

// App holds the application state
type App struct {
	client             *github.Client
	turnClient         *turn.Client
	currentUser        *github.User
	ctx                context.Context
	incoming           []PR
	outgoing           []PR
	menuItems          []*systray.MenuItem
	cacheDir           string
	hideStaleIncoming  bool
	previousBlockedPRs map[string]bool // Track previously blocked PRs by URL
	lastMenuHash       string          // Hash of last menu state to detect changes
	mu                 sync.RWMutex    // Protects concurrent access to PR data
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting GitHub PR Monitor")

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		log.Fatalf("Failed to get cache directory: %v", err)
	}
	cacheDir = filepath.Join(cacheDir, "ready-to-review")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		log.Fatalf("Failed to create cache directory: %v", err)
	}

	app := &App{
		ctx:                context.Background(),
		cacheDir:           cacheDir,
		hideStaleIncoming:  true,
		previousBlockedPRs: make(map[string]bool),
	}

	log.Println("Initializing GitHub clients...")
	err = app.initClients()
	if err != nil {
		log.Fatalf("Failed to initialize clients: %v", err)
	}

	log.Println("Loading current user...")
	err = app.loadCurrentUser()
	if err != nil {
		log.Fatalf("Failed to load current user: %v", err)
	}

	log.Println("Starting systray...")
	systray.Run(app.onReady, app.onExit)
}

func (app *App) onReady() {
	log.Println("System tray ready")
	systray.SetIcon(embeddedIcon)
	systray.SetTitle("Loading PRs...")
	systray.SetTooltip("GitHub PR Monitor")

	// Set up click handlers
	systray.SetOnClick(func(menu systray.IMenu) {
		log.Println("Icon clicked")
		if menu != nil {
			menu.ShowMenu()
		}
	})

	systray.SetOnRClick(func(menu systray.IMenu) {
		log.Println("Right click detected")
		if menu != nil {
			menu.ShowMenu()
		}
	})

	// Create initial menu
	log.Println("Creating initial menu")
	app.updateMenu()

	// Start cache cleanup
	app.startCacheCleanup()

	// Start update loop
	go app.updateLoop()
}

func (app *App) onExit() {
	log.Println("Shutting down application")
	app.cleanupOldCache()
}

func (app *App) updateLoop() {
	ticker := time.NewTicker(updateInterval)
	defer ticker.Stop()

	// Initial update
	app.updatePRs()

	for range ticker.C {
		log.Println("Running scheduled PR update")
		app.updatePRs()
	}
}

func (app *App) updatePRs() {
	incoming, outgoing, err := app.fetchPRs()
	if err != nil {
		log.Printf("Error fetching PRs: %v", err)
		systray.SetTitle("Error")
		return
	}

	// Check for newly blocked PRs
	var newlyBlockedPRs []PR
	currentBlockedPRs := make(map[string]bool)

	// Check incoming PRs
	for _, pr := range incoming {
		if pr.NeedsReview {
			currentBlockedPRs[pr.URL] = true
			// Check if this is newly blocked
			app.mu.RLock()
			wasBlocked := app.previousBlockedPRs[pr.URL]
			app.mu.RUnlock()
			if !wasBlocked {
				newlyBlockedPRs = append(newlyBlockedPRs, pr)
			}
		}
	}

	// Check outgoing PRs
	for _, pr := range outgoing {
		if pr.IsBlocked {
			currentBlockedPRs[pr.URL] = true
			// Check if this is newly blocked
			app.mu.RLock()
			wasBlocked := app.previousBlockedPRs[pr.URL]
			app.mu.RUnlock()
			if !wasBlocked {
				newlyBlockedPRs = append(newlyBlockedPRs, pr)
			}
		}
	}

	// Send notifications for newly blocked PRs
	for _, pr := range newlyBlockedPRs {
		title := "PR Blocked on You"
		message := fmt.Sprintf("%s #%d: %s", pr.Repository, pr.Number, pr.Title)
		if err := beeep.Notify(title, message, ""); err != nil {
			log.Printf("Failed to send notification: %v", err)
		}
	}

	// Update the previous blocked PRs map
	app.mu.Lock()
	app.previousBlockedPRs = currentBlockedPRs
	app.incoming = incoming
	app.outgoing = outgoing
	app.mu.Unlock()

	incomingBlocked := 0
	outgoingBlocked := 0

	app.mu.RLock()
	for _, pr := range app.incoming {
		// Skip stale PRs if hiding them
		if app.hideStaleIncoming && isStale(pr.UpdatedAt) {
			continue
		}
		if pr.NeedsReview {
			incomingBlocked++
		}
	}

	for _, pr := range app.outgoing {
		// Skip stale PRs if hiding them
		if app.hideStaleIncoming && isStale(pr.UpdatedAt) {
			continue
		}
		if pr.IsBlocked {
			outgoingBlocked++
		}
	}
	app.mu.RUnlock()

	// Set title based on PR state
	systray.SetIcon(embeddedIcon)
	if incomingBlocked == 0 && outgoingBlocked == 0 {
		systray.SetTitle("")
	} else if incomingBlocked > 0 {
		systray.SetTitle(fmt.Sprintf("%d/%d 🔴", incomingBlocked, outgoingBlocked))
	} else {
		systray.SetTitle(fmt.Sprintf("0/%d 🚀", outgoingBlocked))
	}

	app.updateMenuIfChanged()
}

// isStale returns true if the PR hasn't been updated in over 90 days
func isStale(updatedAt time.Time) bool {
	return time.Since(updatedAt) > stalePRThreshold
}

// generateMenuHash creates a hash of the current menu state to detect changes
func (app *App) generateMenuHash() string {
	var builder strings.Builder

	app.mu.RLock()
	defer app.mu.RUnlock()

	// Include hideStaleIncoming setting
	builder.WriteString(fmt.Sprintf("hide:%v|", app.hideStaleIncoming))

	// Hash incoming PRs (filtered by stale setting)
	builder.WriteString("incoming:")
	for _, pr := range app.incoming {
		if !app.hideStaleIncoming || !isStale(pr.UpdatedAt) {
			builder.WriteString(fmt.Sprintf("%d:%s:%v:%s|", pr.ID, pr.Repository, pr.NeedsReview, pr.Size))
		}
	}

	// Hash outgoing PRs (filtered by stale setting)
	builder.WriteString("outgoing:")
	for _, pr := range app.outgoing {
		if !app.hideStaleIncoming || !isStale(pr.UpdatedAt) {
			builder.WriteString(fmt.Sprintf("%d:%s:%v|", pr.ID, pr.Repository, pr.IsBlocked))
		}
	}

	// Generate SHA256 hash
	hash := sha256.Sum256([]byte(builder.String()))
	return fmt.Sprintf("%x", hash)
}

// updateMenuIfChanged only rebuilds the menu if the PR data has actually changed
func (app *App) updateMenuIfChanged() {
	currentHash := app.generateMenuHash()
	if currentHash == app.lastMenuHash {
		log.Println("Menu data unchanged, skipping menu rebuild")
		return
	}

	log.Println("Menu data changed, rebuilding menu")
	app.lastMenuHash = currentHash
	app.updateMenu()
}
