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
	defaultMaxBrowserOpensDay = 20
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
	startTime           time.Time
	client              *github.Client
	turnClient          *turn.Client
	currentUser         *github.User
	previousBlockedPRs  map[string]bool
	blockedPRTimes      map[string]time.Time
	browserRateLimiter  *BrowserRateLimiter
	targetUser          string
	cacheDir            string
	authError           string
	pendingTurnResults  []TurnResult
	lastMenuTitles      []string
	incoming            []PR
	outgoing            []PR
	updateInterval      time.Duration
	consecutiveFailures int
	mu                  sync.RWMutex
	loadingTurnData     bool
	menuInitialized     bool
	initialLoadComplete bool
	enableAudioCues     bool
	enableAutoBrowser   bool
	hideStaleIncoming   bool
	noCache             bool
	hiddenOrgs          map[string]bool
	seenOrgs            map[string]bool
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

	app := &App{
		cacheDir:           cacheDir,
		hideStaleIncoming:  true,
		previousBlockedPRs: make(map[string]bool),
		blockedPRTimes:     make(map[string]time.Time),
		targetUser:         targetUser,
		noCache:            noCache,
		updateInterval:     updateInterval,
		pendingTurnResults: make([]TurnResult, 0),
		enableAudioCues:    true,
		enableAutoBrowser:  false, // Default to false for safety
		browserRateLimiter: NewBrowserRateLimiter(browserOpenDelay, maxBrowserOpensMinute, maxBrowserOpensDay),
		startTime:          time.Now(),
		seenOrgs:           make(map[string]bool),
		hiddenOrgs:         make(map[string]bool),
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

// tryAutoOpenPR attempts to open a PR in the browser if enabled and rate limits allow.
func (app *App) tryAutoOpenPR(ctx context.Context, pr PR, autoBrowserEnabled bool, startTime time.Time) {
	if !autoBrowserEnabled {
		return
	}

	if app.browserRateLimiter.CanOpen(startTime, pr.URL) {
		log.Printf("[BROWSER] Auto-opening newly blocked PR: %s #%d - %s",
			pr.Repository, pr.Number, pr.URL)
		// Use strict validation for auto-opened URLs
		if err := openURLAutoStrict(ctx, pr.URL); err != nil {
			log.Printf("[BROWSER] Failed to auto-open PR %s: %v", pr.URL, err)
		} else {
			app.browserRateLimiter.RecordOpen(pr.URL)
			log.Printf("[BROWSER] Successfully opened PR %s #%d in browser",
				pr.Repository, pr.Number)
		}
	}
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
		log.Printf("[SOUND] Playing %s sound for PR: %s #%d - %s", soundType, pr.Repository, pr.Number, pr.Title)
		app.playSound(ctx, soundType)
		*playedSound = true
	}
}

