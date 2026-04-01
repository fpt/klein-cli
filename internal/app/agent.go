package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	pkgErrors "github.com/pkg/errors"

	"github.com/manifoldco/promptui"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/internal/permission"
	"github.com/fpt/klein-cli/internal/repository"
	"github.com/fpt/klein-cli/internal/skill"
	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/agent/events"
	"github.com/fpt/klein-cli/pkg/agent/react"
	"github.com/fpt/klein-cli/pkg/agent/state"
	"github.com/fpt/klein-cli/pkg/client"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
	"github.com/fpt/klein-cli/pkg/message"
)

// DefaultAgentMaxIterations is the default maximum iterations for agent execution.
const DefaultAgentMaxIterations = 10

// Agent handles skill-based tool management and sequential action execution.
type Agent struct {
	llmClient              domain.LLM
	allToolManagers        *tool.CompositeToolManager      // ALL tool managers combined
	todoToolManager        *tool.TodoToolManager
	taskToolManager        *tool.TaskToolManager
	askQuestionManager     *tool.AskUserQuestionToolManager
	planMode               *tool.PlanModeState             // shared with planToolManager and guard
	planToolManager        *tool.PlanToolManager
	spawnAgentManager      *tool.SpawnAgentToolManager
	fsRepo                 repository.FilesystemRepository  // Shared filesystem repository instance
	workingDir             string
	sharedState            domain.State
	skills                 skill.SkillMap
	sessionFilePath        string
	settings               *config.Settings
	logger                 *pkgLogger.Logger
	out                    io.Writer
	router                 *SkillsRouter
	thinkingStarted        bool
	sessionRules           *permission.RuleSet // in-memory allow/deny rules created during this session
	permRules              *permission.RuleSet // persistent allow/deny rules from JSON files
	allowedToolsOverride   []string            // CLI override for skill's allowed-tools
	externalEventHandler   events.EventHandler // optional: forward events to external consumers (e.g., Connect server)
	recentlyReadFiles      []string            // up to 5 most recently read unique file paths
}

// WorkingDir returns the agent's working directory.
func (a *Agent) WorkingDir() string { return a.workingDir }

// FilesystemRepository returns the shared filesystem repository instance.
func (a *Agent) FilesystemRepository() repository.FilesystemRepository { return a.fsRepo }

// SetAllowedToolsOverride sets a CLI-level override for the skill's allowed-tools.
// When non-empty, this list is used instead of the skill's own allowed-tools field.
func (a *Agent) SetAllowedToolsOverride(tools []string) {
	a.allowedToolsOverride = tools
}

// SetEventHandler sets an external event handler that receives all agent events.
// Used by the Connect server to translate events into streaming RPC responses.
func (a *Agent) SetEventHandler(handler events.EventHandler) {
	a.externalEventHandler = handler
}

// SetInteractiveInputHandler configures the AskUserQuestion tool with an
// interactive handler. Call this in interactive mode before the first Invoke.
// The handler receives the question and optional choices; it blocks until the
// user responds or an error occurs.
func (a *Agent) SetInteractiveInputHandler(h tool.UserInputHandler) {
	if a.askQuestionManager != nil {
		a.askQuestionManager.SetHandler(h)
	}
}

// SetPlanApprovalHandler configures interactive plan approval and wires up the
// clear-context callback so "Approve and clear planning context" works.
// Call this in interactive mode before the first Invoke.
func (a *Agent) SetPlanApprovalHandler(h tool.PlanApprovalHandler) {
	if a.planToolManager != nil {
		a.planToolManager.SetApprovalHandler(h)
		a.planToolManager.SetClearContextHandler(func() {
			a.sharedState.Clear()
		})
	}
}

