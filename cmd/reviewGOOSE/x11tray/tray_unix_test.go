//go:build linux || freebsd || openbsd || netbsd || dragonfly || solaris || illumos || aix

package x11tray

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

func skipIfNoDBus(t *testing.T) {
	t.Helper()
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Skipf("D-Bus session bus not available: %v", err)
	}
	conn.Close()
}

func TestHealthCheck(t *testing.T) {
	skipIfNoDBus(t)

	err := HealthCheck()
	if err == nil {
		return // system tray available
	}
	// Verify error mentions the expected service
	if !strings.Contains(err.Error(), "StatusNotifierWatcher") && !strings.Contains(err.Error(), "system tray") {
		t.Errorf("HealthCheck() error = %v, want mention of StatusNotifierWatcher or system tray", err)
	}
}

func TestProxyProcessStop(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		p := &ProxyProcess{}
		if err := p.Stop(); err != nil {
			t.Errorf("Stop() = %v, want nil", err)
		}
	})

	t.Run("with cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		p := &ProxyProcess{cancel: cancel}

		if err := p.Stop(); err != nil {
			t.Errorf("Stop() = %v, want nil", err)
		}
		select {
		case <-ctx.Done():
			// expected
		default:
			t.Error("Stop() did not cancel context")
		}
	})

	t.Run("unstarted cmd", func(t *testing.T) {
		p := &ProxyProcess{cmd: exec.Command("echo", "test")}
		if err := p.Stop(); err != nil {
			t.Errorf("Stop() = %v, want nil", err)
		}
	})
}

func TestTryProxy(t *testing.T) {
	p, err := TryProxy(context.Background())
	if err == nil {
		p.Stop()
		return // snixembed installed and working
	}
	if !strings.Contains(err.Error(), "snixembed") {
		t.Errorf("TryProxy() error = %v, want mention of snixembed", err)
	}
}

func TestEnsureTray(t *testing.T) {
	skipIfNoDBus(t)

	p, err := EnsureTray(context.Background())
	if err == nil {
		if p != nil {
			p.Stop()
		}
		return
	}
	// Verify error is reasonable
	msg := err.Error()
	if !strings.Contains(msg, "tray") && !strings.Contains(msg, "proxy") && !strings.Contains(msg, "snixembed") {
		t.Errorf("EnsureTray() error = %v, want mention of tray/proxy/snixembed", err)
	}
}

func TestShowContextMenu(t *testing.T) {
	// Should not panic regardless of D-Bus availability
	ShowContextMenu()
}
