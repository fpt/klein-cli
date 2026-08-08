package agentbackend

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/internal/tool/memorydb"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/agentserver"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// RunnerOptions carries mode-dependent behavior the settings file doesn't fix.
type RunnerOptions struct {
	// Approver decides on-request approvals (the repl prompts the user); nil for
	// headless modes, which auto-accept.
	Approver agentserver.Approver
	// ApprovalPolicy is the mode default ("never" for headless claw/serve,
	// "on-request" for the interactive repl). An explicit approval_policy in the
	// backend's settings block overrides it.
	ApprovalPolicy string
	// Logger reports app-server items klein has no renderer for. Set by
	// StartAgentBackend, the one place upstream that holds a logger; nil
	// elsewhere, and nil is silent.
	Logger *pkgLogger.Logger
}

// backendLogger converts klein's logger into the client's Logger interface.
//
// The nil check is the point. opts.Logger is a concrete *pkgLogger.Logger, and
// assigning a nil one straight into an interface field yields a non-nil
// interface holding a nil pointer — so the client's `logger != nil` guards would
// all pass and the first drift report would panic on a nil receiver. The
// conversion has to happen here, the last place the concrete type is visible.
func backendLogger(l *pkgLogger.Logger) agentserver.Logger {
	if l == nil {
		return nil
	}
	return l
}

// backendApprover passes an approver through to the client, or nothing when
// there is nobody to ask.
//
// isNil rather than `== nil` for the reason #101 turned up: an approver arriving
// as a typed-nil pointer is a non-nil interface, so the client's "nil accepts
// everything" guard would miss it and panic on the first approval — mid-turn,
// with a backend waiting on the answer.
func backendApprover(a agentserver.Approver) agentserver.Approver {
	if isNil(a) {
		return nil
	}
	return a
}

// defaultAppServerArgs is the conventional subcommand that puts an agent into
// app-server mode. appserver.args overrides it for a server that spells it
// differently.
var defaultAppServerArgs = []string{appServerSubcommand}

// appServerSubcommand is that subcommand name on its own.
const appServerSubcommand = "app-server"

// command resolves the binary and arguments for the configured backend. codex
// defaults to `codex` on PATH; the generic appserver backend has no default
// binary and must name one, since it stands for any conforming server.
//
// codex's launch line also carries the `-c` overrides for the config tables
// klein cannot state per-thread — see codexConfigArgs.
func command(settings *config.Settings) (string, []string, error) {
	switch settings.LLM.Backend {
	case config.BackendCodex:
		path := settings.Codex.CodexPath
		if path == "" {
			path = "codex"
		}
		if err := validateCodexConfig(settings.Codex); err != nil {
			return "", nil, err
		}
		args := append(slices.Clone(defaultAppServerArgs), codexConfigArgs(settings.Codex)...)
		return path, args, nil
	case config.BackendAppServer:
		path := settings.AppServer.Command
		if path == "" {
			return "", nil, errors.New(
				`backend "appserver" requires appserver.command in settings.toml (path to the app-server binary)`)
		}
		args := settings.AppServer.Args
		if len(args) == 0 {
			args = defaultAppServerArgs
		}
		return path, args, nil
	default:
		return "", nil, fmt.Errorf("backend %q is not an app-server backend", settings.LLM.Backend)
	}
}

// approvalPolicy resolves the policy the backend runs under: an explicit setting
// in the backend's own block wins over the mode default from opts.
func approvalPolicy(settings *config.Settings, opts RunnerOptions) string {
	explicit := settings.Codex.ApprovalPolicy
	if settings.LLM.Backend == config.BackendAppServer {
		explicit = settings.AppServer.ApprovalPolicy
	}
	if explicit != "" {
		return explicit
	}
	return opts.ApprovalPolicy
}

// NewRunnerFromSettings builds a Runner from klein settings + a working dir.
// Model/effort come from the llm block; the binary path and sandbox come from
// the optional "codex" or "appserver" block. opts supplies the mode's approval
// behavior. Two sets of tools are made reachable to a backend turn:
//   - klein's configured external MCP servers (translated to backend config), and
//   - klein's native tools (memory, schedule) registered as dynamic tools,
//     serviced in-process over the app-server JSON-RPC connection — so the
//     backend hits the same live tool-manager instances (same files, same locks).
func NewRunnerFromSettings(
	ctx context.Context, settings *config.Settings, workingDir string, opts RunnerOptions,
) (*agentserver.Runner, error) {
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

	// An app-server is configured from its environment, not a config flag — klein
	// stays in control of what reaches the child. When a server config TOML is
	// set, translate its [llm]/[agent] tables into the child's env.
	var env []string
	if settings.LLM.Backend == config.BackendAppServer && settings.AppServer.Config != "" {
		if env, err = appServerEnv(settings.AppServer.Config); err != nil {
			return nil, err
		}
	}

	runner, err := agentserver.NewRunner(ctx, agentserver.Config{
		Command:        path,
		Args:           args,
		Env:            env,
		Dialect:        dialectFor(settings.LLM.Backend),
		Model:          settings.LLM.Model,
		Effort:         settings.LLM.Effort,
		ApprovalPolicy: approvalPolicy(settings, opts),
		SandboxMode:    settings.Codex.SandboxMode,
		Cwd:            workingDir,
		MCPServers:     MCPServersConfig(settings.MCP.Servers),
		Tools:          newToolHost(nativeTools),
		Approver:       backendApprover(opts.Approver),
		Logger:         backendLogger(opts.Logger),
	})
	if err != nil {
		return nil, fmt.Errorf("starting the %s app-server: %w", settings.LLM.Backend, err)
	}
	return runner, nil
}
