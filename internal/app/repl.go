package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chzyer/readline"
	"github.com/fpt/klein-cli/internal/claude"
	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/manifoldco/promptui"
)

// SlashCommand represents a command that starts with /
type SlashCommand struct {
	Name        string
	Description string
	Handler     func(*Agent) bool // Returns true if should exit
}

// getSlashCommands returns all available slash commands
func getSlashCommands() []SlashCommand {
	return []SlashCommand{
		{
			Name:        "help",
			Description: "Show available commands and usage information",
			Handler: func(a *Agent) bool {
				showInteractiveHelp()
				return false
			},
		},
		{
			Name:        "log",
			Description: "Show conversation history (preview)",
			Handler: func(a *Agent) bool {
				history := a.GetConversationPreview(1000)
				if strings.TrimSpace(history) == "" {
					fmt.Println("📜 No conversation history found.")
					return false
				}
				fmt.Println(history)
				return false
			},
		},
		{
			Name:        "clear",
			Description: "Clear conversation history and start fresh",
			Handler: func(a *Agent) bool {
				a.ClearHistory()
				fmt.Println("🧹 Conversation history cleared.")
				return false
			},
		},
		{
			Name:        "status",
			Description: "Show current session status and statistics",
			Handler: func(a *Agent) bool {
				showStatus(a)
				return false
			},
		},
		{
			Name:        "tasks",
			Description: "Show all tasks for this project session",
			Handler: func(a *Agent) bool {
				summary := a.GetTaskListDisplay()
				if summary == "" {
					fmt.Println("No tasks.")
				} else {
					fmt.Println(summary)
				}
				return false
			},
		},
		{
			Name:        "quit",
			Description: "Exit the interactive session",
			Handler: func(a *Agent) bool {
				fmt.Println("👋 Goodbye!")
				return true
			},
		},
		{
			Name:        "exit",
			Description: "Exit the interactive session (alias for quit)",
			Handler: func(a *Agent) bool {
				fmt.Println("👋 Goodbye!")
				return true
			},
		},
	}
}

// handleSlashCommand processes commands that start with /
// Returns true if the command requests program exit, false otherwise
func handleSlashCommand(input string, a *Agent) bool {
	// Check if this is just "/" - show command selector
	if strings.TrimSpace(input) == "/" {
		return showCommandSelector(a)
	}

	parts := strings.Fields(input)
	if len(parts) == 0 {
		return false
	}

	commandName := strings.TrimPrefix(parts[0], "/")
	commands := getSlashCommands()

	// Find and execute the command
	for _, cmd := range commands {
		if cmd.Name == commandName {
			return cmd.Handler(a)
		}
	}

	// Command not found - show available commands
	fmt.Printf("❌ Unknown command: /%s\n", commandName)
	fmt.Println("💡 Available commands:")
	for _, cmd := range commands {
		fmt.Printf("  /%s - %s\n", cmd.Name, cmd.Description)
	}
	if pluginCmds := a.ListPluginCommands(); len(pluginCmds) > 0 {
		fmt.Println("💡 Plugin commands:")
		for _, name := range pluginCmds {
			fmt.Printf("  /%s\n", name)
		}
	}
	fmt.Println("\n💡 Tip: Type just '/' to see an interactive command selector!")
	return false
}

// handlePluginCommand dispatches a plugin-defined slash command (loaded via
// --plugin / --plugin-marketplace). Returns true when the input was consumed
// — either successfully invoked or rejected (e.g. ambiguous). Returns false
// when no plugin command matches, so the caller can fall through to the
// built-in dispatcher.
func handlePluginCommand(ctx context.Context, a *Agent, skillName, input string) bool {
	name, args := SplitSlashCommand(input)
	if name == "" {
		return false
	}

	cmd, ambiguous := a.ResolveCommand(name)
	if ambiguous {
		fmt.Fprintf(a.OutWriter(),
			"⚠️  Command %q matches multiple plugins. Use /<plugin>:%s to scope it.\n",
			name, name)
		return true
	}
	if cmd == nil {
		return false
	}

	fmt.Fprintf(a.OutWriter(), "▶ /%s\n", name)
	response, err := a.InvokeCommand(ctx, cmd, args, skillName)
	if err != nil {
		fmt.Fprintf(a.OutWriter(), "Command failed: %v\n", err)
		return true
	}
	w := a.OutWriter()
	model := "unknown"
	if mi, ok := a.GetLLMClient().(domain.ModelIdentifier); ok {
		model = mi.ModelID()
	}
	WriteResponseHeader(w, model, false)
	fmt.Fprintln(w, response.Content())
	return true
}

