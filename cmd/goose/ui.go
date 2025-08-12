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

// formatAge formats a duration in human-readable form.
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
		if !app.hideStaleIncoming || app.incoming[i].UpdatedAt.After(staleThreshold) {
			incomingCount++
			if app.incoming[i].NeedsReview {
				incomingBlocked++
			}
		}
	}

	for i := range app.outgoing {
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
	switch {
	case counts.IncomingBlocked == 0 && counts.OutgoingBlocked == 0:
		systray.SetTitle("😊")
	case counts.IncomingBlocked > 0 && counts.OutgoingBlocked > 0:
		systray.SetTitle(fmt.Sprintf("🪿 %d 🎉 %d", counts.IncomingBlocked, counts.OutgoingBlocked))
	case counts.IncomingBlocked > 0:
		systray.SetTitle(fmt.Sprintf("🪿 %d", counts.IncomingBlocked))
	default:
		systray.SetTitle(fmt.Sprintf("🎉 %d", counts.OutgoingBlocked))
	}
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

	// Add PR items in sorted order
	for i := range sortedPRs {
		// Apply filters
		// Skip stale PRs if configured
		if app.hideStaleIncoming && sortedPRs[i].UpdatedAt.Before(time.Now().Add(-stalePRThreshold)) {
			continue
		}

		title := fmt.Sprintf("%s #%d", sortedPRs[i].Repository, sortedPRs[i].Number)
		// Add bullet point for PRs where user is blocking
		if sortedPRs[i].NeedsReview {
			title = fmt.Sprintf("• %s", title)
		}
		// Format age inline for tooltip
		duration := time.Since(sortedPRs[i].UpdatedAt)
		var age string
		switch {
		case duration < time.Hour:
			age = fmt.Sprintf("%dm", int(duration.Minutes()))
		case duration < dailyInterval:
			age = fmt.Sprintf("%dh", int(duration.Hours()))
		case duration < 30*dailyInterval:
			age = fmt.Sprintf("%dd", int(duration.Hours()/24))
		case duration < 365*dailyInterval:
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

	// Update tray title
	app.setTrayTitle()

	// Dashboard at the top
	// Add Dashboard link
	dashboardItem := systray.AddMenuItem("Dashboard", "")
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

	// Hide stale PRs
	// Add 'Hide stale PRs' option
	hideStaleItem := systray.AddMenuItem("Hide stale PRs (>90 days)", "")
	if app.hideStaleIncoming {
		hideStaleItem.Check()
	}
	hideStaleItem.Click(func() {
		app.hideStaleIncoming = !app.hideStaleIncoming
		if app.hideStaleIncoming {
			hideStaleItem.Check()
		} else {
			hideStaleItem.Uncheck()
		}
		// Toggle hide stale PRs setting
		// Force menu rebuild since hideStaleIncoming changed
		app.mu.Lock()
		// Clear menu state to force rebuild
		app.lastMenuState = nil
		app.mu.Unlock()
		app.rebuildMenu(ctx)
	})

	// Daily reminders
	// Add 'Daily reminders' option
	reminderItem := systray.AddMenuItem("Daily reminders", "Send reminder notifications for blocked PRs every 24 hours")
	app.mu.RLock()
	if app.enableReminders {
		reminderItem.Check()
	}
	app.mu.RUnlock()
	reminderItem.Click(func() {
		app.mu.Lock()
		app.enableReminders = !app.enableReminders
		enabled := app.enableReminders
		app.mu.Unlock()

		if enabled {
			reminderItem.Check()
			log.Println("[SETTINGS] Daily reminders enabled")
		} else {
			reminderItem.Uncheck()
			log.Println("[SETTINGS] Daily reminders disabled")
		}
	})

	// Add login item option (macOS only)
	addLoginItemUI(ctx, app)

	// Quit
	// Add 'Quit' option
	quitItem := systray.AddMenuItem("Quit", "")
	quitItem.Click(func() {
		log.Println("Quit requested by user")
		systray.Quit()
	})
}
