// Package appsettings provides functionality for loading and saving application settings.
package appsettings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Manager handles loading and saving settings to disk.
type Manager struct {
	appName string
}

// NewManager creates a new settings manager for the given application name.
func NewManager(appName string) *Manager {
	return &Manager{appName: appName}
}

// Path returns the path to the settings file.
func (m *Manager) Path() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("get user config dir: %w", err)
	}
	return filepath.Join(configDir, m.appName, "settings.json"), nil
}

// Load loads settings from disk into the provided struct.
// Returns false if the file doesn't exist (not an error).
func (m *Manager) Load(settings any) (bool, error) {
	path, err := m.Path()
	if err != nil {
		return false, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // File doesn't exist, not an error
		}
		return false, fmt.Errorf("read settings file: %w", err)
	}

	if err := json.Unmarshal(data, settings); err != nil {
		return false, fmt.Errorf("parse settings: %w", err)
	}

	return true, nil
}

// Save saves settings to disk.
func (m *Manager) Save(settings any) error {
	path, err := m.Path()
	if err != nil {
		return err
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write settings file: %w", err)
	}

	return nil
}
