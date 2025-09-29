//go:build darwin

// Package main - loginitem_darwin.go provides macOS-specific login item management.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/energye/systray"
)

// validateAndEscapePathForAppleScript validates and escapes a path for safe use in AppleScript.
// Returns empty string if path contains invalid characters.
func validateAndEscapePathForAppleScript(path string) string {
	// Validate path contains only safe characters
	for _, r := range path {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') && r != ' ' && r != '.' &&
			r != '/' && r != '-' && r != '_' {
			slog.Error("Path contains invalid character for AppleScript", "char", string(r), "path", path)
			return ""
		}
	}
	// Escape backslashes first then quotes
	path = strings.ReplaceAll(path, `\`, `\\`)
	path = strings.ReplaceAll(path, `"`, `\"`)
	return path
}

// isLoginItem checks if the app is set to start at login.
func isLoginItem(ctx context.Context) bool {
	appPath, err := appPath()
	if err != nil {
		slog.Error("Failed to get app path", "error", err)
		return false
	}

	// Use osascript to check login items
	escapedPath := validateAndEscapePathForAppleScript(appPath)
	if escapedPath == "" {
		slog.Error("Invalid app path for AppleScript", "path", appPath)
		return false
	}
	// We use %s here because the string is already validated and escaped
	//nolint:gocritic // already escaped
	script := fmt.Sprintf(
		`tell application "System Events" to get the name of every login item where path is "%s"`,
		escapedPath)
	slog.Debug("Executing command", "command", "osascript", "script", script)
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Error("Failed to check login items", "error", err)
		return false
	}

	result := strings.TrimSpace(string(output))
	return result != ""
}

// setLoginItem adds or removes the app from login items.
func setLoginItem(ctx context.Context, enable bool) error {
	appPath, err := appPath()
	if err != nil {
		return fmt.Errorf("get app path: %w", err)
	}

	if enable {
		// Add to login items
		escapedPath := validateAndEscapePathForAppleScript(appPath)
		if escapedPath == "" {
			return fmt.Errorf("invalid app path for AppleScript: %s", appPath)
		}
		// We use %s here because the string is already validated and escaped
		//nolint:gocritic // already escaped
		script := fmt.Sprintf(
			`tell application "System Events" to make login item at end with properties {path:"%s", hidden:false}`,
			escapedPath)
		slog.Debug("Executing command", "command", "osascript", "script", script)
		cmd := exec.CommandContext(ctx, "osascript", "-e", script)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("add login item: %w (output: %s)", err, string(output))
		}
		slog.Info("Added to login items", "path", appPath)
	} else {
		// Remove from login items
		appName := filepath.Base(appPath)
		appName = strings.TrimSuffix(appName, ".app")
		escapedName := validateAndEscapePathForAppleScript(appName)
		if escapedName == "" {
			return fmt.Errorf("invalid app name for AppleScript: %s", appName)
		}
		// We use %s here because the string is already validated and escaped
		script := fmt.Sprintf(`tell application "System Events" to delete login item "%s"`, escapedName) //nolint:gocritic // already escaped
		slog.Debug("Executing command", "command", "osascript", "script", script)
		cmd := exec.CommandContext(ctx, "osascript", "-e", script)
		if output, err := cmd.CombinedOutput(); err != nil {
			// Ignore error if item doesn't exist
			if !strings.Contains(string(output), "Can't get login item") {
				return fmt.Errorf("remove login item: %w (output: %s)", err, string(output))
			}
		}
		slog.Info("Removed from login items", "app", appName)
	}

	return nil
}

// appPath returns the path to the application bundle.
func appPath() (string, error) {
	// Get the executable path
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("get executable: %w", err)
	}

	// Resolve any symlinks
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return "", fmt.Errorf("eval symlinks: %w", err)
	}

	// Check if we're running from an app bundle
	// App bundles have the structure: /path/to/App.app/Contents/MacOS/executable
	if strings.Contains(execPath, ".app/Contents/MacOS/") {
		// Extract the .app path
		parts := strings.Split(execPath, ".app/Contents/MacOS/")
		if len(parts) >= 2 {
			return parts[0] + ".app", nil
		}
	}

	// Not running from an app bundle, return empty string to indicate this
	return "", errors.New("not running from app bundle")
}

// addLoginItemUI adds the login item menu option (macOS only).
func addLoginItemUI(ctx context.Context, app *App) {
	// Check if we're running from an app bundle
	execPath, err := os.Executable()
	if err != nil {
		slog.Debug("Hiding 'Start at Login' menu item - could not get executable path")
		return
	}

	// Resolve any symlinks
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		slog.Debug("Hiding 'Start at Login' menu item - could not resolve symlinks")
		return
	}

	// App bundles have the structure: /path/to/App.app/Contents/MacOS/executable
	if !strings.Contains(execPath, ".app/Contents/MacOS/") {
		slog.Debug("Hiding 'Start at Login' menu item - not running from app bundle")
		return
	}

	// Add text checkmark for consistency with other menu items
	var loginText string
	if isLoginItem(ctx) {
		loginText = "✓ Start at Login"
	} else {
		loginText = "Start at Login"
	}
	loginItem := systray.AddMenuItem(loginText, "Automatically start when you log in")

	loginItem.Click(func() {
		isEnabled := isLoginItem(ctx)
		newState := !isEnabled

		if err := setLoginItem(ctx, newState); err != nil {
			slog.Error("Failed to set login item", "error", err)
			return
		}

		// Update UI state
		slog.Info("[SETTINGS] Start at Login toggled", "enabled", newState)

		// Rebuild menu to update checkmark
		app.rebuildMenu(ctx)
	})
}
