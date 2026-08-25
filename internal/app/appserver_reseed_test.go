package app

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/pkg/agent/events"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// The ids the stand-in hands out. Two turns given different ids is a thread
// that was replaced; the same id twice is one that held.
const (
	firstThread  = "thread_1"
	secondThread = "thread_2"
)

// recordingBackend stands in for the app-server. It records what each turn was
// given and answers with the thread id it was told to.
type recordingBackend struct {
	// threadIDs are handed out in order, one per turn. A turn that gets a
	// different id from the one it passed in is a thread that was replaced —
	// what happens when the connection died and RunTurn opened a fresh one.
	threadIDs    []string
	instructions []string
	prompts      []string
	turn         int
}

func (b *recordingBackend) RunTurn(
	_ context.Context, _, prompt, developerInstructions string, _ func(events.EventType, any),
) (string, string, error) {
	b.instructions = append(b.instructions, developerInstructions)
	b.prompts = append(b.prompts, prompt)
	id := b.threadIDs[min(b.turn, len(b.threadIDs)-1)]
	b.turn++
	return id, "acknowledged", nil
}

func backendAgent(t *testing.T, backend *recordingBackend) (*Agent, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	a, cleanup, err := NewAgentWithOptions(context.Background(), AgentOptions{
		Settings:          config.GetDefaultSettings(),
		WorkingDir:        t.TempDir(),
		Logger:            pkgLogger.NewLoggerWithConsoleWriter(pkgLogger.LogLevelWarn, logs),
		Out:               io.Discard,
		FsRepo:            infra.NewOSFilesystemRepository(),
		IsInteractiveMode: false,
		LLMClient:         &stubLLM{},
	})
	if err != nil {
		t.Fatalf("NewAgentWithOptions: %v", err)
	}
	t.Cleanup(cleanup)
	a.SetCodexBackend(backend)
	return a, logs
}

// The bug: a thread's history lives on the app-server, and klein sends only the
// prompt, so a thread started after a reconnect reaches the model knowing
// nothing about the conversation. The user asks about "the second one" and the
// model has never heard of the first.
//
// klein does keep the exchange, so every turn now offers it as developer
// instructions, which is the one channel a thread start reads. This asserts the
// second turn carries the first — the thing that was missing.
func TestInvokeCodex_ARestartedThreadIsGivenTheConversation(t *testing.T) {
	t.Parallel()

	// Same id twice would be a thread that survived; these differ, which is a
	// connection that died between the turns.
	backend := &recordingBackend{threadIDs: []string{firstThread, secondThread}}
	a, _ := backendAgent(t, backend)
	ctx := context.Background()

	if _, err := a.Invoke(ctx, "rename the first helper", "code"); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if _, err := a.Invoke(ctx, "now fix the second one too", "code"); err != nil {
		t.Fatalf("second turn: %v", err)
	}

	if len(backend.instructions) != 2 {
		t.Fatalf("expected two turns, got %d", len(backend.instructions))
	}
	// The first turn opens the session: there is nothing to re-seed, and a model
	// told a conversation happened when none did is its own wrong answer.
	if strings.Contains(backend.instructions[0], "Conversation so far") {
		t.Errorf("the first turn was seeded with a conversation that had not happened:\n%s", backend.instructions[0])
	}
	for _, want := range []string{"rename the first helper", "acknowledged"} {
		if !strings.Contains(backend.instructions[1], want) {
			t.Errorf("the replacement thread was not told about %q:\n%s", want, backend.instructions[1])
		}
	}
	// The prompt stays the user's own words; the history rides alongside it.
	if backend.prompts[1] != "now fix the second one too" {
		t.Errorf("the turn prompt was rewritten: %q", backend.prompts[1])
	}
}

// The repair is best-effort — the re-seed is bounded, and whatever the backend
// had open (files it had read, commands it had run) did not come back with the
// thread. Losing that silently is how the gap stayed invisible.
func TestInvokeCodex_SaysWhenAThreadWasReplaced(t *testing.T) {
	t.Parallel()

	backend := &recordingBackend{threadIDs: []string{firstThread, secondThread}}
	a, logs := backendAgent(t, backend)
	ctx := context.Background()

	if _, err := a.Invoke(ctx, "first", "code"); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if got := logs.String(); strings.Contains(got, "connection was lost") {
		t.Fatalf("opening a session reported a lost connection:\n%s", got)
	}

	if _, err := a.Invoke(ctx, "second", "code"); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if got := logs.String(); !strings.Contains(got, "connection was lost") {
		t.Errorf("a replaced thread went unreported:\n%s", got)
	}
}

// A thread that is simply continuing is the ordinary case, and must stay quiet:
// a warning on every turn is a warning nobody reads by the time it matters.
func TestInvokeCodex_QuietWhileTheThreadHolds(t *testing.T) {
	t.Parallel()

	backend := &recordingBackend{threadIDs: []string{firstThread}}
	a, logs := backendAgent(t, backend)
	ctx := context.Background()

	for _, input := range []string{"first", "second", "third"} {
		if _, err := a.Invoke(ctx, input, "code"); err != nil {
			t.Fatalf("turn %q: %v", input, err)
		}
	}
	if got := logs.String(); strings.Contains(got, "connection was lost") {
		t.Errorf("a thread that never changed was reported as replaced:\n%s", got)
	}
}
