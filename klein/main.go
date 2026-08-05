package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fpt/klein-cli/internal/agentserver"
	"github.com/fpt/klein-cli/internal/app"
	"github.com/fpt/klein-cli/internal/config"
	connectserver "github.com/fpt/klein-cli/internal/connectrpc"
	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/internal/mcp"
	pluginpkg "github.com/fpt/klein-cli/internal/plugin"
	"github.com/fpt/klein-cli/internal/skill"
	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/internal/tool/memorydb"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	client "github.com/fpt/klein-cli/pkg/client"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
	"github.com/fpt/klein-cli/pkg/message"
)

// stringSliceFlag implements flag.Value for repeatable --plugin arguments.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string     { return strings.Join(*s, ", ") }
func (s *stringSliceFlag) Set(v string) error { *s = append(*s, v); return nil }

// defaultRole is the role a session opens with when -r is not given.
const defaultRole = "code"

// validateRole rejects a -r that is not a role, before any expensive setup
// (LLM client, MCP servers) happens.
//
// Naming a skill is the mistake worth catching: skills and roles share a
// registry and a prompt format, so "klein -r pdf" would otherwise start
// perfectly happily on a prompt that was never meant to open a session.
func validateRole(name, workingDir string) error {
	roles, err := skill.LoadRoles(workingDir)
	if err != nil {
		return fmt.Errorf("failed to load roles: %w", err)
	}
	if r, ok := roles[name]; ok && r.IsRole() {
		return nil
	}

	available := strings.Join(skill.RoleNames(roles), ", ")
	if skills, err := skill.LoadSkills(workingDir); err == nil {
		if _, isSkill := skills[name]; isSkill {
			return fmt.Errorf("%q is a skill, not a role — roles start a session, "+
				"skills are used within one (roles: %s)", name, available)
		}
	}
	return fmt.Errorf("unknown role %q (roles: %s)", name, available)
}

// resolveStringFlag returns the non-empty value, preferring short flag over long flag
func resolveStringFlag(shortVal, longVal string) string {
	if shortVal != "" {
		return shortVal
	}
	return longVal
}

func printUsage() {
	fmt.Println("klein - AI-powered coding agent with role-based startup and skill-based tools")
	fmt.Println()
	fmt.Println("A role is the startup prompt a session opens with (-r). A skill is a task")
	fmt.Println("capability used within a session, reached by the model as it works.")
	fmt.Println()
	fmt.Println("Available Roles:")
	fmt.Println("  code                    Comprehensive coding assistant (default)")
	fmt.Println("  cad                     Fusion / KiCad / Blender CAD and EDA work")
	fmt.Println("  claw                    Messaging assistant (used by `klein claw`)")
	fmt.Println("  review                  AI code review (used by `klein review`)")
	fmt.Println()
	fmt.Println("Roles and skills are loaded from:")
	fmt.Println("  Built-in (embedded)     Bundled with the binary")
	fmt.Println("  .claude/roles|skills/   Project-specific")
	fmt.Println("  ~/.claude/roles|skills/ Personal (all projects)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  klein                                    # Interactive mode, fresh session (code role)")
	fmt.Println("  klein -r cad                             # Interactive mode in the cad role")
	fmt.Println("  klein -c                                 # Interactive mode, resume the most recent session")
	fmt.Println("  klein \"Create a HTTP server\"             # One-shot mode (code role)")
	fmt.Println("  klein -b anthropic \"Analyze this code\"   # Use Anthropic backend")
	fmt.Println("  klein -f prompts.txt                     # Multi-turn from file (no memory)")
	fmt.Println("  klein -v \"Debug this issue\"              # Enable verbose debug logging")
	fmt.Println("  klein -l                                 # Show conversation history")
	fmt.Println("  klein --json-schema '{\"type\":\"object\",...}' \"...\"  # Structured output (inline schema)")
	fmt.Println("  klein --json-schema schema.json \"...\"               # Structured output (schema file)")
	fmt.Println()
}

