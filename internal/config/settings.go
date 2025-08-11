package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/internal/repository"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// Default maximum iterations for agents
const DefaultAgentMaxIterations = 30

// Settings represents the main application settings
type Settings struct {
	LLM   LLMSettings   `json:"llm"`
	MCP   MCPSettings   `json:"mcp"`
	Agent AgentSettings `json:"agent"`
	Bash  BashSettings  `json:"bash,omitempty"`

	// Repository for persistence (nil for in-memory only)
	settingsRepository repository.SettingsRepository `json:"-"`
}

// LLMSettings contains LLM client configuration
type LLMSettings struct {
	Backend   string `json:"backend"`              // "ollama", "anthropic", "openai", or "gemini"
	Model     string `json:"model"`                // model name
	BaseURL   string `json:"base_url,omitempty"`   // for ollama or openai (Azure)
	Thinking  bool   `json:"thinking,omitempty"`   // enable thinking mode
	MaxTokens int    `json:"max_tokens,omitempty"` // maximum tokens for model responses (0 = use model default)
}

// MCPSettings contains MCP server configuration
type MCPSettings struct {
	Servers []domain.MCPServerConfig `json:"servers,omitempty"`
}

// AgentSettings contains agent behavior configuration
type AgentSettings struct {
	MaxIterations int    `json:"max_iterations"`
	LogLevel      string `json:"log_level"`
}

// BashSettings contains bash tool configuration
type BashSettings struct {
	WhitelistedCommands []string `json:"whitelisted_commands,omitempty"` // Commands that don't require approval
}

// NewSettings creates new settings with in-memory repository
func NewSettings() *Settings {
	return NewSettingsWithRepository(infra.NewInMemorySettingsRepository())
}

// NewSettingsWithRepository creates new settings with injected repository
func NewSettingsWithRepository(settingsRepository repository.SettingsRepository) *Settings {
	settings := GetDefaultSettings()
	settings.settingsRepository = settingsRepository
	return settings
}

// NewSettingsWithPath creates new settings with file-based repository
func NewSettingsWithPath(configPath string) *Settings {
	repo := infra.NewFileSettingsRepository(configPath)
	return NewSettingsWithRepository(repo)
}

// Load loads settings from the repository
func (s *Settings) Load() error {
	if s.settingsRepository == nil {
		return fmt.Errorf("no settings repository configured")
	}

	data, err := s.settingsRepository.Load()
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}

	if err := json.Unmarshal(data, s); err != nil {
		return fmt.Errorf("failed to parse settings: %w", err)
	}

	// Apply defaults for missing fields
	applyDefaults(s)
	return nil
}

