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

// RunLogPath returns the path of the run log for the given date.
// Scheduled-run outputs are appended here (runs/YYYY-MM-DD.md, inside the
// memory directory) so that later jobs — e.g. a nightly memory-extraction
// cron — can read what earlier jobs produced via MemoryGet/MemorySearch.
func (m *MemoryManager) RunLogPath(t time.Time) string {
	return filepath.Join(m.config.BaseDir, "runs", t.Format("2006-01-02")+".md")
}

// AppendRunLog appends one scheduled run's output to today's run log. Entries
// are recorded for silent runs too — capturing output for later distillation
// is exactly what silent data-collection jobs are for.
func (m *MemoryManager) AppendRunLog(now time.Time, schedName, skill, response string) error {
	path := m.RunLogPath(now)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	entry := fmt.Sprintf("## %s %s (skill: %s)\n\n%s\n\n", now.Format("15:04"), schedName, skill, strings.TrimSpace(response))
	_, err = f.WriteString(entry)
	return err
}
