package review

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"pgregory.net/rapid"

	"github.com/fpt/klein-cli/pkg/testhelper/diffgen"
)

var kindOf = map[diffgen.Kind]LineKind{
	diffgen.Context: LineContext,
	diffgen.Added:   LineAdded,
	diffgen.Removed: LineRemoved,
}

// expectedFiles is what parsing d's text must give back, read off the model.
func expectedFiles(d diffgen.Diff) []FileDiff {
	out := make([]FileDiff, 0, len(d.Files))
	for _, f := range d.Files {
		fd := FileDiff{Path: f.Path, OldPath: f.OldPath, IsNew: f.New, IsDeleted: f.Deleted}
		for _, h := range f.Hunks {
			hd := Hunk{OldStart: h.OldStart, OldLines: h.OldLines, NewStart: h.NewStart, NewLines: h.NewLines}
			for _, l := range h.Lines {
				hd.Lines = append(hd.Lines, Line{Content: l.Content, Kind: kindOf[l.Kind], OldNumber: l.Old, NewNumber: l.New})
			}
			fd.Hunks = append(fd.Hunks, hd)
		}
		out = append(out, fd)
	}
	return out
}

func describeFiles(files []FileDiff) string {
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "file %q (old %q) new=%v deleted=%v\n", f.Path, f.OldPath, f.IsNew, f.IsDeleted)
		for _, h := range f.Hunks {
			fmt.Fprintf(&b, "  @@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
			for _, l := range h.Lines {
				fmt.Fprintf(&b, "    kind=%d old=%d new=%d %q\n", l.Kind, l.OldNumber, l.NewNumber, l.Content)
			}
		}
	}
	return b.String()
}

// TestParseUnifiedDiff_RoundTrip checks D1: parsing a well-formed diff gives
// back exactly the files, paths, new/deleted flags, hunk headers, and each body
// line's kind, content and old/new numbers that the diff encodes — including
// when a body line's content reads like a header once its marker is glued on.
// (D2, "each hunk's line counts match its header", is implied: the model's
// headers count its bodies exactly.)
func TestParseUnifiedDiff_RoundTrip(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		d := diffgen.Diffs().Draw(t, "diff")
		text := d.Text()
		if endsInBareEmptyLine(d) {
			return // undecided: TestParseUnifiedDiff_DropsFinalBareEmptyContextLine
		}
		got, err := ParseUnifiedDiff(text)
		if err != nil {
			t.Fatalf("ParseUnifiedDiff: %v\n%s", err, text)
		}
		if g, w := describeFiles(got), describeFiles(expectedFiles(d)); g != w {
			t.Fatalf("parse mismatch\n--- text ---\n%s\n--- got ---\n%s--- want ---\n%s", text, g, w)
		}
	})
}

// endsInBareEmptyLine reports whether d's text has no trailing newline and its
// last line is an empty context line rendered bare — the one input where the
// final "" is a body line and not the split-off end of the text.
func endsInBareEmptyLine(d diffgen.Diff) bool {
	return !d.TrailingNewline && strings.HasSuffix(d.Text(), "\n")
}

// commentable reports whether the model says [line, end] may carry an inline
// comment: every line in it is a new-side line (added or context) of one and
// the same hunk. end <= 0 means a single line.
func commentable(f diffgen.File, line, end int) bool {
	if end <= 0 {
		end = line
	}
	if line <= 0 || end < line || f.Deleted {
		return false
	}
	for _, h := range f.Hunks {
		if coversNewSide(h, line, end) {
			return true
		}
	}
	return false
}

// coversNewSide reports whether every line in [line, end] is an added or
// context line of h.
func coversNewSide(h diffgen.Hunk, line, end int) bool {
	have := map[int]bool{}
	for _, l := range h.Lines {
		if l.Kind != diffgen.Removed {
			have[l.New] = true
		}
	}
	for n := line; n <= end; n++ {
		if !have[n] {
			return false
		}
	}
	return true
}