// showCommandSelector shows an interactive command selector using promptui
func showCommandSelector(a *Agent) bool {
	commands := getSlashCommands()

	templates := &promptui.SelectTemplates{
		Label:    "{{ . }}?",
		Active:   "▸ {{ .Name | cyan }} - {{ .Description | faint }}",
		Inactive: "  {{ .Name | cyan }} - {{ .Description | faint }}",
		Selected: "{{ .Name | red | cyan }}",
		Details: `
--------- Command Details ----------
{{ "Name:" | faint }}\t{{ .Name }}
{{ "Description:" | faint }}\t{{ .Description }}`,
	}

	searcher := func(input string, index int) bool {
		command := commands[index]
		name := strings.ReplaceAll(strings.ToLower(command.Name), " ", "")
		input = strings.ReplaceAll(strings.ToLower(input), " ", "")
		return strings.Contains(name, input)
	}

	prompt := promptui.Select{
		Label:     "Choose a command",
		Items:     commands,
		Templates: templates,
		Size:      10,
		Searcher:  searcher,
	}

	i, _, err := prompt.Run()
	if err != nil {
		if err == promptui.ErrInterrupt {
			fmt.Println("\nCancelled.")
			return false
		}
		fmt.Printf("Command selection failed: %v\n", err)
		return false
	}
	return commands[i].Handler(a)
}

// StartInteractiveMode runs the readline-based REPL
func StartInteractiveMode(ctx context.Context, a *Agent, skillName string) {
	// Configure readline with enhanced features
	// Context display
	contextDisplay := NewContextDisplay()

	// Use a long-lived PromptBuilder for this readline session
	pb := NewPromptBuilder(a.FilesystemRepository(), a.WorkingDir())

	// Create bracketed paste reader wrapping stdin
	pasteReader := NewBracketedPasteReader(readline.Stdin)

	// Enable bracketed paste mode on the terminal
	fmt.Print("\x1b[?2004h")
	defer fmt.Print("\x1b[?2004l")

	rlCfg := &readline.Config{
		Prompt:                 "> ",
		HistoryFile:            "",
		AutoComplete:           createAutoCompleter(),
		InterruptPrompt:        "^C",
		EOFPrompt:              "exit",
		HistorySearchFold:      true,
		HistoryLimit:           2000,
		DisableAutoSaveHistory: true,
		FuncFilterInputRune:    filterInput,
		Stdin:                  pasteReader,
	}

	// Simple listener - let readline handle everything, just sync our state
	rlCfg.SetListener(func(line []rune, pos int, key rune) (newLine []rune, newPos int, ok bool) {
		// Always sync our PromptBuilder state with readline's current state
		pb.SyncFromReadline(line, pos)
		
		// Ctrl+C: allow readline to handle as interrupt
		if key == 3 { // Ctrl+C
			return nil, 0, false
		}
		
		// Ctrl+K: special case - clear our buffer completely if at start
		if key == 11 && pos == 0 { // Ctrl+K at start
			pb.Clear()
			return []rune{}, 0, true
		}
		
		// Let readline handle all other keys (backspace, delete, arrows, typing, etc.)
		// We don't interfere, just stay in sync
		return nil, 0, false
	})

	rl, err := readline.NewEx(rlCfg)
	if err != nil {
		fmt.Printf("❌ Failed to initialize interactive mode: %v\n", err)
		fmt.Println("💡 Please use one-shot mode instead: klein \"your request here\"")
		return
	}
	defer rl.Close()

	// Detect model ID if available
	modelID := "unknown"
	if mi, ok := a.llmClient.(domain.ModelIdentifier); ok {
		modelID = mi.ModelID()
	}

	// Wire the AskUserQuestion tool with an interactive handler that uses
	// promptui for selection menus and a simple prompt for free-form input.
	a.SetInteractiveInputHandler(makeUserInputHandler(a.OutWriter()))

	// Wire the plan approval handler for interactive plan mode.
	a.SetPlanApprovalHandler(makePlanApprovalHandler(a.OutWriter()))

	// Optional splash screen
	WriteSplashScreen(os.Stdout, true)
	fmt.Printf("🧠 Model: %s\n", modelID)
	fmt.Println("💬 Commands start with '/', everything else goes to the AI agent!")
	fmt.Println("⌨️ Arrow keys to navigate; Tab for completion; Ctrl+R searches this session's input.")
	fmt.Println(strings.Repeat("=", 60))

	// On a fresh session inject project context then offer to import CC history.
	if len(a.GetMessageState().GetMessages()) == 0 {
		a.InjectContextFile()
		offerClaudeHistoryImport(a)
	}

	if preview := a.GetConversationPreview(6); preview != "" {
		fmt.Print("\n")
		fmt.Print(preview)
		fmt.Println()
	}

	for {
		pb.Clear() // Clear the prompt buffer at the start of each loop

		// Show task summary + context usage above the prompt.
		line := contextDisplay.ShowStatusLine(a.GetMessageState(), a.GetLLMClient(), a.GetTaskSummary())
		if line != "" {
			fmt.Printf("%s\n", line)
		}

		line, err := rl.Readline()
		if err == readline.ErrInterrupt {
			if len(line) == 0 {
				break
			}
			continue
		} else if err == io.EOF {
			break
		}
		
		// Sync PromptBuilder with the final submitted line
		pb.SyncFromReadline([]rune(line), len([]rune(line)))

		// Attach paste segments from bracketed paste mode
		if segments := pasteReader.GetPasteSegments(); len(segments) > 0 {
			pb.SetPasteSegments(segments)
		}

		// Handle slash commands using the raw buffer so paste compression
		// in VisiblePrompt() does not interfere with detection.
		if pb.IsSlashCommand() {
			cmd := pb.SlashInput()
			// /goal and /loop are multi-turn drivers that need ctx and the
			// active skill, so they are dispatched here rather than via the
			// argument-less handleSlashCommand handlers.
			if handleDrivingCommand(ctx, a, skillName, cmd) {
				pb.Clear()
				rl.Clean()
				rl.Refresh()
				continue
			}
			// Plugin commands (loaded via --plugin / --plugin-marketplace)
			// dispatch before built-ins so they take precedence on bare
			// names. Built-ins like /help, /clear are still reachable
			// because they don't collide with the scoped <plugin>:<name>
			// form and plugins typically don't redefine them.
			if handlePluginCommand(ctx, a, skillName, cmd) {
				pb.Clear()
				rl.Clean()
				rl.Refresh()
				continue
			}
			if handleSlashCommand(cmd, a) {
				break
			}

			// Clear and refresh
			pb.Clear()
			rl.Clean()
			rl.Refresh()
			continue
		}

		// Use builder-captured view for display (may compress pastes)
		userInput := pb.VisiblePrompt()

		// @filename processing is now handled automatically by PromptBuilder
		// VisiblePrompt shows highlights, RawPrompt embeds file content

		if userInput == "" {
			continue
		}

		// Execute one agent turn (shared cancellable path).
		_, canceled, _ := executeTurn(ctx, a, pb.RawPrompt(), skillName)
		if canceled {
			fmt.Printf("🔄 Ready for next command.\n")
		}
	}
}

