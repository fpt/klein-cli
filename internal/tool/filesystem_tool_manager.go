package tool

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fpt/klein-cli/internal/repository"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
	"github.com/fpt/klein-cli/pkg/message"
	"github.com/pkg/errors"
)

var errNotInAllowedDirectory = errors.New("file access denied: path is not within allowed directories")

// FileSystemToolManager provides secure file system operations with safety controls
type FileSystemToolManager struct {
	// Filesystem operations
	fsRepo repository.FilesystemRepository // Injected filesystem repository

	// Access control
	allowedDirectories []string // Directories where file operations are allowed
	blacklistedFiles   []string // Files that cannot be read (to prevent secret leaks)

	// Working directory context
	workingDir string // Working directory for resolving relative paths

	// Read-write semantics tracking
	fileReadTimestamps map[string]time.Time // Track when files were last read
	mu                 sync.RWMutex         // Thread safety for timestamp tracking

	// Edit failure tracking (for ToolStateProvider)
	editFailCounts map[string]int // Consecutive old_string-not-found failures per abs path

	// Tool registry
	tools map[message.ToolName]message.Tool
}

// NewFileSystemToolManager creates a new secure filesystem tool manager
func NewFileSystemToolManager(fsRepo repository.FilesystemRepository, config repository.FileSystemConfig, workingDir string) *FileSystemToolManager {
	absWorkingDir, err := filepath.Abs(workingDir)
	if err != nil {
		absWorkingDir = workingDir
	}

	// Ensure working directory is always in allowed directories for backward compatibility
	allowedDirs := ensureWorkingDirectoryInAllowedList(config.AllowedDirectories, absWorkingDir)

	manager := &FileSystemToolManager{
		fsRepo:             fsRepo,
		allowedDirectories: allowedDirs,
		blacklistedFiles:   config.BlacklistedFiles,
		workingDir:         absWorkingDir,
		fileReadTimestamps: make(map[string]time.Time),
		editFailCounts:     make(map[string]int),
		tools:              make(map[message.ToolName]message.Tool),
	}

	// Register filesystem tools
	manager.registerFileSystemTools()

	return manager
}

// ensureWorkingDirectoryInAllowedList ensures the working directory is always included
// in the allowed directories list for backward compatibility.
// It returns a new slice with the working directory included if not already present.
func ensureWorkingDirectoryInAllowedList(configuredDirectories []string, absWorkingDir string) []string {
	// Create a copy of the original slice to avoid modifying the input
	allowedDirs := make([]string, len(configuredDirectories))
	copy(allowedDirs, configuredDirectories)

	// Check if working directory is already present
	workingDirPresent := false
	for _, dir := range allowedDirs {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue // Skip invalid directories
		}
		if absDir == absWorkingDir {
			workingDirPresent = true
			break
		}
	}

	// Add working directory if not already present
	if !workingDirPresent {
		allowedDirs = append(allowedDirs, absWorkingDir)
	}

	return allowedDirs
}

// Implement domain.ToolManager interface
func (m *FileSystemToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	tool, exists := m.tools[name]
	return tool, exists
}

func (m *FileSystemToolManager) GetTools() map[message.ToolName]message.Tool {
	return m.tools
}

func (m *FileSystemToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	tool, exists := m.tools[name]
	if !exists {
		return message.NewToolResultError(fmt.Sprintf("tool %s not found", name)), nil
	}

	handler := tool.Handler()
	return handler(ctx, args)
}

func (m *FileSystemToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, args []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	tool := &fileSystemTool{
		name:        name,
		description: description,
		arguments:   args,
		handler:     handler,
	}
	m.tools[name] = tool
}