func main() {
	ctx := context.Background()

	// Subcommands are handled before flag parsing (flag treats them as a prompt).
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		os.Exit(runMCPCommand(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "claw" {
		os.Exit(runClawCommand(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "review" {
		os.Exit(runReviewCommand(os.Args[2:]))
	}

	// Define command line flags
	backend := flag.String("b", "", "LLM backend (openai, anthropic, gemini, codex, or appserver)")
	backendLong := flag.String("backend", "", "LLM backend (openai, anthropic, gemini, codex, or appserver)")
	var model = flag.String("m", "", "Model name to use")
	var modelLong = flag.String("model", "", "Model name to use")
	var effort = flag.String("effort", "", "Reasoning effort for reasoning-capable models (none|minimal|low|medium|high|xhigh; primarily OpenAI)")
	var workdir = flag.String("workdir", "", "Working directory")
	var settingsPath = flag.String("settings", "", "Path to settings file")
	var roleFlag = flag.String("r", defaultRole, "Role (startup prompt) to open the session with")
	var roleFlagLong = flag.String("role", defaultRole, "Role (startup prompt) to open the session with")
	var showLog = flag.Bool("l", false, "Print conversation message history and exit")
	var showLogLong = flag.Bool("log", false, "Print conversation message history and exit")
	var continueSession = flag.Bool("c", false, "Resume this project's most recent session (default: start fresh)")
	var continueSessionLong = flag.Bool("continue", false,
		"Resume this project's most recent session (default: start fresh)")
	var promptFile = flag.String("f", "", "File containing multi-turn prompts separated by '----' (no memory between turns)")
	var verbose = flag.Bool("v", false, "Enable verbose logging (debug level)")
	var verboseLong = flag.Bool("verbose", false, "Enable verbose logging (debug level)")
	var allowedTools = flag.String("allowed-tools", "", "Comma-separated list of allowed tools (overrides skill's allowed-tools)")
	var jsonSchema = flag.String("json-schema", "", "Inline JSON Schema string or path to a schema file; constrains the response to that schema (one-shot, no tools)")
	var serve = flag.Bool("serve", false, "Start Connect-gRPC server mode for gateway integration")
	var serveAddr = flag.String("serve-addr", ":50051", "Connect server listen address")
	var sessionsDir = flag.String("sessions-dir", "", "Directory for per-session persistence files (default: <base_dir>/sessions/)")
	var memoryDir = flag.String("memory-dir", "", "Directory for memory files used by MemorySearch/MemoryGet/MemoryWrite tools (serve mode; defaults to <base_dir>/memory/)")
	var schedulesFile = flag.String("schedules-file", "", "JSON file backing the ScheduleCreate/List/Delete tools (serve mode; defaults to <base_dir>/schedules.json)")
	var help = flag.Bool("h", false, "Show this help message")
	var helpLong = flag.Bool("help", false, "Show this help message")
	var pluginPaths stringSliceFlag
	flag.Var(&pluginPaths, "plugin", "Path to a Claude Code plugin directory (repeatable). Loads commands/, agents/, skills/, and .mcp.json from that plugin.")
	var pluginMarketplace = flag.String("plugin-marketplace", "", "Path to a directory containing .claude-plugin/marketplace.json — every plugin listed there is loaded.")

	// Custom usage function
	flag.Usage = func() {
		printUsage()
		fmt.Println("Flags:")
		flag.PrintDefaults()
	}

	// Parse flags
	flag.Parse()

	// Handle help flag
	if *help || *helpLong {
		flag.Usage()
		return
	}

	// Resolve long/short flag conflicts (prefer the one that was set)
	resolvedBackend := resolveStringFlag(*backend, *backendLong)
	resolvedModel := resolveStringFlag(*model, *modelLong)
	resolvedRole := strings.ToLower(resolveStringFlag(*roleFlag, *roleFlagLong))
	resolvedShowLog := *showLog || *showLogLong
	resolvedVerbose := *verbose || *verboseLong
	// --log prints the conversation history, which can only mean the session it
	// would resume — on a fresh one there is nothing to print.
	resolvedContinue := *continueSession || *continueSessionLong || resolvedShowLog

	// Get remaining arguments as the command
	args := flag.Args()

	// Load settings
	settings, err := config.LoadSettings(*settingsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintln(os.Stderr, "Fix the settings file (or pass --settings <path>) and try again.")
		os.Exit(1)
	}

	// Initialize structured logger based on settings
	logLevel := settings.Agent.LogLevel
	if resolvedVerbose {
		logLevel = "debug"
	}
	out := os.Stdout
	pkgLogger.SetGlobalLoggerWithConsoleWriter(pkgLogger.LogLevel(logLevel), out)
	logger := pkgLogger.NewLoggerWithConsoleWriter(pkgLogger.LogLevel(logLevel), out)

	if resolvedVerbose {
		logger.DebugWithIntention(pkgLogger.IntentionStatistics, "Verbose logging enabled", "log_level", logLevel)
	}

	// Override settings with command line arguments
	if resolvedBackend != "" {
		if resolvedModel == "" {
			settings.LLM = config.GetDefaultLLMSettingsForBackend(resolvedBackend)
		} else {
			backendDefaults := config.GetDefaultLLMSettingsForBackend(resolvedBackend)
			settings.LLM = backendDefaults
			settings.LLM.Model = resolvedModel
		}
	} else if resolvedModel != "" {
		settings.LLM.Model = resolvedModel
	}

	// Apply --effort override last so it wins over backend defaults.
	if *effort != "" {
		settings.LLM.Effort = strings.ToLower(*effort)
	}

	// Validate settings
	if err := config.ValidateSettings(settings); err != nil {
		logger.Error("Settings validation failed", "error", err)
		os.Exit(1)
	}

	// Create LLM client based on settings
	llmClient, err := client.NewLLMClient(settings.LLM)
	if err != nil {
		logger.Error("Failed to create LLM client", "error", err)
		os.Exit(1)
	}

	// Determine working directory
	workingDirectory := *workdir
	if workingDirectory != "" {
		if _, err := os.Stat(workingDirectory); err != nil {
			logger.Error("Working directory does not exist",
				"directory", workingDirectory, "error", err)
			os.Exit(1)
		}
		fmt.Printf("Working directory: %s\n", workingDirectory)
	} else {
		workingDirectory = "."
	}

	// Roles are resolved against the working directory, so this has to wait for
	// it — but it still runs before the LLM client and MCP servers are built, so
	// a typo costs nothing.
	if roleErr := validateRole(resolvedRole, workingDirectory); roleErr != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", roleErr)
		os.Exit(1)
	}

	// Load plugins. Plugin MCP servers are merged into settings.MCP.Servers
	// before MCP initialisation so plugin tools are available alongside
	// settings-defined servers. Commands/agents/skills are merged into the
	// agent after construction via RegisterPlugins.
	loadedPlugins := loadPluginsFromFlags(*pluginMarketplace, pluginPaths, logger)
	for _, p := range loadedPlugins {
		settings.MCP.Servers = append(settings.MCP.Servers, p.MCPServers...)
	}

	// Initialize MCP integration if any servers are enabled
	var mcpIntegration *mcp.Integration
	if hasEnabledMCPServers(settings.MCP.Servers) {
		fmt.Println("Initializing MCP Integration...")
		mcpIntegration = initializeMCP(ctx, settings.MCP, logger)
		if mcpIntegration != nil {
			defer mcpIntegration.Close()
		}
	}

	// Create shared FilesystemRepository instance
	fsRepo := infra.NewOSFilesystemRepository()

	// Initialize the agent
	skipSessionRestore := (*promptFile != "")
	isInteractiveMode := len(args) == 0 && *promptFile == ""

	mcpToolManagers := make(map[string]domain.ToolManager)
	if mcpIntegration != nil {
		toolManager := mcpIntegration.GetToolManager()
		serverNames := mcpIntegration.ListServers()
		for _, serverName := range serverNames {
			mcpToolManagers[serverName] = toolManager
		}
	}

	// Handle Connect-gRPC server mode
	if *serve {
		// Register memory tools (serve mode only). Default to the shared base
		// dir's memory directory so MemorySearch/MemoryGet/MemoryWrite work out
		// of the box for the gateway; override with --memory-dir.
		memDir := *memoryDir
		if memDir == "" {
			memDir = settings.MemoryDir()
		}
		mcpToolManagers["memory"] = tool.NewMemoryToolManager(memDir)
		logger.Info("Memory tools enabled", "dir", memDir)

		// Register schedule tools, backed by the dynamic schedule store. Defaults
		// to <base_dir>/schedules.json — the same file the gateway scheduler
		// watches — so ScheduleCreate/List/Delete work out of the box.
		schedFile := *schedulesFile
		if schedFile == "" {
			schedFile = settings.SchedulesFile()
		}
		mcpToolManagers["schedule"] = tool.NewScheduleToolManager(schedFile)
		logger.Info("Schedule tools enabled", "file", schedFile)

		// Register the versioned long-term memory (Remember/Recall/Reinforce),
		// backed by sqlite under the shared memory dir. Degrade gracefully if it
		// can't be opened, matching MCP tool behavior.
		kbPath := filepath.Join(memDir, "memory.sqlite")
		if kb, kbErr := memorydb.NewManager(kbPath); kbErr != nil {
			logger.Warn("Long-term memory (memorydb) disabled", "error", kbErr)
		} else {
			mcpToolManagers["memorydb"] = kb
			defer kb.Close()
			logger.Info("Long-term memory tools enabled", "file", kbPath)
		}

		// Session persistence defaults to <base_dir>/sessions.
		sessDir := *sessionsDir
		if sessDir == "" {
			sessDir = settings.SessionsDir()
		}

		// Whole-agent backend (codex/appserver): one shared app-server process for all sessions (headless).
		backendRunner, startErr := agentserver.Start(
			ctx, settings, workingDirectory, logger, agentserver.RunnerOptions{ApprovalPolicy: agentserver.ApprovalNever},
		)
		if startErr != nil {
			logger.Error("Failed to start agent backend", "error", startErr)
			os.Exit(1)
		}
		var agentBackend domain.AgentBackend
		if backendRunner != nil {
			agentBackend = agentserver.NewSharedBackend(backendRunner)
			defer backendRunner.Close()
		}

		logger.Info("Starting Connect-gRPC server", "addr", *serveAddr)
		if serveErr := connectserver.StartServer(
			ctx, *serveAddr, settings, mcpToolManagers, logger, sessDir, agentBackend,
		); serveErr != nil {
			logger.Error("Server failed", "error", serveErr)
			os.Exit(1)
		}
		return
	}

	// Whole-agent backend for interactive/one-shot CLI (plain `klein -b codex` or `-b appserver`).
	// Only the interactive REPL prompts for approvals; one-shot/file mode is headless.
	// agentserver.Select returns nil for every backend that needs no external process, so the factory just
	// runs the ReAct loop as usual.
	backendOpts := agentserver.RunnerOptions{ApprovalPolicy: agentserver.ApprovalNever}
	if isInteractiveMode {
		// auto_approve_commands answers the allowlisted requests before the prompt
		// ever reaches the terminal; everything else still asks.
		approver := agentserver.WithAutoApprove(
			settings.AutoApproveCommands, logger, terminalApprover(settings.LLM.Backend))
		backendOpts = agentserver.RunnerOptions{
			ApprovalPolicy: agentserver.ApprovalOnRequest,
			Approver:       approver,
		}
	}

	// Long-term memory (Remember/Recall/Reinforce tools + the /memory REPL
	// command) for the interactive REPL, backed by the shared sqlite store at
	// <base_dir>/memory/memory.sqlite. WAL + busy_timeout make concurrent klein
	// REPLs safe (concurrent readers, a serialized writer that waits rather than
	// errors). One-shot/file mode stays ephemeral. Degrade gracefully on failure.
	if isInteractiveMode {
		kbPath := settings.MemoryDBFile()
		if kb, kbErr := memorydb.NewManager(kbPath); kbErr != nil {
			logger.Warn("Long-term memory (memorydb) disabled", "error", kbErr)
		} else {
			mcpToolManagers["memorydb"] = kb
			defer kb.Close()
			logger.Info("Long-term memory tools enabled", "file", kbPath)
		}
	}

	a, cleanup, err := app.NewAgentWithOptions(ctx, app.AgentOptions{
		Settings:           settings,
		WorkingDir:         workingDirectory,
		MCPToolManagers:    mcpToolManagers,
		Logger:             logger,
		Out:                out,
		FsRepo:             fsRepo,
		SkipSessionRestore: skipSessionRestore,
		IsInteractiveMode:  isInteractiveMode,
		ContinueSession:    resolvedContinue,
		LLMClient:          llmClient,
		AgentBackend:       agentserver.Select(settings, logger, backendOpts),
	})
	if err != nil {
		logger.Error("Failed to create agent", "error", err)
		os.Exit(1)
	}
	defer cleanup()

	// Register loaded plugins with the agent so its skill catalog, command
	// dispatcher, and agent loader can see them.
	if len(loadedPlugins) > 0 {
		a.RegisterPlugins(loadedPlugins)
		fmt.Printf("Loaded %d plugin(s) — type / to discover commands.\n", len(loadedPlugins))
	}

	// Apply allowed-tools override if specified
	if *allowedTools != "" {
		tools := strings.Split(*allowedTools, ",")
		for i := range tools {
			tools[i] = strings.TrimSpace(tools[i])
		}
		a.SetAllowedToolsOverride(tools)
	}

	// Handle special command line options
	if resolvedShowLog {
		conversationHistory := a.GetConversationPreview(1000)
		if conversationHistory != "" {
			fmt.Println("Conversation History:")
			fmt.Println(strings.Repeat("=", 60))
			fmt.Print(conversationHistory)
			fmt.Println(strings.Repeat("=", 60))
		} else {
			fmt.Println("No conversation history found.")
		}
		return
	}

	// Show which skill is being used
	fmt.Printf("Using role: %s\n", resolvedRole)

	// Handle multi-turn prompt file if specified
	if *promptFile != "" {
		executeMultiTurnFile(ctx, a, *promptFile, resolvedRole)
		return
	}

	// JSON Schema mode: bypass skill/agent system, emit raw JSON to stdout.
	if *jsonSchema != "" {
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, "Error: --json-schema requires a prompt argument")
			os.Exit(1)
		}
		executeWithSchema(ctx, llmClient, strings.Join(args, " "), *jsonSchema)
		return
	}

	// Determine if we should run in interactive mode or one-shot mode
	if len(args) > 0 {
		userInput := strings.Join(args, " ")
		executeCommand(ctx, a, userInput, resolvedRole)
	} else {
		app.StartInteractiveMode(ctx, a, resolvedRole)
	}
}

func executeCommand(ctx context.Context, a *app.Agent, userInput string, skillName string) {
	fmt.Print("\n")

	var response message.Message
	var err error

	// One-shot input starting with `/` is dispatched as a plugin command if
	// it resolves; otherwise it falls through to a normal chat turn (which
	// is the right behaviour for free-form user prompts that happen to start
	// with /).
	if strings.HasPrefix(strings.TrimSpace(userInput), "/") {
		name, cmdArgs := app.SplitSlashCommand(userInput)
		if cmd, ambiguous := a.ResolveCommand(name); cmd != nil {
			response, err = a.InvokeCommand(ctx, cmd, cmdArgs, skillName)
		} else if ambiguous {
			fmt.Fprintf(os.Stderr, "Command %q is ambiguous; use /<plugin>:%s.\n", name, name)
			os.Exit(1)
		} else {
			response, err = a.Invoke(ctx, userInput, skillName)
		}
	} else {
		response, err = a.Invoke(ctx, userInput, skillName)
	}

	if err != nil {
		fmt.Printf("Command execution failed: %v\n", err)
		os.Exit(1)
	}

	w := a.OutWriter()
	model := a.GetLLMClient().ModelID()
	app.WriteResponseHeader(w, model, false)
	fmt.Fprintln(w, response.Content())
	printTokenUsage(a.GetLLMClient())
}

func executeMultiTurnFile(ctx context.Context, a *app.Agent, filePath string, skillName string) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Printf("Failed to read prompt file '%s': %v\n", filePath, err)
		os.Exit(1)
	}

	prompts := strings.Split(string(content), "----")
	if len(prompts) == 0 {
		fmt.Printf("No prompts found in file '%s'\n", filePath)
		os.Exit(1)
	}

	fmt.Printf("Executing %d turns from file: %s\n", len(prompts), filePath)
	fmt.Printf("Each turn will use skill: %s (memory preserved between turns)\n\n", skillName)

	for i, prompt := range prompts {
		prompt = strings.TrimSpace(prompt)
		if prompt == "" {
			continue
		}

		fmt.Printf("Turn %d/%d:\n", i+1, len(prompts))
		fmt.Printf("Prompt: %s\n", prompt)
		fmt.Print("\n")

		response, err := a.Invoke(ctx, prompt, skillName)
		if err != nil {
			fmt.Printf("Turn %d failed: %v\n", i+1, err)
			continue
		}

		w := a.OutWriter()
		model := a.GetLLMClient().ModelID()
		app.WriteResponseHeader(w, model, false)
		fmt.Fprintln(w, response.Content())
		fmt.Fprintf(w, "%s\n\n", strings.Repeat("-", 60))
		printTokenUsage(a.GetLLMClient())
	}

	fmt.Println("All turns completed.")
}

