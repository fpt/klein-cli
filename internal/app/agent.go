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
	alwaysApprove          bool
	permRules              *permission.RuleSet // persistent allow/deny rules from JSON files
	allowedToolsOverride   []string            // CLI override for skill's allowed-tools
	externalEventHandler   events.EventHandler // optional: forward events to external consumers (e.g., Connect server)
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

// SetInteractiveInputHandler configures the ask_user_question tool with an
// interactive handler. Call this in interactive mode before the first Invoke.
// The handler receives the question and optional choices; it blocks until the
// user responds or an error occurs.
func (a *Agent) SetInteractiveInputHandler(h tool.UserInputHandler) {
	if a.askQuestionManager != nil {
		a.askQuestionManager.SetHandler(h)
	}
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
	alwaysApprove := false
	if isInteractiveMode {
		todoToolManager = tool.NewTodoToolManager(workingDir)
		taskToolManager = tool.NewTaskToolManager(workingDir)
	} else {
		todoToolManager = tool.NewInMemoryTodoToolManager()
		taskToolManager = tool.NewInMemoryTaskToolManager()
		alwaysApprove = true
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

	// Create skill tool manager (provides read_skill tool)
	skillToolManager := tool.NewSkillToolManager(skills, workingDir)

	askQuestionManager := tool.NewAskUserQuestionToolManager()

	// Load persistent permission rules (user + project + local).
	// Missing files are silently ignored; never fatal.
	permRules := permission.LoadForProject(workingDir)

	// Combine ALL tool managers into one composite
	managers := []domain.ToolManager{todoToolManager, taskToolManager, filesystemManager, bashToolManager, searchToolManager, webToolManager, pdfToolManager, skillToolManager, askQuestionManager}
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

	return &Agent{
		llmClient:          llmClient,
		allToolManagers:    allToolManagers,
		todoToolManager:    todoToolManager,
		taskToolManager:    taskToolManager,
		askQuestionManager: askQuestionManager,
		fsRepo:             fsRepo,
		workingDir:         workingDir,
		sharedState:        sharedState,
		skills:             skills,
		sessionFilePath:    sessionFilePath,
		settings:           settings,
		logger:             logger.WithComponent("agent"),
		out:                out,
		router:             NewSkillsRouter(),
		alwaysApprove:      alwaysApprove,
		permRules:          permRules,
	}
}

// Invoke executes a specified skill. Optional images are base64-encoded strings
// that get attached to the user message for vision-capable models.
func (a *Agent) Invoke(ctx context.Context, userInput string, skillName string, images ...string) (message.Message, error) {
	skillName = strings.ToLower(skillName)
	activeSkill, exists := a.skills[skillName]
	if !exists {
		return nil, fmt.Errorf("skill '%s' not found", skillName)
	}

	// Get filtered tool manager based on skill's allowed-tools or CLI override
	var toolManager domain.ToolManager
	if len(a.allowedToolsOverride) > 0 {
		toolManager = skill.NewFilteredToolManager(a.allToolManagers, a.allowedToolsOverride)
	} else {
		toolManager = activeSkill.FilterTools(a.allToolManagers)
	}

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
				if compactErr := a.sharedState.CompactIfNeeded(ctx, a.llmClient, maxCtx, 0); compactErr != nil {
					a.logger.Warn("Context compaction failed, continuing without compaction", "error", compactErr)
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

	if a.alwaysApprove {
		fmt.Fprintf(writer, "Proceeding (Always selected)...\n\n")
		return reactClient.Resume(ctx)
	}

	// Check persistent permission rules before showing a dialog.
	if pending, ok := reactClient.GetPendingToolCall().(*message.ToolCallMessage); ok {
		toolName := string(pending.ToolName())
		arg := extractPermissionArg(toolName, pending.ToolArguments())
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
	}

	lastMessage := reactClient.GetLastMessage()

	stat, err := os.Stdin.Stat()
	if err != nil || (stat.Mode()&os.ModeCharDevice) == 0 {
		fmt.Fprintf(writer, "\nAbout to write file(s):\n")
		fmt.Fprintf(writer, "%s\n", lastMessage.TruncatedString())
		fmt.Fprintf(writer, "Proceeding (non-interactive mode)...\n\n")
		return reactClient.Resume(ctx)
	}

	fmt.Fprintf(writer, "\nAbout to write file(s):\n")
	fmt.Fprintf(writer, "%s\n\n", lastMessage.TruncatedString())

	prompt := promptui.Select{
		Label: "Proceed with this action?",
		Items: []string{"Yes", "Always", "No"},
		Templates: &promptui.SelectTemplates{
			Label:    "{{ . }}",
			Active:   "> {{ . | cyan }}",
			Inactive: "  {{ . }}",
			Selected: "{{ . }}",
		},
		Size: 3,
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
	case "Always":
		a.alwaysApprove = true
		fmt.Fprintf(writer, "Proceeding (will auto-approve future file operations this session)...\n\n")
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
// multi_edit carries multiple paths — we return the first one; the caller may
// want to call Check per-path, but for the initial implementation one suffices.
func extractPermissionArg(toolName string, args message.ToolArgumentValues) string {
	switch toolName {
	case "write_file", "edit_file":
		if path, ok := args["path"].(string); ok {
			return path
		}
	case "multi_edit":
		// edits is []interface{} where each element has "file_path"
		if edits, ok := args["edits"].([]interface{}); ok && len(edits) > 0 {
			if edit, ok := edits[0].(map[string]interface{}); ok {
				if fp, ok := edit["file_path"].(string); ok {
					return fp
				}
			}
		}
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			return cmd
		}
	}
	return ""
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
	llmWithTools, err := client.NewClientWithToolManager(a.llmClient, a.allToolManagers)
	if err != nil {
		return nil, fmt.Errorf("failed to create LLM client with tools: %w", err)
	}

	situation := NewIterationAdvisor(a.allToolManagers)

	maxIterations := DefaultAgentMaxIterations
	if a.settings != nil && a.settings.Agent.MaxIterations > 0 {
		maxIterations = a.settings.Agent.MaxIterations
	}
	reactClient, eventEmitter := react.NewReAct(llmWithTools, a.allToolManagers, a.sharedState, situation, maxIterations)
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
