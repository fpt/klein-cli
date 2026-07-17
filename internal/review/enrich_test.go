package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/internal/infra"
)

func mustContain(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q:\n%s", w, out)
		}
	}
}

func mustNotContain(t *testing.T, out string, absents ...string) {
	t.Helper()
	for _, a := range absents {
		if strings.Contains(out, a) {
			t.Errorf("output unexpectedly contains %q:\n%s", a, out)
		}
	}
}

func TestEnricherRender(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// New-side file content: 40 numbered lines with the hunk's change applied
	// (line 11 is "line 11", line 12 is "line 12").
	var lines []string
	for i := 1; i <= 40; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte(strings.Join(lines, "\n"))
	if err := os.WriteFile(filepath.Join(dir, "internal", "foo.go"), content, 0o600); err != nil {
		t.Fatal(err)
	}

	diff := `--- a/internal/foo.go
+++ b/internal/foo.go
@@ -10,2 +10,3 @@
 line 10
-old line
+line 11
+line 12
`
	files := mustParse(t, diff)
	ranges := CommentableRanges(files)

	e := NewEnricher(infra.NewOSFilesystemRepository(), dir, 3)
	out := e.Render(context.Background(), files, ranges)

	// Commentable header, ±3 context, bracketed diff lines, unnumbered removal.
	mustContain(t, out,
		"Commentable lines (inline comments MUST target these): 10-12",
		"7  ctx| line 7", "9  ctx| line 9",
		"[   10]    | line 10", "[   11] +  | line 11", "[   12] +  | line 12",
		"-  | old line",
		"13  ctx| line 13", "15  ctx| line 15",
	)
	// No context beyond ±3.
	mustNotContain(t, out, "ctx| line 6", "ctx| line 16")
}

func TestEnricherRender_ByteBudget(t *testing.T) {
	t.Parallel()
	bigDiff := func(path string, n int) string {
		var sb strings.Builder
		fmt.Fprintf(&sb, "--- a/%s\n+++ b/%s\n@@ -0,0 +1,%d @@\n", path, path, n)
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&sb, "+line %d in %s\n", i, path)
		}
		return sb.String()
	}
	diff := bigDiff("a.txt", 200) + bigDiff("b.txt", 200) // each ~5 KB rendered
	files := mustParse(t, diff)
	ranges := CommentableRanges(files)

	// Empty workdir → no enrichment; the hunk bodies alone blow a 1500-byte
	// budget, so the render is cut at a line boundary with a visible marker.
	// Marker space is reserved, so the total stays within the budget.
	out := NewEnricher(infra.NewOSFilesystemRepository(), t.TempDir(), 0).
		WithMaxBytes(1500).Render(context.Background(), files, ranges)
	if len(out) > 1500 {
		t.Errorf("rendered %d bytes, expected within the 1500 budget", len(out))
	}
	if !strings.HasSuffix(out, truncationMarker) {
		t.Errorf("truncated output should end with the marker, got tail %q", out[max(0, len(out)-80):])
	}
	mustNotContain(t, out, "line 200 in b.txt")

	// Unbounded render includes everything.
	full := NewEnricher(infra.NewOSFilesystemRepository(), t.TempDir(), 0).
		Render(context.Background(), files, ranges)
	mustContain(t, full, "line 200 in b.txt")
	mustNotContain(t, full, "diff truncated")

	// Budget smaller than the first line: no line boundary fits, so no partial
	// line is emitted — only the marker.
	tiny := NewEnricher(infra.NewOSFilesystemRepository(), t.TempDir(), 0).
		WithMaxBytes(5).Render(context.Background(), files, ranges)
	if tiny != truncationMarker {
		t.Errorf("tiny budget should yield only the marker, got %q", tiny)
	}
}

func TestTruncateAtLine(t *testing.T) {
	t.Parallel()
	s := "aaa\nbbb\nccc\n"
	if got := truncateAtLine(s, 100); got != s {
		t.Errorf("under budget: got %q", got)
	}
	if got := truncateAtLine(s, 6); got != "aaa\n" {
		t.Errorf("cut at line boundary: got %q", got)
	}
	if got := truncateAtLine("verylongsingleline\n", 5); got != "" {
		t.Errorf("no newline within budget should yield empty, got %q", got)
	}
}

