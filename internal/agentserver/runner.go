// Package agentserver adapts an external app-server as a whole-agent backend for
// klein. Unlike the chat backends, such a backend runs its own reasoning + tool
// loop; klein routes a conversation turn to one of its threads and takes back
// the final text.
//
// Two backends are supported. Both speak the same codex-app-server JSON-RPC
// protocol and differ only in the binary spawned and how it is configured:
//
//   - codex     — the codex app-server (`codex app-server`)
//   - appserver — any local agent implementing the subset of that protocol used
//     here; the binary comes from `appserver.command` (e.g. `gallium app-server`)
//
// It drives the app-server over the LOW-LEVEL JSON-RPC protocol (not the SDK's
// high-level Thread helpers) for one reason: klein exposes its own tools to the
// backend via the experimental `dynamicTools` mechanism, which requires the
// `experimentalApi` capability negotiated at `initialize` — something the SDK's
// New() does not send. The backend then calls back for those tools via
// ItemToolCall over the same stdio connection (see dynamictools.go). See
// doc/DESIGN.md.
package agentserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pmenglund/codex-sdk-go/protocol"
	"github.com/pmenglund/codex-sdk-go/rpc"
)

// Config configures a Runner. Model/Effort come from klein's llm settings; the
// rest from the optional "codex"/"appserver" settings block.
type Config struct {
	Tools      DynamicTools
	MCPServers map[string]any
	Approver   Approver // decides on-request approvals (nil = auto-accept, for headless)
	// Command and Args spawn the app-server ("codex app-server", "gallium app-server").
	Command string
	Args    []string
	// Env holds environment overrides for the child (an app-server is configured
	// purely by env; see appServerEnv). Empty means the child inherits klein's
	// environment.
	Env []string
	// Dialect names which app-server this is, for the one piece of
	// implementation-specific behavior the client has (the codex auth probe).
	Dialect Dialect
	// Logger reports item types the backend sent that this renderer has no case
	// for. Optional: nil keeps the old silence, which is what the tests use.
	Logger         Logger
	Model          string
	Effort         string
	ApprovalPolicy string
	SandboxMode    string
	Cwd            string
}

// Runner wraps a single codex app-server process, shared across all klein
// sessions. Each session maps to one codex thread (RunTurn's threadID). Turns
// are serialized (one process; and klein's tool stores assume a single writer).
type Runner struct {
	cfg      Config
	client   *rpc.Client
	started  map[string]bool
	dynTools []map[string]any
	mu       sync.Mutex
	// startGraceOverride shortens startTurnGrace for tests; zero uses the const.
	startGraceOverride time.Duration
}

const clientName = "klein"

// NewRunner spawns the app-server, negotiates the experimentalApi capability,
// and precomputes the dynamic-tool specs. Close it to stop the process. Requires
// the backend binary on PATH (or an explicit path in settings); auth/model are
// the backend's own.
func NewRunner(ctx context.Context, cfg Config) (*Runner, error) {
	if cfg.Command == "" {
		return nil, errors.New("agent backend: no command configured")
	}
	// The spawn context governs process startup only; lifetime is tied to Close.
	// klein's own transport (not rpc.SpawnStdio) so the child's env can carry the
	// backend's config-derived settings.
	transport, err := spawnStdio(
		context.WithoutCancel(ctx), cfg.Command, cfg.Args, childEnv(cfg.Env), os.Stderr,
	)
	if err != nil {
		return nil, fmt.Errorf("spawn %s app-server: %w", cfg.Command, err)
	}

	handler := &toolHandler{tools: cfg.Tools, approver: cfg.Approver}
	client := rpc.NewClient(transport, rpc.ClientOptions{RequestHandler: handler})

	if _, err := client.Initialize(ctx, protocol.InitializeParams{
		ClientInfo:   protocol.ClientInfo{Name: clientName, Version: "1.0"},
		Capabilities: protocol.InitializeCapabilities{ExperimentalApi: true},
	}); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("initialize %s app-server: %w", cfg.Command, err)
	}
	if err := client.Notify(ctx, "initialized", nil); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("%s initialized notify: %w", cfg.Command, err)
	}

	// Eagerly validate codex is logged in so an auth failure surfaces at klein
	// startup, not on the user's first prompt. Skipped for the generic backend —
	// see probeReady.
	if err := probeReady(ctx, client, cfg.Dialect); err != nil {
		_ = client.Close()
		return nil, err
	}

	return &Runner{
		client:   client,
		cfg:      cfg,
		dynTools: buildDynamicTools(cfg.Tools),
		started:  make(map[string]bool),
	}, nil
}