// registerFileSystemTools registers all secure filesystem tools
func (m *FileSystemToolManager) registerFileSystemTools() {
	// Read with optional offset/limit and line-numbered output
	m.RegisterTool("Read", "Read a file with optional offset/limit and line-numbered output",
		[]message.ToolArgument{
			{Name: "file_path", Description: "Path to the file to read", Required: true, Type: "string"},
			{Name: "offset", Description: "1-based line start (optional)", Required: false, Type: "number"},
			{Name: "limit", Description: "Number of lines to return (optional)", Required: false, Type: "number"},
		},
		m.handleRead)

	// Write
	m.RegisterTool("Write", "Write full content to a file",
		[]message.ToolArgument{
			{Name: "file_path", Description: "Path to the file to write", Required: true, Type: "string"},
			{Name: "content", Description: "Full file content", Required: true, Type: "string"},
		},
		m.handleWrite)

	// Edit
	m.RegisterTool("Edit", "Exact string replacement in a file (requires read-before-write semantics)",
		[]message.ToolArgument{
			{Name: "file_path", Description: "Path to the file to edit", Required: true, Type: "string"},
			{Name: "old_string", Description: "Exact string to replace (unique unless replace_all)", Required: true, Type: "string"},
			{Name: "new_string", Description: "Replacement string", Required: true, Type: "string"},
			{Name: "replace_all", Description: "Replace all occurrences (default false)", Required: false, Type: "boolean"},
		},
		m.handleEdit)

	// LS with ignore globs
	m.RegisterTool("LS", "List directory contents with optional ignore globs",
		[]message.ToolArgument{
			{Name: "path", Description: "Directory path to list", Required: true, Type: "string"},
			{Name: "ignore", Description: "Array of glob patterns to ignore", Required: false, Type: "array"},
		},
		m.handleLS)

	// MultiEdit: apply multiple precise edits across files in one call
	m.RegisterTool("MultiEdit", "Apply multiple exact string replacements across files in a single, atomic batch. Requires prior Read of target files.",
		[]message.ToolArgument{
			{
				Name:        "edits",
				Description: "Array of edit objects, each containing file_path, old_string, new_string, and optional replace_all",
				Required:    true,
				Type:        "array",
				Properties: map[string]any{
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"file_path": map[string]any{
								"type":        "string",
								"description": "Path to the file to edit",
							},
							"old_string": map[string]any{
								"type":        "string",
								"description": "Exact string to replace (must match exactly)",
							},
							"new_string": map[string]any{
								"type":        "string",
								"description": "New string to replace with",
							},
							"replace_all": map[string]any{
								"type":        "boolean",
								"description": "Replace all occurrences (default: false)",
								"default":     false,
							},
						},
						"required": []string{"file_path", "old_string", "new_string"},
					},
				},
			},
		},
		m.handleMultiEdit)
}

// Security validation methods

// abs resolves a path to absolute form relative to the tool's working directory
// This replaces filepath.Abs to avoid resolving against the process's current working directory
func (m *FileSystemToolManager) abs(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}

	// For relative paths, resolve against the tool's working directory
	resolved := filepath.Join(m.workingDir, path)

	// Clean the path to handle . and .. elements
	return filepath.Clean(resolved), nil
}

// resolvePath resolves a path relative to the working directory
func (m *FileSystemToolManager) resolvePath(path string) (string, error) {
	// If path is already absolute, check if it's within working directory
	if filepath.IsAbs(path) {
		// Get absolute form of working directory using our own method
		absWorkingDir, err := m.abs(m.workingDir)
		if err != nil {
			return "", fmt.Errorf("failed to resolve working directory: %v", err)
		}

		// Check if the absolute path is within the working directory
		if strings.HasPrefix(path, absWorkingDir+string(os.PathSeparator)) || path == absWorkingDir {
			return path, nil
		}

		// If absolute path is outside working directory, reject it
		return "", fmt.Errorf("absolute path %s is outside working directory %s", path, m.workingDir)
	}

	// Resolve relative path against working directory using our own method
	return m.abs(path)
}

// isPathAllowed checks if a file path is within allowed directories
func (m *FileSystemToolManager) isPathAllowed(path string) error {
	// Note: allowedDirectories always contains at least the working directory (ensured in constructor)

	// Expect path to already be absolute (resolved by caller)
	absPath := path

	// Check if path is within any allowed directory
	for _, allowedDir := range m.allowedDirectories {
		allowedAbs, err := m.abs(allowedDir)
		if err != nil {
			continue // Skip invalid allowed directory
		}

		// Check if the file path is under the allowed directory
		if strings.HasPrefix(absPath, allowedAbs+string(os.PathSeparator)) || absPath == allowedAbs {
			return nil
		}
	}

	return errNotInAllowedDirectory
}

