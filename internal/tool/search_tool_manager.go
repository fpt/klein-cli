package tool

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// SearchToolManager provides Glob and Grep tools
type SearchToolManager struct {
	tools      map[message.ToolName]message.Tool
	workingDir string
}

type SearchConfig struct {
	WorkingDir string
}

func NewSearchToolManager(cfg SearchConfig) domain.ToolManager {
	m := &SearchToolManager{
		tools:      make(map[message.ToolName]message.Tool),
		workingDir: cfg.WorkingDir,
	}
	m.register()
	return m
}

func (m *SearchToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	return m.tools[name], m.tools[name] != nil
}
func (m *SearchToolManager) GetTools() map[message.ToolName]message.Tool { return m.tools }
func (m *SearchToolManager) RegisterTool(name message.ToolName, desc message.ToolDescription, args []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	m.tools[name] = &searchTool{name: name, description: desc, arguments: args, handler: handler}
}
func (m *SearchToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	t, ok := m.tools[name]
	if !ok {
		return message.NewToolResultError(fmt.Sprintf("tool %s not found", name)), nil
	}
	return t.Handler()(ctx, args)
}

func (m *SearchToolManager) register() {
	// Glob tool: fast file listing by pattern
	m.RegisterTool("Glob", "Find files by glob pattern (e.g., **/*.go)",
		[]message.ToolArgument{
			{Name: "pattern", Description: "Glob pattern to match", Required: true, Type: "string"},
			{Name: "path", Description: "Base directory (optional)", Required: false, Type: "string"},
		}, m.handleGlob)

	// Grep tool: ripgrep-style content search
	m.RegisterTool("Grep", "Search file contents using ripgrep-compatible flags",
		[]message.ToolArgument{
			{Name: "pattern", Description: "Regex pattern to search", Required: true, Type: "string"},
			{Name: "path", Description: "File/dir to search (optional)", Required: false, Type: "string"},
			{Name: "glob", Description: "Glob filter (maps to --glob)", Required: false, Type: "string"},
			{Name: "output_mode", Description: "content|files_with_matches|count", Required: false, Type: "string"},
			{Name: "-B", Description: "Lines before (content mode)", Required: false, Type: "number"},
			{Name: "-A", Description: "Lines after (content mode)", Required: false, Type: "number"},
			{Name: "-C", Description: "Lines before/after (content mode)", Required: false, Type: "number"},
			{Name: "-n", Description: "Show line numbers (content mode)", Required: false, Type: "boolean"},
			{Name: "-i", Description: "Case-insensitive", Required: false, Type: "boolean"},
			{Name: "type", Description: "File type (rg --type)", Required: false, Type: "string"},
			{Name: "head_limit", Description: "Limit lines/entries", Required: false, Type: "number"},
			{Name: "multiline", Description: "Dot matches newlines", Required: false, Type: "boolean"},
		}, m.handleGrep)
}

// resolve path relative to working dir
func (m *SearchToolManager) resolvePath(p string) (string, error) {
	if p == "" {
		return m.workingDir, nil
	}
	if filepath.IsAbs(p) {
		return p, nil
	}
	return filepath.Abs(filepath.Join(m.workingDir, p))
}

// handleGlob tries rg --files with --glob when available; falls back to find
func (m *SearchToolManager) handleGlob(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	pattern, ok := args["pattern"].(string)
	if !ok {
		return message.NewToolResultError("pattern parameter is required"), nil
	}
	base := m.workingDir
	if p, ok := args["path"].(string); ok && p != "" {
		rp, err := m.resolvePath(p)
		if err != nil {
			return message.NewToolResultError(fmt.Sprintf("failed to resolve path: %v", err)), nil
		}
		base = rp
	}

	if _, err := exec.LookPath("rg"); err == nil {
		// rg --files --glob <pattern>
		cmd := exec.CommandContext(ctx, "rg", "--files", "--glob", pattern)
		cmd.Dir = base
		out, err := cmd.CombinedOutput()
		if err == nil {
			files := strings.Split(strings.TrimSpace(string(out)), "\n")
			// Portable: alphabetic sort
			sort.Strings(files)
			var b strings.Builder
			for _, f := range files {
				if f == "" {
					continue
				}
				b.WriteString(f)
				b.WriteString("\n")
			}
			return message.NewToolResultText(strings.TrimSuffix(b.String(), "\n")), nil
		}
		// fall through to find on error
	}

	// Fallback: find by -name when pattern has a basename; otherwise list all
	// Note: this is a best-effort portable fallback and may not match ** semantics fully.
	findArgs := []string{"-type", "f"}
	if strings.Contains(pattern, "/") {
		// find supports -name on basename; use -name with last segment
		segs := strings.Split(pattern, "/")
		findArgs = append(findArgs, "-name", segs[len(segs)-1])
	} else {
		findArgs = append(findArgs, "-name", pattern)
	}
	findCallArgs := append([]string{base}, findArgs...)
	cmd := exec.CommandContext(ctx, "find", findCallArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return message.NewToolResultError(fmt.Sprintf("find failed: %v\nOutput: %s", err, string(out))), nil
	}
	return message.NewToolResultText(strings.TrimSpace(string(out))), nil
}