// probeReady checks that a codex app-server is authenticated. initialize
// succeeds even when codex is logged out, so without this the first failure only
// appears on the first turn.
//
// It is codex-only by design. `account/read` is NOT part of the protocol subset
// klein requires of a generic app-server (initialize, thread/start, turn/start,
// dynamicTools), so probing one would reject conforming implementations that
// have no account concept at all. Nothing is lost by skipping it: there is no
// login to validate, and `initialize` is itself a round trip, so a dead or
// non-conforming server has already failed by this point.
func probeReady(ctx context.Context, client *rpc.Client, dialect Dialect) error {
	if dialect != DialectCodex {
		return nil
	}

	var resp protocol.GetAccountResponse
	if err := client.Call(ctx, "account/read", protocol.GetAccountParams{}, &resp); err != nil {
		return fmt.Errorf("codex readiness check (account/read) failed: %w", err)
	}
	if resp.RequiresOpenaiAuth && resp.Account == nil {
		return errors.New("codex is not logged in — run `codex login` (or configure an API key for the codex CLI)")
	}
	return nil
}

// RunTurn runs one turn against a codex thread and returns the thread id and the
// final assistant text. An empty threadID (or one this process did not start)
// begins a fresh thread with klein's dynamic tools registered — codex's
// thread/resume cannot re-register dynamic tools, so a tool-enabled thread is
// always one we started this run. developerInstructions (the active skill
// prompt) steers codex.
func (r *Runner) RunTurn(
	ctx context.Context, threadID, prompt, developerInstructions string, obs Observer,
) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if obs == nil {
		obs = discardObserver{}
	}

	if threadID == "" || !r.started[threadID] {
		id, err := r.startThread(ctx, developerInstructions)
		if err != nil {
			return threadID, "", err
		}
		r.started[id] = true
		threadID = id
	}

	resp, err := r.runTurn(ctx, threadID, prompt, obs)
	if err != nil {
		return threadID, "", err
	}
	return threadID, resp, nil
}

func (r *Runner) startThread(ctx context.Context, developerInstructions string) (string, error) {
	params := map[string]any{
		"cwd":            r.cfg.Cwd,
		"approvalPolicy": r.approvalPolicy(),
		"sandbox":        r.sandboxMode(),
	}
	if r.cfg.Model != "" {
		params["model"] = r.cfg.Model
	}
	if developerInstructions != "" {
		params["developerInstructions"] = developerInstructions
	}
	if len(r.dynTools) > 0 {
		params["dynamicTools"] = r.dynTools
	}
	if len(r.cfg.MCPServers) > 0 {
		params["config"] = map[string]any{"mcp_servers": r.cfg.MCPServers}
	}

	// codex's ThreadStartResponse carries the id inside the thread object.
	var resp struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := r.client.Call(ctx, "thread/start", params, &resp); err != nil {
		return "", fmt.Errorf("codex thread/start: %w", err)
	}
	if resp.Thread.ID == "" {
		return "", errors.New("codex thread/start returned no thread id")
	}
	return resp.Thread.ID, nil
}

