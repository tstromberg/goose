// Package main implements a cross-platform system tray application for monitoring GitHub pull requests.
// It displays incoming and outgoing PRs, highlighting those that are blocked and need attention.
// The app integrates with the Turn API to provide additional PR metadata and uses the GitHub API
// for fetching PR data.
package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/codeGROOVE-dev/goose/cmd/review-goose/x11tray"
	"github.com/codeGROOVE-dev/retry"
	"github.com/codeGROOVE-dev/turnclient/pkg/turn"
	"github.com/energye/systray"
	"github.com/google/go-github/v57/github"
)

// VERSION file embedded at compile time (created by make release)
//
//go:embed VERSION
var versionFile string

// Version information - set during build with -ldflags.
// If not set via ldflags, getVersion() will read from embedded VERSION file.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

// getVersion returns the version string, preferring ldflags but falling back to VERSION file.
func getVersion() string {
	// If version was set via ldflags and isn't the default, use it
	if version != "" && version != "dev" {
		return version
	}
	// Fall back to embedded VERSION file
	if v := strings.TrimSpace(versionFile); v != "" {
		return v
	}
	return "dev"
}

const (
	cacheTTL                  = 10 * 24 * time.Hour // 10 days - rely mostly on PR UpdatedAt
	runningTestsCacheTTL      = 2 * time.Minute     // Short TTL for PRs with incomplete tests to catch completions quickly
	cacheCleanupInterval      = 15 * 24 * time.Hour // 15 days - cleanup older than cache TTL
	stalePRThreshold          = 90 * 24 * time.Hour
	runningTestsCacheBypass   = 90 * time.Minute // Don't cache PRs with running tests if fresher than this
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
	startupGracePeriod        = 1 * time.Minute // Don't play sounds or auto-open for first minute
	authRetryInterval         = 2 * time.Minute // Retry authentication periodically when in error state
)

// PR represents a pull request with metadata.
type PR struct {
	UpdatedAt         time.Time
	CreatedAt         time.Time
	TurnDataAppliedAt time.Time
	FirstBlockedAt    time.Time // When this PR was first detected as blocked
	Title             string
	URL               string
	Repository        string
	Author            string // GitHub username of the PR author
	ActionReason      string
	ActionKind        string // The kind of action expected (review, merge, fix_tests, etc.)
	TestState         string // Test state from Turn API: "running", "passing", "failing", etc.
	WorkflowState     string // Workflow state from Turn API: "running_tests", "waiting_for_review", etc.
	Number            int
	IsDraft           bool
	IsBlocked         bool
	NeedsReview       bool
	AuthorBot         bool // True if the author is a bot (dependabot, renovate, etc.)
}

// App holds the application state.
type App struct {
	lastSearchAttempt            time.Time
	lastSuccessfulFetch          time.Time
	startTime                    time.Time
	systrayInterface             SystrayInterface
	browserRateLimiter           *BrowserRateLimiter
	blockedPRTimes               map[string]time.Time
	currentUser                  *github.User
	stateManager                 *PRStateManager
	client                       *github.Client
	hiddenOrgs                   map[string]bool
	seenOrgs                     map[string]bool
	turnClient                   *turn.Client
	sprinklerMonitor             *sprinklerMonitor
	previousBlockedPRs           map[string]bool
	githubCircuit                *circuitBreaker
	healthMonitor                *healthMonitor
	cacheDir                     string
	lastFetchError               string
	authError                    string
	targetUser                   string
	lastMenuTitles               []string
	outgoing                     []PR
	incoming                     []PR
	updateInterval               time.Duration
	consecutiveFailures          int
	mu                           sync.RWMutex
	updateMutex                  sync.Mutex
	menuMutex                    sync.Mutex
	hideStaleIncoming            bool
	hasPerformedInitialDiscovery bool
	noCache                      bool
	enableAudioCues              bool
	initialLoadComplete          bool
	menuInitialized              bool
	enableAutoBrowser            bool
}

