package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fpt/klein-cli/internal/repository"
)

const pasteInterval = time.Millisecond * 10
const initialPasteWindow = time.Millisecond * 25
const minPasteBlockLen = 100 // only treat as paste if block >= 100 chars

// PromptBuilder is a minimal wrapper around user input with atmark file processing.
// Accumulates runes and provides visual highlights for @filename patterns,
// with smart backspace handling and file existence checking.
type PromptBuilder struct {
	buf        []rune
	times      []time.Time
	workingDir string
	fsRepo     repository.FilesystemRepository
	// usePaste controls whether paste detection/compression and
	// paste-block backspace behavior are active. When false, input is
	// treated purely as typed characters with no special paste handling.
	usePaste bool
	// cursorPos tracks the logical cursor position within the buffer
	cursorPos int
	// pasteSegments holds actual pasted content from bracketed paste mode.
	// Placeholders in the buffer (e.g. "[pasted 3 lines, 42 chars (#0)]")
	// are expanded with this content in RawPrompt().
	pasteSegments []string
}

// NewPromptBuilder creates a new PromptBuilder with the specified working directory and filesystem repository.
func NewPromptBuilder(fsRepo repository.FilesystemRepository, workingDir string) *PromptBuilder {
	if workingDir == "" {
		workingDir, _ = os.Getwd()
	}
	return &PromptBuilder{
		buf:        make([]rune, 0, 256),
		times:      make([]time.Time, 0, 256),
		workingDir: workingDir,
		fsRepo:     fsRepo,
		// Default: paste handling disabled unless explicitly enabled
		usePaste:   false,
		cursorPos:  0,
	}
}

// SetWorkingDir sets the working directory for file existence checks.
func (p *PromptBuilder) SetWorkingDir(dir string) {
	p.workingDir = dir
}

// SetUsePaste toggles paste detection/compression and paste-block backspace behavior.
func (p *PromptBuilder) SetUsePaste(use bool) {
	p.usePaste = use
}

// Input appends a single character to the prompt buffer at the end (legacy method).
func (p *PromptBuilder) Input(r rune) {
	p.InputAtCursor(r)
}

// InputAtCursor inserts a rune at the current cursor position.
func (p *PromptBuilder) InputAtCursor(r rune) {
	now := time.Now()
	
	// Ensure cursor position is valid
	if p.cursorPos < 0 {
		p.cursorPos = 0
	}
	if p.cursorPos > len(p.buf) {
		p.cursorPos = len(p.buf)
	}
	
	// Insert rune at cursor position
	p.buf = append(p.buf[:p.cursorPos], append([]rune{r}, p.buf[p.cursorPos:]...)...)
	p.times = append(p.times[:p.cursorPos], append([]time.Time{now}, p.times[p.cursorPos:]...)...)
	
	// Move cursor forward
	p.cursorPos++
}




// SyncFromReadline updates the PromptBuilder's buffer and cursor from readline's state.
// This is the authoritative way to keep PromptBuilder in sync with readline.
func (p *PromptBuilder) SyncFromReadline(line []rune, pos int) {
	// Update buffer from readline's line
	p.buf = make([]rune, len(line))
	copy(p.buf, line)
	
	// Update times array to match buffer length
	now := time.Now()
	if len(p.times) != len(line) {
		p.times = make([]time.Time, len(line))
		for i := range p.times {
			p.times[i] = now
		}
	}
	
	// Update cursor position
	if pos < 0 {
		pos = 0
	}
	if pos > len(p.buf) {
		pos = len(p.buf)
	}
	p.cursorPos = pos
}

