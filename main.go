package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/energye/systray"
	"github.com/gen2brain/beeep"
	"github.com/google/go-github/v57/github"
	"github.com/ready-to-review/turnclient/pkg/turn"
	"golang.org/x/oauth2"
)

type PRData struct {
	ID           int64  `json:"id"`
	Number       int    `json:"number"`
	Title        string `json:"title"`
	HTMLURL      string `json:"html_url"`
	User         *github.User
	Repository   string
	UpdatedAt    time.Time
	IsBlocked    bool
	BlockedOnYou bool
	Size         string
	Tags         []string
}

type App struct {
	client             *github.Client
	turnClient         *turn.Client
	currentUser        *github.User
	ctx                context.Context
	incoming           []PRData
	outgoing           []PRData
	menuItems          []*systray.MenuItem
	cacheDir           string
	showStaleIncoming  bool
	previousBlockedPRs map[string]bool // Track previously blocked PRs by URL
	defaultIcon        []byte          // Default icon data
	logoIcon           []byte          // Logo icon data when no PRs are blocking
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
		showStaleIncoming:  false, // Default to showing stale PRs
		previousBlockedPRs: make(map[string]bool),
		defaultIcon:        getIcon(),
	}
	
	// Load logo icon if available
	app.logoIcon = app.loadLogoIcon()

	err = app.initClients()
	if err != nil {
		log.Fatalf("Failed to initialize clients: %v", err)
	}

	err = app.loadCurrentUser()
	if err != nil {
		log.Fatalf("Failed to load current user: %v", err)
	}

	systray.Run(app.onReady, app.onExit)
}

func (app *App) initClients() error {
	token, err := app.getGitHubToken()
	if err != nil {
		return fmt.Errorf("get GitHub token: %w", err)
	}

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(app.ctx, ts)
	app.client = github.NewClient(tc)

	// Initialize Turn client with base URL
	turnClient, err := turn.NewClient("https://turn.ready-to-review.dev")
	if err != nil {
		return fmt.Errorf("create turn client: %w", err)
	}
	turnClient.SetAuthToken(token)
	app.turnClient = turnClient

	return nil
}

func (app *App) getGitHubToken() (string, error) {
	cmd := exec.Command("gh", "auth", "token")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("exec 'gh auth token': %w", err)
	}
	token := strings.TrimSpace(string(output))
	if token == "" {
		return "", fmt.Errorf("empty GitHub token")
	}
	if len(token) < 20 {
		return "", fmt.Errorf("invalid GitHub token length")
	}
	return token, nil
}

func (app *App) loadCurrentUser() error {
	user, _, err := app.client.Users.Get(app.ctx, "")
	if err != nil {
		return fmt.Errorf("get current user: %w", err)
	}
	app.currentUser = user
	return nil
}

func (app *App) onReady() {
	log.Println("System tray ready")
	systray.SetIcon(app.defaultIcon)
	systray.SetTitle("Downloading...")
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

	go app.updateLoop()
}

func (app *App) onExit() {
	log.Println("Shutting down application")
	app.cleanupOldCache()
}

