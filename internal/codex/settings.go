package codex

import (
	"context"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/tool"
)

// RunnerOptions carries mode-dependent behavior the settings file doesn't fix.
type RunnerOptions struct {
	// Approver decides on-request approvals (the repl prompts the user); nil for
	// headless modes, which auto-accept.
	Approver Approver
	// ApprovalPolicy is the mode default ("never" for headless claw/serve,
	// "on-request" for the interactive repl). An explicit codex.approval_policy
	// in settings overrides it.
	ApprovalPolicy string
}

// NewRunnerFromSettings builds a Runner from klein settings + a working dir.
// Model/effort come from the llm block; sandbox/codex_path from the optional
// "codex" block. opts supplies the mode's approval behavior. Two sets of tools
// are made reachable to codex-backed turns:
//   - klein's configured external MCP servers (translated to codex config), and
//   - klein's native tools (memory, schedule) registered as codex dynamic tools,
//     serviced in-process over the app-server JSON-RPC connection — so codex hits
//     the same live tool-manager instances (same files, same locks).
func NewRunnerFromSettings(
	ctx context.Context, settings *config.Settings, workingDir string, opts RunnerOptions,
) (*Runner, error) {
	nativeTools := tool.NewCompositeToolManager(
		tool.NewMemoryToolManager(settings.MemoryDir()),
		tool.NewScheduleToolManager(settings.SchedulesFile()),
	)

	// Explicit codex.approval_policy wins; otherwise use the mode default.
	approvalPolicy := settings.Codex.ApprovalPolicy
	if approvalPolicy == "" {
		approvalPolicy = opts.ApprovalPolicy
	}

	return NewRunner(ctx, Config{
		CodexPath:      settings.Codex.CodexPath,
		Model:          settings.LLM.Model,
		Effort:         settings.LLM.Effort,
		ApprovalPolicy: approvalPolicy,
		SandboxMode:    settings.Codex.SandboxMode,
		Cwd:            workingDir,
		MCPServers:     MCPServersConfig(settings.MCP.Servers),
		Tools:          nativeTools,
		Approver:       opts.Approver,
	})
}
