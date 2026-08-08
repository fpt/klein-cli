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
//	GALLIUM_BIN=/path/to/gallium go test ./pkg/agentserver/ -run Gallium
//
// It needs no model: gallium's `scripted` engine replays a JSON script, so a
// whole turn including a tool call finishes in milliseconds.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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

// driftLog returns a logger to hand the Runner, plus the buffer its reports land
// in. Passing it is what makes these tests cover the *other* half of the drift
// defense: turnProgress warns about item types nothing renders and notification
// methods nothing handles, and only a real backend can produce either one klein
// did not think of. assertNoDrift then fails the test on one, so a future
// gallium that adds an item type or a method says so here rather than in a
// user's log.
func driftLog() (Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return bufLogger{buf: &buf}, &buf
}

// assertNoDrift fails if the turn produced an unrendered-item or unhandled-method
// report. Call after RunTurn returns: reports are written from the same goroutine.
func assertNoDrift(t *testing.T, buf *bytes.Buffer) {
	t.Helper()
	if buf.Len() != 0 {
		t.Errorf("gallium sent something klein does not handle: %s", buf.String())
	}
}

// writeScript stores a script for gallium's `scripted` engine and returns its
// path.
func writeScript(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script.json")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatalf("writing script: %v", err)
	}
	return path
}

// galliumScript is one tool call then a reply — the shortest script that
// exercises the whole ReAct loop and both halves of a tool-call notification.
func galliumScript(t *testing.T) string {
	t.Helper()
	return writeScript(t, `{
	  "steps": [
	    { "toolCalls": [{ "id": "c1", "name": "LS", "arguments": { "path": "." } }] },
	    { "text": "I listed the working directory.", "inputTokens": 42 }
	  ]
	}`)
}

// newGalliumRunner spawns a gallium app-server replaying script in workDir, and
// returns it with the buffer its drift reports land in. Closed on cleanup.
//
// GALLIUM_AUTO_APPROVE is set because these turns run tools: the scripts only
// read, but a backend that stopped to ask, with no terminal to ask on, would
// hang rather than fail.
func newGalliumRunner(t *testing.T, bin, script, workDir string) (*Runner, *bytes.Buffer) {
	t.Helper()
	runner, drift, _ := newGalliumRunnerWithTools(t, bin, script, workDir, nil)
	return runner, drift
}

// newGalliumRunnerWithTools is newGalliumRunner plus a tool set offered to the
// backend, for the one test that needs the callback direction.
func newGalliumRunnerWithTools(
	t *testing.T, bin, script, workDir string, tools DynamicTools,
) (*Runner, *bytes.Buffer, Logger) {
	t.Helper()
	logger, drift := driftLog()
	runner, err := NewRunner(context.Background(), Config{
		Command:        bin,
		Args:           []string{"app-server"},
		Logger:         logger,
		Env:            []string{"INFERENCE_ENGINE=scripted", "MODEL_PATH=" + script, "GALLIUM_AUTO_APPROVE=1"},
		Dialect:        DialectGeneric,
		Cwd:            workDir,
		ApprovalPolicy: ApprovalNever,
		Tools:          tools,
	})
	if err != nil {
		t.Fatalf("spawning gallium: %v", err)
	}
	t.Cleanup(func() { _ = runner.Close() })
	return runner, drift, logger
}

// callbackTools offers one tool and records the arguments it was called with.
type callbackTools struct {
	called chan map[string]any
}

func (c callbackTools) Specs() []ToolSpec {
	return []ToolSpec{{
		Name:        "KleinPing",
		Description: "Records a note on the caller's side. Call it with any text.",
		Parameters: []Parameter{
			{Name: "note", Type: jsonTypeString, Required: true, Description: "text to record"},
		},
	}}
}

func (c callbackTools) Call(_ context.Context, name string, args map[string]any) (string, error) {
	if name != "KleinPing" {
		return "", fmt.Errorf("unexpected tool %q", name)
	}
	select {
	case c.called <- args:
	default:
	}
	return "recorded", nil
}

// A dynamic tool is the one direction of this protocol that runs backwards: the
// server calls the client, mid-turn, over the same connection. Every other test
// here drives the client's side of a request it made itself.
//
// It went uncovered against a real backend until the client was extracted, and
// the extraction is exactly what made it worth covering — Config.Tools is now an
// interface an outside caller implements, and nothing but a real server proves
// that a spec written through it is one the server can actually dispatch.
func TestGallium_RealAppServer_CallsBackForADynamicTool(t *testing.T) {
	t.Parallel()
	bin := galliumBin(t)

	script := writeScript(t, `{
	  "steps": [
	    { "toolCalls": [{ "id": "c1", "name": "KleinPing", "arguments": { "note": "from the backend" } }] },
	    { "text": "I called your tool." }
	  ]
	}`)
	tools := callbackTools{called: make(chan map[string]any, 1)}
	runner, drift, _ := newGalliumRunnerWithTools(t, bin, script, t.TempDir(), tools)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var got []capturedEvent
	_, text, err := runner.RunTurn(ctx, "", "ping yourself", "", recorder{got: &got})
	if err != nil {
		t.Fatalf("running a turn: %v", err)
	}

	// The tool ran in this process, with the arguments the backend chose.
	select {
	case args := <-tools.called:
		if args["note"] != "from the backend" {
			t.Errorf("arguments did not survive the round trip: %+v", args)
		}
	default:
		t.Fatal("the backend never called back for the registered tool")
	}

	if !strings.Contains(text, "I called your tool") {
		t.Errorf("turn text: got %q", text)
	}
	// And the call was reported, so a caller can see it happen.
	var names []string
	for _, e := range got {
		if d, ok := e.Data.(ToolCall); ok {
			names = append(names, d.Name)
		}
	}
	if !slices.Contains(names, "KleinPing") {
		t.Errorf("the dynamic tool call was not reported to the observer: %v", names)
	}
	assertNoDrift(t, drift)
}

