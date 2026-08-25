package agentserver

// Concurrency tests for shutdown. They exist because the rest of the package has
// none: every other test drives a Runner from one goroutine, so -race never sees
// the one field two goroutines actually share — the client handle, which a turn
// reads and both Close and a mid-turn redial write.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// blockingTools holds a turn open. The backend calls back for a tool mid-turn,
// this refuses to answer until released, and the Runner is holding mu the whole
// time — which is the window a shutdown has to survive.
type blockingTools struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingTools) Specs() []ToolSpec {
	return []ToolSpec{{Name: pingTool, Description: "blocks until released"}}
}

func (b *blockingTools) Call(_ context.Context, name string, _ map[string]any) (string, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return "ran " + name, nil
}

// turnInFlight starts a turn and returns once the backend has called back into
// it, so the caller knows the Runner is mid-turn and holding mu. release ends
// the turn; turnErr carries how it finished.
func turnInFlight(t *testing.T) (runner *Runner, srv *jsonlServer, release func(), turnErr <-chan error) {
	t.Helper()

	srv = newJSONLServer(t)
	srv.toolCall = &toolCallScript{
		tool:      pingTool,
		arguments: map[string]any{},
		answered:  make(chan string, 1),
	}
	tools := &blockingTools{entered: make(chan struct{}), release: make(chan struct{})}
	runner = runnerDialing(t, srv, tools)

	errc := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _, err := runner.RunTurn(ctx, "", "hello", "", nil)
		errc <- err
	}()

	select {
	case <-tools.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the backend never called back, so no turn was ever in flight")
	}
	return runner, srv, sync.OnceFunc(func() { close(tools.release) }), errc
}

// Close reads the client handle that RunTurn's redial replaces, so shutting
// klein down during a turn raced: Close could read the handle between the nil
// and the reassignment, or read one being replaced underneath it. Nothing in the
// package ran two goroutines against a Runner, so -race never looked.
//
// This is that test. Under -race it fails on the unguarded read; without it, it
// still pins the property the fix turns on — Close does not wait for the turn.
func TestRunner_CloseDuringATurnDoesNotWaitForIt(t *testing.T) {
	t.Parallel()

	runner, _, release, turnErr := turnInFlight(t)
	defer release()

	// The turn is holding mu and will keep holding it until release. A Close
	// that took mu — the obvious fix, and the wrong one — would block here until
	// the deferred release, which never runs, because it is this line that
	// returns first.
	closed := make(chan error, 1)
	go func() { closed <- runner.Close() }()

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close during a turn: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited for the turn it was stopping")
	}

	// And the turn ends rather than hanging: its connection is gone, which is
	// what shutting down means.
	release()
	select {
	case <-turnErr:
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never finished after its connection was closed")
	}
}

// Two Closes and a turn at once, which is what a signal handler racing a
// deferred cleanup looks like. Close has to be idempotent: before this, all four
// callers read the same handle and closed it, so three of them came back with
// "use of closed network connection" from a shutdown that had gone fine.
func TestRunner_ConcurrentClosesAreSafe(t *testing.T) {
	t.Parallel()

	runner, _, release, turnErr := turnInFlight(t)
	defer release()

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := runner.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
	}
	wg.Wait()

	release()
	select {
	case <-turnErr:
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never finished")
	}
}

// A turn still in flight when Close lands must not redial its way back to a live
// server: the Runner was shut down, and a connection opened after that has
// nobody to close it. The turn fails instead, saying why.
func TestRunner_AClosedRunnerDoesNotReconnect(t *testing.T) {
	t.Parallel()

	srv := newJSONLServer(t)
	runner := runnerDialing(t, srv, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := runner.RunTurn(ctx, "", "hello", "", nil); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if err := runner.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, _, err := runner.RunTurn(ctx, "", "again", "", nil)
	if err == nil {
		t.Fatal("a closed runner ran a turn, which means it reconnected")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Errorf("the refusal does not say the runner was closed: %v", err)
	}
	// Not merely refused after the fact: it never dialed. A second initialize
	// would be a connection opened and then thrown away.
	if got := countInitialize(srv); got != 1 {
		t.Errorf("the server saw %d initializes, want 1", got)
	}
}

func countInitialize(srv *jsonlServer) int {
	n := 0
	for _, method := range srv.methods() {
		if method == "initialize" {
			n++
		}
	}
	return n
}

// The race the issue is actually about, and the one no existing test could see:
// Close reads the client handle while a mid-turn redial is replacing it.
// ensureConnected clears the handle and reassigns it with a full dial and two
// round trips in between, so a Close landing in that window read either a nil or
// a client being swapped underneath it.
//
// The window is real but narrow, hence the repetition — and it is -race that
// makes this test worth having: without the detector both goroutines are likely
// to survive the read either way. Under it, the unguarded read is reported the
// first time the two overlap.
func TestRunner_CloseDuringARedialIsRaceFree(t *testing.T) {
	t.Parallel()

	for attempt := range 5 {
		srv := newJSONLServer(t)
		runner := runnerDialing(t, srv, nil)

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if _, _, err := runner.RunTurn(ctx, "", "hello", "", nil); err != nil {
			cancel()
			t.Fatalf("attempt %d, first turn: %v", attempt, err)
		}

		// Take the connection away, so the next turn has to redial — which is
		// what puts a write to the handle in flight for Close to collide with.
		link := runner.link
		displace(t, srv)
		waitHungUp(t, link)

		var wg sync.WaitGroup
		wg.Go(func() {
			// Whether this turn succeeds is not the point: it races a shutdown,
			// so either outcome is correct. What must not happen is an
			// unsynchronized read, or a panic on a handle that changed.
			_, _, _ = runner.RunTurn(ctx, "", "again", "", nil)
		})
		wg.Go(func() { _ = runner.Close() })
		wg.Wait()
		cancel()
	}
}
