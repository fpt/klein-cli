package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fpt/klein-cli/internal/app"
	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/internal/mcp"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/client/anthropic"
	"github.com/fpt/klein-cli/pkg/client/gemini"
	"github.com/fpt/klein-cli/pkg/client/ollama"
	"github.com/fpt/klein-cli/pkg/client/openai"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

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
	fmt.Println("  respond                 Direct knowledge-based responses and tool usage")
	fmt.Println()
	fmt.Println("Skills are loaded from:")
	fmt.Println("  Built-in (embedded)     Default skills bundled with the binary")
	fmt.Println("  .claude/skills/         Project-specific skills")
	fmt.Println("  ~/.claude/skills/       Personal skills (all projects)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  klein                                    # Interactive mode (code skill)")
	fmt.Println("  klein \"Create a HTTP server\"             # One-shot mode (code skill)")
	fmt.Println("  klein -s respond \"Explain Go channels\"   # Respond skill")
	fmt.Println("  klein -b anthropic \"Analyze this code\"   # Use Anthropic backend")
	fmt.Println("  klein -f prompts.txt                     # Multi-turn from file (no memory)")
	fmt.Println("  klein -v \"Debug this issue\"              # Enable verbose debug logging")
	fmt.Println("  klein -l                                 # Show conversation history")
	fmt.Println()
}

func main() {
	ctx := context.Background()

	// Define command line flags
	var backend = flag.String("b", "", "LLM backend (ollama, anthropic, openai, or gemini)")
	var backendLong = flag.String("backend", "", "LLM backend (ollama, anthropic, openai, or gemini)")
	var model = flag.String("m", "", "Model name to use")
	var modelLong = flag.String("model", "", "Model name to use")
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
	var help = flag.Bool("h", false, "Show this help message")
	var helpLong = flag.Bool("help", false, "Show this help message")

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

	// Validate settings
	if err := config.ValidateSettings(settings); err != nil {
		logger.Error("Settings validation failed", "error", err)
		os.Exit(1)
	}

	// Create LLM client based on settings
	var llmClient domain.LLM
	switch settings.LLM.Backend {
	case "anthropic", "claude":
		llmClient, err = anthropic.NewAnthropicClientWithTokens(settings.LLM.Model, settings.LLM.MaxTokens)
		if err != nil {
			logger.Error("Failed to create Anthropic client", "error", err)
			os.Exit(1)
		}
	case "openai":
		llmClient, err = openai.NewOpenAIClient(settings.LLM.Model, settings.LLM.MaxTokens)
		if err != nil {
			logger.Error("Failed to create OpenAI client", "error", err)
			os.Exit(1)
		}
	case "gemini":
		llmClient, err = gemini.NewGeminiClientWithTokens(settings.LLM.Model, settings.LLM.MaxTokens)
		if err != nil {
			logger.Error("Failed to create Gemini client", "error", err)
			os.Exit(1)
		}
	default:
		if !ollama.IsModelInKnownList(settings.LLM.Model) {
			logger.Warn("Model not in known capabilities list, testing tool calling capability",
				"model", settings.LLM.Model)

			testCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			hasToolCapability, testErr := ollama.DynamicCapabilityCheck(testCtx, settings.LLM.Model, false)
			if testErr != nil {
				logger.Warn("Failed to test model capability, proceeding without tool support",
					"model", settings.LLM.Model, "error", testErr)
			} else if !hasToolCapability {
				logger.Warn("Model does not support tool calling - limited functionality",
					"model", settings.LLM.Model, "suggestion", "consider using 'gpt-oss:latest'")
			} else {
				logger.InfoWithIntention(pkgLogger.IntentionSuccess, "Model supports tool calling, proceeding with full functionality",
					"model", settings.LLM.Model)
			}
		}

		llmClient, err = ollama.NewOllamaClient(settings.LLM.Model, settings.LLM.MaxTokens, settings.LLM.Thinking)
		if err != nil {
			logger.Error("Failed to create Ollama client", "error", err)
			os.Exit(1)
		}
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

	a := app.NewAgentWithOptions(llmClient, workingDirectory, mcpToolManagers, settings, logger, out, skipSessionRestore, isInteractiveMode, fsRepo)

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

	response, err := a.Invoke(ctx, userInput, skillName)
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
