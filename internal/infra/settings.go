package infra

import (
	"fmt"
	"os"
	"path/filepath"
)

// FileSettingsRepository represents file-persisted settings repository
type FileSettingsRepository struct {
	configPath string // Specific path (empty means search for file)
}

// InMemorySettingsRepository represents in-memory-only settings repository
type InMemorySettingsRepository struct {
	data []byte
}

// NewFileSettingsRepository creates a new file-based settings repository
func NewFileSettingsRepository(configPath string) *FileSettingsRepository {
	return &FileSettingsRepository{
		configPath: configPath,
	}
}

// NewInMemorySettingsRepository creates a new in-memory settings repository
func NewInMemorySettingsRepository() *InMemorySettingsRepository {
	return &InMemorySettingsRepository{}
}

// FileSettingsRepository methods
func (fr *FileSettingsRepository) Load() ([]byte, error) {
	configPath := fr.configPath
	if configPath == "" {
		// Search for settings file
		foundPath, err := fr.FindSettingsFile()
		if err != nil {
			return nil, err
		}
		if foundPath == "" {
			return nil, fmt.Errorf("no settings file found")
		}
		configPath = foundPath
	}

	// Check if file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("settings file does not exist: %s", configPath)
	}

	// Read file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read settings file: %w", err)
	}

	return data, nil
}

func (fr *FileSettingsRepository) Save(data []byte) error {
	configPath := fr.configPath
	if configPath == "" {
		// Try to find existing settings file first
		foundPath, _ := fr.FindSettingsFile()
		if foundPath != "" {
			configPath = foundPath
		} else {
			// No existing file, save to .agents in current directory
			configPath = filepath.Join(".agents", "settings.json")
		}
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Write to file
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write settings file: %w", err)
	}

	return nil
}

func (fr *FileSettingsRepository) FindSettingsFile() (string, error) {
	// Check .agents in current directory
	currentDirPath := filepath.Join(".agents", "settings.json")
	if _, err := os.Stat(currentDirPath); err == nil {
		return currentDirPath, nil
	}

	// Check $HOME/.klein
	homeDir, err := os.UserHomeDir()
	if err == nil {
		homeDirPath := filepath.Join(homeDir, ".klein", "settings.json")
		if _, err := os.Stat(homeDirPath); err == nil {
			return homeDirPath, nil
		}
	}

	// No settings file found
	return "", nil
}

// InMemorySettingsRepository methods
func (mr *InMemorySettingsRepository) Load() ([]byte, error) {
	if mr.data == nil {
		return nil, fmt.Errorf("no data stored in memory repository")
	}
	return mr.data, nil
}

func (mr *InMemorySettingsRepository) Save(data []byte) error {
	mr.data = make([]byte, len(data))
	copy(mr.data, data)
	return nil
}

func (mr *InMemorySettingsRepository) FindSettingsFile() (string, error) {
	// In-memory repository doesn't have files
	return "", nil
}
