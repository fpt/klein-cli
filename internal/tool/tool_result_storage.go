package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// maxToolResultChars is the maximum number of characters a tool result may
	// occupy inline in the conversation history. Results larger than this are
	// written to disk and replaced with a stub + preview so the LLM still has
	// enough context without burning the entire context window on a single result.
	maxToolResultChars = 8_000

	// previewChars is the number of characters kept inline even when a result
	// is offloaded to disk. Gives the model enough context to understand what
	// the tool returned without reading the full file.
	previewChars = 500
)

// neverOffloadTools lists tool names whose results must always stay inline.
// These results are either small by construction or semantically important
// (e.g. user answers, todo state) and must not be replaced with file stubs.
var neverOffloadTools = map[string]bool{
	"ask_user_question": true,
	"todo_read":         true,
	"todo_write":        true,
	"task_create":       true,
	"task_update":       true,
	"task_list":         true,
	"task_get":          true,
}

// ToolResultStorage manages offloading of large tool results to disk.
// When a result exceeds maxToolResultChars, its content is written to a
// file under storageDir and the in-conversation text is replaced with a
// concise stub that includes a path, total size, and a short preview.
//
// Using a nil ToolResultStorage (or calling MaybeOffload on nil) is safe —
// the original content is returned unchanged. This allows the storage to be
// omitted in one-shot / non-persistent sessions without special-casing.
type ToolResultStorage struct {
	storageDir string // absolute path; created on first use
}

// NewToolResultStorage creates a ToolResultStorage that persists offloaded
// results under storageDir/tool_results/. The directory is created lazily on
// first write.
func NewToolResultStorage(storageDir string) *ToolResultStorage {
	return &ToolResultStorage{storageDir: storageDir}
}

// MaybeOffload inspects content and, if it exceeds the budget, writes it to
// disk and returns a stub string. Otherwise the original content is returned
// unchanged.
//
// toolUseID is the unique call identifier (used as the filename).
// toolName is checked against neverOffloadTools.
func (s *ToolResultStorage) MaybeOffload(toolUseID, toolName, content string) string {
	if s == nil || len(content) <= maxToolResultChars {
		return content
	}
	if neverOffloadTools[toolName] {
		return content
	}

	// Persist to disk
	dir := filepath.Join(s.storageDir, "tool_results")
	if err := os.MkdirAll(dir, 0700); err != nil {
		// If we can't create the directory, return content unchanged.
		return content
	}

	filename := sanitizeID(toolUseID) + ".txt"
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return content
	}

	// Build inline stub
	preview := content
	if len(preview) > previewChars {
		preview = preview[:previewChars]
	}
	// Trim to a clean line boundary so the preview doesn't cut mid-word
	if idx := strings.LastIndexByte(preview, '\n'); idx > previewChars/2 {
		preview = preview[:idx]
	}

	return fmt.Sprintf(
		"[Result offloaded to disk: %s (%d chars total)]\nPreview:\n%s\n...",
		path, len(content), preview,
	)
}

// sanitizeID replaces characters that are unsafe in filenames with underscores.
func sanitizeID(id string) string {
	var b strings.Builder
	b.Grow(len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "result"
	}
	return b.String()
}