// SpawnSubAgent runs a sub-agent with a fresh conversation state and returns its result.
// The sub-agent shares the parent's LLM client and tool managers but has its own
// message state and cannot spawn further agents (recursion prevention).
func (a *Agent) SpawnSubAgent(ctx context.Context, task string, skillName string, allowedTools []string, maxIterations int) (string, error) {
	skillName = strings.ToLower(skillName)
	if skillName == "" {
		skillName = "code"
	}
	activeSkill, exists := a.skills[skillName]
	if !exists {
		return "", fmt.Errorf("sub-agent skill %q not found", skillName)
	}

	// Build the effective allowed-tools list, always excluding spawn_agent to prevent recursion.
	var subToolManager domain.ToolManager
	effectiveAllowed := allowedTools
	if len(effectiveAllowed) == 0 {
		effectiveAllowed = activeSkill.AllowedTools
	}
	if len(effectiveAllowed) > 0 {
		filtered := make([]string, 0, len(effectiveAllowed))
		for _, name := range effectiveAllowed {
			if name != "spawn_agent" {
				filtered = append(filtered, name)
			}
		}
		subToolManager = skill.NewFilteredToolManager(a.allToolManagers, filtered)
	} else {
		// No restriction from skill or caller — allow all tools except spawn_agent.
		allTools := a.allToolManagers.GetTools()
		names := make([]string, 0, len(allTools))
		for name := range allTools {
			if name != "spawn_agent" {
				names = append(names, string(name))
			}
		}
		subToolManager = skill.NewFilteredToolManager(a.allToolManagers, names)
	}

	if maxIterations <= 0 {
		maxIterations = DefaultAgentMaxIterations
	}

	// Emit sub-agent start event on the parent's output.
	writer := a.OutWriter()
	fmt.Fprintf(writer, "  [sub-agent:%s] Starting: %s\n", skillName, task)

	// Fresh conversation state — isolated from the parent.
	subState := state.NewMessageState()

	// Inject skill system prompt.
	systemPrompt := activeSkill.RenderContent("", a.workingDir)
	if systemPrompt != "" {
		subState.AddMessage(message.NewSystemMessage(systemPrompt))
	}

	llmWithTools, err := client.NewClientWithToolManager(a.llmClient, subToolManager)
	if err != nil {
		return "", fmt.Errorf("sub-agent: failed to create LLM client: %w", err)
	}

	situation := NewIterationAdvisor(a.allToolManagers)
	reactClient, eventEmitter := react.NewReAct(llmWithTools, subToolManager, subState, situation, maxIterations)

	// Forward sub-agent events to our output with an indent prefix.
	eventEmitter.AddHandler(func(event events.AgentEvent) {
		switch event.Type {
		case events.EventTypeToolCallStart:
			if data, ok := event.Data.(events.ToolCallStartData); ok {
				fmt.Fprintf(writer, "  [sub-agent] Running tool %s %v\n", data.ToolName, data.Arguments)
			}
		case events.EventTypeToolResult:
			if data, ok := event.Data.(events.ToolResultData); ok {
				if data.IsError {
					fmt.Fprintf(writer, "  [sub-agent] ERROR %s\n", data.Content)
				}
			}
		case events.EventTypeThinkingChunk:
			if data, ok := event.Data.(events.ThinkingChunkData); ok {
				fmt.Fprintf(writer, "\x1b[90m%s", data.Content)
			}
		case events.EventTypeResponse:
			fmt.Fprint(writer, "\x1b[0m")
		}
		if a.externalEventHandler != nil {
			a.externalEventHandler(event)
		}
	})

	result, err := reactClient.Run(ctx, task)
	defer reactClient.Close()
	if err != nil {
		fmt.Fprintf(writer, "  [sub-agent:%s] Failed: %v\n", skillName, err)
		return "", err
	}

	resultText := result.Content()
	fmt.Fprintf(writer, "  [sub-agent:%s] Done\n", skillName)
	return resultText, nil
}

// NewAgent creates a new Agent with MCP tools and settings.
func NewAgent(llmClient domain.LLM, workingDir string, mcpToolManagers map[string]domain.ToolManager, settings *config.Settings, logger *pkgLogger.Logger, out io.Writer) *Agent {
	fsRepo := infra.NewOSFilesystemRepository()
	return NewAgentWithOptions(llmClient, workingDir, mcpToolManagers, settings, logger, out, false, true, fsRepo)
}

