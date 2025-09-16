// Package main - ui.go handles system tray UI and menu management.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/energye/systray" // needed for MenuItem type
)

// Ensure systray package is used.
var _ *systray.MenuItem = nil

// openURL safely opens a URL in the default browser after validation.
func openURL(ctx context.Context, rawURL string) error {
	// Parse and validate the URL
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}

	// Security validation: scheme, host whitelist, no userinfo
	if u.Scheme != "https" {
		return fmt.Errorf("invalid url scheme: %s (only https allowed)", u.Scheme)
	}

	allowedHosts := map[string]bool{
		"github.com":               true,
		"www.github.com":           true,
		"dash.ready-to-review.dev": true,
	}
	if !allowedHosts[u.Host] {
		return fmt.Errorf("invalid host: %s", u.Host)
	}

	if u.User != nil {
		return errors.New("URLs with user info are not allowed")
	}

	// Add goose=1 parameter to track source for GitHub and dash URLs
	if u.Host == "github.com" || u.Host == "www.github.com" || u.Host == "dash.ready-to-review.dev" {
		q := u.Query()
		q.Set("goose", "1")
		u.RawQuery = q.Encode()
		rawURL = u.String()
	}

	// Execute the open command based on OS
	// Use safer methods that don't invoke shell interpretation
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		// Use open command with explicit arguments to prevent injection
		slog.Debug("Executing command", "command", "/usr/bin/open", "url", rawURL)
		cmd = exec.CommandContext(ctx, "/usr/bin/open", "-u", rawURL)
	case "windows":
		// Use rundll32 to open URL safely without cmd shell
		slog.Debug("Executing command", "command", "rundll32.exe url.dll,FileProtocolHandler", "url", rawURL)
		cmd = exec.CommandContext(ctx, "rundll32.exe", "url.dll,FileProtocolHandler", rawURL)
	case "linux":
		// Use xdg-open with full path
		slog.Debug("Executing command", "command", "/usr/bin/xdg-open", "url", rawURL)
		cmd = exec.CommandContext(ctx, "/usr/bin/xdg-open", rawURL)
	default:
		// Try xdg-open for other Unix-like systems
		slog.Debug("Executing command", "command", "/usr/bin/xdg-open", "url", rawURL)
		cmd = exec.CommandContext(ctx, "/usr/bin/xdg-open", rawURL)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open url: %w", err)
	}

	// Don't wait for the command to finish - browser launch is fire-and-forget
	// The OS will handle the browser process lifecycle

	return nil
}

// PRCounts represents PR count information.
type PRCounts struct {
	IncomingTotal   int
	IncomingBlocked int
	OutgoingTotal   int
	OutgoingBlocked int
}

// countPRs counts the number of PRs that need review/are blocked.
func (app *App) countPRs() PRCounts {
	app.mu.RLock()
	defer app.mu.RUnlock()

	var incomingCount, incomingBlocked, outgoingCount, outgoingBlocked int

	// Pre-calculate stale threshold to avoid repeated time calculations
	now := time.Now()
	staleThreshold := now.Add(-stalePRThreshold)

	for i := range app.incoming {
		// Check if org is hidden
		org := extractOrgFromRepo(app.incoming[i].Repository)
		if org != "" && app.hiddenOrgs[org] {
			continue
		}

		if !app.hideStaleIncoming || app.incoming[i].UpdatedAt.After(staleThreshold) {
			incomingCount++
			if app.incoming[i].NeedsReview {
				incomingBlocked++
			}
		}
	}

	for i := range app.outgoing {
		// Check if org is hidden
		org := extractOrgFromRepo(app.outgoing[i].Repository)
		if org != "" && app.hiddenOrgs[org] {
			continue
		}

		if !app.hideStaleIncoming || app.outgoing[i].UpdatedAt.After(staleThreshold) {
			outgoingCount++
			if app.outgoing[i].IsBlocked {
				outgoingBlocked++
			}
		}
	}
	return PRCounts{
		IncomingTotal:   incomingCount,
		IncomingBlocked: incomingBlocked,
		OutgoingTotal:   outgoingCount,
		OutgoingBlocked: outgoingBlocked,
	}
}

