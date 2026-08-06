package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// testUserConfig returns a UserConfig rooted in a temp dir, so these tests never
// touch the developer's real ~/.klein.
func testUserConfig(t *testing.T) (*UserConfig, string) {
	t.Helper()
	base := t.TempDir()
	c := &UserConfig{
		BaseDir:     base,
		ProjectsDir: filepath.Join(base, "projects"),
		ConfigFile:  filepath.Join(base, "config.json"),
	}
	if err := c.EnsureDirectories(); err != nil {
		t.Fatal(err)
	}
	return c, filepath.Join(base, "workdir")
}

// writeSession creates a session file with the given mtime, standing in for a
// conversation that was last used then.
func writeSession(t *testing.T, path string, modTime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{"messages":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

// A fresh session must not be created on disk. An interactive run that exits
// without a single exchange has to leave nothing behind, or the next
// `--continue` would resume that empty session instead of the real conversation
// before it.
func TestNewProjectSessionFile_IsNotCreatedUntilWritten(t *testing.T) {
	t.Parallel()
	c, workdir := testUserConfig(t)

	path, err := c.NewProjectSessionFile(workdir)
	if err != nil {
		t.Fatalf("NewProjectSessionFile: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a fresh session path must not exist yet, stat err = %v", err)
	}

	latest, err := c.LatestProjectSessionFile(workdir)
	if err != nil {
		t.Fatalf("LatestProjectSessionFile: %v", err)
	}
	if latest != "" {
		t.Errorf("an unwritten session must not be resumable, got %q", latest)
	}
}

// Two shells starting in the same second must not collide onto one file, or
// they would silently share (and clobber) a conversation.
func TestNewProjectSessionFile_NamesAreUnique(t *testing.T) {
	t.Parallel()
	c, workdir := testUserConfig(t)

	seen := make(map[string]bool)
	for range 50 {
		path, err := c.NewProjectSessionFile(workdir)
		if err != nil {
			t.Fatalf("NewProjectSessionFile: %v", err)
		}
		if seen[path] {
			t.Fatalf("duplicate session path handed out: %q", path)
		}
		seen[path] = true
	}
}

// `--continue` resumes the session most recently *used*, which is what ordering
// by mtime buys: a session started days ago but worked in today is the one you
// mean, whenever its name says it began.
func TestLatestProjectSessionFile_PicksMostRecentlyUsed(t *testing.T) {
	t.Parallel()
	c, workdir := testUserConfig(t)
	dir, err := c.GetProjectSessionsDir(workdir)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	oldName := filepath.Join(dir, "20260101T090000.000000000.json")
	newName := filepath.Join(dir, "20200101T090000.000000000.json")
	writeSession(t, oldName, now.Add(-2*time.Hour)) // newest name, oldest use
	writeSession(t, newName, now)                   // oldest name, newest use

	latest, err := c.LatestProjectSessionFile(workdir)
	if err != nil {
		t.Fatalf("LatestProjectSessionFile: %v", err)
	}
	if latest != newName {
		t.Errorf("expected the most recently used session %q, got %q", newName, latest)
	}
}

// Two sessions can land on the same mtime — filesystem timestamp resolution is
// coarser than the nanoseconds a session name carries, and starting two sessions
// back to back is enough to collide. Before the tie-break this was not merely
// arbitrary but reliably *wrong*: `os.ReadDir` returns names ascending, so the
// first entry won and `--continue` resumed the older session.
//
// Observed in CI on fpt/klein-cli#95, on two files 1.6ms apart.
func TestLatestProjectSessionFile_EqualMtimesPickTheLaterName(t *testing.T) {
	t.Parallel()
	c, workdir := testUserConfig(t)
	dir, err := c.GetProjectSessionsDir(workdir)
	if err != nil {
		t.Fatal(err)
	}

	// Identical mtimes, so only the name can decide.
	same := time.Now().Truncate(time.Second)
	earlier := filepath.Join(dir, "20260101T090000.011827479.json")
	later := filepath.Join(dir, "20260101T090000.013434046.json")
	writeSession(t, earlier, same)
	writeSession(t, later, same)

	latest, err := c.LatestProjectSessionFile(workdir)
	if err != nil {
		t.Fatalf("LatestProjectSessionFile: %v", err)
	}
	if latest != later {
		t.Errorf("expected the later session %q, got %q", later, latest)
	}
}

// The codex thread sidecar lives beside its session as "<session>.json.codex-thread".
// Mistaking one for a session of its own would hand --continue a file that holds
// a thread id rather than a conversation.
func TestLatestProjectSessionFile_IgnoresSidecars(t *testing.T) {
	t.Parallel()
	c, workdir := testUserConfig(t)
	dir, err := c.GetProjectSessionsDir(workdir)
	if err != nil {
		t.Fatal(err)
	}

	session := filepath.Join(dir, "20260101T090000.000000000.json")
	writeSession(t, session, time.Now().Add(-time.Hour))
	// Written later, so it would win on mtime if it were considered at all.
	if err := os.WriteFile(session+".codex-thread", []byte("thread-123"), 0o600); err != nil {
		t.Fatal(err)
	}

	latest, err := c.LatestProjectSessionFile(workdir)
	if err != nil {
		t.Fatalf("LatestProjectSessionFile: %v", err)
	}
	if latest != session {
		t.Errorf("expected the session file %q, got %q", session, latest)
	}
}

// An empty project is not an error: `--continue` with nothing to continue falls
// back to a fresh session.
func TestLatestProjectSessionFile_EmptyProject(t *testing.T) {
	t.Parallel()
	c, workdir := testUserConfig(t)

	latest, err := c.LatestProjectSessionFile(workdir)
	if err != nil {
		t.Fatalf("an empty project should not error: %v", err)
	}
	if latest != "" {
		t.Errorf("expected no session, got %q", latest)
	}
}

// The conversation in flight when the user upgrades has to stay resumable —
// otherwise this change silently eats it. The codex sidecar travels with it, or
// a resumed session would start a brand-new codex thread.
func TestMigrateLegacySession_KeepsThePreUpgradeConversation(t *testing.T) {
	t.Parallel()
	c, workdir := testUserConfig(t)
	projectDir, err := c.GetProjectDataDir(workdir)
	if err != nil {
		t.Fatal(err)
	}

	legacy := filepath.Join(projectDir, "session.json")
	writeSession(t, legacy, time.Now().Add(-time.Hour))
	if err := os.WriteFile(legacy+".codex-thread", []byte("thread-abc"), 0o600); err != nil {
		t.Fatal(err)
	}

	latest, err := c.LatestProjectSessionFile(workdir)
	if err != nil {
		t.Fatalf("LatestProjectSessionFile: %v", err)
	}
	if latest == "" {
		t.Fatal("the pre-upgrade session should be resumable")
	}
	if filepath.Dir(latest) != filepath.Join(projectDir, "sessions") {
		t.Errorf("the migrated session should live in sessions/, got %q", latest)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("the legacy file should have been moved, stat err = %v", err)
	}
	sidecar, err := os.ReadFile(latest + ".codex-thread")
	if err != nil {
		t.Fatalf("the codex sidecar should have travelled with the session: %v", err)
	}
	if string(sidecar) != "thread-abc" {
		t.Errorf("sidecar content: got %q", sidecar)
	}
}

// Two klein processes starting at once during the upgrade can both pass the
// emptiness check and race on the rename. The loser must treat "source already
// gone" as success: migration failure costs that run its session persistence
// entirely, since newSharedSessionState falls back to in-memory state.
func TestMigrateLegacySession_ConcurrentStartupIsBenign(t *testing.T) {
	t.Parallel()
	c, workdir := testUserConfig(t)
	projectDir, err := c.GetProjectDataDir(workdir)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, filepath.Join(projectDir, "session.json"), time.Now().Add(-time.Hour))

	const racers = 16
	var wg sync.WaitGroup
	results := make([]string, racers)
	errs := make([]error, racers)
	start := make(chan struct{})

	for i := range racers {
		wg.Go(func() {
			<-start // release them together to maximize overlap
			results[i], errs[i] = c.LatestProjectSessionFile(workdir)
		})
	}
	close(start)
	wg.Wait()

	want := filepath.Join(projectDir, "sessions", "migrated-session.json")
	for i := range racers {
		if errs[i] != nil {
			t.Errorf("racer %d failed startup: %v", i, errs[i])
		}
		if errs[i] == nil && results[i] != want {
			t.Errorf("racer %d resolved %q, want the migrated session %q", i, results[i], want)
		}
	}
}

// Once a project has real per-run sessions, a stale session.json left over from
// before the upgrade must not jump ahead of them.
func TestMigrateLegacySession_DoesNotDisplaceRealSessions(t *testing.T) {
	t.Parallel()
	c, workdir := testUserConfig(t)
	projectDir, err := c.GetProjectDataDir(workdir)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := c.GetProjectSessionsDir(workdir)
	if err != nil {
		t.Fatal(err)
	}

	real := filepath.Join(dir, "20260101T090000.000000000.json")
	writeSession(t, real, time.Now().Add(-time.Hour))
	// A legacy file that is newer than the real session, so it would win if moved.
	legacy := filepath.Join(projectDir, "session.json")
	writeSession(t, legacy, time.Now())

	latest, err := c.LatestProjectSessionFile(workdir)
	if err != nil {
		t.Fatalf("LatestProjectSessionFile: %v", err)
	}
	if latest != real {
		t.Errorf("expected the real session %q, got %q", real, latest)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("the legacy file should have been left alone: %v", err)
	}
}
