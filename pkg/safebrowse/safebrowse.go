// Package safebrowse provides secure URL validation and browser opening.
// All validation rules apply uniformly across platforms to prevent injection attacks.
package safebrowse

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

const maxURLLength = 2048

// Open validates and opens a URL in the system browser.
func Open(ctx context.Context, rawURL string) error {
	if err := validate(rawURL, false); err != nil {
		return err
	}
	return open(ctx, rawURL)
}

// OpenWithParams validates and opens a URL with query parameters.
func OpenWithParams(ctx context.Context, rawURL string, params map[string]string) error {
	if err := validate(rawURL, false); err != nil {
		return err
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}

	// Validate parameters before encoding
	for key, value := range params {
		if err := validateParamString(key); err != nil {
			return fmt.Errorf("invalid parameter key %q: %w", key, err)
		}
		if err := validateParamString(value); err != nil {
			return fmt.Errorf("invalid parameter value %q: %w", value, err)
		}
	}

	// Build query string
	q := u.Query()
	for key, value := range params {
		q.Set(key, value)
	}
	u.RawQuery = q.Encode()

	// Validate the final URL after encoding to catch any encoding issues
	finalURL := u.String()
	if strings.Contains(finalURL, "%") {
		return errors.New("URL encoding produced unsafe characters")
	}

	if err := validate(finalURL, true); err != nil {
		return err
	}

	return open(ctx, finalURL)
}

// ValidateURL performs strict security validation on a URL.
func ValidateURL(rawURL string) error {
	return validate(rawURL, false)
}

// ValidateGitHubPRURL validates URLs matching https://github.com/{owner}/{repo}/pull/{number}[?goose=value]
func ValidateGitHubPRURL(rawURL string) error {
	if err := validate(rawURL, true); err != nil {
		return err
	}

	// Additional GitHub PR-specific checks
	u, _ := url.Parse(rawURL) // Already validated
	if u.Host != "github.com" {
		return errors.New("must be github.com")
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" {
		return errors.New("must match format: /{owner}/{repo}/pull/{number}")
	}

	// Validate PR number (must start with 1-9)
	if len(parts[3]) == 0 || parts[3][0] < '1' || parts[3][0] > '9' {
		return errors.New("PR number must start with 1-9")
	}
	for _, c := range parts[3] {
		if c < '0' || c > '9' {
			return errors.New("PR number must be digits only")
		}
	}

	// If query params exist, only allow ?goose= (no other params or & characters)
	if u.RawQuery != "" {
		if !strings.HasPrefix(u.RawQuery, "goose=") || strings.Contains(u.RawQuery, "&") {
			return errors.New("only ?goose= query parameter allowed")
		}
	}

	return nil
}

// validate performs the core validation logic.
func validate(rawURL string, allowParams bool) error {
	if rawURL == "" {
		return errors.New("URL cannot be empty")
	}

	if len(rawURL) > maxURLLength {
		return fmt.Errorf("URL exceeds maximum length of %d", maxURLLength)
	}

	// Check every character
	for i, r := range rawURL {
		if r < 0x20 || r == 0x7F || r > 127 {
			return fmt.Errorf("invalid character at position %d", i)
		}
		if r == '%' {
			return errors.New("percent-encoding not allowed")
		}
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if u.Scheme != "https" {
		return errors.New("must use HTTPS")
	}

	if u.User != nil {
		return errors.New("user info not allowed")
	}

	if u.Fragment != "" {
		return errors.New("fragments (#) not allowed")
	}

	// Reject custom ports
	if u.Port() != "" {
		return errors.New("custom ports not allowed")
	}

	if !allowParams && u.RawQuery != "" {
		return errors.New("query parameters not allowed (use OpenWithParams)")
	}

	// Normalize host to lowercase
	u.Host = strings.ToLower(u.Host)

	// Validate host and path contain only safe characters
	if err := validateSafeChars(u.Host); err != nil {
		return fmt.Errorf("invalid host: %w", err)
	}

	if err := validateSafeChars(u.Path); err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	// Check for path traversal
	if strings.Contains(u.Path, "..") {
		return errors.New("path traversal (..) not allowed")
	}

	if strings.Contains(u.Path, "//") {
		return errors.New("empty path segments (//) not allowed")
	}

	return nil
}

// validateSafeChars checks that a string contains only alphanumeric, dash, underscore, dot, slash.
func validateSafeChars(s string) error {
	for _, r := range s {
		if !isSafe(r) {
			return fmt.Errorf("unsafe character %q", r)
		}
	}
	return nil
}

// isSafe returns true if r is an allowed character in host/path.
// Specifically excludes colon to prevent port/scheme confusion.
func isSafe(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r == '-' || r == '_' || r == '.' || r == '/'
}

// validateParamString validates query parameter keys and values.
func validateParamString(s string) error {
	if s == "" {
		return errors.New("cannot be empty")
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return fmt.Errorf("contains invalid character %q", r)
		}
	}
	return nil
}

// open opens a URL in the system browser.
func open(ctx context.Context, rawURL string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "/usr/bin/open", "-u", rawURL)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32.exe", "url.dll,FileProtocolHandler", rawURL)
	default:
		xdgOpen, err := findXDGOpen()
		if err != nil {
			return err
		}
		cmd = exec.CommandContext(ctx, xdgOpen, rawURL)
	}

	return cmd.Start()
}

// findXDGOpen locates xdg-open on Unix systems.
func findXDGOpen() (string, error) {
	if path, err := exec.LookPath("xdg-open"); err == nil {
		return path, nil
	}

	for _, path := range []string{
		"/usr/local/bin/xdg-open",
		"/usr/bin/xdg-open",
		"/usr/pkg/bin/xdg-open",
		"/opt/local/bin/xdg-open",
	} {
		if _, err := exec.LookPath(path); err == nil {
			return path, nil
		}
	}

	return "", errors.New("xdg-open not found")
}