// NewAgentWithOptions creates a new Agent with session control options.
func NewAgentWithOptions(llmClient domain.LLM, workingDir string, mcpToolManagers map[string]domain.ToolManager, settings *config.Settings, logger *pkgLogger.Logger, out io.Writer, skipSessionRestore bool, isInteractiveMode bool, fsRepo repository.FilesystemRepository) *Agent {
	// Create individual tool managers
	var todoToolManager *tool.TodoToolManager
	var taskToolManager *tool.TaskToolManager
	if isInteractiveMode {
		todoToolManager = tool.NewTodoToolManager(workingDir)
		taskToolManager = tool.NewTaskToolManager(workingDir)
	} else {
		todoToolManager = tool.NewInMemoryTodoToolManager()
		taskToolManager = tool.NewInMemoryTaskToolManager()
	}

	fsConfig := infra.DefaultFileSystemConfig(workingDir)
	filesystemManager := tool.NewFileSystemToolManager(fsRepo, fsConfig, workingDir)

	bashConfig := tool.BashConfig{
		WorkingDir:          workingDir,
		MaxDuration:         2 * time.Minute,
		WhitelistedCommands: settings.Bash.WhitelistedCommands,
	}
	bashToolManager := tool.NewBashToolManager(bashConfig)

	searchToolManager := tool.NewSearchToolManager(tool.SearchConfig{WorkingDir: workingDir})
	webToolManager := tool.NewWebToolManager()
	pdfToolManager := tool.NewPDFToolManager(workingDir)

	// Load skills (embedded + filesystem) before creating tool managers
	skills, err := skill.LoadSkills(workingDir)
	if err != nil {
		logger.Warn("Failed to load skills, using empty fallback", "error", err)
		skills = make(skill.SkillMap)
	}

	// Create skill tool manager (provides ReadSkill tool)
	skillToolManager := tool.NewSkillToolManager(skills, workingDir)

	askQuestionManager := tool.NewAskUserQuestionToolManager()

	planModeState := new(tool.PlanModeState) // starts as PlanModeOff
	planToolManager := tool.NewPlanToolManager(planModeState)

	spawnAgentManager := tool.NewSpawnAgentToolManager()

	// Load persistent permission rules (user + project + local).
	// Missing files are silently ignored; never fatal.
	permRules := permission.LoadForProject(workingDir)

	// Combine ALL tool managers into one composite
	managers := []domain.ToolManager{todoToolManager, taskToolManager, filesystemManager, bashToolManager, searchToolManager, webToolManager, pdfToolManager, skillToolManager, askQuestionManager, planToolManager, spawnAgentManager}
	for _, mcpManager := range mcpToolManagers {
		managers = append(managers, mcpManager)
	}
	allToolManagers := tool.NewCompositeToolManager(managers...)

	// Create or restore shared message state with session persistence
	var sharedState domain.State
	var sessionFilePath string

	if isInteractiveMode {
		if userConfig, err := config.DefaultUserConfig(); err == nil {
			if sessionPath, err := userConfig.GetProjectSessionFile(workingDir); err == nil {
				sessionFilePath = sessionPath
				messageRepo := infra.NewMessageHistoryRepository(sessionFilePath)
				sharedState = state.NewMessageStateWithRepository(messageRepo)

				if !skipSessionRestore {
					if err := sharedState.LoadFromFile(); err != nil {
						logger.DebugWithIntention(pkgLogger.IntentionStatus, "Starting with new session",
							"reason", "could not load existing session", "error", err)
					} else {
						logger.DebugWithIntention(pkgLogger.IntentionStatus, "Restored session state",
							"message_count", len(sharedState.GetMessages()), "session_file", sessionFilePath)
					}
				} else {
					logger.DebugWithIntention(pkgLogger.IntentionStatus, "Starting with clean session",
						"reason", "session restore skipped for file mode")
				}
			} else {
				logger.Warn("Could not get session file path", "error", err)
				sharedState = state.NewMessageState()
			}
		} else {
			logger.Warn("Could not access user config for session persistence", "error", err)
			sharedState = state.NewMessageState()
		}
	} else {
		sharedState = state.NewMessageState()
		logger.DebugWithIntention(pkgLogger.IntentionStatus, "Starting with clean session", "reason", "one-shot mode")
	}

	a := &Agent{
		llmClient:          llmClient,
		allToolManagers:    allToolManagers,
		todoToolManager:    todoToolManager,
		taskToolManager:    taskToolManager,
		askQuestionManager: askQuestionManager,
		planMode:           planModeState,
		planToolManager:    planToolManager,
		spawnAgentManager:  spawnAgentManager,
		fsRepo:             fsRepo,
		workingDir:         workingDir,
		sharedState:        sharedState,
		skills:             skills,
		sessionFilePath:    sessionFilePath,
		settings:           settings,
		logger:             logger.WithComponent("agent"),
		out:                out,
		router:        NewSkillsRouter(),
		sessionRules:  newSessionRules(isInteractiveMode),
		permRules:     permRules,
	}

	// Wire spawn_agent callback (two-phase init: callback references the agent).
	spawnAgentManager.SetCallback(func(ctx context.Context, task string, skillName string, allowedTools []string, maxIterations int) (string, error) {
		return a.SpawnSubAgent(ctx, task, skillName, allowedTools, maxIterations)
	})

	return a
}