// runTurn starts a turn and drains notifications until the turn completes,
// returning the last agent message text. Intermediate items (commands run,
// reasoning, file changes, tool calls) are reported to obs as they stream in so
// the caller can show what the backend is doing, not just the final text.
func (r *Runner) runTurn(
	ctx context.Context, threadID, prompt string, obs Observer,
) (string, error) {
	iter := r.client.SubscribeNotifications(0)
	defer iter.Close()

	turnParams := map[string]any{
		keyThreadID: threadID,
		"input":     []map[string]any{{keyType: keyText, keyText: prompt}},
	}
	if r.cfg.Effort != "" {
		turnParams["effort"] = r.cfg.Effort
	}
	turnID, err := r.startTurn(ctx, threadID, turnParams)
	if err != nil {
		// A start that was canceled in flight may still have left a turn running,
		// and startTurn hands back its id when it managed to learn one. An empty
		// id (a refusal, a dead transport) makes this a no-op.
		r.interruptTurn(ctx, threadID, turnID)
		return "", fmt.Errorf("codex turn/start: %w", err)
	}

	progress := &turnProgress{
		obs:       obs,
		announced: map[string]bool{},
		logger:    r.cfg.Logger,
		reported:  map[string]bool{},
	}
	final := ""
	for {
		note, err := iter.Next(ctx)
		if err != nil {
			// Ctrl+C (or any canceled/expired context) lands here. Leaving now
			// would abandon a turn the backend is still running: it holds the
			// thread's turn slot, and the next turn/start is refused ("one turn
			// at a time") until it ends on its own.
			r.interruptTurn(ctx, threadID, turnID)
			return "", fmt.Errorf("codex notification stream: %w", err)
		}
		text, status := classifyNote(note, threadID, progress)
		if text != "" {
			final = text
		}
		switch status {
		case noteDone:
			return final, nil
		case noteFailed:
			return final, fmt.Errorf("codex turn failed: %s", string(note.Raw))
		default: // noteContinue
		}
	}
}

// startTurnGrace bounds how long a canceled turn/start is given to come back
// with its turn id. Short: the response is an acknowledgement the backend sends
// before doing any of the turn's work, so if it has not arrived by now it is not
// going to arrive in time to be useful.
const startTurnGrace = 3 * time.Second

// startTurn issues turn/start and returns the new turn's id — the handle
// turn/interrupt needs.
//
// The call deliberately does not travel on ctx. A request already on the wire
// has a turn behind it whether or not klein is still listening, and dropping the
// response loses the only handle for stopping it: the same abandoned-turn leak
// this path exists to prevent, in the window where the id does not exist yet. So
// a cancellation waits a bounded grace period for the id and returns it
// alongside the error, leaving the caller to interrupt.
//
// A response that arrives after that grace is not discarded either: by then
// nobody is left to act on it, so the goroutine stops the turn itself. Ownership
// of that duty passes under the lock, so exactly one of the two does it.
func (r *Runner) startTurn(ctx context.Context, threadID string, params map[string]any) (string, error) {
	// Already given up before asking: starting a turn would be work for nobody.
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("turn not started: %w", err)
	}

	type startResult struct {
		err error
		id  string
	}
	done := make(chan startResult, 1)
	var (
		mu        sync.Mutex
		abandoned bool // the caller stopped waiting; the goroutine cleans up
	)

	go func() {
		// codex answers turn/start at once and reports the rest by notification,
		// so this response is an acknowledgement, not the turn's outcome. Like
		// thread/start, the id travels inside the object (TurnStartResponse).
		var started struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		err := r.client.Call(context.WithoutCancel(ctx), "turn/start", params, &started)
		id := started.Turn.ID

		mu.Lock()
		if !abandoned {
			done <- startResult{id: id, err: err} // buffered: never blocks
			mu.Unlock()
			return
		}
		mu.Unlock()

		// Late. runTurn returned long ago and cannot interrupt what it never
		// learned the name of, so this is the only chance the turn gets to be
		// stopped rather than run on holding the thread's slot.
		r.interruptTurn(ctx, threadID, id)
	}()

	select {
	case res := <-done:
		return res.id, res.err
	case <-ctx.Done():
	}

	select {
	case res := <-done:
		// The turn exists and klein knows its id; the caller can now stop it.
		return res.id, fmt.Errorf("canceled while starting the turn: %w", ctx.Err())
	case <-time.After(r.startGrace()):
		mu.Lock()
		abandoned = true
		mu.Unlock()
		// The answer may have landed between the timer firing and the handover.
		select {
		case res := <-done:
			return res.id, fmt.Errorf("canceled while starting the turn: %w", ctx.Err())
		default:
		}
		return "", fmt.Errorf(
			"canceled while starting the turn, and it did not answer within %s: %w", r.startGrace(), ctx.Err())
	}
}