// createAutoCompleter creates an autocompletion function for readline
func createAutoCompleter() *readline.PrefixCompleter {
	commands := getSlashCommands()
	var pcItems []readline.PrefixCompleterInterface
	for _, cmd := range commands {
		pcItems = append(pcItems, readline.PcItem("/"+cmd.Name))
	}
	// Multi-turn driving commands handled outside getSlashCommands.
	pcItems = append(pcItems, readline.PcItem("/goal"), readline.PcItem("/loop"))
	pcItems = append(pcItems, readline.PcItem("/"))
	for _, pattern := range []string{
		"Create a", "Analyze the", "Write unit tests for", "List files in",
		"Run go build", "Fix any errors", "Explain how", "Show me",
		"Generate", "Debug", "Test", "Refactor",
	} {
		pcItems = append(pcItems, readline.PcItem(pattern))
	}
	return readline.NewPrefixCompleter(pcItems...)
}

// filterInput filters input runes to handle special keys
func filterInput(r rune) (rune, bool) {
	switch r {
	case readline.CharCtrlZ:
		return r, false
	}
	return r, true
}

func showInteractiveHelp() {
	commands := getSlashCommands()
	fmt.Println("\n📚 Interactive Commands:")
	fmt.Println("  /                - Show interactive command selector 🆕")
	for _, cmd := range commands {
		fmt.Printf("  /%-15s - %s\n", cmd.Name, cmd.Description)
	}
	fmt.Printf("  /%-15s - %s\n", "goal <cond>", "Keep working until a fast evaluator confirms <cond> (Ctrl+C to stop)")
	fmt.Printf("  /%-15s - %s\n", "loop [iv] <p>", "Repeat prompt <p> every interval [iv] (e.g. /loop 5m check CI)")
	fmt.Println("\n⌨️  Enhanced Features:")
	fmt.Println("  Ctrl+C           - Cancel current input")
	fmt.Println("  Ctrl+R           - Search this session's input history")
	fmt.Println("  Tab              - Auto-complete commands and patterns")
	fmt.Println("  Arrow keys       - Navigate input and history")
	fmt.Println("  /                - Interactive command selector with search!")
	fmt.Println("\n💡 Example requests:")
	fmt.Println("  > Create a HTTP server with health check")
	fmt.Println("  > Analyze the current codebase structure")
	fmt.Println("  > Write unit tests for the Agent")
	fmt.Println("  > List files in the current directory")
	fmt.Println("  > Run go build and fix any errors")
	fmt.Println("\n✨ New: Type just '/' to see a beautiful command selector!")
	fmt.Println("🔧 The agent will automatically use tools when needed!")
}