// Invoke executes a specified skill. Optional images are base64-encoded strings
// that get attached to the user message for vision-capable models.
func (a *Agent) Invoke(ctx context.Context, userInput string, skillName string, images ...string) (message.Message, error) {
	skillName = strings.ToLower(skillName)
	activeSkill, exists := a.skills[skillName]
	if !exists {
		return nil, fmt.Errorf("skill '%s' not found", skillName)
	}

	// Reset plan mode at the start of each invocation
	if a.planMode != nil {
		*a.planMode = tool.PlanModeOff
	}

	// Get filtered tool manager based on skill's allowed-tools or CLI override
	var filteredTools domain.ToolManager
	if len(a.allowedToolsOverride) > 0 {
		filteredTools = skill.NewFilteredToolManager(a.allToolManagers, a.allowedToolsOverride)
	} else {
		filteredTools = activeSkill.FilterTools(a.allToolManagers)
	}

	// Wrap with plan mode guard to block destructive operations during planning
	guard := tool.NewPlanModeGuard(filteredTools, a.planMode)
	toolManager := domain.ToolManager(guard)

	// Create LLM client with filtered tools
	llmWithTools, err := client.NewClientWithToolManager(a.llmClient, toolManager)
	if err != nil {
		return nil, fmt.Errorf("failed to create LLM client with tools: %w", err)
	}

	situation := NewIterationAdvisor(a.allToolManagers).
		WithRoutingHint(a.router.Route(userInput, skillName, a.sharedState.GetMessages()))

	maxIterations := DefaultAgentMaxIterations
	if a.settings != nil && a.settings.Agent.MaxIterations > 0 {
		maxIterations = a.settings.Agent.MaxIterations
	}
	reactClient, eventEmitter := react.NewReAct(llmWithTools, toolManager, a.sharedState, situation, maxIterations)
	a.setupEventHandlers(eventEmitter)

	// Tool result budgeting: offload large tool results to disk so they don't
	// permanently consume context window space. Only active in interactive/persistent
	// sessions where a project directory exists; one-shot mode keeps everything
	// in memory.
	if a.sessionFilePath != "" {
		projectDir := filepath.Dir(a.sessionFilePath)
		storage := tool.NewToolResultStorage(projectDir)
		reactClient.SetToolResultTransform(storage.MaybeOffload)
	}

	// Mandatory cleanup: remove stale situation messages and truncate old vision
	// content. Runs before catalog/prompt injection so dedup checks see a clean slate.
	if err := a.sharedState.CleanupMandatory(); err != nil {
		a.logger.Warn("Mandatory cleanup failed, continuing", "error", err)
	}

	// Inject skill catalog into system prompt
	catalogContent := skill.BuildSkillCatalog(a.skills)
	if catalogContent != "" {
		catalogMarker := "[[SKILL_CATALOG]]\n"
		catalogCandidate := catalogMarker + catalogContent

		var lastCatalog string
		for _, msg := range a.sharedState.GetMessages() {
			if msg.Type() == message.MessageTypeSystem && strings.HasPrefix(msg.Content(), catalogMarker) {
				lastCatalog = msg.Content()
			}
		}
		if lastCatalog == "" || lastCatalog != catalogCandidate {
			a.sharedState.AddMessage(message.NewSystemMessage(catalogCandidate))
		}
	}

	// Inject stable system prompt from skill content
	systemPrompt := activeSkill.RenderContent("", a.workingDir)
	if systemPrompt != "" {
		marker := fmt.Sprintf("[[SKILL_PROMPT:%s]]\n", skillName)
		candidate := marker + systemPrompt

		// Find the most recent matching marker message
		var lastMatched string
		for _, msg := range a.sharedState.GetMessages() {
			if msg.Type() == message.MessageTypeSystem && strings.HasPrefix(msg.Content(), marker) {
				lastMatched = msg.Content()
			}
		}

		if lastMatched == "" || lastMatched != candidate {
			a.sharedState.AddMessage(message.NewSystemMessage(candidate))
		}
	}

	// Build the user-facing prompt content
	userPrompt := userInput
	if a.todoToolManager != nil {
		if todosContext := a.todoToolManager.GetTodosForPrompt(); todosContext != "" {
			userPrompt = fmt.Sprintf("%s\n\n## Current Todos:\n%s\n\nUse TodoWrite tool to update todos as you progress.", userPrompt, todosContext)
		}
	}

	// Expand line-based @filename includes in the user prompt
	if strings.Contains(userPrompt, "@") {
		lines := strings.Split(userPrompt, "\n")
		out := make([]string, 0, len(lines))
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "@") {
				rel := strings.TrimSpace(strings.TrimPrefix(trimmed, "@"))
				if rel == "" {
					continue
				}
				var fullPath string
				if filepath.IsAbs(rel) {
					fullPath = rel
				} else {
					fullPath = filepath.Join(a.workingDir, rel)
				}
				if data, err := os.ReadFile(fullPath); err == nil {
					out = append(out,
						"----- BEGIN "+rel+" -----",
						string(data),
						"----- END "+rel+" -----",
					)
					continue
				}
				continue
			}
			out = append(out, line)
		}
		userPrompt = strings.Join(out, "\n")
	}

	// Token-based compaction: compact the conversation history if context usage
	// approaches the model's context window limit. Skipped for backends that handle
	// context overflow server-side (e.g. OpenAI Responses API with auto-truncation).
	if ssc, ok := a.llmClient.(domain.ServerSideCompactionLLM); !ok || !ssc.SupportsServerSideCompaction() {
		if cwp, ok := a.llmClient.(domain.ContextWindowProvider); ok {
			if maxCtx := cwp.MaxContextTokens(); maxCtx > 0 {
				compacted, compactErr := a.sharedState.CompactIfNeeded(ctx, a.llmClient, maxCtx, 0)
				if compactErr != nil {
					a.logger.Warn("Context compaction failed, continuing without compaction", "error", compactErr)
				}
				if compacted {
					a.postCompactRestore(ctx)
				}
			}
		}
	}

	result, err := reactClient.Run(ctx, userPrompt, images...)

	// Handle multiple approval workflows in sequence
	var approvalErrors []error
	for err != nil && pkgErrors.Is(err, react.ErrWaitingForApproval) {
		result, err = a.handleApprovalWorkflow(ctx, reactClient)
		if err != nil && !pkgErrors.Is(err, react.ErrWaitingForApproval) {
			approvalErrors = append(approvalErrors, err)
		}
	}

	if err != nil {
		if len(approvalErrors) > 0 {
			return nil, fmt.Errorf("action execution failed: %w", errors.Join(append(approvalErrors, err)...))
		}
		return nil, fmt.Errorf("action execution failed: %w", err)
	}
	defer reactClient.Close()

	// Save session state after successful interaction
	if a.sessionFilePath != "" {
		if saveErr := a.sharedState.SaveToFile(); saveErr != nil {
			a.logger.Warn("Failed to save session state",
				"session_file", a.sessionFilePath, "error", saveErr)
		}
	}

	return result, nil
}