// checkForNewlyBlockedPRs sends notifications for blocked PRs.
func (app *App) checkForNewlyBlockedPRs(ctx context.Context) {
	// Check for context cancellation early
	select {
	case <-ctx.Done():
		log.Print("[BLOCKED] Context cancelled, skipping newly blocked PR check")
		return
	default:
	}

	app.mu.Lock()
	// Make deep copies to work with while holding the lock
	incoming := make([]PR, len(app.incoming))
	copy(incoming, app.incoming)
	outgoing := make([]PR, len(app.outgoing))
	copy(outgoing, app.outgoing)
	previousBlocked := app.previousBlockedPRs

	// Clean up blockedPRTimes first to remove stale entries
	// Only keep blocked times for PRs that are actually in the current lists
	cleanedBlockedTimes := make(map[string]time.Time)
	for i := range app.incoming {
		if blockTime, exists := app.blockedPRTimes[app.incoming[i].URL]; exists {
			cleanedBlockedTimes[app.incoming[i].URL] = blockTime
		}
	}
	for i := range app.outgoing {
		if blockTime, exists := app.blockedPRTimes[app.outgoing[i].URL]; exists {
			cleanedBlockedTimes[app.outgoing[i].URL] = blockTime
		}
	}

	// Get hidden orgs for filtering
	hiddenOrgs := make(map[string]bool)
	for org, hidden := range app.hiddenOrgs {
		hiddenOrgs[org] = hidden
	}

	// Log any removed entries
	removedCount := 0
	for url := range app.blockedPRTimes {
		if _, exists := cleanedBlockedTimes[url]; !exists {
			log.Printf("[BLOCKED] Removing stale blocked time for PR no longer in list: %s", url)
			removedCount++
		}
	}
	if removedCount > 0 {
		log.Printf("[BLOCKED] Cleaned up %d stale blocked PR times", removedCount)
	}

	// Update the app's blockedPRTimes to the cleaned version to prevent memory growth
	app.blockedPRTimes = cleanedBlockedTimes
	blockedTimes := cleanedBlockedTimes
	autoBrowserEnabled := app.enableAutoBrowser
	startTime := app.startTime
	hideStaleIncoming := app.hideStaleIncoming
	app.mu.Unlock()

	currentBlocked := make(map[string]bool)
	newBlockedTimes := make(map[string]time.Time)
	playedHonk := false
	playedJet := false
	now := time.Now()
	staleThreshold := now.Add(-stalePRThreshold)

	// Check incoming PRs
	for i := range incoming {
		// Skip PRs from hidden orgs for notifications
		org := extractOrgFromRepo(incoming[i].Repository)
		if org != "" && hiddenOrgs[org] {
			continue
		}

		if incoming[i].NeedsReview {
			currentBlocked[incoming[i].URL] = true
			// Track when first blocked
			if blockedTime, exists := blockedTimes[incoming[i].URL]; exists {
				newBlockedTimes[incoming[i].URL] = blockedTime
				incoming[i].FirstBlockedAt = blockedTime
			} else if !previousBlocked[incoming[i].URL] {
				// Newly blocked PR
				newBlockedTimes[incoming[i].URL] = now
				incoming[i].FirstBlockedAt = now
				log.Printf("[BLOCKED] Setting FirstBlockedAt for incoming PR: %s #%d at %v",
					incoming[i].Repository, incoming[i].Number, now)

				// Skip sound and auto-open for stale PRs when hideStaleIncoming is enabled
				isStale := incoming[i].UpdatedAt.Before(staleThreshold)
				if hideStaleIncoming && isStale {
					log.Printf("[BLOCKED] New incoming PR blocked (stale, skipping): %s #%d - %s",
						incoming[i].Repository, incoming[i].Number, incoming[i].Title)
				} else {
					log.Printf("[BLOCKED] New incoming PR blocked: %s #%d - %s",
						incoming[i].Repository, incoming[i].Number, incoming[i].Title)
					app.notifyWithSound(ctx, incoming[i], true, &playedHonk)
					app.tryAutoOpenPR(ctx, incoming[i], autoBrowserEnabled, startTime)
				}
			}
		}
	}

	// Check outgoing PRs
	for i := range outgoing {
		// Skip PRs from hidden orgs for notifications
		org := extractOrgFromRepo(outgoing[i].Repository)
		if org != "" && hiddenOrgs[org] {
			continue
		}

		if outgoing[i].IsBlocked {
			currentBlocked[outgoing[i].URL] = true
			// Track when first blocked
			if blockedTime, exists := blockedTimes[outgoing[i].URL]; exists {
				newBlockedTimes[outgoing[i].URL] = blockedTime
				outgoing[i].FirstBlockedAt = blockedTime
			} else if !previousBlocked[outgoing[i].URL] {
				// Newly blocked PR
				newBlockedTimes[outgoing[i].URL] = now
				outgoing[i].FirstBlockedAt = now
				log.Printf("[BLOCKED] Setting FirstBlockedAt for outgoing PR: %s #%d at %v",
					outgoing[i].Repository, outgoing[i].Number, now)

				// Skip sound and auto-open for stale PRs when hideStaleIncoming is enabled
				isStale := outgoing[i].UpdatedAt.Before(staleThreshold)
				if hideStaleIncoming && isStale {
					log.Printf("[BLOCKED] New outgoing PR blocked (stale, skipping): %s #%d - %s",
						outgoing[i].Repository, outgoing[i].Number, outgoing[i].Title)
				} else {
					// Add delay if we already played honk sound
					if playedHonk && !playedJet {
						time.Sleep(2 * time.Second)
					}
					log.Printf("[BLOCKED] New outgoing PR blocked: %s #%d - %s",
						outgoing[i].Repository, outgoing[i].Number, outgoing[i].Title)
					app.notifyWithSound(ctx, outgoing[i], false, &playedJet)
					app.tryAutoOpenPR(ctx, outgoing[i], autoBrowserEnabled, startTime)
				}
			}
		}
	}

	// Update state with a lock
	app.mu.Lock()
	app.previousBlockedPRs = currentBlocked
	app.blockedPRTimes = newBlockedTimes
	// Update the PR lists with FirstBlockedAt times
	app.incoming = incoming
	app.outgoing = outgoing
	menuInitialized := app.menuInitialized
	app.mu.Unlock()

	// Update UI after releasing the lock
	// Check if the set of blocked PRs has changed
	blockedPRsChanged := !reflect.DeepEqual(currentBlocked, previousBlocked)

	// Update UI if blocked PRs changed or if we cleaned up stale entries
	if menuInitialized && (blockedPRsChanged || removedCount > 0) {
		switch {
		case len(currentBlocked) > len(previousBlocked):
			log.Print("[BLOCKED] Updating UI for newly blocked PRs")
		case len(currentBlocked) < len(previousBlocked):
			log.Print("[BLOCKED] Updating UI - blocked PRs were removed")
		case blockedPRsChanged:
			log.Print("[BLOCKED] Updating UI - blocked PRs changed (same count)")
		default:
			log.Printf("[BLOCKED] Updating UI after cleaning up %d stale entries", removedCount)
		}
		// updateMenu will call setTrayTitle via rebuildMenu
		app.updateMenu(ctx)
	}
}