// TestRangesValidate_MatchesNewSideLines checks D3: Validate accepts a
// comment on [line, end] of a file exactly when every line in it is an added
// or context line of a single hunk, and rejects every path not in the diff
// (a rename's old path included).
func TestRangesValidate_MatchesNewSideLines(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		d := diffgen.Diffs().Draw(t, "diff")
		files, err := ParseUnifiedDiff(d.Text())
		if err != nil {
			t.Fatalf("ParseUnifiedDiff: %v", err)
		}
		rs := CommentableRanges(files)
		for _, f := range d.Files {
			checkValidate(t, rs, f)
		}
		if rs.Validate("not/in/diff.go", 1, 0) == nil {
			t.Fatal("Validate accepted a path that is not in the diff")
		}
	})
}

// checkValidate compares Validate with the model over every line and range
// near f's hunks.
func checkValidate(t *rapid.T, rs Ranges, f diffgen.File) {
	maxNew := 0
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			maxNew = max(maxNew, l.New)
		}
	}
	for line := -1; line <= maxNew+2; line++ {
		for _, end := range []int{0, line - 1, line, line + 1, line + 3} {
			got := rs.Validate(f.Path, line, end) == nil
			if want := commentable(f, line, end); got != want {
				t.Fatalf("Validate(%q, %d, %d) ok=%v, want %v", f.Path, line, end, got, want)
			}
		}
	}
	if f.OldPath != f.Path && rs.Validate(f.OldPath, 1, 0) == nil {
		t.Fatalf("Validate accepted renamed-away path %q", f.OldPath)
	}
}

// TestCommentableRanges_DeletedFilePresentButEmpty checks D5: a deleted file
// has an entry, and the entry is empty, so Validate can tell "deleted" apart
// from "not in the diff".
func TestCommentableRanges_DeletedFilePresentButEmpty(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		d := diffgen.Diffs().Draw(t, "diff")
		files, err := ParseUnifiedDiff(d.Text())
		if err != nil {
			t.Fatalf("ParseUnifiedDiff: %v", err)
		}
		rs := CommentableRanges(files)
		for _, f := range d.Files {
			ranges, ok := rs[f.Path]
			if !ok {
				t.Fatalf("no Ranges entry for %q", f.Path)
			}
			if f.Deleted && len(ranges) != 0 {
				t.Fatalf("deleted %q has ranges %v", f.Path, ranges)
			}
		}
	})
}

// TestParseUnifiedDiff_MiscountCannotSwallowGitFile checks D6, the guarantee
// stated on diffParser.feed: in git format, a hunk header that miscounts its
// body never swallows the next file — every file's "diff --git" still starts a
// FileDiff with that file's path, in order. Files whose own body forges a
// "--- "/"+++ " header are excluded: what a miscount does *inside* such a file
// is undecided (see TestParseUnifiedDiff_UndercountLetsBodyForgeHeader).
func TestParseUnifiedDiff_MiscountCannotSwallowGitFile(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		d := diffgen.Diffs().Draw(t, "diff")
		if !d.Git {
			return
		}
		bad := diffgen.Miscount(t, d)
		got, err := ParseUnifiedDiff(bad.Text())
		if err != nil {
			t.Fatalf("ParseUnifiedDiff: %v\n%s", err, bad.Text())
		}
		// A file that forges a header in its own body may lose its own path
		// (pinned below); the guarantee is that it cannot take the next one's.
		var want []string
		for _, f := range d.Files {
			if !forgesHeader(f) {
				want = append(want, f.Path)
			}
		}
		i := 0
		for _, f := range got {
			if i < len(want) && f.Path == want[i] {
				i++
			}
		}
		if i != len(want) {
			t.Fatalf("file %q was swallowed\n--- text ---\n%s\n--- got ---\n%s", want[i], bad.Text(), describeFiles(got))
		}
	})
}

// forgesHeader reports whether one of f's body lines reads as a "--- " or
// "+++ " header once its marker is glued on.
func forgesHeader(f diffgen.File) bool {
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			forgesOld := l.Kind == diffgen.Removed && strings.HasPrefix(l.Content, "-- ")
			forgesNew := l.Kind == diffgen.Added && strings.HasPrefix(l.Content, "++ ")
			if forgesOld || forgesNew {
				return true
			}
		}
	}
	return false
}

