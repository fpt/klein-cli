package agentserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/pmenglund/codex-sdk-go/rpc"
)

// A turn klein never learned the id of cannot be interrupted, and finding that
// out must not reach for the client: this runs on the error path, where the
// connection may be exactly what failed. The nil client makes the check
// load-bearing — if interruptTurn called through, this panics.
func TestInterruptTurn_WithoutTurnID_DoesNotCallTheBackend(t *testing.T) {
	t.Parallel()

	r := &Runner{}
	r.interruptTurn(context.Background(), "thread_1", "")
}

// fakeServer is an app-server on the other end of a Transport, in-process: it
// answers turn/start after startDelay and records every request it received.
//
// The delay is the point. Cancellation between sending turn/start and reading
// its response is a race no real backend can be asked to reproduce on demand,
// and it is the window where klein has a turn running with no id to stop it.
type fakeServer struct {
	out    chan string
	closed chan struct{}
	// threadStartResult is the raw JSON answered to thread/start, so a test can
	// choose the response shape.
	threadStartResult string
	// threadForkResult is the raw JSON answered to thread/fork.
	threadForkResult string
	seen             []map[string]any

	once sync.Once
	mu   sync.Mutex

	startDelay time.Duration
}

func newFakeServer(startDelay time.Duration) *fakeServer {
	return &fakeServer{
		startDelay:        startDelay,
		threadStartResult: `{"thread":{"id":"thread_new"}}`,
		threadForkResult:  `{"thread":{"id":"thread_fork"}}`,
		out:               make(chan string, 8),
		closed:            make(chan struct{}),
	}
}

func (f *fakeServer) ReadLine() (string, error) {
	select {
	case line := <-f.out:
		return line, nil
	case <-f.closed:
		return "", io.EOF
	}
}

func (f *fakeServer) WriteLine(line string) error {
	var req struct {
		Params map[string]any  `json:"params"`
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
	}
	// A notification, or something this fake does not model: nothing to answer.
	decoded := json.Unmarshal([]byte(line), &req) == nil
	if !decoded || req.Method == "" {
		return nil
	}

	f.mu.Lock()
	f.seen = append(f.seen, map[string]any{"method": req.Method, "params": req.Params})
	f.mu.Unlock()

	switch req.Method {
	case "thread/start":
		f.reply(req.ID, f.threadStartResult)
	case "thread/fork":
		f.reply(req.ID, f.threadForkResult)
	case "turn/start":
		go func() {
			select {
			case <-time.After(f.startDelay):
			case <-f.closed:
				return
			}
			f.reply(req.ID, `{"turn":{"id":"turn_1","status":"inProgress"}}`)
		}()
	case "turn/interrupt":
		f.reply(req.ID, `{}`)
	case "turn/steer":
		f.reply(req.ID, `{"turnId":"turn_1"}`)
	default:
		f.reply(req.ID, `{}`)
	}
	return nil
}

func (f *fakeServer) reply(id json.RawMessage, result string) {
	select {
	case f.out <- fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, id, result):
	case <-f.closed:
	}
}

func (f *fakeServer) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

// requests returns the methods the server was asked for, in order.
func (f *fakeServer) requests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.seen...)
}

// runnerOn wires a Runner to a fake server, skipping NewRunner's handshake: the
// thread is pre-marked as started so RunTurn goes straight to the turn. grace
// stands in for startTurnGrace so a test does not have to wait out the real one.
func runnerOn(t *testing.T, server *fakeServer, grace time.Duration) *Runner {
	t.Helper()
	client := rpc.NewClient(server, rpc.ClientOptions{})
	t.Cleanup(func() { _ = client.Close() })
	return &Runner{
		client:             client,
		cfg:                Config{Dialect: DialectGeneric},
		started:            map[string]bool{"thread_1": true},
		startGraceOverride: grace,
	}
}

// codex's ThreadStartResponse carries the new thread's id inside the thread
// object, and that is the only spelling klein reads.
func TestStartThread_ReadsTheIDFromTheThreadObject(t *testing.T) {
	t.Parallel()

	runner := runnerOn(t, newFakeServer(0), time.Second)

	id, err := runner.startThread(context.Background(), "")
	if err != nil {
		t.Fatalf("startThread: %v", err)
	}
	if id != "thread_new" {
		t.Errorf("thread id = %q, want thread_new", id)
	}
}

