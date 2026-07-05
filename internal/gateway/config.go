package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// GatewayConfig is the configuration for the klein claw gateway. On disk it is
// the "claw" section of settings.json (see internal/config.Settings.Claw); this
// package owns its schema. Path-shaped state (sessions, memory, schedule store)
// is NOT configured here — it is derived from the shared base dir so the CLI and
// the gateway agree on locations. See ParseClawConfig.
type GatewayConfig struct {
	AgentAddr      string        `json:"agent_addr"`      // Connect server address; empty = start an embedded in-process server
	WorkingDir     string        `json:"working_dir"`     // Agent working directory
	DefaultSkill   string        `json:"default_skill"`   // Default skill (default: "claw")
	SessionTimeout string        `json:"session_timeout"` // Inactivity timeout for sessions (Go duration, default: "30m")
	Discord        DiscordConfig `json:"discord"`
	Memory         MemoryConfig  `json:"memory"` // Only MaxNotes is read from JSON; BaseDir is derived from the shared base dir.

	// Schedules is the multi-job scheduler. Each entry runs on its own
	// goroutine, fires on a cron expression evaluated in its timezone, and can
	// opt into Silent mode (run the prompt but don't echo the response to a
	// chat channel).
	//
	// A leftover "heartbeat" key from the retired single-job block is ignored
	// (unknown JSON fields don't error).
	Schedules []ScheduleConfig `json:"schedules,omitempty"`

	// Derived from the shared base dir (set by ParseClawConfig, not from the
	// claw JSON block). SessionsDir and SchedulesFile are used directly; the
	// memory directory is written into Memory.BaseDir.
	BaseDir       string `json:"-"`
	SessionsDir   string `json:"-"`
	SchedulesFile string `json:"-"`
}

// DiscordConfig holds Discord bot configuration.
type DiscordConfig struct {
	Token             string   `json:"token"`
	AllowedGuildIDs   []string `json:"allowed_guild_ids"`
	AllowedChannelIDs []string `json:"allowed_channel_ids"`
	AllowedUserIDs    []string `json:"allowed_user_ids"`
	MentionOnly       bool     `json:"mention_only"` // In guilds, only respond when @mentioned
}

// ParseClawConfig decodes the "claw" section of settings.json (raw may be nil or
// empty for an all-defaults gateway) and derives all path-shaped state from the
// shared base dir: <base>/sessions, <base>/schedules.json, <base>/memory. This
// keeps the agent's Schedule* tools and the scheduler pointed at the same file,
// and the [SESSION LOG] path the gateway injects at the same directory the agent
// server persists to.
func ParseClawConfig(raw json.RawMessage, baseDir string) (*GatewayConfig, error) {
	cfg := DefaultGatewayConfig()
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse claw config: %w", err)
		}
	}

	cfg.WorkingDir = os.ExpandEnv(cfg.WorkingDir)
	cfg.applyBaseDir(baseDir)
	return cfg, nil
}

// applyBaseDir fills the derived path fields from the shared base dir.
func (cfg *GatewayConfig) applyBaseDir(baseDir string) {
	cfg.BaseDir = baseDir
	cfg.SessionsDir = filepath.Join(baseDir, "sessions")
	cfg.SchedulesFile = filepath.Join(baseDir, "schedules.json")
	cfg.Memory.BaseDir = filepath.Join(baseDir, "memory")
	if cfg.Memory.MaxNotes <= 0 {
		cfg.Memory.MaxNotes = 30
	}
}

// DefaultGatewayConfig returns sensible defaults. AgentAddr is empty by default,
// which selects the embedded in-process agent server.
func DefaultGatewayConfig() *GatewayConfig {
	return &GatewayConfig{
		DefaultSkill:   "claw",
		SessionTimeout: "30m",
		Memory:         MemoryConfig{MaxNotes: 30},
	}
}
