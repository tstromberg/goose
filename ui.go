package main

import (
	"fmt"
	"log"
	"net/url"
	"os/exec"
	"runtime"
	"time"

	"github.com/energye/systray"
)

// formatAge formats a duration in human-readable form
func formatAge(updatedAt time.Time) string {
	duration := time.Since(updatedAt)

	if duration < time.Minute {
		seconds := int(duration.Seconds())
		if seconds == 1 {
			return "1 second"
		}
		return fmt.Sprintf("%d seconds", seconds)
	} else if duration < time.Hour {
		minutes := int(duration.Minutes())
		if minutes == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", minutes)
	} else if duration < 24*time.Hour {
		hours := int(duration.Hours())
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	} else if duration < 30*24*time.Hour {
		days := int(duration.Hours() / 24)
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	} else if duration < 365*24*time.Hour {
		months := int(duration.Hours() / (24 * 30))
		if months == 1 {
			return "1 month"
		}
		return fmt.Sprintf("%d months", months)
	} else {
		// For PRs older than a year, show the year
		return updatedAt.Format("2006")
	}
}

// openURL safely opens a URL in the default browser after validation
func openURL(rawURL string) error {
	// Parse and validate the URL
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}

	// Only allow https URLs for security
	if u.Scheme != "https" {
		return fmt.Errorf("invalid url scheme: %s (only https allowed)", u.Scheme)
	}

	// Whitelist of allowed hosts
	allowedHosts := map[string]bool{
		"github.com":               true,
		"www.github.com":           true,
		"dash.ready-to-review.dev": true,
	}

	if !allowedHosts[u.Host] {
		return fmt.Errorf("invalid host: %s", u.Host)
	}

	// Execute the open command based on OS
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", rawURL)
	case "linux":
		cmd = exec.Command("xdg-open", rawURL)
	default:
		// Try xdg-open for other Unix-like systems
		cmd = exec.Command("xdg-open", rawURL)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open url: %w", err)
	}

	// Don't wait for the command to finish
	go func() {
		cmd.Wait() // Prevent zombie processes
	}()

	return nil
}

