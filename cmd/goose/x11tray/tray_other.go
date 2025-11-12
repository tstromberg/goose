//go:build !linux && !freebsd && !openbsd && !netbsd && !dragonfly && !solaris && !illumos && !aix

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
func (p *ProxyProcess) Stop() error {
	return nil
}

// TryProxy is not needed on non-Unix platforms and always returns nil.
func TryProxy(ctx context.Context) (*ProxyProcess, error) {
	return nil, nil
}

// EnsureTray always succeeds on non-Unix platforms.
func EnsureTray(ctx context.Context) (*ProxyProcess, error) {
	return nil, nil
}

// ShowContextMenu is a no-op on non-Unix platforms.
// On macOS and Windows, the systray library handles menu display natively.
func ShowContextMenu() {
	// No-op - menu display is handled by the systray library on these platforms
}