//nolint:maintidx // Main function complexity is acceptable for initialization logic
func main() {
	// Parse command line flags
	var targetUser string
	var noCache bool
	var debugMode bool
	var showVersion bool
	var updateInterval time.Duration
	var browserOpenDelay time.Duration
	var maxBrowserOpensMinute int
	var maxBrowserOpensDay int
	flag.StringVar(&targetUser, "user", "", "GitHub user to query PRs for (defaults to authenticated user)")
	flag.BoolVar(&noCache, "no-cache", false, "Bypass cache for debugging")
	flag.BoolVar(&debugMode, "debug", false, "Enable debug logging")
	flag.BoolVar(&showVersion, "version", false, "Show version information and exit")
	flag.DurationVar(&updateInterval, "interval", defaultUpdateInterval, "Update interval (e.g. 30s, 1m, 5m)")
	flag.DurationVar(&browserOpenDelay, "browser-delay", 1*time.Minute, "Minimum delay before opening PRs in browser after startup")
	flag.IntVar(&maxBrowserOpensMinute, "browser-max-per-minute", 2, "Maximum browser windows to open per minute")
	flag.IntVar(&maxBrowserOpensDay, "browser-max-per-day", defaultMaxBrowserOpensDay, "Maximum browser windows to open per day")
	flag.Parse()

	// Handle version flag
	if showVersion {
		fmt.Printf("goose version %s\ncommit: %s\nbuilt: %s\n", getVersion(), commit, date)
		os.Exit(0)
	}

	// Validate target user if provided
	if targetUser != "" {
		if err := validateGitHubUsername(targetUser); err != nil {
			slog.Error("Invalid target user", "error", err)
			os.Exit(1)
		}
	}

	// Validate update interval
	if updateInterval < minUpdateInterval {
		slog.Warn("Update interval too short, using minimum", "requested", updateInterval, "minimum", minUpdateInterval)
		updateInterval = minUpdateInterval
	}

	// Validate browser rate limit parameters
	if maxBrowserOpensMinute < 0 {
		slog.Warn("Invalid browser-max-per-minute, using default", "invalid", maxBrowserOpensMinute, "default", 2)
		maxBrowserOpensMinute = 2
	}
	if maxBrowserOpensDay < 0 {
		slog.Warn("Invalid browser-max-per-day, using default", "invalid", maxBrowserOpensDay, "default", defaultMaxBrowserOpensDay)
		maxBrowserOpensDay = defaultMaxBrowserOpensDay
	}
	if browserOpenDelay < 0 {
		slog.Warn("Invalid browser-delay, using default", "invalid", browserOpenDelay, "default", "1m")
		browserOpenDelay = 1 * time.Minute
	}

	// Set up structured logging with source location
	logLevel := slog.LevelInfo
	if debugMode {
		logLevel = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{
		AddSource: true,
		Level:     logLevel,
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, opts)))
	slog.Info("Starting Goose", "version", getVersion(), "commit", commit, "date", date)
	slog.Info("Configuration", "update_interval", updateInterval, "max_retries", maxRetries, "max_delay", maxRetryDelay)
	slog.Info("Browser auto-open configuration",
		"startup_delay", browserOpenDelay,
		"max_per_minute", maxBrowserOpensMinute,
		"max_per_day", maxBrowserOpensDay)

	ctx := context.Background()

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		slog.Error("Failed to get cache directory", "error", err)
		os.Exit(1)
	}
	cacheDir = filepath.Join(cacheDir, "review-goose")
	const dirPerm = 0o700 // Only owner can access cache directory
	if err := os.MkdirAll(cacheDir, dirPerm); err != nil {
		slog.Error("Failed to create cache directory", "error", err)
		os.Exit(1)
	}

	// Set up file-based logging alongside cache
	logDir := filepath.Join(cacheDir, "logs")
	if err := os.MkdirAll(logDir, dirPerm); err != nil {
		slog.Error("Failed to create log directory", "error", err)
		// Continue without file logging
	} else {
		// Create log file with daily rotation
		logPath := filepath.Join(logDir, fmt.Sprintf("goose-%s.log", time.Now().Format("2006-01-02")))
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			slog.Error("Failed to open log file", "error", err)
		} else {
			// Update logger to write to both stderr and file
			multiHandler := &MultiHandler{
				handlers: []slog.Handler{
					slog.NewTextHandler(os.Stderr, opts),
					slog.NewTextHandler(logFile, opts),
				},
			}
			slog.SetDefault(slog.New(multiHandler))
			slog.Info("Logs are being written to", "path", logPath)
		}
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
		healthMonitor:      newHealthMonitor(),
		githubCircuit:      newCircuitBreaker("github", 5, 2*time.Minute),
	}

	// Set app reference in health monitor for sprinkler status
	app.healthMonitor.app = app

	// Load saved settings
	app.loadSettings()

	slog.Info("Initializing GitHub clients...")
	err = app.initClients(ctx)
	if err != nil {
		slog.Warn("Failed to initialize clients", "error", err)
		app.authError = err.Error()
		// Continue running with auth error - will show error in UI
	}

	// Load current user if we have a client
	slog.Info("Loading current user...")
	if app.client != nil {
		var user *github.User
		err := retry.Do(func() error {
			var err error
			user, _, err = app.client.Users.Get(ctx, "")
			if err != nil {
				slog.Warn("GitHub Users.Get failed (will retry)", "error", err)
				return err
			}
			return nil
		},
			retry.Attempts(maxRetries),
			retry.DelayType(retry.CombineDelay(retry.BackOffDelay, retry.RandomDelay)), // Add jitter for better backoff distribution
			retry.MaxDelay(maxRetryDelay),
			retry.OnRetry(func(n uint, err error) {
				slog.Warn("[GITHUB] Users.Get retry", "attempt", n+1, "maxRetries", maxRetries, "error", err)
			}),
			retry.Context(ctx),
		)
		switch {
		case err != nil:
			slog.Warn("Failed to load current user after retries", "maxRetries", maxRetries, "error", err)
			if app.authError == "" {
				app.authError = fmt.Sprintf("Failed to load user: %v", err)
			}
		case user != nil:
			app.currentUser = user
			// Log if we're using a different target user (sanitized)
			if app.targetUser != "" && app.targetUser != user.GetLogin() {
				slog.Info("Querying PRs for different user", "targetUser", sanitizeForLog(app.targetUser))
			}

			// Initialize sprinkler with user's organizations now that we have the user
			go func() {
				if err := app.initSprinklerOrgs(ctx); err != nil {
					slog.Warn("[SPRINKLER] Failed to initialize organizations", "error", err)
				}
			}()
		default:
			slog.Warn("GitHub API returned nil user")
		}
	} else {
		slog.Info("Skipping user load - no GitHub client available")
	}

	slog.Info("Checking system tray availability...")
	trayProxy, err := x11tray.EnsureTray(ctx)
	if err != nil {
		slog.Error("FATAL: System tray unavailable",
			"error", err,
			"help", "Ensure your desktop environment has a system tray, or install snixembed")
		os.Exit(1)
	}

	slog.Info("Starting systray...")
	// Create a cancellable context for the application
	appCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	systray.Run(func() { app.onReady(appCtx) }, func() {
		slog.Info("Shutting down application")
		cancel() // Cancel the context to stop goroutines
		if app.sprinklerMonitor != nil {
			app.sprinklerMonitor.stop()
		}
		// Stop tray proxy if we started one
		if trayProxy != nil {
			slog.Info("Stopping system tray proxy")
			if err := trayProxy.Stop(); err != nil {
				slog.Warn("Failed to stop tray proxy cleanly", "error", err)
			}
		}
		app.cleanupOldCache()
	})
}