// isFileBlacklisted checks if a file is in the blacklist
func (m *FileSystemToolManager) isFileBlacklisted(path string) error {
	fileName := filepath.Base(path)
	absPath := path // Expect path to already be absolute (resolved by caller)

	for _, blacklisted := range m.blacklistedFiles {
		// Check both filename and full path patterns
		if matched, _ := filepath.Match(blacklisted, fileName); matched {
			return fmt.Errorf("file access denied: %s matches blacklisted pattern %s", fileName, blacklisted)
		}
		if matched, _ := filepath.Match(blacklisted, absPath); matched {
			return fmt.Errorf("file access denied: %s matches blacklisted pattern %s", absPath, blacklisted)
		}
		// Also check for exact matches
		if fileName == blacklisted || absPath == blacklisted {
			return fmt.Errorf("file access denied: %s is blacklisted", path)
		}
	}

	return nil
}

// validateReadWriteSemantics checks if a write operation is safe based on read timestamps
func (m *FileSystemToolManager) validateReadWriteSemantics(ctx context.Context, path string) error {
	m.mu.RLock()
	lastReadTime, wasRead := m.fileReadTimestamps[path]
	m.mu.RUnlock()

	if !wasRead {
		return fmt.Errorf("read-write semantics violation: file %s was not read before write attempt", path)
	}

	// Check if file was modified since last read
	fileInfo, err := m.fsRepo.Stat(ctx, path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to check file modification time: %v", err)
	}

	if err == nil && fileInfo.ModTime().After(lastReadTime) {
		return fmt.Errorf("read-write semantics violation: file %s was modified after last read", path)
	}

	return nil
}

// recordFileRead records that a file was successfully read
func (m *FileSystemToolManager) recordFileRead(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fileReadTimestamps[path] = time.Now()
}

// Tool handlers with security

func (m *FileSystemToolManager) handleReadFile(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	pathParam, ok := args["path"].(string)
	if !ok {
		return message.NewToolResultError("path parameter is required"), nil
	}

	// Resolve path relative to working directory
	path, resolveErr := m.resolvePath(pathParam)
	if resolveErr != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to resolve path: %v", resolveErr)), nil
	}

	// Security checks
	if err := m.isPathAllowed(path); err != nil {
		return message.NewToolResultError(err.Error()), nil
	}

	if err := m.isFileBlacklisted(path); err != nil {
		return message.NewToolResultError(err.Error()), nil
	}

	// Perform the read operation
	content, err := m.fsRepo.ReadFile(ctx, path)
	if err != nil {
		// Even if read fails, record the attempt for read-write semantics
		// This allows creating new files after attempting to read them
		if os.IsNotExist(err) {
			m.recordFileRead(path)
			return message.NewToolResultError(fmt.Sprintf("file does not exist: %s", path)), nil
		}
		return message.NewToolResultError(fmt.Sprintf("failed to read file: %v", err)), nil
	}

	// Record successful read for read-write semantics
	m.recordFileRead(path)

	return message.NewToolResultText(string(content)), nil
}

