package appsettings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testSettings struct {
	Name    string          `json:"name"`
	Enabled bool            `json:"enabled"`
	Count   int             `json:"count"`
	Tags    map[string]bool `json:"tags"`
}

func TestNewManager(t *testing.T) {
	m := NewManager("testapp")
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	if m.appName != "testapp" {
		t.Errorf("appName = %q, want %q", m.appName, "testapp")
	}
}

func TestPath(t *testing.T) {
	m := NewManager("testapp")
	path, err := m.Path()
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}

	if !filepath.IsAbs(path) {
		t.Errorf("Path is not absolute: %q", path)
	}

	if !strings.HasPrefix(path, os.Getenv("HOME")) && !strings.HasPrefix(path, os.Getenv("APPDATA")) {
		t.Logf("Warning: Path may not be in user directory: %q", path)
	}

	expectedSuffix := filepath.Join("testapp", "settings.json")
	if !strings.HasSuffix(path, expectedSuffix) {
		t.Errorf("Path should end with %q, got %q", expectedSuffix, path)
	}
}

func TestSaveAndLoad(t *testing.T) {
	// Use temporary directory to avoid interfering with real config
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir) // Linux
	t.Setenv("HOME", tmpDir)            // macOS fallback
	t.Setenv("APPDATA", tmpDir)         // Windows

	m := NewManager("testapp")

	// Create test settings
	original := testSettings{
		Name:    "test",
		Enabled: true,
		Count:   42,
		Tags:    map[string]bool{"go": true, "rust": false},
	}

	// Save settings
	err := m.Save(&original)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify file exists
	path, err := m.Path()
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("Settings file was not created")
	}

	// Load settings
	var loaded testSettings
	found, err := m.Load(&loaded)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !found {
		t.Fatal("Load() returned found=false, expected true")
	}

	// Verify loaded matches original
	if loaded.Name != original.Name {
		t.Errorf("Name = %q, want %q", loaded.Name, original.Name)
	}
	if loaded.Enabled != original.Enabled {
		t.Errorf("Enabled = %v, want %v", loaded.Enabled, original.Enabled)
	}
	if loaded.Count != original.Count {
		t.Errorf("Count = %d, want %d", loaded.Count, original.Count)
	}
	if len(loaded.Tags) != len(original.Tags) {
		t.Errorf("Tags length = %d, want %d", len(loaded.Tags), len(original.Tags))
	}
	for k, v := range original.Tags {
		if loaded.Tags[k] != v {
			t.Errorf("Tags[%q] = %v, want %v", k, loaded.Tags[k], v)
		}
	}
}

func TestLoad_FileNotExists(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("HOME", tmpDir)
	t.Setenv("APPDATA", tmpDir)

	m := NewManager("nonexistent")

	var settings testSettings
	found, err := m.Load(&settings)
	if err != nil {
		t.Errorf("Load() error = %v, want nil for nonexistent file", err)
	}
	if found {
		t.Error("Load() returned found=true for nonexistent file, want false")
	}
}

func TestLoad_CorruptedFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("HOME", tmpDir)
	t.Setenv("APPDATA", tmpDir)

	m := NewManager("corrupted")

	// Create corrupted settings file
	path, err := m.Path()
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	err = os.MkdirAll(filepath.Dir(path), 0o700)
	if err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}
	err = os.WriteFile(path, []byte("not valid json {{{"), 0o600)
	if err != nil {
		t.Fatalf("Failed to write corrupted file: %v", err)
	}

	var settings testSettings
	found, err := m.Load(&settings)
	if err == nil {
		t.Error("Load() should return error for corrupted file")
	}
	if found {
		t.Error("Load() returned found=true for corrupted file")
	}
}

func TestSave_CreateDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("HOME", tmpDir)
	t.Setenv("APPDATA", tmpDir)

	m := NewManager("testapp")

	settings := testSettings{Name: "test"}

	err := m.Save(&settings)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify directory was created
	path, err := m.Path()
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Error("Settings directory should have been created")
	}

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("Settings file should have been created")
	}
}

func TestSave_Overwrite(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("HOME", tmpDir)
	t.Setenv("APPDATA", tmpDir)

	m := NewManager("testapp")

	// Save first version
	first := testSettings{Name: "first", Count: 1}
	err := m.Save(&first)
	if err != nil {
		t.Fatalf("First Save() error = %v", err)
	}

	// Save second version (overwrite)
	second := testSettings{Name: "second", Count: 2}
	err = m.Save(&second)
	if err != nil {
		t.Fatalf("Second Save() error = %v", err)
	}

	// Load and verify it has the second version
	var loaded testSettings
	found, err := m.Load(&loaded)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !found {
		t.Fatal("Load() returned found=false")
	}

	if loaded.Name != "second" {
		t.Errorf("Name = %q, want %q (should be overwritten)", loaded.Name, "second")
	}
	if loaded.Count != 2 {
		t.Errorf("Count = %d, want %d (should be overwritten)", loaded.Count, 2)
	}
}

func TestSave_FilePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("HOME", tmpDir)
	t.Setenv("APPDATA", tmpDir)

	m := NewManager("testapp")

	settings := testSettings{Name: "test"}
	err := m.Save(&settings)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	path, err := m.Path()
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}

	// Check file permissions are restrictive (0o600 on Unix)
	mode := info.Mode()
	if mode.Perm() != 0o600 {
		t.Logf("Warning: File permissions are %o, expected 0o600", mode.Perm())
	}
}

func TestLoad_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("HOME", tmpDir)
	t.Setenv("APPDATA", tmpDir)

	m := NewManager("emptyfile")

	// Create empty settings file
	path, err := m.Path()
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	err = os.MkdirAll(filepath.Dir(path), 0o700)
	if err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}
	err = os.WriteFile(path, []byte(""), 0o600)
	if err != nil {
		t.Fatalf("Failed to write empty file: %v", err)
	}

	var settings testSettings
	found, err := m.Load(&settings)
	if err == nil {
		t.Error("Load() should return error for empty file")
	}
	if found {
		t.Error("Load() returned found=true for empty file")
	}
}
