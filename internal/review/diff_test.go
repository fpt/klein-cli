package review

import (
	"strings"
	"testing"
)

const fooPath = "internal/foo.go"

const sampleGitDiff = `diff --git a/internal/foo.go b/internal/foo.go
index 1111111..2222222 100644
--- a/internal/foo.go
+++ b/internal/foo.go
@@ -10,7 +10,8 @@ func existing() {
 	a := 1
 	b := 2
-	c := a + b
+	c := a - b
+	d := c * 2
 	_ = c
 	return
 }
@@ -30,3 +31,4 @@ func tail() {
 	x()
 	y()
 	z()
+	w()
diff --git a/newfile.txt b/newfile.txt
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/newfile.txt
@@ -0,0 +1,2 @@
+hello
+world
diff --git a/gone.txt b/gone.txt
deleted file mode 100644
index 4444444..0000000
--- a/gone.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-bye
-now
`

func mustParse(t *testing.T, diff string) []FileDiff {
	t.Helper()
	files, err := ParseUnifiedDiff(diff)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	return files
}

func linesOfKind(h Hunk, kind LineKind) []Line {
	var out []Line
	for _, l := range h.Lines {
		if l.Kind == kind {
			out = append(out, l)
		}
	}
	return out
}

func TestParseUnifiedDiff_GitFormat(t *testing.T) {
	t.Parallel()
	files := mustParse(t, sampleGitDiff)
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(files))
	}

	f := files[0]
	if f.Path != fooPath || f.IsNew || f.IsDeleted {
		t.Errorf("file 0: got %+v", f)
	}
	if len(f.Hunks) != 2 {
		t.Fatalf("file 0: expected 2 hunks, got %d", len(f.Hunks))
	}
	h := f.Hunks[0]
	if h.OldStart != 10 || h.OldLines != 7 || h.NewStart != 10 || h.NewLines != 8 {
		t.Errorf("hunk 0 header: %+v", h)
	}
}

func TestParseUnifiedDiff_NewAndDeletedFiles(t *testing.T) {
	t.Parallel()
	files := mustParse(t, sampleGitDiff)
	if !files[1].IsNew || files[1].Path != "newfile.txt" {
		t.Errorf("file 1: got %+v", files[1])
	}
	if !files[2].IsDeleted || files[2].Path != "gone.txt" {
		t.Errorf("file 2: got %+v", files[2])
	}
}

func TestParseUnifiedDiff_LineNumbers(t *testing.T) {
	t.Parallel()
	files := mustParse(t, sampleGitDiff)
	h := files[0].Hunks[0]

	// Added lines "c := a - b" and "d := c * 2" follow context lines 10,11 →
	// new lines 12,13.
	added := linesOfKind(h, LineAdded)
	if len(added) != 2 || added[0].NewNumber != 12 || added[1].NewNumber != 13 {
		t.Errorf("added lines: %+v", added)
	}
	removed := linesOfKind(h, LineRemoved)
	if len(removed) != 1 || removed[0].OldNumber != 12 || removed[0].NewNumber != 0 {
		t.Errorf("removed lines: %+v", removed)
	}
}

func TestParseUnifiedDiff_PlainFormat(t *testing.T) {
	t.Parallel()
	plain := `--- a/x.txt
+++ b/x.txt
@@ -1,2 +1,2 @@
-old
+new
 same
--- a/y.txt
+++ b/y.txt
@@ -5 +5 @@
-five
+FIVE
`
	files := mustParse(t, plain)
	if len(files) != 2 || files[0].Path != "x.txt" || files[1].Path != "y.txt" {
		t.Fatalf("files: %+v", files)
	}
	if h := files[1].Hunks[0]; h.OldStart != 5 || h.OldLines != 1 || h.NewStart != 5 || h.NewLines != 1 {
		t.Errorf("count-less hunk header: %+v", h)
	}
}