func (m *FileSystemToolManager) handleWriteFile(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	logger.DebugWithIntention(pkgLogger.IntentionDebug, "Write file operation started",
		"arg_count", len(args))

	pathParam, ok := args["path"].(string)
	if !ok {
		return message.NewToolResultError("path parameter is required and must be a string"), nil
	}

	// Resolve path relative to working directory
	path, resolveErr := m.resolvePath(pathParam)
	if resolveErr != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to resolve path: %v", resolveErr)), nil
	}

	content, ok := args["content"].(string)
	if !ok {
		return message.NewToolResultError("content parameter is required and must be a string"), nil
	}

	logger.DebugWithIntention(pkgLogger.IntentionDebug, "Write file proceeding",
		"path", path, "content_length", len(content))

	// Security checks
	if err := m.isPathAllowed(path); err != nil {
		return message.NewToolResultError(err.Error()), nil
	}

	// Check if the file exists - only validate read-write semantics for existing files
	if _, err := m.fsRepo.Stat(ctx, path); err == nil {
		// File exists - validate read-write semantics
		if err := m.validateReadWriteSemantics(ctx, path); err != nil {
			return message.NewToolResultError(err.Error()), nil
		}
	} else if !os.IsNotExist(err) {
		// Other error (permission, etc.) - report it
		return message.NewToolResultError(fmt.Sprintf("failed to check file status: %v", err)), nil
	}
	// If file doesn't exist (os.IsNotExist), allow creating new file without validation

	// Create directory if it doesn't exist
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to create directory: %v", err)), nil
	}

	// Perform the write operation
	if err := m.fsRepo.WriteFile(ctx, path, []byte(content), 0644); err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to write file: %v", err)), nil
	}

	// Update read timestamp after successful write to allow sequential edits
	m.recordFileRead(path)

	// Run auto-validation based on file type
	validationResult := m.autoValidateFile(ctx, path)

	return message.NewToolResultText(fmt.Sprintf("Successfully wrote to %s%s", path, validationResult)), nil
}

func (m *FileSystemToolManager) handleEnhancedEdit(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	filePath, ok := args["file_path"].(string)
	if !ok {
		return message.NewToolResultError("file_path parameter is required"), nil
	}

	oldString, ok := args["old_string"].(string)
	if !ok {
		return message.NewToolResultError("old_string parameter is required"), nil
	}

	newString, ok := args["new_string"].(string)
	if !ok {
		return message.NewToolResultError("new_string parameter is required"), nil
	}

	// Get replace_all parameter (defaults to false)
	replaceAll := false
	if val, exists := args["replace_all"]; exists {
		if boolVal, ok := val.(bool); ok {
			replaceAll = boolVal
		}
	}

	// Validate that old_string and new_string are different
	if oldString == newString {
		return message.NewToolResultError("old_string and new_string cannot be identical"), nil
	}

	// Resolve path relative to working directory
	absPath, err := m.resolvePath(filePath)
	if err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to resolve path: %v", err)), nil
	}

	// Security checks
	if err := m.isPathAllowed(absPath); err != nil {
		return message.NewToolResultError(err.Error()), nil
	}

	if err := m.isFileBlacklisted(absPath); err != nil {
		return message.NewToolResultError(err.Error()), nil
	}

	// Read-write semantics validation
	if err := m.validateReadWriteSemantics(ctx, absPath); err != nil {
		return message.NewToolResultError(err.Error()), nil
	}

	// Read the file
	content, err := m.fsRepo.ReadFile(ctx, absPath)
	if err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to read file %s: %v", absPath, err)), nil
	}

	fileContent := string(content)

	// Validate exact string matching, with indentation normalization fallback
	if !strings.Contains(fileContent, oldString) {
		// Try indentation normalization: detect whether the file uses tabs or 4-spaces and
		// normalize old_string/new_string to match.  This handles the common case where a
		// model writes a file with one style (e.g. tabs from gofmt) and then provides an
		// old_string with the other style (e.g. 4-spaces).
		fileUsesTabs := strings.Contains(fileContent, "\n\t") || strings.HasPrefix(fileContent, "\t")
		var normalizedOld string
		if fileUsesTabs {
			normalizedOld = strings.ReplaceAll(oldString, "    ", "\t")
		} else {
			normalizedOld = strings.ReplaceAll(oldString, "\t", "    ")
		}
		if normalizedOld != oldString && strings.Contains(fileContent, normalizedOld) {
			// Apply the same normalization to new_string so the replacement is consistent
			if fileUsesTabs {
				newString = strings.ReplaceAll(newString, "    ", "\t")
			} else {
				newString = strings.ReplaceAll(newString, "\t", "    ")
			}
			oldString = normalizedOld
		} else {
			m.mu.Lock()
			m.editFailCounts[absPath]++
			m.mu.Unlock()
			return message.NewToolResultError(fmt.Sprintf("old_string not found in file %s. Please ensure exact whitespace and formatting match.", absPath)), nil
		}
	}

	// Count occurrences for safety
	occurrences := strings.Count(fileContent, oldString)
	if occurrences == 0 {
		m.mu.Lock()
		m.editFailCounts[absPath]++
		m.mu.Unlock()
		return message.NewToolResultError(fmt.Sprintf("old_string not found in file %s", absPath)), nil
	}

	// Check if multiple occurrences exist and replace_all is false
	if occurrences > 1 && !replaceAll {
		return message.NewToolResultError(fmt.Sprintf("old_string appears %d times in file %s (use replace_all=true to replace all occurrences)", occurrences, absPath)), nil
	}

	// Perform the replacement
	var newContent string
	if replaceAll {
		// Replace all occurrences
		newContent = strings.ReplaceAll(fileContent, oldString, newString)
	} else {
		// Replace only the first occurrence
		newContent = strings.Replace(fileContent, oldString, newString, 1)
	}

	// Verify that the replacement actually changed the content
	if newContent == fileContent {
		return message.NewToolResultError("no changes made to file - old_string and new_string may be identical"), nil
	}

	// Write the modified content back to the file
	if err := m.fsRepo.WriteFile(ctx, absPath, []byte(newContent), 0644); err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to write file %s: %v", absPath, err)), nil
	}

	// Update read timestamp after successful edit to allow sequential edits
	m.recordFileRead(absPath)

	// Reset consecutive Edit failure counter now that the edit succeeded
	m.mu.Lock()
	delete(m.editFailCounts, absPath)
	m.mu.Unlock()

	// Calculate change statistics for feedback
	oldLines := strings.Count(oldString, "\n") + 1
	newLines := strings.Count(newString, "\n") + 1

	// Create success message with occurrence information
	var occurrenceInfo string
	if replaceAll {
		occurrenceInfo = fmt.Sprintf("Replaced %d occurrence(s)", occurrences)
	} else {
		occurrenceInfo = "Replaced 1 occurrence"
	}

	// Run auto-validation based on file type
	validationResult := m.autoValidateFile(ctx, absPath)

	return message.NewToolResultText(fmt.Sprintf("Successfully edited %s\n%s\nReplaced %d line(s) with %d line(s)\nOld content: %d characters\nNew content: %d characters%s",
		absPath, occurrenceInfo, oldLines, newLines, len(oldString), len(newString), validationResult)), nil
}

