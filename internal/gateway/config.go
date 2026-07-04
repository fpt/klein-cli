package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// GatewayConfig is the top-level configuration for klein-claw.
type GatewayConfig struct {
	AgentAddr      string        `json:"agent_addr"`      // Connect server address, e.g., "http://localhost:50051"
	WorkingDir     string        `json:"working_dir"`     // Agent working directory
	DefaultSkill   string        `json:"default_skill"`   // Default skill (default: "claw")
	SessionTimeout string        `json:"session_timeout"` // Inactivity timeout for sessions (Go duration, default: "30m")
	SessionsDir    string        `json:"sessions_dir"`    // Directory for per-session persistence files (default: ~/.klein/claw/sessions/)
	SchedulesFile  string        `json:"schedules_file"`  // Dynamic schedule store the agent's Schedule* tools write and the scheduler watches (default: ~/.klein/claw/schedules.json)
	Discord        DiscordConfig `json:"discord"`
	Memory         MemoryConfig  `json:"memory"`

	// Schedules is the multi-job scheduler. Each entry runs on its own
	// goroutine, fires on a cron expression evaluated in its timezone, and can
	// opt into Silent mode (run the prompt but don't echo the response to a
	// chat channel). Right for time-series data collection — e.g. an hourly
	// ResearcherFetch that updates the local store with no Discord chatter,
	// plus a daily digest that DOES post.
	//
	// The legacy single-job "heartbeat" block is retired; a leftover heartbeat
	// key in config.json is ignored (unknown JSON fields don't error).
	Schedules []ScheduleConfig `json:"schedules,omitempty"`
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

	// Expand environment variables (e.g. a literal "$HOME/.klein/...") in path
	// fields so they aren't used verbatim and create a stray "$HOME" directory.
	cfg.WorkingDir = os.ExpandEnv(cfg.WorkingDir)
	cfg.SessionsDir = os.ExpandEnv(cfg.SessionsDir)
	cfg.SchedulesFile = os.ExpandEnv(cfg.SchedulesFile)
	cfg.Memory.BaseDir = os.ExpandEnv(cfg.Memory.BaseDir)

	// Fall back to defaults when unset.
	if cfg.Memory.BaseDir == "" {
		home, _ := os.UserHomeDir()
		cfg.Memory.BaseDir = filepath.Join(home, ".klein", "claw", "memory")
	}
	if cfg.SessionsDir == "" {
		home, _ := os.UserHomeDir()
		cfg.SessionsDir = filepath.Join(home, ".klein", "claw", "sessions")
	}
	if cfg.SchedulesFile == "" {
		home, _ := os.UserHomeDir()
		cfg.SchedulesFile = filepath.Join(home, ".klein", "claw", "schedules.json")
	}

	return cfg, nil
}

// DefaultGatewayConfig returns sensible defaults.
func DefaultGatewayConfig() *GatewayConfig {
	home, _ := os.UserHomeDir()
	return &GatewayConfig{
		AgentAddr:      "http://localhost:50051",
		DefaultSkill:   "claw",
		SessionTimeout: "30m",
		SessionsDir:    filepath.Join(home, ".klein", "claw", "sessions"),
		SchedulesFile:  filepath.Join(home, ".klein", "claw", "schedules.json"),
		Memory: MemoryConfig{
			BaseDir:  filepath.Join(home, ".klein", "claw", "memory"),
			MaxNotes: 30,
		},
	}
}

// DefaultConfigPath returns the default config file path.
func DefaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".klein", "claw", "config.json")
}