func (app *App) cleanupOldCache() {
	entries, err := os.ReadDir(app.cacheDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		filePath := filepath.Join(app.cacheDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		// Remove cache files older than 5 days
		if time.Since(info.ModTime()) > 5*24*time.Hour {
			log.Printf("Removing old cache file: %s", entry.Name())
			os.Remove(filePath)
		}
	}
}

func (app *App) updateLoop() {
	ticker := time.NewTicker(2 * time.Minute)
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
		systray.SetTitle("!!!")
		return
	}

	// Check for newly blocked PRs
	newlyBlockedPRs := []PRData{}
	currentBlockedPRs := make(map[string]bool)

	// Check incoming PRs
	for _, pr := range incoming {
		if pr.BlockedOnYou {
			currentBlockedPRs[pr.HTMLURL] = true
			// Check if this is newly blocked
			if !app.previousBlockedPRs[pr.HTMLURL] {
				newlyBlockedPRs = append(newlyBlockedPRs, pr)
			}
		}
	}

	// Check outgoing PRs
	for _, pr := range outgoing {
		if pr.IsBlocked {
			currentBlockedPRs[pr.HTMLURL] = true
			// Check if this is newly blocked
			if !app.previousBlockedPRs[pr.HTMLURL] {
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
	app.previousBlockedPRs = currentBlockedPRs

	app.incoming = incoming
	app.outgoing = outgoing

	incomingBlocked := 0
	outgoingBlocked := 0

	for _, pr := range incoming {
		// Only count PRs that will be shown in the menu
		if app.showStaleIncoming || !isStale(pr.UpdatedAt) {
			if pr.BlockedOnYou {
				incomingBlocked++
			}
		}
	}

	for _, pr := range outgoing {
		if pr.IsBlocked {
			outgoingBlocked++
		}
	}

	// Set title and icon based on PR state
	if incomingBlocked == 0 && outgoingBlocked == 0 {
		// Show only icon when no PRs are blocking
		systray.SetTitle("")
		// Use logo icon when no PRs are blocking
		if app.logoIcon != nil {
			systray.SetIcon(app.logoIcon)
		}
	} else if incomingBlocked > 0 {
		title := fmt.Sprintf("%d/%d 🔴", incomingBlocked, outgoingBlocked)
		systray.SetTitle(title)
		// Use default icon when there are blocking PRs
		systray.SetIcon(app.defaultIcon)
	} else {
		title := fmt.Sprintf("0/%d 🚀", outgoingBlocked)
		systray.SetTitle(title)
		// Use default icon when there are blocking PRs
		systray.SetIcon(app.defaultIcon)
	}

	app.updateMenu()
}

func (app *App) fetchPRs() ([]PRData, []PRData, error) {
	user := app.currentUser.GetLogin()

	// Single query to get all PRs involving the user
	query := fmt.Sprintf("is:open is:pr involves:%s archived:false", user)

	opts := &github.SearchOptions{
		ListOptions: github.ListOptions{PerPage: 100},
		Sort:        "updated",
		Order:       "desc",
	}

	log.Printf("Searching for PRs with query: %s", query)
	searchStart := time.Now()

	result, _, err := app.client.Search.Issues(app.ctx, query, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("search PRs: %w", err)
	}

	log.Printf("GitHub search completed in %v, found %d PRs", time.Since(searchStart), len(result.Issues))

	// Process results
	prMap := make(map[int64]*github.Issue, len(result.Issues))
	for _, issue := range result.Issues {
		if issue.IsPullRequest() {
			prMap[issue.GetID()] = issue
		}
	}

	var incoming, outgoing []PRData
	turnStart := time.Now()
	turnSuccesses := 0
	turnFailures := 0

	log.Printf("Processing %d PRs for Turn API status", len(prMap))

	for _, issue := range prMap {
		repo := strings.TrimPrefix(issue.GetRepositoryURL(), "https://api.github.com/repos/")

		prData := PRData{
			ID:         issue.GetID(),
			Number:     issue.GetNumber(),
			Title:      issue.GetTitle(),
			HTMLURL:    issue.GetHTMLURL(),
			User:       issue.GetUser(),
			Repository: repo,
			UpdatedAt:  issue.GetUpdatedAt().Time,
		}

		// Get Turn API data with caching
		turnData, err := app.getTurnDataWithCache(issue.GetHTMLURL(), issue.GetUpdatedAt().Time)
		if err == nil && turnData != nil {
			turnSuccesses++
			prData.Tags = turnData.PRState.Tags
			prData.Size = turnData.PRState.Size

			// Check if user is in UnblockAction
			if turnData.PRState.UnblockAction != nil {
				if _, exists := turnData.PRState.UnblockAction[user]; exists {
					prData.BlockedOnYou = true
					log.Printf("PR %s #%d is blocked on %s", repo, issue.GetNumber(), user)
				}
			}
		} else if err != nil {
			turnFailures++
			log.Printf("Turn API error for %s #%d: %v", repo, issue.GetNumber(), err)
		}

		// Categorize as incoming or outgoing
		if issue.GetUser().GetLogin() == user {
			prData.IsBlocked = prData.BlockedOnYou
			outgoing = append(outgoing, prData)
			log.Printf("Outgoing PR: %s #%d (blocked: %v)", repo, issue.GetNumber(), prData.IsBlocked)
		} else {
			incoming = append(incoming, prData)
			log.Printf("Incoming PR: %s #%d (blocked on you: %v)", repo, issue.GetNumber(), prData.BlockedOnYou)
		}
	}

	log.Printf("Turn API calls completed in %v (successes: %d, failures: %d)",
		time.Since(turnStart), turnSuccesses, turnFailures)
	log.Printf("Final count: %d incoming, %d outgoing PRs", len(incoming), len(outgoing))

	return incoming, outgoing, nil
}

// isStale returns true if the PR hasn't been updated in over 90 days
func isStale(updatedAt time.Time) bool {
	return time.Since(updatedAt) > 90*24*time.Hour
}

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

func (app *App) updateMenu() {
	log.Printf("Updating menu with %d incoming and %d outgoing PRs", len(app.incoming), len(app.outgoing))

	// Store the current menu items to clean up later
	oldMenuItems := app.menuItems
	app.menuItems = nil

	// Calculate counts first
	incomingBlocked := 0
	incomingCount := 0
	for _, pr := range app.incoming {
		if pr.BlockedOnYou {
			incomingBlocked++
		}
		if app.showStaleIncoming || !isStale(pr.UpdatedAt) {
			incomingCount++
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

	// Show "No pull requests" if both lists are empty
	if len(app.incoming) == 0 && len(app.outgoing) == 0 {
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
		for _, pr := range app.incoming {
			// Apply filters
			if !app.showStaleIncoming && isStale(pr.UpdatedAt) {
				continue
			}
			title := fmt.Sprintf("%s #%d – %s", pr.Repository, pr.Number, pr.Size)
			if pr.BlockedOnYou {
				title = fmt.Sprintf("%s 🔴", title)
			}
			tooltip := fmt.Sprintf("%s by %s (%s)", pr.Title, pr.User.GetLogin(), formatAge(pr.UpdatedAt))
			item := systray.AddMenuItem(title, tooltip)
			app.menuItems = append(app.menuItems, item)
			url := pr.HTMLURL
			item.Click(func() {
				exec.Command("open", url).Start()
			})
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
		for _, pr := range app.outgoing {
			// No filters for outgoing PRs
			title := fmt.Sprintf("%s #%d", pr.Repository, pr.Number)
			if pr.IsBlocked {
				title = fmt.Sprintf("%s 🚀", title)
			}
			tooltip := fmt.Sprintf("%s by %s (%s)", pr.Title, pr.User.GetLogin(), formatAge(pr.UpdatedAt))
			item := systray.AddMenuItem(title, tooltip)
			app.menuItems = append(app.menuItems, item)
			url := pr.HTMLURL
			item.Click(func() {
				exec.Command("open", url).Start()
			})
		}
	}

	systray.AddSeparator()

	// Show stale incoming
	showStaleIncomingItem := systray.AddMenuItem("Hide Stale (last update >90 days)", "")
	app.menuItems = append(app.menuItems, showStaleIncomingItem)
	if !app.showStaleIncoming {
		showStaleIncomingItem.Check()
	}
	showStaleIncomingItem.Click(func() {
		app.showStaleIncoming = !app.showStaleIncoming
		if !app.showStaleIncoming {
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
		exec.Command("open", "https://dash.ready-to-review.dev/").Start()
	})

	// About
	aboutItem := systray.AddMenuItem("About", "")
	app.menuItems = append(app.menuItems, aboutItem)
	aboutItem.Click(func() {
		log.Println("GitHub PR Monitor - A system tray app for tracking PR reviews")
		exec.Command("open", "https://github.com/ready-to-review/turnturnturn").Start()
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
		if item != nil {
			item.Hide()
		}
	}
}

type cacheEntry struct {
	Data      *turn.CheckResponse `json:"data"`
	CachedAt  time.Time           `json:"cached_at"`
	UpdatedAt time.Time           `json:"updated_at"`
}

func (app *App) getTurnDataWithCache(url string, updatedAt time.Time) (*turn.CheckResponse, error) {
	// Create cache key from URL and updated timestamp
	key := fmt.Sprintf("%s-%s", url, updatedAt.Format(time.RFC3339))
	hash := sha256.Sum256([]byte(key))
	cacheFile := filepath.Join(app.cacheDir, hex.EncodeToString(hash[:])[:16]+".json")

	// Try to read from cache
	if data, err := os.ReadFile(cacheFile); err == nil {
		var entry cacheEntry
		if err := json.Unmarshal(data, &entry); err == nil {
			// Check if cache is still valid (2 hour TTL)
			if time.Since(entry.CachedAt) < 2*time.Hour && entry.UpdatedAt.Equal(updatedAt) {
				return entry.Data, nil
			}
		}
	}

	// Cache miss, fetch from API
	log.Printf("Cache miss for %s, fetching from Turn API", url)
	data, err := app.turnClient.Check(app.ctx, url, app.currentUser.GetLogin(), updatedAt)
	if err != nil {
		return nil, err
	}

	// Save to cache
	entry := cacheEntry{
		Data:      data,
		CachedAt:  time.Now(),
		UpdatedAt: updatedAt,
	}
	cacheData, err := json.Marshal(entry)
	if err == nil {
		if err := os.WriteFile(cacheFile, cacheData, 0o644); err != nil {
			log.Printf("Failed to write cache for %s: %v", url, err)
		}
	}

	return data, nil
}

func getIcon() []byte {
	// Simple icon data - you can replace this with a proper icon
	return []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x10, 0x00, 0x00, 0x00, 0x10,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0xF3, 0xFF, 0x61, 0x00, 0x00, 0x00,
		0x19, 0x74, 0x45, 0x58, 0x74, 0x53, 0x6F, 0x66, 0x74, 0x77, 0x61, 0x72,
		0x65, 0x00, 0x41, 0x64, 0x6F, 0x62, 0x65, 0x20, 0x49, 0x6D, 0x61, 0x67,
		0x65, 0x52, 0x65, 0x61, 0x64, 0x79, 0x71, 0xC9, 0x65, 0x3C, 0x00, 0x00,
		0x00, 0x46, 0x49, 0x44, 0x41, 0x54, 0x38, 0xCB, 0x63, 0x60, 0x18, 0x05,
		0xA3, 0x60, 0x14, 0x8C, 0x02, 0x08, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45,
		0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
	}
}

func (app *App) loadLogoIcon() []byte {
	// When running from app bundle, we need to find the resources
	execPath, err := os.Executable()
	if err != nil {
		log.Printf("Failed to get executable path: %v", err)
		return nil
	}
	
	// Try multiple possible paths
	possiblePaths := []string{
		// Development: relative to working directory (use pre-sized icon)
		"out/menubar-icon.png",
		// App bundle: in Resources directory
		filepath.Join(filepath.Dir(execPath), "..", "Resources", "menubar-icon.png"),
	}
	
	for _, path := range possiblePaths {
		if data, err := os.ReadFile(path); err == nil {
			log.Printf("Successfully loaded logo icon from %s", path)
			return data
		}
	}
	
	log.Printf("Could not find logo icon in any of the expected paths")
	return nil
}