// imageExtensions lists file extensions recognized as images for vision analysis.
var imageExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true,
	".gif": true, ".webp": true, ".bmp": true,
}

// handleRead implements Read with optional paging and line numbering.
// For image files, it returns a resized base64-encoded image for vision analysis.
func (m *FileSystemToolManager) handleRead(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	pathParam, ok := args["file_path"].(string)
	if !ok {
		return message.NewToolResultError("file_path parameter is required"), nil
	}

	path, resolveErr := m.resolvePath(pathParam)
	if resolveErr != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to resolve path: %v", resolveErr)), nil
	}
	if err := m.isPathAllowed(path); err != nil {
		return message.NewToolResultError(err.Error()), nil
	}
	if err := m.isFileBlacklisted(path); err != nil {
		return message.NewToolResultError(err.Error()), nil
	}

	contentBytes, err := m.fsRepo.ReadFile(ctx, path)
	if err != nil {
		if os.IsNotExist(err) {
			m.recordFileRead(path)
			return message.NewToolResultError(fmt.Sprintf("file does not exist: %s", path)), nil
		}
		return message.NewToolResultError(fmt.Sprintf("failed to read file: %v", err)), nil
	}

	// Record successful read and clear any Edit failure history for this file
	m.recordFileRead(path)
	m.mu.Lock()
	delete(m.editFailCounts, path)
	m.mu.Unlock()

	// If file is an image, resize and return as base64 for vision analysis
	ext := strings.ToLower(filepath.Ext(path))
	if imageExtensions[ext] {
		resized, resizeErr := ResizeImageToJPEG(contentBytes, MaxImageDim, MaxJPEGQuality)
		if resizeErr != nil {
			// Fallback: return raw base64 if resize fails
			b64 := base64.StdEncoding.EncodeToString(contentBytes)
			desc := fmt.Sprintf("Image file: %s (%dKB, resize failed: %v). Analyze the attached image.", path, len(contentBytes)/1024, resizeErr)
			return message.NewToolResultWithImages(desc, []string{b64}), nil
		}
		b64 := base64.StdEncoding.EncodeToString(resized)
		desc := fmt.Sprintf("Image file: %s (original %dKB, resized to JPEG %dKB). Analyze the attached image.", path, len(contentBytes)/1024, len(resized)/1024)
		return message.NewToolResultWithImages(desc, []string{b64}), nil
	}

	content := string(contentBytes)
	lines := strings.Split(content, "\n")

	// Determine paging
	start := 0
	if off, ok := args["offset"]; ok {
		switch v := off.(type) {
		case float64:
			if v > 1 {
				start = int(v) - 1
			}
		case int:
			if v > 1 {
				start = v - 1
			}
		}
	}
	end := len(lines)
	if lim, ok := args["limit"]; ok {
		switch v := lim.(type) {
		case float64:
			if v > 0 && start+int(v) < end {
				end = start + int(v)
			}
		case int:
			if v > 0 && start+v < end {
				end = start + v
			}
		}
	}
	if start < 0 {
		start = 0
	}
	if start > len(lines) {
		start = len(lines)
	}
	if end < start {
		end = start
	}

	// Line-numbered output (cat -n style: spaces + line + tab + content)
	var b strings.Builder
	ln := start + 1
	for i := start; i < end; i++ {
		b.WriteString(fmt.Sprintf("%6d\t%s\n", ln, lines[i]))
		ln++
	}
	return message.NewToolResultText(b.String()), nil
}

