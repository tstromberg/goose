package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	// Security constants.
	minTokenLength   = 40   // GitHub tokens are at least 40 chars
	maxTokenLength   = 255  // Reasonable upper bound
	maxUsernameLen   = 39   // GitHub username max length
	maxURLLength     = 2048 // Maximum URL length
	minPrintableChar = 0x20 // Minimum printable character
	deleteChar       = 0x7F // Delete character
)

var (
	// githubUsernameRegex validates GitHub usernames.
	// GitHub usernames can only contain alphanumeric characters and hyphens,
	// cannot start or end with hyphen, and max 39 characters.
	githubUsernameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,37}[a-zA-Z0-9]$|^[a-zA-Z0-9]$`)

	// githubTokenRegex validates GitHub token format.
	// Classic tokens: 40 hex chars.
	// New tokens: ghp_ (personal), ghs_ (server), ghr_ (refresh), gho_ (OAuth), ghu_ (user-to-server) followed by base62 chars.
	// Fine-grained tokens: github_pat_ followed by base62 chars.
	githubTokenRegex = regexp.MustCompile(`^[a-f0-9]{40}$|^gh[psoru]_[A-Za-z0-9]{36,251}$|^github_pat_[A-Za-z0-9]{82}$`)

	// githubPRURLRegex validates strict GitHub PR URL format for auto-opening.
	// Must match: https://github.com/{owner}/{repo}/pull/{number}
	// Owner and repo follow GitHub naming rules, number is digits only.
	githubPRURLRegex = regexp.MustCompile(`^https://github\.com/[a-zA-Z0-9][a-zA-Z0-9-]{0,38}/[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}/pull/[1-9][0-9]{0,9}$`)
)

// validateGitHubUsername validates a GitHub username.
func validateGitHubUsername(username string) error {
	if username == "" {
		return errors.New("username cannot be empty")
	}
	if len(username) > maxUsernameLen {
		return fmt.Errorf("username too long: %d > %d", len(username), maxUsernameLen)
	}
	if !githubUsernameRegex.MatchString(username) {
		return fmt.Errorf("invalid GitHub username format: %s", username)
	}
	return nil
}

// validateGitHubToken performs basic validation on a GitHub token.
func validateGitHubToken(token string) error {
	if token == "" {
		return errors.New("token cannot be empty")
	}

	tokenLen := len(token)
	if tokenLen < minTokenLength {
		return fmt.Errorf("token too short: %d < %d", tokenLen, minTokenLength)
	}
	if tokenLen > maxTokenLength {
		return fmt.Errorf("token too long: %d > %d", tokenLen, maxTokenLength)
	}

	// Check for common placeholder values
	if strings.Contains(strings.ToLower(token), "your_token") ||
		strings.Contains(strings.ToLower(token), "xxx") ||
		strings.Contains(token, "...") {
		return errors.New("token appears to be a placeholder")
	}

	if !githubTokenRegex.MatchString(token) {
		return errors.New("token does not match expected GitHub token format")
	}

	return nil
}

// sanitizeForLog removes sensitive information from strings before logging.
func sanitizeForLog(s string) string {
	// Redact tokens (both classic 40-char hex and new format)
	// Classic tokens
	s = regexp.MustCompile(`\b[a-f0-9]{40}\b`).ReplaceAllString(s, "[REDACTED-TOKEN]")
	// New format tokens (ghp_, ghs_, ghr_, gho_, ghu_)
	s = regexp.MustCompile(`\bgh[psoru]_[A-Za-z0-9]{36,251}\b`).ReplaceAllString(s, "[REDACTED-TOKEN]")
	// Fine-grained personal access tokens
	s = regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9]{82}\b`).ReplaceAllString(s, "[REDACTED-TOKEN]")
	// Bearer tokens in headers
	s = regexp.MustCompile(`Bearer [A-Za-z0-9_\-.]+`).ReplaceAllString(s, "Bearer [REDACTED]")
	// Authorization headers
	s = regexp.MustCompile(`Authorization: \S+`).ReplaceAllString(s, "Authorization: [REDACTED]")

	return s
}

// validateURL performs strict validation on URLs.
func validateURL(rawURL string) error {
	if rawURL == "" {
		return errors.New("URL cannot be empty")
	}

	// Check for null bytes or control characters
	for _, r := range rawURL {
		if r < minPrintableChar || r == deleteChar {
			return errors.New("URL contains control characters")
		}
	}

	// Ensure URL starts with https://
	if !strings.HasPrefix(rawURL, "https://") {
		return errors.New("URL must use HTTPS")
	}

	// Check for URL length limits
	if len(rawURL) > maxURLLength {
		return errors.New("URL too long")
	}

	return nil
}

// validateGitHubPRURL performs strict validation for GitHub PR URLs used in auto-opening.
// This ensures the URL follows the exact pattern: https://github.com/{owner}/{repo}/pull/{number}
// with no additional path segments, fragments, or suspicious characters.
// The URL may optionally have ?goose=<action> parameter which we add for tracking.
func validateGitHubPRURL(rawURL string) error {
	// First do basic URL validation
	if err := validateURL(rawURL); err != nil {
		return err
	}

	// Strip the ?goose parameter if present for pattern validation
	urlToValidate := rawURL
	if idx := strings.Index(rawURL, "?goose="); idx != -1 {
		urlToValidate = rawURL[:idx]
	}

	// Check against strict GitHub PR URL pattern
	if !githubPRURLRegex.MatchString(urlToValidate) {
		return fmt.Errorf("URL does not match GitHub PR pattern: %s", urlToValidate)
	}

	// Additional security checks
	// Reject URLs with @ (potential credential injection)
	if strings.Contains(rawURL, "@") {
		return errors.New("URL contains @ character")
	}

	// Reject URLs with URL encoding (could hide malicious content)
	// Exception: %3D which is = in URL encoding, only as part of ?goose parameter
	if strings.Contains(rawURL, "%") {
		// Allow URL encoding only in the goose parameter value
		idx := strings.Index(rawURL, "?goose=")
		if idx == -1 {
			return errors.New("URL contains encoded characters")
		}
		// Check if encoding is only in the goose parameter
		if strings.Contains(rawURL[:idx], "%") {
			return errors.New("URL contains encoded characters outside goose parameter")
		}
	}

	// Reject URLs with fragments
	if strings.Contains(rawURL, "#") {
		return errors.New("URL contains fragments")
	}

	// Allow only ?goose=<value> query parameter, nothing else
	if strings.Contains(rawURL, "?") {
		// Check if it's the goose parameter
		if idx := strings.Index(rawURL, "?goose="); idx == -1 {
			return errors.New("URL contains unexpected query parameters")
		}
		// Ensure no additional parameters after goose
		if strings.Contains(rawURL[strings.Index(rawURL, "?goose=")+7:], "&") {
			return errors.New("URL contains additional query parameters")
		}
	}

	// Reject URLs with double slashes (except after https:)
	if strings.Contains(rawURL[8:], "//") {
		return errors.New("URL contains double slashes")
	}

	return nil
}
