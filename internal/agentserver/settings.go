package agentserver

import (
	"context"
	"fmt"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/internal/tool/memorydb"
	"github.com/fpt/klein-cli/pkg/agent/domain"
)

// RunnerOptions carries mode-dependent behavior the settings file doesn't fix.
type RunnerOptions struct {
	// Approver decides on-request approvals (the repl prompts the user); nil for
	// headless modes, which auto-accept.
	Approver Approver
	// ApprovalPolicy is the mode default ("never" for headless claw/serve,
	// "on-request" for the interactive repl). An explicit approval_policy in the
	// backend's settings block overrides it.
	ApprovalPolicy string
}

// command resolves the binary and arguments for the configured backend. Both
// backends expose their app-server under an `app-server` subcommand.
func command(settings *config.Settings) (string, []string, error) {
	var path string
	switch settings.LLM.Backend {
	case BackendCodex:
		if path = settings.Codex.CodexPath; path == "" {
			path = "codex"
		}
	case BackendKessel:
		if path = settings.Kessel.KesselPath; path == "" {
			path = "kessel-cli"
		}
	default:
		return "", nil, fmt.Errorf("backend %q is not an app-server backend", settings.LLM.Backend)
	}
	return path, []string{"app-server"}, nil
}

// approvalPolicy resolves the policy the backend runs under: an explicit setting
// in the backend's own block wins over the mode default from opts.
func approvalPolicy(settings *config.Settings, opts RunnerOptions) string {
	explicit := settings.Codex.ApprovalPolicy
	if settings.LLM.Backend == BackendKessel {
		explicit = settings.Kessel.ApprovalPolicy
	}
	if explicit != "" {
		return explicit
	}
	return opts.ApprovalPolicy
}

// NewRunnerFromSettings builds a Runner from klein settings + a working dir.
// Model/effort come from the llm block; the binary path and sandbox come from
// the optional "codex" or "kessel" block. opts supplies the mode's approval
// behavior. Two sets of tools are made reachable to a backend turn:
//   - klein's configured external MCP servers (translated to backend config), and
//   - klein's native tools (memory, schedule) registered as dynamic tools,
//     serviced in-process over the app-server JSON-RPC connection — so the
//     backend hits the same live tool-manager instances (same files, same locks).
func NewRunnerFromSettings(
	ctx context.Context, settings *config.Settings, workingDir string, opts RunnerOptions,
) (*Runner, error) {
	nativeManagers := []domain.ToolManager{
		tool.NewMemoryToolManager(settings.MemoryDir()),
		tool.NewScheduleToolManager(settings.SchedulesFile()),
	}
	// Versioned long-term memory (Remember/Recall/Reinforce) as embedded dynamic
	// tools. Degrade gracefully if the sqlite store can't be opened. The handle
	// lives for the backend process's lifetime (WAL auto-checkpoints).
	if kb, err := memorydb.NewManager(settings.MemoryDBFile()); err != nil {
		// No logger here; skip silently rather than fail backend startup.
		_ = err
	} else {
		nativeManagers = append(nativeManagers, kb)
	}
	nativeTools := tool.NewCompositeToolManager(nativeManagers...)

	path, args, err := command(settings)
	if err != nil {
		return nil, err
	}

	return NewRunner(ctx, Config{
		Command:        path,
		Args:           args,
		Backend:        settings.LLM.Backend,
		Model:          settings.LLM.Model,
		Effort:         settings.LLM.Effort,
		ApprovalPolicy: approvalPolicy(settings, opts),
		SandboxMode:    settings.Codex.SandboxMode,
		Cwd:            workingDir,
		MCPServers:     MCPServersConfig(settings.MCP.Servers),
		Tools:          nativeTools,
		Approver:       opts.Approver,
	})
}