// handleWrite implements Write
func (m *FileSystemToolManager) handleWrite(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	pathParam, ok := args["file_path"].(string)
	if !ok {
		return message.NewToolResultError("file_path parameter is required"), nil
	}
	if _, ok := args["content"].(string); !ok {
		return message.NewToolResultError("content parameter is required"), nil
	}
	return m.handleWriteFile(ctx, message.ToolArgumentValues{
		"path":    pathParam,
		"content": args["content"],
	})
}

// handleEdit maps to edit_file
func (m *FileSystemToolManager) handleEdit(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return m.handleEnhancedEdit(ctx, args)
}

// handleLS provides LS with ignore globs
func (m *FileSystemToolManager) handleLS(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	pathParam, ok := args["path"].(string)
	if !ok {
		return message.NewToolResultError("path parameter is required"), nil
	}

	path, resolveErr := m.resolvePath(pathParam)
	if resolveErr != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to resolve path: %v", resolveErr)), nil
	}
	if err := m.isPathAllowed(path); err != nil {
		return message.NewToolResultError(err.Error()), nil
	}

	// Parse ignore patterns
	var ignores []string
	if raw, ok := args["ignore"]; ok {
		if list, ok := raw.([]interface{}); ok {
			for _, v := range list {
				if s, ok := v.(string); ok {
					ignores = append(ignores, s)
				}
			}
		}
	}

	entries, err := m.fsRepo.ReadDir(ctx, path)
	if err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to read directory: %v", err)), nil
	}

	matchesIgnore := func(name string) bool {
		for _, pat := range ignores {
			if ok, _ := filepath.Match(pat, name); ok {
				return true
			}
		}
		return false
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Contents of %s:\n", path))
	for _, e := range entries {
		name := e.Name()
		if matchesIgnore(name) {
			continue
		}
		if e.IsDir() {
			b.WriteString(fmt.Sprintf("  %s/ (directory)\n", name))
		} else {
			b.WriteString(fmt.Sprintf("  %s (file)\n", name))
		}
	}
	return message.NewToolResultText(b.String()), nil
}

