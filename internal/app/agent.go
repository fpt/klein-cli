package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pkgErrors "github.com/pkg/errors"

	"github.com/manifoldco/promptui"

	"github.com/fpt/klein-cli/internal/claude"
	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/internal/permission"
	pluginpkg "github.com/fpt/klein-cli/internal/plugin"
	"github.com/fpt/klein-cli/internal/repository"
	"github.com/fpt/klein-cli/internal/skill"
	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/internal/tool/memorydb"
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
	llmClient            domain.LLM
	allToolManagers      *tool.CompositeToolManager // ALL tool managers combined
	deferredTools        *tool.DeferredToolManager  // tool-search view over allToolManagers (used when a skill omits allowed-tools)
	todoToolManager      *tool.TodoToolManager
	taskToolManager      *tool.TaskToolManager
	askQuestionManager   *tool.AskUserQuestionToolManager
	planMode             *tool.PlanModeState // shared with planToolManager and guard
	planToolManager      *tool.PlanToolManager
	taskAgentManager     *tool.TaskAgentToolManager      // Provides the Task tool that delegates to loaded agent definitions
	fsRepo               repository.FilesystemRepository // Shared filesystem repository instance
	workingDir           string
	sharedState          domain.State
	definitions          skill.DefinitionMap
	sessionFilePath      string
	settings             *config.Settings
	logger               *pkgLogger.Logger
	out                  io.Writer
	router               *SkillsRouter
	thinkingStarted      bool
	sessionRules         *permission.RuleSet // in-memory allow/deny rules created during this session
	permRules            *permission.RuleSet // persistent allow/deny rules from JSON files
	allowedToolsOverride []string            // CLI override for skill's allowed-tools (guarded by sandboxMu)
	sandboxMu            sync.RWMutex        // guards allowedToolsOverride: background runs read it off-turn
	sanitizeToolResults  bool                // neutralize chat-template control tokens in tool results
	tokenBudget          int                 // cumulative token cap per Invoke run (0 = unlimited)
	externalEventHandler events.EventHandler // optional: forward events to external consumers (e.g., Connect server)
	recentlyReadFiles    []string            // up to 5 most recently read unique file paths
	memoryDir            string              // $HOME/.klein/projects/<hash>/memory/ (interactive mode only)
	toolResultsDir       string              // $HOME/.klein/projects/<hash>/tool_results/ (interactive mode only)
	memoryManager        *memorydb.Manager   // sqlite long-term memory, when wired in (serve/claw); nil otherwise

	// codexBackend, when set (llm.backend == "codex"), routes Invoke to a codex
	// app-server thread instead of the ReAct loop. codexThreadID caches this
	// session's thread id (also persisted alongside the session file).
	codexBackend  domain.BackendRunner
	codexThreadID string

	// Plugin registry. Commands and agents are stored under their scoped
	// "<plugin>:<name>" identifier; bare-name entries are also populated when
	// unambiguous. ambiguousCommands/ambiguousAgents track names that appear
	// in more than one plugin and therefore require explicit scoping at
	// dispatch time.
	plugins           []*pluginpkg.Plugin
	pluginCommands    map[string]*pluginpkg.Command
	ambiguousCommands map[string]bool
	ambiguousAgents   map[string]bool
	agentRuns         *agentRunRegistry
}

// WorkingDir returns the agent's working directory.
func (a *Agent) WorkingDir() string { return a.workingDir }

// FilesystemRepository returns the shared filesystem repository instance.
func (a *Agent) FilesystemRepository() repository.FilesystemRepository { return a.fsRepo }

// SetAllowedToolsOverride sets a CLI-level override for the skill's allowed-tools.
// When non-empty, this list is used instead of the skill's own allowed-tools field.
// It is a hard sandbox: subagents dispatched from this agent are bounded by it
// too (their definition's tools are intersected with it in resolveSubagentTools).
func (a *Agent) SetAllowedToolsOverride(tools []string) {
	a.setToolSandbox(tools)
}

// toolSandbox returns a copy of the active hard tool override, or nil if there
// is none. It is a copy because a background run resolves its tools while the
// turn that dispatched it may still be mutating the field (InvokeCommand swaps
// an override in for the length of one plugin command).
func (a *Agent) toolSandbox() []string {
	a.sandboxMu.RLock()
	defer a.sandboxMu.RUnlock()
	if len(a.allowedToolsOverride) == 0 {
		return nil
	}
	out := make([]string, len(a.allowedToolsOverride))
	copy(out, a.allowedToolsOverride)
	return out
}

// setToolSandbox installs a new hard override and returns the previous one, so
// a caller scoping an override to one turn can restore it.
func (a *Agent) setToolSandbox(tools []string) []string {
	a.sandboxMu.Lock()
	defer a.sandboxMu.Unlock()
	prev := a.allowedToolsOverride
	a.allowedToolsOverride = tools
	return prev
}

// SetSanitizeToolResults neutralizes chat-template control tokens (`<|…|>`) in
// tool results before the model sees them, so a provider's prompt filter cannot
// fail a run over ordinary source text — see internal/sanitize.
//
// Enable this only for a read-only toolset. It rewrites what the model reads,
// so with Write/Edit reachable the substitution could be written back to disk.
func (a *Agent) SetSanitizeToolResults(enabled bool) {
	a.sanitizeToolResults = enabled
}

// SetTokenBudget caps the cumulative token usage of each Invoke run; 0 = no
// cap. When exceeded the run stops with react.ErrTokenBudgetExceeded — callers
// can salvage state accumulated up to that point.
func (a *Agent) SetTokenBudget(budget int) {
	a.tokenBudget = budget
}

// SetCodexBackend enables a whole-agent backend (e.g. codex) for this agent.
// When set, Invoke routes each turn to it instead of the ReAct loop (Option C:
// the backend is the agent for this session; klein provides the frontend,
// memory-context injection, session↔thread mapping, and run-log).
func (a *Agent) SetCodexBackend(b domain.BackendRunner) { a.codexBackend = b }

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

