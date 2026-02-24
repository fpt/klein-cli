package tool

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// MemoryToolManager provides memory_search and memory_get tools for the claw memory system.
type MemoryToolManager struct {
	tools   map[message.ToolName]message.Tool
	baseDir string // e.g. ~/.klein/claw/memory
}

// NewMemoryToolManager creates a memory tool manager rooted at the given base directory.
func NewMemoryToolManager(baseDir string) domain.ToolManager {
	m := &MemoryToolManager{
		tools:   make(map[message.ToolName]message.Tool),
		baseDir: baseDir,
	}
	m.register()
	return m
}

func (m *MemoryToolManager) register() {
	m.RegisterTool("memory_search",
		"Search memory files (MEMORY.md and daily notes) for a keyword. Returns matching lines with file paths and line numbers.",
		[]message.ToolArgument{
			{Name: "query", Description: "Keyword or phrase to search for (case-insensitive)", Required: true, Type: "string"},
			{Name: "max_results", Description: "Maximum number of matching lines to return (default: 20)", Required: false, Type: "number"},
		},
		m.handleMemorySearch)

	m.RegisterTool("memory_get",
		"Read a specific memory file. Use 'MEMORY.md' for long-term memory, or 'daily/YYYY-MM-DD.md' for a daily note. Returns the file content or empty string if not found.",
		[]message.ToolArgument{
			{Name: "path", Description: "Relative path within the memory directory (e.g., 'MEMORY.md', 'daily/2024-01-15.md')", Required: true, Type: "string"},
		},
		m.handleMemoryGet)
}

func (m *MemoryToolManager) handleMemorySearch(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return message.NewToolResultError("query parameter is required"), nil
	}

	maxResults := 20
	if v, ok := args["max_results"].(float64); ok && int(v) > 0 {
		maxResults = int(v)
	}

	queryLower := strings.ToLower(query)
	var matches []string

	// Collect all memory files to search
	var files []string

	// MEMORY.md
	memPath := filepath.Join(m.baseDir, "MEMORY.md")
	if _, err := os.Stat(memPath); err == nil {
		files = append(files, memPath)
	}

	// daily/*.md
	dailyDir := filepath.Join(m.baseDir, "daily")
	entries, err := os.ReadDir(dailyDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				files = append(files, filepath.Join(dailyDir, e.Name()))
			}
		}
	}

	if len(files) == 0 {
		return message.NewToolResultText("No memory files found."), nil
	}

	for _, filePath := range files {
		if len(matches) >= maxResults {
			break
		}

		relPath, _ := filepath.Rel(m.baseDir, filePath)
		f, err := os.Open(filePath)
		if err != nil {
			continue
		}

		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if strings.Contains(strings.ToLower(line), queryLower) {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", relPath, lineNum, line))
				if len(matches) >= maxResults {
					break
				}
			}
		}
		f.Close()
	}

	if len(matches) == 0 {
		return message.NewToolResultText(fmt.Sprintf("No matches found for %q in memory files.", query)), nil
	}

	return message.NewToolResultText(strings.Join(matches, "\n")), nil
}

func (m *MemoryToolManager) handleMemoryGet(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	relPath, ok := args["path"].(string)
	if !ok || relPath == "" {
		return message.NewToolResultError("path parameter is required"), nil
	}

	// Prevent directory traversal
	cleaned := filepath.Clean(relPath)
	if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
		return message.NewToolResultError("path must be relative within the memory directory"), nil
	}

	fullPath := filepath.Join(m.baseDir, cleaned)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return message.NewToolResultText(""), nil
		}
		return message.NewToolResultError(fmt.Sprintf("failed to read memory file: %v", err)), nil
	}

	return message.NewToolResultText(string(data)), nil
}

// ToolManager interface implementation

func (m *MemoryToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	t, ok := m.tools[name]
	return t, ok
}

func (m *MemoryToolManager) GetTools() map[message.ToolName]message.Tool {
	return m.tools
}

func (m *MemoryToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	t, ok := m.tools[name]
	if !ok {
		return message.NewToolResultError(fmt.Sprintf("tool '%s' not found", name)), nil
	}
	return t.Handler()(ctx, args)
}

func (m *MemoryToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	m.tools[name] = &memoryTool{
		name:        name,
		description: description,
		arguments:   arguments,
		handler:     handler,
	}
}

// memoryTool implements message.Tool
type memoryTool struct {
	name        message.ToolName
	description message.ToolDescription
	arguments   []message.ToolArgument
	handler     func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)
}

func (t *memoryTool) RawName() message.ToolName            { return t.name }
func (t *memoryTool) Name() message.ToolName               { return t.name }
func (t *memoryTool) Description() message.ToolDescription { return t.description }
func (t *memoryTool) Arguments() []message.ToolArgument    { return t.arguments }
func (t *memoryTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return t.handler
}