// A fork continues a thread another process ran: it names the source, asks for
// no history back, sends no dynamic tools (thread/fork takes none; the fork
// keeps the source's), and its id is one RunTurn continues rather than replaces.
func TestForkThread_ContinuesTheSource(t *testing.T) {
	t.Parallel()

	server := newFakeServer(0)
	runner := runnerOn(t, server, time.Second)
	runner.cfg.Model = "m"
	runner.cfg.MCPServers = map[string]any{"docs": map[string]any{"command": "docs"}}
	runner.dynTools = []map[string]any{{"name": "t"}}

	id, err := runner.ForkThread(context.Background(), "thread_old", "be brief")
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	if id != "thread_fork" || !runner.started[id] {
		t.Fatalf("fork id = %q, started = %v", id, runner.started)
	}
	reqs := server.requests()
	if len(reqs) != 1 || reqs[0]["method"] != "thread/fork" {
		t.Fatalf("requests = %v", reqs)
	}
	p, _ := reqs[0]["params"].(map[string]any)
	want := map[string]any{
		"threadId": "thread_old", "excludeTurns": true, "developerInstructions": "be brief", "model": "m",
	}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("thread/fork %s = %v, want %v", k, p[k], v)
		}
	}
	if p["config"] == nil {
		t.Errorf("thread/fork was sent no MCP servers: %v", p)
	}
	if _, ok := p["dynamicTools"]; ok {
		t.Errorf("thread/fork was sent dynamicTools: %v", p)
	}
}

// Nothing to continue is an error, not a fresh thread under the old name: the
// caller decides to start over.
func TestForkThread_WithoutASourceOrAnID_Fails(t *testing.T) {
	t.Parallel()

	server := newFakeServer(0)
	runner := runnerOn(t, server, time.Second)
	if _, err := runner.ForkThread(context.Background(), "", ""); err == nil {
		t.Error("want an error for an empty source thread")
	}
	if len(server.requests()) != 0 {
		t.Errorf("an empty source reached the server: %v", server.requests())
	}
	server.threadForkResult = `{}`
	if _, err := runner.ForkThread(context.Background(), "thread_old", ""); err == nil {
		t.Error("want an error when thread/fork returns no id")
	}
	if len(runner.started) != 1 { // only runnerOn's thread_1
		t.Errorf("started = %v", runner.started)
	}
}

// A response carrying no id has to fail here. Passing an empty thread id on to
// turn/start would fail later and less clearly.
func TestStartThread_WithoutAnID_Fails(t *testing.T) {
	t.Parallel()

	server := newFakeServer(0)
	server.threadStartResult = `{}`

	if _, err := runnerOn(t, server, time.Second).startThread(context.Background(), ""); err == nil {
		t.Fatal("want an error when thread/start returns no id")
	}
}