// startGrace is startTurnGrace, or the override tests set to keep themselves
// fast — nothing configures it at runtime.
func (r *Runner) startGrace() time.Duration {
	if r.startGraceOverride > 0 {
		return r.startGraceOverride
	}
	return startTurnGrace
}

// interruptTimeout bounds the wait for the backend to confirm the turn stopped.
// A backend with no interruption point (one blocked in a model call that ignores
// cancellation) would otherwise hold klein here indefinitely, and the caller has
// already given up on this turn.
const interruptTimeout = 10 * time.Second

// interruptTurn asks the backend to stop a turn klein is walking away from, and
// waits for it to confirm.
//
// The wait is the point, not politeness: the protocol parks the request and
// answers only once the turn has aborted, so a response means *stopped*, not
// *heard*. Returning before that would hand the next turn/start a thread whose
// slot is still taken.
//
// Best-effort. An app-server that predates turn/interrupt answers
// method-not-found, and there is nothing to do about it here — the caller is
// already returning an error, and the turn will free the thread when it ends on
// its own. It is worth a log line: it means Ctrl+C left work running.
func (r *Runner) interruptTurn(ctx context.Context, threadID, turnID string) {
	if turnID == "" {
		return
	}
	// ctx is the reason we are here — canceled or expired — so the interrupt
	// cannot travel on it, or it would fail before reaching the backend.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), interruptTimeout)
	defer cancel()

	params := map[string]any{keyThreadID: threadID, keyTurnID: turnID}
	if err := r.client.Call(ctx, "turn/interrupt", params, &json.RawMessage{}); err != nil && r.cfg.Logger != nil {
		r.cfg.Logger.Warn(
			"could not stop the abandoned turn; the backend may still be working",
			"thread", threadID,
			"turn", turnID,
			"error", err,
		)
	}
}

// methodError is codex's `error` server notification (v2 ErrorNotification).
const methodError = "error"

type noteStatus int

const (
	noteContinue noteStatus = iota
	noteDone
	noteFailed
)

// classifyNote returns any assistant text carried by the notification and
// whether the turn is done/failed. Notifications for other threads are ignored
// (the subscription is process-global). Along the way it forwards item activity
// (item/started announces a command/tool call; item/completed carries its
// result or a completed reasoning block) to the progress tracker for display.
func classifyNote(note rpc.Notification, threadID string, progress *turnProgress) (string, noteStatus) {
	var p struct {
		ThreadID string          `json:"threadId"`
		Item     json.RawMessage `json:"item"`
		Turn     struct {
			Status string `json:"status"`
		} `json:"turn"`
		// The `error` notification's payload (codex v2 ErrorNotification).
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		WillRetry bool `json:"willRetry"`
	}
	_ = json.Unmarshal(note.Raw, &p)
	if p.ThreadID != "" && p.ThreadID != threadID {
		return "", noteContinue
	}
	switch note.Method {
	case "item/started":
		progress.render(p.Item, false)
	case "item/completed":
		progress.render(p.Item, true)
		if text, ok := extractText(p.Item); ok {
			return text, noteContinue
		}
	case "turn/completed":
		// codex spells every ending this way and puts the outcome in the
		// status, so the method alone does not say whether the turn worked.
		// Reading only the method reported a failed turn as a successful one
		// carrying whatever text happened to precede it.
		//
		// "interrupted" stays a success: the turn ended the way it was asked
		// to, and `final` holds what it had produced, which is what a user who
		// pressed Ctrl+C wants back.
		if p.Turn.Status == statusFailed {
			return "", noteFailed
		}
		return "", noteDone
	case methodError:
		// codex *does* define `error` as a server notification
		// (app-server-protocol v2 ErrorNotification: error, willRetry, threadId,
		// turnId), and willRetry is the whole point of reading it: codex sets it
		// on a stream error it is retrying itself, which by definition does not
		// interrupt the turn. Failing here would show the user a failure that
		// did not happen and abandon a turn still holding the thread's slot, so
		// the next turn/start collides with it ("one turn at a time"). The turn's
		// real outcome still arrives as turn/completed.
		if p.WillRetry {
			progress.reportRetry(p.Error.Message)
			return "", noteContinue
		}
		return "", noteFailed
	default:
		// Everything else is ignored, but not silently: an unhandled method is
		// how a client and a backend drift apart. There is deliberately no arm
		// for `turn/failed` — rs-gallium's own spelling, which gallium stopped
		// sending once it reported failures the codex way (fpt/rs-gallium#77/
		// #80) — so a backend old enough to still send it lands here and says
		// so, rather than hanging the turn in silence.
		progress.reportUnhandledMethod(note)
	}
	return "", noteContinue
}

