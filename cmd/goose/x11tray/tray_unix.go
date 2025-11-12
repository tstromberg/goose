//go:build linux || freebsd || openbsd || netbsd || dragonfly || solaris || illumos || aix

// Package x11tray provides system tray functionality for Unix platforms.
// It handles StatusNotifierItem integration via DBus and manages the snixembed proxy
// for compatibility with legacy X11 system trays.
package x11tray

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	statusNotifierWatcher     = "org.kde.StatusNotifierWatcher"
	statusNotifierWatcherPath = "/StatusNotifierWatcher"
)

// HealthCheck verifies that a system tray implementation is available via D-Bus.
// It checks for the KDE StatusNotifierWatcher service which is required for
// system tray icons on modern Linux desktops.
//
// Returns nil if a tray is available, or an error describing the issue.
func HealthCheck() error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("failed to connect to D-Bus session bus: %w", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			slog.Debug("[X11TRAY] Failed to close DBus connection", "error", err)
		}
	}()

	// Check if the StatusNotifierWatcher service exists
	var names []string
	err = conn.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Store(&names)
	if err != nil {
		return fmt.Errorf("failed to query D-Bus services: %w", err)
	}

	for _, name := range names {
		if name == statusNotifierWatcher {
			slog.Debug("[X11TRAY] StatusNotifierWatcher found", "service", statusNotifierWatcher)
			return nil
		}
	}

	return fmt.Errorf("no system tray found: %s service not available", statusNotifierWatcher)
}

// ProxyProcess represents a running snixembed background process.
type ProxyProcess struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

// Stop terminates the proxy process gracefully.
func (p *ProxyProcess) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}

// TryProxy attempts to start snixembed as a system tray proxy service.
// snixembed bridges legacy X11 system trays to modern StatusNotifier-based trays.
//
// If successful, returns a ProxyProcess that should be stopped when the application exits.
// Returns an error if snixembed is not found or fails to start successfully.
func TryProxy(ctx context.Context) (*ProxyProcess, error) {
	// Check if snixembed is available
	snixembedPath, err := exec.LookPath("snixembed")
	if err != nil {
		return nil, errors.New(
			"snixembed not found in PATH: install it with your package manager " +
				"(e.g., 'apt install snixembed' or 'yay -S snixembed')")
	}

	slog.Info("[X11TRAY] Starting snixembed proxy", "path", snixembedPath)

	// Create a cancellable context for the proxy process
	proxyCtx, cancel := context.WithCancel(ctx)

	// Start snixembed in the background
	cmd := exec.CommandContext(proxyCtx, snixembedPath)

	// Capture output for debugging
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to start snixembed: %w", err)
	}

	proxy := &ProxyProcess{
		cmd:    cmd,
		cancel: cancel,
	}

	// Give snixembed time to register with D-Bus
	// This is necessary because the service takes a moment to become available
	time.Sleep(500 * time.Millisecond)

	// Verify that the proxy worked by checking again
	if err := HealthCheck(); err != nil {
		// snixembed started but didn't fix the problem
		if stopErr := proxy.Stop(); stopErr != nil {
			slog.Debug("[X11TRAY] Failed to stop proxy after failed health check", "error", stopErr)
		}
		return nil, fmt.Errorf("snixembed started but system tray still unavailable: %w", err)
	}

	slog.Info("[X11TRAY] snixembed proxy started successfully")
	return proxy, nil
}

// EnsureTray checks for system tray availability and attempts to start a proxy if needed.
// This is a convenience function that combines HealthCheck and TryProxy.
//
// Returns a ProxyProcess if one was started (caller must Stop() it on exit), or nil if
// the native tray was available. Returns an error if no tray solution could be found.
func EnsureTray(ctx context.Context) (*ProxyProcess, error) {
	// First, check if we already have a working tray
	if err := HealthCheck(); err == nil {
		slog.Debug("[X11TRAY] Native system tray available")
		// No proxy needed (nil) and no error (nil) - native tray is working
		return nil, nil //nolint:nilnil // nil proxy is valid when native tray exists
	}

	slog.Warn("[X11TRAY] No native system tray found, attempting to start proxy")

	// Try to start the proxy
	proxy, err := TryProxy(ctx)
	if err != nil {
		return nil, fmt.Errorf("system tray unavailable and proxy failed: %w", err)
	}

	return proxy, nil
}

// ShowContextMenu triggers the context menu via DBus on Unix platforms.
// On Linux/FreeBSD with StatusNotifierItem, the menu parameter in click handlers is nil,
// so we need to manually call the ContextMenu method via DBus to show the menu.
func ShowContextMenu() {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		slog.Warn("[X11TRAY] Failed to connect to session bus", "error", err)
		return
	}
	defer func() {
		if err := conn.Close(); err != nil {
			slog.Debug("[X11TRAY] Failed to close DBus connection", "error", err)
		}
	}()

	// Find our StatusNotifierItem service
	var names []string
	err = conn.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Store(&names)
	if err != nil {
		slog.Warn("[X11TRAY] Failed to list DBus names", "error", err)
		return
	}

	// Find the StatusNotifierItem service for this process
	var serviceName string
	pid := os.Getpid()
	expectedPrefix := fmt.Sprintf("org.kde.StatusNotifierItem-%d-", pid)
	for _, name := range names {
		if strings.HasPrefix(name, expectedPrefix) {
			serviceName = name
			break
		}
	}

	if serviceName == "" {
		slog.Warn("[X11TRAY] StatusNotifierItem service not found", "pid", pid, "expectedPrefix", expectedPrefix)
		return
	}

	slog.Info("[X11TRAY] Attempting to trigger context menu", "service", serviceName)

	// Try different methods to trigger the menu display
	obj := conn.Object(serviceName, "/StatusNotifierItem")

	// First try: Call ContextMenu method (standard StatusNotifierItem)
	call := obj.Call("org.kde.StatusNotifierItem.ContextMenu", 0, int32(0), int32(0))
	if call.Err != nil {
		slog.Info("[X11TRAY] ContextMenu method failed, trying SecondaryActivate", "error", call.Err)

		// Second try: Call SecondaryActivate (right-click equivalent)
		call = obj.Call("org.kde.StatusNotifierItem.SecondaryActivate", 0, int32(0), int32(0))
		if call.Err != nil {
			slog.Warn("[X11TRAY] Both ContextMenu and SecondaryActivate failed", "contextMenuErr", call.Err)
			slog.Info("[X11TRAY] Note: Menu should still work with right-click via snixembed")
			return
		}
		slog.Info("[X11TRAY] Successfully triggered SecondaryActivate")
		return
	}

	slog.Info("[X11TRAY] Successfully triggered ContextMenu")
}