// executeWithSchema performs a one-shot structured output call using the provided
// JSON Schema. schemaArg may be an inline JSON string or a file path — inline is
// tried first; if it is not valid JSON the value is treated as a path.
// The agent/skill system is bypassed; the raw JSON result is written to stdout.
func executeWithSchema(ctx context.Context, llm domain.LLM, prompt string, schemaArg string) {
	var schema map[string]any

	// Try inline JSON first (matches Claude Code's --json-schema behaviour).
	if err := json.Unmarshal([]byte(schemaArg), &schema); err != nil {
		// Not valid JSON — treat as a file path.
		schemaBytes, readErr := os.ReadFile(schemaArg)
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %q is neither valid JSON nor a readable file: %v\n", schemaArg, readErr)
			os.Exit(1)
		}
		if err := json.Unmarshal(schemaBytes, &schema); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %q is not valid JSON: %v\n", schemaArg, err)
			os.Exit(1)
		}
	}

	result, err := client.InvokeWithSchema(ctx, llm, prompt, schema)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to format result: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}

// printTokenUsage prints a [usage] line to stderr if the client exposes token usage.
// The line is written to stderr so it does not pollute stdout output parsing in tests.
// Format: [usage] input=N output=N total=N cached=N
func printTokenUsage(llm domain.LLM) {
	provider, ok := llm.(domain.TokenUsageProvider)
	if !ok {
		return
	}
	usage, ok := provider.LastTokenUsage()
	if !ok {
		return
	}
	fmt.Fprintf(os.Stderr, "[usage] input=%d output=%d total=%d cached=%d cache_creation=%d\n",
		usage.InputTokens, usage.OutputTokens, usage.TotalTokens, usage.CachedTokens, usage.CacheCreationTokens)
}

