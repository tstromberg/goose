//go:build !linux && !freebsd && !openbsd && !netbsd && !dragonfly && !solaris && !illumos && !aix

package x11tray

import (
	"context"
	"testing"
)

func TestHealthCheck(t *testing.T) {
	if err := HealthCheck(); err != nil {
		t.Errorf("HealthCheck() = %v, want nil", err)
	}
}

func TestProxyProcessStop(t *testing.T) {
	p := &ProxyProcess{}
	if err := p.Stop(); err != nil {
		t.Errorf("Stop() = %v, want nil", err)
	}
}

func TestTryProxy(t *testing.T) {
	p, err := TryProxy(context.Background())
	if err != nil {
		t.Errorf("TryProxy() error = %v, want nil", err)
	}
	if p == nil {
		t.Error("TryProxy() = nil, want non-nil")
	}
	if err := p.Stop(); err != nil {
		t.Errorf("Stop() = %v, want nil", err)
	}
}

func TestTryProxyCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p, err := TryProxy(ctx)
	if err != nil {
		t.Errorf("TryProxy() error = %v, want nil", err)
	}
	if p == nil {
		t.Error("TryProxy() = nil, want non-nil")
	}
}

func TestEnsureTray(t *testing.T) {
	p, err := EnsureTray(context.Background())
	if err != nil {
		t.Errorf("EnsureTray() error = %v, want nil", err)
	}
	if p == nil {
		t.Error("EnsureTray() = nil, want non-nil")
	}
	if err := p.Stop(); err != nil {
		t.Errorf("Stop() = %v, want nil", err)
	}
}

func TestEnsureTrayCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p, err := EnsureTray(ctx)
	if err != nil {
		t.Errorf("EnsureTray() error = %v, want nil", err)
	}
	if p == nil {
		t.Error("EnsureTray() = nil, want non-nil")
	}
}

func TestShowContextMenu(t *testing.T) {
	// No-op on non-Unix; just verify no panic
	ShowContextMenu()
}
