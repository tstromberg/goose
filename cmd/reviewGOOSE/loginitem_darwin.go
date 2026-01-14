//go:build darwin

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
	"sync"
	"sync/atomic"
	"time"

	"github.com/energye/systray"
)

var (
	legacyCleanupOnce sync.Once
	loginItemMu       sync.Mutex  // prevents concurrent toggle operations
	loginItemCached   atomic.Bool // cached state to avoid repeated osascript calls
	loginItemChecked  atomic.Bool // whether we've done the initial check
)

const osascriptTimeout = 10 * time.Second

// escapeForAppleScript validates and escapes a string for safe use in AppleScript.
func escapeForAppleScript(s string) string {
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') && r != ' ' && r != '.' &&
			r != '/' && r != '-' && r != '_' {
			slog.Error("invalid character for AppleScript", "char", string(r), "input", s)
			return ""
		}
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`)
}

// bundlePath returns the .app bundle path, or an error if not running from one.
func bundlePath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("executable: %w", err)
	}
	p, err = filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("symlinks: %w", err)
	}
	if i := strings.Index(p, ".app/Contents/MacOS/"); i != -1 {
		return p[:i+4], nil
	}
	return "", errors.New("not an app bundle")
}

// queryLoginItemEnabled queries System Events for login item status (slow, use sparingly).
func queryLoginItemEnabled(ctx context.Context) bool {
	bp, err := bundlePath()
	if err != nil {
		return false
	}
	ep := escapeForAppleScript(bp)
	if ep == "" {
		return false
	}

	ctx, cancel := context.WithTimeout(ctx, osascriptTimeout)
	defer cancel()

	//nolint:gocritic // ep is already escaped
	script := fmt.Sprintf(
		`tell application "System Events" to get the name of every login item where path is "%s"`, ep)
	out, err := exec.CommandContext(ctx, "osascript", "-e", script).CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			slog.Warn("timeout checking login item")
		} else {
			slog.Debug("failed to check login item", "error", err)
		}
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// loginItemEnabled returns cached login item state (fast, safe to call frequently).
func loginItemEnabled() bool {
	return loginItemCached.Load()
}

// setLoginItem adds or removes the app from login items.
func setLoginItem(ctx context.Context, enable bool) error {
	bp, err := bundlePath()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, osascriptTimeout)
	defer cancel()

	var script string
	if enable {
		ep := escapeForAppleScript(bp)
		if ep == "" {
			return fmt.Errorf("invalid path: %s", bp)
		}
		//nolint:gocritic // ep is already escaped
		script = fmt.Sprintf(
			`tell application "System Events" to make login item at end with properties {path:"%s", hidden:false}`, ep)
	} else {
		name := strings.TrimSuffix(filepath.Base(bp), ".app")
		en := escapeForAppleScript(name)
		if en == "" {
			return fmt.Errorf("invalid name: %s", name)
		}
		//nolint:gocritic // en is already escaped
		script = fmt.Sprintf(`tell application "System Events" to delete login item "%s"`, en)
	}

	slog.Debug("executing login item command", "enable", enable)
	out, err := exec.CommandContext(ctx, "osascript", "-e", script).CombinedOutput()
	if err != nil {
		s := string(out)
		if !enable && strings.Contains(s, "Can't get login item") {
			return nil
		}
		if ctx.Err() == context.DeadlineExceeded {
			return errors.New("timed out")
		}
		return fmt.Errorf("%w (output: %s)", err, s)
	}
	slog.Info("login item updated", "enabled", enable, "path", bp)
	return nil
}

// addLoginItemUI adds the login item menu option (macOS only).
func addLoginItemUI(ctx context.Context, app *App) {
	if _, err := bundlePath(); err != nil {
		slog.Debug("hiding Start at Login menu item", "error", err)
		return
	}

	// Remove legacy login items (once per session, async to not block menu).
	// Uses background context since this is fire-and-forget cleanup.
	legacyCleanupOnce.Do(func() {
		go func() {
			for _, name := range []string{"Ready to Review", "Review Goose"} {
				en := escapeForAppleScript(name)
				if en == "" {
					continue
				}
				// Use background context - this cleanup should complete even if app is shutting down.
				cleanupCtx, cancel := context.WithTimeout(context.Background(), osascriptTimeout)
				script := fmt.Sprintf(`tell application "System Events" to delete login item %q`, en)
				out, err := exec.CommandContext(cleanupCtx, "osascript", "-e", script).CombinedOutput()
				cancel()
				if err == nil {
					slog.Info("removed legacy login item", "name", name)
				} else if !strings.Contains(string(out), "Can't get login item") {
					slog.Debug("could not remove legacy login item", "name", name, "error", err)
				}
			}
		}()
	})

	// Check state asynchronously on first menu build, use cached value for display.
	if !loginItemChecked.Load() {
		go func() {
			loginItemCached.Store(queryLoginItemEnabled(ctx))
			loginItemChecked.Store(true)
			slog.Debug("login item state refreshed", "enabled", loginItemCached.Load())
		}()
	}

	// Use cached state for menu display (fast, non-blocking).
	text := "Start at Login"
	if loginItemEnabled() {
		text = "✓ Start at Login"
	}
	item := systray.AddMenuItem(text, "Automatically start when you log in")

	item.Click(func() {
		// Prevent concurrent toggle operations.
		if !loginItemMu.TryLock() {
			slog.Debug("[LOGIN_ITEM] toggle already in progress")
			return
		}
		defer loginItemMu.Unlock()

		cur := loginItemEnabled()
		next := !cur
		slog.Debug("[LOGIN_ITEM] toggling", "from", cur, "to", next)

		// Optimistically update cache before the slow osascript call.
		loginItemCached.Store(next)

		if err := setLoginItem(ctx, next); err != nil {
			slog.Error("[LOGIN_ITEM] failed to set", "error", err, "enable", next)
			loginItemCached.Store(cur) // revert on failure
			go showLoginItemError(ctx, next, err)
			return
		}

		slog.Info("[SETTINGS] Start at Login toggled", "enabled", next)
		app.rebuildMenu(ctx)
	})
}

func showLoginItemError(ctx context.Context, enable bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	action, verb := "enable", "adding"
	if !enable {
		action, verb = "disable", "removing"
	}
	msg := fmt.Sprintf("Could not %s 'Start at Login'.\n\nError: %v\n\n"+
		"Try %s reviewGOOSE manually in System Settings > General > Login Items.",
		action, err, verb)
	script := fmt.Sprintf(
		`display dialog %q with title "reviewGOOSE" buttons {"OK"} default button "OK" with icon caution`, msg)
	if out, err := exec.CommandContext(ctx, "osascript", "-e", script).CombinedOutput(); err != nil {
		slog.Debug("failed to show error dialog", "error", err, "output", string(out))
	}
}
