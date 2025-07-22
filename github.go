package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/go-github/v57/github"
	"github.com/ready-to-review/turnclient/pkg/turn"
	"golang.org/x/oauth2"
)

// initClients initializes GitHub and Turn API clients
func (app *App) initClients() error {
	token, err := app.githubToken()
	if err != nil {
		return fmt.Errorf("get github token: %w", err)
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

// findGHCommand locates the gh CLI tool
func findGHCommand() (string, error) {
	log.Println("Looking for gh command...")

	// Log current PATH
	log.Printf("Current PATH: %s", os.Getenv("PATH"))

	// Check if gh is in PATH first
	ghCmd := "gh"
	if runtime.GOOS == "windows" {
		ghCmd = "gh.exe"
	}
	if path, err := exec.LookPath(ghCmd); err == nil {
		log.Printf("Found gh in PATH: %s", path)
		return path, nil
	}

	log.Println("gh not found in PATH, checking common locations...")

	// Common installation paths for gh based on OS
	var commonPaths []string
	switch runtime.GOOS {
	case "windows":
		commonPaths = []string{
			`C:\Program Files\GitHub CLI\gh.exe`,
			`C:\Program Files (x86)\GitHub CLI\gh.exe`,
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "gh", "gh.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "GitHub CLI", "gh.exe"),
		}
	case "darwin":
		commonPaths = []string{
			"/opt/homebrew/bin/gh", // Homebrew on Apple Silicon
			"/usr/local/bin/gh",    // Homebrew on Intel / manual install
			"/usr/bin/gh",          // System package managers
		}
	case "linux":
		commonPaths = []string{
			"/usr/local/bin/gh",                 // Manual install
			"/usr/bin/gh",                       // System package managers
			"/home/linuxbrew/.linuxbrew/bin/gh", // Linuxbrew
			"/snap/bin/gh",                      // Snap package
		}
	default:
		// BSD and other Unix-like systems
		commonPaths = []string{
			"/usr/local/bin/gh",
			"/usr/bin/gh",
		}
	}

	for _, path := range commonPaths {
		log.Printf("Checking: %s", path)
		if _, err := os.Stat(path); err == nil {
			log.Printf("Found gh at: %s", path)
			return path, nil
		}
	}

	return "", fmt.Errorf("gh cli not found, please install from https://cli.github.com")
}

// githubToken retrieves the GitHub token using gh CLI
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
		return "", fmt.Errorf("empty github token")
	}
	if len(token) < 20 {
		return "", fmt.Errorf("invalid github token length: %d", len(token))
	}
	log.Printf("Successfully obtained GitHub token (length: %d)", len(token))
	return token, nil
}

// loadCurrentUser loads the authenticated GitHub user
func (app *App) loadCurrentUser() error {
	user, _, err := app.client.Users.Get(app.ctx, "")
	if err != nil {
		return fmt.Errorf("get current user: %w", err)
	}
	app.currentUser = user
	return nil
}

// fetchPRs retrieves all PRs involving the current user
func (app *App) fetchPRs() ([]PR, []PR, error) {
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

	var incoming, outgoing []PR
	turnStart := time.Now()
	turnSuccesses := 0
	turnFailures := 0

	log.Printf("Processing %d PRs for Turn API status", len(prMap))

	for _, issue := range prMap {
		repo := strings.TrimPrefix(issue.GetRepositoryURL(), "https://api.github.com/repos/")

		pr := PR{
			ID:         issue.GetID(),
			Number:     issue.GetNumber(),
			Title:      issue.GetTitle(),
			URL:        issue.GetHTMLURL(),
			User:       issue.GetUser(),
			Repository: repo,
			UpdatedAt:  issue.GetUpdatedAt().Time,
		}

		// Get Turn API data with caching
		turnData, err := app.getTurnData(issue.GetHTMLURL(), issue.GetUpdatedAt().Time)
		if err == nil && turnData != nil {
			turnSuccesses++
			pr.Tags = turnData.PRState.Tags
			pr.Size = turnData.PRState.Size

			// Check if user is in UnblockAction
			if turnData.PRState.UnblockAction != nil {
				if _, exists := turnData.PRState.UnblockAction[user]; exists {
					pr.NeedsReview = true
					log.Printf("PR %s #%d needs review from %s", repo, issue.GetNumber(), user)
				}
			}
		} else if err != nil {
			turnFailures++
			log.Printf("Turn API error for %s #%d: %v", repo, issue.GetNumber(), err)
		}

		// Categorize as incoming or outgoing
		if issue.GetUser().GetLogin() == user {
			pr.IsBlocked = pr.NeedsReview
			outgoing = append(outgoing, pr)
			log.Printf("Outgoing PR: %s #%d (blocked: %v)", repo, issue.GetNumber(), pr.IsBlocked)
		} else {
			incoming = append(incoming, pr)
			log.Printf("Incoming PR: %s #%d (needs review: %v)", repo, issue.GetNumber(), pr.NeedsReview)
		}
	}

	log.Printf("Turn API calls completed in %v (successes: %d, failures: %d)",
		time.Since(turnStart), turnSuccesses, turnFailures)
	log.Printf("Final count: %d incoming, %d outgoing PRs", len(incoming), len(outgoing))

	return incoming, outgoing, nil
}