// setTrayTitle updates the system tray title based on PR counts.
func (app *App) setTrayTitle() {
	counts := app.countPRs()

	// Set title based on PR state
	var title string
	switch {
	case counts.IncomingBlocked == 0 && counts.OutgoingBlocked == 0:
		title = "😊"
	case counts.IncomingBlocked > 0 && counts.OutgoingBlocked > 0:
		title = fmt.Sprintf("🪿 %d 🎉 %d", counts.IncomingBlocked, counts.OutgoingBlocked)
	case counts.IncomingBlocked > 0:
		title = fmt.Sprintf("🪿 %d", counts.IncomingBlocked)
	default:
		title = fmt.Sprintf("🎉 %d", counts.OutgoingBlocked)
	}

	// Log title change with detailed counts
	slog.Debug("[TRAY] Setting title",
		"title", title,
		"incoming_total", counts.IncomingTotal,
		"incoming_blocked", counts.IncomingBlocked,
		"outgoing_total", counts.OutgoingTotal,
		"outgoing_blocked", counts.OutgoingBlocked)
	app.systrayInterface.SetTitle(title)
}

// addPRSection adds a section of PRs to the menu.
func (app *App) addPRSection(ctx context.Context, prs []PR, sectionTitle string, blockedCount int) {
	if len(prs) == 0 {
		return
	}

	// Add header
	headerText := fmt.Sprintf("%s — %d blocked on you", sectionTitle, blockedCount)
	// Create section header
	header := app.systrayInterface.AddMenuItem(headerText, "")
	header.Disable()

	// Sort PRs with blocked ones first - inline for simplicity
	sortedPRs := make([]PR, len(prs))
	copy(sortedPRs, prs)
	sort.SliceStable(sortedPRs, func(i, j int) bool {
		// First priority: blocked status
		if sortedPRs[i].NeedsReview != sortedPRs[j].NeedsReview {
			return sortedPRs[i].NeedsReview // true (blocked) comes before false
		}
		if sortedPRs[i].IsBlocked != sortedPRs[j].IsBlocked {
			return sortedPRs[i].IsBlocked // true (blocked) comes before false
		}
		// Second priority: more recent PRs first
		return sortedPRs[i].UpdatedAt.After(sortedPRs[j].UpdatedAt)
	})

	// Get hidden orgs with proper locking
	app.mu.RLock()
	hiddenOrgs := make(map[string]bool)
	for org, hidden := range app.hiddenOrgs {
		hiddenOrgs[org] = hidden
	}
	hideStale := app.hideStaleIncoming
	app.mu.RUnlock()

	// Add PR items in sorted order
	for prIndex := range sortedPRs {
		// Apply filters
		// Skip PRs from hidden orgs
		org := extractOrgFromRepo(sortedPRs[prIndex].Repository)
		if org != "" && hiddenOrgs[org] {
			continue
		}

		// Skip stale PRs if configured
		if hideStale && sortedPRs[prIndex].UpdatedAt.Before(time.Now().Add(-stalePRThreshold)) {
			continue
		}

		title := fmt.Sprintf("%s #%d", sortedPRs[prIndex].Repository, sortedPRs[prIndex].Number)
		// Add bullet point or emoji for blocked PRs
		if sortedPRs[prIndex].NeedsReview || sortedPRs[prIndex].IsBlocked {
			// Get the blocked time from state manager
			prState, hasState := app.stateManager.PRState(sortedPRs[prIndex].URL)

			// Show emoji for PRs blocked within the last 5 minutes
			if hasState && !prState.FirstBlockedAt.IsZero() && time.Since(prState.FirstBlockedAt) < blockedPRIconDuration {
				timeSinceBlocked := time.Since(prState.FirstBlockedAt)
				// Use party popper for outgoing PRs, goose for incoming PRs
				if sectionTitle == "Outgoing" {
					title = fmt.Sprintf("🎉 %s", title)
					slog.Debug("[MENU] Adding party popper to outgoing PR",
						"url", sortedPRs[prIndex].URL,
						"blocked_ago", timeSinceBlocked,
						"remaining", blockedPRIconDuration-timeSinceBlocked)
				} else {
					title = fmt.Sprintf("🪿 %s", title)
					slog.Debug("[MENU] Adding goose to incoming PR",
						"url", sortedPRs[prIndex].URL,
						"blocked_ago", timeSinceBlocked,
						"remaining", blockedPRIconDuration-timeSinceBlocked)
				}
			} else {
				title = fmt.Sprintf("• %s", title)
				// Log when we transition from emoji to bullet point
				if hasState && !prState.FirstBlockedAt.IsZero() {
					timeSinceBlocked := time.Since(prState.FirstBlockedAt)
					if sectionTitle == "Outgoing" {
						slog.Debug("[MENU] Removing party popper from outgoing PR",
							"url", sortedPRs[prIndex].URL,
							"blocked_ago", timeSinceBlocked,
							"duration", blockedPRIconDuration)
					} else {
						slog.Debug("[MENU] Removing goose from incoming PR",
							"url", sortedPRs[prIndex].URL,
							"blocked_ago", timeSinceBlocked,
							"duration", blockedPRIconDuration)
					}
				}
			}
		}
		// Format age inline for tooltip
		duration := time.Since(sortedPRs[prIndex].UpdatedAt)
		var age string
		switch {
		case duration < time.Hour:
			age = fmt.Sprintf("%dm", int(duration.Minutes()))
		case duration < 24*time.Hour:
			age = fmt.Sprintf("%dh", int(duration.Hours()))
		case duration < 30*24*time.Hour:
			age = fmt.Sprintf("%dd", int(duration.Hours()/24))
		case duration < 365*24*time.Hour:
			age = fmt.Sprintf("%dmo", int(duration.Hours()/(24*30)))
		default:
			age = sortedPRs[prIndex].UpdatedAt.Format("2006")
		}
		tooltip := fmt.Sprintf("%s (%s)", sortedPRs[prIndex].Title, age)
		// Add action reason for blocked PRs
		if (sortedPRs[prIndex].NeedsReview || sortedPRs[prIndex].IsBlocked) && sortedPRs[prIndex].ActionReason != "" {
			tooltip = fmt.Sprintf("%s - %s", tooltip, sortedPRs[prIndex].ActionReason)
		}

		// Create PR menu item
		item := app.systrayInterface.AddMenuItem(title, tooltip)

		// Capture URL to avoid loop variable capture bug
		prURL := sortedPRs[prIndex].URL
		item.Click(func() {
			if err := openURL(ctx, prURL); err != nil {
				slog.Error("failed to open url", "error", err)
			}
		})
	}
}