// codexItem is the subset of a codex ThreadItem (see the app-server protocol's
// ItemCompletedNotification schema) that klein renders as progress. The `type`
// field discriminates the variant; the rest are variant-specific and left zero
// when absent.
type codexItem struct {
	AggregatedOutput *string  `json:"aggregatedOutput"`
	ExitCode         *int     `json:"exitCode"`
	Error            *string  `json:"error"`
	ID               string   `json:"id"`
	Type             string   `json:"type"`
	Command          string   `json:"command"`
	Status           string   `json:"status"`
	Tool             string   `json:"tool"`
	Server           string   `json:"server"`
	Query            string   `json:"query"`
	Summary          []string `json:"summary"`
	Changes          []struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	} `json:"changes"`
	// Tool-call input/output. Field names vary across backends and item variants,
	// so several output candidates are captured and the first non-empty is shown;
	// all are optional and absence degrades to the status string.
	Arguments json.RawMessage `json:"arguments"`
	Result    json.RawMessage `json:"result"`
	Output    json.RawMessage `json:"output"`
	Content   json.RawMessage `json:"content"`
	// codex's dynamicToolCall spells its output this way, and defines no
	// `result` field on that variant at all — so this is the only candidate
	// that carries anything for the tool calls gallium reports.
	ContentItems json.RawMessage `json:"contentItems"`
}

// Synthetic tool names klein assigns to codex activity so it renders through
// the same ReAct event path as native tool use.
const (
	toolExec       = "exec"
	toolApplyPatch = "apply_patch"
	toolWebSearch  = "web_search"
)

// statusFailed is the codex CommandExecutionStatus/PatchApplyStatus value
// marking a command or patch that did not succeed.
const statusFailed = "failed"

// Argument keys used when announcing codex activity as tool calls.
const (
	argCommand = "command"
	argFiles   = "files"
	argQuery   = "query"
)

// turnProgress translates a turn's codex ThreadItems into ReAct-style events so
// the app layer renders backend activity through the same path as native tool
// use. It tracks which items have already been announced (by item id) so a
// tool call is shown exactly once whether codex emits item/started, only
// item/completed, or both.
type turnProgress struct {
	obs       Observer
	announced map[string]bool
	// logger reports item types nothing renders. Optional; nil stays silent.
	logger Logger
	// reported dedupes those reports by type, so a backend that sends an
	// unhandled variant on every item costs one line per turn, not one per item.
	reported map[string]bool
	// reportedMethods does the same for notification methods (see
	// reportUnhandledMethod). Separate from reported: the two namespaces are
	// unrelated, and one map would let an item type mask a method or vice versa.
	reportedMethods map[string]bool
}

