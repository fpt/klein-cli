package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/fpt/klein-cli/internal/app"
	"github.com/fpt/klein-cli/internal/config"
	connectserver "github.com/fpt/klein-cli/internal/connectrpc"
	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/internal/mcp"
	pluginpkg "github.com/fpt/klein-cli/internal/plugin"
	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	client "github.com/fpt/klein-cli/pkg/client"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
	"github.com/fpt/klein-cli/pkg/message"
)

// stringSliceFlag implements flag.Value for repeatable --plugin arguments.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string     { return strings.Join(*s, ", ") }
func (s *stringSliceFlag) Set(v string) error { *s = append(*s, v); return nil }

// resolveStringFlag returns the non-empty value, preferring short flag over long flag
func resolveStringFlag(shortVal, longVal string) string {
	if shortVal != "" {
		return shortVal
	}
	return longVal
}

func printUsage() {
	fmt.Println("klein - AI-powered coding agent with skill-based tool management")
	fmt.Println()
	fmt.Println("Available Skills:")
	fmt.Println("  code                    Comprehensive coding assistant (default)")
	fmt.Println()
	fmt.Println("Skills are loaded from:")
	fmt.Println("  Built-in (embedded)     Default skills bundled with the binary")
	fmt.Println("  .claude/skills/         Project-specific skills")
	fmt.Println("  ~/.claude/skills/       Personal skills (all projects)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  klein                                    # Interactive mode (code skill)")
	fmt.Println("  klein \"Create a HTTP server\"             # One-shot mode (code skill)")
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

	// Define command line flags
	var backend = flag.String("b", "", "LLM backend (ollama, anthropic, openai, or gemini)")
	var backendLong = flag.String("backend", "", "LLM backend (ollama, anthropic, openai, or gemini)")
	var model = flag.String("m", "", "Model name to use")
	var modelLong = flag.String("model", "", "Model name to use")
	var effort = flag.String("effort", "", "Reasoning effort for reasoning-capable models (none|minimal|low|medium|high|xhigh; primarily OpenAI)")
	var workdir = flag.String("workdir", "", "Working directory")
	var settingsPath = flag.String("settings", "", "Path to settings file")
	var skillFlag = flag.String("s", "code", "Skill to use (default: code)")
	var skillFlagLong = flag.String("skill", "code", "Skill to use (default: code)")
	var showLog = flag.Bool("l", false, "Print conversation message history and exit")
	var showLogLong = flag.Bool("log", false, "Print conversation message history and exit")
	var promptFile = flag.String("f", "", "File containing multi-turn prompts separated by '----' (no memory between turns)")
	var verbose = flag.Bool("v", false, "Enable verbose logging (debug level)")
	var verboseLong = flag.Bool("verbose", false, "Enable verbose logging (debug level)")
	var allowedTools = flag.String("allowed-tools", "", "Comma-separated list of allowed tools (overrides skill's allowed-tools)")
	var jsonSchema = flag.String("json-schema", "", "Inline JSON Schema string or path to a schema file; constrains the response to that schema (one-shot, no tools)")
	var serve = flag.Bool("serve", false, "Start Connect-gRPC server mode for gateway integration")
	var serveAddr = flag.String("serve-addr", ":50051", "Connect server listen address")
	var sessionsDir = flag.String("sessions-dir", "", "Directory for per-session persistence files (default: ~/.klein/claw/sessions/)")
	var memoryDir = flag.String("memory-dir", "", "Directory for memory files used by MemorySearch/MemoryGet tools (e.g., ~/.klein/claw/memory/)")
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
	resolvedSkill := strings.ToLower(resolveStringFlag(*skillFlag, *skillFlagLong))
	resolvedShowLog := *showLog || *showLogLong
	resolvedVerbose := *verbose || *verboseLong

	// Get remaining arguments as the command
	args := flag.Args()

	// Load settings
	settings, err := config.LoadSettings(*settingsPath)
	if err != nil {
		fmt.Printf("Warning: failed to load settings: %v\n", err)
		settings = config.GetDefaultSettings()
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
		// Register memory tools (serve mode only)
		if *memoryDir != "" {
			mcpToolManagers["memory"] = tool.NewMemoryToolManager(*memoryDir)
		}
		logger.Info("Starting Connect-gRPC server", "addr", *serveAddr)
		if err := connectserver.StartServer(ctx, *serveAddr, settings, mcpToolManagers, logger, *sessionsDir); err != nil {
			logger.Error("Server failed", "error", err)
			os.Exit(1)
		}
		return
	}

	a := app.NewAgentWithOptions(llmClient, workingDirectory, mcpToolManagers, settings, logger, out, skipSessionRestore, isInteractiveMode, fsRepo)

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
	fmt.Printf("Using skill: %s\n", resolvedSkill)

	// Handle multi-turn prompt file if specified
	if *promptFile != "" {
		executeMultiTurnFile(ctx, a, *promptFile, resolvedSkill)
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
		executeCommand(ctx, a, userInput, resolvedSkill)
	} else {
		app.StartInteractiveMode(ctx, a, resolvedSkill)
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