// generateMenuTitles generates the list of menu item titles that would be shown
// without actually building the UI. Used for change detection.
func (app *App) generateMenuTitles() []string {
	var titles []string

	// Check for auth error first
	if app.authError != "" {
		titles = append(titles,
			"⚠️ Authentication Error",
			app.authError,
			"To fix this issue:",
			"1. Install GitHub CLI: brew install gh",
			"2. Run: gh auth login",
			"3. Or set GITHUB_TOKEN environment variable",
			"Quit")
		return titles
	}

	app.mu.RLock()
	incoming := make([]PR, len(app.incoming))
	copy(incoming, app.incoming)
	outgoing := make([]PR, len(app.outgoing))
	copy(outgoing, app.outgoing)
	hiddenOrgs := make(map[string]bool)
	for org, hidden := range app.hiddenOrgs {
		hiddenOrgs[org] = hidden
	}
	hideStale := app.hideStaleIncoming
	app.mu.RUnlock()

	// Add common menu items
	titles = append(titles, "Web Dashboard")

	// Generate PR section titles
	if len(incoming) == 0 && len(outgoing) == 0 {
		titles = append(titles, "No pull requests")
	} else {
		// Add incoming PR titles
		if len(incoming) > 0 {
			titles = append(titles, "📥 Incoming PRs")
			titles = append(titles, app.generatePRSectionTitles(incoming, "Incoming", hiddenOrgs, hideStale)...)
		}

		// Add outgoing PR titles
		if len(outgoing) > 0 {
			titles = append(titles, "📤 Outgoing PRs")
			titles = append(titles, app.generatePRSectionTitles(outgoing, "Outgoing", hiddenOrgs, hideStale)...)
		}
	}

	// Add settings menu items
	titles = append(titles,
		"⚙️ Settings",
		"Hide Stale Incoming PRs",
		"Honks enabled",
		"Auto-open in Browser",
		"Hidden Organizations",
		"Quit")

	return titles
}