// handleApprovalWorkflow handles the write confirmation workflow when the agent is waiting for approval.
func (a *Agent) handleApprovalWorkflow(ctx context.Context, reactClient domain.ReAct) (message.Message, error) {
	writer := a.OutWriter()

	var toolName, arg string
	if pending, ok := reactClient.GetPendingToolCall().(*message.ToolCallMessage); ok {
		toolName = string(pending.ToolName())
		arg = extractPermissionArg(toolName, pending.ToolArguments())
	}

	// 1. Session rules (in-memory, highest priority).
	if behavior, matched := a.sessionRules.Check(toolName, arg); matched {
		switch behavior {
		case permission.RuleAllow:
			fmt.Fprintf(writer, "Proceeding (session rule matched)...\n\n")
			return reactClient.Resume(ctx)
		case permission.RuleDeny:
			fmt.Fprintf(writer, "Cancelled (session deny rule matched).\n")
			reactClient.CancelPendingToolCall()
			return reactClient.Resume(ctx)
		}
	}

	// 2. Persistent rules from JSON files.
	if behavior, matched := a.permRules.Check(toolName, arg); matched {
		switch behavior {
		case permission.RuleAllow:
			fmt.Fprintf(writer, "Proceeding (allow rule matched)...\n\n")
			return reactClient.Resume(ctx)
		case permission.RuleDeny:
			fmt.Fprintf(writer, "Cancelled (deny rule matched).\n")
			reactClient.CancelPendingToolCall()
			return reactClient.Resume(ctx)
		}
	}

	// 3. Non-interactive stdin (pipe / script): auto-approve.
	lastMessage := reactClient.GetLastMessage()
	stat, err := os.Stdin.Stat()
	if err != nil || (stat.Mode()&os.ModeCharDevice) == 0 {
		fmt.Fprintf(writer, "\nAbout to write file(s):\n")
		fmt.Fprintf(writer, "%s\n", lastMessage.TruncatedString())
		fmt.Fprintf(writer, "Proceeding (non-interactive mode)...\n\n")
		return reactClient.Resume(ctx)
	}

	// 4. Interactive dialog.
	fmt.Fprintf(writer, "\nAbout to write file(s):\n")
	fmt.Fprintf(writer, "%s\n\n", lastMessage.TruncatedString())

	items := []string{"Yes", "Always (save to project)", "No"}

	prompt := promptui.Select{
		Label: "Proceed with this action?",
		Items: items,
		Templates: &promptui.SelectTemplates{
			Label:    "{{ . }}",
			Active:   "> {{ . | cyan }}",
			Inactive: "  {{ . }}",
			Selected: "{{ . }}",
		},
		Size: len(items),
	}

	_, result, err := prompt.Run()
	if err != nil {
		fmt.Fprintf(writer, "Input error, proceeding...\n\n")
		return reactClient.Resume(ctx)
	}

	switch result {
	case "Yes":
		fmt.Fprintf(writer, "Proceeding...\n\n")
		return reactClient.Resume(ctx)
	case "Always (save to project)":
		pattern := inferPattern(toolName, arg)
		rule := permission.PermissionRule{Tool: toolName, Pattern: pattern, Behavior: permission.RuleAllow}
		if saveErr := permission.AppendToProjectFile(a.workingDir, rule); saveErr != nil {
			fmt.Fprintf(writer, "Warning: could not save rule: %v\n", saveErr)
		} else {
			fmt.Fprintf(writer, "Rule saved to .klein/permissions.json (%s %s).\n", toolName, pattern)
		}
		// Also add to in-memory permRules so subsequent calls in this session are covered.
		a.permRules.Rules = append([]permission.PermissionRule{rule}, a.permRules.Rules...)
		fmt.Fprintf(writer, "Proceeding...\n\n")
		return reactClient.Resume(ctx)
	case "No":
		fmt.Fprintf(writer, "Cancelled.\n")
		reactClient.CancelPendingToolCall()
		return reactClient.Resume(ctx)
	default:
		fmt.Fprintf(writer, "Proceeding...\n\n")
		return reactClient.Resume(ctx)
	}
}