// lineText draws text with newlines and multi-byte runes, so a cut in the wrong
// place would split a rune.
func lineText() *rapid.Generator[string] {
	return rapid.StringMatching(`([ab]|é|\n){0,40}`)
}

// refTruncateAtLine is the longest prefix of s that is at most n bytes and is
// either s itself or ends in "\n", found by trying every prefix.
func refTruncateAtLine(s string, n int) string {
	best := ""
	for i := 0; i <= len(s); i++ {
		p := s[:i]
		if len(p) <= n && (i == len(s) || strings.HasSuffix(p, "\n")) {
			best = p
		}
	}
	return best
}

// TestTruncateAtLine_LongestLinePrefix checks D4: truncateAtLine(s, n) is the
// longest prefix of s within n bytes that ends on a line boundary (or is s),
// so it never ends mid-line or mid-rune.
func TestTruncateAtLine_LongestLinePrefix(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		s := lineText().Draw(t, "s")
		n := rapid.IntRange(0, len(s)+2).Draw(t, "n")
		got := truncateAtLine(s, n)
		if want := refTruncateAtLine(s, n); got != want {
			t.Fatalf("truncateAtLine(%q, %d) = %q, want %q", s, n, got, want)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("truncateAtLine(%q, %d) = %q splits a rune", s, n, got)
		}
	})
}

// TestApplyByteBudget_WithinBudget checks D4b: with a budget, the rendered
// diff is returned unchanged when it fits; otherwise the result is at most
// maxBytes, is the longest line-boundary prefix that leaves room for the
// truncation marker, and carries the marker whenever the marker fits at all.
func TestApplyByteBudget_WithinBudget(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		// Longer than the marker, or the over-budget-with-marker path is never
		// reached: the marker alone is about 75 bytes.
		// (rapid.StringMatching keeps strings short, so build it from lines.)
		lines := rapid.SliceOfN(rapid.StringMatching(`([ab]|é){0,12}`), 0, 30).Draw(t, "lines")
		out := strings.Join(lines, "\n") + "\n"
		// Budgets around the marker's size are the ones fix 5bcebad was about.
		maxBytes := rapid.OneOf(
			rapid.IntRange(-1, len(out)+2),
			rapid.IntRange(len(truncationMarker)-2, len(truncationMarker)+len(out)+2),
		).Draw(t, "maxBytes")
		e := &Enricher{maxBytes: maxBytes}
		got := e.applyByteBudget(out)

		if maxBytes <= 0 || len(out) <= maxBytes {
			if got != out {
				t.Fatalf("budget %d, len %d: changed a diff that fits: %q", maxBytes, len(out), got)
			}
			return
		}
		if len(got) > maxBytes {
			t.Fatalf("budget %d: result is %d bytes: %q", maxBytes, len(got), got)
		}
		want := refTruncateAtLine(out, maxBytes)
		if maxBytes >= len(truncationMarker) {
			want = refTruncateAtLine(out, maxBytes-len(truncationMarker)) + truncationMarker
		}
		if got != want {
			t.Fatalf("applyByteBudget(%q, %d) = %q, want %q", out, maxBytes, got, want)
		}
	})
}

// Behavior below is pinned as it is today, not confirmed as intended. Each one
// is listed under "Team decision" for §3 in doc/FORMAL_FIT.md.

