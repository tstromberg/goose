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
	"time"

	"github.com/energye/systray"
)

// formatAge formats a duration in human-readable form.
func formatAge(updatedAt time.Time) string {
	duration := time.Since(updatedAt)

	switch {
	case duration < time.Hour:
		return fmt.Sprintf("%dm", int(duration.Minutes()))
	case duration < 24*time.Hour:
		return fmt.Sprintf("%dh", int(duration.Hours()))
	case duration < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int(duration.Hours()/24))
	case duration < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(duration.Hours()/(24*30)))
	default:
		return updatedAt.Format("2006")
	}
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

	// Don't wait for the command to finish, but add timeout to prevent goroutine leak
	go func() {
		// Kill the process after 30 seconds if it hasn't finished
		timer := time.AfterFunc(30*time.Second, func() {
			if cmd.Process != nil {
				if err := cmd.Process.Kill(); err != nil {
					log.Printf("Failed to kill process: %v", err)
				}
			}
		})
		defer timer.Stop()

		if err := cmd.Wait(); err != nil {
			log.Printf("Failed to wait for command: %v", err)
		}
	}()

	return nil
}

// countPRs counts the number of PRs that need review/are blocked.
// Returns: incomingCount, incomingBlocked, outgoingCount, outgoingBlocked
//
//nolint:revive,gocritic // 4 return values is clearer than a struct here
func (app *App) countPRs() (int, int, int, int) {
	app.mu.RLock()
	defer app.mu.RUnlock()

	var incomingCount, incomingBlocked, outgoingCount, outgoingBlocked int

	for i := range app.incoming {
		if !app.hideStaleIncoming || !isStale(app.incoming[i].UpdatedAt) {
			incomingCount++
			if app.incoming[i].NeedsReview {
				incomingBlocked++
			}
		}
	}

	for i := range app.outgoing {
		if !app.hideStaleIncoming || !isStale(app.outgoing[i].UpdatedAt) {
			outgoingCount++
			if app.outgoing[i].IsBlocked {
				outgoingBlocked++
			}
		}
	}
	return incomingCount, incomingBlocked, outgoingCount, outgoingBlocked
}

// addPRMenuItem adds a menu item for a pull request.
func (app *App) addPRMenuItem(ctx context.Context, pr PR, isOutgoing bool) {
	title := fmt.Sprintf("%s #%d", pr.Repository, pr.Number)
	if (!isOutgoing && pr.NeedsReview) || (isOutgoing && pr.IsBlocked) {
		if isOutgoing {
			title = fmt.Sprintf("🚀 %s", title)
		} else {
			title = fmt.Sprintf("🕵️ %s", title)
		}
	}
	tooltip := fmt.Sprintf("%s (%s)", pr.Title, formatAge(pr.UpdatedAt))
	item := systray.AddMenuItem(title, tooltip)
	app.menuItems = append(app.menuItems, item)

	// Capture URL and context properly to avoid loop variable capture bug
	item.Click(func(capturedCtx context.Context, url string) func() {
		return func() {
			if err := openURL(capturedCtx, url); err != nil {
				log.Printf("failed to open url: %v", err)
			}
		}
	}(ctx, pr.URL))
}

// addPRSection adds a section of PRs to the menu.
func (app *App) addPRSection(ctx context.Context, prs []PR, sectionTitle string, blockedCount int, isOutgoing bool) {
	if len(prs) == 0 {
		return
	}

	// Add header
	header := systray.AddMenuItem(fmt.Sprintf("%s — %d blocked on you", sectionTitle, blockedCount), "")
	header.Disable()
	app.menuItems = append(app.menuItems, header)

	// Add PR items (already protected by mutex in caller)
	for i := range prs {
		// Apply filters
		if app.hideStaleIncoming && isStale(prs[i].UpdatedAt) {
			continue
		}
		app.addPRMenuItem(ctx, prs[i], isOutgoing)
	}
}

// updateMenu rebuilds the system tray menu.
func (app *App) updateMenu(ctx context.Context) {
	app.mu.RLock()
	incomingLen := len(app.incoming)
	outgoingLen := len(app.outgoing)
	app.mu.RUnlock()

	log.Printf("Updating menu with %d incoming and %d outgoing PRs", incomingLen, outgoingLen)

	// Store the current menu items to clean up later
	oldMenuItems := app.menuItems
	app.menuItems = nil

	// Calculate counts first
	incomingCount, incomingBlocked, outgoingCount, outgoingBlocked := app.countPRs()

	// Show "No pull requests" if both lists are empty
	if incomingLen == 0 && outgoingLen == 0 {
		noPRs := systray.AddMenuItem("No pull requests", "")
		noPRs.Disable()
		app.menuItems = append(app.menuItems, noPRs)
		systray.AddSeparator()
	}

	// Incoming section
	if incomingCount > 0 {
		app.addPRSection(ctx, app.incoming, "Incoming", incomingBlocked, false)
	}

	systray.AddSeparator()

	// Outgoing section
	if outgoingCount > 0 {
		app.addPRSection(ctx, app.outgoing, "Outgoing", outgoingBlocked, true)
	}

	// Add static menu items
	app.addStaticMenuItems(ctx)

	// Now hide old menu items after new ones are created
	// This prevents the flicker by ensuring new items exist before old ones disappear
	for _, item := range oldMenuItems {
		item.Hide()
	}
}

// addStaticMenuItems adds the static menu items (hide stale, dashboard, about, quit).
func (app *App) addStaticMenuItems(ctx context.Context) {
	systray.AddSeparator()

	// Hide stale PRs
	hideStaleItem := systray.AddMenuItem("Hide stale PRs (>90 days)", "")
	app.menuItems = append(app.menuItems, hideStaleItem)
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
		log.Printf("Hide stale PRs: %v", app.hideStaleIncoming)
		// Force menu rebuild since hideStaleIncoming changed
		app.lastMenuHash = ""
		app.updateMenu(ctx)
	})

	systray.AddSeparator()

	// Dashboard link
	dashboardItem := systray.AddMenuItem("Dashboard", "")
	app.menuItems = append(app.menuItems, dashboardItem)
	dashboardItem.Click(func() {
		if err := openURL(ctx, "https://dash.ready-to-review.dev/"); err != nil {
			log.Printf("failed to open dashboard: %v", err)
		}
	})

	// About
	aboutText := "About"
	if app.targetUser != "" {
		aboutText = fmt.Sprintf("About (viewing @%s)", app.targetUser)
	}
	aboutItem := systray.AddMenuItem(aboutText, "")
	app.menuItems = append(app.menuItems, aboutItem)
	aboutItem.Click(func() {
		log.Println("GitHub PR Monitor - A system tray app for tracking PR reviews")
		if err := openURL(ctx, "https://github.com/ready-to-review/pr-menubar"); err != nil {
			log.Printf("failed to open about page: %v", err)
		}
	})

	systray.AddSeparator()

	// Add login item option (macOS only)
	addLoginItemUI(ctx, app)

	// Quit
	quitItem := systray.AddMenuItem("Quit", "")
	app.menuItems = append(app.menuItems, quitItem)
	quitItem.Click(func() {
		log.Println("Quit requested by user")
		systray.Quit()
	})
}
