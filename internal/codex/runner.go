// Package codex adapts the codex app-server (via the codex-sdk-go client) as a
// whole-agent backend for klein. Unlike the chat backends (anthropic/openai/…),
// codex runs its own reasoning + tool loop; klein routes a conversation turn to
// a codex thread and takes back the final assistant text. See doc/DESIGN.md
// (backend layer) and the Runner's RunTurn.
package codex

import (
	"context"
	"fmt"
	"os"
	"sync"

	sdk "github.com/pmenglund/codex-sdk-go"
)

// Config configures a Runner. Model/Effort come from klein's llm settings; the
// rest from the optional "codex" settings block.
type Config struct {
	CodexPath      string         // path to the codex binary ("" → "codex" on PATH)
	Model          string         // model, from llm.model ("" → codex default)
	Effort         string         // reasoning effort, from llm.effort ("" → default)
	ApprovalPolicy string         // "" → "never" (headless auto-approve)
	SandboxMode    string         // "" → "workspace-write"
	Cwd            string         // working directory for codex threads
	MCPServers     map[string]any // codex mcp_servers table (see MCPServersConfig)
}

// Runner wraps a single codex app-server process, shared across all klein
// sessions in the host process. Each klein session maps to one codex thread
// (RunTurn's threadID). Turns are serialized (one app-server; and the shared
// tool stores assume a single writer).
type Runner struct {
	client *sdk.Codex
	cfg    Config
	mu     sync.Mutex
}

// NewRunner spawns the codex app-server. Close it to stop the process. It
// requires the codex binary on PATH (or Config.CodexPath); auth and model
// config are codex's own (the codex CLI login/config).
func NewRunner(ctx context.Context, cfg Config) (*Runner, error) {
	c, err := sdk.New(ctx, sdk.Options{
		Spawn: sdk.SpawnOptions{
			CodexPath: cfg.CodexPath,
			Stderr:    os.Stderr,
		},
		// Headless: no human is present to approve command/file/permission
		// requests, so accept them (the sandbox mode still bounds what codex can do).
		ApprovalHandler: sdk.AutoApproveHandler{},
	})
	if err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	return &Runner{client: c, cfg: cfg}, nil
}

// RunTurn runs one turn against a codex thread. An empty threadID starts a new
// thread; otherwise the thread is resumed. developerInstructions steers codex
// (klein passes the active skill's prompt). Returns the thread id (freshly
// created when threadID was empty) and the final assistant text.
func (r *Runner) RunTurn(ctx context.Context, threadID, prompt, developerInstructions string) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	th, err := r.openThread(ctx, threadID, developerInstructions)
	if err != nil {
		return threadID, "", err
	}

	var opts *sdk.TurnOptions
	if r.cfg.Effort != "" {
		opts = &sdk.TurnOptions{Effort: sdk.ReasoningEffort(r.cfg.Effort)}
	}
	res, err := th.Run(ctx, prompt, opts)
	if err != nil {
		return th.ID(), "", fmt.Errorf("codex turn: %w", err)
	}
	return th.ID(), res.FinalResponse, nil
}

func (r *Runner) openThread(ctx context.Context, threadID, devInstr string) (*sdk.Thread, error) {
	if threadID == "" {
		th, err := r.client.StartThread(ctx, sdk.ThreadStartOptions{
			Model:                 r.cfg.Model,
			Cwd:                   r.cfg.Cwd,
			ApprovalPolicy:        r.approvalPolicy(),
			SandboxPolicy:         r.sandbox(),
			DeveloperInstructions: devInstr,
			Config:                r.threadConfig(),
		})
		if err != nil {
			return nil, fmt.Errorf("start codex thread: %w", err)
		}
		return th, nil
	}
	th, err := r.client.ResumeThread(ctx, sdk.ThreadResumeOptions{
		ThreadID:              threadID,
		Model:                 r.cfg.Model,
		Cwd:                   r.cfg.Cwd,
		ApprovalPolicy:        r.approvalPolicy(),
		Sandbox:               r.sandbox(),
		DeveloperInstructions: devInstr,
		Config:                r.threadConfig(),
	})
	if err != nil {
		return nil, fmt.Errorf("resume codex thread %s: %w", threadID, err)
	}
	return th, nil
}

func (r *Runner) threadConfig() map[string]any {
	if len(r.cfg.MCPServers) == 0 {
		return nil
	}
	return map[string]any{"mcp_servers": r.cfg.MCPServers}
}

func (r *Runner) approvalPolicy() string {
	if r.cfg.ApprovalPolicy != "" {
		return r.cfg.ApprovalPolicy
	}
	return sdk.ApprovalPolicyNever
}

func (r *Runner) sandbox() sdk.SandboxMode {
	if r.cfg.SandboxMode != "" {
		return sdk.SandboxMode(r.cfg.SandboxMode)
	}
	return sdk.SandboxModeWorkspaceWrite
}

// Close stops the codex app-server process. Safe to call on a nil Runner.
func (r *Runner) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Close()
}