// itemTypesKnownUnrendered are variants this renderer recognises and
// deliberately skips: the turn's own text arrives through extractText, and the
// rest are backend bookkeeping klein has no display for.
//
// Anything outside both this set and render's switch is reported, because a
// type nobody handles usually means the backend and the client have drifted
// apart — fpt/rs-gallium#49 was exactly that, and stayed invisible because an
// unrecognised type is indistinguishable from one deliberately ignored.
//
// The list is best-effort. If a legitimate variant turns up in the log, adding
// it here is the fix; the per-type dedupe keeps the cost to one line meanwhile.
//
// autoApprovalReview and permissions are here for that reason: the codex SDK
// carries item-scoped notifications for both (item/autoApprovalReview/{started,
// completed}, item/permissions/requestApproval), so they are ordinary codex
// bookkeeping rather than drift — klein handles approval through toolHandler and
// has nothing to display for the items themselves.
//
// userMessage is codex echoing back the prompt klein just sent, as the first
// item of every turn (both item/started and item/completed; verified against
// codex-cli 0.144.1). Displaying the user's own input back to them is noise, and
// leaving it out of this set made the drift warning fire on every single prompt.
var itemTypesKnownUnrendered = map[string]bool{
	"agentMessage":       true,
	"userMessage":        true,
	"plan":               true,
	"sleep":              true,
	"reviewMode":         true,
	"enteredReviewMode":  true,
	"exitedReviewMode":   true,
	"autoApprovalReview": true,
	"permissions":        true,
}

// reportUnrendered logs an item type that reached render with no case for it.
func (tp *turnProgress) reportUnrendered(itemType string) {
	if tp.logger == nil || itemType == "" || itemTypesKnownUnrendered[itemType] {
		return
	}
	if tp.reported == nil {
		tp.reported = map[string]bool{}
	}
	if tp.reported[itemType] {
		return
	}
	tp.reported[itemType] = true
	tp.logger.Warn(
		"app-server sent an item type this build does not render",
		"type", itemType,
		"hint", "the backend may be newer than klein, or the two have drifted",
	)
}

// reportUnhandledMethod logs a notification method classifyNote has no arm for,
// when that method is one the codex protocol does not define either.
//
// It is the message-level twin of reportUnrendered, and exists for the same
// reason: rs-gallium warns on every client notification it does not know
// ("the client may speak a newer app-server protocol than this build
// implements") because both ends of this protocol are hand-written against one
// spec and drift apart quietly — fpt/rs-gallium#49 stayed invisible for exactly
// as long as that took to notice by hand. klein was the half of that pair still
// dropping unknown methods without a word.
//
// The "codex does not define it either" qualifier is what keeps the signal
// readable. codex defines dozens of server notifications klein has nothing to do
// with — the streaming deltas (item/agentMessage/delta, item/reasoning/textDelta,
// item/commandExecution/outputDelta) arrive per token, and klein renders their
// completed items instead. Warning on those would bury the one line that means
// something under the backend's ordinary bookkeeping. A method nobody on either
// side of the generated protocol claims is the real drift shape.
//
// rpc.Notification.Params is the discriminator, since the SDK's method table is
// unexported: the client fills Params with the decoded payload for a method it
// knows and leaves it nil otherwise. It is also nil for a known method whose
// params failed to decode — drift of another kind, and equally worth the line.
// The one false positive it admits is a known method whose payload type is one
// of the SDK's `interface{}` fallbacks *and* which arrives with no params at
// all; that costs a single deduped line, which is the cheaper mistake.
func (tp *turnProgress) reportUnhandledMethod(note rpc.Notification) {
	if tp.logger == nil || note.Method == "" || note.Params != nil {
		return
	}
	if tp.reportedMethods == nil {
		tp.reportedMethods = map[string]bool{}
	}
	if tp.reportedMethods[note.Method] {
		return
	}
	tp.reportedMethods[note.Method] = true
	tp.logger.Warn(
		"app-server sent a notification this build does not handle",
		"method", note.Method,
		"hint", "the backend may be newer than klein, or the two have drifted",
	)
}

