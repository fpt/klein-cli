// Package agentserver adapts an external app-server as a whole-agent backend for
// klein. Unlike the chat backends, such a backend runs its own reasoning + tool
// loop; klein routes a conversation turn to one of its threads and takes back
// the final text.
//
// Two backends are supported. Both speak the same JSON-RPC app-server protocol
// and differ only in the binary spawned:
//
//   - codex  — the codex app-server (`codex app-server`)
//   - kessel — the kessel agent (`kessel-cli app-server`), which implements the
//     subset of that protocol used here
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

	"github.com/pmenglund/codex-sdk-go/protocol"
	"github.com/pmenglund/codex-sdk-go/rpc"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/agent/events"
	"github.com/fpt/klein-cli/pkg/message"
)

// Config configures a Runner. Model/Effort come from klein's llm settings; the
// rest from the optional "codex"/"kessel" settings block.
type Config struct {
	Tools      domain.ToolManager
	MCPServers map[string]any
	Approver   Approver // decides on-request approvals (nil = auto-accept, for headless)
	// Command and Args spawn the app-server ("codex app-server", "kessel-cli app-server").
	Command string
	Args    []string
	// Backend names which app-server this is, for backend-specific behavior
	// (e.g. the codex-only auth probe) and for log/error messages.
	Backend        string
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
	transport, err := rpc.SpawnStdio(context.WithoutCancel(ctx), cfg.Command, cfg.Args, os.Stderr)
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

	// Eagerly validate the backend is usable so a login/config failure surfaces
	// at klein startup, not on the user's first prompt. Doubles as a liveness
	// check on the handshake. Kessel answers this too — it carries credentials in
	// its own config and reports no auth requirement.
	if err := probeReady(ctx, client, cfg.Backend); err != nil {
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

// probeReady checks that the app-server is authenticated. initialize succeeds
// even when codex is logged out, so without this the first failure only appears
// on the first turn. Backends with no login (kessel) report no auth requirement
// and pass.
func probeReady(ctx context.Context, client *rpc.Client, backend string) error {
	var resp protocol.GetAccountResponse
	if err := client.Call(ctx, "account/read", protocol.GetAccountParams{}, &resp); err != nil {
		return fmt.Errorf("%s readiness check (account/read) failed: %w", backend, err)
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
	ctx context.Context, threadID, prompt, developerInstructions string, emit func(events.EventType, any),
) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if emit == nil {
		emit = func(events.EventType, any) {}
	}

	if threadID == "" || !r.started[threadID] {
		id, err := r.startThread(ctx, developerInstructions)
		if err != nil {
			return threadID, "", err
		}
		r.started[id] = true
		threadID = id
	}

	resp, err := r.runTurn(ctx, threadID, prompt, emit)
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

	var resp struct {
		ThreadID string `json:"threadId"`
		Thread   struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := r.client.Call(ctx, "thread/start", params, &resp); err != nil {
		return "", fmt.Errorf("codex thread/start: %w", err)
	}
	id := resp.ThreadID
	if id == "" {
		id = resp.Thread.ID
	}
	if id == "" {
		return "", errors.New("codex thread/start returned no thread id")
	}
	return id, nil
}

// runTurn starts a turn and drains notifications until the turn completes,
// returning the last agent message text. Intermediate items (commands run,
// reasoning, file changes, tool calls) are forwarded to emit as they stream in
// so the caller can show what the backend is doing, not just the final text.
func (r *Runner) runTurn(
	ctx context.Context, threadID, prompt string, emit func(events.EventType, any),
) (string, error) {
	iter := r.client.SubscribeNotifications(0)
	defer iter.Close()

	turnParams := map[string]any{
		"threadId": threadID,
		"input":    []map[string]any{{keyType: keyText, keyText: prompt}},
	}
	if r.cfg.Effort != "" {
		turnParams["effort"] = r.cfg.Effort
	}
	if err := r.client.Call(ctx, "turn/start", turnParams, &json.RawMessage{}); err != nil {
		return "", fmt.Errorf("codex turn/start: %w", err)
	}

	progress := &turnProgress{emit: emit, announced: map[string]bool{}}
	final := ""
	for {
		note, err := iter.Next(ctx)
		if err != nil {
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
		return "", noteDone
	case "turn/failed", "error":
		return "", noteFailed
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
	emit      func(events.EventType, any)
	announced map[string]bool
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
		tp.announce(it.ID, toolWebSearch, message.ToolArgumentValues{argQuery: it.Query})
	case "reasoning":
		if completed {
			if s := strings.TrimSpace(strings.Join(it.Summary, "\n")); s != "" {
				tp.emit(events.EventTypeThinkingChunk, events.ThinkingChunkData{Content: s + "\n"})
			}
		}
	}
}

func (tp *turnProgress) renderCommand(it codexItem, completed bool) {
	tp.announce(it.ID, toolExec, message.ToolArgumentValues{argCommand: it.Command})
	if !completed {
		return
	}
	tp.emit(events.EventTypeToolResult, events.ToolResultData{
		ToolName: toolExec,
		Content:  commandResultSummary(it.AggregatedOutput, it.ExitCode),
		IsError:  it.Status == statusFailed || (it.ExitCode != nil && *it.ExitCode != 0),
	})
}

func (tp *turnProgress) renderFileChange(it codexItem, completed bool) {
	paths := make([]string, 0, len(it.Changes))
	for _, c := range it.Changes {
		paths = append(paths, strings.TrimSpace(c.Kind+" "+c.Path))
	}
	tp.announce(it.ID, toolApplyPatch, message.ToolArgumentValues{argFiles: paths})
	if !completed {
		return
	}
	tp.emit(events.EventTypeToolResult, events.ToolResultData{
		ToolName: toolApplyPatch,
		Content:  fmt.Sprintf("%d file(s): %s", len(it.Changes), statusOrDefault(it.Status, "applied")),
		IsError:  it.Status == statusFailed,
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
	tp.emit(events.EventTypeToolResult, events.ToolResultData{ToolName: name, Content: content, IsError: isErr})
}

// maxArgValueLen bounds a single displayed argument value; the result content is
// tail-truncated by the app layer, so only the input is capped here.
const maxArgValueLen = 200

// toolCallArgs parses a tool call's raw arguments into a display map, truncating
// long string values so the input is visible without flooding the terminal. A
// non-object payload is shown compactly under a single "input" key.
func toolCallArgs(raw json.RawMessage) message.ToolArgumentValues {
	if len(raw) == 0 {
		return message.ToolArgumentValues{}
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		for k, v := range obj {
			if s, ok := v.(string); ok {
				obj[k] = truncateStr(s, maxArgValueLen)
			}
		}
		return obj
	}
	return message.ToolArgumentValues{"input": compactJSON(raw, maxArgValueLen)}
}

// toolCallOutcome extracts a displayable result + error flag for a completed
// tool call: an explicit error, else the tool's output text, else the status.
func toolCallOutcome(it codexItem) (content string, isErr bool) {
	isErr = it.Status == statusFailed
	if it.Error != nil && strings.TrimSpace(*it.Error) != "" {
		return strings.TrimSpace(*it.Error), true
	}
	for _, raw := range []json.RawMessage{it.Result, it.Output, it.Content} {
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
func (tp *turnProgress) announce(id, tool string, args message.ToolArgumentValues) {
	if id != "" {
		if tp.announced[id] {
			return
		}
		tp.announced[id] = true
	}
	tp.emit(events.EventTypeToolCallStart, events.ToolCallStartData{ToolName: tool, Arguments: args})
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