// loadPluginsFromFlags loads plugins specified via --plugin-marketplace and
// --plugin flags. Errors are logged but never fatal — a broken plugin must
// not prevent klein from starting. Returns the successfully loaded plugins
// in the order: marketplace first, then individual --plugin arguments.
func loadPluginsFromFlags(marketplace string, pluginDirs []string, logger *pkgLogger.Logger) []*pluginpkg.Plugin {
	var out []*pluginpkg.Plugin

	if marketplace != "" {
		mp, err := pluginpkg.LoadMarketplace(marketplace)
		if err != nil {
			logger.Warn("Failed to load plugin marketplace", "path", marketplace, "error", err)
		} else {
			fmt.Printf("Loaded marketplace %q with %d plugin(s) from %s\n", mp.Name, len(mp.Plugins), marketplace)
			for _, p := range mp.Plugins {
				out = append(out, p)
			}
		}
	}

	for _, dir := range pluginDirs {
		p, err := pluginpkg.LoadPlugin(dir, "")
		if err != nil {
			logger.Warn("Failed to load plugin", "path", dir, "error", err)
			continue
		}
		out = append(out, p)
		fmt.Printf("Loaded plugin %q from %s\n", p.Name, dir)
	}

	return out
}

// terminalApprover prompts the user (y/N) for a backend's on-request approvals.
// It reads a line from stdin byte-by-byte so it doesn't buffer ahead of the
// REPL's readline (the two never read concurrently — the approver only runs
// mid-turn, while readline is idle).
func terminalApprover(backend string) agentserver.Approver {
	return func(req agentserver.ApprovalRequest) bool {
		fmt.Printf("\n🔐 %s wants to %s:\n    %s\nApprove? [y/N] ", backend, req.Kind, req.Summary)
		var b []byte
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if buf[0] == '\n' {
					break
				}
				if buf[0] != '\r' {
					b = append(b, buf[0])
				}
			}
			if err != nil {
				break
			}
		}
		ans := strings.ToLower(strings.TrimSpace(string(b)))
		return ans == "y" || ans == "yes"
	}
}

