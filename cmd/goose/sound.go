// Package main - sound.go handles platform-specific sound playback.
package main

import (
	"context"
	_ "embed"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed sounds/tada.wav
var tadaSound []byte

//go:embed sounds/honk.wav
var honkSound []byte

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

		// Write tada sound
		tadaPath := filepath.Join(soundDir, "tada.wav")
		if _, err := os.Stat(tadaPath); os.IsNotExist(err) {
			if err := os.WriteFile(tadaPath, tadaSound, 0o600); err != nil {
				log.Printf("Failed to cache tada sound: %v", err)
			}
		}

		// Write honk sound
		honkPath := filepath.Join(soundDir, "honk.wav")
		if _, err := os.Stat(honkPath); os.IsNotExist(err) {
			if err := os.WriteFile(honkPath, honkSound, 0o600); err != nil {
				log.Printf("Failed to cache honk sound: %v", err)
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
		"rocket": "tada.wav",
		"honk":   "honk.wav",
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
				log.Printf("Failed to play sound: %v", err)
			}
		}
	}()
}
