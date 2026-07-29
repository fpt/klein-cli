package app

import (
	"os"
	"testing"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
	"github.com/fpt/klein-cli/pkg/message"
)

// sessionStateFor drives newSharedSessionState the way an interactive run does.
// The HOME override keeps every session file under the test's temp dir.
func sessionStateFor(t *testing.T, continueSession bool, workingDir string) (domain.State, string) {
	t.Helper()
	return newSharedSessionState(
		true /* interactive */, false /* skipSessionRestore */, continueSession,
		workingDir, pkgLogger.NewLogger(pkgLogger.LogLevelError))
}

// record adds one message and persists, standing in for a turn of conversation.
func record(t *testing.T, st domain.State, text string) {
	t.Helper()
	st.AddMessage(message.NewChatMessage(message.MessageTypeUser, text))
	if err := st.SaveToFile(); err != nil {
		t.Fatalf("SaveToFile: %v", err)
	}
}

// The whole point of the change, end to end: a plain `klein` starts empty and
// leaves the previous conversation alone, and `--continue` picks that
// conversation back up.
//
// The middle step is the one that used to be impossible — under a single
// session.json, the fresh run's first save overwrote the history that
// `--continue` is supposed to resume.
func TestSessionLifecycle_FreshByDefault_ContinueResumes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workingDir := t.TempDir()

	// A first session with some history.
	first, firstPath := sessionStateFor(t, false, workingDir)
	if got := len(first.GetMessages()); got != 0 {
		t.Fatalf("a fresh session should start empty, got %d messages", got)
	}
	record(t, first, "first conversation")

	// A plain `klein` afterwards: empty, and on a different file.
	second, secondPath := sessionStateFor(t, false, workingDir)
	if got := len(second.GetMessages()); got != 0 {
		t.Errorf("a fresh session must not inherit history, got %d messages", got)
	}
	if secondPath == firstPath {
		t.Errorf("a fresh session must not reuse the previous file (%q)", firstPath)
	}
	record(t, second, "second conversation")

	// The first conversation is still on disk, untouched by the fresh run.
	if _, err := os.Stat(firstPath); err != nil {
		t.Errorf("the previous session should survive a fresh run: %v", err)
	}

	// `--continue` resumes the most recently used session — the second one.
	resumed, resumedPath := sessionStateFor(t, true, workingDir)
	if resumedPath != secondPath {
		t.Errorf("--continue should resume the most recent session %q, got %q", secondPath, resumedPath)
	}
	msgs := resumed.GetMessages()
	if len(msgs) != 1 || msgs[0].Content() != "second conversation" {
		t.Errorf("--continue restored the wrong history: %+v", msgs)
	}
}

// `--continue` on a project that has never been used is a no-op, not an error:
// it falls through to a fresh session.
func TestSessionLifecycle_ContinueWithNoHistoryStartsFresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st, path := sessionStateFor(t, true, t.TempDir())
	if st == nil {
		t.Fatal("--continue with no history should still yield a session")
	}
	if len(st.GetMessages()) != 0 {
		t.Errorf("expected an empty session, got %d messages", len(st.GetMessages()))
	}
	if path == "" {
		t.Error("the fresh fallback should still be file-backed")
	}
}

// File mode (-f) asks for no inherited history, so it must win over --continue
// rather than quietly resuming a conversation into a scripted run.
func TestSessionLifecycle_SkipRestoreBeatsContinue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workingDir := t.TempDir()

	first, firstPath := sessionStateFor(t, false, workingDir)
	record(t, first, "prior conversation")

	skipped, path := newSharedSessionState(
		true /* interactive */, true /* skipSessionRestore */, true, /* continueSession */
		workingDir, pkgLogger.NewLogger(pkgLogger.LogLevelError))

	if len(skipped.GetMessages()) != 0 {
		t.Errorf("skipSessionRestore must not inherit history, got %d messages", len(skipped.GetMessages()))
	}
	if path == firstPath {
		t.Errorf("skipSessionRestore must not reuse the previous session file %q", firstPath)
	}
}
