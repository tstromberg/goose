// Package main - ui.go handles system tray UI and menu management.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os/exec"
	"runtime"
	"sort"
	"time"

	"github.com/energye/systray"
)

// openURLAutoStrict safely opens a URL in the default browser with strict validation for auto-opening.
// This function is used for auto-opening PRs and enforces stricter URL patterns.
func openURLAutoStrict(ctx context.Context, rawURL string) error {
	// Validate against strict GitHub PR URL pattern
	if err := validateGitHubPRURL(rawURL); err != nil {
		return fmt.Errorf("strict validation failed: %w", err)
	}

	// Use the regular openURL after strict validation passes
	return openURL(ctx, rawURL)
}

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
		log.Printf("Executing command: /usr/bin/open -u %q", rawURL)
		cmd = exec.CommandContext(ctx, "/usr/bin/open", "-u", rawURL)
	case "windows":
		// Use rundll32 to open URL safely without cmd shell
		log.Printf("Executing command: rundll32.exe url.dll,FileProtocolHandler %q", rawURL)
		cmd = exec.CommandContext(ctx, "rundll32.exe", "url.dll,FileProtocolHandler", rawURL)
	case "linux":
		// Use xdg-open with full path
		log.Printf("Executing command: /usr/bin/xdg-open %q", rawURL)
		cmd = exec.CommandContext(ctx, "/usr/bin/xdg-open", rawURL)
	default:
		// Try xdg-open for other Unix-like systems
		log.Printf("Executing command: /usr/bin/xdg-open %q", rawURL)
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
	log.Printf("[TRAY] Setting title to '%s' (incoming_total=%d, incoming_blocked=%d, outgoing_total=%d, outgoing_blocked=%d)",
		title, counts.IncomingTotal, counts.IncomingBlocked, counts.OutgoingTotal, counts.OutgoingBlocked)
	systray.SetTitle(title)
}

// sortPRsBlockedFirst creates a sorted copy of PRs with blocked ones first.
// This maintains stable ordering within blocked and non-blocked groups.
func sortPRsBlockedFirst(prs []PR) []PR {
	// Create a copy to avoid modifying the original slice
	sorted := make([]PR, len(prs))
	copy(sorted, prs)

	// Stable sort: blocked PRs first, then by update time (newest first)
	sort.SliceStable(sorted, func(i, j int) bool {
		// First priority: blocked status
		if sorted[i].NeedsReview != sorted[j].NeedsReview {
			return sorted[i].NeedsReview // true (blocked) comes before false
		}
		if sorted[i].IsBlocked != sorted[j].IsBlocked {
			return sorted[i].IsBlocked // true (blocked) comes before false
		}
		// Second priority: more recent PRs first
		return sorted[i].UpdatedAt.After(sorted[j].UpdatedAt)
	})

	return sorted
}

// addPRSection adds a section of PRs to the menu.
func (app *App) addPRSection(ctx context.Context, prs []PR, sectionTitle string, blockedCount int) {
	if len(prs) == 0 {
		return
	}

	// Add header
	headerText := fmt.Sprintf("%s — %d blocked on you", sectionTitle, blockedCount)
	// Create section header
	header := systray.AddMenuItem(headerText, "")
	header.Disable()

	// Sort PRs with blocked ones first
	sortedPRs := sortPRsBlockedFirst(prs)

	// Get hidden orgs with proper locking
	app.mu.RLock()
	hiddenOrgs := make(map[string]bool)
	for org, hidden := range app.hiddenOrgs {
		hiddenOrgs[org] = hidden
	}
	hideStale := app.hideStaleIncoming
	app.mu.RUnlock()

	// Add PR items in sorted order
	for i := range sortedPRs {
		// Apply filters
		// Skip PRs from hidden orgs
		org := extractOrgFromRepo(sortedPRs[i].Repository)
		if org != "" && hiddenOrgs[org] {
			continue
		}

		// Skip stale PRs if configured
		if hideStale && sortedPRs[i].UpdatedAt.Before(time.Now().Add(-stalePRThreshold)) {
			continue
		}

		title := fmt.Sprintf("%s #%d", sortedPRs[i].Repository, sortedPRs[i].Number)
		// Add bullet point or emoji for blocked PRs
		if sortedPRs[i].NeedsReview || sortedPRs[i].IsBlocked {
			// Show emoji for PRs blocked within the last 25 minutes
			if !sortedPRs[i].FirstBlockedAt.IsZero() && time.Since(sortedPRs[i].FirstBlockedAt) < blockedPRIconDuration {
				// Use party popper for outgoing PRs, goose for incoming PRs
				if sectionTitle == "Outgoing" {
					title = fmt.Sprintf("🎉 %s", title)
					log.Printf("[MENU] Adding party popper to outgoing PR: %s (blocked %v ago)",
						sortedPRs[i].URL, time.Since(sortedPRs[i].FirstBlockedAt))
				} else {
					title = fmt.Sprintf("🪿 %s", title)
					log.Printf("[MENU] Adding goose to incoming PR: %s (blocked %v ago)",
						sortedPRs[i].URL, time.Since(sortedPRs[i].FirstBlockedAt))
				}
			} else {
				title = fmt.Sprintf("• %s", title)
			}
		}
		// Format age inline for tooltip
		duration := time.Since(sortedPRs[i].UpdatedAt)
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
			age = sortedPRs[i].UpdatedAt.Format("2006")
		}
		tooltip := fmt.Sprintf("%s (%s)", sortedPRs[i].Title, age)
		// Add action reason for blocked PRs
		if (sortedPRs[i].NeedsReview || sortedPRs[i].IsBlocked) && sortedPRs[i].ActionReason != "" {
			tooltip = fmt.Sprintf("%s - %s", tooltip, sortedPRs[i].ActionReason)
		}

		// Create PR menu item
		item := systray.AddMenuItem(title, tooltip)

		// Capture URL to avoid loop variable capture bug
		prURL := sortedPRs[i].URL
		item.Click(func() {
			if err := openURL(ctx, prURL); err != nil {
				log.Printf("failed to open url: %v", err)
			}
		})
	}
}