// Save saves settings to the repository
func (s *Settings) Save() error {
	if s.settingsRepository == nil {
		return fmt.Errorf("no settings repository configured")
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	return s.settingsRepository.Save(data)
}

// LoadSettings loads application settings from a JSON file
func LoadSettings(configPath string) (*Settings, error) {
	// Create settings with file repository
	settings := NewSettingsWithPath(configPath)

	// If config path is empty, search for existing settings file
	if configPath == "" {
		foundPath, _ := settings.settingsRepository.FindSettingsFile()
		if foundPath == "" {
			// No settings file found, create default one and return defaults
			return createDefaultSettingsFile()
		}
	}

	// Try to load settings
	err := settings.Load()
	if err != nil {
		// If file doesn't exist and a specific path was provided, create it
		if configPath != "" {
			createdSettings, _ := createSettingsFileAtPath(configPath)
			return createdSettings, nil
		}
		// Otherwise return defaults
		return GetDefaultSettings(), nil
	}

	return settings, nil
}

// SaveSettings saves application settings to a JSON file
func SaveSettings(configPath string, settings *Settings) error {
	if settings.settingsRepository != nil {
		// Use the injected repository
		return settings.Save()
	}

	// Fallback to direct file operations (for backward compatibility)
	if configPath == "" {
		// Try to find existing settings file first
		configPath = findSettingsFile()
		if configPath == "" {
			// No existing file, save to .agents in current directory
			configPath = filepath.Join(".agents", "settings.json")
		}
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Marshal to JSON with pretty formatting
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	// Write to file
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write settings file: %w", err)
	}

	return nil
}

// GetDefaultSettings returns default application settings
func GetDefaultSettings() *Settings {
	return &Settings{
		LLM: LLMSettings{
			Backend:   "ollama",
			Model:     "gpt-oss:latest",
			BaseURL:   "http://localhost:11434",
			Thinking:  true,
			MaxTokens: 0, // 0 = use model-specific defaults
		},
		Agent: AgentSettings{
			MaxIterations: DefaultAgentMaxIterations,
			LogLevel:      "info",
		},
		Bash: BashSettings{
			WhitelistedCommands: []string{
				"go build",
				"go test",
				"go run",
				"go mod tidy",
				"go fmt",
				"go vet",
				"git status",
				"git log",
				"git diff",
				"ls",
				"pwd",
				"cat",
				"head",
				"tail",
				"grep",
				"find",
				"echo",
				"which",
				"make",
				"npm install",
				"npm run",
				"npm test",
			},
		},
	}
}

// GetDefaultLLMSettingsForBackend returns default LLM settings for a specific backend
func GetDefaultLLMSettingsForBackend(backend string) LLMSettings {
	switch backend {
	case "ollama":
		return LLMSettings{
			Backend:   "ollama",
			Model:     "gpt-oss:latest",
			BaseURL:   "http://localhost:11434",
			Thinking:  true,
			MaxTokens: 0,
		}
	case "anthropic", "claude":
		return LLMSettings{
			Backend:   "anthropic",
			Model:     "claude-sonnet-4-5-20250929",
			BaseURL:   "",
			Thinking:  true,
			MaxTokens: 0,
		}
	case "openai":
		return LLMSettings{
			Backend:   "openai",
			Model:     "gpt-5-mini",
			BaseURL:   "",
			Thinking:  true,
			MaxTokens: 0,
		}
	case "gemini":
		return LLMSettings{
			Backend:   "gemini",
			Model:     "gemini-2.5-flash-lite",
			BaseURL:   "",
			Thinking:  false, // Gemini doesn't support thinking in our implementation
			MaxTokens: 0,
		}
	default:
		// Default to ollama settings for unknown backends
		return GetDefaultLLMSettingsForBackend("ollama")
	}
}

// applyDefaults fills in missing fields with default values
func applyDefaults(settings *Settings) {
	defaults := GetDefaultSettings()

	// Apply LLM defaults
	if settings.LLM.Backend == "" {
		settings.LLM.Backend = defaults.LLM.Backend
	}
	if settings.LLM.Model == "" {
		settings.LLM.Model = defaults.LLM.Model
	}
	if settings.LLM.BaseURL == "" && settings.LLM.Backend == "ollama" {
		settings.LLM.BaseURL = defaults.LLM.BaseURL
	}

	// Apply MCP defaults (no config_path needed anymore)

	// Apply Agent defaults
	if settings.Agent.MaxIterations == 0 {
		settings.Agent.MaxIterations = defaults.Agent.MaxIterations
	}
	if settings.Agent.LogLevel == "" {
		settings.Agent.LogLevel = defaults.Agent.LogLevel
	}
}

// ValidateSettings validates the settings configuration
func ValidateSettings(settings *Settings) error {
	// Validate LLM settings
	if settings.LLM.Backend != "ollama" && settings.LLM.Backend != "anthropic" && settings.LLM.Backend != "openai" && settings.LLM.Backend != "gemini" {
		return fmt.Errorf("unsupported LLM backend: %s (must be 'ollama', 'anthropic', 'openai', or 'gemini')", settings.LLM.Backend)
	}

	if settings.LLM.Model == "" {
		return fmt.Errorf("LLM model is required")
	}

	if settings.LLM.Backend == "anthropic" {
		// Check environment variable for API key
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			return fmt.Errorf("Anthropic API key is required (set ANTHROPIC_API_KEY environment variable)")
		}
	}

	if settings.LLM.Backend == "openai" {
		// Check environment variable for API key
		if os.Getenv("OPENAI_API_KEY") == "" {
			return fmt.Errorf("OpenAI API key is required (set OPENAI_API_KEY environment variable)")
		}
	}

	if settings.LLM.Backend == "gemini" {
		// Check environment variable for API key
		if os.Getenv("GEMINI_API_KEY") == "" {
			return fmt.Errorf("Gemini API key is required (set GEMINI_API_KEY environment variable)")
		}
	}

	// Validate Agent settings
	if settings.Agent.MaxIterations <= 0 {
		return fmt.Errorf("max_iterations must be positive")
	}

	// Validate MCP server configurations
	for _, serverConfig := range settings.MCP.Servers {
		if err := ValidateMCPServerConfig(serverConfig); err != nil {
			return fmt.Errorf("invalid MCP server configuration for %s: %w", serverConfig.Name, err)
		}
	}

	return nil
}

// findSettingsFile searches for settings.json in order of preference:
// 1. .agents/settings.json in current directory
// 2. $HOME/.klein/settings.json
// Returns empty string if none found
func findSettingsFile() string {
	// Check .agents in current directory
	currentDirPath := filepath.Join(".agents", "settings.json")
	if _, err := os.Stat(currentDirPath); err == nil {
		return currentDirPath
	}

	// Check $HOME/.klein
	homeDir, err := os.UserHomeDir()
	if err == nil {
		homeDirPath := filepath.Join(homeDir, ".klein", "settings.json")
		if _, err := os.Stat(homeDirPath); err == nil {
			return homeDirPath
		}
	}

	// No settings file found
	return ""
}

// ValidateMCPServerConfig validates an MCP server configuration
func ValidateMCPServerConfig(config domain.MCPServerConfig) error {
	if config.Name == "" {
		return fmt.Errorf("server name is required")
	}

	switch config.Type {
	case domain.MCPServerTypeStdio:
		if config.Command == "" {
			return fmt.Errorf("command is required for stdio servers")
		}
	case domain.MCPServerTypeSSE:
		if config.URL == "" {
			return fmt.Errorf("URL is required for HTTP/SSE servers")
		}
	default:
		return fmt.Errorf("unsupported server type: %s", config.Type)
	}

	return nil
}

// createDefaultSettingsFile creates a default settings.json file in ~/.klein/
func createDefaultSettingsFile() (*Settings, error) {
	// Determine where to create the file (prefer home directory)
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return GetDefaultSettings(), nil // Fall back to defaults without file creation
	}

	settingsPath := filepath.Join(homeDir, ".klein", "settings.json")
	return createSettingsFileAtPath(settingsPath)
}

// createSettingsFileAtPath creates a default settings file at the specified path
func createSettingsFileAtPath(settingsPath string) (*Settings, error) {
	// Create settings with file repository
	settings := NewSettingsWithPath(settingsPath)

	// Save default settings to file
	if err := settings.Save(); err != nil {
		// Return defaults without repository if saving fails
		return GetDefaultSettings(), nil
	}

	// Log success message
	pkgLogger.NewComponentLogger("settings").InfoWithIntention(pkgLogger.IntentionConfig, "Created default settings file", "path", settingsPath)
	pkgLogger.NewComponentLogger("settings").InfoWithIntention(pkgLogger.IntentionStatus, "You can edit this file to customize your configuration")

	return settings, nil
}
