package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fpt/klein-cli/internal/skill"
	"github.com/fpt/klein-cli/internal/tool"
)

// RunStatus is where a background agent got to.
type RunStatus string

// Background agent lifecycle states.
const (
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunKilled    RunStatus = "killed"
)

// agentRun is one detached subagent. Its transcript is held in memory so it can
// be read back without touching the filesystem (one-shot mode has no project
// directory), and mirrored to a file when there is somewhere to put it.
type agentRun struct {
	transcript *syncBuffer
	cancel     context.CancelFunc
	done       chan struct{}

	// A time.Time carries its pointer in the middle of its 24 bytes, so the
	// two of them go before the strings: ending the struct on a string keeps
	// the last pointer 8 bytes earlier (govet fieldalignment).
	started time.Time // immutable
	ended   time.Time // guarded by mu

	// Immutable after construction; readable without synchronization.
	id         string
	label      string
	task       string
	outputPath string

	// Guarded by mu.
	result  string
	errText string
	status  RunStatus
	// notified records that this run's outcome has been handed to the
	// conversation. Set inside the same critical section that reads the
	// outcome, so a run that is stopped and then completes cannot report
	// twice.
	notified bool

	// mu guards ended, result, errText, and status. It is the run's own lock, not the
	// registry's: the registry lock protects the map, and a finishing run must
	// not have to take it. StopAgentRun waits on done while a worker completes,
	// and if that completion needed the registry lock the two would deadlock.
	//
	// Lock order is registry.mu -> run.mu, never the reverse. syncBuffer.mu is a
	// leaf and is never held while acquiring either.
	//
	// Declared last so the pointerless mutex does not sit between pointer
	// fields (govet fieldalignment).
	mu sync.Mutex
}

// finish records a run's outcome. Takes only the run's own lock.
func (a *agentRun) finish(status RunStatus, result, errText string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status, a.result, a.errText, a.ended = status, result, errText, time.Now()
}

// takeNotification returns this run's outcome exactly once, and only after it
// has finished. The check and the flag are set under one lock so two drains
// racing cannot both claim it.
func (a *agentRun) takeNotification() (RunNotification, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.notified || a.status == RunRunning {
		return RunNotification{}, false
	}
	a.notified = true
	return RunNotification{
		ID: a.id, Label: a.label, Task: a.task, Status: a.status,
		Result: a.result, Error: a.errText, OutputPath: a.outputPath,
		Elapsed: a.ended.Sub(a.started).Round(time.Second),
	}, true
}

// snapshot returns a consistent copy of the mutable fields.
func (a *agentRun) snapshot() (status RunStatus, result, errText string, ended time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status, a.result, a.errText, a.ended
}

// RunNotification is a finished run's outcome, delivered into the conversation
// once.
type RunNotification struct {
	ID         string
	Label      string
	Task       string
	Result     string
	Error      string
	OutputPath string
	Status     RunStatus
	Elapsed    time.Duration
}

// RunInfo is a snapshot of a background agent for the listing tools.
type RunInfo struct {
	ID         string
	Label      string
	Task       string
	OutputPath string
	Started    time.Time
	Ended      time.Time
	Status     RunStatus
}

// syncBuffer is an io.Writer safe for the run goroutine to write while a tool
// call reads it.
type syncBuffer struct {
	f   *os.File
	buf bytes.Buffer
	mu  sync.Mutex
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.f != nil {
		// Best-effort mirror: a failed write to the transcript file must not
		// take down the run that is producing it.
		_, _ = b.f.Write(p)
	}
	n, err := b.buf.Write(p)
	if err != nil {
		return n, fmt.Errorf("writing agent transcript: %w", err)
	}
	return n, nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.f != nil {
		_ = b.f.Close()
		b.f = nil
	}
}

// agentRunRegistry tracks detached subagents for the lifetime of the process.
//
// Runs are deliberately not tied to the conversation: /clear wipes the message
// history, not work already in flight, and a run started before a clear is
// still the answer to something the user asked for. They do not outlive the
// process — CancelAll is called on shutdown so a killed REPL does not leave
// orphaned model calls billing in the background.
type agentRunRegistry struct {
	runs map[string]*agentRun
	seq  int
	mu   sync.Mutex
}

func newAgentRunRegistry() *agentRunRegistry {
	return &agentRunRegistry{runs: make(map[string]*agentRun)}
}

// nextID returns a short, readable, collision-free id: agent-1, agent-2, …
// Readable matters because the model has to pass it back in a later turn.
func (r *agentRunRegistry) nextID() string {
	r.seq++
	return fmt.Sprintf("agent-%d", r.seq)
}

func (r *agentRunRegistry) add(run *agentRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[run.id] = run
}

func (r *agentRunRegistry) get(id string) (*agentRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[id]
	return run, ok
}

