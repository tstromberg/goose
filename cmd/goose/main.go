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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/codeGROOVE-dev/retry"
	"github.com/energye/systray"
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
	blockedPRIconDuration     = 5 * time.Minute
	maxRetryDelay             = 2 * time.Minute
	maxRetries                = 10
	minorFailureThreshold     = 3
	majorFailureThreshold     = 10
	panicFailureIncrement     = 10
	turnAPITimeout            = 10 * time.Second
	maxConcurrentTurnAPICalls = 20
	defaultMaxBrowserOpensDay = 20
	startupGracePeriod        = 30 * time.Second // Don't play sounds or auto-open for first 30 seconds
)

// PR represents a pull request with metadata.
type PR struct {
	UpdatedAt         time.Time
	TurnDataAppliedAt time.Time
	FirstBlockedAt    time.Time // When this PR was first detected as blocked
	Title             string
	URL               string
	Repository        string
	ActionReason      string
	Number            int
	IsBlocked         bool
	NeedsReview       bool
}

// App holds the application state.
type App struct {
	lastSuccessfulFetch time.Time
	lastSearchAttempt   time.Time // For rate limiting forced refreshes
	lastMenuTitles      []string  // For change detection to prevent unnecessary redraws
	startTime           time.Time
	client              *github.Client
	turnClient          *turn.Client
	currentUser         *github.User
	stateManager        *PRStateManager // NEW: Centralized state management
	browserRateLimiter  *BrowserRateLimiter
	targetUser          string
	cacheDir            string
	authError           string
	incoming            []PR
	outgoing            []PR
	updateInterval      time.Duration
	consecutiveFailures int
	mu                  sync.RWMutex
	menuInitialized     bool
	initialLoadComplete bool
	enableAudioCues     bool
	enableAutoBrowser   bool
	hideStaleIncoming   bool
	noCache             bool
	systrayInterface    SystrayInterface // For mocking systray operations in tests
	hiddenOrgs          map[string]bool
	seenOrgs            map[string]bool

	// Deprecated: These fields are kept for backward compatibility with tests
	// The actual state is managed by stateManager
	previousBlockedPRs map[string]bool      // Deprecated: use stateManager
	blockedPRTimes     map[string]time.Time // Deprecated: use stateManager
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
		retry.DelayType(retry.CombineDelay(retry.BackOffDelay, retry.RandomDelay)), // Add jitter for better backoff distribution
		retry.MaxDelay(maxRetryDelay),
		retry.OnRetry(func(n uint, err error) {
			log.Printf("[GITHUB] Users.Get retry %d/%d: %v", n+1, maxRetries, err)
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
	var browserOpenDelay time.Duration
	var maxBrowserOpensMinute int
	var maxBrowserOpensDay int
	flag.StringVar(&targetUser, "user", "", "GitHub user to query PRs for (defaults to authenticated user)")
	flag.BoolVar(&noCache, "no-cache", false, "Bypass cache for debugging")
	flag.DurationVar(&updateInterval, "interval", defaultUpdateInterval, "Update interval (e.g. 30s, 1m, 5m)")
	flag.DurationVar(&browserOpenDelay, "browser-delay", 1*time.Minute, "Minimum delay before opening PRs in browser after startup")
	flag.IntVar(&maxBrowserOpensMinute, "browser-max-per-minute", 2, "Maximum browser windows to open per minute")
	flag.IntVar(&maxBrowserOpensDay, "browser-max-per-day", defaultMaxBrowserOpensDay, "Maximum browser windows to open per day")
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

	// Validate browser rate limit parameters
	if maxBrowserOpensMinute < 0 {
		log.Printf("Invalid browser-max-per-minute %d, using default of 2", maxBrowserOpensMinute)
		maxBrowserOpensMinute = 2
	}
	if maxBrowserOpensDay < 0 {
		log.Printf("Invalid browser-max-per-day %d, using default of %d", maxBrowserOpensDay, defaultMaxBrowserOpensDay)
		maxBrowserOpensDay = defaultMaxBrowserOpensDay
	}
	if browserOpenDelay < 0 {
		log.Printf("Invalid browser-delay %v, using default of 1 minute", browserOpenDelay)
		browserOpenDelay = 1 * time.Minute
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Starting GitHub PR Monitor (version=%s, commit=%s, date=%s)", version, commit, date)
	log.Printf("Configuration: update_interval=%v, max_retries=%d, max_delay=%v", updateInterval, maxRetries, maxRetryDelay)
	log.Printf("Browser auto-open: startup_delay=%v, max_per_minute=%d, max_per_day=%d",
		browserOpenDelay, maxBrowserOpensMinute, maxBrowserOpensDay)

	ctx := context.Background()

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		log.Fatalf("Failed to get cache directory: %v", err)
	}
	cacheDir = filepath.Join(cacheDir, "review-goose")
	const dirPerm = 0o700 // Only owner can access cache directory
	if err := os.MkdirAll(cacheDir, dirPerm); err != nil {
		log.Fatalf("Failed to create cache directory: %v", err)
	}

	startTime := time.Now()
	app := &App{
		cacheDir:           cacheDir,
		hideStaleIncoming:  true,
		stateManager:       NewPRStateManager(startTime), // NEW: Simplified state tracking
		targetUser:         targetUser,
		noCache:            noCache,
		updateInterval:     updateInterval,
		enableAudioCues:    true,
		enableAutoBrowser:  false, // Default to false for safety
		browserRateLimiter: NewBrowserRateLimiter(browserOpenDelay, maxBrowserOpensMinute, maxBrowserOpensDay),
		startTime:          startTime,
		systrayInterface:   &RealSystray{}, // Use real systray implementation
		seenOrgs:           make(map[string]bool),
		hiddenOrgs:         make(map[string]bool),
		// Deprecated fields for test compatibility
		previousBlockedPRs: make(map[string]bool),
		blockedPRTimes:     make(map[string]time.Time),
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
		
		// Check if we can perform a forced refresh (rate limited to every 10 seconds)
		app.mu.RLock()
		timeSinceLastSearch := time.Since(app.lastSearchAttempt)
		app.mu.RUnlock()
		
		if timeSinceLastSearch >= minUpdateInterval {
			log.Printf("[CLICK] Forcing search refresh (last search %v ago)", timeSinceLastSearch)
			go func() {
				app.updatePRs(ctx)
			}()
		} else {
			remainingTime := minUpdateInterval - timeSinceLastSearch
			log.Printf("[CLICK] Rate limited - search performed %v ago, %v remaining", timeSinceLastSearch, remainingTime)
		}
		
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
			// Check if we should skip this scheduled update due to recent forced refresh
			app.mu.RLock()
			timeSinceLastSearch := time.Since(app.lastSearchAttempt)
			app.mu.RUnlock()
			
			if timeSinceLastSearch >= minUpdateInterval {
				log.Println("Running scheduled PR update")
				app.updatePRs(ctx)
			} else {
				remainingTime := minUpdateInterval - timeSinceLastSearch
				log.Printf("Skipping scheduled update - recent search %v ago, %v remaining until next allowed", 
					timeSinceLastSearch, remainingTime)
			}
		case <-ctx.Done():
			log.Println("Update loop stopping due to context cancellation")
			return
		}
	}
}

func (app *App) updatePRs(ctx context.Context) {
	incoming, outgoing, err := app.fetchPRsInternal(ctx)
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
	// Log PRs that were removed (likely merged/closed)
	for i := range app.incoming {
		found := false
		for j := range incoming {
			if app.incoming[i].URL == incoming[j].URL {
				found = true
				break
			}
		}
		if !found {
			log.Printf("[UPDATE] Incoming PR removed (likely merged/closed): %s #%d - %s",
				app.incoming[i].Repository, app.incoming[i].Number, app.incoming[i].URL)
		}
	}
	for i := range app.outgoing {
		found := false
		for j := range outgoing {
			if app.outgoing[i].URL == outgoing[j].URL {
				found = true
				break
			}
		}
		if !found {
			log.Printf("[UPDATE] Outgoing PR removed (likely merged/closed): %s #%d - %s",
				app.outgoing[i].Repository, app.outgoing[i].Number, app.outgoing[i].URL)
		}
	}

	app.incoming = incoming
	app.outgoing = outgoing
	// Mark initial load as complete after first successful update
	if !app.initialLoadComplete {
		app.initialLoadComplete = true
	}
	app.mu.Unlock()

	app.updateMenu(ctx)

	// Process notifications using the simplified state manager
	log.Print("[DEBUG] Processing PR state updates and notifications")
	app.updatePRStatesAndNotify(ctx)
	log.Print("[DEBUG] Completed PR state updates and notifications")
}

// updateMenu rebuilds the menu only if there are changes to improve UX.
func (app *App) updateMenu(ctx context.Context) {
	// Generate current menu titles
	currentTitles := app.generateMenuTitles()
	
	// Compare with last titles to see if rebuild is needed
	app.mu.RLock()
	lastTitles := app.lastMenuTitles
	app.mu.RUnlock()
	
	// Check if titles have changed
	if slices.Equal(currentTitles, lastTitles) {
		log.Printf("[MENU] No changes detected, skipping rebuild (%d items unchanged)", len(currentTitles))
		return
	}
	
	// Titles have changed, rebuild menu
	log.Printf("[MENU] Changes detected, rebuilding menu (%d→%d items)", len(lastTitles), len(currentTitles))
	app.rebuildMenu(ctx)
	
	// Store new titles
	app.mu.Lock()
	app.lastMenuTitles = currentTitles
	app.mu.Unlock()
}


// updatePRsWithWait fetches PRs and waits for Turn data before building initial menu.
func (app *App) updatePRsWithWait(ctx context.Context) {
	incoming, outgoing, err := app.fetchPRsInternal(ctx)
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
			// Store initial menu titles to prevent unnecessary rebuild on first update
			app.mu.Lock()
			app.lastMenuTitles = app.generateMenuTitles()
			app.mu.Unlock()
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

	// Debug logging to track PR states
	blockedIncoming := 0
	for i := range incoming {
		if incoming[i].NeedsReview {
			blockedIncoming++
		}
	}
	blockedOutgoing := 0
	for i := range outgoing {
		if outgoing[i].IsBlocked {
			blockedOutgoing++
			log.Printf("[DEBUG] Blocked outgoing PR: %s #%d (URL: %s)",
				outgoing[i].Repository, outgoing[i].Number, outgoing[i].URL)
		}
	}
	log.Printf("[DEBUG] updatePRsInternal: Setting app state with %d incoming (%d blocked), %d outgoing (%d blocked)",
		len(incoming), blockedIncoming, len(outgoing), blockedOutgoing)

	app.mu.Unlock()

	// Create initial menu after first successful data load
	if !app.menuInitialized {
		// Create initial menu with Turn data
		// Initialize menu structure
		app.rebuildMenu(ctx)
		app.menuInitialized = true
		// Store initial menu titles to prevent unnecessary rebuild on first update
		app.mu.Lock()
		app.lastMenuTitles = app.generateMenuTitles()
		app.mu.Unlock()
		// Menu initialization complete
	} else {
		app.updateMenu(ctx)
	}

	// Process notifications using the simplified state manager
	log.Print("[DEBUG] Processing PR state updates and notifications")
	app.updatePRStatesAndNotify(ctx)
	log.Print("[DEBUG] Completed PR state updates and notifications")
	// Mark initial load as complete after first successful update
	if !app.initialLoadComplete {
		app.mu.Lock()
		app.initialLoadComplete = true
		app.mu.Unlock()
	}
}

// tryAutoOpenPR attempts to open a PR in the browser if enabled and rate limits allow.
func (app *App) tryAutoOpenPR(ctx context.Context, pr PR, autoBrowserEnabled bool, startTime time.Time) {
	if !autoBrowserEnabled {
		return
	}

	if app.browserRateLimiter.CanOpen(startTime, pr.URL) {
		log.Printf("[BROWSER] Auto-opening newly blocked PR: %s #%d - %s",
			pr.Repository, pr.Number, pr.URL)
		// Use strict validation for auto-opened URLs
		// Validate against strict GitHub PR URL pattern for auto-opening
		if err := validateGitHubPRURL(pr.URL); err != nil {
			log.Printf("Auto-open strict validation failed for %s: %v", sanitizeForLog(pr.URL), err)
			return
		}
		if err := openURL(ctx, pr.URL); err != nil {
			log.Printf("[BROWSER] Failed to auto-open PR %s: %v", pr.URL, err)
		} else {
			app.browserRateLimiter.RecordOpen(pr.URL)
			log.Printf("[BROWSER] Successfully opened PR %s #%d in browser",
				pr.Repository, pr.Number)
		}
	}
}

// checkForNewlyBlockedPRs provides backward compatibility for tests
// while using the new state manager internally.
func (app *App) checkForNewlyBlockedPRs(ctx context.Context) {
	// Simply delegate to the new implementation
	app.updatePRStatesAndNotify(ctx)
}