// handleReauthentication attempts to re-authenticate when auth errors occur.
func (app *App) handleReauthentication(ctx context.Context) {
	// Try to reinitialize clients which will attempt to get token via gh auth token
	if err := app.initClients(ctx); err != nil {
		slog.Warn("[CLICK] Re-authentication failed", "error", err)
		app.mu.Lock()
		app.authError = err.Error()
		app.mu.Unlock()
		return
	}

	// Success! Clear auth error and reload user
	slog.Info("[CLICK] Re-authentication successful")
	app.mu.Lock()
	app.authError = ""
	app.mu.Unlock()

	// Load current user
	if app.client != nil {
		var user *github.User
		err := retry.Do(func() error {
			var err error
			user, _, err = app.client.Users.Get(ctx, "")
			if err != nil {
				slog.Warn("GitHub Users.Get failed (will retry)", "error", err)
				return err
			}
			return nil
		},
			retry.Attempts(maxRetries),
			retry.DelayType(retry.CombineDelay(retry.BackOffDelay, retry.RandomDelay)),
			retry.MaxDelay(maxRetryDelay),
			retry.OnRetry(func(n uint, err error) {
				slog.Debug("[RETRY] Retrying GitHub API call", "attempt", n, "error", err)
			}),
		)
		if err == nil && user != nil {
			if app.targetUser == "" {
				app.targetUser = user.GetLogin()
				slog.Info("Set target user to current user", "user", app.targetUser)
			}
		}
	}

	// Update tooltip
	tooltip := "Review Goose"
	if app.targetUser != "" {
		tooltip = fmt.Sprintf("Review Goose (@%s)", app.targetUser)
	}
	systray.SetTooltip(tooltip)

	// Rebuild menu to remove error state
	app.rebuildMenu(ctx)

	// Start update loop if not already running
	if !app.menuInitialized {
		app.menuInitialized = true
		go app.updateLoop(ctx)
	} else {
		// Just do a single update to refresh data
		go app.updatePRs(ctx)
	}
}

