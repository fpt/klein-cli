package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

func TestMemoryWriteAppendAndGet(t *testing.T) {
	dir := t.TempDir()
	m := NewMemoryToolManager(dir)
	ctx := context.Background()

	// Append two entries to MEMORY.md (file does not exist yet).
	r1, _ := m.CallTool(ctx, "MemoryWrite", message.ToolArgumentValues{
		"path": "MEMORY.md", "content": "- Watching: MU, NVDA",
	})
	if r1.Error != "" {
		t.Fatalf("append 1 error: %s", r1.Error)
	}
	r2, _ := m.CallTool(ctx, "MemoryWrite", message.ToolArgumentValues{
		"path": "MEMORY.md", "content": "- Prefers Japanese",
	})
	if r2.Error != "" {
		t.Fatalf("append 2 error: %s", r2.Error)
	}

	got, _ := m.CallTool(ctx, "MemoryGet", message.ToolArgumentValues{"path": "MEMORY.md"})
	if !strings.Contains(got.Text, "MU, NVDA") || !strings.Contains(got.Text, "Prefers Japanese") {
		t.Errorf("round-trip failed, got:\n%s", got.Text)
	}
	// Each appended entry should be newline-terminated (two lines).
	if lines := strings.Count(strings.TrimRight(got.Text, "\n"), "\n"); lines != 1 {
		t.Errorf("expected 2 lines (1 separator), got %d:\n%q", lines+1, got.Text)
	}
}

func TestMemoryWriteOverwrite(t *testing.T) {
	dir := t.TempDir()
	m := NewMemoryToolManager(dir)
	ctx := context.Background()

	_, _ = m.CallTool(ctx, "MemoryWrite", message.ToolArgumentValues{"path": "MEMORY.md", "content": "old"})
	r, _ := m.CallTool(ctx, "MemoryWrite", message.ToolArgumentValues{
		"path": "MEMORY.md", "content": "new content", "mode": "overwrite",
	})
	if r.Error != "" {
		t.Fatalf("overwrite error: %s", r.Error)
	}
	got, _ := m.CallTool(ctx, "MemoryGet", message.ToolArgumentValues{"path": "MEMORY.md"})
	if got.Text != "new content" {
		t.Errorf("overwrite: got %q, want %q", got.Text, "new content")
	}
}

func TestMemoryWriteCreatesDailySubdir(t *testing.T) {
	dir := t.TempDir()
	m := NewMemoryToolManager(dir)
	r, _ := m.CallTool(context.Background(), "MemoryWrite", message.ToolArgumentValues{
		"path": "daily/2026-06-22.md", "content": "milestone",
	})
	if r.Error != "" {
		t.Fatalf("daily write error: %s", r.Error)
	}
	got, _ := m.CallTool(context.Background(), "MemoryGet", message.ToolArgumentValues{"path": "daily/2026-06-22.md"})
	if !strings.Contains(got.Text, "milestone") {
		t.Errorf("daily round-trip failed: %q", got.Text)
	}
}

func TestMemoryWriteRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	m := NewMemoryToolManager(dir)
	ctx := context.Background()

	if r, _ := m.CallTool(ctx, "MemoryWrite", message.ToolArgumentValues{"path": "../escape.md", "content": "x"}); r.Error == "" {
		t.Error("expected traversal path to be rejected")
	}
	if r, _ := m.CallTool(ctx, "MemoryWrite", message.ToolArgumentValues{"path": "MEMORY.md", "content": "x", "mode": "delete"}); r.Error == "" {
		t.Error("expected invalid mode to be rejected")
	}
	if r, _ := m.CallTool(ctx, "MemoryWrite", message.ToolArgumentValues{"path": "MEMORY.md"}); r.Error == "" {
		t.Error("expected missing content to be rejected")
	}
}
