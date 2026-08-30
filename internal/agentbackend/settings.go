package agentbackend

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

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
	// ProxiedTools are already-connected tool managers the caller wants offered
	// to the backend on top of the ones settings can describe — in practice
	// klein's live MCP servers, selected by appserver.proxy_mcp_servers.
	//
	// They arrive as managers rather than as config because that is the whole
	// point: settings can only say how to *start* an MCP server, and a started
	// copy on the far side of a socket is a different server on a different
	// machine. Passing the live manager makes the backend's call land on the
	// connection klein already holds.
	ProxiedTools []domain.ToolManager
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

// dialAddress returns the address of an app-server klein should dial rather than
// spawn, or "" when there is none. Only the generic appserver backend has one:
// codex is a local CLI klein launches.
func dialAddress(settings *config.Settings) string {
	if settings.LLM.Backend != config.BackendAppServer {
		return ""
	}
	return settings.AppServer.Address
}

// validateDialedAppServer rejects settings that only mean something for a server
// klein starts itself. Ignoring them silently would be the worse failure: a user
// who set appserver.env believes they chose a model, and the process that reads
// it is on another machine entirely.
func validateDialedAppServer(s config.AppServerSettings) error {
	var stray []string
	if s.Command != "" {
		stray = append(stray, "command")
	}
	if len(s.Args) > 0 {
		stray = append(stray, "args")
	}
	if len(s.Env) > 0 {
		stray = append(stray, "env")
	}
	//nolint:staticcheck // SA1019: deprecated, but still honored — and still meaningless when dialing.
	if s.Config != "" {
		stray = append(stray, "config")
	}
	if len(stray) == 0 {
		return nil
	}
	return fmt.Errorf(
		"appserver.address = %q dials an app-server klein does not start, so appserver.%s "+
			"would have no effect: they configure a spawned server. Remove them, or set them "+
			"where that server runs",
		s.Address, strings.Join(stray, "/"))
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

// offeredManagers assembles the tool managers klein exposes to a backend turn.
//
// The workspace set is opt-in because it is a substitution rather than an
// addition: a server resolving a call to the first exact name match will run
// klein's Read/Write/Bash in place of the ones it shipped with. Where the server
// has good tools of its own and runs on the same machine, that only moves the
// work; it earns its keep when the server is somewhere else.
func offeredManagers(settings *config.Settings, workingDir string, proxied []domain.ToolManager) []domain.ToolManager {
	managers := []domain.ToolManager{
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
		managers = append(managers, kb)
	}
	if wantsWorkspaceTools(settings) {
		managers = append(managers, newWorkspaceTools(settings, workingDir))
	}
	// Live managers from the caller (klein's connected MCP servers). Skipped when
	// nil so a caller with no MCP integration passes nothing rather than a typed
	// nil that would panic on enumeration.
	for _, m := range proxied {
		if !isNil(m) {
			managers = append(managers, m)
		}
	}
	return managers
}

// wantsWorkspaceTools reports whether klein should supply the workspace tools.
//
// The default follows the transport, because the server's own behavior does. A
// dialed server lends no filesystem or shell tools of its own — over a socket
// that carries no identity, its built-ins would act with the privileges of
// whoever started it, for whoever happened to connect — so klein's are the only
// hands the model has, and they are on unless the user says otherwise. A spawned
// server is klein's own child with klein's privileges and working tools already,
// so there klein's are a substitution and stay off until asked for.
//
// Appserver-only, like dialAddress: the setting lives in that block, and codex
// brings its own shell and apply_patch that klein has no business shadowing.
func wantsWorkspaceTools(settings *config.Settings) bool {
	if settings.LLM.Backend != config.BackendAppServer {
		return false
	}
	if explicit := settings.AppServer.WorkspaceTools; explicit != nil {
		return *explicit
	}
	return settings.AppServer.Address != ""
}

// validateWorkspaceTools rejects the one combination that cannot work: a dialed
// server, told explicitly not to expect klein's tools.
//
// Nothing on the other end will cover for it. The server lends none of its own,
// so the model gets a turn with no way to read, write or run anything — a
// session that starts cleanly and then fails at the first useful thing it tries.
// Better to say so at startup, where the setting that caused it is still in
// view.
func validateWorkspaceTools(s config.AppServerSettings) error {
	if s.Address == "" || s.WorkspaceTools == nil || *s.WorkspaceTools {
		return nil
	}
	return fmt.Errorf(
		"appserver.workspace_tools = false leaves the app-server at %s with no tools: a server reached "+
			"over the network lends none of its own (its built-ins would run as the user it was started "+
			"as, for whoever connects), so klein has to supply them. Remove the setting, or spawn the "+
			"server locally with appserver.command instead",
		s.Address)
}

// NewRunnerFromSettings builds a Runner from klein settings + a working dir.
// Model/effort come from the llm block; the binary path and sandbox come from
// the optional "codex" or "appserver" block. opts supplies the mode's approval
// behavior. Three sets of tools are made reachable to a backend turn:
//   - klein's configured external MCP servers (translated to backend config),
//   - klein's native tools (memory, schedule) registered as dynamic tools,
//     serviced in-process over the app-server JSON-RPC connection — so the
//     backend hits the same live tool-manager instances (same files, same
//     locks), and
//   - klein's workspace tools (Read/Write/Edit/…/Bash), when
//     appserver.workspace_tools says so — the client half of a server serving
//     none of its own. See newWorkspaceTools.
func NewRunnerFromSettings(
	ctx context.Context, settings *config.Settings, workingDir string, opts RunnerOptions,
) (*agentserver.Runner, error) {
	nativeTools := tool.NewCompositeToolManager(offeredManagers(settings, workingDir, opts.ProxiedTools)...)

	// Dialed or spawned: an address names a server running somewhere else, which
	// klein neither launches nor configures, so none of the launch settings apply.
	var (
		path, address string
		args, env     []string
	)
	if address = dialAddress(settings); address != "" {
		if err := validateDialedAppServer(settings.AppServer); err != nil {
			return nil, err
		}
		if err := validateWorkspaceTools(settings.AppServer); err != nil {
			return nil, err
		}
	} else {
		var err error
		if path, args, err = command(settings); err != nil {
			return nil, err
		}
		if env, err = appServerEnvironment(settings, opts.Logger); err != nil {
			return nil, err
		}
	}

	warnUnproxiedStdioServers(opts.Logger, settings.MCP.Servers, settings.AppServer.ProxyMCPServers, address)

	// Wrapped, not raw: klein now runs tools the backend used to run itself, and
	// the approval that came with them has to come from somewhere.
	offeredTools := withApproval(newToolHost(nativeTools), backendApprover(opts.Approver))
	logOfferedTools(opts.Logger, offeredTools)
	// Last check before the connection: what was actually assembled, not what the
	// settings asked for.
	if err := verifyWorkspaceToolsOffered(address, offeredTools); err != nil {
		return nil, err
	}

	runner, err := agentserver.NewRunner(ctx, agentserver.Config{
		Command:        path,
		Address:        address,
		Args:           args,
		Env:            env,
		Dialect:        dialectFor(settings.LLM.Backend),
		Model:          settings.LLM.Model,
		Effort:         settings.LLM.Effort,
		ApprovalPolicy: approvalPolicy(settings, opts),
		SandboxMode:    settings.Codex.SandboxMode,
		Cwd:            workingDir,
		MCPServers:     MCPServersConfig(settings.MCP.Servers, settings.AppServer.ProxyMCPServers),
		Tools:          offeredTools,
		Approver:       backendApprover(opts.Approver),
		Logger:         backendLogger(opts.Logger),
	})
	if err != nil {
		return nil, fmt.Errorf("starting the %s app-server: %w", settings.LLM.Backend, err)
	}
	return runner, nil
}
