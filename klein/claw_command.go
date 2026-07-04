package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/fpt/klein-cli/internal/config"
	connectserver "github.com/fpt/klein-cli/internal/connectrpc"
	"github.com/fpt/klein-cli/internal/gateway"
	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/pkg/agent/domain"
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
	// `migrate --settings X`) parse — Go's flag package stops at the first
	// positional argument otherwise.
	if len(args) > 0 {
		switch args[0] {
		case "migrate":
			return clawMigrate(args[1:])
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
		mcpToolManagers := buildClawToolManagers(ctx, settings, cfg, logger)
		bound, err := connectserver.StartServerListener(ctx, *serveAddr, settings, mcpToolManagers, logger, cfg.SessionsDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start embedded agent server: %v\n", err)
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
  klein claw migrate [--settings <path>]

Runs the messaging gateway. Configuration lives in the "claw" section of
settings.json; sessions, memory, and the schedule store are derived from the
shared "base_dir" (default ~/.klein).

  migrate  Fold a legacy ~/.klein/claw/config.json into settings.json and move
           its memory/sessions/schedules into the base dir.`

// buildClawToolManagers assembles the MCP + memory + schedule tool managers the
// embedded agent server exposes to the claw agent, mirroring `klein --serve`.
func buildClawToolManagers(ctx context.Context, settings *config.Settings, cfg *gateway.GatewayConfig, logger *pkgLogger.Logger) map[string]domain.ToolManager {
	mcpToolManagers := make(map[string]domain.ToolManager)

	if hasEnabledMCPServers(settings.MCP.Servers) {
		if integration := initializeMCP(ctx, settings.MCP, logger); integration != nil {
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

	return mcpToolManagers
}

// clawMigrate folds a legacy ~/.klein/claw/config.json into the claw section of
// settings.json and relocates its state directories into the base dir.
func clawMigrate(args []string) int {
	fs := flag.NewFlagSet("claw migrate", flag.ContinueOnError)
	settingsPathFlag := fs.String("settings", "", "Path to settings file to migrate into")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	settingsPath := *settingsPathFlag

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot resolve home directory: %v\n", err)
		return 1
	}
	oldDir := filepath.Join(home, ".klein", "claw")
	oldCfg := filepath.Join(oldDir, "config.json")

	raw, err := os.ReadFile(oldCfg)
	if err != nil {
		fmt.Printf("No legacy config at %s — nothing to migrate.\n", oldCfg)
		return 0
	}

	// Strip the retired path/heartbeat keys; keep only behavior in the claw block.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		fmt.Fprintf(os.Stderr, "Legacy config %s is not valid JSON: %v\n", oldCfg, err)
		return 1
	}
	delete(m, "sessions_dir")
	delete(m, "schedules_file")
	delete(m, "heartbeat")
	if mem, ok := m["memory"]; ok {
		var mm map[string]json.RawMessage
		if json.Unmarshal(mem, &mm) == nil {
			delete(mm, "base_dir")
			if len(mm) == 0 {
				delete(m, "memory")
			} else if b, err := json.Marshal(mm); err == nil {
				m["memory"] = b
			}
		}
	}
	clawBlock, err := json.Marshal(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to build claw block: %v\n", err)
		return 1
	}

	settings, err := config.LoadSettings(settingsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load settings: %v\n", err)
		return 1
	}
	settings.Claw = clawBlock
	if err := settings.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to save settings: %v\n", err)
		return 1
	}
	fmt.Println("Migrated claw config into settings.json (\"claw\" section).")

	// Relocate state into the base dir when the destination is free.
	base := settings.ResolvedBaseDir()
	moves := []struct{ from, to string }{
		{filepath.Join(oldDir, "memory"), filepath.Join(base, "memory")},
		{filepath.Join(oldDir, "sessions"), filepath.Join(base, "sessions")},
		{filepath.Join(oldDir, "schedules.json"), filepath.Join(base, "schedules.json")},
	}
	for _, mv := range moves {
		if _, err := os.Stat(mv.from); err != nil {
			continue
		}
		if _, err := os.Stat(mv.to); err == nil {
			fmt.Printf("  Skipped %s → %s (destination exists; move manually)\n", mv.from, mv.to)
			continue
		}
		if err := os.Rename(mv.from, mv.to); err != nil {
			fmt.Printf("  Could not move %s → %s: %v\n", mv.from, mv.to, err)
			continue
		}
		fmt.Printf("  Moved %s → %s\n", mv.from, mv.to)
	}
	fmt.Printf("\nReview the migrated Discord token in %s and delete %s once verified.\n", settingsPath, oldDir)
	return 0
}