// extractPermissionArg returns the primary argument used for rule pattern matching.
// For file tools this is the path; for bash this is the command string.
// MultiEdit carries multiple paths — we return the first one; the caller may
// want to call Check per-path, but for the initial implementation one suffices.
func extractPermissionArg(toolName string, args message.ToolArgumentValues) string {
	switch toolName {
	case "Write", "Edit":
		if path, ok := args["path"].(string); ok {
			return path
		}
	case "MultiEdit":
		// edits is []interface{} where each element has "file_path"
		if edits, ok := args["edits"].([]interface{}); ok && len(edits) > 0 {
			if edit, ok := edits[0].(map[string]interface{}); ok {
				if fp, ok := edit["file_path"].(string); ok {
					return fp
				}
			}
		}
	case "Bash":
		if cmd, ok := args["command"].(string); ok {
			return cmd
		}
	}
	return ""
}

// newSessionRules returns the initial session rule set.
// In non-interactive mode (one-shot, file, server) all approval-requiring tools
// are pre-approved so the dialog never blocks a piped or scripted invocation.
func newSessionRules(isInteractive bool) *permission.RuleSet {
	if isInteractive {
		return &permission.RuleSet{}
	}
	tools := []string{"Write", "Edit", "MultiEdit", "Bash"}
	rules := make([]permission.PermissionRule, len(tools))
	for i, t := range tools {
		rules[i] = permission.PermissionRule{Tool: t, Pattern: "", Behavior: permission.RuleAllow}
	}
	return &permission.RuleSet{Rules: rules}
}

// inferPattern derives a suggested allow pattern from the tool name and its primary argument.
//
// For file tools the first path segment becomes a dir glob (e.g. "src/foo/bar.go" → "src/**").
// A root-level file with an extension gets a glob on the extension (e.g. "main.go" → "*.go").
// For Bash the first whitespace-delimited word(s) become a prefix wildcard (e.g. "go build ./..." → "go build *").
// Falls back to "*" (match all) when no useful structure is found.
func inferPattern(toolName, arg string) string {
	if arg == "" {
		return "*"
	}
	switch toolName {
	case "Write", "Edit", "MultiEdit":
		// Normalise to forward slashes and strip leading ./
		arg = strings.TrimPrefix(filepath.ToSlash(arg), "./")
		if idx := strings.Index(arg, "/"); idx > 0 {
			return arg[:idx] + "/**"
		}
		// Root-level file: glob on extension if present
		if dot := strings.LastIndex(arg, "."); dot > 0 {
			return "*" + arg[dot:]
		}
		return "*"
	case "Bash":
		// Use the first two words if there are at least two, otherwise one word
		words := strings.Fields(arg)
		if len(words) >= 2 {
			return words[0] + " " + words[1] + " *"
		}
		if len(words) == 1 {
			return words[0] + " *"
		}
		return "*"
	}
	return "*"
}