func TestGallium_RealAppServer_RendersAToolCall(t *testing.T) {
	bin := galliumBin(t)

	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "only-file.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("seeding the work dir: %v", err)
	}
	runner, drift := newGalliumRunner(t, bin, galliumScript(t), work)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var got []capturedEvent

	_, text, err := runner.RunTurn(ctx, "", "what is here?", "", recorder{got: &got})
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
	var starts []ToolCall
	for _, e := range got {
		if d, ok := e.Data.(ToolCall); ok {
			starts = append(starts, d)
		}
	}
	if len(starts) != 1 {
		t.Fatalf("want 1 tool-call start, got %d: %+v", len(starts), got)
	}
	if starts[0].Name != "LS" {
		t.Errorf("tool name: want LS, got %q", starts[0].Name)
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

	assertNoDrift(t, drift)
}

// A failing tool must be distinguishable from a passing one. It was not: IsError
// derives from fields only the dropped item carried, so every call looked like a
// success.
func TestGallium_RealAppServer_RendersAFailingToolCall(t *testing.T) {
	bin := galliumBin(t)

	// Read a file that is not there: a real tool failure, not a synthetic one.
	script := writeScript(t, `{
	  "steps": [
	    { "toolCalls": [{ "id": "c1", "name": "Read", "arguments": { "path": "no-such-file.txt" } }] },
	    { "text": "I could not read it." }
	  ]
	}`)
	runner, drift := newGalliumRunner(t, bin, script, t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var got []capturedEvent

	if _, _, err := runner.RunTurn(ctx, "", "read it", "", recorder{got: &got}); err != nil {
		t.Fatalf("running a turn: %v", err)
	}

	results := toolResults(got)
	if len(results) != 1 {
		t.Fatalf("want 1 tool result, got %d: %+v", len(results), got)
	}
	if !results[0].IsError {
		t.Errorf("a failing tool rendered as a success: %+v", results[0])
	}

	assertNoDrift(t, drift)
}

// A turn abandoned mid-flight — Ctrl+C, or any canceled context — must not
// leave the backend working.
//
// gallium answers turn/start at once and runs the turn behind it
// (fpt/rs-gallium#53), and refuses a second turn on a thread whose slot is still
// held: "one turn at a time". So walking away without saying anything cost the
// user their *next* message too, and every one after it until the abandoned turn
// ended on its own — a sleeping shell, or a model call, for as long as it took.
func TestGallium_RealAppServer_CancelledTurnFreesTheThread(t *testing.T) {
	t.Parallel()
	bin := galliumBin(t)

	// A tool that outlives the cancellation by a wide margin, so "the thread is
	// free again" cannot be confused with "the turn happened to finish".
	script := writeScript(t, `{
	  "steps": [
	    { "toolCalls": [{ "id": "c1", "name": "Bash", "arguments": { "command": "sleep 30" } }] },
	    { "text": "first turn." },
	    { "text": "second turn." },
	    { "text": "third turn." }
	  ]
	}`)
	runner, drift := newGalliumRunner(t, bin, script, t.TempDir())

	// Turn 1, canceled while the tool is still running. This is Ctrl+C.
	ctx1, cancel1 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel1()
	started := time.Now()
	threadID, _, err1 := runner.RunTurn(ctx1, "", "sleep please", "", discardObserver{})
	if err1 == nil {
		t.Fatal("a canceled turn should return an error")
	}
	// The interrupt is answered only once the turn has aborted, so returning at
	// all means the backend confirmed it stopped — well before the sleep's 30s.
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Errorf("waited %v for the interrupt: the turn was not stopped, only waited out", elapsed)
	}

	// Turn 2 is the next thing the user types. It must be served.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	_, text, err2 := runner.RunTurn(ctx2, threadID, "still there?", "", discardObserver{})
	if err2 != nil {
		t.Fatalf("the thread was left unusable by the canceled turn: %v", err2)
	}
	if text == "" {
		t.Error("the next turn produced no text")
	}

	assertNoDrift(t, drift)
}

// A turn gallium fails must come back as an error, and this is the pair that
// nothing else covers: klein reads the outcome from turn/completed's status, and
// gallium is the only thing that can prove it sends the status klein reads.
//
// The script runs dry — one tool call and no reply — so the ReAct loop asks for
// a step that does not exist and the provider errors. That fails the turn inside
// gallium, which is the only honest way to reach this path.
func TestGallium_RealAppServer_FailedTurnIsAnError(t *testing.T) {
	t.Parallel()
	bin := galliumBin(t)

	script := writeScript(t, `{
	  "steps": [
	    { "toolCalls": [{ "id": "c1", "name": "LS", "arguments": { "path": "." } }] }
	  ]
	}`)
	runner, drift := newGalliumRunner(t, bin, script, t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, text, err := runner.RunTurn(ctx, "", "list it", "", discardObserver{})
	if err == nil {
		t.Fatalf("a failed turn returned success with text %q", text)
	}
	// Specifically the turn-failed classification, not a dead transport or a
	// timeout — either of which would also produce "an error" and hide the fact
	// that klein never read the status at all.
	if !strings.Contains(err.Error(), "turn failed") {
		t.Errorf("the turn failed for the wrong reason: %v", err)
	}

	assertNoDrift(t, drift)
}
