package gateway

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fpt/klein-cli/internal/config"
)

// GatewayConfig is the configuration for the klein claw gateway. On disk it is
// the "claw" section of settings.toml (see internal/config.Settings.Claw); this
// package owns its schema. Path-shaped state (sessions, memory, schedule store)
// is NOT configured here — it is derived from the shared base dir so the CLI and
// the gateway agree on locations. See ParseClawConfig.
type GatewayConfig struct {
	AgentAddr      string        `toml:"agent_addr"`      // Connect server address; empty = start an embedded in-process server
	WorkingDir     string        `toml:"working_dir"`     // Agent working directory
	SessionTimeout string        `toml:"session_timeout"` // Inactivity timeout for sessions (Go duration, default: "30m")
	Discord        DiscordConfig `toml:"discord"`
	Memory         MemoryConfig  `toml:"memory"` // Only MaxNotes is read from the file; BaseDir is derived from the shared base dir.

	// Schedules is the multi-job scheduler. Each entry runs on its own
	// goroutine, fires on a cron expression evaluated in its timezone, and can
	// opt into Silent mode (run the prompt but don't echo the response to a
	// chat channel).
	//
	// A leftover "heartbeat" key from the retired single-job block is ignored
	// (unknown fields don't error).
	Schedules []ScheduleConfig `toml:"schedules,omitempty"`

	// Derived from the shared base dir (set by ParseClawConfig, not from the
	// claw block). SessionsDir and SchedulesFile are used directly; the
	// memory directory is written into Memory.BaseDir.
	BaseDir       string `toml:"-"`
	SessionsDir   string `toml:"-"`
	SchedulesFile string `toml:"-"`
}

// DiscordConfig holds Discord bot configuration.
type DiscordConfig struct {
	Token             string   `toml:"token"`
	AllowedGuildIDs   []string `toml:"allowed_guild_ids"`
	AllowedChannelIDs []string `toml:"allowed_channel_ids"`
	AllowedUserIDs    []string `toml:"allowed_user_ids"`
	MentionOnly       bool     `toml:"mention_only"` // In guilds, only respond when @mentioned
}

// ParseClawConfig decodes the "claw" section of settings.toml (block may be nil
// or empty for an all-defaults gateway) and derives all path-shaped state from
// the shared base dir: <base>/sessions, <base>/schedules.json, <base>/memory. This
// keeps the agent's Schedule* tools and the scheduler pointed at the same file,
// and the [SESSION LOG] path the gateway injects at the same directory the agent
// server persists to.
func ParseClawConfig(block map[string]any, baseDir string) (*GatewayConfig, error) {
	cfg := DefaultGatewayConfig()
	if len(block) > 0 {
		if err := config.DecodeBlock(block, cfg); err != nil {
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
		SessionTimeout: "30m",
		Memory:         MemoryConfig{MaxNotes: 30},
	}
}

// ClawRole is the role every gateway session opens with. It is fixed rather
// than configurable: the gateway *is* the claw role — memory injection, the
// 2000-char reply shaping, and the scheduled-run preamble all assume that
// prompt. A per-message skill (a `/<skill>` command or schedules[].skill) still
// selects a capability within the session.
const ClawRole = "claw"