// reportRetry notes a transient backend error the app-server is retrying by
// itself. Worth a line: the turn survives, but it stalls for as long as the
// retry takes, and silence there looks like a hung agent.
func (tp *turnProgress) reportRetry(msg string) {
	if tp.logger == nil {
		return
	}
	tp.logger.Warn(
		"app-server hit a transient error and is retrying; the turn is still running",
		"detail", strings.TrimSpace(msg),
	)
}

// render emits progress for one ThreadItem, dispatching by variant. completed
// marks an item/completed notification (carrying the outcome) versus an
// item/started announcement. agentMessage (returned as the turn text) and other
// item types (plan, sleep, review-mode, …) are not rendered as progress.
func (tp *turnProgress) render(raw json.RawMessage, completed bool) {
	var it codexItem
	if len(raw) == 0 || json.Unmarshal(raw, &it) != nil {
		return
	}

	switch it.Type {
	case "commandExecution":
		tp.renderCommand(it, completed)
	case "fileChange":
		tp.renderFileChange(it, completed)
	case "mcpToolCall", "dynamicToolCall":
		tp.renderToolCall(it, completed)
	case "webSearch":
		tp.announce(it.ID, toolWebSearch, map[string]any{argQuery: it.Query})
	case "reasoning":
		if completed {
			if s := strings.TrimSpace(strings.Join(it.Summary, "\n")); s != "" {
				tp.obs.ReasoningSummary(s)
			}
		}
	default:
		tp.reportUnrendered(it.Type)
	}
}

func (tp *turnProgress) renderCommand(it codexItem, completed bool) {
	tp.announce(it.ID, toolExec, map[string]any{argCommand: it.Command})
	if !completed {
		return
	}
	tp.obs.ToolCallCompleted(ToolCallResult{
		Name:    toolExec,
		Content: commandResultSummary(it.AggregatedOutput, it.ExitCode),
		IsError: it.Status == statusFailed || (it.ExitCode != nil && *it.ExitCode != 0),
	})
}

func (tp *turnProgress) renderFileChange(it codexItem, completed bool) {
	paths := make([]string, 0, len(it.Changes))
	for _, c := range it.Changes {
		paths = append(paths, strings.TrimSpace(c.Kind+" "+c.Path))
	}
	tp.announce(it.ID, toolApplyPatch, map[string]any{argFiles: paths})
	if !completed {
		return
	}
	tp.obs.ToolCallCompleted(ToolCallResult{
		Name:    toolApplyPatch,
		Content: fmt.Sprintf("%d file(s): %s", len(it.Changes), statusOrDefault(it.Status, "applied")),
		IsError: it.Status == statusFailed,
	})
}

func (tp *turnProgress) renderToolCall(it codexItem, completed bool) {
	name := it.Tool
	if it.Server != "" {
		name = it.Server + "/" + it.Tool
	}
	tp.announce(it.ID, name, toolCallArgs(it.Arguments))
	if !completed {
		return
	}
	content, isErr := toolCallOutcome(it)
	tp.obs.ToolCallCompleted(ToolCallResult{Name: name, Content: content, IsError: isErr})
}

// toolCallArgs parses a tool call's raw arguments into a map for the Observer.
// A payload that is not a JSON object has nothing useful to be said about its
// shape, so it travels whole under a single "input" key.
//
// Nothing is truncated or summarized here. That was display policy living in the
// client, and it was applied unevenly — dynamic and MCP calls were summarized,
// exec and apply_patch were not — so the same argument read differently
// depending on which kind of call produced it. The caller now decides, once, for
// all of them.
func toolCallArgs(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj
	}
	return map[string]any{"input": compactJSON(raw, 0)}
}

// toolCallOutcome extracts a displayable result + error flag for a completed
// tool call: an explicit error, else the tool's output text, else the status.
func toolCallOutcome(it codexItem) (content string, isErr bool) {
	isErr = it.Status == statusFailed
	if it.Error != nil && strings.TrimSpace(*it.Error) != "" {
		return strings.TrimSpace(*it.Error), true
	}
	for _, raw := range []json.RawMessage{it.Result, it.Output, it.Content, it.ContentItems} {
		if s := renderToolResult(raw); s != "" {
			return s, isErr
		}
	}
	return statusOrDefault(it.Status, "done"), isErr
}