// authRetryLoop periodically attempts to re-authenticate when in auth error state.
// This ensures the app can recover from transient auth failures without user intervention.
func (app *App) authRetryLoop(ctx context.Context) {
	ticker := time.NewTicker(authRetryInterval)
	defer ticker.Stop()

	slog.Info("[AUTH] Starting auth retry loop", "interval", authRetryInterval)

	for {
		select {
		case <-ctx.Done():
			slog.Info("[AUTH] Auth retry loop stopped (context cancelled)")
			return
		case <-ticker.C:
			app.mu.RLock()
			hasAuthError := app.authError != ""
			app.mu.RUnlock()

			if !hasAuthError {
				slog.Info("[AUTH] Auth error cleared, stopping retry loop")
				return
			}

			slog.Info("[AUTH] Attempting automatic re-authentication")
			app.handleReauthentication(ctx)

			// Check if we recovered
			app.mu.RLock()
			stillHasError := app.authError != ""
			app.mu.RUnlock()

			if !stillHasError {
				slog.Info("[AUTH] Automatic re-authentication successful, stopping retry loop")
				return
			}
			slog.Info("[AUTH] Re-authentication failed, will retry", "nextRetry", authRetryInterval)
		}
	}
}

func (app *App) onReady(ctx context.Context) {
	slog.Info("System tray ready")

	// On Linux, immediately build a minimal menu to ensure it's visible
	if runtime.GOOS == "linux" {
		slog.Info("[LINUX] Building initial minimal menu")
		app.systrayInterface.ResetMenu()
		placeholderItem := app.systrayInterface.AddMenuItem("Loading...", "Goose is starting up")
		if placeholderItem != nil {
			placeholderItem.Disable()
		}
		app.systrayInterface.AddSeparator()
		quitItem := app.systrayInterface.AddMenuItem("Quit", "Quit Goose")
		if quitItem != nil {
			quitItem.Click(func() {
				slog.Info("Quit clicked")
				systray.Quit()
			})
		}
	}

	// Set up click handlers first (needed for both success and error states)
	systray.SetOnClick(func(menu systray.IMenu) {
		slog.Debug("Icon clicked")

		// Check if we're in auth error state and should retry
		app.mu.RLock()
		hasAuthError := app.authError != ""
		app.mu.RUnlock()

		if hasAuthError {
			slog.Info("[CLICK] Auth error detected, attempting to re-authenticate")
			go app.handleReauthentication(ctx)
		} else {
			// Normal operation - check if we can perform a forced refresh
			app.mu.RLock()
			timeSinceLastSearch := time.Since(app.lastSearchAttempt)
			app.mu.RUnlock()

			if timeSinceLastSearch >= minUpdateInterval {
				slog.Info("[CLICK] Forcing search refresh", "lastSearchAgo", timeSinceLastSearch)
				go func() {
					app.updatePRs(ctx)
				}()
			} else {
				remainingTime := minUpdateInterval - timeSinceLastSearch
				slog.Debug("[CLICK] Rate limited", "lastSearchAgo", timeSinceLastSearch, "remaining", remainingTime)
			}
		}

		// On Unix platforms with snixembed, menu display is controlled by physical
		// right-clicks detected by snixembed. Left-click is used for refresh action.
		if menu != nil {
			// On macOS/Windows, show the menu
			if err := menu.ShowMenu(); err != nil {
				slog.Error("Failed to show menu", "error", err)
			}
		}
	})

	systray.SetOnRClick(func(menu systray.IMenu) {
		slog.Debug("Right click detected")
		if menu != nil {
			// On macOS/Windows, explicitly show the menu
			if err := menu.ShowMenu(); err != nil {
				slog.Error("Failed to show menu", "error", err)
			}
		}
		// On Unix platforms with snixembed, the menu is automatically shown
		// by snixembed when it detects the right-click
	})

	// Check if we have an auth error
	if app.authError != "" {
		systray.SetTitle("")
		app.setTrayIcon(IconLock, PRCounts{})
		systray.SetTooltip("Goose - Authentication Error")
		// Create initial error menu
		app.rebuildMenu(ctx)
		// Clean old cache on startup
		app.cleanupOldCache()
		// Start background auth retry loop
		go app.authRetryLoop(ctx)
		return
	}

	systray.SetTitle("")
	app.setTrayIcon(IconSmiling, PRCounts{}) // Start with smiling icon while loading

	// Set tooltip based on whether we're using a custom user
	tooltip := "Review Goose"
	if app.targetUser != "" {
		tooltip = fmt.Sprintf("Review Goose (@%s)", app.targetUser)
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
			slog.Error("PANIC in update loop", "panic", r)

			// Set error state in UI
			systray.SetTitle("")
			app.setTrayIcon(IconWarning, PRCounts{})
			systray.SetTooltip("Goose - Critical error")

			// Update failure count
			app.mu.Lock()
			app.consecutiveFailures += panicFailureIncrement // Treat panic as critical failure
			app.mu.Unlock()

			// Signal app to quit after panic
			slog.Error("Update loop panic - signaling quit")
			systray.Quit()
		}
	}()

	ticker := time.NewTicker(app.updateInterval)
	defer ticker.Stop()

	// Health monitoring ticker - log metrics every 5 minutes
	healthTicker := time.NewTicker(5 * time.Minute)
	defer healthTicker.Stop()

	slog.Info("[UPDATE] Update loop started", "interval", app.updateInterval)

	// Initial update with wait for Turn data
	app.updatePRsWithWait(ctx)

	for {
		select {
		case <-healthTicker.C:
			// Log health metrics periodically
			if app.healthMonitor != nil {
				app.healthMonitor.logMetrics()
			}
		case <-ticker.C:
			// Check if we should skip this scheduled update due to recent forced refresh
			app.mu.RLock()
			timeSinceLastSearch := time.Since(app.lastSearchAttempt)
			app.mu.RUnlock()

			if timeSinceLastSearch >= minUpdateInterval {
				slog.Debug("Running scheduled PR update")
				app.updatePRs(ctx)
			} else {
				remainingTime := minUpdateInterval - timeSinceLastSearch
				slog.Debug("Skipping scheduled update", "recentSearchAgo", timeSinceLastSearch, "remaining", remainingTime)
			}
		case <-ctx.Done():
			slog.Info("Update loop stopping due to context cancellation")
			return
		}
	}
}

