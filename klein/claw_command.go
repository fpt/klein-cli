package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/fpt/klein-cli/internal/app"
	"github.com/fpt/klein-cli/internal/codex"
	"github.com/fpt/klein-cli/internal/config"
	connectserver "github.com/fpt/klein-cli/internal/connectrpc"
	"github.com/fpt/klein-cli/internal/gateway"
	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/internal/mcp"
	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	client "github.com/fpt/klein-cli/pkg/client"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// runClawCommand implements `klein claw`, the messaging gateway (formerly the
// separate klein-claw binary). It reads the "claw" section of settings.json and
// derives all state (sessions, memory, schedule store) from the shared base dir,
// so `klein claw --settings other.json` — with a different base_dir and Discord
// token — runs a fully isolated instance.
//
// By default the gateway starts an embedded in-process agent server; set
// "agent_addr" in the claw block (or pass --agent-addr) to dial a remote
// `klein --serve` instead.
func runClawCommand(args []string) int {
	// Subcommands are detected before flag parsing so their own flags (e.g.
	// `repl --settings X`) parse — Go's flag package stops at the first
	// positional argument otherwise.
	if len(args) > 0 {
		switch args[0] {
		case "repl", "chat":
			return runClawREPL(args[1:])
		default:
			if !strings.HasPrefix(args[0], "-") {
				fmt.Printf("Unknown claw subcommand %q.\n\n%s\n", args[0], clawUsage)
				return 1
			}
		}
	}

	fs := flag.NewFlagSet("claw", flag.ContinueOnError)
	settingsPath := fs.String("settings", "", "Path to settings file (default: .agents/settings.json or ~/.klein/settings.json)")
	logLevel := fs.String("log-level", "info", "Log level (debug, info, warn, error)")
	agentAddr := fs.String("agent-addr", "", "Dial a remote klein --serve at this address instead of embedding the agent (overrides claw.agent_addr)")
	serveAddr := fs.String("serve-addr", "127.0.0.1:0", "Listen address for the embedded agent server (ephemeral loopback by default)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, clawUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := os.Stdout
	pkgLogger.SetGlobalLoggerWithConsoleWriter(pkgLogger.LogLevel(*logLevel), out)
	logger := pkgLogger.NewLoggerWithConsoleWriter(pkgLogger.LogLevel(*logLevel), out)

	// Load settings (LLM/MCP/Agent + the shared base dir + the claw block).
	settings, err := config.LoadSettings(*settingsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load settings: %v\n", err)
		return 1
	}

	baseDir := settings.ResolvedBaseDir()
	cfg, err := gateway.ParseClawConfig(settings.Claw, baseDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse claw config: %v\n", err)
		return 1
	}
	if *agentAddr != "" {
		cfg.AgentAddr = *agentAddr
	}

	// Embedded mode: no agent_addr → start the Connect server in-process on an
	// ephemeral loopback port and dial it. The embedded server's memory and
	// schedule tools share the gateway's derived paths so ScheduleCreate writes
	// the same file the scheduler watches.
	if cfg.AgentAddr == "" {
		mcpToolManagers, integration := buildClawToolManagers(ctx, settings, cfg, logger)
		if integration != nil {
			defer integration.Close()
		}

		codexWorkingDir := cfg.WorkingDir
		if codexWorkingDir == "" {
			codexWorkingDir = "."
		}
		codexRunner, startErr := codex.Start(
			ctx, settings, codexWorkingDir, logger, codex.RunnerOptions{ApprovalPolicy: codex.ApprovalNever},
		)
		if startErr != nil {
			fmt.Fprintf(os.Stderr, "Failed to start codex backend: %v\n", startErr)
			return 1
		}
		var agentBackend domain.AgentBackend
		if codexRunner != nil {
			agentBackend = codex.NewSharedBackend(codexRunner)
			defer codexRunner.Close()
		}

		bound, listenErr := connectserver.StartServerListener(
			ctx, *serveAddr, settings, mcpToolManagers, logger, cfg.SessionsDir, agentBackend,
		)
		if listenErr != nil {
			fmt.Fprintf(os.Stderr, "Failed to start embedded agent server: %v\n", listenErr)
			return 1
		}
		cfg.AgentAddr = "http://" + bound
	}

	gw, err := gateway.NewGateway(cfg, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create gateway: %v\n", err)
		return 1
	}
	defer gw.Close()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Info("Received signal, shutting down", "signal", sig)
		cancel()
	}()

	fmt.Println("klein claw gateway starting...")
	fmt.Printf("  Base dir: %s\n", baseDir)
	fmt.Printf("  Agent: %s\n", cfg.AgentAddr)
	fmt.Printf("  Skill: %s\n", cfg.DefaultSkill)
	if cfg.Discord.Token != "" {
		fmt.Println("  Discord: enabled")
	}
	if n := len(cfg.Schedules); n > 0 {
		fmt.Printf("  Schedules: %d configured\n", n)
	}
	fmt.Println()

	if err := gw.Run(ctx); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "Gateway error: %v\n", err)
		return 1
	}
	return 0
}