// VisiblePrompt returns the string to show in the UI.
func (p *PromptBuilder) VisiblePrompt() string {
	if len(p.buf) == 0 {
		return ""
	}
	out := make([]rune, 0, len(p.buf))
	if !p.usePaste {
		// No paste compression; just sanitize newlines for display
		for _, r := range p.buf {
			if r == '\n' || r == '\r' {
				r = ' '
			}
			out = append(out, r)
		}
	} else {
		// Replace fast-typed (pasted) contiguous regions with a short placeholder.
		i := 0
		for i < len(p.buf) {
			if i > 0 && p.times[i].Sub(p.times[i-1]) < pasteInterval {
				// find left boundary
				start := i - 1
				for start > 0 && p.times[start].Sub(p.times[start-1]) < pasteInterval {
					start--
				}
				// find right boundary
				end := i
				for end+1 < len(p.buf) && p.times[end+1].Sub(p.times[end]) < pasteInterval {
					end++
				}
				// If paste burst started at the very beginning within a small window,
				// include the first character as part of the paste block.
				if start == 1 && p.times[end].Sub(p.times[0]) <= initialPasteWindow {
					start = 0
				}

				n := end - start + 1
				if n >= minPasteBlockLen {
					// Build preview (first 20 runes)
					run := p.buf[start : end+1]
					previewLen := 20
					if n < previewLen {
						previewLen = n
					}
					preview := string(run[:previewLen])
					ellipsis := ""
					if n > previewLen {
						ellipsis = "…"
					}
					placeholder := []rune(fmt.Sprintf("[pasted: '%s%s', %d chars]", preview, ellipsis, n))
					out = append(out, placeholder...)
					i = end + 1
					continue
				}
				// If below threshold, do not compress; fall through to append current rune normally.
			}
			r := p.buf[i]
			if r == '\n' || r == '\r' {
				r = ' '
			}
			out = append(out, r)
			i++
		}
	}

	// Apply @filename highlighting to the compressed output
	result := string(out)
	return p.highlightAtmarkFiles(result)
}

// highlightAtmarkFiles adds visual indicators for @filename patterns based on file existence
func (p *PromptBuilder) highlightAtmarkFiles(input string) string {
	// Pattern to match @filename at word boundaries
	re := regexp.MustCompile(`@([\w\-\./]+)`)

	return re.ReplaceAllStringFunc(input, func(match string) string {
		// Extract filename (remove @ prefix)
		filename := match[1:]

		// Check if file exists (relative to working directory)
		fullPath := filepath.Join(p.workingDir, filename)
		if _, err := p.fsRepo.Stat(context.Background(), fullPath); err == nil {
			// File exists - color it cyan
			return fmt.Sprintf("\033[36m@%s\033[0m", filename)
		}
		// File doesn't exist - return as is
		return match
	})
}

// SetPasteSegments attaches paste segment content from bracketed paste.
func (p *PromptBuilder) SetPasteSegments(segments []string) {
	p.pasteSegments = segments
}

// PasteSegments returns the current paste segments.
func (p *PromptBuilder) PasteSegments() []string {
	return p.pasteSegments
}

// RawPrompt returns the string to send to the model/tools with @filename content embedded
// and paste placeholders expanded with actual pasted content.
func (p *PromptBuilder) RawPrompt() string {
	raw := string(p.buf)

	// Expand paste placeholders with actual content
	if len(p.pasteSegments) > 0 {
		raw = p.expandPasteSegments(raw)
	}

	return p.embedFileContent(raw)
}

// expandPasteSegments replaces [pasted ... (#N)] placeholders with actual content.
func (p *PromptBuilder) expandPasteSegments(input string) string {
	for i, segment := range p.pasteSegments {
		placeholder := fmt.Sprintf("[pasted %%d lines, %%d chars (#%d)]", i)
		// Find the actual placeholder by matching the (#N) suffix
		// since line/char counts are already in the placeholder
		prefix := "[pasted "
		suffix := fmt.Sprintf("(#%d)]", i)
		idx := strings.Index(input, suffix)
		if idx < 0 {
			continue
		}
		// Find the start of this placeholder
		start := strings.LastIndex(input[:idx], prefix)
		if start < 0 {
			continue
		}
		_ = placeholder // used for documentation
		fullPlaceholder := input[start : idx+len(suffix)]
		input = strings.Replace(input, fullPlaceholder, segment, 1)
	}
	return input
}

// embedFileContent replaces @filename patterns with actual file content
func (p *PromptBuilder) embedFileContent(input string) string {
	// Pattern to match @filename at word boundaries
	re := regexp.MustCompile(`@([\w\-\./]+)`)

	return re.ReplaceAllStringFunc(input, func(match string) string {
		// Extract filename (remove @ prefix)
		filename := match[1:]

		// Check if file exists and read content
		fullPath := filepath.Join(p.workingDir, filename)
		if content, err := p.readFileContent(fullPath); err == nil {
			return fmt.Sprintf("\n\nFile: %s\n\n%s\n\n", filename, content)
		}

		// File doesn't exist or can't be read - return as is with note
		return fmt.Sprintf("%s (file not found)", match)
	})
}

