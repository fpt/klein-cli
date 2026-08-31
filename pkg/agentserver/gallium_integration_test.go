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
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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

// appServerArgs puts gallium into app-server mode, pointed at a config file that
// configures nothing.
//
// The --config is what makes these tests hermetic. It was learned the hard way,
// from a developer config whose `[agent] listen` turned a stdio test into a
// server that opened a socket and never answered — gallium stopped reading that
// key (an address is typed on the command line now, nowhere else), but the same
// file can still hand a test an inferenceEngine or skillPaths it did not choose.
// The flag names the server's configuration instead of inheriting whoever's
// machine this is.
func appServerArgs(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gallium.toml")
	if err := os.WriteFile(path, []byte("[agent]\n"), 0o600); err != nil {
		t.Fatalf("writing an empty gallium config: %v", err)
	}
	return []string{appServerArg, configFlag, path}
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
		Args:           appServerArgs(t),
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
		Name:        pingTool,
		Description: "Records a note on the caller's side. Call it with any text.",
		Parameters: []Parameter{
			{Name: pingNoteArg, Type: jsonTypeString, Required: true, Description: "text to record"},
		},
	}}
}

func (c callbackTools) Call(_ context.Context, name string, args map[string]any) (string, error) {
	if name != pingTool {
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
		if args[pingNoteArg] != "from the backend" {
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

// reserveAddress picks a loopback port by binding one and letting it go.
//
// Racy in principle — the port is free the instant it is released, and anything
// could take it — but gallium does not report which port it bound, so the choice
// is this or parsing its output. Loopback ephemeral ports are not reused that
// quickly in practice, and a lost race fails loudly (gallium exits saying it
// cannot bind) rather than quietly.
func reserveAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}
	return address
}

// listenAt starts `gallium app-server` serving on a TCP address instead of
// stdio, and returns the address once it is accepting connections.
//
// This is the deployment the TCP transport exists for, in miniature: a server
// somebody else started, which klein neither spawns nor configures — so the
// script and engine are set *here*, on the server's side, exactly as they would
// be on a GPU box.
func listenAt(t *testing.T, bin, script, workDir string) string {
	t.Helper()
	address := reserveAddress(t)

	args := append([]string{}, appServerArgs(t)...)
	args = append(args, "--listen", address)
	cmd := exec.Command(bin, args...) //nolint:gosec // the binary is GALLIUM_BIN, the tester's own
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"INFERENCE_ENGINE=scripted",
		"MODEL_PATH="+script,
		"GALLIUM_AUTO_APPROVE=1",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a listening gallium: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// It binds within milliseconds or it has already failed; poll rather than
	// sleep so the usual case costs nothing.
	deadline := time.Now().Add(20 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			_ = conn.Close()
			return address
		}
		if time.Now().After(deadline) {
			t.Fatalf("gallium never listened on %s: %s", address, stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The whole point of dialing rather than spawning: the agent runs in a process
// klein did not start (on a GPU box, in the real deployment) while the tools it
// calls back for still run *here*. Both halves have to survive the socket, and
// only a real gallium on a real port can show that they do.
func TestGallium_OverTCP_RunsATurnAndCallsBackForATool(t *testing.T) {
	t.Parallel()
	bin := galliumBin(t)

	script := writeScript(t, `{
	  "steps": [
	    { "toolCalls": [{ "id": "c1", "name": "KleinPing", "arguments": { "note": "over the wire" } }] },
	    { "text": "I called your tool." }
	  ]
	}`)
	workDir := t.TempDir()
	address := listenAt(t, bin, script, workDir)

	logger, drift := driftLog()
	tools := callbackTools{called: make(chan map[string]any, 1)}
	runner, err := NewRunner(context.Background(), Config{
		Address:        address,
		Logger:         logger,
		Dialect:        DialectGeneric,
		Cwd:            workDir,
		ApprovalPolicy: ApprovalNever,
		Tools:          tools,
	})
	if err != nil {
		t.Fatalf("dialing gallium at %s: %v", address, err)
	}
	t.Cleanup(func() { _ = runner.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var got []capturedEvent
	threadID, text, err := runner.RunTurn(ctx, "", "ping yourself", "", recorder{got: &got})
	if err != nil {
		t.Fatalf("running a turn over TCP: %v", err)
	}
	if threadID == "" {
		t.Error("the turn produced no thread id")
	}
	if !strings.Contains(text, "I called your tool") {
		t.Errorf("turn text: got %q", text)
	}

	// The tool ran in this process, with the arguments the remote agent chose.
	select {
	case args := <-tools.called:
		if args[pingNoteArg] != "over the wire" {
			t.Errorf("arguments did not survive the round trip: %+v", args)
		}
	default:
		t.Fatal("the remote agent never called back for the registered tool")
	}

	assertNoDrift(t, drift)
}

// gallium serves one client at a time and lets the newest win, so klein can find
// its connection closed under it — deliberately, so a laptop that slept cannot
// lock its owner out of their own box. What must not follow is a session that
// stays broken: the next turn the user asks for redials and starts a fresh
// thread, because thread ids die with the connection that issued them.
func TestGallium_OverTCP_ReconnectsAfterBeingDisplaced(t *testing.T) {
	t.Parallel()
	bin := galliumBin(t)

	// One step per turn: the scripted engine advances per turn, not per thread,
	// so the turn after the redial consumes the second step.
	script := writeScript(t, `{
	  "steps": [
	    { "text": "first answer" },
	    { "text": "second answer" }
	  ]
	}`)
	workDir := t.TempDir()
	address := listenAt(t, bin, script, workDir)

	runner, err := NewRunner(context.Background(), Config{
		Address:        address,
		Dialect:        DialectGeneric,
		Cwd:            workDir,
		ApprovalPolicy: ApprovalNever,
	})
	if err != nil {
		t.Fatalf("dialing gallium at %s: %v", address, err)
	}
	t.Cleanup(func() { _ = runner.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	first, _, err := runner.RunTurn(ctx, "", "hello", "", discardObserver{})
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}

	// Somebody else connects. gallium hands them the session and closes klein's.
	intruder, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("connecting as a second client: %v", err)
	}
	defer func() { _ = intruder.Close() }()

	link := runner.link
	waitHungUp(t, link)

	// The intruder goes away again, so the next dial is klein's to win.
	_ = intruder.Close()

	// The user asks for another turn on the thread they had. klein redials and
	// starts a new one rather than naming a thread that no longer exists.
	second, text, err := runner.RunTurn(ctx, first, "hello again", "", discardObserver{})
	if err != nil {
		t.Fatalf("turn after being displaced: %v", err)
	}
	if text == "" {
		t.Error("the turn after the reconnect produced no text")
	}
	if !runner.started[second] {
		t.Errorf("thread %q was not recorded as started after the reconnect", second)
	}
	if runner.link == link {
		t.Error("the runner is still holding the connection gallium closed")
	}
}

// hiddenTool is the name of the deferred tool in the deferral tests. Deliberately
// unlike pingTool: gallium matches call names leniently (case and underscores are
// normalized away), so two names that could collapse into one would prove nothing.
const hiddenTool = "KleinHiddenSearch"

// deferringTools offers one advertised tool and one deferred one, recording which
// were called.
type deferringTools struct {
	called chan string
}

func (d deferringTools) Specs() []ToolSpec {
	return []ToolSpec{
		{
			Name:        pingTool,
			Description: "Records a note on the caller's side.",
			Parameters: []Parameter{
				{Name: pingNoteArg, Type: jsonTypeString, Required: true, Description: "text to record"},
			},
		},
		{
			Name:        hiddenTool,
			Description: "Searches an imaginary corpus. Deferred: registered, not advertised.",
			Deferred:    true,
			Parameters: []Parameter{
				{Name: "query", Type: jsonTypeString, Required: true, Description: "what to look for"},
			},
		},
	}
}

func (d deferringTools) Call(_ context.Context, name string, _ map[string]any) (string, error) {
	select {
	case d.called <- name:
	default:
	}
	return "ok", nil
}

// tracedTools spawns gallium with tracing on, so a test can read back the tool
// list gallium actually put in front of the model.
//
// The trace is the only witness that matters here. Every other signal — the tool
// running, the turn succeeding — is equally true of a backend that ignored the
// flag and advertised everything, which is exactly the failure this has to be
// able to see.
func tracedGalliumRunner(
	t *testing.T, bin, script, workDir string, tools DynamicTools,
) (*Runner, string) {
	t.Helper()
	traceDir := t.TempDir()
	logger, _ := driftLog()
	runner, err := NewRunner(context.Background(), Config{
		Command: bin,
		Args:    appServerArgs(t),
		Logger:  logger,
		Env: []string{
			"INFERENCE_ENGINE=scripted", "MODEL_PATH=" + script, "GALLIUM_AUTO_APPROVE=1",
			"GALLIUM_TRACE=1", "GALLIUM_TRACE_DIR=" + traceDir,
		},
		Dialect:        DialectGeneric,
		Cwd:            workDir,
		ApprovalPolicy: ApprovalNever,
		Tools:          tools,
	})
	if err != nil {
		t.Fatalf("spawning gallium: %v", err)
	}
	t.Cleanup(func() { _ = runner.Close() })
	return runner, traceDir
}

// tracedToolNames reads the tool list gallium recorded for the turn.
func tracedToolNames(t *testing.T, traceDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(traceDir)
	if err != nil {
		t.Fatalf("reading the trace dir: %v", err)
	}
	var names []string
	found := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(traceDir, e.Name())) //nolint:gosec // a temp dir this test made
		if readErr != nil {
			t.Fatalf("reading %s: %v", e.Name(), readErr)
		}
		var trace struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(raw, &trace); err != nil {
			t.Fatalf("parsing %s: %v", e.Name(), err)
		}
		found = true
		for _, tl := range trace.Tools {
			names = append(names, tl.Name)
		}
	}
	if !found {
		t.Fatalf("gallium wrote no trace to %s; the test cannot tell what the model saw", traceDir)
	}
	slices.Sort(names)
	return names
}

// The claim klein's `advertised: false` makes: the tool is registered, so the
// backend can route a call to it, but its schema never reaches the model.
//
// Both halves are asserted from the backend's own trace, because only the trace
// separates them. A turn that succeeds proves the tool was registered; nothing
// but the recorded tool list proves it was not also advertised.
func TestGallium_RealAppServer_DeferredToolIsRegisteredButNotAdvertised(t *testing.T) {
	t.Parallel()
	bin := galliumBin(t)

	script := writeScript(t, `{
	  "steps": [
	    { "toolCalls": [{ "id": "c1", "name": "KleinHiddenSearch", "arguments": { "query": "anything" } }] },
	    { "text": "I called the deferred tool." }
	  ]
	}`)
	tools := deferringTools{called: make(chan string, 2)}
	runner, traceDir := tracedGalliumRunner(t, bin, script, t.TempDir(), tools)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, text, err := runner.RunTurn(ctx, "", "use the hidden tool", "", nil)
	if err != nil {
		t.Fatalf("running a turn: %v", err)
	}
	if !strings.Contains(text, "I called the deferred tool") {
		t.Errorf("turn text: got %q", text)
	}

	// Registered: the backend routed a call to a tool it never advertised, and it
	// ran here. Deferral is a context budget, never a permission boundary.
	select {
	case name := <-tools.called:
		if name != hiddenTool {
			t.Errorf("called %q, want %q", name, hiddenTool)
		}
	default:
		t.Fatal("the deferred tool was never called; deferral must not make a tool unreachable")
	}

	// Not advertised: its schema stayed out of the prompt.
	advertised := tracedToolNames(t, traceDir)
	if slices.Contains(advertised, hiddenTool) {
		t.Errorf("%q was advertised to the model; deferral did nothing. Tools: %v", hiddenTool, advertised)
	}
	if !slices.Contains(advertised, pingTool) {
		t.Errorf("%q should still be advertised; got %v", pingTool, advertised)
	}
}

// gallium registers its own ToolSearch when, and only when, something is
// deferred — otherwise a thread that defers nothing would spend a schema
// advertising a search over an empty set.
//
// klein does not send this tool; it is the backend's half of the bargain, and
// the reason klein can defer a tool without stranding it. Asserted here because
// klein's `defer_mcp_tools` is only safe against a backend that offers some way
// back, and this is where the two halves actually meet.
func TestGallium_RealAppServer_ToolSearchAppearsOnlyWhenSomethingIsDeferred(t *testing.T) {
	t.Parallel()
	bin := galliumBin(t)

	script := writeScript(t, `{ "steps": [{ "text": "nothing to do." }] }`)

	withDeferred, deferredTrace := tracedGalliumRunner(
		t, bin, script, t.TempDir(), deferringTools{called: make(chan string, 2)})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, _, err := withDeferred.RunTurn(ctx, "", "hello", "", nil); err != nil {
		t.Fatalf("turn with a deferred tool: %v", err)
	}

	script2 := writeScript(t, `{ "steps": [{ "text": "nothing to do." }] }`)
	noDeferred, plainTrace := tracedGalliumRunner(
		t, bin, script2, t.TempDir(), callbackTools{called: make(chan map[string]any, 1)})
	if _, _, err := noDeferred.RunTurn(ctx, "", "hello", "", nil); err != nil {
		t.Fatalf("turn with nothing deferred: %v", err)
	}

	deferred, plain := tracedToolNames(t, deferredTrace), tracedToolNames(t, plainTrace)
	if !hasToolSearch(deferred) {
		t.Errorf("a thread with a deferred tool got no discovery tool, stranding it: %v", deferred)
	}
	if hasToolSearch(plain) {
		t.Errorf("a thread deferring nothing was charged for a search over an empty set: %v", plain)
	}
}

// hasToolSearch reports whether the backend advertised a tool-discovery tool.
// Matched loosely on purpose: the name is the backend's to choose, and klein
// depends on such a tool existing, not on what it is called.
func hasToolSearch(names []string) bool {
	for _, n := range names {
		if strings.Contains(strings.ToLower(strings.ReplaceAll(n, "_", "")), "toolsearch") {
			return true
		}
	}
	return false
}

// usageObserver records the token accounting a real backend reports.
type usageObserver struct {
	discardObserver
	mu  sync.Mutex
	got []TokenUsage
}

func (o *usageObserver) TokenUsageUpdated(u TokenUsage) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.got = append(o.got, u)
}

func (o *usageObserver) snapshot() []TokenUsage {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]TokenUsage(nil), o.got...)
}

// The gauge klein draws is only as good as its reading of a real notification,
// and the shape is the backend's to define. Asserted against a live gallium
// because the two traps here are both invisible to a unit test with a payload
// this test wrote itself: that `total` is a running sum rather than an occupancy
// figure, and that the window may legitimately be absent.
func TestGallium_RealAppServer_ReportsTokenUsage(t *testing.T) {
	t.Parallel()
	bin := galliumBin(t)

	// Two model calls, so `total` has something to accumulate past `last`.
	script := writeScript(t, `{
	  "steps": [
	    { "toolCalls": [{ "id": "c1", "name": "LS", "arguments": { "path": "." } }], "inputTokens": 100, "outputTokens": 10 },
	    { "text": "done.", "inputTokens": 150, "outputTokens": 20 }
	  ]
	}`)
	obs := &usageObserver{}
	runner, _ := newGalliumRunner(t, bin, script, t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, _, err := runner.RunTurn(ctx, "", "count something", "", obs); err != nil {
		t.Fatalf("running a turn: %v", err)
	}

	got := obs.snapshot()
	if len(got) == 0 {
		t.Fatal("gallium reported no token usage; klein's gauge would never move")
	}

	// The running total is a different number from the last call's prompt, and
	// klein must be reading the one that belongs against a context window. If
	// these are ever equal the test proves nothing, so say so rather than pass.
	last := got[len(got)-1]
	if last.TotalTokens == last.LastInputTokens {
		t.Skip("this backend's total and last input coincide; the distinction is untested here")
	}
	if last.LastInputTokens <= 0 {
		t.Errorf("no prompt size reported, so there is nothing to draw: %+v", last)
	}
	if last.TotalTokens < last.LastInputTokens {
		t.Errorf("total should accumulate past one call's prompt: %+v", last)
	}
}
