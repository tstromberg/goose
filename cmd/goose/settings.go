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

// settingsDir returns the configuration directory for settings.
func settingsDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "review-goose"), nil
}

// loadSettings loads settings from disk or returns defaults.
func (app *App) loadSettings() {
	settingsDir, err := settingsDir()
	if err != nil {
		slog.Error("Failed to get settings directory", "error", err)
		// Use defaults
		app.enableAudioCues = true
		app.hideStaleIncoming = true
		app.enableAutoBrowser = false
		app.hiddenOrgs = make(map[string]bool)
		return
	}

	settingsPath := filepath.Join(settingsDir, "settings.json")

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Debug("Failed to read settings", "error", err)
		}
		// Use defaults
		app.enableAudioCues = true
		app.hideStaleIncoming = true
		app.enableAutoBrowser = false
		app.hiddenOrgs = make(map[string]bool)
		return
	}

	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		slog.Error("Failed to parse settings", "error", err)
		// Use defaults
		app.enableAudioCues = true
		app.hideStaleIncoming = true
		app.enableAutoBrowser = false
		app.hiddenOrgs = make(map[string]bool)
		return
	}

	app.enableAudioCues = settings.EnableAudioCues
	app.hideStaleIncoming = settings.HideStale
	app.enableAutoBrowser = settings.EnableAutoBrowser
	if settings.HiddenOrgs != nil {
		app.hiddenOrgs = settings.HiddenOrgs
	} else {
		app.hiddenOrgs = make(map[string]bool)
	}
	slog.Info("Loaded settings",
		"audio_cues", app.enableAudioCues,
		"hide_stale", app.hideStaleIncoming,
		"auto_browser", app.enableAutoBrowser,
		"hidden_orgs", len(app.hiddenOrgs))
}

// saveSettings saves current settings to disk.
func (app *App) saveSettings() {
	settingsDir, err := settingsDir()
	if err != nil {
		slog.Error("Failed to get settings directory", "error", err)
		return
	}

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
