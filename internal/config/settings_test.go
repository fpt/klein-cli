package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testBackend is a sample backend value used across the load tests.
const testBackend = "anthropic"

func TestCreateDefaultSettingsFile(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "klein-settings-test")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Test creating settings file at a specific path
	settingsPath := filepath.Join(tempDir, ".klein", "settings.json")
	settings, err := createSettingsFileAtPath(settingsPath)
	if err != nil {
		t.Fatalf("createSettingsFileAtPath failed: %v", err)
	}

	// Verify settings returned are valid
	if settings == nil {
		t.Fatal("Expected non-nil settings")
	}

	if settings.LLM.Backend != "ollama" {
		t.Errorf("Expected backend 'ollama', got '%s'", settings.LLM.Backend)
	}

	// Verify file was created
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		t.Fatal("Settings file was not created")
	}

	// Verify file contents can be loaded
	loadedSettings, err := LoadSettings(settingsPath)
	if err != nil {
		t.Fatalf("Failed to load created settings file: %v", err)
	}

	if loadedSettings.LLM.Backend != settings.LLM.Backend {
		t.Errorf("Expected backend '%s', got '%s'", settings.LLM.Backend, loadedSettings.LLM.Backend)
	}
}

func TestLoadSettingsCreatesFileWhenNoneExists(t *testing.T) {
	// Temporarily override the home directory for testing
	originalHome := os.Getenv("HOME")
	tempDir, err := os.MkdirTemp("", "klein-home-test")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer func() {
		os.Setenv("HOME", originalHome)
		os.RemoveAll(tempDir)
	}()

	os.Setenv("HOME", tempDir)

	// Load settings when no file exists - should create default file
	settings, err := LoadSettings("")
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}

	// Verify settings are valid
	if settings == nil {
		t.Fatal("Expected non-nil settings")
	}

	// Verify file was created in the fake home directory
	expectedPath := filepath.Join(tempDir, ".klein", "settings.json")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Fatal("Settings file was not created in home directory")
	}
}

// TestLoadSettingsMalformedReportsError verifies a settings file that exists but
// contains invalid JSON surfaces an error instead of silently falling back to
// defaults — and is not overwritten in the process.
func TestLoadSettingsMalformedReportsError(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "settings.json")
	bad := []byte(`{"llm": {"backend": "` + testBackend + `",}}`) // trailing comma = invalid JSON
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	settings, err := LoadSettings(path)
	if err == nil {
		t.Fatal("expected an error for malformed settings, got nil")
	}
	if settings != nil {
		t.Fatalf("expected nil settings on error, got %+v", settings)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error should name the offending file; got %q", err)
	}

	// The malformed file must be left intact, not clobbered with defaults.
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	if string(after) != string(bad) {
		t.Errorf("settings file was modified; want it left intact.\n got: %s", after)
	}
}

// TestLoadSettingsMalformedFoundBySearch verifies the same reporting when the
// file is discovered via search (empty configPath), the common no-flag path.
func TestLoadSettingsMalformedFoundBySearch(t *testing.T) {
	// t.Setenv marks the test as non-parallel; it can't run alongside others.
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	dir := filepath.Join(tempDir, ".klein")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("not json at all"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := LoadSettings(""); err == nil {
		t.Fatal("expected an error for malformed discovered settings, got nil")
	}
}

// TestLoadSettingsValidLoads confirms a well-formed file still loads its values.
func TestLoadSettingsValidLoads(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "settings.json")
	content := []byte(`{"llm": {"backend": "` + testBackend + `", "model": "claude-x"}}`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	settings, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if settings.LLM.Backend != testBackend || settings.LLM.Model != "claude-x" {
		t.Fatalf("loaded values = %q/%q, want %s/claude-x", settings.LLM.Backend, settings.LLM.Model, testBackend)
	}
}
