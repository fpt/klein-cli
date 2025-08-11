package tool

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
	"github.com/fpt/klein-cli/pkg/message"
)

// BashToolManager provides shell command execution capabilities
type BashToolManager struct {
	tools               map[message.ToolName]message.Tool
	workingDir          string
	maxDuration         time.Duration
	whitelistedCommands []string // Commands that don't require approval
}

// BashConfig holds configuration for the bash tool manager
type BashConfig struct {
	WorkingDir          string        `json:"working_dir"`          // Working directory for commands
	MaxDuration         time.Duration `json:"max_duration"`         // Maximum execution time (default: 2 minutes)
	WhitelistedCommands []string      `json:"whitelisted_commands"` // Commands that don't require approval
}

// NewBashToolManager creates a new bash tool manager
func NewBashToolManager(config BashConfig) *BashToolManager {
	if config.MaxDuration == 0 {
		config.MaxDuration = 2 * time.Minute // Default timeout
	}

	manager := &BashToolManager{
		tools:               make(map[message.ToolName]message.Tool),
		workingDir:          config.WorkingDir,
		maxDuration:         config.MaxDuration,
		whitelistedCommands: config.WhitelistedCommands,
	}

	// Register bash tools
	manager.registerBashTools()

	return manager
}

// Implement domain.ToolManager interface
func (m *BashToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	tool, exists := m.tools[name]
	return tool, exists
}

func (m *BashToolManager) GetTools() map[message.ToolName]message.Tool {
	return m.tools
}

func (m *BashToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	tool, exists := m.tools[name]
	if !exists {
		return message.NewToolResultError(fmt.Sprintf("tool %s not found", name)), nil
	}

	handler := tool.Handler()
	return handler(ctx, args)
}

func (m *BashToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, args []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	tool := &bashTool{
		name:        name,
		description: description,
		arguments:   args,
		handler:     handler,
	}
	m.tools[name] = tool
}

// registerBashTools registers all bash command tools
func (m *BashToolManager) registerBashTools() {
	// Primary Bash tool
	m.RegisterTool("bash", "Execute shell commands with timeout and error handling. Prefer tools over shell for file reads/search (use Read/Glob/Grep/LS). Provide a short description; quote paths with spaces.",
		[]message.ToolArgument{
			{
				Name:        "command",
				Description: "Shell command to execute (e.g., 'go build ./klein', 'git status', 'ls -la')",
				Required:    true,
				Type:        "string",
			},
			{
				Name:        "description",
				Description: "Clear description of what this command does (5-10 words)",
				Required:    false,
				Type:        "string",
			},
			{
				Name:        "timeout",
				Description: "Optional timeout in milliseconds (max 600000ms / 10 minutes)",
				Required:    false,
				Type:        "number",
			},
		},
		m.handleBash)

	// Note: dedicated Grep tool is provided by SearchToolManager; avoid duplicating here.
}

// resolvePath resolves a path relative to the working directory
func (m *BashToolManager) resolvePath(path string) (string, error) {
	// If path is already absolute, use it as-is
	if filepath.IsAbs(path) {
		return path, nil
	}

	// Resolve relative path against working directory
	if m.workingDir != "" {
		resolved := filepath.Join(m.workingDir, path)
		return filepath.Abs(resolved)
	}

	// If no working directory set, resolve against current directory
	return filepath.Abs(path)
}

// IsCommandWhitelisted checks if a command is in the whitelist (doesn't require approval)
func (m *BashToolManager) IsCommandWhitelisted(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}

	// Check if command starts with any whitelisted command
	for _, whitelisted := range m.whitelistedCommands {
		if strings.HasPrefix(command, whitelisted) {
			// Make sure it's a complete word match (not just prefix)
			if len(command) == len(whitelisted) {
				return true
			}
			// Check if next character is a space or flag (allowing arguments)
			if len(command) > len(whitelisted) {
				nextChar := command[len(whitelisted)]
				if nextChar == ' ' || nextChar == '\t' {
					return true
				}
			}
		}
	}
	return false
}

// RequiresApproval checks if a bash command requires user approval
func (m *BashToolManager) RequiresApproval(command string) bool {
	return !m.IsCommandWhitelisted(command)
}

// Main Bash handler
func (m *BashToolManager) handleBash(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	command, ok := args["command"].(string)
	if !ok {
		return message.NewToolResultError("command parameter is required"), nil
	}

	// Get optional description
	description := ""
	if desc, ok := args["description"].(string); ok {
		description = desc
	}

	// Get optional timeout (default to manager's maxDuration)
	timeout := m.maxDuration
	if timeoutArg, ok := args["timeout"]; ok {
		if timeoutMs, ok := timeoutArg.(float64); ok {
			// Convert milliseconds to duration, with max limit
			timeoutDuration := time.Duration(timeoutMs) * time.Millisecond
			if timeoutDuration > 10*time.Minute {
				timeoutDuration = 10 * time.Minute // Cap at 10 minutes
			}
			timeout = timeoutDuration
		}
	}

	// Security validation - prevent dangerous commands
	if err := m.validateCommand(command); err != nil {
		return message.NewToolResultError(err.Error()), nil
	}

	// Create context with timeout
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Execute command
	result, err := m.executeCommand(cmdCtx, command, description)
	if err != nil {
		return message.NewToolResultError(err.Error()), nil
	}

	return message.NewToolResultText(result), nil
}

