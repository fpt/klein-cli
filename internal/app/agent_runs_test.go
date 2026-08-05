package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

const testRunID = "agent-1"

func TestSyncBuffer_ConcurrentWriteAndRead(t *testing.T) {
	t.Parallel()

	// The run goroutine writes the transcript while AgentOutput reads it; the
	// race detector is the point of this test.
	b := &syncBuffer{}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 200 {
			_, _ = b.Write([]byte("x"))
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			_ = b.String()
		}
	}()
	wg.Wait()

	if got := len(b.String()); got != 200 {
		t.Errorf("wrote 200 bytes, buffer holds %d", got)
	}
}

func TestAgentRunRegistry_IDsAreUniqueAndReadable(t *testing.T) {
	t.Parallel()

	r := newAgentRunRegistry()
	seen := map[string]bool{}
	for range 5 {
		id := r.nextID()
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		if !strings.HasPrefix(id, "agent-") {
			t.Errorf("id %q should be readable enough to pass back in a later turn", id)
		}
		seen[id] = true
	}
}

// newTestRun registers a run the tests can drive without a model.
func newTestRun(r *agentRunRegistry, id string, status RunStatus) *agentRun {
	ctx, cancel := context.WithCancel(context.Background())
	run := &agentRun{
		id: id, label: "test", task: "a task",
		started: time.Now(), status: status,
		transcript: &syncBuffer{}, cancel: cancel, done: make(chan struct{}),
	}
	if status != RunRunning {
		run.ended = time.Now()
		close(run.done)
	}
	_ = ctx
	r.add(run)
	return run
}

func TestAgentRunRegistry_ListIsOrderedByStart(t *testing.T) {
	t.Parallel()

	r := newAgentRunRegistry()
	first := newTestRun(r, testRunID, RunCompleted)
	first.started = time.Now().Add(-time.Minute)
	newTestRun(r, "agent-2", RunRunning)

	list := r.list()
	if len(list) != 2 || list[0].ID != testRunID || list[1].ID != "agent-2" {
		t.Errorf("list = %+v, want oldest first", list)
	}
}

func TestAgentRunRegistry_CancelAllOnlyTouchesRunning(t *testing.T) {
	t.Parallel()

	r := newAgentRunRegistry()
	newTestRun(r, testRunID, RunCompleted)
	newTestRun(r, "agent-2", RunRunning)
	newTestRun(r, "agent-3", RunRunning)

	if n := r.CancelAll(); n != 2 {
		t.Errorf("CancelAll canceled %d, want the 2 running ones", n)
	}
}

// Concurrent AgentStop and completion is the case the original tests missed:
// they only ever stopped runs whose status nothing else was writing. Under
// -race this fails if status is read without the run's own lock.
func TestStopAgentRun_RacesWithCompletion(t *testing.T) {
	t.Parallel()

	for range 50 {
		r := newAgentRunRegistry()
		agent := &Agent{agentRuns: r}
		run := newTestRun(r, testRunID, RunRunning)

		var wg sync.WaitGroup
		wg.Add(2)
		// A worker finishing at the same moment the user stops it.
		go func() {
			defer wg.Done()
			run.finish(RunCompleted, "done", "")
			close(run.done)
		}()
		go func() {
			defer wg.Done()
			if _, err := agent.StopAgentRun(testRunID); err != nil {
				t.Errorf("StopAgentRun: %v", err)
			}
		}()
		wg.Wait()

		// Whichever won, the reported status must be a real terminal one —
		// never "stopped" for a run that completed.
		if got := run.getStatus(); got != RunCompleted {
			t.Fatalf("status = %q, want the recorded outcome to survive", got)
		}
	}
}

