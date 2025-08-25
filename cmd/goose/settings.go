// Package main - settings.go provides persistent settings storage.
package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

// Settings represents persistent user settings.
type Settings struct {
	EnableAudioCues   bool `json:"enable_audio_cues"`
	HideStale         bool `json:"hide_stale"`
	EnableAutoBrowser bool `json:"enable_auto_browser"`
}

// settingsDir returns the configuration directory for settings.
func settingsDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "ready-to-review"), nil
}

// loadSettings loads settings from disk or returns defaults.
func (app *App) loadSettings() {
	settingsDir, err := settingsDir()
	if err != nil {
		log.Printf("Failed to get settings directory: %v", err)
		// Use defaults
		app.enableAudioCues = true
		app.hideStaleIncoming = true
		app.enableAutoBrowser = false
		return
	}

	settingsPath := filepath.Join(settingsDir, "settings.json")

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Failed to read settings: %v", err)
		}
		// Use defaults
		app.enableAudioCues = true
		app.hideStaleIncoming = true
		app.enableAutoBrowser = false
		return
	}

	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		log.Printf("Failed to parse settings: %v", err)
		// Use defaults
		app.enableAudioCues = true
		app.hideStaleIncoming = true
		app.enableAutoBrowser = false
		return
	}

	app.enableAudioCues = settings.EnableAudioCues
	app.hideStaleIncoming = settings.HideStale
	app.enableAutoBrowser = settings.EnableAutoBrowser
	log.Printf("Loaded settings: audio_cues=%v, hide_stale=%v, auto_browser=%v",
		app.enableAudioCues, app.hideStaleIncoming, app.enableAutoBrowser)
}

// saveSettings saves current settings to disk.
func (app *App) saveSettings() {
	settingsDir, err := settingsDir()
	if err != nil {
		log.Printf("Failed to get settings directory: %v", err)
		return
	}

	app.mu.RLock()
	settings := Settings{
		EnableAudioCues:   app.enableAudioCues,
		HideStale:         app.hideStaleIncoming,
		EnableAutoBrowser: app.enableAutoBrowser,
	}
	app.mu.RUnlock()

	// Ensure directory exists
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		log.Printf("Failed to create settings directory: %v", err)
		return
	}

	settingsPath := filepath.Join(settingsDir, "settings.json")

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		log.Printf("Failed to marshal settings: %v", err)
		return
	}

	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		log.Printf("Failed to save settings: %v", err)
		return
	}

	log.Printf("Saved settings: audio_cues=%v, hide_stale=%v, auto_browser=%v",
		settings.EnableAudioCues, settings.HideStale, settings.EnableAutoBrowser)
}
