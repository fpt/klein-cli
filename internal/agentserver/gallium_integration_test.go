package agentserver

// An integration test against a real rs-gallium app-server.
//
// Everything else in this package tests klein's beliefs about the protocol:
// notifications are hand-built and fed to classifyNote. That is exactly how
// fpt/rs-gallium#49 stayed hidden — gallium sent an item type this renderer had
// no case for, klein's tests passed, gallium's tests passed, and every tool call
// displayed as a successful `exec` returning "exit 0". Nothing tested the pair.
//
// This does. It spawns the real binary, runs a real turn through the real
// Runner, and asserts on what klein actually renders.
//
// Skipped unless GALLIUM_BIN points at a gallium build, matching gallium's own opt-in
// model tests:
//
//	GALLIUM_BIN=/path/to/gallium go test ./internal/agentserver/ -run Gallium
//
// It needs no model: gallium's `scripted` engine replays a JSON script, so a
// whole turn including a tool call finishes in milliseconds.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fpt/klein-cli/pkg/agent/events"
)

// galliumBin returns the gallium to test against, or skips.
//
// Deliberately only GALLIUM_BIN, with no fall back to `gallium` on PATH: an
// installed gallium is usually whatever was last `make install`ed, and running
// these against a stale one turns "you did not configure this" into a confusing
// assertion failure. Observed while writing this — the PATH binary was a day old
// and had no scripted engine.
func galliumBin(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("GALLIUM_BIN")
	if bin == "" {
		t.Skip("set GALLIUM_BIN to a gallium build to run the integration tests")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("GALLIUM_BIN=%q is not usable: %v", bin, err)
	}
	return bin
}

// galliumScript writes a script for gallium's `scripted` engine: one tool call,
// then a reply. The shortest script that exercises the whole ReAct loop and both
// halves of a tool-call notification.
func galliumScript(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "script.json")
	script := `{
	  "steps": [
	    { "toolCalls": [{ "id": "c1", "name": "LS", "arguments": { "path": "." } }] },
	    { "text": "I listed the working directory.", "inputTokens": 42 }
	  ]
	}`
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatalf("writing script: %v", err)
	}
	return path
}

func TestGallium_RealAppServer_RendersAToolCall(t *testing.T) {
	bin := galliumBin(t)

	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "only-file.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("seeding the work dir: %v", err)
	}
	script := galliumScript(t, t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	runner, err := NewRunner(ctx, Config{
		Command: bin,
		Args:    []string{"app-server"},
		Env: []string{
			"INFERENCE_ENGINE=scripted",
			"MODEL_PATH=" + script,
			// The turn calls LS, which is read-only, but a backend prompting for
			// approval with no terminal would hang rather than fail.
			"GALLIUM_AUTO_APPROVE=1",
		},
		Backend:        BackendAppServer,
		Cwd:            work,
		ApprovalPolicy: ApprovalNever,
	})
	if err != nil {
		t.Fatalf("spawning gallium: %v", err)
	}
	defer func() { _ = runner.Close() }()

	var got []capturedEvent
	emit := func(typ events.EventType, data any) {
		got = append(got, capturedEvent{Type: typ, Data: data})
	}

	_, text, err := runner.RunTurn(ctx, "", "what is here?", "", emit)
	if err != nil {
		t.Fatalf("running a turn: %v", err)
	}

	// The reply the script dictates, round-tripped through the protocol.
	if !strings.Contains(text, "I listed the working directory.") {
		t.Errorf("turn text: got %q", text)
	}

	// The tool call must be announced as the tool it is. Before
	// fpt/rs-gallium#50 this arrived as `commandExecution` and rendered as a
	// shell named `exec`.
	var starts []events.ToolCallStartData
	for _, e := range got {
		if d, ok := e.Data.(events.ToolCallStartData); ok {
			starts = append(starts, d)
		}
	}
	if len(starts) != 1 {
		t.Fatalf("want 1 tool-call start, got %d: %+v", len(starts), got)
	}
	if starts[0].ToolName != "LS" {
		t.Errorf("tool name: want LS, got %q", starts[0].ToolName)
	}

	// And its real output must reach the client. Before #50 the result item
	// carried a type nothing rendered, so it was dropped and the placeholder
	// "exit 0" was shown in its place.
	results := toolResults(got)
	if len(results) != 1 {
		t.Fatalf("want 1 tool result, got %d: %+v", len(results), got)
	}
	if results[0].IsError {
		t.Errorf("a successful LS rendered as an error: %+v", results[0])
	}
	if !strings.Contains(results[0].Content, "only-file.txt") {
		t.Errorf("the tool's real output did not reach the client: %q", results[0].Content)
	}
	if strings.Contains(results[0].Content, "exit 0") {
		t.Errorf("still rendering the sandboxed-shell placeholder: %q", results[0].Content)
	}
}

// A failing tool must be distinguishable from a passing one. It was not: IsError
// derives from fields only the dropped item carried, so every call looked like a
// success.
func TestGallium_RealAppServer_RendersAFailingToolCall(t *testing.T) {
	bin := galliumBin(t)

	work := t.TempDir()
	scriptDir := t.TempDir()
	path := filepath.Join(scriptDir, "script.json")
	// Read a file that is not there: a real tool failure, not a synthetic one.
	script := `{
	  "steps": [
	    { "toolCalls": [{ "id": "c1", "name": "Read", "arguments": { "path": "no-such-file.txt" } }] },
	    { "text": "I could not read it." }
	  ]
	}`
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatalf("writing script: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	runner, err := NewRunner(ctx, Config{
		Command:        bin,
		Args:           []string{"app-server"},
		Env:            []string{"INFERENCE_ENGINE=scripted", "MODEL_PATH=" + path, "GALLIUM_AUTO_APPROVE=1"},
		Backend:        BackendAppServer,
		Cwd:            work,
		ApprovalPolicy: ApprovalNever,
	})
	if err != nil {
		t.Fatalf("spawning gallium: %v", err)
	}
	defer func() { _ = runner.Close() }()

	var got []capturedEvent
	emit := func(typ events.EventType, data any) {
		got = append(got, capturedEvent{Type: typ, Data: data})
	}

	if _, _, err := runner.RunTurn(ctx, "", "read it", "", emit); err != nil {
		t.Fatalf("running a turn: %v", err)
	}

	results := toolResults(got)
	if len(results) != 1 {
		t.Fatalf("want 1 tool result, got %d: %+v", len(results), got)
	}
	if !results[0].IsError {
		t.Errorf("a failing tool rendered as a success: %+v", results[0])
	}
}