// handleMultiEdit processes a batch of exact string edits in one tool call
func (m *FileSystemToolManager) handleMultiEdit(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	editsArg, ok := args["edits"]
	if !ok {
		return message.NewToolResultError("edits parameter is required"), nil
	}

	type edit struct {
		FilePath   string
		OldString  string
		NewString  string
		ReplaceAll bool
	}

	var edits []edit
	switch v := editsArg.(type) {
	case string:
		// JSON string array
		// To avoid pulling in JSON here, expect structured args (slice) path; return error
		return message.NewToolResultError("edits must be an array, not a JSON string"), nil
	case []interface{}:
		for _, item := range v {
			mapp, ok := item.(map[string]interface{})
			if !ok {
				return message.NewToolResultError("each edit must be an object"), nil
			}
			e := edit{}
			if s, ok := mapp["file_path"].(string); ok {
				e.FilePath = s
			}
			if s, ok := mapp["old_string"].(string); ok {
				e.OldString = s
			}
			if s, ok := mapp["new_string"].(string); ok {
				e.NewString = s
			}
			if b, ok := mapp["replace_all"].(bool); ok {
				e.ReplaceAll = b
			}
			if e.FilePath == "" || e.OldString == "" || e.NewString == "" {
				return message.NewToolResultError("each edit requires file_path, old_string, and new_string"), nil
			}
			edits = append(edits, e)
		}
	default:
		return message.NewToolResultError("unsupported 'edits' parameter format"), nil
	}

	var results []string
	for idx, e := range edits {
		// Prepare arguments for the existing enhanced edit handler
		editArgs := message.ToolArgumentValues{
			"file_path":  e.FilePath,
			"old_string": e.OldString,
			"new_string": e.NewString,
		}
		if e.ReplaceAll {
			editArgs["replace_all"] = true
		}
		res, _ := m.handleEnhancedEdit(ctx, editArgs)
		if res.Error != "" {
			results = append(results, fmt.Sprintf("%d) %s: ERROR: %s", idx+1, e.FilePath, res.Error))
		} else {
			// Trim validation details to first line for compactness
			line := res.Text
			if nl := strings.IndexByte(line, '\n'); nl > 0 {
				line = line[:nl]
			}
			results = append(results, fmt.Sprintf("%d) %s: %s", idx+1, e.FilePath, line))
		}
	}

	return message.NewToolResultText("MultiEdit results:\n" + strings.Join(results, "\n")), nil
}

// fileSystemTool is a helper struct for filesystem tool registration
type fileSystemTool struct {
	name        message.ToolName
	description message.ToolDescription
	arguments   []message.ToolArgument
	handler     func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)
}

func (t *fileSystemTool) RawName() message.ToolName {
	return t.name
}

func (t *fileSystemTool) Name() message.ToolName {
	return t.name
}

func (t *fileSystemTool) Description() message.ToolDescription {
	return t.description
}

func (t *fileSystemTool) Arguments() []message.ToolArgument {
	return t.arguments
}

func (t *fileSystemTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return t.handler
}

// ValidationResult represents the result of a Go validation check
type ValidationResult struct {
	Check   string `json:"check"`
	Status  string `json:"status"` // "pass", "fail", "error"
	Output  string `json:"output,omitempty"`
	Summary string `json:"summary"`
}

// autoValidateFile performs automatic validation after write/edit operations based on file type
func (m *FileSystemToolManager) autoValidateFile(ctx context.Context, filePath string) string {
	// Get file extension
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".go":
		return m.autoValidateGoFile(ctx, filePath)
	default:
		// No validation available for this file type
		return ""
	}
}

// autoValidateGoFile performs automatic Go validation after write/edit operations
func (m *FileSystemToolManager) autoValidateGoFile(ctx context.Context, filePath string) string {
	// Find the directory containing go files
	dir := filepath.Dir(filePath)
	fileName := filepath.Base(filePath)

	// Check if this looks like a Go project (has .go files)
	hasGoFiles, err := m.hasGoFilesInDirectory(ctx, dir)
	if err != nil || !hasGoFiles {
		return ""
	}

	results := []ValidationResult{}

	// Run go vet on the specific file
	vetResult := m.runGoVet(ctx, dir, fileName)
	results = append(results, vetResult)

	// Run go build -n (dry run) on the specific file
	buildResult := m.runGoBuild(ctx, dir, fileName)
	results = append(results, buildResult)

	// Format validation results
	return m.formatValidationResults(results)
}

