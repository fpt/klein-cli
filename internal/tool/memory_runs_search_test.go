package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

// TestMemorySearchScansRunLogs verifies MemorySearch covers runs/*.md so a
// nightly memory job can find what earlier scheduled jobs produced.
func TestMemorySearchScansRunLogs(t *testing.T) {
	dir := t.TempDir()
	runsDir := filepath.Join(dir, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runsDir, "2026-07-03.md"),
		[]byte("## 08:00 morning-market (skill: report)\n\n日経平均は68,733円で反発。\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewMemoryToolManager(dir)
	r, _ := m.CallTool(context.Background(), "MemorySearch", message.ToolArgumentValues{"query": "日経平均"})
	if r.Error != "" || !strings.Contains(r.Text, "runs/2026-07-03.md") {
		t.Errorf("MemorySearch should find run-log content: text=%q err=%q", r.Text, r.Error)
	}

	// MemoryGet reads the run log by relative path.
	g, _ := m.CallTool(context.Background(), "MemoryGet", message.ToolArgumentValues{"path": "runs/2026-07-03.md"})
	if g.Error != "" || !strings.Contains(g.Text, "morning-market") {
		t.Errorf("MemoryGet runs/ failed: text=%q err=%q", g.Text, g.Error)
	}
}