// readFileContent reads file content with size limits for safety
func (p *PromptBuilder) readFileContent(filePath string) (string, error) {
	// Read full file using repository
	content, err := p.fsRepo.ReadFile(context.Background(), filePath)
	if err != nil {
		return "", err
	}

	// Limit file size to prevent huge files from being embedded (1MB limit)
	const maxFileSize = 1024 * 1024
	if len(content) > maxFileSize {
		content = content[:maxFileSize]
	}

	return string(content), nil
}

// IsSlashCommand reports whether the raw buffer (trimmed) starts with '/'.
// Uses the unmodified buffer so detection is not affected by paste-compression
// used in VisiblePrompt().
func (p *PromptBuilder) IsSlashCommand() bool {
	s := strings.TrimSpace(string(p.buf))
	return strings.HasPrefix(s, "/")
}

// SlashInput returns the trimmed raw buffer when it represents a slash command.
// Returns an empty string if it's not a slash command.
func (p *PromptBuilder) SlashInput() string {
	s := strings.TrimSpace(string(p.buf))
	if strings.HasPrefix(s, "/") {
		return s
	}
	return ""
}

// Backspace removes the rune before cursor position, with smart @filename removal.
func (p *PromptBuilder) Backspace() {
	n := len(p.buf)
	if n == 0 || p.cursorPos == 0 {
		return
	}

	// For backward compatibility, if cursor is at the end, use old logic
	if p.cursorPos == n {
		// Check if we're at the end of an @filename pattern and remove the entire block
		if p.isAtEndOfAtmarkPattern() {
			p.removeLastAtmarkPattern()
			// Update cursor position after pattern removal
			p.cursorPos = len(p.buf)
			return
		}

		if p.usePaste {
			// If the last two runes were entered within pasteInterval, treat the
			// trailing contiguous fast-typed region as a single unit and delete it.
			if n >= 2 && p.times[n-1].Sub(p.times[n-2]) < pasteInterval {
				// Find left boundary (matching VisiblePrompt compression rule)
				start := n - 2
				for start > 0 && p.times[start].Sub(p.times[start-1]) < pasteInterval {
					start--
				}
				// If the burst began near the start and stayed within the initial window, include first char.
				if start == 1 && p.times[n-1].Sub(p.times[0]) <= initialPasteWindow {
					start = 0
				}
				blockLen := n - start
				if blockLen >= minPasteBlockLen {
					// Delete [start..n-1]
					p.buf = append(p.buf[:start], p.buf[n:]...)
					p.times = append(p.times[:start], p.times[n:]...)
					p.cursorPos = start
					return
				}
			}
		}
	}

	// Delete the rune before cursor position
	deletePos := p.cursorPos - 1
	p.buf = append(p.buf[:deletePos], p.buf[p.cursorPos:]...)
	if len(p.times) > deletePos {
		p.times = append(p.times[:deletePos], p.times[p.cursorPos:]...)
	}
	p.cursorPos--
}


// isAtEndOfAtmarkPattern checks if the cursor is at the end of an @filename pattern
func (p *PromptBuilder) isAtEndOfAtmarkPattern() bool {
	text := string(p.buf)
	if len(text) == 0 {
		return false
	}

	// Look for @filename pattern ending at the current position
	re := regexp.MustCompile(`@([\w\-\./]+)$`)
	return re.MatchString(text)
}

// removeLastAtmarkPattern removes the last @filename pattern from the buffer
func (p *PromptBuilder) removeLastAtmarkPattern() {
	text := string(p.buf)
	re := regexp.MustCompile(`@([\w\-\./]+)$`)

	match := re.FindStringSubmatch(text)
	if len(match) > 0 {
		// Remove the entire @filename pattern
		fullMatch := match[0]
		removeLen := len([]rune(fullMatch))

		// Remove from buffer and times
		newLen := len(p.buf) - removeLen
		if newLen >= 0 {
			p.buf = p.buf[:newLen]
			if len(p.times) > removeLen {
				p.times = p.times[:len(p.times)-removeLen]
			} else {
				p.times = p.times[:0]
			}
		}
	}
}

// Clear resets the buffer and paste segments.
func (p *PromptBuilder) Clear() {
	p.buf = p.buf[:0]
	p.times = p.times[:0]
	p.cursorPos = 0
	p.pasteSegments = nil
}

// Len returns the current rune length.
func (p *PromptBuilder) Len() int { return len(p.buf) }
