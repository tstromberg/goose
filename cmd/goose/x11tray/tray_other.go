//go:build !linux && !freebsd && !openbsd && !netbsd && !dragonfly && !solaris && !illumos && !aix

// Package x11tray provides system tray proxy support for Unix-like systems.
// On non-Unix platforms (macOS, Windows), the systray library handles tray functionality natively.
package x11tray

import (
	"context"
)

// HealthCheck always returns nil on non-Unix platforms where system tray
// availability is handled differently by the OS.
func HealthCheck() error {
	return nil
}

// ProxyProcess represents a running proxy process (not used on non-Unix platforms).
type ProxyProcess struct{}

// Stop is a no-op on non-Unix platforms.
func (*ProxyProcess) Stop() error {
	return nil
}

// TryProxy is not needed on non-Unix platforms.
func TryProxy(_ context.Context) (*ProxyProcess, error) {
	return &ProxyProcess{}, nil
}

// EnsureTray always succeeds on non-Unix platforms.
func EnsureTray(_ context.Context) (*ProxyProcess, error) {
	return &ProxyProcess{}, nil
}

// ShowContextMenu is a no-op on non-Unix platforms.
// On macOS and Windows, the systray library handles menu display natively.
func ShowContextMenu() {
	// No-op - menu display is handled by the systray library on these platforms
}
