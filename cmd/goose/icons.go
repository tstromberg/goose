package main

import (
	"log/slog"
)

// Icon variables are defined in platform-specific files:
// - icons_windows.go: uses .ico files.
// - icons_unix.go: uses .png files.

// IconType represents different icon states.
type IconType int

const (
	IconSmiling IconType = iota // No blocked PRs
	IconGoose                   // Incoming PRs blocked
	IconPopper                  // Outgoing PRs blocked
	IconBoth                    // Both incoming and outgoing blocked
	IconWarning                 // General error/warning
	IconLock                    // Authentication error
)

// getIcon returns the icon bytes for the given type.
func getIcon(iconType IconType) []byte {
	switch iconType {
	case IconGoose:
		return iconGoose
	case IconPopper:
		return iconPopper
	case IconSmiling:
		return iconSmiling
	case IconWarning:
		return iconWarning
	case IconLock:
		return iconLock
	case IconBoth:
		// For both, we'll use the goose icon as primary
		return iconGoose
	default:
		return iconSmiling
	}
}

// setTrayIcon updates the system tray icon based on PR counts.
func (app *App) setTrayIcon(iconType IconType) {
	iconBytes := getIcon(iconType)
	if iconBytes == nil || len(iconBytes) == 0 {
		slog.Warn("Icon bytes are empty, skipping icon update", "type", iconType)
		return
	}

	app.systrayInterface.SetIcon(iconBytes)
	slog.Debug("[TRAY] Setting icon", "type", iconType)
}
