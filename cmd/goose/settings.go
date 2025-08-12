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
	EnableAudioCues bool `json:"enable_audio_cues"`
	EnableReminders bool `json:"enable_reminders"`
	HideStale       bool `json:"hide_stale"`
}

// getSettingsDir returns the configuration directory for settings.
func getSettingsDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "ready-to-review"), nil
}

// loadSettings loads settings from disk or returns defaults.
func (app *App) loadSettings() {
	settingsDir, err := getSettingsDir()
	if err != nil {
		log.Printf("Failed to get settings directory: %v", err)
		// Use defaults
		app.enableAudioCues = true
		app.enableReminders = true
		app.hideStaleIncoming = true
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
		app.enableReminders = true
		app.hideStaleIncoming = true
		return
	}

	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		log.Printf("Failed to parse settings: %v", err)
		// Use defaults
		app.enableAudioCues = true
		app.enableReminders = true
		app.hideStaleIncoming = true
		return
	}

	app.enableAudioCues = settings.EnableAudioCues
	app.enableReminders = settings.EnableReminders
	app.hideStaleIncoming = settings.HideStale
	log.Printf("Loaded settings: audio_cues=%v, reminders=%v, hide_stale=%v",
		app.enableAudioCues, app.enableReminders, app.hideStaleIncoming)
}

// saveSettings saves current settings to disk.
func (app *App) saveSettings() {
	settingsDir, err := getSettingsDir()
	if err != nil {
		log.Printf("Failed to get settings directory: %v", err)
		return
	}

	app.mu.RLock()
	settings := Settings{
		EnableAudioCues: app.enableAudioCues,
		EnableReminders: app.enableReminders,
		HideStale:       app.hideStaleIncoming,
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

	log.Printf("Saved settings: audio_cues=%v, reminders=%v, hide_stale=%v",
		settings.EnableAudioCues, settings.EnableReminders, settings.HideStale)
}
