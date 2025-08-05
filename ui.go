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
	return incomingCount, incomingBlocked, outgoingCount, outgoingBlocked
}

// setTrayTitle updates the system tray title based on PR counts and loading state.
func (app *App) setTrayTitle() {
	app.mu.RLock()
	loading := app.turnDataLoading
	app.mu.RUnlock()

	_, incomingBlocked, _, outgoingBlocked := app.countPRs()

	if loading && !app.turnDataLoaded {
		// Initial load - show loading indicator
		systray.SetTitle("...")
		return
	}

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
}

// updateSectionHeaders updates the section headers with current blocked counts.
func (app *App) updateSectionHeaders() {
	_, incomingBlocked, _, outgoingBlocked := app.countPRs()

	app.mu.Lock()
	defer app.mu.Unlock()

	if incomingHeader, exists := app.sectionHeaders["Incoming"]; exists {
		headerText := fmt.Sprintf("Incoming — %d blocked on you", incomingBlocked)
		log.Printf("[MENU] Updating section header 'Incoming': %s", headerText)
		incomingHeader.SetTitle(headerText)
	}

	if outgoingHeader, exists := app.sectionHeaders["Outgoing"]; exists {
		headerText := fmt.Sprintf("Outgoing — %d blocked on you", outgoingBlocked)
		log.Printf("[MENU] Updating section header 'Outgoing': %s", headerText)
		outgoingHeader.SetTitle(headerText)
	}
}

// updatePRMenuItem updates an existing PR menu item with new data.
func (app *App) updatePRMenuItem(pr PR) {
	app.mu.Lock()
	defer app.mu.Unlock()

	if item, exists := app.prMenuItems[pr.URL]; exists {
		oldTitle := item.String()
		title := fmt.Sprintf("%s #%d", pr.Repository, pr.Number)
		// Add bullet point for PRs where user is blocking
		if pr.NeedsReview {
			title = fmt.Sprintf("• %s", title)
		}
		log.Printf("[MENU] Updating PR menu item for %s: '%s' -> '%s'", pr.URL, oldTitle, title)
		item.SetTitle(title)
	} else {
		log.Printf("[MENU] WARNING: Tried to update non-existent PR menu item for %s", pr.URL)
	}
}

// addPRMenuItem adds a menu item for a pull request.
// NOTE: Caller must hold app.mu.Lock() when calling this function.
func (app *App) addPRMenuItem(ctx context.Context, pr PR, _ bool) {
	title := fmt.Sprintf("%s #%d", pr.Repository, pr.Number)
	// Add bullet point for PRs where user is blocking
	if pr.NeedsReview {
		title = fmt.Sprintf("• %s", title)
	}
	tooltip := fmt.Sprintf("%s (%s)", pr.Title, formatAge(pr.UpdatedAt))

	// Check if menu item already exists
	if existingItem, exists := app.prMenuItems[pr.URL]; exists {
		// Update existing menu item and ensure it's visible
		log.Printf("[MENU] PR menu item already exists for %s, updating title to '%s' and showing", pr.URL, title)
		existingItem.SetTitle(title)
		existingItem.SetTooltip(tooltip)
		existingItem.Show() // Make sure it's visible if it was hidden
		return
	}

	// Create new menu item
	log.Printf("[MENU] Creating new PR menu item for %s: '%s'", pr.URL, title)
	item := systray.AddMenuItem(title, tooltip)
	app.menuItems = append(app.menuItems, item)
	app.prMenuItems[pr.URL] = item

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
	var headerText string
	app.mu.RLock()
	loading := app.turnDataLoading
	app.mu.RUnlock()

	if loading && !app.turnDataLoaded {
		headerText = fmt.Sprintf("%s — <loading> blocked on you", sectionTitle)
	} else {
		headerText = fmt.Sprintf("%s — %d blocked on you", sectionTitle, blockedCount)
	}

	// Check if header already exists (need to protect sectionHeaders access)
	app.mu.Lock()
	existingHeader, exists := app.sectionHeaders[sectionTitle]
	if exists {
		// Update existing header
		log.Printf("[MENU] Section header already exists for '%s', updating to '%s'", sectionTitle, headerText)
		existingHeader.SetTitle(headerText)
	} else {
		// Create new header
		log.Printf("[MENU] Creating new section header '%s': '%s'", sectionTitle, headerText)
		header := systray.AddMenuItem(headerText, "")
		header.Disable()
		app.menuItems = append(app.menuItems, header)
		app.sectionHeaders[sectionTitle] = header
	}

	// Add PR items (mutex already held)
	for i := range prs {
		// Apply filters
		if app.hideStaleIncoming && isStale(prs[i].UpdatedAt) {
			continue
		}
		app.addPRMenuItem(ctx, prs[i], isOutgoing)
	}

	app.mu.Unlock()
}