// EnablePersistence upgrades an in-memory agent to file-backed session persistence.
// Must be called before any Invoke. Loads existing history if the file exists.
func (a *Agent) EnablePersistence(filePath string) error {
	messageRepo := infra.NewMessageHistoryRepository(filePath)
	newState := state.NewMessageStateWithRepository(messageRepo)
	if err := newState.LoadFromFile(); err != nil {
		a.logger.Warn("Could not load existing session, starting fresh",
			"file", filePath, "error", err)
	}
	a.sharedState = newState
	a.sessionFilePath = filePath
	return nil
}

// ClearHistory clears the conversation history.
func (a *Agent) ClearHistory() {
	a.sharedState.Clear()
}

// InvokeWithOptions creates a ReAct client with all tools and configured maxIterations.
func (a *Agent) InvokeWithOptions(ctx context.Context, prompt string) (message.Message, error) {
	// Reset plan mode at the start of each invocation
	if a.planMode != nil {
		*a.planMode = tool.PlanModeOff
	}

	// Wrap all tools with plan mode guard
	guard := tool.NewPlanModeGuard(a.allToolManagers, a.planMode)

	llmWithTools, err := client.NewClientWithToolManager(a.llmClient, guard)
	if err != nil {
		return nil, fmt.Errorf("failed to create LLM client with tools: %w", err)
	}

	situation := NewIterationAdvisor(a.allToolManagers)

	maxIterations := DefaultAgentMaxIterations
	if a.settings != nil && a.settings.Agent.MaxIterations > 0 {
		maxIterations = a.settings.Agent.MaxIterations
	}
	reactClient, eventEmitter := react.NewReAct(llmWithTools, guard, a.sharedState, situation, maxIterations)
	a.setupEventHandlers(eventEmitter)

	result, err := reactClient.Run(ctx, prompt)

	var approvalErrors []error
	for err != nil && pkgErrors.Is(err, react.ErrWaitingForApproval) {
		result, err = a.handleApprovalWorkflow(ctx, reactClient)
		if err != nil && !pkgErrors.Is(err, react.ErrWaitingForApproval) {
			approvalErrors = append(approvalErrors, err)
		}
	}

	if err != nil {
		if len(approvalErrors) > 0 {
			return nil, errors.Join(append(approvalErrors, err)...)
		}
		return nil, err
	}

	return result, err
}

// GetConversationPreview returns a formatted preview of the last few messages.
func (a *Agent) GetConversationPreview(maxMessages int) string {
	messages := a.sharedState.GetMessages()
	if len(messages) == 0 {
		return ""
	}

	startIdx := 0
	if len(messages) > maxMessages {
		startIdx = len(messages) - maxMessages
	}

	recentMessages := messages[startIdx:]

	var preview strings.Builder
	preview.WriteString("Previous Conversation:\n")
	preview.WriteString(strings.Repeat("-", 50) + "\n")

	isFirstMessage := true
	for _, msg := range recentMessages {
		truncated := msg.TruncatedString()
		if truncated == "" {
			continue
		}
		if !isFirstMessage {
			preview.WriteString("\n")
		}
		isFirstMessage = false
		preview.WriteString(truncated + "\n")
	}

	preview.WriteString(strings.Repeat("-", 50) + "\n")
	return preview.String()
}

// GetMessageState returns the shared message state for context calculations.
func (a *Agent) GetMessageState() domain.State {
	return a.sharedState
}

// GetLLMClient returns the LLM client for context window estimation.
func (a *Agent) GetLLMClient() domain.LLM {
	return a.llmClient
}

// GetTaskSummary returns a compact one-line task status string, or "" when
// no tasks exist. Shown in the REPL status line above the prompt.
func (a *Agent) GetTaskSummary() string {
	if a.taskToolManager == nil {
		return ""
	}
	return a.taskToolManager.GetToolState()
}

// GetTaskListDisplay returns the full task list formatted for display, or "" if none.
func (a *Agent) GetTaskListDisplay() string {
	if a.taskToolManager == nil {
		return ""
	}
	result, err := a.taskToolManager.CallTool(context.Background(), "TaskList", nil)
	if err != nil || result.Error != "" {
		return ""
	}
	if result.Text == "No tasks." {
		return ""
	}
	return result.Text
}