// list returns a snapshot ordered by start time, newest last.
func (r *agentRunRegistry) list() []RunInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RunInfo, 0, len(r.runs))
	for _, run := range r.runs {
		out = append(out, run.info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// CancelAll stops every still-running agent. Called on shutdown.
func (r *agentRunRegistry) CancelAll() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, run := range r.runs {
		if run.getStatus() == RunRunning {
			run.cancel()
			n++
		}
	}
	return n
}

func (a *agentRun) info() RunInfo {
	status, _, _, ended := a.snapshot()
	return RunInfo{
		ID: a.id, Label: a.label, Task: a.task, Status: status,
		OutputPath: a.outputPath, Started: a.started, Ended: ended,
	}
}

func (a *agentRun) getStatus() RunStatus {
	status, _, _, _ := a.snapshot()
	return status
}

// StartBackgroundAgent launches def as a detached subagent and returns its run
// id immediately.
//
// The run does NOT inherit the caller's context. That context belongs to the
// turn that started it and is canceled the moment the turn ends (or the user
// hits Ctrl+C), which would kill the background agent instantly — the opposite
// of backgrounding. It gets its own cancellable root instead, held in the
// registry so shutdown and AgentStop can reach it.
func (a *Agent) StartBackgroundAgent(def *skill.Definition, task string) (RunInfo, error) {
	if def == nil {
		return RunInfo{}, errors.New("background agent: nil definition")
	}
	if !def.Permits(skill.ModeSubagent) {
		return RunInfo{}, fmt.Errorf("%q cannot run as a subagent (it permits: %s)",
			def.Name, strings.Join(def.ModeNames(), ", "))
	}

	a.agentRuns.mu.Lock()
	id := a.agentRuns.nextID()
	a.agentRuns.mu.Unlock()

	label := def.Name
	if def.PluginName != "" {
		label = def.PluginName + ":" + def.Name
	}

	transcript := &syncBuffer{}
	outputPath := a.openRunTranscript(id, transcript)

	ctx, cancel := context.WithCancel(context.Background())
	run := &agentRun{
		id: id, label: label, task: task,
		started: time.Now(), status: RunRunning,
		outputPath: outputPath, transcript: transcript,
		cancel: cancel, done: make(chan struct{}),
	}
	a.agentRuns.add(run)

	go func() {
		defer cancel()
		defer close(run.done)
		defer transcript.close()

		result, err := a.runSubagent(ctx, def, task, subagentOptions{
			Writer: transcript,
			// Nobody is at the prompt to answer an approval request for a
			// detached run; the definition's tool list is the surface area.
			SkipApproval: true,
		})

		switch {
		case err == nil:
			run.finish(RunCompleted, result, "")
		case ctx.Err() != nil:
			run.finish(RunKilled, "", "stopped")
		default:
			run.finish(RunFailed, "", err.Error())
		}
	}()

	return run.info(), nil
}

// openRunTranscript points the run's buffer at a file under the project's
// agent_runs/ directory when there is one, and returns its path. One-shot mode
// has no project directory; the transcript then lives only in memory, which
// AgentOutput reads either way.
func (a *Agent) openRunTranscript(id string, buf *syncBuffer) string {
	if a.toolResultsDir == "" {
		return ""
	}
	dir := filepath.Join(filepath.Dir(a.toolResultsDir), "agent_runs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.logger.Warn("could not create agent_runs directory", "dir", dir, "error", err)
		return ""
	}
	path := filepath.Join(dir, id+".md")
	f, err := os.Create(path) //nolint:gosec // path is derived from a generated id
	if err != nil {
		a.logger.Warn("could not create agent transcript", "path", path, "error", err)
		return ""
	}
	buf.f = f
	return path
}

// ListAgentRuns returns every background agent started this session.
func (a *Agent) ListAgentRuns() []tool.AgentRunInfo {
	infos := a.agentRuns.list()
	out := make([]tool.AgentRunInfo, 0, len(infos))
	for _, i := range infos {
		out = append(out, tool.AgentRunInfo{
			ID: i.ID, Label: i.Label, Task: i.Task, Status: string(i.Status),
			OutputPath: i.OutputPath, Elapsed: elapsed(i),
		})
	}
	return out
}

// AgentRunOutput returns a run's transcript, optionally waiting for it to
// finish first. A zero or negative timeout does not block.
func (a *Agent) AgentRunOutput(
	ctx context.Context, id string, block bool, timeout time.Duration,
) (tool.AgentRunOutput, error) {
	run, ok := a.agentRuns.get(id)
	if !ok {
		return tool.AgentRunOutput{}, fmt.Errorf("no such background agent %q", id)
	}

	if block && timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-run.done:
		case <-timer.C:
		case <-ctx.Done():
		}
	}

	// Snapshot first, then read the transcript with no lock held: it can be
	// megabytes, and copying it under a lock would stall a finishing run.
	status, result, errText, _ := run.snapshot()
	return tool.AgentRunOutput{
		ID: run.id, Label: run.label, Status: string(status),
		OutputPath: run.outputPath, Transcript: run.transcript.String(),
		Result: result, Error: errText, Elapsed: elapsed(run.info()),
	}, nil
}