// initializeMenu creates the initial menu structure with static items.
func (app *App) initializeMenu(ctx context.Context) {
	log.Print("[MENU] Initializing menu structure")

	// Create initial structure - this should only be called once
	app.menuItems = nil
	app.prMenuItems = make(map[string]*systray.MenuItem)
	app.sectionHeaders = make(map[string]*systray.MenuItem)

	// The menu will be populated by updateMenu
	app.updateMenu(ctx)

	// Add static items at the end - these never change
	app.addStaticMenuItems(ctx)

	app.menuInitialized = true
	log.Print("[MENU] Menu initialization complete")
}

// updateMenu updates the dynamic parts of the menu (PRs and headers).
func (app *App) updateMenu(ctx context.Context) {
	// If menu is already initialized, only allow updates if we're not rebuilding everything
	if app.menuInitialized {
		log.Print("[MENU] Menu already initialized, performing incremental update")
	} else {
		// Log the call stack to see who's calling updateMenu during initialization
		log.Print("[MENU] updateMenu called during initialization from:")
		const maxStackFrames = 5
		for i := 1; i < maxStackFrames; i++ {
			pc, file, line, ok := runtime.Caller(i)
			if !ok {
				break
			}
			fn := runtime.FuncForPC(pc)
			log.Printf("[MENU]   %s:%d %s", file, line, fn.Name())
		}
	}

	app.mu.RLock()
	incomingLen := len(app.incoming)
	outgoingLen := len(app.outgoing)
	app.mu.RUnlock()

	log.Printf("[MENU] Updating menu with %d incoming and %d outgoing PRs", incomingLen, outgoingLen)

	// Update tray title
	app.setTrayTitle()

	// Track which PRs we currently have
	currentPRURLs := make(map[string]bool)
	app.mu.RLock()
	for _, pr := range app.incoming {
		currentPRURLs[pr.URL] = true
	}
	for _, pr := range app.outgoing {
		currentPRURLs[pr.URL] = true
	}
	app.mu.RUnlock()

	// Hide PR menu items that are no longer in the current data
	app.mu.Lock()
	for prURL, item := range app.prMenuItems {
		if !currentPRURLs[prURL] {
			log.Printf("[MENU] Hiding PR menu item that's no longer in data: %s", prURL)
			item.Hide()
			delete(app.prMenuItems, prURL)
		}
	}
	app.mu.Unlock()

	// Calculate counts first
	incomingCount, incomingBlocked, outgoingCount, outgoingBlocked := app.countPRs()

	// Handle "No pull requests" item
	const noPRsKey = "__no_prs__"
	app.mu.Lock()
	if incomingLen == 0 && outgoingLen == 0 {
		if noPRItem, exists := app.prMenuItems[noPRsKey]; exists {
			// Show existing item
			log.Print("[MENU] Showing existing 'No pull requests' item")
			noPRItem.Show()
		} else {
			// Create new item
			log.Print("[MENU] Creating 'No pull requests' item")
			noPRs := systray.AddMenuItem("No pull requests", "")
			noPRs.Disable()
			app.menuItems = append(app.menuItems, noPRs)
			app.prMenuItems[noPRsKey] = noPRs
		}
	} else {
		// Hide "No pull requests" if we have PRs
		if noPRItem, exists := app.prMenuItems[noPRsKey]; exists {
			log.Print("[MENU] Hiding 'No pull requests' item")
			noPRItem.Hide()
		}
	}
	app.mu.Unlock()

	// Incoming section
	if incomingCount > 0 {
		app.addPRSection(ctx, app.incoming, "Incoming", incomingBlocked, false)
	}

	systray.AddSeparator()

	// Outgoing section
	if outgoingCount > 0 {
		app.addPRSection(ctx, app.outgoing, "Outgoing", outgoingBlocked, true)
	}

	log.Print("[MENU] Menu update complete")
}

// addStaticMenuItems adds the static menu items (hide stale, dashboard, about, quit).
func (app *App) addStaticMenuItems(ctx context.Context) {
	log.Print("[MENU] Adding static menu items")

	systray.AddSeparator()

	// Hide stale PRs
	log.Print("[MENU] Adding 'Hide stale PRs' menu item")
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
		app.lastMenuHashInt = 0
		app.updateMenu(ctx)
	})

	systray.AddSeparator()

	// Dashboard link
	log.Print("[MENU] Adding 'Dashboard' menu item")
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
	log.Printf("[MENU] Adding '%s' menu item", aboutText)
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
	log.Print("[MENU] Adding 'Quit' menu item")
	quitItem := systray.AddMenuItem("Quit", "")
	app.menuItems = append(app.menuItems, quitItem)
	quitItem.Click(func() {
		log.Println("Quit requested by user")
		systray.Quit()
	})
}
