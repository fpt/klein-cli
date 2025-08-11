package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/chzyer/readline"
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
	fmt.Println("\n💡 Tip: Type just '/' to see an interactive command selector!")
	return false
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

	// Optional splash screen
	WriteSplashScreen(os.Stdout, true)
	fmt.Printf("🧠 Model: %s\n", modelID)
	fmt.Println("💬 Commands start with '/', everything else goes to the AI agent!")
	fmt.Println("⌨️ Arrow keys to navigate; Tab for completion; Ctrl+R searches this session's input.")
	fmt.Println(strings.Repeat("=", 60))

	if preview := a.GetConversationPreview(6); preview != "" {
		fmt.Print("\n")
		fmt.Print(preview)
		fmt.Println()
	}

	for {
		pb.Clear() // Clear the prompt buffer at the start of each loop

		// Show context usage above the prompt, reflecting the latest LLM turn
		line := contextDisplay.ShowContextUsage(a.GetMessageState(), a.GetLLMClient())
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

		// Execute via scenario runner with cancellable context
		// Set up signal handling for Ctrl+C during execution
		execCtx, cancel := context.WithCancel(ctx)
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT)

		// Handle Ctrl+C during execution in a goroutine
		go func() {
			select {
			case <-sigChan:
				fmt.Println() // Move to new line after ^C
				cancel()      // Cancel the execution context
			case <-execCtx.Done():
				// Execution finished, clean up
			}
		}()

		response, invokeErr := a.Invoke(execCtx, pb.RawPrompt(), skillName)

		// Check for cancellation BEFORE cleaning up
		wasCanceled := execCtx.Err() == context.Canceled

		// Clean up signal handling
		signal.Stop(sigChan)
		close(sigChan)
		cancel()

		if invokeErr != nil {
			// Check if the error was due to cancellation
			if wasCanceled {
				fmt.Printf("🔄 Ready for next command.\n")
			} else {
				fmt.Printf("❌ Error: %v\n", invokeErr)
			}
			continue
		}
		// Print response via Agent's writer with model header
		w := a.OutWriter()
		model := a.GetLLMClient().ModelID()
		// Skyblue/bright-cyan header without icon
		WriteResponseHeader(w, model, true)
		fmt.Fprintln(w, response.Content())

		// No placeholder state to reset
	}
}

// createAutoCompleter creates an autocompletion function for readline
func createAutoCompleter() *readline.PrefixCompleter {
	commands := getSlashCommands()
	var pcItems []readline.PrefixCompleterInterface
	for _, cmd := range commands {
		pcItems = append(pcItems, readline.PcItem("/"+cmd.Name))
	}
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
