package main

import (
	"log/slog"

	"github.com/codeGROOVE-dev/goose/pkg/appsettings"
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

	manager := appsettings.NewManager("reviewGOOSE")

	var settings Settings
	found, err := manager.Load(&settings)
	if err != nil {
		slog.Error("Failed to load settings", "error", err)
		return
	}

	if !found {
		slog.Debug("No settings file found, using defaults")
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
	app.mu.RLock()
	settings := Settings{
		EnableAudioCues:   app.enableAudioCues,
		HideStale:         app.hideStaleIncoming,
		EnableAutoBrowser: app.enableAutoBrowser,
		HiddenOrgs:        app.hiddenOrgs,
	}
	app.mu.RUnlock()

	manager := appsettings.NewManager("reviewGOOSE")
	if err := manager.Save(&settings); err != nil {
		slog.Error("Failed to save settings", "error", err)
		return
	}

	slog.Info("Saved settings",
		"audio_cues", settings.EnableAudioCues,
		"hide_stale", settings.HideStale,
		"auto_browser", settings.EnableAutoBrowser,
		"hidden_orgs", len(settings.HiddenOrgs))
}
