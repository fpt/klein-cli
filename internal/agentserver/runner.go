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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/pmenglund/codex-sdk-go/protocol"
	"github.com/pmenglund/codex-sdk-go/rpc"

	"github.com/fpt/klein-cli/pkg/agent/domain"
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
func (r *Runner) RunTurn(ctx context.Context, threadID, prompt, developerInstructions string) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if threadID == "" || !r.started[threadID] {
		id, err := r.startThread(ctx, developerInstructions)
		if err != nil {
			return threadID, "", err
		}
		r.started[id] = true
		threadID = id
	}

	resp, err := r.runTurn(ctx, threadID, prompt)
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
// returning the last agent message text.
func (r *Runner) runTurn(ctx context.Context, threadID, prompt string) (string, error) {
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

	final := ""
	for {
		note, err := iter.Next(ctx)
		if err != nil {
			return "", fmt.Errorf("codex notification stream: %w", err)
		}
		text, status := classifyNote(note, threadID)
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
// (the subscription is process-global).
func classifyNote(note rpc.Notification, threadID string) (string, noteStatus) {
	var p struct {
		ThreadID string          `json:"threadId"`
		Item     json.RawMessage `json:"item"`
	}
	_ = json.Unmarshal(note.Raw, &p)
	if p.ThreadID != "" && p.ThreadID != threadID {
		return "", noteContinue
	}
	switch note.Method {
	case "item/completed":
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