func (app *App) updatePRs(ctx context.Context) {
	// Prevent concurrent updates
	if !app.updateMutex.TryLock() {
		slog.Debug("[UPDATE] Update already in progress, skipping")
		return
	}
	defer app.updateMutex.Unlock()

	var incoming, outgoing []PR
	err := safeExecute("fetchPRs", func() error {
		var err error
		incoming, outgoing, err = app.fetchPRsInternal(ctx)
		return err
	})
	if err != nil {
		slog.Error("Error fetching PRs", "error", err)
		app.mu.Lock()
		app.consecutiveFailures++
		failureCount := app.consecutiveFailures
		app.lastFetchError = err.Error()
		app.mu.Unlock()

		// Progressive degradation based on failure count
		var tooltip string
		var iconType IconType
		switch {
		case failureCount <= minorFailureThreshold:
			iconType = IconWarning
			tooltip = fmt.Sprintf("Goose - %d consecutive failures", failureCount)
		default:
			iconType = IconWarning
			tooltip = "Goose - Connection failures, check network/auth"
		}

		systray.SetTitle("")
		app.setTrayIcon(iconType, PRCounts{})

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
	previousFailures := app.consecutiveFailures
	app.lastSuccessfulFetch = time.Now()
	app.consecutiveFailures = 0
	app.lastFetchError = ""
	app.mu.Unlock()

	// Restore normal tray icon after successful fetch
	if previousFailures > 0 {
		slog.Info("[RECOVERY] Network recovered, restoring tray icon",
			"previousFailures", previousFailures)
	}
	app.setTrayTitle()

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
			slog.Info("[UPDATE] Incoming PR removed (likely merged/closed)",
				"repo", app.incoming[i].Repository, "number", app.incoming[i].Number, "url", app.incoming[i].URL)
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
			slog.Info("[UPDATE] Outgoing PR removed (likely merged/closed)",
				"repo", app.outgoing[i].Repository, "number", app.outgoing[i].Number, "url", app.outgoing[i].URL)
		}
	}

	app.incoming = incoming
	app.outgoing = outgoing
	slog.Info("[UPDATE] PR counts after update",
		"incoming_count", len(incoming),
		"outgoing_count", len(outgoing))
	// Log ALL outgoing PRs for debugging
	slog.Debug("[UPDATE] Listing ALL outgoing PRs for debugging")
	for i := range outgoing {
		slog.Debug("[UPDATE] Outgoing PR details",
			"index", i,
			"repo", outgoing[i].Repository,
			"number", outgoing[i].Number,
			"blocked", outgoing[i].IsBlocked,
			"updated_at", outgoing[i].UpdatedAt.Format(time.RFC3339),
			"title", outgoing[i].Title,
			"url", outgoing[i].URL)
	}
	// Mark initial load as complete after first successful update
	if !app.initialLoadComplete {
		app.initialLoadComplete = true
	}
	app.mu.Unlock()

	app.updateMenu(ctx)

	// Process notifications using the simplified state manager
	slog.Debug("[DEBUG] Processing PR state updates and notifications")
	app.processNotifications(ctx)
	slog.Debug("[DEBUG] Completed PR state updates and notifications")
}