// hasGoFilesInDirectory checks if directory contains .go files
func (m *FileSystemToolManager) hasGoFilesInDirectory(ctx context.Context, dir string) (bool, error) {
	entries, err := m.fsRepo.ReadDir(ctx, dir)
	if err != nil {
		return false, err
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			return true, nil
		}
	}
	return false, nil
}

// runGoVet executes go vet and returns the result
func (m *FileSystemToolManager) runGoVet(ctx context.Context, dir string, fileName string) ValidationResult {
	result := ValidationResult{
		Check: "go vet - Static analysis to find suspicious constructs",
	}

	// Validate the specific file that was written/edited
	cmd := exec.CommandContext(ctx, "go", "vet", fileName)
	cmd.Dir = dir

	output, err := cmd.CombinedOutput()
	outputStr := strings.TrimSpace(string(output))

	if err != nil {
		if outputStr != "" {
			result.Status = "fail"
			result.Output = outputStr
			lines := strings.Split(outputStr, "\n")
			result.Summary = fmt.Sprintf("Found %d vet issues", len(lines))
		} else {
			result.Status = "error"
			result.Output = err.Error()
			result.Summary = fmt.Sprintf("Could not run go vet: %v", err)
		}
	} else {
		result.Status = "pass"
		result.Summary = "No vet issues found"
	}

	return result
}

// runGoBuild executes go build -n (dry run) and returns the result
func (m *FileSystemToolManager) runGoBuild(ctx context.Context, dir string, fileName string) ValidationResult {
	result := ValidationResult{
		Check: "go build -n - Check if code compiles without building",
	}

	// Validate the specific file that was written/edited
	cmd := exec.CommandContext(ctx, "go", "build", "-n", fileName)
	cmd.Dir = dir

	output, err := cmd.CombinedOutput()
	outputStr := strings.TrimSpace(string(output))

	if err != nil {
		result.Status = "fail"
		result.Output = outputStr
		result.Summary = "Build would fail - compilation errors found"
	} else {
		result.Status = "pass"
		result.Summary = "Code compiles successfully"
	}

	return result
}

// formatValidationResults formats validation results into a readable string
func (m *FileSystemToolManager) formatValidationResults(results []ValidationResult) string {
	if len(results) == 0 {
		return ""
	}

	var output strings.Builder
	output.WriteString("\n\nGo Validation Results:\n")

	passed := 0
	failed := 0

	for _, result := range results {
		switch result.Status {
		case "pass":
			output.WriteString(fmt.Sprintf("PASS: %s: %s\n", result.Check, result.Summary))
			passed++
		case "fail":
			output.WriteString(fmt.Sprintf("FAIL: %s: %s\n", result.Check, result.Summary))
			if result.Output != "" {
				// Limit output to prevent overwhelming response
				lines := strings.Split(result.Output, "\n")
				if len(lines) > 5 {
					output.WriteString(fmt.Sprintf("```\n%s\n... (%d more lines)\n```\n",
						strings.Join(lines[:5], "\n"), len(lines)-5))
				} else {
					output.WriteString(fmt.Sprintf("```\n%s\n```\n", result.Output))
				}
			}
			failed++
		case "error":
			output.WriteString(fmt.Sprintf("ERROR: %s: %s\n", result.Check, result.Summary))
		}
	}

	if failed == 0 {
		output.WriteString(fmt.Sprintf("\nAll %d validation checks passed.\n", passed))
	} else {
		output.WriteString(fmt.Sprintf("\nValidation Summary: %d passed, %d failed.\n", passed, failed))
	}

	return output.String()
}

// GetToolState implements domain.ToolStateProvider.
// Reports files with consecutive Edit failures so the situation message can
// advise the model to re-read before retrying, without inspecting raw error text.
func (m *FileSystemToolManager) GetToolState() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var parts []string
	for path, count := range m.editFailCounts {
		if count >= 1 {
			parts = append(parts, fmt.Sprintf("%s (Edit failed %d time(s) — re-read the file before retrying)", filepath.Base(path), count))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Edit failures requiring re-read: " + strings.Join(parts, ", ")
}

// Compile-time check that FileSystemToolManager implements ToolStateProvider.
var _ domain.ToolStateProvider = (*FileSystemToolManager)(nil)
