package tool

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// MemoryToolManager provides MemorySearch and MemoryGet tools for the claw memory system.
type MemoryToolManager struct {
	tools   map[message.ToolName]message.Tool
	baseDir string // e.g. ~/.klein/memory

	// writeMu serializes MemoryWrite operations. This manager is a single
	// instance shared by every concurrent agent session in a gateway (a Discord
	// peer and a firing cron can call MemoryWrite at the same time), so the
	// writes need in-process mutual exclusion. Cross-process writers (the REPL)
	// rely on atomic file replacement instead.
	writeMu sync.Mutex
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
	m.RegisterTool("MemorySearch",
		"Search memory files for a keyword: MEMORY.md, daily notes (daily/*.md), and scheduled-run "+
			"output logs (runs/*.md — what each cron job produced). Returns matching lines with file paths and line numbers.",
		[]message.ToolArgument{
			{Name: "query", Description: "Keyword or phrase to search for (case-insensitive)", Required: true, Type: "string"},
			{Name: "max_results", Description: "Maximum number of matching lines to return (default: 20)", Required: false, Type: "number"},
		},
		m.handleMemorySearch)

	m.RegisterTool("MemoryGet",
		"Read a specific memory file. 'MEMORY.md' = long-term memory; 'daily/YYYY-MM-DD.md' = a daily note; "+
			"'runs/YYYY-MM-DD.md' = that day's scheduled-run outputs (each cron job's report, timestamped). "+
			"Returns the file content or empty string if not found.",
		[]message.ToolArgument{
			{Name: "path", Description: "Relative path within the memory directory (e.g., 'MEMORY.md', 'daily/2024-01-15.md', 'runs/2024-01-15.md')", Required: true, Type: "string"},
		},
		m.handleMemoryGet)

	m.RegisterTool("MemoryWrite",
		"Persist information to a memory file so it survives across conversations. Use 'MEMORY.md' for durable long-term facts, or 'daily/YYYY-MM-DD.md' for a dated note. "+
			"mode='append' (default) adds to the end; mode='overwrite' replaces the whole file (read first with MemoryGet, then write back the edited content to update/dedupe). Creates the file and directories as needed.",
		[]message.ToolArgument{
			{Name: "path", Description: "Relative path within the memory directory (e.g., 'MEMORY.md', 'daily/2024-01-15.md')", Required: true, Type: "string"},
			{Name: "content", Description: "Text to write. For append mode, a single entry/line is typical (a trailing newline is added).", Required: true, Type: "string"},
			{Name: "mode", Description: "'append' (default) or 'overwrite'", Required: false, Type: "string"},
		},
		m.handleMemoryWrite)
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

	// daily/*.md (journal notes) and runs/*.md (scheduled-run output logs)
	for _, sub := range []string{"daily", "runs"} {
		dir := filepath.Join(m.baseDir, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				files = append(files, filepath.Join(dir, e.Name()))
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

func (m *MemoryToolManager) handleMemoryWrite(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	relPath, ok := args["path"].(string)
	if !ok || relPath == "" {
		return message.NewToolResultError("path parameter is required"), nil
	}
	content, ok := args["content"].(string)
	if !ok {
		return message.NewToolResultError("content parameter is required"), nil
	}

	mode := "append"
	if v, ok := args["mode"].(string); ok && v != "" {
		mode = strings.ToLower(strings.TrimSpace(v))
	}
	if mode != "append" && mode != "overwrite" {
		return message.NewToolResultError("mode must be 'append' or 'overwrite'"), nil
	}

	// Prevent directory traversal (same rule as MemoryGet).
	cleaned := filepath.Clean(relPath)
	if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
		return message.NewToolResultError("path must be relative within the memory directory"), nil
	}
	fullPath := filepath.Join(m.baseDir, cleaned)

	// Serialize concurrent writes from other sessions in this process.
	m.writeMu.Lock()
	defer m.writeMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to create memory directory: %v", err)), nil
	}

	if mode == "overwrite" {
		// Atomic replace so a concurrent reader never sees a truncated file.
		if err := atomicWriteFile(fullPath, []byte(content), 0o644); err != nil {
			return message.NewToolResultError(fmt.Sprintf("failed to write memory file: %v", err)), nil
		}
		return message.NewToolResultText(fmt.Sprintf("Wrote %d bytes to %s (overwrite).", len(content), cleaned)), nil
	}

	// append: read-modify-write atomically so appends don't interleave and a
	// reader never catches a partial file. (O_APPEND alone is atomic per write
	// but does not compose with the overwrite path's rename.)
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	existing, err := os.ReadFile(fullPath)
	if err != nil && !os.IsNotExist(err) {
		return message.NewToolResultError(fmt.Sprintf("failed to read memory file: %v", err)), nil
	}
	if err := atomicWriteFile(fullPath, append(existing, content...), 0o644); err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to append to memory file: %v", err)), nil
	}
	return message.NewToolResultText(fmt.Sprintf("Appended %d bytes to %s.", len(content), cleaned)), nil
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