// updateMenu rebuilds the menu only if there are changes to improve UX.
func (app *App) updateMenu(ctx context.Context) {
	slog.Debug("[MENU] updateMenu called, generating current titles")
	// Generate current menu titles
	currentTitles := app.generateMenuTitles()

	// Compare with last titles to see if rebuild is needed
	app.mu.RLock()
	lastTitles := app.lastMenuTitles
	app.mu.RUnlock()

	// Check if titles have changed
	if slices.Equal(currentTitles, lastTitles) {
		slog.Debug("[MENU] No changes detected, skipping rebuild", "itemCount", len(currentTitles))
		return
	}

	// Titles have changed, rebuild menu
	slog.Info("[MENU] Changes detected, rebuilding menu", "oldCount", len(lastTitles), "newCount", len(currentTitles))

	// Log what changed for debugging
	for i, current := range currentTitles {
		if i < len(lastTitles) {
			if current != lastTitles[i] {
				slog.Debug("[MENU] Title changed", "index", i, "old", lastTitles[i], "new", current)
			}
		} else {
			slog.Debug("[MENU] New title added", "index", i, "title", current)
		}
	}
	for i := len(currentTitles); i < len(lastTitles); i++ {
		slog.Debug("[MENU] Title removed", "index", i, "title", lastTitles[i])
	}

	app.rebuildMenu(ctx)

	// Store new titles
	app.mu.Lock()
	app.lastMenuTitles = currentTitles
	app.mu.Unlock()
}

