package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaybeOffload_SmallResult(t *testing.T) {
	dir := t.TempDir()
	s := NewToolResultStorage(dir)

	content := strings.Repeat("x", maxToolResultChars-1)
	got := s.MaybeOffload("call-1", "bash", content)

	if got != content {
		t.Errorf("expected content unchanged for small result, got different value")
	}
	// No file should have been written
	entries, _ := os.ReadDir(filepath.Join(dir, "tool_results"))
	if len(entries) != 0 {
		t.Errorf("expected no files written for small result, got %d", len(entries))
	}
}

func TestMaybeOffload_LargeResult(t *testing.T) {
	dir := t.TempDir()
	s := NewToolResultStorage(dir)

	content := strings.Repeat("y", maxToolResultChars+1)
	got := s.MaybeOffload("call-abc", "bash", content)

	if got == content {
		t.Error("expected content to be replaced with stub for large result")
	}
	if !strings.Contains(got, "[Result offloaded to disk:") {
		t.Errorf("expected stub header, got: %s", got[:min(200, len(got))])
	}
	if !strings.Contains(got, "Preview:") {
		t.Error("expected preview section in stub")
	}

	// File should exist on disk
	entries, _ := os.ReadDir(filepath.Join(dir, "tool_results"))
	if len(entries) != 1 {
		t.Errorf("expected 1 offloaded file, got %d", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(dir, "tool_results", entries[0].Name()))
	if err != nil {
		t.Fatalf("could not read offloaded file: %v", err)
	}
	if string(data) != content {
		t.Error("offloaded file content does not match original")
	}
}

func TestMaybeOffload_NeverOffloadTools(t *testing.T) {
	dir := t.TempDir()
	s := NewToolResultStorage(dir)

	content := strings.Repeat("z", maxToolResultChars+1)
	for toolName := range neverOffloadTools {
		got := s.MaybeOffload("call-x", toolName, content)
		if got != content {
			t.Errorf("tool %q should never be offloaded, but content was changed", toolName)
		}
	}
}

func TestMaybeOffload_NilStorage(t *testing.T) {
	var s *ToolResultStorage
	content := strings.Repeat("a", maxToolResultChars+1)
	got := s.MaybeOffload("call-1", "bash", content)
	if got != content {
		t.Error("nil ToolResultStorage should return content unchanged")
	}
}

func TestMaybeOffload_ExactBoundary(t *testing.T) {
	dir := t.TempDir()
	s := NewToolResultStorage(dir)

	// Exactly at limit — should NOT offload
	content := strings.Repeat("b", maxToolResultChars)
	got := s.MaybeOffload("call-boundary", "bash", content)
	if got != content {
		t.Error("content exactly at maxToolResultChars should not be offloaded")
	}
}

func TestSanitizeID(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"abc-123", "abc-123"},
		{"call_id/slash", "call_id_slash"},
		{"toolu_01", "toolu_01"},
		{"", "result"},
		{"../evil", "___evil"},
	}
	for _, c := range cases {
		got := sanitizeID(c.input)
		if got != c.want {
			t.Errorf("sanitizeID(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
