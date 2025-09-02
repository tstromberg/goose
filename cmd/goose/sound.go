// Package main - sound.go handles platform-specific sound playback.
package main

import (
	"context"
	_ "embed"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed sounds/jet.wav
var jetSound []byte

//go:embed sounds/honk.wav
var honkSound []byte

var soundCacheOnce sync.Once

// initSoundCache writes embedded sounds to cache directory once.
func (app *App) initSoundCache() {
	soundCacheOnce.Do(func() {
		// Create sounds subdirectory in cache
		soundDir := filepath.Join(app.cacheDir, "sounds")
		if err := os.MkdirAll(soundDir, 0o700); err != nil {
			slog.Error("Failed to create sound cache dir", "error", err)
			return
		}

		// Write jet sound
		jetPath := filepath.Join(soundDir, "jet.wav")
		if _, err := os.Stat(jetPath); os.IsNotExist(err) {
			if err := os.WriteFile(jetPath, jetSound, 0o600); err != nil {
				slog.Error("Failed to cache jet sound", "error", err)
			}
		}

		// Write honk sound
		honkPath := filepath.Join(soundDir, "honk.wav")
		if _, err := os.Stat(honkPath); os.IsNotExist(err) {
			if err := os.WriteFile(honkPath, honkSound, 0o600); err != nil {
				slog.Error("Failed to cache honk sound", "error", err)
			}
		}
	})
}

// playSound plays a cached sound file using platform-specific commands.
func (app *App) playSound(ctx context.Context, soundType string) {
	// Check if audio cues are enabled
	app.mu.RLock()
	audioEnabled := app.enableAudioCues
	app.mu.RUnlock()

	if !audioEnabled {
		slog.Debug("[SOUND] Sound playback skipped (audio cues disabled)", "soundType", soundType)
		return
	}

	slog.Debug("[SOUND] Playing sound", "soundType", soundType)
	// Ensure sounds are cached
	app.initSoundCache()

	// Select the sound file with validation to prevent path traversal
	allowedSounds := map[string]string{
		"rocket": "jet.wav",
		"honk":   "honk.wav",
	}

	soundName, ok := allowedSounds[soundType]
	if !ok {
		slog.Error("Invalid sound type requested", "soundType", soundType)
		return
	}

	// Double-check the sound name contains no path separators
	if strings.Contains(soundName, "/") || strings.Contains(soundName, "\\") || strings.Contains(soundName, "..") {
		slog.Error("Sound name contains invalid characters", "soundName", soundName)
		return
	}

	soundPath := filepath.Join(app.cacheDir, "sounds", soundName)

	// Check if file exists
	if _, err := os.Stat(soundPath); os.IsNotExist(err) {
		slog.Error("Sound file not found in cache", "soundPath", soundPath)
		return
	}

	// Check if we're in test mode (environment variable set by tests)
	if os.Getenv("GOOSE_TEST_MODE") == "1" {
		slog.Debug("[SOUND] Test mode - skipping actual sound playback", "soundPath", soundPath)
		return
	}

	// Play sound in background
	go func() {
		// Use a timeout context for sound playback
		soundCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.CommandContext(soundCtx, "afplay", soundPath)
		case "windows":
			// Use Windows Media Player API via rundll32 to avoid PowerShell script injection
			// This is safer than constructing PowerShell scripts with user paths
			cmd = exec.CommandContext(soundCtx, "cmd", "/c", "start", "/min", "", soundPath)
		case "linux":
			// Try paplay first (PulseAudio), then aplay (ALSA)
			cmd = exec.CommandContext(soundCtx, "paplay", soundPath)
			if err := cmd.Run(); err != nil {
				cmd = exec.CommandContext(soundCtx, "aplay", "-q", soundPath)
			}
		default:
			return
		}

		if cmd != nil {
			if err := cmd.Run(); err != nil {
				slog.Error("Failed to play sound", "error", err)
			}
		}
	}()
}
