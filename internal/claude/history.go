package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fpt/klein-cli/pkg/message"
)

// EncodePath converts an absolute path to the Claude Code project directory
// encoding: leading slash dropped and all slashes replaced with hyphens.
// e.g. /Users/foo/bar → -Users-foo-bar
func EncodePath(absPath string) string {
	return strings.ReplaceAll(absPath, "/", "-")
}

// ClaudeProjectDir returns ~/.claude/projects/<encoded-path> for the given
// working directory. Returns an error if the home directory cannot be resolved
// or the resulting directory does not exist.
func ClaudeProjectDir(workingDir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	abs, err := filepath.Abs(workingDir)
	if err != nil {
		return "", fmt.Errorf("cannot resolve working directory: %w", err)
	}
	dir := filepath.Join(home, ".claude", "projects", EncodePath(abs))
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("Claude project directory not found: %s", dir)
	}
	return dir, nil
}

// FindLatestSession returns the path to the most-recently modified *.jsonl file
// inside the Claude project directory for workingDir, or an empty string when
// none exist. A non-nil error is returned only for unexpected I/O failures.
func FindLatestSession(workingDir string) (string, error) {
	projectDir, err := ClaudeProjectDir(workingDir)
	if err != nil {
		return "", nil // directory absent → no history to offer
	}

	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return "", fmt.Errorf("cannot read Claude project directory: %w", err)
	}

	type candidate struct {
		path    string
		modTime int64
	}
	var candidates []candidate
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{
			path:    filepath.Join(projectDir, e.Name()),
			modTime: info.ModTime().Unix(),
		})
	}
	if len(candidates) == 0 {
		return "", nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime > candidates[j].modTime
	})
	return candidates[0].path, nil
}

// jsonlRecord is the minimal shape we need to parse from each JSONL line.
type jsonlRecord struct {
	Type       string `json:"type"`
	IsSidechain bool  `json:"isSidechain"`
	IsMeta     bool   `json:"isMeta"`
	Message    struct {
		Role    string            `json:"role"`
		Content json.RawMessage   `json:"content"`
	} `json:"message"`
}

// contentBlock represents one element of message.content when it is an array.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// extractText returns the plain-text content from a message.content field
// which may be either a plain string or an array of content blocks.
// Only "text" blocks are included; tool_use, tool_result, thinking, image
// blocks are discarded.
func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try plain string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	// Try array of content blocks.
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, strings.TrimSpace(b.Text))
		}
	}
	return strings.Join(parts, "\n")
}

// ImportMessages reads a Claude Code *.jsonl session file and returns the
// conversation as klein message.Message values. Only user and assistant turns
// with extractable text are included; sidechain, meta, and non-text records
// are skipped. The messages arrive in file order (oldest first).
func ImportMessages(jsonlPath string) ([]message.Message, error) {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return nil, fmt.Errorf("cannot open session file: %w", err)
	}
	defer f.Close()

	var msgs []message.Message
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024) // 4 MB line buffer
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec jsonlRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // skip malformed lines
		}
		if rec.IsSidechain || rec.IsMeta {
			continue
		}
		if rec.Type != "user" && rec.Type != "assistant" {
			continue
		}

		text := extractText(rec.Message.Content)
		if text == "" {
			continue
		}

		var msgType message.MessageType
		switch rec.Type {
		case "user":
			msgType = message.MessageTypeUser
		case "assistant":
			msgType = message.MessageTypeAssistant
		default:
			continue
		}
		msgs = append(msgs, message.NewChatMessage(msgType, text))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading session file: %w", err)
	}
	return msgs, nil
}

// FindContextFile looks for AGENTS.md then CLAUDE.md in workingDir and returns
// the content of the first one found. Returns "", nil when neither exists.
func FindContextFile(workingDir string) (string, error) {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		path := filepath.Join(workingDir, name)
		data, err := os.ReadFile(path)
		if err == nil {
			return string(data), nil
		}
	}
	return "", nil
}