const clawUsage = `Usage:
  klein claw [--settings <path>] [--agent-addr <addr>] [--serve-addr <addr>]
  klein claw repl [--settings <path>] [--skill <name>]

Runs the messaging gateway. Configuration lives in the "claw" section of
settings.json; sessions, memory, and the schedule store are derived from the
shared "base_dir" (default ~/.klein).

  repl     Interactive terminal chat sharing claw's tools (memory, schedules,
           MCP) and backend, with its own session. Runs alongside the gateway —
           schedules/memory it changes are picked up via the shared base dir.`

// buildClawToolManagers assembles the MCP + memory + schedule tool managers the
// claw agent exposes, mirroring `klein --serve`. The returned integration (nil
// when no MCP servers are configured) must be Closed by the caller.
func buildClawToolManagers(ctx context.Context, settings *config.Settings, cfg *gateway.GatewayConfig, logger *pkgLogger.Logger) (map[string]domain.ToolManager, *mcp.Integration) {
	mcpToolManagers := make(map[string]domain.ToolManager)

	var integration *mcp.Integration
	if hasEnabledMCPServers(settings.MCP.Servers) {
		if integration = initializeMCP(ctx, settings.MCP, logger); integration != nil {
			toolManager := integration.GetToolManager()
			for _, name := range integration.ListServers() {
				mcpToolManagers[name] = toolManager
			}
		}
	}

	mcpToolManagers["memory"] = tool.NewMemoryToolManager(cfg.Memory.BaseDir)
	logger.Info("Memory tools enabled", "dir", cfg.Memory.BaseDir)
	mcpToolManagers["schedule"] = tool.NewScheduleToolManager(cfg.SchedulesFile)
	logger.Info("Schedule tools enabled", "file", cfg.SchedulesFile)

	return mcpToolManagers, integration
}

// runClawREPL runs an interactive terminal chat that shares claw's tools and
// backend (LLM, base dir) but keeps its own session — a local frontend for
// inspecting/curating memory and schedules. It does not start Discord or the
// scheduler; schedules it creates land in <base_dir>/schedules.json, which a
// running `klein claw` gateway live-reloads.
func runClawREPL(args []string) int {
	fs := flag.NewFlagSet("claw repl", flag.ContinueOnError)
	settingsPath := fs.String("settings", "", "Path to settings file (default: .agents/settings.json or ~/.klein/settings.json)")
	skillFlag := fs.String("skill", "claw", "Skill to use for the session")
	verbose := fs.Bool("v", false, "Enable verbose (debug) logging")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx := context.Background()
	out := os.Stdout

	settings, err := config.LoadSettings(*settingsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load settings: %v\n", err)
		return 1
	}

	logLevel := settings.Agent.LogLevel
	if *verbose {
		logLevel = "debug"
	}
	pkgLogger.SetGlobalLoggerWithConsoleWriter(pkgLogger.LogLevel(logLevel), out)
	logger := pkgLogger.NewLoggerWithConsoleWriter(pkgLogger.LogLevel(logLevel), out)

	if err := config.ValidateSettings(settings); err != nil {
		fmt.Fprintf(os.Stderr, "Settings validation failed: %v\n", err)
		return 1
	}

	baseDir := settings.ResolvedBaseDir()
	cfg, err := gateway.ParseClawConfig(settings.Claw, baseDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse claw config: %v\n", err)
		return 1
	}

	workingDir := cfg.WorkingDir
	if workingDir == "" {
		workingDir = "."
	}

	llmClient, err := client.NewLLMClient(settings.LLM)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create LLM client: %v\n", err)
		return 1
	}

	mcpToolManagers, integration := buildClawToolManagers(ctx, settings, cfg, logger)
	if integration != nil {
		defer integration.Close()
	}

	fsRepo := infra.NewOSFilesystemRepository()

	// Codex backend for the interactive claw REPL — prompts for on-request approvals.
	// codex.Select returns nil for non-codex backends, leaving the ReAct loop in place.
	codexOpts := codex.RunnerOptions{ApprovalPolicy: codex.ApprovalOnRequest, Approver: terminalApprover()}
	a, cleanup, err := app.NewAgentWithOptions(ctx, app.AgentOptions{
		Settings:          settings,
		WorkingDir:        workingDir,
		MCPToolManagers:   mcpToolManagers,
		Logger:            logger,
		Out:               out,
		FsRepo:            fsRepo,
		IsInteractiveMode: true,
		LLMClient:         llmClient,
		AgentBackend:      codex.Select(settings, logger, codexOpts),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create agent: %v\n", err)
		return 1
	}
	defer cleanup()

	fmt.Printf("klein claw — interactive (base dir: %s, skill: %s)\n", baseDir, *skillFlag)
	app.StartInteractiveMode(ctx, a, *skillFlag)
	return 0
}
