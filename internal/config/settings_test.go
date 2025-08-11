package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