// StopAgentRun cancels a background agent. Stopping one that already finished
// is not an error — the caller's intent (make it not be running) is satisfied.
func (a *Agent) StopAgentRun(id string) (string, error) {
	run, ok := a.agentRuns.get(id)
	if !ok {
		return "", fmt.Errorf("no such background agent %q", id)
	}
	if status := run.getStatus(); status != RunRunning {
		return fmt.Sprintf("agent %s already %s", id, status), nil
	}

	// cancel() and the wait happen with no lock held: the worker takes the
	// run's lock to record its outcome, so holding it here would deadlock.
	run.cancel()
	<-run.done

	// Report what actually happened. A run that completed in the moment
	// between the check and the cancel is completed, not stopped, and saying
	// otherwise would send the caller looking for a result that exists.
	return fmt.Sprintf("agent %s %s", id, run.getStatus()), nil
}

// CancelBackgroundAgents stops everything still running and reports how many.
func (a *Agent) CancelBackgroundAgents() int {
	if a.agentRuns == nil {
		return 0
	}
	return a.agentRuns.CancelAll()
}

func elapsed(i RunInfo) time.Duration {
	end := i.Ended
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(i.Started).Round(time.Second)
}

// drainNotifications returns the outcome of every run that has finished and not
// yet been reported, marking each as reported.
func (r *agentRunRegistry) drainNotifications() []RunNotification {
	r.mu.Lock()
	runs := make([]*agentRun, 0, len(r.runs))
	for _, run := range r.runs {
		runs = append(runs, run)
	}
	r.mu.Unlock()

	var out []RunNotification
	for _, run := range runs {
		if n, ok := run.takeNotification(); ok {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// HasPendingAgentNotifications reports whether any finished run is still
// waiting to be reported. The REPL uses it to decide whether a bare Enter
// should drive a turn.
func (a *Agent) HasPendingAgentNotifications() bool {
	if a.agentRuns == nil {
		return false
	}
	a.agentRuns.mu.Lock()
	runs := make([]*agentRun, 0, len(a.agentRuns.runs))
	for _, run := range a.agentRuns.runs {
		runs = append(runs, run)
	}
	a.agentRuns.mu.Unlock()

	for _, run := range runs {
		run.mu.Lock()
		pending := !run.notified && run.status != RunRunning
		run.mu.Unlock()
		if pending {
			return true
		}
	}
	return false
}

// prependAgentNotifications puts any finished background agents' outcomes in
// front of the user's message for this turn.
//
// This lives in Invoke rather than in the REPL's executeTurn so every caller of
// a session turn gets it for free — the REPL, the Connect server, and the
// gateway all funnel through here, and duplicating the drain per front end
// would be three chances to forget it.
func (a *Agent) prependAgentNotifications(userInput string) string {
	if a.agentRuns == nil {
		return userInput
	}
	notes := a.agentRuns.drainNotifications()
	if len(notes) == 0 {
		return userInput
	}

	var b strings.Builder
	for _, n := range notes {
		b.WriteString(formatRunNotification(n))
		b.WriteString("\n")
	}

	// Say what to do with them. Delivering the block alone is not enough: a
	// model handed a notification plus an unrelated question will answer the
	// question and silently drop the result, which is exactly the outcome
	// backgrounding was supposed to avoid. Stated once, not per notification.
	b.WriteString(notificationInstruction(len(notes)))
	if userInput == "" {
		return strings.TrimRight(b.String(), "\n")
	}
	b.WriteString("\n")
	b.WriteString(userInput)
	return b.String()
}

// notificationInstruction tells the model to relay what just arrived. Without
// it the notification is delivered and ignored.
func notificationInstruction(n int) string {
	subject := "A background agent you started has finished"
	if n > 1 {
		subject = fmt.Sprintf("%d background agents you started have finished", n)
	}
	return subject + ". Report the result(s) to the user in your reply, before " +
		"anything else they asked for. Do not silently drop them, and do not " +
		"re-run the work yourself — the answer is above."
}

// formatRunNotification renders one outcome. The result is included inline —
// that is the whole point of backgrounding, so the caller does not have to go
// and fetch it — but the transcript is not; it stays in the file.
func formatRunNotification(n RunNotification) string {
	var b strings.Builder
	b.WriteString("<agent-notification>\n")
	fmt.Fprintf(&b, "<agent-id>%s</agent-id>\n", n.ID)
	fmt.Fprintf(&b, "<agent>%s</agent>\n", n.Label)
	fmt.Fprintf(&b, "<status>%s</status>\n", n.Status)
	fmt.Fprintf(&b, "<summary>Background agent %s (%s) %s after %s.</summary>\n",
		n.ID, n.Label, n.Status, n.Elapsed)
	if n.OutputPath != "" {
		fmt.Fprintf(&b, "<transcript-file>%s</transcript-file>\n", n.OutputPath)
	}
	if n.Error != "" {
		fmt.Fprintf(&b, "<error>%s</error>\n", n.Error)
	}
	if n.Result != "" {
		fmt.Fprintf(&b, "<result>\n%s\n</result>\n", n.Result)
	}
	b.WriteString("</agent-notification>")
	return b.String()
}