// generatePRSectionTitles generates the titles for a specific PR section.
func (app *App) generatePRSectionTitles(prs []PR, sectionTitle string, hiddenOrgs map[string]bool, hideStale bool) []string {
	var titles []string

	// Sort PRs by UpdatedAt (most recent first)
	sortedPRs := make([]PR, len(prs))
	copy(sortedPRs, prs)
	sort.Slice(sortedPRs, func(i, j int) bool {
		return sortedPRs[i].UpdatedAt.After(sortedPRs[j].UpdatedAt)
	})

	for prIndex := range sortedPRs {
		// Apply filters (same logic as in addPRSection)
		org := extractOrgFromRepo(sortedPRs[prIndex].Repository)
		if org != "" && hiddenOrgs[org] {
			continue
		}

		if hideStale && sortedPRs[prIndex].UpdatedAt.Before(time.Now().Add(-stalePRThreshold)) {
			continue
		}

		title := fmt.Sprintf("%s #%d", sortedPRs[prIndex].Repository, sortedPRs[prIndex].Number)

		// Add bullet point or emoji for blocked PRs (same logic as in addPRSection)
		if sortedPRs[prIndex].NeedsReview || sortedPRs[prIndex].IsBlocked {
			prState, hasState := app.stateManager.PRState(sortedPRs[prIndex].URL)

			if hasState && !prState.FirstBlockedAt.IsZero() && time.Since(prState.FirstBlockedAt) < blockedPRIconDuration {
				if sectionTitle == "Outgoing" {
					title = fmt.Sprintf("🎉 %s", title)
				} else {
					title = fmt.Sprintf("🪿 %s", title)
				}
			} else {
				title = fmt.Sprintf("• %s", title)
			}
		}

		titles = append(titles, title)
	}

	return titles
}