// A body line is the file's own text with one marker glued to the front, so a
// language whose comments start with "--" (Lua, Haskell, SQL) produces deleted
// lines that read exactly like the "--- a/path" header of the next file — and
// "++ x" produces added lines that read like "+++ b/path". Splitting the file
// there truncates it to its first hunk, and every inline comment past that
// hunk is then rejected as "outside the diff" (fpt/rs-kessel#90).
func TestParseUnifiedDiff_BodyLinesThatLookLikeHeaders(t *testing.T) {
	t.Parallel()
	diff := `diff --git a/game.lua b/game.lua
index 1111111..2222222 100644
--- a/game.lua
+++ b/game.lua
@@ -1,3 +1,3 @@
--- the old comment
+-- the new comment
 local x = 1
 local y = 2
@@ -40,2 +40,3 @@ function set_mode(m)
   if m ~= mode then
+    release_all()
     mode = m
diff --git a/inc.c b/inc.c
index 3333333..4444444 100644
--- a/inc.c
+++ b/inc.c
@@ -7,1 +7,2 @@ void f(void) {
 	int i = 0;
+++ i;
`
	files := mustParse(t, diff)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d: %+v", len(files), files)
	}
	if files[0].Path != "game.lua" || len(files[0].Hunks) != 2 {
		t.Fatalf("game.lua: got path %q with %d hunks", files[0].Path, len(files[0].Hunks))
	}
	if files[1].Path != "inc.c" || len(files[1].Hunks) != 1 {
		t.Fatalf("inc.c: got path %q with %d hunks", files[1].Path, len(files[1].Hunks))
	}

	// The deleted comment is a removed line, not a file header.
	removed := linesOfKind(files[0].Hunks[0], LineRemoved)
	if len(removed) != 1 || removed[0].Content != "-- the old comment" {
		t.Errorf("removed lines: %+v", removed)
	}
	// The added "++ i;" is an added line, not a "+++ b/path" header.
	added := linesOfKind(files[1].Hunks[0], LineAdded)
	if len(added) != 1 || added[0].Content != "++ i;" {
		t.Errorf("added lines: %+v", added)
	}

	// And the second hunk is commentable, which is what the rejection cost.
	rs := CommentableRanges(files)
	if err := rs.Validate("game.lua", 41, 0); err != nil {
		t.Errorf("Validate(game.lua:41): %v", err)
	}
}

func TestParseUnifiedDiff_Errors(t *testing.T) {
	t.Parallel()
	if _, err := ParseUnifiedDiff("not a diff at all"); err == nil {
		t.Error("expected error for non-diff input")
	}
	if _, err := ParseUnifiedDiff("--- a/x\n+++ b/x\n@@ bogus @@\n"); err == nil {
		t.Error("expected error for malformed hunk header")
	}
}

func TestCommentableRangesAndValidate(t *testing.T) {
	t.Parallel()
	rs := CommentableRanges(mustParse(t, sampleGitDiff))

	// foo.go: hunks cover new lines 10-17 and 31-34.
	got := rs[fooPath]
	if len(got) != 2 || got[0] != (LineRange{10, 17}) || got[1] != (LineRange{31, 34}) {
		t.Fatalf("foo.go ranges: %+v", got)
	}

	cases := []struct {
		path      string
		errSubstr string
		line, end int
		wantErr   bool
	}{
		{fooPath, "", 12, 0, false},
		{fooPath, "", 10, 17, false},
		{fooPath, "", 31, 34, false},
		{fooPath, "outside the diff", 9, 0, true},
		{fooPath, "outside the diff", 17, 31, true}, // spans two hunks
		{fooPath, "invalid", 0, 0, true},
		{fooPath, "invalid", 12, 11, true},
		{"newfile.txt", "", 1, 2, false},
		{"gone.txt", "no commentable lines", 1, 0, true},
		{"unknown.go", "not part of the diff", 1, 0, true},
	}
	for _, c := range cases {
		err := rs.Validate(c.path, c.line, c.end)
		if !c.wantErr {
			if err != nil {
				t.Errorf("Validate(%s,%d,%d): unexpected error %v", c.path, c.line, c.end, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("Validate(%s,%d,%d): expected error", c.path, c.line, c.end)
		} else if !strings.Contains(err.Error(), c.errSubstr) {
			t.Errorf("Validate(%s,%d,%d): error %q missing %q", c.path, c.line, c.end, err, c.errSubstr)
		}
	}
}

func TestRangesDescribe(t *testing.T) {
	t.Parallel()
	rs := CommentableRanges(mustParse(t, sampleGitDiff))

	if got := rs.Describe(fooPath); got != "10-17, 31-34" {
		t.Errorf("Describe(%s) = %q", fooPath, got)
	}
	// Nothing to name: a deleted file and a file outside the diff both yield "".
	if got := rs.Describe("gone.txt"); got != "" {
		t.Errorf("Describe(gone.txt) = %q, want empty", got)
	}
	if got := rs.Describe("unknown.go"); got != "" {
		t.Errorf("Describe(unknown.go) = %q, want empty", got)
	}
}
