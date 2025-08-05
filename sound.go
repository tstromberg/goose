// Package main - sound.go handles platform-specific sound playback.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed media/launch-85216.wav
var launchSound []byte

//go:embed media/dark-impact-232945.wav
var impactSound []byte

var soundCacheOnce sync.Once

// initSoundCache writes embedded sounds to cache directory once.
func (app *App) initSoundCache() {
	soundCacheOnce.Do(func() {
		// Create sounds subdirectory in cache
		soundDir := filepath.Join(app.cacheDir, "sounds")
		if err := os.MkdirAll(soundDir, 0o700); err != nil {
			log.Printf("Failed to create sound cache dir: %v", err)
			return
		}

		// Write launch sound
		launchPath := filepath.Join(soundDir, "launch.wav")
		if _, err := os.Stat(launchPath); os.IsNotExist(err) {
			if err := os.WriteFile(launchPath, launchSound, 0o600); err != nil {
				log.Printf("Failed to cache launch sound: %v", err)
			}
		}

		// Write impact sound
		impactPath := filepath.Join(soundDir, "impact.wav")
		if _, err := os.Stat(impactPath); os.IsNotExist(err) {
			if err := os.WriteFile(impactPath, impactSound, 0o600); err != nil {
				log.Printf("Failed to cache impact sound: %v", err)
			}
		}
	})
}

// playSound plays a cached sound file using platform-specific commands.
func (app *App) playSound(ctx context.Context, soundType string) {
	log.Printf("[SOUND] Playing %s sound", soundType)
	// Ensure sounds are cached
	app.initSoundCache()

	// Select the sound file with validation to prevent path traversal
	allowedSounds := map[string]string{
		"rocket":    "launch.wav",
		"detective": "impact.wav",
	}

	soundName, ok := allowedSounds[soundType]
	if !ok {
		log.Printf("Invalid sound type requested: %s", soundType)
		return
	}

	// Double-check the sound name contains no path separators
	if strings.Contains(soundName, "/") || strings.Contains(soundName, "\\") || strings.Contains(soundName, "..") {
		log.Printf("Sound name contains invalid characters: %s", soundName)
		return
	}

	soundPath := filepath.Join(app.cacheDir, "sounds", soundName)

	// Check if file exists
	if _, err := os.Stat(soundPath); os.IsNotExist(err) {
		log.Printf("Sound file not found in cache: %s", soundPath)
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
			// Use PowerShell's SoundPlayer with proper escaping
			//nolint:gocritic // Need literal quotes in PowerShell script
			script := fmt.Sprintf(`(New-Object Media.SoundPlayer "%s").PlaySync()`,
				strings.ReplaceAll(soundPath, `"`, `""`))
			cmd = exec.CommandContext(soundCtx, "powershell", "-WindowStyle", "Hidden", "-c", script)
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
				log.Printf("Failed to play sound: %v", err)
			}
		}
	}()
}