// rebuildMenu completely rebuilds the menu from scratch.
func (app *App) rebuildMenu(ctx context.Context) {
	// Rebuild entire menu

	// Clear all existing menu items
	app.systrayInterface.ResetMenu()

	// Check for errors (auth or connection failures)
	app.mu.RLock()
	authError := app.authError
	failureCount := app.consecutiveFailures
	lastFetchError := app.lastFetchError
	app.mu.RUnlock()

	// Show auth error if present
	if authError != "" {
		// Show authentication error message
		errorTitle := app.systrayInterface.AddMenuItem("⚠️ Authentication Error", "")
		errorTitle.Disable()

		app.systrayInterface.AddSeparator()

		// Add error details
		errorMsg := app.systrayInterface.AddMenuItem(authError, "Click to see setup instructions")
		errorMsg.Click(func() {
			if err := openURL(ctx, "https://cli.github.com/manual/gh_auth_login"); err != nil {
				slog.Error("failed to open setup instructions", "error", err)
			}
		})

		app.systrayInterface.AddSeparator()

		// Add setup instructions
		setupInstr := app.systrayInterface.AddMenuItem("To fix this issue:", "")
		setupInstr.Disable()

		option1 := app.systrayInterface.AddMenuItem("1. Install GitHub CLI: brew install gh", "")
		option1.Disable()

		option2 := app.systrayInterface.AddMenuItem("2. Run: gh auth login", "")
		option2.Disable()

		option3 := app.systrayInterface.AddMenuItem("3. Or set GITHUB_TOKEN environment variable", "")
		option3.Disable()

		app.systrayInterface.AddSeparator()

		// Add quit option
		quitItem := app.systrayInterface.AddMenuItem("Quit", "")
		quitItem.Click(func() {
			app.systrayInterface.Quit()
		})

		return
	}

	// Show connection error if we have consecutive failures
	if failureCount > 0 && lastFetchError != "" {
		var errorMsg string
		switch {
		case failureCount == 1:
			errorMsg = "⚠️ Connection Error"
		case failureCount <= 3:
			errorMsg = fmt.Sprintf("⚠️ Connection Issues (%d failures)", failureCount)
		case failureCount <= 10:
			errorMsg = "❌ Multiple Connection Failures"
		default:
			errorMsg = "💀 Service Degraded"
		}

		errorTitle := app.systrayInterface.AddMenuItem(errorMsg, "")
		errorTitle.Disable()

		// Determine hostname and error type
		hostname := "api.github.com"
		for _, h := range []struct{ match, host string }{
			{"ready-to-review.dev", "dash.ready-to-review.dev"},
			{"api.github.com", "api.github.com"},
			{"github.com", "github.com"},
		} {
			if strings.Contains(lastFetchError, h.match) {
				hostname = h.host
				break
			}
		}

		errorType := "Connection failed"
		for _, e := range []struct{ match, errType string }{
			{"timeout", "Request timeout"},
			{"context deadline", "Request timeout (context deadline)"},
			{"rate limit", "Rate limit exceeded"},
			{"401", "Authentication failed"},
			{"unauthorized", "Authentication failed"},
			{"403", "Access forbidden"},
			{"forbidden", "Access forbidden"},
			{"404", "Resource not found"},
			{"connection refused", "Connection refused"},
			{"no such host", "DNS resolution failed"},
			{"TLS", "TLS/Certificate error"},
			{"x509", "TLS/Certificate error"},
		} {
			if strings.Contains(lastFetchError, e.match) {
				errorType = e.errType
				break
			}
		}

		// Show technical details
		techDetails := app.systrayInterface.AddMenuItem(fmt.Sprintf("Host: %s", hostname), "")
		techDetails.Disable()

		errorTypeItem := app.systrayInterface.AddMenuItem(fmt.Sprintf("Error: %s", errorType), "")
		errorTypeItem.Disable()

		// Show truncated raw error for debugging (max 80 chars)
		rawError := lastFetchError
		if len(rawError) > 80 {
			rawError = rawError[:77] + "..."
		}
		rawErrorItem := app.systrayInterface.AddMenuItem(fmt.Sprintf("Details: %s", rawError), "Click to copy full error")
		rawErrorItem.Click(func() {
			// Would need clipboard support to implement copy
			slog.Info("Full error", "error", lastFetchError)
		})

		app.systrayInterface.AddSeparator()
	}

	// Update tray title
	app.setTrayTitle()

	// Dashboard at the top
	// Add Web Dashboard link
	dashboardItem := app.systrayInterface.AddMenuItem("Web Dashboard", "")
	dashboardItem.Click(func() {
		if err := openURL(ctx, "https://dash.ready-to-review.dev/"); err != nil {
			slog.Error("failed to open dashboard", "error", err)
		}
	})

	app.systrayInterface.AddSeparator()

	// Get PR counts
	counts := app.countPRs()

	// Handle "No pull requests" case
	if counts.IncomingTotal == 0 && counts.OutgoingTotal == 0 {
		// No PRs to display
		noPRs := app.systrayInterface.AddMenuItem("No pull requests", "")
		noPRs.Disable()
	} else {
		// Incoming section
		if counts.IncomingTotal > 0 {
			app.mu.RLock()
			incoming := app.incoming
			app.mu.RUnlock()
			app.addPRSection(ctx, incoming, "Incoming", counts.IncomingBlocked)
		}

		app.systrayInterface.AddSeparator()

		// Outgoing section
		if counts.OutgoingTotal > 0 {
			app.mu.RLock()
			outgoing := app.outgoing
			app.mu.RUnlock()
			app.addPRSection(ctx, outgoing, "Outgoing", counts.OutgoingBlocked)
		}
	}

	// Add static items at the end
	app.addStaticMenuItems(ctx)

	// Menu rebuild complete
}