// handleGrep executes ripgrep when available; falls back to grep
func (m *SearchToolManager) handleGrep(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	pattern, ok := args["pattern"].(string)
	if !ok {
		return message.NewToolResultError("pattern parameter is required"), nil
	}

	base := m.workingDir
	if p, ok := args["path"].(string); ok && p != "" {
		rp, err := m.resolvePath(p)
		if err != nil {
			return message.NewToolResultError(fmt.Sprintf("failed to resolve path: %v", err)), nil
		}
		base = rp
	}

	outputMode := "files_with_matches"
	if om, ok := args["output_mode"].(string); ok && om != "" {
		outputMode = om
	}

	useRG := true
	if _, err := exec.LookPath("rg"); err != nil {
		useRG = false
	}

	if useRG {
		rgArgs := []string{}
		switch outputMode {
		case "content":
			// default: show matches
		case "files_with_matches":
			rgArgs = append(rgArgs, "-l")
		case "count":
			rgArgs = append(rgArgs, "-c")
		default:
			// keep default
		}
		if v, ok := args["-B"].(float64); ok {
			rgArgs = append(rgArgs, "-B", fmt.Sprintf("%d", int(v)))
		}
		if v, ok := args["-A"].(float64); ok {
			rgArgs = append(rgArgs, "-A", fmt.Sprintf("%d", int(v)))
		}
		if v, ok := args["-C"].(float64); ok {
			rgArgs = append(rgArgs, "-C", fmt.Sprintf("%d", int(v)))
		}
		if v, ok := args["-n"].(bool); ok && v {
			rgArgs = append(rgArgs, "-n")
		}
		if v, ok := args["-i"].(bool); ok && v {
			rgArgs = append(rgArgs, "-i")
		}
		if v, ok := args["type"].(string); ok && v != "" {
			rgArgs = append(rgArgs, "--type", v)
		}
		if v, ok := args["glob"].(string); ok && v != "" {
			rgArgs = append(rgArgs, "--glob", v)
		}
		if v, ok := args["multiline"].(bool); ok && v {
			rgArgs = append(rgArgs, "-U", "--multiline-dotall")
		}

		rgArgs = append(rgArgs, pattern)
		rgArgs = append(rgArgs, base)

		cmd := exec.CommandContext(ctx, "rg", rgArgs...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			// non-zero with no matches returns exit 1; handle it as empty
			if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
				return message.NewToolResultText(""), nil
			}
			return message.NewToolResultError(fmt.Sprintf("rg failed: %v\nOutput: %s", err, string(out))), nil
		}
		text := string(out)
		// head_limit trim
		if v, ok := args["head_limit"].(float64); ok && int(v) > 0 {
			lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
			if len(lines) > int(v) {
				text = strings.Join(lines[:int(v)], "\n")
			}
		}
		return message.NewToolResultText(strings.TrimRight(text, "\n")), nil
	}

	// Fallback to grep
	grepArgs := []string{"-R"}
	if v, ok := args["-i"].(bool); ok && v {
		grepArgs = append(grepArgs, "-i")
	}
	if v, ok := args["-n"].(bool); ok && v {
		grepArgs = append(grepArgs, "-n")
	}
	if v, ok := args["-B"].(float64); ok {
		grepArgs = append(grepArgs, "-B", fmt.Sprintf("%d", int(v)))
	}
	if v, ok := args["-A"].(float64); ok {
		grepArgs = append(grepArgs, "-A", fmt.Sprintf("%d", int(v)))
	}
	if v, ok := args["-C"].(float64); ok {
		grepArgs = append(grepArgs, "-C", fmt.Sprintf("%d", int(v)))
	}
	grepArgs = append(grepArgs, "-E", pattern, base)
	cmd := exec.CommandContext(ctx, "grep", grepArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return message.NewToolResultText(""), nil
		}
		return message.NewToolResultError(fmt.Sprintf("grep failed: %v\nOutput: %s", err, string(out))), nil
	}
	text := string(out)
	if v, ok := args["head_limit"].(float64); ok && int(v) > 0 {
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		if len(lines) > int(v) {
			text = strings.Join(lines[:int(v)], "\n")
		}
	}
	return message.NewToolResultText(strings.TrimRight(text, "\n")), nil
}

type searchTool struct {
	name        message.ToolName
	description message.ToolDescription
	arguments   []message.ToolArgument
	handler     func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)
}

func (t *searchTool) RawName() message.ToolName            { return t.name }
func (t *searchTool) Name() message.ToolName               { return t.name }
func (t *searchTool) Description() message.ToolDescription { return t.description }
func (t *searchTool) Arguments() []message.ToolArgument    { return t.arguments }
func (t *searchTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return t.handler
}
