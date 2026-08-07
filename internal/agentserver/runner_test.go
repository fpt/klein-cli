package agentserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/pmenglund/codex-sdk-go/rpc"

	"github.com/fpt/klein-cli/pkg/agent/events"
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
	seen   []map[string]any

	once sync.Once
	mu   sync.Mutex

	startDelay time.Duration
}

func newFakeServer(startDelay time.Duration) *fakeServer {
	return &fakeServer{
		startDelay: startDelay,
		out:        make(chan string, 8),
		closed:     make(chan struct{}),
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
		cfg:                Config{Backend: BackendAppServer},
		started:            map[string]bool{"thread_1": true},
		startGraceOverride: grace,
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

	_, _, err := runner.RunTurn(ctx, "thread_1", "hi", "", func(events.EventType, any) {})
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

	if _, _, err := runner.RunTurn(ctx, "thread_1", "hi", "", func(events.EventType, any) {}); err == nil {
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
	if _, _, err := runner.RunTurn(ctx, "thread_1", "hi", "", func(events.EventType, any) {}); err == nil {
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