// OutWriter returns the output writer used for streaming thinking/log lines.
func (a *Agent) OutWriter() io.Writer {
	if a.out != nil {
		return a.out
	}
	return os.Stdout
}

// setupEventHandlers configures event handlers to convert events back to output format.
func (a *Agent) setupEventHandlers(emitter events.EventEmitter) {
	emitter.AddHandler(func(event events.AgentEvent) {
		writer := a.OutWriter()
		if writer == nil {
			return
		}

		switch event.Type {
		case events.EventTypeToolCallStart:
			if data, ok := event.Data.(events.ToolCallStartData); ok {
				fmt.Fprintf(writer, "Running tool %s %v\n", data.ToolName, data.Arguments)
				if data.ToolName == "Read" {
					if path, ok := data.Arguments["file_path"].(string); ok && path != "" {
						a.recordRecentlyRead(path)
					}
				}
			}

		case events.EventTypeToolResult:
			if data, ok := event.Data.(events.ToolResultData); ok {
				if data.Content == "" {
					fmt.Fprintln(writer, "  (no output)")
				} else if data.IsError {
					lines := strings.Split(data.Content, "\n")
					for _, line := range lines {
						fmt.Fprintf(writer, "  ERROR %s\n", line)
					}
				} else {
					lines := strings.Split(data.Content, "\n")
					maxLines := 5
					if len(lines) > maxLines {
						fmt.Fprintf(writer, "  ...(%d more lines)\n", len(lines)-maxLines)
						lines = lines[len(lines)-maxLines:]
					}
					for _, line := range lines {
						if len(line) > 80 {
							line = line[:77] + "..."
						}
						fmt.Fprintf(writer, "  %s\n", line)
					}
				}
			}

		case events.EventTypeThinkingChunk:
			if data, ok := event.Data.(events.ThinkingChunkData); ok {
				if !a.thinkingStarted {
					fmt.Fprint(writer, "\x1b[90m💭 ")
					a.thinkingStarted = true
				}
				fmt.Fprintf(writer, "\x1b[90m%s", data.Content)
			}

		case events.EventTypeResponse:
			if a.thinkingStarted {
				fmt.Fprint(writer, "\x1b[0m\n")
				a.thinkingStarted = false
			}

		case events.EventTypeError:
			if data, ok := event.Data.(events.ErrorData); ok {
				fmt.Fprintf(writer, "Error: %v\n", data.Error)
			}
		}

		// Forward to external handler if set (e.g., Connect server)
		if a.externalEventHandler != nil {
			a.externalEventHandler(event)
		}
	})
}

// recordRecentlyRead records a file path as recently read, keeping only the 5
// most recent unique paths (most recently read first).
func (a *Agent) recordRecentlyRead(path string) {
	// Remove existing occurrence of path (dedup)
	out := a.recentlyReadFiles[:0]
	for _, p := range a.recentlyReadFiles {
		if p != path {
			out = append(out, p)
		}
	}
	// Prepend (most recently read first)
	a.recentlyReadFiles = append([]string{path}, out...)
	// Cap at 5
	if len(a.recentlyReadFiles) > 5 {
		a.recentlyReadFiles = a.recentlyReadFiles[:5]
	}
}

const (
	postCompactTokenBudget = 50_000
	postCompactMaxFiles    = 5
	postCompactMaxPerFile  = 5_000
)

// postCompactRestore re-injects recently-read files as situation messages so
// the agent retains working context immediately after a compaction.
func (a *Agent) postCompactRestore(ctx context.Context) {
	if len(a.recentlyReadFiles) == 0 {
		return
	}
	budget := postCompactTokenBudget
	count := 0
	for _, path := range a.recentlyReadFiles {
		if count >= postCompactMaxFiles || budget <= 0 {
			break
		}
		data, err := a.fsRepo.ReadFile(ctx, path)
		if err != nil {
			continue
		}
		content := string(data)
		tokens := len(content) / 4
		if tokens > postCompactMaxPerFile || tokens > budget {
			continue
		}
		msg := message.NewSituationSystemMessage(
			fmt.Sprintf("# Recently-read file (restored after compaction): %s\n%s", path, content))
		a.sharedState.AddMessage(msg)
		budget -= tokens
		count++
	}
	if count > 0 {
		a.logger.Info("Post-compact context restoration", "files_restored", count)
	}
}