// updateMenu rebuilds the system tray menu
func (app *App) updateMenu() {
	app.mu.RLock()
	incomingLen := len(app.incoming)
	outgoingLen := len(app.outgoing)
	app.mu.RUnlock()

	log.Printf("Updating menu with %d incoming and %d outgoing PRs", incomingLen, outgoingLen)

	// Store the current menu items to clean up later
	oldMenuItems := app.menuItems
	app.menuItems = nil

	// Calculate counts first
	incomingBlocked := 0
	incomingCount := 0

	app.mu.RLock()
	for _, pr := range app.incoming {
		if app.showStaleIncoming || !isStale(pr.UpdatedAt) {
			incomingCount++
			if pr.NeedsReview {
				incomingBlocked++
			}
		}
	}

	outgoingBlocked := 0
	outgoingCount := 0
	for _, pr := range app.outgoing {
		if pr.IsBlocked {
			outgoingBlocked++
		}
		// Count all outgoing PRs
		outgoingCount++
	}
	app.mu.RUnlock()

	// Show "No pull requests" if both lists are empty
	if incomingLen == 0 && outgoingLen == 0 {
		noPRs := systray.AddMenuItem("No pull requests", "")
		noPRs.Disable()
		app.menuItems = append(app.menuItems, noPRs)
		systray.AddSeparator()
	}

	// Incoming section - clean header
	if incomingCount > 0 {
		incomingHeader := systray.AddMenuItem(fmt.Sprintf("Incoming — %d blocked on you", incomingBlocked), "")
		incomingHeader.Disable()
		app.menuItems = append(app.menuItems, incomingHeader)
	}

	if incomingCount > 0 {
		app.mu.RLock()
		prs := make([]PR, len(app.incoming))
		copy(prs, app.incoming)
		app.mu.RUnlock()

		for _, pr := range prs {
			// Apply filters
			if !app.showStaleIncoming && isStale(pr.UpdatedAt) {
				continue
			}
			title := fmt.Sprintf("%s #%d", pr.Repository, pr.Number)
			if pr.Size != "" {
				title = fmt.Sprintf("%s – %s", title, pr.Size)
			}
			if pr.NeedsReview {
				title = fmt.Sprintf("%s 🔴", title)
			}
			tooltip := fmt.Sprintf("%s by %s (%s)", pr.Title, pr.User.GetLogin(), formatAge(pr.UpdatedAt))
			item := systray.AddMenuItem(title, tooltip)
			app.menuItems = append(app.menuItems, item)

			// Capture URL properly to avoid loop variable capture bug
			item.Click(func(url string) func() {
				return func() {
					if err := openURL(url); err != nil {
						log.Printf("failed to open url: %v", err)
					}
				}
			}(pr.URL))
		}
	}

	systray.AddSeparator()

	// Outgoing section - clean header
	if outgoingCount > 0 {
		outgoingHeader := systray.AddMenuItem(fmt.Sprintf("Outgoing — %d blocked on you", outgoingBlocked), "")
		outgoingHeader.Disable()
		app.menuItems = append(app.menuItems, outgoingHeader)
	}

	if outgoingCount > 0 {
		app.mu.RLock()
		prs := make([]PR, len(app.outgoing))
		copy(prs, app.outgoing)
		app.mu.RUnlock()

		for _, pr := range prs {
			// No filters for outgoing PRs
			title := fmt.Sprintf("%s #%d", pr.Repository, pr.Number)
			if pr.IsBlocked {
				title = fmt.Sprintf("%s 🚀", title)
			}
			tooltip := fmt.Sprintf("%s by %s (%s)", pr.Title, pr.User.GetLogin(), formatAge(pr.UpdatedAt))
			item := systray.AddMenuItem(title, tooltip)
			app.menuItems = append(app.menuItems, item)

			// Capture URL properly to avoid loop variable capture bug
			item.Click(func(url string) func() {
				return func() {
					if err := openURL(url); err != nil {
						log.Printf("failed to open url: %v", err)
					}
				}
			}(pr.URL))
		}
	}

	systray.AddSeparator()

	// Show stale incoming
	showStaleIncomingItem := systray.AddMenuItem("Show stale PRs (>90 days)", "")
	app.menuItems = append(app.menuItems, showStaleIncomingItem)
	if !app.showStaleIncoming {
		showStaleIncomingItem.Check()
	}
	showStaleIncomingItem.Click(func() {
		app.showStaleIncoming = !app.showStaleIncoming
		if app.showStaleIncoming {
			showStaleIncomingItem.Check()
		} else {
			showStaleIncomingItem.Uncheck()
		}
		log.Printf("Show stale incoming: %v", app.showStaleIncoming)
		app.updateMenu()
	})

	systray.AddSeparator()

	// Dashboard link
	dashboardItem := systray.AddMenuItem("Dashboard", "")
	app.menuItems = append(app.menuItems, dashboardItem)
	dashboardItem.Click(func() {
		if err := openURL("https://dash.ready-to-review.dev/"); err != nil {
			log.Printf("failed to open dashboard: %v", err)
		}
	})

	// About
	aboutItem := systray.AddMenuItem("About", "")
	app.menuItems = append(app.menuItems, aboutItem)
	aboutItem.Click(func() {
		log.Println("GitHub PR Monitor - A system tray app for tracking PR reviews")
		if err := openURL("https://github.com/ready-to-review/pr-menubar"); err != nil {
			log.Printf("failed to open about page: %v", err)
		}
	})

	systray.AddSeparator()

	// Quit
	quitItem := systray.AddMenuItem("Quit", "")
	app.menuItems = append(app.menuItems, quitItem)
	quitItem.Click(func() {
		log.Println("Quit requested by user")
		systray.Quit()
	})

	// Now hide old menu items after new ones are created
	// This prevents the flicker by ensuring new items exist before old ones disappear
	for _, item := range oldMenuItems {
		item.Hide()
	}
}