// rebuildMenu completely rebuilds the menu from scratch.
func (app *App) rebuildMenu(ctx context.Context) {
	// Rebuild entire menu

	// Clear all existing menu items
	systray.ResetMenu()

	// Check for auth error first
	if app.authError != "" {
		// Show authentication error message
		errorTitle := systray.AddMenuItem("⚠️ Authentication Error", "")
		errorTitle.Disable()

		systray.AddSeparator()

		// Add error details
		errorMsg := systray.AddMenuItem(app.authError, "Click to see setup instructions")
		errorMsg.Click(func() {
			if err := openURL(ctx, "https://cli.github.com/manual/gh_auth_login"); err != nil {
				log.Printf("failed to open setup instructions: %v", err)
			}
		})

		systray.AddSeparator()

		// Add setup instructions
		setupInstr := systray.AddMenuItem("To fix this issue:", "")
		setupInstr.Disable()

		option1 := systray.AddMenuItem("1. Install GitHub CLI: brew install gh", "")
		option1.Disable()

		option2 := systray.AddMenuItem("2. Run: gh auth login", "")
		option2.Disable()

		option3 := systray.AddMenuItem("3. Or set GITHUB_TOKEN environment variable", "")
		option3.Disable()

		systray.AddSeparator()

		// Add quit option
		quitItem := systray.AddMenuItem("Quit", "")
		quitItem.Click(func() {
			systray.Quit()
		})

		return
	}

	// Update tray title
	app.setTrayTitle()

	// Dashboard at the top
	// Add Web Dashboard link
	dashboardItem := systray.AddMenuItem("Web Dashboard", "")
	dashboardItem.Click(func() {
		if err := openURL(ctx, "https://dash.ready-to-review.dev/"); err != nil {
			log.Printf("failed to open dashboard: %v", err)
		}
	})

	systray.AddSeparator()

	// Get PR counts
	counts := app.countPRs()

	// Handle "No pull requests" case
	if counts.IncomingTotal == 0 && counts.OutgoingTotal == 0 {
		// No PRs to display
		noPRs := systray.AddMenuItem("No pull requests", "")
		noPRs.Disable()
	} else {
		// Incoming section
		if counts.IncomingTotal > 0 {
			app.mu.RLock()
			incoming := app.incoming
			app.mu.RUnlock()
			app.addPRSection(ctx, incoming, "Incoming", counts.IncomingBlocked)
		}

		systray.AddSeparator()

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

	systray.AddSeparator()

	// Hide orgs submenu
	// Add 'Hide orgs' submenu
	hideOrgsMenu := systray.AddMenuItem("Hide orgs", "Select organizations to hide PRs from")

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
					log.Printf("[SETTINGS] Unhiding org: %s", orgName)
				} else {
					app.hiddenOrgs[orgName] = true
					orgItem.Check()
					log.Printf("[SETTINGS] Hiding org: %s", orgName)
				}
				// Clear menu titles to force rebuild
				app.lastMenuTitles = nil
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
	hideStaleItem := systray.AddMenuItem("Hide stale PRs (>90 days)", "")
	if app.hideStaleIncoming {
		hideStaleItem.Check()
	}
	hideStaleItem.Click(func() {
		app.mu.Lock()
		app.hideStaleIncoming = !app.hideStaleIncoming
		hideStale := app.hideStaleIncoming
		// Clear menu titles to force rebuild
		app.lastMenuTitles = nil
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
	audioItem := systray.AddMenuItem("Audio cues", "Play sounds for notifications")
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
			log.Println("[SETTINGS] Audio cues enabled")
		} else {
			audioItem.Uncheck()
			log.Println("[SETTINGS] Audio cues disabled")
		}

		// Save settings to disk
		app.saveSettings()
	})

	// Auto-open blocked PRs in browser
	// Add 'Auto-open PRs' option
	autoOpenItem := systray.AddMenuItem("Auto-open incoming PRs", "Automatically open newly blocked PRs in browser (rate limited)")
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
			log.Println("[SETTINGS] Auto-open blocked PRs enabled")
		} else {
			autoOpenItem.Uncheck()
			log.Println("[SETTINGS] Auto-open blocked PRs disabled")
		}

		// Save settings to disk
		app.saveSettings()
	})

	// Quit
	// Add 'Quit' option
	quitItem := systray.AddMenuItem("Quit", "")
	quitItem.Click(func() {
		log.Println("Quit requested by user")
		systray.Quit()
	})
}
