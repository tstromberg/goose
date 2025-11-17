package main

import (
	"log/slog"
)

// Icon implementations are in platform-specific files:
//   - icons_darwin.go: macOS (static PNG icons, counts shown in title)
//   - icons_badge.go: Linux/BSD/Windows (dynamic circle badges with counts)

// IconType represents different icon states.
type IconType int

const (
	IconSmiling   IconType = iota // No blocked PRs
	IconGoose                     // Incoming PRs blocked
	IconPopper                    // Outgoing PRs blocked
	IconCockroach                 // Outgoing PRs blocked (fix_tests only)
	IconBoth                      // Both incoming and outgoing blocked
	IconWarning                   // General error/warning
	IconLock                      // Authentication error
)

// getIcon returns icon bytes for the given type and counts.
// Implementation is platform-specific:
//   - macOS: returns static icons (counts displayed in title bar)
//   - Linux/Windows: generates dynamic badges with embedded counts.
// Implemented in icons_darwin.go and icons_badge.go.

// setTrayIcon updates the system tray icon.
func (app *App) setTrayIcon(iconType IconType, counts PRCounts) {
	iconBytes := getIcon(iconType, counts)
	if len(iconBytes) == 0 {
		slog.Warn("icon bytes empty, skipping update", "type", iconType)
		return
	}

	app.systrayInterface.SetIcon(iconBytes)
	slog.Debug("tray icon updated", "type", iconType, "incoming", counts.IncomingBlocked, "outgoing", counts.OutgoingBlocked)
}