// TestParseUnifiedDiff_DropsFinalBareEmptyContextLine pins: when the diff text
// has no trailing newline and its last line is an empty context line written
// bare (""), that line is taken for the end of the text and dropped, leaving
// the hunk one line short of its header. Not reachable from the GHA harness
// (git writes " ", and jq --rawfile keeps the trailing newline); unverified for
// other callers of `klein review --input`.
func TestParseUnifiedDiff_DropsFinalBareEmptyContextLine(t *testing.T) {
	t.Parallel()
	const header = "--- a/x.txt\n+++ b/x.txt\n@@ -1,2 +1,2 @@\n"
	// The header owes three body lines: "-a", "+b" and one empty context line
	// (old 2, new 2). Written bare with no trailing newline, that last line is
	// the "" after the final "\n" — the same text a trailing newline produces,
	// which is why the parser cannot tell them apart.
	h := mustParse(t, header+"-a\n+b\n"+"")[0].Hunks[0]
	if h.OldLines != 2 || h.NewLines != 2 || len(h.Lines) != 2 {
		t.Fatalf("expected the final bare context line dropped (header 2/2, 2 body lines), got %+v", h)
	}
	// Contrast: the same body followed by a trailing newline keeps it.
	h = mustParse(t, header+"-a\n+b\n"+"\n")[0].Hunks[0]
	if len(h.Lines) != 3 || h.Lines[2].Kind != LineContext || h.Lines[2].Content != "" || h.Lines[2].NewNumber != 2 {
		t.Fatalf("with a trailing newline the empty context line should be kept, got %+v", h.Lines)
	}
}

// TestParseUnifiedDiff_UndercountLetsBodyForgeHeader pins: once a hunk header's
// counts run out, a later body line that reads as "--- "/"+++ " is a header.
// An added "++ i;" then renames the file to "i;", and a deleted "-- x" starts a
// spurious file. The next "diff --git" file is unaffected either way (D6).
func TestParseUnifiedDiff_UndercountLetsBodyForgeHeader(t *testing.T) {
	t.Parallel()
	renamed := mustParse(t, "diff --git a/c.c b/c.c\n--- a/c.c\n+++ b/c.c\n@@ -1 +1,1 @@\n a\n+++ i;\n")
	if len(renamed) != 1 || renamed[0].Path != "i;" {
		t.Errorf("forged +++: %s", describeFiles(renamed))
	}
	split := mustParse(t, "diff --git a/g.lua b/g.lua\n--- a/g.lua\n+++ b/g.lua\n@@ -1 +1 @@\n a\n--- x\n")
	if len(split) != 2 || split[0].Path != "g.lua" || split[1].Path != "x" {
		t.Errorf("forged ---: %s", describeFiles(split))
	}
}

// TestParseUnifiedDiff_PlainOvercountSwallowsNextFile pins: in plain `diff -u`
// format, where "--- " is the only file separator, a hunk header that claims
// more lines than it has reads the next file's ---/+++ lines as body, and that
// file's hunks join the previous file. (Git format is protected by its
// "diff --git" line — D6.)
func TestParseUnifiedDiff_PlainOvercountSwallowsNextFile(t *testing.T) {
	t.Parallel()
	files := mustParse(t, "--- a/x\n+++ b/x\n@@ -1,3 +1,3 @@\n-a\n+b\n--- a/y\n+++ b/y\n@@ -1 +1 @@\n-c\n+d\n")
	if len(files) != 1 || files[0].Path != "x" || len(files[0].Hunks) != 2 {
		t.Errorf("expected y swallowed into x: %s", describeFiles(files))
	}
}

// TestParseUnifiedDiff_PlainDiffCommandLineIsContext pins: `diff -ruN` writes a
// "diff -ruN a/y b/y" line before each file's --- header. After a hunk it is
// read as a context line of that hunk, one past its header's count. The file
// split itself still happens at the "--- " that follows.
func TestParseUnifiedDiff_PlainDiffCommandLineIsContext(t *testing.T) {
	t.Parallel()
	files := mustParse(t, "diff -ruN a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n"+
		"diff -ruN a/y b/y\n--- a/y\n+++ b/y\n@@ -1 +1 @@\n-c\n+d\n")
	if len(files) != 2 {
		t.Fatalf("files: %s", describeFiles(files))
	}
	lines := files[0].Hunks[0].Lines
	if last := lines[len(lines)-1]; len(lines) != 3 || last.Kind != LineContext || last.Content != "diff -ruN a/y b/y" {
		t.Errorf("x's hunk: %s", describeFiles(files[:1]))
	}
}
