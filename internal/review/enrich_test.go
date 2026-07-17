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
}