// updatePRsWithWait fetches PRs and waits for Turn data before building initial menu.
func (app *App) updatePRsWithWait(ctx context.Context) {
	// Prevent concurrent updates
	if !app.updateMutex.TryLock() {
		slog.Debug("[UPDATE] Update already in progress, skipping")
		return
	}
	defer app.updateMutex.Unlock()

	incoming, outgoing, err := app.fetchPRsInternal(ctx)
	if err != nil {
		slog.Error("Error fetching PRs", "error", err)
		app.mu.Lock()
		app.consecutiveFailures++
		failureCount := app.consecutiveFailures
		app.lastFetchError = err.Error()
		app.mu.Unlock()

		// Progressive degradation based on failure count
		var tooltip string
		var iconType IconType
		switch {
		case failureCount <= minorFailureThreshold:
			iconType = IconWarning
			tooltip = fmt.Sprintf("Goose - %d consecutive failures", failureCount)
		default:
			iconType = IconWarning
			tooltip = "Goose - Connection failures, check network/auth"
		}

		systray.SetTitle("")
		app.setTrayIcon(iconType, PRCounts{})
		systray.SetTooltip(tooltip)

		// Create or update menu to show error state
		if !app.menuInitialized {
			// Create initial menu despite error
			app.rebuildMenu(ctx)
			app.menuInitialized = true
			// Store initial menu titles to prevent unnecessary rebuild on first update
			// generateMenuTitles acquires its own read lock, so we can't hold a lock here
			menuTitles := app.generateMenuTitles()
			app.mu.Lock()
			app.lastMenuTitles = menuTitles
			app.mu.Unlock()
			// Menu initialization complete
		} else if failureCount == 1 {
			// On first failure, rebuild menu to show error at top
			app.rebuildMenu(ctx)
		}
		return
	}

	// Update health status on success
	app.mu.Lock()
	previousFailures := app.consecutiveFailures
	app.lastSuccessfulFetch = time.Now()
	app.consecutiveFailures = 0
	app.lastFetchError = ""
	app.mu.Unlock()

	// Restore normal tray icon after successful fetch
	if previousFailures > 0 {
		slog.Info("[RECOVERY] Network recovered, restoring tray icon",
			"previousFailures", previousFailures)
	}
	app.setTrayTitle()

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
			slog.Debug("[DEBUG] Blocked outgoing PR",
				"repo", outgoing[i].Repository, "number", outgoing[i].Number, "url", outgoing[i].URL)
		}
	}
	slog.Debug("[DEBUG] updatePRsInternal: Setting app state",
		"incoming", len(incoming), "blockedIncoming", blockedIncoming,
		"outgoing", len(outgoing), "blockedOutgoing", blockedOutgoing)

	app.mu.Unlock()

	// Create initial menu after first successful data load
	if !app.menuInitialized {
		// Create initial menu with Turn data
		// Initialize menu structure
		slog.Info("[FLOW] Creating initial menu (first time)")
		app.rebuildMenu(ctx)
		app.menuInitialized = true
		// Store initial menu titles to prevent unnecessary rebuild on first update
		// generateMenuTitles acquires its own read lock, so we can't hold a lock here
		menuTitles := app.generateMenuTitles()
		app.mu.Lock()
		app.lastMenuTitles = menuTitles
		app.mu.Unlock()
		// Menu initialization complete
		slog.Info("[FLOW] Initial menu created successfully")
	} else {
		slog.Info("[FLOW] Updating existing menu")
		app.updateMenu(ctx)
		slog.Info("[FLOW] Menu update completed")
	}

	// Process notifications using the simplified state manager
	slog.Info("[FLOW] About to process PR state updates and notifications")
	app.processNotifications(ctx)
	slog.Info("[FLOW] Completed PR state updates and notifications")
	// Mark initial load as complete after first successful update
	if !app.initialLoadComplete {
		app.mu.Lock()
		app.initialLoadComplete = true
		app.mu.Unlock()
	}
}