// hasEnabledMCPServers checks if there are any enabled MCP servers
func hasEnabledMCPServers(servers []domain.MCPServerConfig) bool {
	for _, server := range servers {
		if server.Enabled {
			return true
		}
	}
	return false
}

// initializeMCP initializes MCP integration with enabled servers from settings
func initializeMCP(ctx context.Context, mcpSettings config.MCPSettings, logger *pkgLogger.Logger) *mcp.Integration {
	integration := mcp.NewIntegration()

	var connectedServers []string
	var failedServers []string

	for _, serverConfig := range mcpSettings.Servers {
		if !serverConfig.Enabled {
			continue
		}

		if err := integration.AddServer(ctx, serverConfig); err != nil {
			logger.Warn("Failed to connect to MCP server",
				"server", serverConfig.Name, "error", err)
			failedServers = append(failedServers, serverConfig.Name)
		} else {
			connectedServers = append(connectedServers, serverConfig.Name)
		}
	}

	if len(connectedServers) > 0 {
		logger.DebugWithIntention(pkgLogger.IntentionSuccess, "Successfully connected to MCP servers",
			"servers", connectedServers)
	}
	if len(failedServers) > 0 {
		logger.Warn("Failed to connect to MCP servers",
			"servers", failedServers)
	}

	if len(connectedServers) == 0 {
		logger.Warn("No MCP servers connected")
		return nil
	}

	return integration
}
