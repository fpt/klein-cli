package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMaybeOffload_SmallResult(t *testing.T) {
	dir := t.TempDir()
	s := NewToolResultStorage(dir, DefaultMaxToolResultRunes)

	content := strings.Repeat("x", DefaultMaxToolResultRunes-1)
	got := s.MaybeOffload("call-1", "bash", content)

	if got != content {
		t.Errorf("expected content unchanged for small result, got different value")
	}
	// No file should have been written
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected no files written for small result, got %d", len(entries))
	}
}

func TestMaybeOffload_LargeResult(t *testing.T) {
	dir := t.TempDir()
	s := NewToolResultStorage(dir, DefaultMaxToolResultRunes)

	content := strings.Repeat("y", DefaultMaxToolResultRunes+1)
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
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 offloaded file, got %d", len(entries))
	}
	// The stub must name the file it wrote, or the model cannot read it back.
	path := filepath.Join(dir, entries[0].Name())
	if !strings.Contains(got, path) {
		t.Errorf("stub does not contain the offloaded path %q: %s", path, got[:min(200, len(got))])
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read offloaded file: %v", err)
	}
	if string(data) != content {
		t.Error("offloaded file content does not match original")
	}
}

func TestMaybeOffload_NeverOffloadTools(t *testing.T) {
	dir := t.TempDir()
	s := NewToolResultStorage(dir, DefaultMaxToolResultRunes)

	content := strings.Repeat("z", DefaultMaxToolResultRunes+1)
	for toolName := range neverOffloadTools {
		got := s.MaybeOffload("call-x", toolName, content)
		if got != content {
			t.Errorf("tool %q should never be offloaded, but content was changed", toolName)
		}
	}
}

func TestMaybeOffload_NilStorage(t *testing.T) {
	var s *ToolResultStorage
	content := strings.Repeat("a", DefaultMaxToolResultRunes+1)
	got := s.MaybeOffload("call-1", "bash", content)
	if got != content {
		t.Error("nil ToolResultStorage should return content unchanged")
	}
}

func TestMaybeOffload_ExactBoundary(t *testing.T) {
	dir := t.TempDir()
	s := NewToolResultStorage(dir, DefaultMaxToolResultRunes)

	// Exactly at limit — should NOT offload
	content := strings.Repeat("b", DefaultMaxToolResultRunes)
	got := s.MaybeOffload("call-boundary", "bash", content)
	if got != content {
		t.Error("content exactly at the budget should not be offloaded")
	}
}

func TestNewToolResultStorage_DefaultsAndOverride(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// 0 and negative select the default budget.
	for _, maxRunes := range []int{0, -1} {
		s := NewToolResultStorage(dir, maxRunes)
		if s.maxRunes != DefaultMaxToolResultRunes {
			t.Errorf("maxRunes=%d: got budget %d, want default %d", maxRunes, s.maxRunes, DefaultMaxToolResultRunes)
		}
	}

	// An explicit budget is honored.
	s := NewToolResultStorage(dir, 10)
	if got := s.MaybeOffload("call-small-budget", "bash", strings.Repeat("c", 11)); got == strings.Repeat("c", 11) {
		t.Error("expected offload with a 10-rune budget")
	}
}

// The budget is counted in runes, not bytes: 8k bytes of Japanese is only ~2.9k
// characters, which used to offload ordinary documents that fit fine inline.
func TestMaybeOffload_BudgetCountsRunes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewToolResultStorage(dir, DefaultMaxToolResultRunes)

	// Under the rune budget, but far over it when counted in bytes.
	content := strings.Repeat("供給側EMS協業戦略について調査した。\n", 400)
	if utf8.RuneCountInString(content) > DefaultMaxToolResultRunes {
		t.Fatalf("test fixture too long: %d runes", utf8.RuneCountInString(content))
	}
	if len(content) <= DefaultMaxToolResultRunes {
		t.Fatalf("test fixture too short in bytes (%d) to prove the distinction", len(content))
	}

	if got := s.MaybeOffload("call-jp", "Read", content); got != content {
		t.Errorf("Japanese content within the rune budget should stay inline, got: %.120s", got)
	}
}

// A byte-sliced preview can split a multi-byte rune and put invalid UTF-8 on the
// wire to the model.
func TestMaybeOffload_PreviewIsValidUTF8(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewToolResultStorage(dir, 100)

	// One long line: no newline for the line-boundary trim to fall back on.
	content := strings.Repeat("供給側EMS協業戦略について調査した結果をここに記載する。", 100)
	got := s.MaybeOffload("call-jp-preview", "bash", content)
	if got == content {
		t.Fatal("expected offload")
	}
	if !utf8.ValidString(got) {
		t.Error("stub contains invalid UTF-8: preview was cut mid-rune")
	}
}

func TestTruncateRunes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
		n    int
	}{
		{"abcdef", "abc", 3},
		{"xyz", "xyz", 10},
		{"", "", 3},
		{"日本語テキスト", "日本語", 3},
		{"日本語", "", 0},
	}
	for _, c := range cases {
		if got := truncateRunes(c.in, c.n); got != c.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
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
