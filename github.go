// Package main - github.go contains GitHub API integration functions.
package main

import (
	"context"
	"errors"
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

// initClients initializes GitHub and Turn API clients.
func (app *App) initClients(ctx context.Context) error {
	token, err := app.githubToken(ctx)
	if err != nil {
		return fmt.Errorf("get github token: %w", err)
	}

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
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

// githubToken retrieves the GitHub token using gh CLI.
func (*App) githubToken(ctx context.Context) (string, error) {
	// Only check absolute paths for security - never use PATH
	var trustedPaths []string
	switch runtime.GOOS {
	case "windows":
		trustedPaths = []string{
			`C:\Program Files\GitHub CLI\gh.exe`,
			`C:\Program Files (x86)\GitHub CLI\gh.exe`,
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "gh", "gh.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "GitHub CLI", "gh.exe"),
		}
	case "darwin":
		trustedPaths = []string{
			"/opt/homebrew/bin/gh", // Homebrew on Apple Silicon
			"/usr/local/bin/gh",    // Homebrew on Intel / manual install
			"/usr/bin/gh",          // System package managers
		}
	case "linux":
		trustedPaths = []string{
			"/usr/local/bin/gh",                 // Manual install
			"/usr/bin/gh",                       // System package managers
			"/home/linuxbrew/.linuxbrew/bin/gh", // Linuxbrew
			"/snap/bin/gh",                      // Snap package
		}
	default:
		// BSD and other Unix-like systems
		trustedPaths = []string{
			"/usr/local/bin/gh",
			"/usr/bin/gh",
		}
	}

	var ghPath string
	for _, path := range trustedPaths {
		// Verify the file exists and is executable
		if info, err := os.Stat(path); err == nil {
			// Check if it's a regular file and executable
			const executableMask = 0o111
			if info.Mode().IsRegular() && info.Mode()&executableMask != 0 {
				// Verify it's actually the gh binary by running version command
				cmd := exec.Command(path, "version") //nolint:noctx // Quick version check doesn't need context
				output, err := cmd.Output()
				if err == nil && strings.Contains(string(output), "gh version") {
					log.Printf("Found and verified gh at: %s", path)
					ghPath = path
					break
				}
			}
		}
	}

	if ghPath == "" {
		return "", errors.New("gh cli not found in trusted locations, please install from https://cli.github.com")
	}

	log.Printf("Executing command: %s auth token", ghPath)
	cmd := exec.CommandContext(ctx, ghPath, "auth", "token")
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("gh command failed with output: %s", string(output))
		return "", fmt.Errorf("exec 'gh auth token': %w (output: %s)", err, string(output))
	}
	token := strings.TrimSpace(string(output))
	if token == "" {
		return "", errors.New("empty github token")
	}
	const minTokenLength = 20
	if len(token) < minTokenLength {
		return "", fmt.Errorf("invalid github token length: %d", len(token))
	}
	log.Println("Successfully obtained GitHub token")
	return token, nil
}

// fetchPRs retrieves all PRs involving the current user.
func (app *App) fetchPRs(ctx context.Context) (incoming []PR, outgoing []PR, err error) {
	user := app.currentUser.GetLogin()

	// Single query to get all PRs involving the user
	query := fmt.Sprintf("is:open is:pr involves:%s archived:false", user)

	const perPage = 100
	opts := &github.SearchOptions{
		ListOptions: github.ListOptions{PerPage: perPage},
		Sort:        "updated",
		Order:       "desc",
	}

	log.Printf("Searching for PRs with query: %s", query)
	searchStart := time.Now()

	// Just try once - GitHub API is reliable enough
	result, resp, err := app.client.Search.Issues(ctx, query, opts)
	if err != nil {
		// Check for rate limit
		const httpStatusForbidden = 403
		if resp != nil && resp.StatusCode == httpStatusForbidden {
			log.Print("GitHub API rate limited")
		}
		return nil, nil, fmt.Errorf("search PRs: %w", err)
	}

	log.Printf("GitHub search completed in %v, found %d PRs", time.Since(searchStart), len(result.Issues))

	// Limit PRs for performance
	if len(result.Issues) > maxPRsToProcess {
		log.Printf("Limiting to %d PRs for performance (total: %d)", maxPRsToProcess, len(result.Issues))
		result.Issues = result.Issues[:maxPRsToProcess]
	}

	// Process results
	turnSuccesses := 0
	turnFailures := 0

	for _, issue := range result.Issues {
		if !issue.IsPullRequest() {
			continue
		}
		repo := strings.TrimPrefix(issue.GetRepositoryURL(), "https://api.github.com/repos/")

		pr := PR{
			Title:      issue.GetTitle(),
			URL:        issue.GetHTMLURL(),
			Repository: repo,
			Number:     issue.GetNumber(),
			UpdatedAt:  issue.GetUpdatedAt().Time,
		}

		// Get Turn API data with caching
		turnData, err := app.turnData(ctx, issue.GetHTMLURL(), issue.GetUpdatedAt().Time)
		if err == nil && turnData != nil && turnData.PRState.UnblockAction != nil {
			turnSuccesses++
			if _, exists := turnData.PRState.UnblockAction[user]; exists {
				pr.NeedsReview = true
			}
		} else if err != nil {
			turnFailures++
		}

		// Categorize as incoming or outgoing
		if issue.GetUser().GetLogin() == user {
			pr.IsBlocked = pr.NeedsReview
			outgoing = append(outgoing, pr)
		} else {
			incoming = append(incoming, pr)
		}
	}

	log.Printf("Found %d incoming, %d outgoing PRs (Turn API: %d/%d succeeded)",
		len(incoming), len(outgoing), turnSuccesses, turnSuccesses+turnFailures)
	return incoming, outgoing, nil
}
