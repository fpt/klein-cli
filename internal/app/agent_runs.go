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

	id         string
	label      string
	task       string
	result     string
	errText    string
	outputPath string

	started time.Time
	ended   time.Time
	status  RunStatus
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
	return RunInfo{
		ID: a.id, Label: a.label, Task: a.task, Status: a.getStatus(),
		OutputPath: a.outputPath, Started: a.started, Ended: a.ended,
	}
}

func (a *agentRun) getStatus() RunStatus { return a.status }

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

		a.agentRuns.mu.Lock()
		defer a.agentRuns.mu.Unlock()
		run.ended = time.Now()
		switch {
		case err == nil:
			run.status, run.result = RunCompleted, result
		case ctx.Err() != nil:
			run.status, run.errText = RunKilled, "stopped"
		default:
			run.status, run.errText = RunFailed, err.Error()
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

	a.agentRuns.mu.Lock()
	defer a.agentRuns.mu.Unlock()
	return tool.AgentRunOutput{
		ID: run.id, Label: run.label, Status: string(run.status),
		OutputPath: run.outputPath, Transcript: run.transcript.String(),
		Result: run.result, Error: run.errText, Elapsed: elapsed(run.info()),
	}, nil
}

// StopAgentRun cancels a background agent. Stopping one that already finished
// is not an error — the caller's intent (make it not be running) is satisfied.
func (a *Agent) StopAgentRun(id string) (string, error) {
	run, ok := a.agentRuns.get(id)
	if !ok {
		return "", fmt.Errorf("no such background agent %q", id)
	}
	if run.getStatus() != RunRunning {
		return fmt.Sprintf("agent %s already %s", id, run.getStatus()), nil
	}
	run.cancel()
	<-run.done
	return fmt.Sprintf("agent %s stopped", id), nil
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
