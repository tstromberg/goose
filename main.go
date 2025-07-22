package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
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

//go:embed menubar-icon.png
var embeddedIcon []byte

const (
	// Cache settings
	cacheTTL             = 2 * time.Hour
	cacheCleanupInterval = 5 * 24 * time.Hour
	
	// PR settings
	stalePRThreshold = 90 * 24 * time.Hour
	
	// Update intervals
	updateInterval = 2 * time.Minute
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
		showStaleIncoming:  false,
		previousBlockedPRs: make(map[string]bool),
	}
	

	log.Println("Initializing GitHub clients...")
	err = app.initClients()
	if err != nil {
		log.Fatalf("Failed to initialize clients: %v", err)
	}

	log.Println("Loading current user...")
	err = app.loadCurrentUser()
	if err != nil {
		log.Fatalf("Failed to load current user: %v", err)
	}

	log.Println("Starting systray...")
	systray.Run(app.onReady, app.onExit)
}

func (app *App) initClients() error {
	token, err := app.githubToken()
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

func findGHCommand() (string, error) {
	log.Println("Looking for gh command...")
	
	// Log current PATH
	log.Printf("Current PATH: %s", os.Getenv("PATH"))
	
	// Check if gh is in PATH first
	if path, err := exec.LookPath("gh"); err == nil {
		log.Printf("Found gh in PATH: %s", path)
		return path, nil
	}
	
	log.Println("gh not found in PATH, checking common locations...")
	
	// Common installation paths for gh
	commonPaths := []string{
		"/opt/homebrew/bin/gh",      // Homebrew on Apple Silicon
		"/usr/local/bin/gh",          // Homebrew on Intel / manual install
		"/usr/bin/gh",                // System package managers
		"/home/linuxbrew/.linuxbrew/bin/gh", // Linuxbrew
	}
	
	for _, path := range commonPaths {
		log.Printf("Checking: %s", path)
		if _, err := os.Stat(path); err == nil {
			log.Printf("Found gh at: %s", path)
			return path, nil
		}
	}
	
	return "", fmt.Errorf("gh CLI not found. Please install it from https://cli.github.com")
}

func (app *App) githubToken() (string, error) {
	ghPath, err := findGHCommand()
	if err != nil {
		return "", err
	}
	
	log.Printf("Executing: %s auth token", ghPath)
	cmd := exec.Command(ghPath, "auth", "token")
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("gh command failed with output: %s", string(output))
		return "", fmt.Errorf("exec 'gh auth token': %w (output: %s)", err, string(output))
	}
	token := strings.TrimSpace(string(output))
	if token == "" {
		return "", fmt.Errorf("empty GitHub token")
	}
	if len(token) < 20 {
		return "", fmt.Errorf("invalid GitHub token length: %d", len(token))
	}
	log.Printf("Successfully obtained GitHub token (length: %d)", len(token))
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
	systray.SetIcon(embeddedIcon)
	systray.SetTitle("Loading PRs...")
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
	ticker := time.NewTicker(updateInterval)
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
		systray.SetTitle("Error")
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
		// Skip stale PRs if not showing them
		if !app.showStaleIncoming && isStale(pr.UpdatedAt) {
			continue
		}
		if pr.BlockedOnYou {
			incomingBlocked++
		}
	}

	for _, pr := range outgoing {
		if pr.IsBlocked {
			outgoingBlocked++
		}
	}

	// Set title based on PR state
	systray.SetIcon(embeddedIcon)
	if incomingBlocked == 0 && outgoingBlocked == 0 {
		systray.SetTitle("")
	} else if incomingBlocked > 0 {
		systray.SetTitle(fmt.Sprintf("%d/%d 🔴", incomingBlocked, outgoingBlocked))
	} else {
		systray.SetTitle(fmt.Sprintf("0/%d 🚀", outgoingBlocked))
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
		turnData, err := app.turnDataWithCache(issue.GetHTMLURL(), issue.GetUpdatedAt().Time)
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
	return time.Since(updatedAt) > stalePRThreshold
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
		if app.showStaleIncoming || !isStale(pr.UpdatedAt) {
			incomingCount++
			if pr.BlockedOnYou {
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
			title := fmt.Sprintf("%s #%d", pr.Repository, pr.Number)
			if pr.Size != "" {
				title = fmt.Sprintf("%s – %s", title, pr.Size)
			}
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
		item.Hide()
	}
}

type cacheEntry struct {
	Data      *turn.CheckResponse `json:"data"`
	CachedAt  time.Time           `json:"cached_at"`
	UpdatedAt time.Time           `json:"updated_at"`
}

func (app *App) turnDataWithCache(url string, updatedAt time.Time) (*turn.CheckResponse, error) {
	// Create cache key from URL and updated timestamp
	key := fmt.Sprintf("%s-%s", url, updatedAt.Format(time.RFC3339))
	hash := sha256.Sum256([]byte(key))
	cacheFile := filepath.Join(app.cacheDir, hex.EncodeToString(hash[:])[:16]+".json")

	// Try to read from cache
	if data, err := os.ReadFile(cacheFile); err == nil {
		var entry cacheEntry
		if err := json.Unmarshal(data, &entry); err == nil {
			// Check if cache is still valid (2 hour TTL)
			if time.Since(entry.CachedAt) < cacheTTL && entry.UpdatedAt.Equal(updatedAt) {
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
		if err := os.WriteFile(cacheFile, cacheData, 0o600); err != nil {
			log.Printf("Failed to write cache for %s: %v", url, err)
		}
	}

	return data, nil
}