// tryAutoOpenPR attempts to open a PR in the browser if enabled and rate limits allow.
func (app *App) tryAutoOpenPR(ctx context.Context, pr *PR, autoBrowserEnabled bool, startTime time.Time) {
	slog.Debug("[BROWSER] tryAutoOpenPR called",
		"repo", pr.Repository,
		"number", pr.Number,
		"enabled", autoBrowserEnabled,
		"time_since_start", time.Since(startTime).Round(time.Second))

	if !autoBrowserEnabled {
		slog.Debug("[BROWSER] Auto-open disabled, skipping")
		return
	}

	// Skip draft PRs authored by the user we're querying for
	queriedUser := app.targetUser
	if queriedUser == "" && app.currentUser != nil {
		queriedUser = app.currentUser.GetLogin()
	}
	if pr.IsDraft && pr.Author == queriedUser {
		slog.Debug("[BROWSER] Skipping auto-open for draft PR by queried user",
			"repo", pr.Repository, "number", pr.Number, "author", pr.Author)
		return
	}

	// Only auto-open if the PR is actually blocked or needs review
	// This ensures we have a valid NextAction before opening
	if !pr.IsBlocked && !pr.NeedsReview {
		slog.Debug("[BROWSER] Skipping auto-open for non-blocked PR",
			"repo", pr.Repository, "number", pr.Number,
			"is_blocked", pr.IsBlocked, "needs_review", pr.NeedsReview)
		return
	}

	if app.browserRateLimiter.CanOpen(startTime, pr.URL) {
		slog.Info("[BROWSER] Auto-opening newly blocked PR",
			"repo", pr.Repository,
			"number", pr.Number,
			"url", pr.URL,
			"workflow_state", pr.WorkflowState,
			"test_state", pr.TestState,
			"is_draft", pr.IsDraft,
			"age_since_creation", time.Since(pr.CreatedAt).Round(time.Second),
			"age_since_update", time.Since(pr.UpdatedAt).Round(time.Second))
		// Use strict GitHub PR validation for auto-opening
		// Use ActionKind as the goose parameter value, or "next_action" if not set
		gooseParam := pr.ActionKind
		if gooseParam == "" {
			gooseParam = "next_action"
		}

		// OpenWithParams will validate the URL and add the goose parameter
		if err := openURL(ctx, pr.URL, gooseParam); err != nil {
			slog.Error("[BROWSER] Failed to auto-open PR", "url", sanitizeForLog(pr.URL), "error", err)
		} else {
			app.browserRateLimiter.RecordOpen(pr.URL)
			slog.Info("[BROWSER] Successfully opened PR in browser",
				"repo", pr.Repository, "number", pr.Number, "action", pr.ActionKind, "goose_param", gooseParam)
		}
	}
}

// checkForNewlyBlockedPRs provides backward compatibility for tests.
func (app *App) checkForNewlyBlockedPRs(ctx context.Context) {
	app.processNotifications(ctx)
}
