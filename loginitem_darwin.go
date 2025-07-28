//go:build darwin
// +build darwin

// Package main - loginitem_darwin.go provides macOS-specific login item management.
package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/energye/systray"
)

// isLoginItem checks if the app is set to start at login
func isLoginItem() bool {
	appPath, err := getAppPath()
	if err != nil {
		log.Printf("Failed to get app path: %v", err)
		return false
	}

	// Use osascript to check login items
	script := fmt.Sprintf(`tell application "System Events" to get the name of every login item where path is "%s"`, appPath)
	cmd := exec.Command("osascript", "-e", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed to check login items: %v", err)
		return false
	}

	result := strings.TrimSpace(string(output))
	return result != ""
}

// setLoginItem adds or removes the app from login items
func setLoginItem(enable bool) error {
	appPath, err := getAppPath()
	if err != nil {
		return fmt.Errorf("get app path: %w", err)
	}

	if enable {
		// Add to login items
		script := fmt.Sprintf(`tell application "System Events" to make login item at end with properties {path:"%s", hidden:false}`, appPath)
		cmd := exec.Command("osascript", "-e", script)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("add login item: %w (output: %s)", err, string(output))
		}
		log.Printf("Added %s to login items", appPath)
	} else {
		// Remove from login items
		appName := filepath.Base(appPath)
		appName = strings.TrimSuffix(appName, ".app")
		script := fmt.Sprintf(`tell application "System Events" to delete login item "%s"`, appName)
		cmd := exec.Command("osascript", "-e", script)
		if output, err := cmd.CombinedOutput(); err != nil {
			// Ignore error if item doesn't exist
			if !strings.Contains(string(output), "Can't get login item") {
				return fmt.Errorf("remove login item: %w (output: %s)", err, string(output))
			}
		}
		log.Printf("Removed %s from login items", appName)
	}

	return nil
}

// isRunningFromAppBundle checks if the app is running from a .app bundle
func isRunningFromAppBundle() bool {
	execPath, err := os.Executable()
	if err != nil {
		return false
	}

	// Resolve any symlinks
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return false
	}

	// Check if we're running from an app bundle
	// App bundles have the structure: /path/to/App.app/Contents/MacOS/executable
	return strings.Contains(execPath, ".app/Contents/MacOS/")
}

// getAppPath returns the path to the application bundle
func getAppPath() (string, error) {
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
	return "", fmt.Errorf("not running from app bundle")
}

// addLoginItemUI adds the login item menu option (macOS only)
func addLoginItemUI(app *App) {
	// Only show login item menu if running from an app bundle
	if !isRunningFromAppBundle() {
		log.Println("Hiding 'Start at Login' menu item - not running from app bundle")
		return
	}

	loginItem := systray.AddMenuItem("Start at Login", "Automatically start when you log in")
	app.menuItems = append(app.menuItems, loginItem)

	// Set initial state
	if isLoginItem() {
		loginItem.Check()
	}

	loginItem.Click(func() {
		isEnabled := isLoginItem()
		newState := !isEnabled

		if err := setLoginItem(newState); err != nil {
			log.Printf("Failed to set login item: %v", err)
			// Revert the UI state on error
			if isEnabled {
				loginItem.Check()
			} else {
				loginItem.Uncheck()
			}
			return
		}

		// Update UI state
		if newState {
			loginItem.Check()
		} else {
			loginItem.Uncheck()
		}
	})
}
