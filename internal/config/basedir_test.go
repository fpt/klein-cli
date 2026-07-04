package config

import (
	"path/filepath"
	"testing"
)

// TestBaseDirHelpers confirms the shared base dir derives sessions, memory, and
// the schedule store, and that an explicit (env-expanded) base_dir wins — this
// is the isolation knob for `klein claw --settings other.json`.
func TestBaseDirHelpers(t *testing.T) {
	base := t.TempDir()
	s := &Settings{BaseDir: base}

	if s.ResolvedBaseDir() != base {
		t.Errorf("ResolvedBaseDir: got %q want %q", s.ResolvedBaseDir(), base)
	}
	if want := filepath.Join(base, "sessions"); s.SessionsDir() != want {
		t.Errorf("SessionsDir: got %q want %q", s.SessionsDir(), want)
	}
	if want := filepath.Join(base, "memory"); s.MemoryDir() != want {
		t.Errorf("MemoryDir: got %q want %q", s.MemoryDir(), want)
	}
	if want := filepath.Join(base, "schedules.json"); s.SchedulesFile() != want {
		t.Errorf("SchedulesFile: got %q want %q", s.SchedulesFile(), want)
	}
}

// TestBaseDirDefault confirms an unset base_dir resolves under ~/.klein.
func TestBaseDirDefault(t *testing.T) {
	s := &Settings{}
	got := s.ResolvedBaseDir()
	if filepath.Base(got) != ".klein" {
		t.Errorf("default base dir should end in .klein, got %q", got)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("default base dir should be absolute, got %q", got)
	}
}

// TestBaseDirEnvExpand confirms base_dir is env-expanded.
func TestBaseDirEnvExpand(t *testing.T) {
	t.Setenv("KLEIN_TEST_BASE", "/tmp/klein-xyz")
	s := &Settings{BaseDir: "$KLEIN_TEST_BASE/inst"}
	if want := "/tmp/klein-xyz/inst"; s.ResolvedBaseDir() != want {
		t.Errorf("ResolvedBaseDir: got %q want %q", s.ResolvedBaseDir(), want)
	}
}