// renderToolResult turns a raw tool-result payload into a readable string,
// handling a JSON string, MCP-style content items ([{text:…}] or {content:[…]}),
// or arbitrary JSON (compacted). Returns "" when there is nothing to show.
func renderToolResult(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	if texts := contentItemTexts(raw); texts != "" {
		return texts
	}
	return compactJSON(raw, 0)
}

// contentItemTexts joins the "text" fields of MCP-style content items, accepting
// either a bare array or a {content:[…]} wrapper. Returns "" if neither matches.
func contentItemTexts(raw json.RawMessage) string {
	var wrapper struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &wrapper) == nil && len(wrapper.Content) > 0 {
		if s := joinTexts(wrapper.Content); s != "" {
			return s
		}
	}
	return joinTexts(raw)
}

func joinTexts(raw json.RawMessage) string {
	var items []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, it := range items {
		if t := strings.TrimSpace(it.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

// compactJSON re-encodes raw as single-line JSON, truncated to limit runes (0 =
// no limit). Falls back to the raw string if compaction fails.
func compactJSON(raw json.RawMessage, limit int) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return truncateStr(strings.TrimSpace(string(raw)), limit)
	}
	return truncateStr(buf.String(), limit)
}

// truncateStr shortens s to limit runes with an ellipsis (limit <= 0 = no limit).
func truncateStr(s string, limit int) string {
	if limit <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}

// announce emits a ToolCallStart for an item the first time it is seen, keyed by
// item id, so a call announced at item/started is not repeated at
// item/completed (and is still shown when only item/completed arrives).
func (tp *turnProgress) announce(id, tool string, args map[string]any) {
	if id != "" {
		if tp.announced[id] {
			return
		}
		tp.announced[id] = true
	}
	tp.obs.ToolCallStarted(ToolCall{Name: tool, Arguments: args})
}

// commandResultSummary renders a command's output followed by its exit status.
// The status goes last so it survives the app layer's tail-truncation of long
// results — the exit code is the part the user most needs to see.
func commandResultSummary(aggregatedOutput *string, exitCode *int) string {
	status := "exit 0"
	if exitCode != nil {
		status = fmt.Sprintf("exit %d", *exitCode)
	}
	if aggregatedOutput == nil || strings.TrimSpace(*aggregatedOutput) == "" {
		return status
	}
	return strings.TrimRight(*aggregatedOutput, "\n") + "\n" + status
}

// statusOrDefault returns status, or fallback when codex omitted it.
func statusOrDefault(status, fallback string) string {
	if status == "" {
		return fallback
	}
	return status
}

func (r *Runner) approvalPolicy() string {
	if r.cfg.ApprovalPolicy != "" {
		return r.cfg.ApprovalPolicy
	}
	return "never"
}

func (r *Runner) sandboxMode() string {
	if r.cfg.SandboxMode != "" {
		return r.cfg.SandboxMode
	}
	return "workspace-write"
}

// Close stops the codex app-server process. Safe on a nil Runner.
func (r *Runner) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	if err := r.client.Close(); err != nil {
		return fmt.Errorf("close codex client: %w", err)
	}
	return nil
}

// extractText pulls the assistant text out of an item/completed payload's item.
// Codex items carry the text either directly or nested one level under a variant
// key (agent message).
func extractText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var direct struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &direct) == nil && direct.Text != "" {
		return direct.Text, true
	}
	var wrapper map[string]json.RawMessage
	if json.Unmarshal(raw, &wrapper) != nil || len(wrapper) != 1 {
		return "", false
	}
	for _, inner := range wrapper {
		var nested struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(inner, &nested) == nil && nested.Text != "" {
			return nested.Text, true
		}
	}
	return "", false
}
