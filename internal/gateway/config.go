package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// GatewayConfig is the top-level configuration for klein-claw.
type GatewayConfig struct {
	AgentAddr      string          `json:"agent_addr"`      // Connect server address, e.g., "http://localhost:50051"
	WorkingDir     string          `json:"working_dir"`     // Agent working directory
	DefaultSkill   string          `json:"default_skill"`   // Default skill (default: "claw")
	DefaultModel   string          `json:"default_model"`   // LLM model
	MaxIterations  int             `json:"max_iterations"`  // ReAct loop cap
	SessionTimeout string          `json:"session_timeout"` // Inactivity timeout for sessions (Go duration, default: "30m")
	SessionsDir string        `json:"sessions_dir"` // Directory for per-session persistence files (default: ~/.klein/claw/sessions/)
	Discord     DiscordConfig `json:"discord"`
	Memory         MemoryConfig    `json:"memory"`
	Heartbeat      HeartbeatConfig `json:"heartbeat"`
}

// DiscordConfig holds Discord bot configuration.
type DiscordConfig struct {
	Token             string   `json:"token"`
	AllowedGuildIDs   []string `json:"allowed_guild_ids"`
	AllowedChannelIDs []string `json:"allowed_channel_ids"`
	AllowedUserIDs    []string `json:"allowed_user_ids"`
	MentionOnly       bool     `json:"mention_only"` // In guilds, only respond when @mentioned
}

// LoadGatewayConfig loads configuration from a JSON file.
func LoadGatewayConfig(path string) (*GatewayConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read gateway config %s: %w", path, err)
	}

	cfg := DefaultGatewayConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse gateway config: %w", err)
	}

	// Expand $HOME in memory base_dir
	if cfg.Memory.BaseDir == "" {
		home, _ := os.UserHomeDir()
		cfg.Memory.BaseDir = filepath.Join(home, ".klein", "claw", "memory")
	}

	// Expand $HOME in sessions_dir
	if cfg.SessionsDir == "" {
		home, _ := os.UserHomeDir()
		cfg.SessionsDir = filepath.Join(home, ".klein", "claw", "sessions")
	}

	return cfg, nil
}

// DefaultGatewayConfig returns sensible defaults.
func DefaultGatewayConfig() *GatewayConfig {
	home, _ := os.UserHomeDir()
	return &GatewayConfig{
		AgentAddr:      "http://localhost:50051",
		DefaultSkill:   "claw",
		DefaultModel:   "claude-sonnet-4-5-20250929",
		MaxIterations:  15,
		SessionTimeout: "30m",
		SessionsDir: filepath.Join(home, ".klein", "claw", "sessions"),
		Memory: MemoryConfig{
			BaseDir:  filepath.Join(home, ".klein", "claw", "memory"),
			MaxNotes: 30,
		},
		Heartbeat: HeartbeatConfig{
			Enabled:  false,
			Interval: "24h",
		},
	}
}

// DefaultConfigPath returns the default config file path.
func DefaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".klein", "claw", "config.json")
}