// waitForInterrupt returns the params of the first turn/interrupt the server
// saw, waiting up to timeout for one that has not arrived yet.
func waitForInterrupt(t *testing.T, server *fakeServer, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, req := range server.requests() {
			if req["method"] == "turn/interrupt" {
				params, _ := req["params"].(map[string]any)
				return params
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the turn was left running: %+v", server.requests())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Canceling while turn/start is in flight is the one window where klein has a
// turn running and no id for it. Walking away there leaks the thread's turn slot
// exactly as an uninterrupted turn would, so the id is worth waiting for.
func TestRunTurn_CanceledDuringTurnStart_StillInterrupts(t *testing.T) {
	t.Parallel()

	// Answers within the grace, so runTurn itself does the interrupting.
	server := newFakeServer(300 * time.Millisecond)
	runner := runnerOn(t, server, 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, _, err := runner.RunTurn(ctx, "thread_1", "hi", "", discardObserver{})
	if err == nil {
		t.Fatal("a canceled turn should return an error")
	}

	interrupted := waitForInterrupt(t, server, time.Second)
	if interrupted[keyTurnID] != testTurn || interrupted[keyThreadID] != "thread_1" {
		t.Errorf("interrupted the wrong turn: %+v", interrupted)
	}
}

// A context already canceled when RunTurn is called must not start a turn at
// all — there is nobody left to read it.
func TestRunTurn_AlreadyCanceled_StartsNothing(t *testing.T) {
	t.Parallel()

	server := newFakeServer(0)
	runner := runnerOn(t, server, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := runner.RunTurn(ctx, "thread_1", "hi", "", discardObserver{}); err == nil {
		t.Fatal("want an error for an already-canceled context")
	}
	for _, req := range server.requests() {
		if req["method"] == "turn/start" {
			t.Errorf("started a turn for a canceled context: %+v", server.requests())
		}
	}
}

// The acknowledgement can also arrive after klein has stopped waiting for it. By
// then runTurn has returned and cannot interrupt a turn it never learned the
// name of, so whoever finally reads the id has to stop the turn itself —
// otherwise it runs on, holding the thread's slot, which is the leak this whole
// path exists to close.
func TestRunTurn_TurnStartAnswersAfterTheGrace_InterruptsAnyway(t *testing.T) {
	t.Parallel()

	// The answer lands well after the grace has expired.
	server := newFakeServer(400 * time.Millisecond)
	runner := runnerOn(t, server, 50*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	started := time.Now()
	if _, _, err := runner.RunTurn(ctx, "thread_1", "hi", "", discardObserver{}); err == nil {
		t.Fatal("a canceled turn should return an error")
	}
	// The caller is not made to wait for the late answer.
	if waited := time.Since(started); waited > 300*time.Millisecond {
		t.Errorf("RunTurn waited %v for a response it had given up on", waited)
	}

	interrupted := waitForInterrupt(t, server, 2*time.Second)
	if interrupted[keyTurnID] != testTurn {
		t.Errorf("interrupted the wrong turn: %+v", interrupted)
	}
}

// TestProbeReadySkipsGenericDialect confirms the codex account probe never runs
// for a generic server. `account/read` is outside the protocol subset the client
// requires, so probing it would reject a conforming server that has no account
// concept at all. A nil client is the assertion: reaching the RPC call would panic.
func TestProbeReadySkipsGenericDialect(t *testing.T) {
	t.Parallel()
	if err := probeReady(t.Context(), nil, DialectGeneric); err != nil {
		t.Errorf("probeReady(DialectGeneric) = %v, want nil (no probe)", err)
	}
}

// The zero value has to be the harmless one: a caller that never heard of
// Dialect must not have its server probed for an account it does not have.
func TestDialectZeroValueIsGeneric(t *testing.T) {
	t.Parallel()
	var unset Dialect
	if unset != DialectGeneric {
		t.Errorf("the zero Dialect is %v, want DialectGeneric", unset)
	}
}

// A fork point names the turn to cut at, by the field codex reads for that
// side of it, and only that field.
func TestForkThread_AtATurn(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		opt        ForkOption
		sent, left string
	}{
		{ForkThrough("turn_3"), "lastTurnId", "beforeTurnId"},
		{ForkBefore("turn_3"), "beforeTurnId", "lastTurnId"},
	} {
		server := newFakeServer(0)
		runner := runnerOn(t, server, time.Second)
		if _, err := runner.ForkThread(context.Background(), "thread_old", "", tc.opt); err != nil {
			t.Fatalf("ForkThread: %v", err)
		}
		p, _ := server.requests()[0]["params"].(map[string]any)
		if p[tc.sent] != "turn_3" {
			t.Errorf("thread/fork %s = %v, want turn_3", tc.sent, p[tc.sent])
		}
		if _, ok := p[tc.left]; ok {
			t.Errorf("thread/fork was sent %s too: %v", tc.left, p)
		}
	}
}

// A fork point that cannot be honored fails before reaching the server, rather
// than quietly forking the whole history: an empty id is a caller that lost the
// turn it meant.
func TestForkThread_AtAnUnusableTurn_Fails(t *testing.T) {
	t.Parallel()

	for name, opts := range map[string][]ForkOption{
		"both":          {ForkThrough("turn_1"), ForkBefore("turn_2")},
		"empty through": {ForkThrough("")},
		"empty before":  {ForkBefore("")},
	} {
		server := newFakeServer(0)
		runner := runnerOn(t, server, time.Second)
		if _, err := runner.ForkThread(context.Background(), "thread_old", "", opts...); err == nil {
			t.Errorf("%s: want an error", name)
		}
		if len(server.requests()) != 0 {
			t.Errorf("%s: reached the server: %v", name, server.requests())
		}
	}
}

type turnRecorder struct {
	discardObserver
	mu    sync.Mutex
	turns [][2]string
}

func (o *turnRecorder) TurnStarted(threadID, turnID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.turns = append(o.turns, [2]string{threadID, turnID})
}

// A TurnObserver is told each turn's id once the backend accepts it — the id a
// later ForkThrough or ForkBefore takes.
func TestRunTurn_ReportsTheTurnID(t *testing.T) {
	t.Parallel()

	server := newFakeServer(0)
	runner := runnerOn(t, server, time.Second)
	obs := &turnRecorder{}

	// The fake never completes a turn, so the deadline ends it once started.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, _ = runner.RunTurn(ctx, "thread_1", "hi", "", obs)

	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.turns) != 1 || obs.turns[0] != [2]string{"thread_1", testTurn} {
		t.Errorf("turns reported = %v", obs.turns)
	}
}

// signalingObserver closes started when the turn begins, so a test can act
// during it.
type signalingObserver struct {
	discardObserver
	started chan struct{}
}

func (o *signalingObserver) TurnStarted(string, string) { close(o.started) }

// Input steered into a running turn goes to that turn, by the id codex checks it
// against — and once the turn is over, there is nothing left to steer.
func TestSteerTurn_ReachesTheRunningTurn(t *testing.T) {
	t.Parallel()

	server := newFakeServer(0)
	runner := runnerOn(t, server, time.Second)
	obs := &signalingObserver{started: make(chan struct{})}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = runner.RunTurn(ctx, "thread_1", "hi", "", obs)
	}()
	select {
	case <-obs.started:
	case <-time.After(time.Second):
		t.Fatal("the turn never started")
	}

	if err := runner.SteerTurn(context.Background(), "thread_1", "also this"); err != nil {
		t.Fatalf("SteerTurn: %v", err)
	}
	var steer map[string]any
	for _, req := range server.requests() {
		if req["method"] == "turn/steer" {
			steer, _ = req["params"].(map[string]any)
		}
	}
	if steer == nil {
		t.Fatalf("no turn/steer sent: %v", server.requests())
	}
	if steer[keyThreadID] != "thread_1" || steer["expectedTurnId"] != testTurn {
		t.Errorf("turn/steer params = %v", steer)
	}
	input, _ := steer["input"].([]any)
	if len(input) != 1 || input[0].(map[string]any)[keyText] != "also this" {
		t.Errorf("turn/steer input = %v", steer["input"])
	}

	cancel()
	<-done
	if err := runner.SteerTurn(context.Background(), "thread_1", "late"); !errors.Is(err, ErrNoActiveTurn) {
		t.Errorf("steer after the turn ended: err = %v, want ErrNoActiveTurn", err)
	}
}

// With no turn running on the thread — none at all, or one on another thread —
// SteerTurn says so without asking the server.
func TestSteerTurn_WithoutARunningTurn(t *testing.T) {
	t.Parallel()

	server := newFakeServer(0)
	runner := runnerOn(t, server, time.Second)
	if err := runner.SteerTurn(context.Background(), "thread_1", "x"); !errors.Is(err, ErrNoActiveTurn) {
		t.Errorf("idle runner: err = %v, want ErrNoActiveTurn", err)
	}
	runner.active = &activeTurn{client: runner.client, threadID: "thread_2", turnID: "turn_9"}
	if err := runner.SteerTurn(context.Background(), "thread_1", "x"); !errors.Is(err, ErrNoActiveTurn) {
		t.Errorf("other thread's turn: err = %v, want ErrNoActiveTurn", err)
	}
	if len(server.requests()) != 0 {
		t.Errorf("reached the server: %v", server.requests())
	}
}

// steeringObserver steers from inside TurnStarted, the earliest a caller can.
type steeringObserver struct {
	discardObserver
	runner *Runner
	err    chan error
}

func (o *steeringObserver) TurnStarted(threadID, _ string) {
	o.err <- o.runner.SteerTurn(context.Background(), threadID, "right away")
}

// A turn is steerable by the time its observer is told it started.
func TestSteerTurn_FromTurnStarted(t *testing.T) {
	t.Parallel()

	server := newFakeServer(0)
	runner := runnerOn(t, server, time.Second)
	obs := &steeringObserver{runner: runner, err: make(chan error, 1)}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, _ = runner.RunTurn(ctx, "thread_1", "hi", "", obs)

	select {
	case err := <-obs.err:
		if err != nil {
			t.Errorf("steer from TurnStarted: %v", err)
		}
	default:
		t.Fatal("TurnStarted was never called")
	}
}