// validateCommand performs security validation on commands
func (m *BashToolManager) validateCommand(command string) error {
	// Remove leading/trailing whitespace
	command = strings.TrimSpace(command)

	if command == "" {
		return fmt.Errorf("command cannot be empty")
	}

	// Block dangerous commands
	dangerousCommands := []string{
		"rm -rf /",
		"sudo rm",
		"mkfs",
		"dd if=",
		":(){ :|:& };:", // Fork bomb
		"chmod -R 777 /",
		"> /dev/sda",
	}

	commandLower := strings.ToLower(command)
	for _, dangerous := range dangerousCommands {
		if strings.Contains(commandLower, dangerous) {
			return fmt.Errorf("dangerous command blocked: %s", dangerous)
		}
	}

	// Block commands that try to modify system files
	if strings.Contains(commandLower, "/etc/") ||
		strings.Contains(commandLower, "/bin/") ||
		strings.Contains(commandLower, "/sbin/") ||
		strings.Contains(commandLower, "/usr/bin/") {
		// Allow read operations
		if !strings.HasPrefix(commandLower, "cat ") &&
			!strings.HasPrefix(commandLower, "less ") &&
			!strings.HasPrefix(commandLower, "head ") &&
			!strings.HasPrefix(commandLower, "tail ") &&
			!strings.HasPrefix(commandLower, "grep ") &&
			!strings.HasPrefix(commandLower, "ls ") {
			return fmt.Errorf("modifying system directories is not allowed")
		}
	}

	return nil
}

// executeCommand executes a shell command and returns the output
func (m *BashToolManager) executeCommand(ctx context.Context, command, description string) (string, error) {
	// Log command execution
	if description != "" {
		logger.InfoWithIntention(pkgLogger.IntentionTool, "Executing command", "description", description, "command", command)
	} else {
		logger.InfoWithIntention(pkgLogger.IntentionTool, "Executing command", "command", command)
	}

	// Prepare command
	cmd := exec.CommandContext(ctx, "bash", "-c", command)

	// Set working directory if specified
	if m.workingDir != "" {
		cmd.Dir = m.workingDir
	}

	// Capture both stdout and stderr
	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	// Handle different exit scenarios
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("command timed out after %v: %s", m.maxDuration, command)
	}

	if err != nil {
		// Check if it's a normal exit error with output
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode := exitError.ExitCode()

			// For some commands (like grep with no matches), non-zero exit is normal
			if exitCode == 1 && (strings.Contains(command, "grep") || strings.Contains(command, "test")) {
				// Return output even with exit code 1 for these commands
				if outputStr == "" {
					return "Command completed with no output (exit code 1)", nil
				}
				return outputStr, nil
			}

			// For other commands, include exit code in error
			return "", fmt.Errorf("command failed (exit code %d): %s\nOutput: %s", exitCode, command, outputStr)
		}

		// Other execution errors
		return "", fmt.Errorf("command execution error: %v\nOutput: %s", err, outputStr)
	}

	// Success case
	if outputStr == "" {
		return "Command completed successfully with no output", nil
	}

	return outputStr, nil
}

// handleRunGrep handles grep pattern searching
func (m *BashToolManager) handleRunGrep(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	pattern, ok := args["pattern"].(string)
	if !ok {
		return message.NewToolResultError("pattern parameter is required"), nil
	}

	// Get path (default to current directory)
	pathParam := "."
	if pathArg, ok := args["path"].(string); ok && pathArg != "" {
		pathParam = pathArg
	}

	// Resolve path relative to working directory
	path, resolveErr := m.resolvePath(pathParam)
	if resolveErr != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to resolve path: %v", resolveErr)), nil
	}

	// Get recursive flag (default to true)
	recursive := true
	if recursiveArg, ok := args["recursive"].(bool); ok {
		recursive = recursiveArg
	}

	// Get case sensitive flag (default to false)
	caseSensitive := false
	if caseArg, ok := args["case_sensitive"].(bool); ok {
		caseSensitive = caseArg
	}

	// Build grep command
	var grepArgs []string
	grepArgs = append(grepArgs, "-n")      // Show line numbers
	grepArgs = append(grepArgs, "-A", "1") // Show 1 line after match
	grepArgs = append(grepArgs, "-B", "1") // Show 1 line before match

	if !caseSensitive {
		grepArgs = append(grepArgs, "-i") // Case insensitive
	}

	if recursive {
		grepArgs = append(grepArgs, "-r") // Recursive search
		// Exclude common directories that should not be searched
		grepArgs = append(grepArgs, "--exclude-dir=.git", "--exclude-dir=node_modules", "--exclude-dir=vendor")
	}

	// Use -F for literal string matching to avoid regex issues
	grepArgs = append(grepArgs, "-F") // Fixed strings (literal) instead of regex
	grepArgs = append(grepArgs, pattern, path)

	cmd := exec.CommandContext(ctx, "grep", grepArgs...)

	// Set working directory if specified
	if m.workingDir != "" {
		cmd.Dir = m.workingDir
	}

	output, err := cmd.CombinedOutput()

	// grep returns exit code 1 when no matches found, which is not an error
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 1 {
			return message.NewToolResultText("No matches found"), nil
		}
		return message.NewToolResultError(fmt.Sprintf("grep command failed: %v\nOutput: %s", err, string(output))), nil
	}

	return message.NewToolResultText(string(output)), nil
}

// bashTool is a helper struct for bash tool registration
type bashTool struct {
	name        message.ToolName
	description message.ToolDescription
	arguments   []message.ToolArgument
	handler     func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)
}

func (t *bashTool) RawName() message.ToolName {
	return t.name
}

func (t *bashTool) Name() message.ToolName {
	return t.name
}

func (t *bashTool) Description() message.ToolDescription {
	return t.description
}

func (t *bashTool) Arguments() []message.ToolArgument {
	return t.arguments
}

func (t *bashTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return t.handler
}