// makeUserInputHandler returns a UserInputHandler that uses promptui to collect
// answers from the terminal. When options are provided a selection menu is shown;
// otherwise a free-form text prompt is displayed.
func makeUserInputHandler(w io.Writer) func(question string, options []string) (string, error) {
	return func(question string, options []string) (string, error) {
		fmt.Fprintf(w, "\n")

		if len(options) > 0 {
			// Multiple-choice: present a selection menu
			prompt := promptui.Select{
				Label: question,
				Items: options,
				Templates: &promptui.SelectTemplates{
					Label:    "{{ . }}",
					Active:   "> {{ . | cyan }}",
					Inactive: "  {{ . }}",
					Selected: "{{ . | bold }}",
				},
				Size: len(options),
			}
			_, result, err := prompt.Run()
			if err != nil {
				if err == promptui.ErrInterrupt {
					return "", fmt.Errorf("cancelled by user")
				}
				return "", fmt.Errorf("selection failed: %w", err)
			}
			return result, nil
		}

		// Free-form text input
		prompt := promptui.Prompt{
			Label: question,
		}
		result, err := prompt.Run()
		if err != nil {
			if err == promptui.ErrInterrupt {
				return "", fmt.Errorf("cancelled by user")
			}
			return "", fmt.Errorf("input failed: %w", err)
		}
		return result, nil
	}
}

// makePlanApprovalHandler returns a PlanApprovalHandler that shows the proposed
// plan to the user via a promptui selection menu and waits for approval.
// The returned bool pair is (approved, clearContext).
func makePlanApprovalHandler(w io.Writer) tool.PlanApprovalHandler {
	return func(plan string) (bool, bool, error) {
		fmt.Fprintf(w, "\nProposed Plan:\n%s\n\n", plan)

		const (
			optApproveAndClear = "Approve and clear planning context"
			optApprove         = "Approve"
			optReject          = "Reject"
		)
		items := []string{optApproveAndClear, optApprove, optReject}
		prompt := promptui.Select{
			Label: "Approve this plan?",
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
			if err == promptui.ErrInterrupt {
				return false, false, fmt.Errorf("cancelled by user")
			}
			return false, false, fmt.Errorf("plan approval failed: %w", err)
		}
		switch result {
		case optApproveAndClear:
			return true, true, nil
		case optApprove:
			return true, false, nil
		default:
			return false, false, nil
		}
	}
}

// stdinIsInteractive reports whether stdin is a terminal (character device).
// When stdin is a pipe or file, interactive prompts (promptui selectors) cannot
// read user input and would consume/garble the piped input, so callers should
// skip them.
func stdinIsInteractive() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

// offerClaudeHistoryImport checks whether a Claude Code session file exists for
// the agent's working directory. When one is found the user is prompted to import
// it (y/n). Errors are printed as informational warnings, never fatal.
func offerClaudeHistoryImport(a *Agent) {
	// The import prompt is an interactive selector; skip it entirely when stdin
	// is piped (non-interactive), otherwise it would swallow the piped input.
	if !stdinIsInteractive() {
		return
	}

	jsonlPath, err := claude.FindLatestSession(a.WorkingDir())
	if err != nil {
		fmt.Printf("⚠️  Could not check for Claude history: %v\n", err)
		return
	}
	if jsonlPath == "" {
		return // no history available
	}

	prompt := promptui.Select{
		Label: "Claude Code history found. Import it into this session?",
		Items: []string{"Yes", "No"},
		Templates: &promptui.SelectTemplates{
			Label:    "{{ . }}",
			Active:   "> {{ . | cyan }}",
			Inactive: "  {{ . }}",
			Selected: "{{ . | bold }}",
		},
		Size: 2,
	}
	_, result, err := prompt.Run()
	if err != nil || result != "Yes" {
		return
	}

	count, err := a.ImportClaudeHistory(jsonlPath)
	if err != nil {
		fmt.Printf("⚠️  Failed to import Claude history: %v\n", err)
		return
	}
	fmt.Printf("✅ Imported %d messages from Claude Code history.\n", count)
}

func showStatus(a *Agent) {
	fmt.Println("\n📊 Session Status:")
	preview := a.GetConversationPreview(100)
	if preview != "" {
		userMsgCount := strings.Count(preview, "👤 You:")
		assistantMsgCount := strings.Count(preview, "🤖 Assistant:")
		fmt.Printf("  💬 Messages: %d from you, %d from assistant\n", userMsgCount, assistantMsgCount)
	} else {
		fmt.Println("  💬 Messages: No conversation history")
	}
	fmt.Println("  🔧 Tools: Available and active")
	fmt.Println("  🧠 Agent: ReAct with skill-based tools")
	fmt.Println("  ⚡ Status: Ready for requests")
}
