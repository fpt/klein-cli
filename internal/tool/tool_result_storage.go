package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	// DefaultMaxToolResultRunes is the default budget, in runes, for a single
	// tool result kept inline in the conversation history. Results larger than
	// this are written to disk and replaced with a stub + preview so the LLM
	// still has enough context without burning the entire context window on a
	// single result.
	//
	// The budget is counted in runes, not bytes: a byte budget shrinks by ~3x
	// for CJK text, which offloaded ordinary Japanese documents that fit
	// comfortably in context.
	DefaultMaxToolResultRunes = 16_000

	// previewRunes is the number of runes kept inline even when a result is
	// offloaded to disk. Gives the model enough context to understand what the
	// tool returned without reading the full file.
	previewRunes = 500
)

// neverOffloadTools lists tool names whose results must always stay inline.
// These results are either small by construction or semantically important
// (e.g. user answers, todo state) and must not be replaced with file stubs.
var neverOffloadTools = map[string]bool{
	"AskUserQuestion": true,
	"TodoRead":        true,
	"TodoWrite":       true,
	"TaskCreate":      true,
	"TaskUpdate":      true,
	"TaskList":        true,
	"TaskGet":         true,
}

// ToolResultStorage manages offloading of large tool results to disk.
// When a result exceeds maxRunes, its content is written to a file under
// storageDir and the in-conversation text is replaced with a concise stub that
// includes a path, total size, and a short preview.
//
// storageDir must be on the filesystem tool manager's allowlist, or the model
// cannot act on the path the stub hands it (see app.computeToolResultsDir).
//
// Using a nil ToolResultStorage (or calling MaybeOffload on nil) is safe —
// the original content is returned unchanged. This allows the storage to be
// omitted in one-shot / non-persistent sessions without special-casing.
type ToolResultStorage struct {
	storageDir string // absolute path; created on first use
	maxRunes   int    // inline budget in runes; <= 0 means DefaultMaxToolResultRunes
}

// NewToolResultStorage creates a ToolResultStorage that persists offloaded
// results under storageDir. The directory is created lazily on first write.
// A maxRunes of 0 or less selects DefaultMaxToolResultRunes.
func NewToolResultStorage(storageDir string, maxRunes int) *ToolResultStorage {
	if maxRunes <= 0 {
		maxRunes = DefaultMaxToolResultRunes
	}
	return &ToolResultStorage{storageDir: storageDir, maxRunes: maxRunes}
}

// MaybeOffload inspects content and, if it exceeds the budget, writes it to
// disk and returns a stub string. Otherwise the original content is returned
// unchanged.
//
// toolUseID is the unique call identifier (used as the filename).
// toolName is checked against neverOffloadTools.
func (s *ToolResultStorage) MaybeOffload(toolUseID, toolName, content string) string {
	if s == nil {
		return content
	}
	totalRunes := utf8.RuneCountInString(content)
	if totalRunes <= s.maxRunes {
		return content
	}
	if neverOffloadTools[toolName] {
		return content
	}

	// Persist to disk
	if err := os.MkdirAll(s.storageDir, 0o700); err != nil {
		// If we can't create the directory, return content unchanged.
		return content
	}

	filename := sanitizeID(toolUseID) + ".txt"
	path := filepath.Join(s.storageDir, filename)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return content
	}

	// Build inline stub. Cut on a rune boundary: a byte cut can split a
	// multi-byte rune and put invalid UTF-8 on the wire to the model.
	preview := truncateRunes(content, previewRunes)
	// Trim to a clean line boundary so the preview doesn't cut mid-word
	if idx := strings.LastIndexByte(preview, '\n'); idx > len(preview)/2 {
		preview = preview[:idx]
	}

	return fmt.Sprintf(
		"[Result offloaded to disk: %s (%d chars total)]\n"+
			"Read that path to see the rest — page it with Read's offset/limit, "+
			"or re-run the original tool on a narrower range.\nPreview:\n%s\n...",
		path, totalRunes, preview,
	)
}

// truncateRunes returns the first n runes of s, never splitting a rune.
func truncateRunes(s string, n int) string {
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
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