// RunSubagent runs def in subagent mode: its own ReAct loop over fresh message
// state, returning its final text to the caller. This is the single dispatcher
// behind the Task tool — it replaces the SpawnSubAgent/RunPluginAgent pair,
// which differed only in which registry they looked in and what the frontmatter
// called the tool list.
//
// maxIterations <= 0 uses the default. toolsOverride, when non-empty, replaces
// the definition's own tool list for this call.
func (a *Agent) RunSubagent(
	ctx context.Context, def *skill.Definition, task string, toolsOverride []string, maxIterations int,
) (string, error) {
	return a.runSubagent(ctx, def, task, subagentOptions{
		ToolsOverride: toolsOverride,
		MaxIterations: maxIterations,
		Writer:        a.OutWriter(),
	})
}

// subagentOptions configures one subagent run.
type subagentOptions struct {
	// Writer receives the run's progress. A foreground run shares the parent's
	// writer; a background run gets its own, so its tool noise does not
	// interleave with whatever the user is typing.
	Writer        io.Writer
	ToolsOverride []string
	MaxIterations int
	// SkipApproval forces auto-approval. Background runs set it because there
	// is no one at the prompt to answer.
	SkipApproval bool
}

func (a *Agent) runSubagent(
	ctx context.Context, def *skill.Definition, task string, opts subagentOptions,
) (string, error) {
	toolsOverride, maxIterations := opts.ToolsOverride, opts.MaxIterations
	if def == nil {
		return "", errors.New("subagent: nil definition")
	}
	if !def.Permits(skill.ModeSubagent) {
		return "", fmt.Errorf("%q cannot run as a subagent (it permits: %s)",
			def.Name, strings.Join(def.ModeNames(), ", "))
	}

	allowed, err := a.resolveSubagentTools(def, toolsOverride)
	if err != nil {
		return "", err
	}
	subToolManager := buildSubAgentToolManager(a.allToolManagers, allowed)

	if maxIterations <= 0 {
		maxIterations = DefaultAgentMaxIterations
	}

	label := def.Name
	if def.PluginName != "" {
		label = def.PluginName + ":" + def.Name
	}
	writer := opts.Writer
	if writer == nil {
		writer = a.OutWriter()
	}
	fmt.Fprintf(writer, "  [agent:%s] Starting: %s\n", label, truncate(task, 80))

	// Fresh conversation state — isolated from the parent.
	subState := state.NewMessageState()
	if prompt := def.RenderContent("", a.workingDir); prompt != "" {
		subState.AddMessage(message.NewSystemMessage(prompt))
	}

	llmWithTools, err := client.NewClientWithToolManager(a.llmClient, subToolManager)
	if err != nil {
		return "", fmt.Errorf("agent %s: failed to create LLM client: %w", label, err)
	}

	situation := NewIterationAdvisor(a.allToolManagers)
	reactClient, eventEmitter := react.NewReAct(llmWithTools, subToolManager, subState, situation, maxIterations)
	defer reactClient.Close()
	if a.settings != nil {
		reactClient.SetBashWhitelist(a.settings.Bash.WhitelistedCommands)
	}
	// Auto-approve every tool call when the definition declares itself a
	// background agent, or when this run is detached and nobody is at the
	// prompt to answer. The frontmatter tool list is the surface area the user
	// opted into; foreground runs keep the normal approval workflow.
	if def.Background || opts.SkipApproval {
		reactClient.SetSkipApproval(true)
	}

	eventEmitter.AddHandler(func(event events.AgentEvent) {
		switch event.Type {
		case events.EventTypeToolCallStart:
			if data, ok := event.Data.(events.ToolCallStartData); ok {
				fmt.Fprintf(writer, "  [agent:%s] %s %v\n", label, data.ToolName, data.Arguments)
			}
		case events.EventTypeToolResult:
			if data, ok := event.Data.(events.ToolResultData); ok && data.IsError {
				fmt.Fprintf(writer, "  [agent:%s] ERROR %s\n", label, data.Content)
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
	if err != nil {
		fmt.Fprintf(writer, "  [agent:%s] Failed: %v\n", label, err)
		return "", err
	}
	fmt.Fprintf(writer, "  [agent:%s] Done\n", label)
	return result.Content(), nil
}

// DispatchTask resolves a name for the Task tool and runs it as a subagent.
// Any definition permitting subagent mode is reachable, not only agent-kind
// ones — which is what lets Task absorb the deleted spawn_agent tool.
func (a *Agent) DispatchTask(ctx context.Context, req tool.TaskRequest) (string, error) {
	name, task := req.SubagentType, req.Prompt
	def, ambiguous := a.ResolveSubagent(name)
	if ambiguous {
		return "", fmt.Errorf("agent name %q is ambiguous — scope it as <plugin>:<name>", name)
	}
	if def == nil {
		if other, ok := a.definitions[strings.ToLower(name)]; ok {
			return "", fmt.Errorf("%q cannot run as a subagent (it permits: %s)",
				name, strings.Join(other.ModeNames(), ", "))
		}
		return "", fmt.Errorf("agent %q not found", name)
	}
	// A definition marked `background: true` detaches by default; the caller
	// can also ask for it per dispatch.
	if req.Background || def.Background {
		info, err := a.StartBackgroundAgent(def, task)
		if err != nil {
			return "", err
		}
		return formatBackgroundLaunch(info), nil
	}
	return a.RunSubagent(ctx, def, task, nil, 0)
}

// formatBackgroundLaunch is what the model sees the instant a detached agent
// starts. It states plainly that no result exists yet, because the failure mode
// here is the model inventing one rather than saying it launched something.
func formatBackgroundLaunch(info RunInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Launched %s in the background as %s.\n", info.Label, info.ID)
	if info.OutputPath != "" {
		fmt.Fprintf(&b, "transcript_file: %s\n", info.OutputPath)
	}
	b.WriteString("No result yet. It will be delivered to you automatically in a later " +
		"turn as an <agent-notification>; you do not need to poll for it. " +
		"AgentOutput(agent_id: \"" + info.ID + "\") checks early, AgentStop cancels.\n" +
		"Tell the user what you launched and end your response. Do not predict or " +
		"fabricate what it will find — if the user asks before it lands, say it is " +
		"still running and give status, not a guess.")
	return b.String()
}

// resolveSubagentTools computes the final tool whitelist for one subagent run:
// the caller's per-dispatch override, else the definition's own list, bounded by
// the active hard sandbox.
//
// A CLI-level hard override (SetAllowedToolsOverride) is a security boundary,
// not a preference: a subagent must never exceed it, whatever its definition or
// the caller asks for. An uncapped definition collapses to the sandbox itself
// rather than inheriting every tool.
//
// This MUST be called on the goroutine that dispatches the run, not inside it.
// The sandbox is mutable turn-scoped state — InvokeCommand swaps one in for the
// length of a plugin command — so a background run that resolved its own tools
// later could find the override already restored and escape the boundary.
func (a *Agent) resolveSubagentTools(def *skill.Definition, toolsOverride []string) ([]string, error) {
	allowed := toolsOverride
	if len(allowed) == 0 {
		allowed = def.EffectiveTools()
	}
	sandbox := a.toolSandbox()
	if len(sandbox) == 0 {
		return allowed, nil
	}
	if allowed = intersectTools(allowed, sandbox); len(allowed) == 0 {
		return nil, fmt.Errorf(
			"subagent %q: none of its tools are permitted under the active tool sandbox", def.Name)
	}
	return allowed, nil
}

// intersectTools bounds a definition's tool list by a hard override. An empty
// definition list means "all tools", so the bound itself is the result. The
// result must never be conflated with "no cap": callers treat empty as an
// error, because buildSubAgentToolManager reads an empty list as unrestricted.
func intersectTools(defTools, bound []string) []string {
	if len(defTools) == 0 {
		out := make([]string, len(bound))
		copy(out, bound)
		return out
	}
	inBound := make(map[string]bool, len(bound))
	for _, n := range bound {
		inBound[n] = true
	}
	var out []string
	for _, n := range defTools {
		if inBound[n] {
			out = append(out, n)
		}
	}
	return out
}

// buildSubAgentToolManager constructs a filtered tool manager for sub-agents:
// always strips Task to prevent recursion; respects the optional allowedTools
// whitelist.
func buildSubAgentToolManager(all *tool.CompositeToolManager, allowedTools []string) domain.ToolManager {
	excluded := map[string]bool{"Task": true}
	if len(allowedTools) > 0 {
		filtered := make([]string, 0, len(allowedTools))
		for _, name := range allowedTools {
			if !excluded[name] {
				filtered = append(filtered, name)
			}
		}
		return skill.NewFilteredToolManager(all, filtered)
	}
	allTools := all.GetTools()
	names := make([]string, 0, len(allTools))
	for name := range allTools {
		if !excluded[string(name)] {
			names = append(names, string(name))
		}
	}
	return skill.NewFilteredToolManager(all, names)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// len is a byte count; truncate on a rune boundary so multibyte text
	// (e.g. Japanese) is never split into an invalid sequence.
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// AgentOptions configures agent construction. NewAgentWithOptions is a factory
// keyed on the backend type (Settings.LLM.Backend): it builds the LLM client and
// wires an optional whole-agent backend, so callers no longer coordinate client
// creation, agent construction, and backend startup separately.
type AgentOptions struct {
	Settings        *config.Settings
	MCPToolManagers map[string]domain.ToolManager
	Logger          *pkgLogger.Logger
	Out             io.Writer
	FsRepo          repository.FilesystemRepository

	// LLMClient overrides client construction; when nil the client is built from
	// Settings.LLM. Mainly for tests and custom wiring.
	LLMClient domain.LLM

	// AgentBackend provisions a whole-agent backend (e.g. codex). When non-nil,
	// its EnsureBackendProcess is called and the resulting runner routes turns
	// instead of the ReAct loop. nil means the ReAct loop handles every turn.
	AgentBackend domain.AgentBackend

	WorkingDir string

	SkipSessionRestore bool
	IsInteractiveMode  bool

	// ContinueSession resumes the project's most recently used session instead of
	// starting a fresh one (`klein --continue`). Interactive mode only; a fresh
	// session is the default so a plain `klein` never inherits stale context.
	ContinueSession bool
}

// resolveLLMClient returns opts.LLMClient when set, otherwise builds one from the
// configured backend (a stub for the codex backend, whose Chat is never called).
func resolveLLMClient(opts AgentOptions) (domain.LLM, error) {
	if opts.LLMClient != nil {
		return opts.LLMClient, nil
	}
	c, err := client.NewLLMClient(opts.Settings.LLM)
	if err != nil {
		return nil, fmt.Errorf("failed to create LLM client: %w", err)
	}
	return c, nil
}

// computeMemoryDir returns the project memory directory for interactive mode, or
// "" in one-shot/test mode (or when user config is unavailable).
func computeMemoryDir(isInteractiveMode bool, workingDir string) string {
	if !isInteractiveMode {
		return ""
	}
	userConfig, err := config.DefaultUserConfig()
	if err != nil {
		return ""
	}
	dir, err := userConfig.GetProjectMemoryDir(workingDir)
	if err != nil {
		return ""
	}
	return dir
}

// computeToolResultsDir returns the directory where oversized tool results are
// offloaded, or "" in one-shot/test mode (or when user config is unavailable),
// which keeps every result inline and in memory.
//
// The path is computed here rather than derived from the session file because
// buildAgentTools needs it: it goes on the filesystem allowlist so the model can
// read back a result the harness moved out of the conversation.
func computeToolResultsDir(isInteractiveMode bool, workingDir string) string {
	if !isInteractiveMode {
		return ""
	}
	userConfig, err := config.DefaultUserConfig()
	if err != nil {
		return ""
	}
	dir, err := userConfig.GetProjectToolResultsDir(workingDir)
	if err != nil {
		return ""
	}
	return dir
}

// newSharedSessionState creates the shared message state and its session file
// path. Interactive mode gets a *fresh* session file per run; it resumes the
// project's most recently used session only when continueSession is set
// (`klein --continue`). one-shot/test mode gets a clean in-memory state.
//
// Fresh is the default so a plain `klein` never silently inherits context from
// whatever was being worked on hours earlier. Because each run owns its own
// file, defaulting to fresh costs nothing: the previous conversation stays on
// disk and is exactly what `--continue` picks up.
func newSharedSessionState(
	isInteractiveMode, skipSessionRestore, continueSession bool, workingDir string, logger *pkgLogger.Logger,
) (domain.State, string) {
	if !isInteractiveMode {
		logger.DebugWithIntention(pkgLogger.IntentionStatus, "Starting with clean session", "reason", "one-shot mode")
		return state.NewMessageState(), ""
	}

	userConfig, err := config.DefaultUserConfig()
	if err != nil {
		logger.Warn("Could not access user config for session persistence", "error", err)
		return state.NewMessageState(), ""
	}

	// skipSessionRestore (file mode) means "do not inherit history", which is
	// what a fresh session already is.
	if continueSession && !skipSessionRestore {
		if resumed, path := resumeLatestSession(userConfig, workingDir, logger); resumed != nil {
			return resumed, path
		}
	}

	sessionFilePath, err := userConfig.NewProjectSessionFile(workingDir)
	if err != nil {
		logger.Warn("Could not get session file path", "error", err)
		return state.NewMessageState(), ""
	}
	logger.DebugWithIntention(pkgLogger.IntentionStatus, "Starting a fresh session",
		"session_file", sessionFilePath)
	return state.NewMessageStateWithRepository(infra.NewMessageHistoryRepository(sessionFilePath)), sessionFilePath
}

// resumeLatestSession loads the project's most recently used session. It returns
// (nil, "") when there is nothing to resume or the file will not load, leaving
// the caller to start a fresh session instead — `--continue` on a project with
// no history is a no-op, not an error.
func resumeLatestSession(
	userConfig *config.UserConfig, workingDir string, logger *pkgLogger.Logger,
) (domain.State, string) {
	latest, err := userConfig.LatestProjectSessionFile(workingDir)
	if err != nil {
		logger.Warn("Could not look up previous sessions", "error", err)
		return nil, ""
	}
	if latest == "" {
		logger.DebugWithIntention(pkgLogger.IntentionStatus, "Starting a fresh session",
			"reason", "--continue given but this project has no previous session")
		return nil, ""
	}

	sharedState := state.NewMessageStateWithRepository(infra.NewMessageHistoryRepository(latest))
	if err := sharedState.LoadFromFile(); err != nil {
		logger.Warn("Could not load previous session; starting fresh",
			"session_file", latest, "error", err)
		return nil, ""
	}
	logger.DebugWithIntention(pkgLogger.IntentionStatus, "Resumed previous session",
		"message_count", len(sharedState.GetMessages()), "session_file", latest)
	return sharedState, latest
}

// agentTools bundles the tool managers an Agent keeps references to after
// construction, plus the composite/deferred views the ReAct loop binds per skill.
type agentTools struct {
	todo        *tool.TodoToolManager
	task        *tool.TaskToolManager
	askQuestion *tool.AskUserQuestionToolManager
	planMode    *tool.PlanModeState
	plan        *tool.PlanToolManager
	taskAgent   *tool.TaskAgentToolManager
	agentRuns   *tool.AgentRunToolManager
	all         *tool.CompositeToolManager
	deferred    *tool.DeferredToolManager
}

// agentFileSystemConfig assembles the paths the agent may touch: its working
// directory, plus the klein-owned directories it has to reach to do its job.
// The list bounds reading and searching as well as writing, so anything left out
// is invisible to the agent entirely.
func agentFileSystemConfig(workingDir, memoryDir, toolResultsDir string) repository.FileSystemConfig {
	fsConfig := infra.DefaultFileSystemConfig(workingDir)
	if memoryDir != "" {
		fsConfig.AllowedDirectories = append(fsConfig.AllowedDirectories, memoryDir)
	}
	// A tool result too large to keep inline is offloaded to a file here and the
	// model is handed the path, so that path has to be readable — otherwise the
	// model's only route back to the content is to re-run the tool, get the same
	// stub, and loop.
	if toolResultsDir != "" {
		fsConfig.AllowedDirectories = append(fsConfig.AllowedDirectories, toolResultsDir)
	}
	// Allow writing to ~/.klein/skills so the create-skill skill can persist new
	// skills there, and ~/.klein/roles so a custom role can be added the same way
	// (both are scanned by the loader).
	if home, err := os.UserHomeDir(); err == nil {
		fsConfig.AllowedDirectories = append(fsConfig.AllowedDirectories,
			filepath.Join(home, ".klein", "skills"),
			filepath.Join(home, ".klein", "roles"))
	}
	return fsConfig
}

// buildAgentTools constructs every tool manager (universal + specialized + MCP)
// and combines them into the composite/deferred views. memoryDir and
// toolResultsDir (when non-empty) and ~/.klein/skills are added to the
// filesystem allowlist.
func buildAgentTools(opts AgentOptions, skills skill.DefinitionMap, memoryDir, toolResultsDir string) agentTools {
	workingDir := opts.WorkingDir

	var todoToolManager *tool.TodoToolManager
	var taskToolManager *tool.TaskToolManager
	if opts.IsInteractiveMode {
		todoToolManager = tool.NewTodoToolManager(workingDir)
		taskToolManager = tool.NewTaskToolManager(workingDir)
	} else {
		todoToolManager = tool.NewInMemoryTodoToolManager()
		taskToolManager = tool.NewInMemoryTaskToolManager()
	}

	fsConfig := agentFileSystemConfig(workingDir, memoryDir, toolResultsDir)
	filesystemManager := tool.NewFileSystemToolManager(opts.FsRepo, fsConfig, workingDir)

	bashToolManager := tool.NewBashToolManager(tool.BashConfig{
		WorkingDir:          workingDir,
		MaxDuration:         2 * time.Minute,
		WhitelistedCommands: opts.Settings.Bash.WhitelistedCommands,
	})

	askQuestionManager := tool.NewAskUserQuestionToolManager()
	planModeState := new(tool.PlanModeState) // starts as PlanModeOff
	planToolManager := tool.NewPlanToolManager(planModeState)
	taskAgentManager := tool.NewTaskAgentToolManager()
	agentRunManager := tool.NewAgentRunToolManager()

	// Combine ALL tool managers into one composite.
	managers := []domain.ToolManager{
		todoToolManager, taskToolManager, filesystemManager, bashToolManager,
		tool.NewSearchToolManager(tool.SearchConfig{
			// Searching is reading, so it is bounded by the same list — including
			// the memory/tool-results/skills directories added above, which a
			// search would otherwise be unable to reach now that it is bounded.
			WorkingDir:         workingDir,
			AllowedDirectories: fsConfig.AllowedDirectories,
		}),
		tool.NewWebToolManager(), tool.NewPDFToolManager(workingDir), tool.NewMarketToolManager(),
		tool.NewSkillToolManager(skills, workingDir), askQuestionManager, planToolManager,
		taskAgentManager, agentRunManager, tool.NewResearcherToolManager(),
	}
	for _, mcpManager := range opts.MCPToolManagers {
		managers = append(managers, mcpManager)
	}
	allToolManagers := tool.NewCompositeToolManager(managers...)

	return agentTools{
		todo:        todoToolManager,
		task:        taskToolManager,
		askQuestion: askQuestionManager,
		planMode:    planModeState,
		plan:        planToolManager,
		taskAgent:   taskAgentManager,
		agentRuns:   agentRunManager,
		all:         allToolManagers,
		deferred:    tool.NewDeferredToolManager(allToolManagers),
	}
}

// NewAgent creates a new Agent with MCP tools and settings.
func NewAgent(
	llmClient domain.LLM, workingDir string, mcpToolManagers map[string]domain.ToolManager,
	settings *config.Settings, logger *pkgLogger.Logger, out io.Writer,
) *Agent {
	agent, _, err := NewAgentWithOptions(context.Background(), AgentOptions{
		Settings:          settings,
		WorkingDir:        workingDir,
		MCPToolManagers:   mcpToolManagers,
		Logger:            logger,
		Out:               out,
		FsRepo:            infra.NewOSFilesystemRepository(),
		IsInteractiveMode: true,
		LLMClient:         llmClient,
	})
	if err != nil {
		// NewAgent has no error return and no AgentBackend is supplied here, so the
		// only failure path (backend startup) is unreachable; log defensively.
		logger.Error("NewAgent construction failed", "error", err)
	}
	return agent
}

// NewAgentWithOptions builds an Agent from opts, creating the LLM client from
// Settings.LLM.Backend (unless opts.LLMClient is set) and starting/attaching the
// optional whole-agent backend. It returns the Agent, a cleanup func (never nil;
// closes any backend process the factory started), and an error.
func NewAgentWithOptions(ctx context.Context, opts AgentOptions) (*Agent, func(), error) {
	cleanup := func() {}
	settings := opts.Settings
	workingDir := opts.WorkingDir
	logger := opts.Logger
	out := opts.Out
	fsRepo := opts.FsRepo
	skipSessionRestore := opts.SkipSessionRestore
	isInteractiveMode := opts.IsInteractiveMode

	// Build the LLM client from the backend type unless one was injected. For the
	// codex backend this yields a stub whose Chat is never called (turns route to
	// the backend runner below).
	llmClient, err := resolveLLMClient(opts)
	if err != nil {
		return nil, cleanup, err
	}

	// Compute per-project directories for interactive mode; empty strings in
	// one-shot/test mode (which keeps state in memory).
	memoryDir := computeMemoryDir(isInteractiveMode, workingDir)
	toolResultsDir := computeToolResultsDir(isInteractiveMode, workingDir)

	// Load roles and skills (embedded + filesystem) before creating tool
	// managers. Both land in one registry: Invoke resolves a name without caring
	// whether it is the session's startup role or a skill reached mid-session.
	skills, err := skill.LoadRolesAndSkills(workingDir)
	if err != nil {
		logger.Warn("Failed to load roles/skills, using empty fallback", "error", err)
		skills = make(skill.DefinitionMap)
	}

	// Load subagents (embedded built-ins + personal + project). Plugin agents
	// are merged on top later by RegisterPlugins, which also indexes them under
	// their scoped "<plugin>:<agent>" name.
	localAgents, agentsErr := pluginpkg.LoadAgents(workingDir)
	if agentsErr != nil {
		logger.Warn("Failed to load agents, using empty fallback", "error", agentsErr)
		localAgents = make(pluginpkg.AgentMap)
	}

	// Load persistent permission rules (user + project + local); missing files are
	// silently ignored, never fatal.
	permRules := permission.LoadForProject(workingDir)

	// Build every tool manager (universal + specialized + MCP) and the
	// composite/deferred views the ReAct loop binds per skill.
	tools := buildAgentTools(opts, skills, memoryDir, toolResultsDir)

	// Create or restore shared message state with session persistence
	sharedState, sessionFilePath := newSharedSessionState(
		isInteractiveMode, skipSessionRestore, opts.ContinueSession, workingDir, logger,
	)

	a := &Agent{
		llmClient:          llmClient,
		allToolManagers:    tools.all,
		deferredTools:      tools.deferred,
		todoToolManager:    tools.todo,
		taskToolManager:    tools.task,
		askQuestionManager: tools.askQuestion,
		planMode:           tools.planMode,
		planToolManager:    tools.plan,
		taskAgentManager:   tools.taskAgent,
		fsRepo:             fsRepo,
		workingDir:         workingDir,
		sharedState:        sharedState,
		definitions:        mergeDefinitions(logger, skills, localAgents),
		sessionFilePath:    sessionFilePath,
		settings:           settings,
		logger:             logger.WithComponent("agent"),
		out:                out,
		router:             NewSkillsRouter(),
		sessionRules:       newSessionRules(isInteractiveMode),
		permRules:          permRules,
		ambiguousAgents:    make(map[string]bool),
		agentRuns:          newAgentRunRegistry(),
		memoryDir:          memoryDir,
		toolResultsDir:     toolResultsDir,
		memoryManager:      findMemoryManager(opts.MCPToolManagers),
	}

	cleanup, err = a.wireToolsAndBackend(ctx, tools, opts.AgentBackend)
	if err != nil {
		return nil, cleanup, err
	}

	return a, cleanup, nil
}

// MemoryManager returns the sqlite long-term memory manager, or nil when it is
// not wired into this session (e.g. the plain CLI, which has no memorydb).
func (a *Agent) MemoryManager() *memorydb.Manager { return a.memoryManager }

// findMemoryManager returns the first *memorydb.Manager among the MCP tool
// managers, or nil.
func findMemoryManager(managers map[string]domain.ToolManager) *memorydb.Manager {
	for _, m := range managers {
		if kb, ok := m.(*memorydb.Manager); ok {
			return kb
		}
	}
	return nil
}

// wireToolsAndBackend performs the two-phase init that needs the constructed
// Agent: it wires the self-referential Task callback and provisions the
// optional whole-agent backend, returning the cleanup for any process started
// (never nil).
func (a *Agent) wireToolsAndBackend(
	ctx context.Context, tools agentTools, backend domain.AgentBackend,
) (func(), error) {
	// Wire the Task dispatcher. The catalog provider is evaluated lazily on
	// each Description() because plugins are registered after the agent is
	// constructed.
	tools.taskAgent.SetCallback(a.DispatchTask)
	tools.taskAgent.SetCatalogProvider(a.AgentCatalog)
	tools.agentRuns.SetCallbacks(tool.AgentRunCallbacks{
		List:   a.ListAgentRuns,
		Output: a.AgentRunOutput,
		Stop:   a.StopAgentRun,
	})

	// Provision the whole-agent backend (e.g. codex), if one was injected. This
	// may start an external process; its cleanup is folded into the returned func.
	if backend == nil {
		return func() {}, nil
	}
	runner, cleanup, err := backend.EnsureBackendProcess(ctx, a.workingDir)
	if err != nil {
		return func() {}, fmt.Errorf("failed to start agent backend: %w", err)
	}
	a.SetCodexBackend(runner)
	return cleanup, nil
}

// Invoke executes a specified skill. Optional images are base64-encoded strings
// that get attached to the user message for vision-capable models.
func (a *Agent) Invoke(ctx context.Context, userInput string, skillName string, images ...string) (message.Message, error) {
	skillName = strings.ToLower(skillName)
	activeSkill, exists := a.lookupInvocable(skillName)
	if !exists {
		return nil, fmt.Errorf("skill '%s' not found", skillName)
	}

	// Deliver any background agent that finished since the last turn. Draining
	// here rather than in each front end means the REPL, the Connect server and
	// the gateway all get it without three separate chances to forget.
	//
	// The claim is provisional until the message actually reaches the
	// conversation. Invoke has several error returns below, and a turn the user
	// interrupts with Ctrl+C is the common one; without the release a finished
	// agent's result would be marked delivered, never shown, and skipped by
	// every later drain.
	userInput, releaseNotifications := a.prependAgentNotifications(userInput)
	notificationsDelivered := false
	defer func() {
		if !notificationsDelivered {
			releaseNotifications()
		}
	}()

	// Codex backend: route the whole turn to a codex thread instead of the
	// ReAct loop. The skill still resolves (its prompt steers codex), but klein's
	// tool managers/ReAct are bypassed.
	if a.codexBackend != nil {
		resp, err := a.invokeCodex(ctx, activeSkill, userInput)
		// invokeCodex records the exchange in session state only on success, so
		// a failed turn leaves the notifications for the next one.
		notificationsDelivered = err == nil
		return resp, err
	}

	// Reset plan mode at the start of each invocation
	if a.planMode != nil {
		*a.planMode = tool.PlanModeOff
	}

	// Choose the tool manager. Every skill gets the ToolSearch/deferred view:
	// the skill's allowed-tools (or a default core when omitted) are exposed
	// up front, and everything else — including MCP tools, which can't be named
	// in allowed-tools — stays deferred and loadable on demand via ToolSearch.
	// The CLI --allowed-tools override remains a HARD restriction (no ToolSearch).
	filteredTools, usingDeferred := a.selectToolManager(activeSkill)

	// Wrap with plan mode guard to block destructive operations during planning
	guard := tool.NewPlanModeGuard(filteredTools, a.planMode)
	toolManager := domain.ToolManager(guard)
	if a.sanitizeToolResults {
		toolManager = tool.NewControlTokenSanitizer(toolManager)
	}

	// Create LLM client with filtered tools
	llmWithTools, err := client.NewClientWithToolManager(a.llmClient, toolManager)
	if err != nil {
		return nil, fmt.Errorf("failed to create LLM client with tools: %w", err)
	}

	situation := NewIterationAdvisor(a.allToolManagers).
		WithRoutingHint(a.router.Route(userInput, skillName, a.sharedState.GetMessages()))
	if usingDeferred {
		situation = situation.WithDeferredHint(a.deferredTools.CatalogHint())
	}

	maxIterations := DefaultAgentMaxIterations
	if a.settings != nil && a.settings.Agent.MaxIterations > 0 {
		maxIterations = a.settings.Agent.MaxIterations
	}
	reactClient, eventEmitter := react.NewReAct(llmWithTools, toolManager, a.sharedState, situation, maxIterations)
	a.setupEventHandlers(eventEmitter)
	// Ensure the thinking-channel drainer goroutine is always reclaimed, even on
	// error returns below; Close is idempotent and nil-safe.
	defer reactClient.Close()
	if a.tokenBudget > 0 {
		reactClient.SetTokenBudget(a.tokenBudget)
	}
	if a.settings != nil {
		reactClient.SetBashWhitelist(a.settings.Bash.WhitelistedCommands)
	}

	// Tool result budgeting: offload large tool results to disk so they don't
	// permanently consume context window space. Only active in interactive/persistent
	// sessions where a project directory exists; one-shot mode keeps everything
	// in memory. The directory is on the filesystem allowlist (see
	// buildAgentTools), so the model can read back what was offloaded.
	if a.toolResultsDir != "" {
		maxRunes := 0 // 0 → tool.DefaultMaxToolResultRunes
		if a.settings != nil {
			maxRunes = a.settings.Agent.MaxToolResultRunes
		}
		storage := tool.NewToolResultStorage(a.toolResultsDir, maxRunes)
		reactClient.SetToolResultTransform(storage.MaybeOffload)
	}

	// Mandatory cleanup: remove stale situation messages and truncate old vision
	// content. Runs before catalog/prompt injection so dedup checks see a clean slate.
	if err := a.sharedState.CleanupMandatory(); err != nil {
		a.logger.Warn("Mandatory cleanup failed, continuing", "error", err)
	}

	// Inject memory system prompt (interactive mode only; re-read on every call
	// so the agent sees the latest MEMORY.md after it writes to it).
	if memoryPrompt := a.buildMemorySystemPrompt(); memoryPrompt != "" {
		const memoryMarker = "[[MEMORY_SYSTEM]]\n"
		candidate := memoryMarker + memoryPrompt
		var lastMemory string
		for _, msg := range a.sharedState.GetMessages() {
			if msg.Type() == message.MessageTypeSystem && strings.HasPrefix(msg.Content(), memoryMarker) {
				lastMemory = msg.Content()
			}
		}
		if lastMemory == "" || lastMemory != candidate {
			a.sharedState.AddMessage(message.NewSystemMessage(candidate))
		}
	}

	// Inject skill catalog into system prompt
	catalogContent := skill.BuildSkillCatalog(a.definitions)
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
					out = append(
						out,
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

	// Run adds the user message to the conversation before the first model
	// call, so once it is entered the notifications have landed even if the
	// turn later fails or is interrupted. Re-delivering them then would
	// duplicate what the model already has.
	notificationsDelivered = true
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
	stat, err := os.Stdin.Stat()
	if err != nil || (stat.Mode()&os.ModeCharDevice) == 0 {
		fmt.Fprintf(writer, "\n%s\n", describePendingToolCall(toolName, arg))
		fmt.Fprintf(writer, "Proceeding (non-interactive mode)...\n\n")
		return reactClient.Resume(ctx)
	}

	// 4. Interactive dialog.
	fmt.Fprintf(writer, "\n%s\n\n", describePendingToolCall(toolName, arg))

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

// Tool names that route through the approval workflow (see react.ReAct's
// requiresApproval check).
const (
	toolWrite     = "Write"
	toolEdit      = "Edit"
	toolMultiEdit = "MultiEdit"
	toolBash      = "Bash"
)

// describePendingToolCall renders the approval prompt header for the tool call
// awaiting a decision. It deliberately describes the *pending call* — the tool
// and the target the permission rule would be matched (and saved) against — and
// not the last message in the conversation, which is the preceding tool result
// and says nothing about what is about to happen.
func describePendingToolCall(toolName, arg string) string {
	var action string
	switch toolName {
	case toolWrite:
		action = "About to write file:"
	case toolEdit, toolMultiEdit:
		action = "About to edit file:"
	case toolBash:
		action = "About to run command:"
	case "":
		return "About to run a tool (details unavailable):"
	default:
		action = fmt.Sprintf("About to run %s:", toolName)
	}
	if arg == "" {
		return action
	}
	// truncateForDisplay (loop.go) cuts on a rune boundary, so a Japanese path
	// or command is shortened without being mangled.
	return fmt.Sprintf("%s\n   ↳ %s", action, truncateForDisplay(arg, 200))
}

// extractPermissionArg returns the primary argument used for rule pattern matching.
// For file tools this is the path; for bash this is the command string.
// MultiEdit carries multiple paths — we return the first one; the caller may
// want to call Check per-path, but for the initial implementation one suffices.
func extractPermissionArg(toolName string, args message.ToolArgumentValues) string {
	switch toolName {
	case toolWrite, toolEdit:
		// Filesystem tools register the parameter as "file_path".
		if path, ok := args["file_path"].(string); ok {
			return path
		}
	case toolMultiEdit:
		// edits is []interface{} where each element has "file_path"
		if edits, ok := args["edits"].([]interface{}); ok && len(edits) > 0 {
			if edit, ok := edits[0].(map[string]interface{}); ok {
				if fp, ok := edit["file_path"].(string); ok {
					return fp
				}
			}
		}
	case toolBash:
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
	tools := []string{toolWrite, toolEdit, toolMultiEdit, toolBash}
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
	case toolWrite, toolEdit, toolMultiEdit:
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
	case toolBash:
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
	defer reactClient.Close()
	if a.settings != nil {
		reactClient.SetBashWhitelist(a.settings.Bash.WhitelistedCommands)
	}

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

// ImportClaudeHistory loads messages from the given Claude Code *.jsonl session
// file and appends them to the shared message state. It also saves the updated
// state so the imported history survives the next restart.
func (a *Agent) ImportClaudeHistory(jsonlPath string) (int, error) {
	msgs, err := claude.ImportMessages(jsonlPath)
	if err != nil {
		return 0, err
	}
	for _, m := range msgs {
		a.sharedState.AddMessage(m)
	}
	if saveErr := a.sharedState.SaveToFile(); saveErr != nil {
		// Non-fatal: history is in memory even if persist fails.
		a.logger.Warn("Failed to persist imported history", "error", saveErr)
	}
	return len(msgs), nil
}

// InjectContextFile reads AGENTS.md or CLAUDE.md from the working directory
// and prepends it as a system message. Does nothing when neither file exists.
func (a *Agent) InjectContextFile() {
	content, err := claude.FindContextFile(a.workingDir)
	if err != nil || content == "" {
		return
	}
	a.sharedState.AddMessage(message.NewSystemMessage(
		"# Project Context\n\n" + content,
	))
}

// setupEventHandlers configures event handlers to convert events back to output format.
// invokeCodex runs one turn through the codex app-server backend. The skill's
// rendered prompt is passed as codex developer instructions; the user input is
// the turn prompt (any memory context is already prepended by the gateway).
//
// The thread's history lives on the backend, and klein sends only the prompt, so
// every thread klein does not continue starts with no memory of the
// conversation. That happens more often than it sounds: the connection drops
// (laptop sleeps, server restarts) and RunTurn opens a fresh thread, and a
// resumed session is a new process, which cannot continue a thread it did not
// start. So the developer instructions carry the conversation so far, and the
// session↔thread mapping persisted beside the session file is what tells the
// two cases apart.
func (a *Agent) invokeCodex(
	ctx context.Context, activeSkill *skill.Definition, userInput string,
) (message.Message, error) {
	threadID := a.loadCodexThreadID()
	// The transcript is built every turn and used only when a thread is actually
	// started — klein cannot know beforehand which turn that is, because the
	// reconnect that forces one is discovered inside RunTurn.
	devInstr := seedInstructions(
		activeSkill.RenderContent("", a.workingDir),
		renderBackendTranscript(a.sharedState.GetMessages()),
	)

	// Route the backend's intermediate activity (commands it runs, reasoning,
	// file changes, tool calls) through the same event pipeline as the ReAct
	// loop, so progress is written to the console and forwarded to any external
	// handler (Connect server / gateway) exactly like native tool use.
	emitter := events.NewSimpleEventEmitter()
	a.setupEventHandlers(emitter)

	newThreadID, response, err := a.codexBackend.RunTurn(ctx, threadID, userInput, devInstr, emitter.EmitEvent)
	if err != nil {
		// Forward to the external handler only (Connect server / gateway rely on
		// it). Interactive callers print the error themselves via executeTurn, so
		// routing it through the console writer here would double it.
		if a.externalEventHandler != nil {
			a.externalEventHandler(events.AgentEvent{Type: events.EventTypeError, Data: events.ErrorData{Error: err}})
		}
		return nil, err
	}
	if newThreadID != "" && newThreadID != threadID {
		if threadID != "" {
			// A different id back for an id we passed in means the thread was
			// replaced, not created: the connection died and this turn ran on a
			// fresh one. Said out loud because the repair is best-effort — the
			// re-seed is bounded, and the backend's own working state (what it
			// read, what it ran) did not come back with it.
			a.logger.Warn(
				"the app-server connection was lost, so this turn ran on a new thread "+
					"seeded from the session log; anything the backend had open is gone",
				"previous_thread", threadID, "new_thread", newThreadID,
			)
		}
		a.saveCodexThreadID(newThreadID)
	}

	// Record the exchange in klein's session state for history/preview. Codex
	// holds the authoritative thread; this keeps klein's session log meaningful.
	a.sharedState.AddMessage(message.NewChatMessage(message.MessageTypeUser, userInput))
	assistant := message.NewChatMessage(message.MessageTypeAssistant, response)
	a.sharedState.AddMessage(assistant)
	if a.sessionFilePath != "" {
		if err := a.sharedState.SaveToFile(); err != nil {
			a.logger.Warn("Failed to persist session after codex turn", "error", err)
		}
	}

	// EventTypeResponse resets any in-progress thinking style and forwards the
	// final message to the external handler (the console text itself is printed
	// by the caller from the returned message).
	emitter.EmitEvent(events.EventTypeResponse, events.ResponseData{Message: assistant})
	return assistant, nil
}

// codexThreadPath returns the sidecar file that stores this session's codex
// thread id, or "" when the session is not file-backed (one-shot/in-memory).
func (a *Agent) codexThreadPath() string {
	if a.sessionFilePath == "" {
		return ""
	}
	return a.sessionFilePath + ".codex-thread"
}

func (a *Agent) loadCodexThreadID() string {
	if a.codexThreadID != "" {
		return a.codexThreadID
	}
	p := a.codexThreadPath()
	if p == "" {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	a.codexThreadID = strings.TrimSpace(string(data))
	return a.codexThreadID
}

func (a *Agent) saveCodexThreadID(id string) {
	a.codexThreadID = id
	if p := a.codexThreadPath(); p != "" {
		if err := os.WriteFile(p, []byte(id), 0o644); err != nil {
			a.logger.Warn("Failed to persist codex thread id", "error", err)
		}
	}
}

func (a *Agent) setupEventHandlers(emitter events.EventEmitter) {
	emitter.AddHandler(func(event events.AgentEvent) {
		writer := a.OutWriter()
		if writer == nil {
			return
		}

		switch event.Type {
		case events.EventTypeToolCallStart:
			if data, ok := event.Data.(events.ToolCallStartData); ok {
				fmt.Fprintf(writer, "%sRunning tool%s %s%s%s %v\n",
					ansiDim, ansiReset, ansiCyan, data.ToolName, ansiReset, data.Arguments)
				if data.ToolName == "Read" {
					if path, ok := data.Arguments["file_path"].(string); ok && path != "" {
						a.recordRecentlyRead(path)
					}
				}
			}

		case events.EventTypeToolResult:
			if data, ok := event.Data.(events.ToolResultData); ok {
				writeToolResult(writer, data)
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

// buildMemorySystemPrompt constructs the memory system prompt by reading the
// current MEMORY.md index and composing it with instructions for all four
// memory types. Returns "" when memoryDir is empty (non-interactive mode).
func (a *Agent) buildMemorySystemPrompt() string {
	if a.memoryDir == "" {
		return ""
	}

	memoryFile := filepath.Join(a.memoryDir, "MEMORY.md")
	var memoryContent string
	if data, err := os.ReadFile(memoryFile); err == nil {
		memoryContent = string(data)
	}

	prompt := fmt.Sprintf(`# Memory System

You have a persistent, file-based memory system at %q. Use it to remember
information across conversations so you can tailor future behavior to this user.

## Memory Types

- **user**: The user's role, goals, expertise, and preferences.
- **feedback**: Guidance about how to approach work — corrections AND confirmations.
  Lead with the rule, then a **Why:** and **How to apply:** line.
- **project**: Ongoing work, goals, decisions, bugs, or incidents (not derivable
  from code or git history). Convert relative dates to absolute dates.
  Lead with the fact, then **Why:** and **How to apply:** lines.
- **reference**: Pointers to external resources (Linear projects, dashboards, Slack
  channels, etc.) and their purpose.

## How to Save a Memory

**Step 1** — write the memory to its own file (e.g., %s) using this frontmatter:

`+"```"+`markdown
---
name: <memory name>
description: <one-line description>
type: <user|feedback|project|reference>
---

<memory content>
`+"```"+`

**Step 2** — add a pointer to that file in %s. MEMORY.md is an index; never
write memory content directly into it. Keep it under 200 lines.

## What NOT to Save

- Code patterns, conventions, or architecture (read the code instead).
- Git history or who-changed-what (use git log).
- Debugging solutions already reflected in the code.
- Anything already in CLAUDE.md.
- Ephemeral task details from the current conversation.

## Current Memory Index (MEMORY.md)
`, a.memoryDir,
		filepath.Join(a.memoryDir, "memory_name.md"),
		memoryFile)

	if memoryContent == "" {
		prompt += "(empty — no memories saved yet)\n"
	} else {
		prompt += memoryContent + "\n"
	}

	return prompt
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
			fmt.Sprintf("# Recently-read file (restored after compaction): %s\n%s", path, content),
		)
		a.sharedState.AddMessage(msg)
		budget -= tokens
		count++
	}
	if count > 0 {
		a.logger.Info("Post-compact context restoration", "files_restored", count)
	}
}
