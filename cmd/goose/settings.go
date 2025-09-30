// Package main - settings.go provides persistent settings storage.
package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
)

// Settings represents persistent user settings.
type Settings struct {
	HiddenOrgs        map[string]bool `json:"hidden_orgs"`
	EnableAudioCues   bool            `json:"enable_audio_cues"`
	HideStale         bool            `json:"hide_stale"`
	EnableAutoBrowser bool            `json:"enable_auto_browser"`
}

// loadSettings loads settings from disk or returns defaults.
func (app *App) loadSettings() {
	// Set defaults first
	app.enableAudioCues = true
	app.hideStaleIncoming = true
	app.enableAutoBrowser = true
	app.hiddenOrgs = make(map[string]bool)

	configDir, err := os.UserConfigDir()
	if err != nil {
		slog.Error("Failed to get settings directory", "error", err)
		return
	}

	settingsPath := filepath.Join(configDir, "review-goose", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Debug("Failed to read settings", "error", err)
		}
		return
	}

	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		slog.Error("Failed to parse settings", "error", err)
		return
	}

	// Override defaults with loaded values
	app.enableAudioCues = settings.EnableAudioCues
	app.hideStaleIncoming = settings.HideStale
	app.enableAutoBrowser = settings.EnableAutoBrowser
	if settings.HiddenOrgs != nil {
		app.hiddenOrgs = settings.HiddenOrgs
	}

	slog.Info("Loaded settings",
		"audio_cues", app.enableAudioCues,
		"hide_stale", app.hideStaleIncoming,
		"auto_browser", app.enableAutoBrowser,
		"hidden_orgs", len(app.hiddenOrgs))
}

// saveSettings saves current settings to disk.
func (app *App) saveSettings() {
	configDir, err := os.UserConfigDir()
	if err != nil {
		slog.Error("Failed to get settings directory", "error", err)
		return
	}
	settingsDir := filepath.Join(configDir, "review-goose")

	app.mu.RLock()
	settings := Settings{
		EnableAudioCues:   app.enableAudioCues,
		HideStale:         app.hideStaleIncoming,
		EnableAutoBrowser: app.enableAutoBrowser,
		HiddenOrgs:        app.hiddenOrgs,
	}
	app.mu.RUnlock()

	// Ensure directory exists
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		slog.Error("Failed to create settings directory", "error", err)
		return
	}

	settingsPath := filepath.Join(settingsDir, "settings.json")

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		slog.Error("Failed to marshal settings", "error", err)
		return
	}

	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		slog.Error("Failed to save settings", "error", err)
		return
	}

	slog.Info("Saved settings",
		"audio_cues", settings.EnableAudioCues,
		"hide_stale", settings.HideStale,
		"auto_browser", settings.EnableAutoBrowser,
		"hidden_orgs", len(settings.HiddenOrgs))
}