func TestEnricherRender_UnreadableFile(t *testing.T) {
	t.Parallel()
	diff := `--- a/missing.txt
+++ b/missing.txt
@@ -1 +1 @@
-a
+b
`
	files := mustParse(t, diff)
	e := NewEnricher(infra.NewOSFilesystemRepository(), t.TempDir(), 10)
	out := e.Render(context.Background(), files, CommentableRanges(files))
	// Falls back to hunk-only rendering, still numbered, no context lines.
	mustContain(t, out, "[    1] +  | b")
	mustNotContain(t, out, "ctx|")
}

func TestEnricherRender_PathTraversalBlocked(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A secret outside the "checkout" subdirectory must never be read.
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("TOP-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(dir, "repo")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}

	diff := `--- a/../secret.txt
+++ b/../secret.txt
@@ -1 +1 @@
-a
+b
`
	files := mustParse(t, diff)
	e := NewEnricher(infra.NewOSFilesystemRepository(), checkout, 10)
	out := e.Render(context.Background(), files, CommentableRanges(files))
	mustNotContain(t, out, "TOP-SECRET")
}

func TestEnricherRender_SymlinkTraversalBlocked(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("TOP-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(dir, "repo")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	// A checkout-internal symlink pointing outside: "escape" -> parent dir.
	// The lexical path "escape/secret.txt" passes IsLocal but must be blocked
	// after symlink resolution.
	if err := os.Symlink(dir, filepath.Join(checkout, "escape")); err != nil {
		t.Fatal(err)
	}

	diff := `--- a/escape/secret.txt
+++ b/escape/secret.txt
@@ -1 +1 @@
-a
+b
`
	files := mustParse(t, diff)
	e := NewEnricher(infra.NewOSFilesystemRepository(), checkout, 10)
	out := e.Render(context.Background(), files, CommentableRanges(files))
	mustNotContain(t, out, "TOP-SECRET")
}

func TestContainedAfterSymlinks_AllowsRegularFiles(t *testing.T) {
	t.Parallel()
	// Guard against over-blocking: a normal file under a (possibly
	// symlinked, e.g. macOS /tmp) working directory must stay readable.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("fine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !containedAfterSymlinks(dir, filepath.Join(dir, "ok.txt")) {
		t.Error("regular file inside workdir should be contained")
	}
}

func TestBuildPrompt(t *testing.T) {
	t.Parallel()
	p := BuildPrompt(Request{Title: "Fix bug", Body: "Details here"}, "DIFF-CONTENT", "")
	mustContain(t, p,
		"# PR Title\nFix bug", "# PR Description\nDetails here", "DIFF-CONTENT",
		"AddInlineReview", "AddSummaryReview", "FinalizeReview",
		"in this language: en",
	)
	// Empty body → no description section.
	p2 := BuildPrompt(Request{Title: "T"}, "d", "ja")
	mustNotContain(t, p2, "# PR Description")
	mustContain(t, p2, "in this language: ja")
	// No previous comments → no resolution instructions.
	mustNotContain(t, p, "Previous Review Comments", "ResolveReviewComment")
}

func TestBuildPrompt_IncrementalWithPreviousComments(t *testing.T) {
	t.Parallel()
	req := Request{
		Title: "T",
		Mode:  "incremental",
		PreviousComments: []PreviousComment{
			{ID: "PRRT_1", Path: "a.go", Line: 9, Body: "bad divisor\nsecond line"},
		},
	}
	p := BuildPrompt(req, "DIFF", "")
	mustContain(t, p,
		"# Review Mode: incremental",
		"ONLY the changes since the last review round",
		"# Previous Review Comments",
		"id=PRRT_1 a.go:9",
		"bad divisor\n  second line", // multi-line body indented under its item
		"ResolveReviewComment",
		"do NOT re-post a duplicate",
	)
}
