package gateway

import (
	"path/filepath"
	"testing"
)

// TestParseClawConfigDefaults confirms an empty claw block yields defaults and
// derives every path-shaped field from the shared base dir.
func TestParseClawConfigDefaults(t *testing.T) {
	base := t.TempDir()
	cfg, err := ParseClawConfig(nil, base)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if cfg.AgentAddr != "" {
		t.Errorf("AgentAddr should be empty (embedded) by default, got %q", cfg.AgentAddr)
	}
	// The gateway's role is fixed, not configurable.
	if ClawRole != "claw" {
		t.Errorf("ClawRole: got %q want claw", ClawRole)
	}
	if cfg.SessionTimeout != "30m" {
		t.Errorf("SessionTimeout: got %q want 30m", cfg.SessionTimeout)
	}
	if cfg.Memory.MaxNotes != 30 {
		t.Errorf("MaxNotes: got %d want 30", cfg.Memory.MaxNotes)
	}

	// All state derives from the base dir — this is what keeps a --settings
	// instance isolated and keeps the Schedule* tools and the scheduler in sync.
	if want := filepath.Join(base, "sessions"); cfg.SessionsDir != want {
		t.Errorf("SessionsDir: got %q want %q", cfg.SessionsDir, want)
	}
	if want := filepath.Join(base, "schedules.json"); cfg.SchedulesFile != want {
		t.Errorf("SchedulesFile: got %q want %q", cfg.SchedulesFile, want)
	}
	if want := filepath.Join(base, "memory"); cfg.Memory.BaseDir != want {
		t.Errorf("Memory.BaseDir: got %q want %q", cfg.Memory.BaseDir, want)
	}
}

// TestParseClawConfigOverrides confirms behavior fields come from the block while
// paths are still base-derived (the block cannot set them).
func TestParseClawConfigOverrides(t *testing.T) {
	base := t.TempDir()
	raw := []byte(`{
		"agent_addr": "http://remote:50051",
		"discord": {"token": "abc", "mention_only": true},
		"memory": {"max_notes": 10},
		"schedules": [{"name": "j", "enabled": true, "cron": "0 8 * * *", "timezone": "Asia/Tokyo", "prompt": "go"}]
	}`)
	cfg, err := ParseClawConfig(raw, base)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.AgentAddr != "http://remote:50051" {
		t.Errorf("AgentAddr: got %q", cfg.AgentAddr)
	}
	if !cfg.Discord.MentionOnly || cfg.Discord.Token != "abc" {
		t.Errorf("Discord not parsed: %+v", cfg.Discord)
	}
	if cfg.Memory.MaxNotes != 10 {
		t.Errorf("MaxNotes: got %d want 10", cfg.Memory.MaxNotes)
	}
	if len(cfg.Schedules) != 1 || cfg.Schedules[0].Name != "j" {
		t.Errorf("Schedules not parsed: %+v", cfg.Schedules)
	}
	// Base-derived paths win regardless of block content.
	if cfg.SchedulesFile != filepath.Join(base, "schedules.json") {
		t.Errorf("SchedulesFile should be base-derived, got %q", cfg.SchedulesFile)
	}
}