// AgentOutput must not block a run from finishing: it reads the transcript
// without holding a lock the worker needs.
func TestAgentRunOutput_DoesNotBlockCompletion(t *testing.T) {
	t.Parallel()

	r := newAgentRunRegistry()
	agent := &Agent{agentRuns: r}
	run := newTestRun(r, testRunID, RunRunning)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 100 {
			_, _ = run.transcript.Write([]byte("progress "))
			run.finish(RunRunning, "", "")
		}
		run.finish(RunCompleted, "final", "")
		close(run.done)
	}()
	go func() {
		defer wg.Done()
		for range 100 {
			if _, err := agent.AgentRunOutput(context.Background(), testRunID, false, 0); err != nil {
				t.Errorf("AgentRunOutput: %v", err)
			}
		}
	}()
	wg.Wait()

	out, err := agent.AgentRunOutput(context.Background(), testRunID, true, time.Second)
	if err != nil {
		t.Fatalf("AgentRunOutput: %v", err)
	}
	if out.Result != "final" {
		t.Errorf("result = %q, want the completed result", out.Result)
	}
}

func TestStopAgentRun(t *testing.T) {
	t.Parallel()

	a := &Agent{agentRuns: newAgentRunRegistry()}

	t.Run("unknown id is an error", func(t *testing.T) {
		t.Parallel()

		if _, err := a.StopAgentRun("nope"); err == nil {
			t.Error("expected an error for an unknown id")
		}
	})

	t.Run("already finished is not an error", func(t *testing.T) {
		t.Parallel()

		r := newAgentRunRegistry()
		agent := &Agent{agentRuns: r}
		newTestRun(r, "agent-done", RunCompleted)

		msg, err := agent.StopAgentRun("agent-done")
		if err != nil {
			t.Fatalf("stopping a finished run should not error: %v", err)
		}
		if !strings.Contains(msg, "already") {
			t.Errorf("message %q should say it had already finished", msg)
		}
	})
}

// AgentOutput with block=true must return when the run finishes rather than
// waiting out the full timeout.
func TestAgentRunOutput_BlockUnblocksOnCompletion(t *testing.T) {
	t.Parallel()

	r := newAgentRunRegistry()
	a := &Agent{agentRuns: r}
	run := newTestRun(r, testRunID, RunRunning)

	go func() {
		time.Sleep(20 * time.Millisecond)
		r.mu.Lock()
		run.status, run.result, run.ended = RunCompleted, "the answer", time.Now()
		r.mu.Unlock()
		close(run.done)
	}()

	start := time.Now()
	out, err := a.AgentRunOutput(context.Background(), testRunID, true, 5*time.Second)
	if err != nil {
		t.Fatalf("AgentRunOutput: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("blocked for %s; should return as soon as the run finishes", took)
	}
	if out.Status != string(RunCompleted) || out.Result != "the answer" {
		t.Errorf("out = %+v, want the completed result", out)
	}
}

func TestAgentRunOutput_NonBlockingReturnsPartialTranscript(t *testing.T) {
	t.Parallel()

	r := newAgentRunRegistry()
	a := &Agent{agentRuns: r}
	run := newTestRun(r, testRunID, RunRunning)
	_, _ = run.transcript.Write([]byte("partial progress"))

	out, err := a.AgentRunOutput(context.Background(), testRunID, false, 0)
	if err != nil {
		t.Fatalf("AgentRunOutput: %v", err)
	}
	if out.Status != string(RunRunning) {
		t.Errorf("status = %q, want running", out.Status)
	}
	if !strings.Contains(out.Transcript, "partial progress") {
		t.Errorf("transcript = %q, want the progress so far", out.Transcript)
	}
}

func TestAgentRunOutput_UnknownID(t *testing.T) {
	t.Parallel()

	a := &Agent{agentRuns: newAgentRunRegistry()}
	if _, err := a.AgentRunOutput(context.Background(), "nope", false, 0); err == nil {
		t.Error("expected an error for an unknown id")
	}
}

// The launch message must not read as though a result exists.
func TestFormatBackgroundLaunch(t *testing.T) {
	t.Parallel()

	msg := formatBackgroundLaunch(RunInfo{
		ID: testRunID, Label: "pr-watcher", OutputPath: "/tmp/agent-1.md",
	})
	for _, want := range []string{testRunID, "pr-watcher", "No result yet", "AgentOutput", "/tmp/agent-1.md"} {
		if !strings.Contains(msg, want) {
			t.Errorf("launch message should mention %q, got:\n%s", want, msg)
		}
	}
}
