package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MemoryConfig holds memory system configuration.
type MemoryConfig struct {
	BaseDir  string `json:"base_dir"`
	MaxNotes int    `json:"max_notes"`
}

// MemoryManager manages MEMORY.md and daily notes.
type MemoryManager struct {
	config MemoryConfig
}

// NewMemoryManager creates a new memory manager.
func NewMemoryManager(cfg MemoryConfig) *MemoryManager {
	if cfg.MaxNotes <= 0 {
		cfg.MaxNotes = 30
	}
	return &MemoryManager{config: cfg}
}

// MemoryPath returns the path to MEMORY.md.
func (m *MemoryManager) MemoryPath() string {
	return filepath.Join(m.config.BaseDir, "MEMORY.md")
}

// TodayNotePath returns the path to today's daily note.
func (m *MemoryManager) TodayNotePath() string {
	date := time.Now().Format("2006-01-02")
	return filepath.Join(m.config.BaseDir, "daily", date+".md")
}

// GetMemoryContext returns the current MEMORY.md content for prompt injection.
func (m *MemoryManager) GetMemoryContext() (string, error) {
	data, err := os.ReadFile(m.MemoryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// GetRecentDailyNotes returns the last N daily notes.
func (m *MemoryManager) GetRecentDailyNotes(n int) ([]string, error) {
	dir := filepath.Join(m.config.BaseDir, "daily")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	// Sort by name (date-formatted, so lexicographic is chronological)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	start := len(entries) - n
	if start < 0 {
		start = 0
	}

	var notes []string
	for _, e := range entries[start:] {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		notes = append(notes, fmt.Sprintf("## %s\n%s", strings.TrimSuffix(e.Name(), ".md"), string(data)))
	}
	return notes, nil
}

// BuildMemoryPrompt constructs a memory context block for injection into the user prompt.
func (m *MemoryManager) BuildMemoryPrompt() string {
	memory, err := m.GetMemoryContext()
	if err != nil || memory == "" {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("[MEMORY CONTEXT]\n")
	sb.WriteString(memory)

	notes, err := m.GetRecentDailyNotes(3)
	if err == nil && len(notes) > 0 {
		sb.WriteString("\n\n## Recent Daily Notes\n")
		sb.WriteString(strings.Join(notes, "\n\n"))
	}

	sb.WriteString("\n[END MEMORY CONTEXT]\n\n")
	return sb.String()
}

// EnsureDirectories creates the memory base directory and daily subdirectory if needed.
func (m *MemoryManager) EnsureDirectories() error {
	return os.MkdirAll(filepath.Join(m.config.BaseDir, "daily"), 0o755)
}