// addStaticMenuItems adds the static menu items (hide stale, start at login, quit).
func (app *App) addStaticMenuItems(ctx context.Context) {
	// Add static menu items

	app.systrayInterface.AddSeparator()

	// Hide orgs submenu
	// Add 'Hide orgs' submenu
	hideOrgsMenu := app.systrayInterface.AddMenuItem("Hide orgs", "Select organizations to hide PRs from")

	// Get combined list of seen orgs and hidden orgs
	app.mu.RLock()
	orgSet := make(map[string]bool)
	// Add all seen orgs
	for org := range app.seenOrgs {
		orgSet[org] = true
	}
	// Add all hidden orgs (in case they're not in seenOrgs yet)
	for org := range app.hiddenOrgs {
		orgSet[org] = true
	}
	// Convert to sorted slice
	orgs := make([]string, 0, len(orgSet))
	for org := range orgSet {
		orgs = append(orgs, org)
	}
	hiddenOrgs := make(map[string]bool)
	for org, hidden := range app.hiddenOrgs {
		hiddenOrgs[org] = hidden
	}
	app.mu.RUnlock()

	sort.Strings(orgs)

	if len(orgs) == 0 {
		noOrgsItem := hideOrgsMenu.AddSubMenuItem("No organizations found", "")
		noOrgsItem.Disable()
	} else {
		// Add checkbox items for each org
		for _, org := range orgs {
			orgName := org // Capture for closure
			orgItem := hideOrgsMenu.AddSubMenuItem(orgName, "")

			// Check if org is currently hidden
			if hiddenOrgs[orgName] {
				orgItem.Check()
			}

			orgItem.Click(func() {
				app.mu.Lock()
				if app.hiddenOrgs[orgName] {
					delete(app.hiddenOrgs, orgName)
					orgItem.Uncheck()
					slog.Info("[SETTINGS] Unhiding org", "org", orgName)
				} else {
					app.hiddenOrgs[orgName] = true
					orgItem.Check()
					slog.Info("[SETTINGS] Hiding org", "org", orgName)
				}
				// Menu always rebuilds now - no need to clear titles
				app.mu.Unlock()

				// Save settings
				app.saveSettings()

				// Rebuild menu to reflect changes
				app.rebuildMenu(ctx)
			})
		}
	}

	// Hide stale PRs
	// Add 'Hide stale PRs' option
	hideStaleItem := app.systrayInterface.AddMenuItem("Hide stale PRs (>90 days)", "")
	if app.hideStaleIncoming {
		hideStaleItem.Check()
	}
	hideStaleItem.Click(func() {
		app.mu.Lock()
		app.hideStaleIncoming = !app.hideStaleIncoming
		hideStale := app.hideStaleIncoming
		// Menu always rebuilds now - no need to clear titles
		app.mu.Unlock()

		if hideStale {
			hideStaleItem.Check()
		} else {
			hideStaleItem.Uncheck()
		}

		// Save settings to disk
		app.saveSettings()

		// Toggle hide stale PRs setting
		// Force menu rebuild since hideStaleIncoming changed
		app.rebuildMenu(ctx)
	})

	// Add login item option (macOS only)
	addLoginItemUI(ctx, app)

	// Audio cues
	// Add 'Audio cues' option
	audioItem := app.systrayInterface.AddMenuItem("Honks enabled", "Play sounds for notifications")
	app.mu.RLock()
	if app.enableAudioCues {
		audioItem.Check()
	}
	app.mu.RUnlock()
	audioItem.Click(func() {
		app.mu.Lock()
		app.enableAudioCues = !app.enableAudioCues
		enabled := app.enableAudioCues
		app.mu.Unlock()

		if enabled {
			audioItem.Check()
			slog.Info("[SETTINGS] Audio cues enabled")
		} else {
			audioItem.Uncheck()
			slog.Info("[SETTINGS] Audio cues disabled")
		}

		// Save settings to disk
		app.saveSettings()
	})

	// Auto-open blocked PRs in browser
	// Add 'Auto-open PRs' option
	autoOpenItem := app.systrayInterface.AddMenuItem("Auto-open incoming PRs", "Automatically open newly blocked PRs in browser (rate limited)")
	app.mu.RLock()
	if app.enableAutoBrowser {
		autoOpenItem.Check()
	}
	app.mu.RUnlock()
	autoOpenItem.Click(func() {
		app.mu.Lock()
		app.enableAutoBrowser = !app.enableAutoBrowser
		enabled := app.enableAutoBrowser
		// Reset rate limiter when toggling the feature
		if !enabled {
			app.browserRateLimiter.Reset()
		}
		app.mu.Unlock()

		if enabled {
			autoOpenItem.Check()
			slog.Info("[SETTINGS] Auto-open blocked PRs enabled")
		} else {
			autoOpenItem.Uncheck()
			slog.Info("[SETTINGS] Auto-open blocked PRs disabled")
		}

		// Save settings to disk
		app.saveSettings()
	})

	// Quit
	// Add 'Quit' option
	quitItem := app.systrayInterface.AddMenuItem("Quit", "")
	quitItem.Click(func() {
		slog.Info("Quit requested by user")
		app.systrayInterface.Quit()
	})
}
