// Package diffgen provides rapid generators for unified diffs, for property
// tests over code that parses them (internal/review/diff.go).
//
// A generated Diff is a model: it carries the files, hunks and per-line
// old/new numbers the text is supposed to encode, computed here by simple
// counting rather than by the parser under test. Text renders it.
package diffgen

import (
	"fmt"
	"strconv"
	"strings"

	"pgregory.net/rapid"
)

const devNull = "/dev/null"

// Kind is a hunk body line's kind.
type Kind int

// Line kinds, in the order internal/review numbers them.
const (
	Context Kind = iota
	Added
	Removed
)

// Line is one body line. Old is 0 for added lines, New is 0 for removed ones.
type Line struct {
	Content string
	Kind    Kind
	Old     int
	New     int
}

// Hunk is one @@ section. The header counts are truthful unless a Miscount
// generator changed them afterwards.
type Hunk struct {
	Lines              []Line
	OldStart, OldLines int
	NewStart, NewLines int
	// NoNewline marks the hunk's last line with "\ No newline at end of file".
	NoNewline bool
}

// File is one file's diff.
type File struct {
	Path    string // new-side path
	OldPath string // old-side path; differs from Path on rename
	Hunks   []Hunk
	New     bool
	Deleted bool
	Binary  bool // git only: "Binary files … differ", no ---/+++ lines, no hunks
}

// Diff is a whole diff and the choices that shape its text.
type Diff struct {
	Files []File
	// Git selects `git diff` output (diff --git, index, mode lines); otherwise
	// plain `diff -u` output, where a "--- " line is the only file separator.
	Git bool
	// TrailingNewline is false for a diff text whose last line has no "\n".
	TrailingNewline bool
	// BareEmptyContext renders an empty context line as "" instead of " ",
	// which some tools (and editors that strip trailing spaces) produce.
	BareEmptyContext bool
	// Timestamps appends a tab and a timestamp to plain-format ---/+++ paths.
	Timestamps bool
}

// tricky are line contents that read like diff syntax once a marker is glued
// to the front: a deleted Lua/SQL comment "-- x" becomes "--- x", an added
// "++ i;" becomes "+++ i;" (fix 2bc7f11), plus contents that start with the
// other syntax a parser dispatches on.
var tricky = []string{
	"",
	"-- a lua comment",
	"--- a/fake.go",
	"++ i;",
	"++ b/fake.go",
	"-",
	"+",
	`\ not a marker`,
	"@@ -1 +1 @@",
	"diff --git a/z b/z",
	"new file mode 100644",
	"deleted file mode 100644",
	"\tindented",
	"héllo wörld",
}

// Diffs generates diffs shaped the way `git diff` (and, for the plain format,
// `diff -u`) writes them: per file, hunks in increasing order on both sides,
// each header counting exactly the lines in its body.
//
// Boundaries drawn on purpose:
//   - body lines whose content forges a header once its marker is prepended
//     ("-- x" deleted, "++ x" added), and contents starting with "@@ ",
//     "diff --git ", "\", or nothing at all;
//   - empty context lines rendered bare (""), including as the very last line;
//   - "\ No newline at end of file" after a hunk's last line;
//   - a diff text without a trailing newline;
//   - hunk headers with an omitted ",1" count, and zero-count sides
//     (pure additions / pure deletions, "@@ -0,0 +1,n @@");
//   - new, deleted, renamed, binary and empty-new files.
func Diffs() *rapid.Generator[Diff] {
	return rapid.Custom(func(t *rapid.T) Diff {
		d := Diff{
			Git:              rapid.Bool().Draw(t, "git"),
			TrailingNewline:  rapid.IntRange(0, 7).Draw(t, "noTrailingNewlineOdds") > 0,
			BareEmptyContext: rapid.IntRange(0, 3).Draw(t, "bareEmptyOdds") == 0,
			Timestamps:       rapid.Bool().Draw(t, "timestamps"),
		}
		n := rapid.IntRange(1, 4).Draw(t, "files")
		// Distinct ids, two per file, so no path is ever reused (a rename's old
		// path included) and Ranges keys stay one-to-one with files.
		ids := rapid.SliceOfNDistinct(rapid.IntRange(0, 99), 2*n, 2*n, rapid.ID[int]).Draw(t, "pathIDs")
		for i := range n {
			d.Files = append(d.Files, file(t, d.Git, ids[2*i], ids[2*i+1]))
		}
		return d
	})
}

func pathFor(id int) string { return fmt.Sprintf("pkg%d/f%d.lua", id%5, id) }

func file(t *rapid.T, git bool, id, renameID int) File {
	f := File{Path: pathFor(id), OldPath: pathFor(id)}
	kind := rapid.IntRange(0, 9).Draw(t, "fileKind")
	switch {
	case kind == 0:
		f.New = true
		size := rapid.IntRange(0, 4).Draw(t, "newSize")
		if size == 0 && !git {
			size = 1 // plain diff cannot express an empty new file
		}
		if size > 0 {
			f.Hunks = []Hunk{wholeFile(t, Added, size)}
		}
	case kind == 1:
		f.Deleted = true
		f.Hunks = []Hunk{wholeFile(t, Removed, rapid.IntRange(1, 4).Draw(t, "deletedSize"))}
	case kind == 2 && git:
		f.Binary = true
	default:
		if kind == 3 {
			f.OldPath = pathFor(renameID)
		}
		f.Hunks = hunks(t)
	}
	return f
}

func content(t *rapid.T) string {
	if rapid.IntRange(0, 2).Draw(t, "trickyOdds") > 0 {
		return rapid.SampledFrom(tricky).Draw(t, "tricky")
	}
	return rapid.StringMatching(`[a-z =()]{0,8}`).Draw(t, "text")
}

// wholeFile is the single hunk of a new (Added) or deleted (Removed) file.
func wholeFile(t *rapid.T, k Kind, size int) Hunk {
	h := Hunk{}
	for i := 1; i <= size; i++ {
		l := Line{Content: content(t), Kind: k}
		if k == Added {
			l.New = i
		} else {
			l.Old = i
		}
		h.Lines = append(h.Lines, l)
	}
	if k == Added {
		h.NewStart, h.NewLines = 1, size
	} else {
		h.OldStart, h.OldLines = 1, size
	}
	h.NoNewline = rapid.IntRange(0, 5).Draw(t, "noNewlineOdds") == 0
	return h
}

func hunks(t *rapid.T) []Hunk {
	oldPos, newPos := 1, 1
	n := rapid.IntRange(1, 3).Draw(t, "hunks")
	out := make([]Hunk, 0, n)
	for i := range n {
		// Unchanged lines between hunks; the same count on both sides.
		gap := rapid.IntRange(0, 30).Draw(t, "gap")
		if i > 0 {
			gap++ // hunks never touch, or git would have merged them
		}
		oldPos += gap
		newPos += gap

		h := Hunk{}
		size := rapid.IntRange(1, 6).Draw(t, "hunkSize")
		for range size {
			l := Line{Content: content(t), Kind: Kind(rapid.IntRange(0, 2).Draw(t, "kind"))}
			if l.Kind != Added {
				l.Old = oldPos
				oldPos++
				h.OldLines++
			}
			if l.Kind != Removed {
				l.New = newPos
				newPos++
				h.NewLines++
			}
			h.Lines = append(h.Lines, l)
		}
		h.OldStart, h.NewStart = start(h.Lines, true), start(h.Lines, false)
		if h.OldLines == 0 {
			h.OldStart = oldPos - 1 // git: a zero-count side names the line before
		}
		if h.NewLines == 0 {
			h.NewStart = newPos - 1
		}
		out = append(out, h)
	}
	if rapid.IntRange(0, 5).Draw(t, "noNewlineOdds") == 0 {
		out[len(out)-1].NoNewline = true
	}
	return out
}

func start(lines []Line, old bool) int {
	for _, l := range lines {
		if old && l.Kind != Added {
			return l.Old
		}
		if !old && l.Kind != Removed {
			return l.New
		}
	}
	return 0
}

// Miscount returns a copy of d where some hunk headers claim more or fewer
// lines than their bodies hold — what a hand-edited or truncated diff looks
// like. Body lines and their model numbers are left as they were.
func Miscount(t *rapid.T, d Diff) Diff {
	out := d
	out.Files = make([]File, len(d.Files))
	for i, f := range d.Files {
		f.Hunks = append([]Hunk(nil), f.Hunks...)
		for j := range f.Hunks {
			if rapid.Bool().Draw(t, "miscount") {
				f.Hunks[j].OldLines = max(0, f.Hunks[j].OldLines+rapid.IntRange(-3, 3).Draw(t, "oldDelta"))
				f.Hunks[j].NewLines = max(0, f.Hunks[j].NewLines+rapid.IntRange(-3, 3).Draw(t, "newDelta"))
			}
		}
		out.Files[i] = f
	}
	return out
}

// Text renders d as diff text.
func (d Diff) Text() string {
	var lines []string
	for _, f := range d.Files {
		lines = append(lines, d.header(f)...)
		for _, h := range f.Hunks {
			lines = append(lines, fmt.Sprintf("@@ -%s +%s @@",
				hunkRange(h.OldStart, h.OldLines), hunkRange(h.NewStart, h.NewLines)))
			for _, l := range h.Lines {
				lines = append(lines, d.body(l))
			}
			if h.NoNewline {
				lines = append(lines, `\ No newline at end of file`)
			}
		}
	}
	text := strings.Join(lines, "\n")
	if d.TrailingNewline {
		text += "\n"
	}
	return text
}

// sideNames are a file's ---/+++ names, /dev/null for the side that is absent.
func sideNames(f File) (oldName, newName string) {
	oldName, newName = "a/"+f.OldPath, "b/"+f.Path
	if f.New {
		oldName = devNull
	}
	if f.Deleted {
		newName = devNull
	}
	return oldName, newName
}

func (d Diff) header(f File) []string {
	oldName, newName := sideNames(f)
	if !d.Git {
		if d.Timestamps {
			oldName += "\t2026-11-28 10:00:00.000000000 +0900"
			newName += "\t2026-11-28 10:00:00.000000000 +0900"
		}
		return []string{"--- " + oldName, "+++ " + newName}
	}
	out := append([]string{fmt.Sprintf("diff --git a/%s b/%s", f.OldPath, f.Path)}, gitExtended(f)...)
	if f.Binary {
		return append(out, fmt.Sprintf("Binary files a/%s and b/%s differ", f.OldPath, f.Path))
	}
	if f.New && len(f.Hunks) == 0 {
		return out // git prints no ---/+++ for an empty new file
	}
	return append(out, "--- "+oldName, "+++ "+newName)
}

// gitExtended are the lines git writes between "diff --git" and "---".
func gitExtended(f File) []string {
	switch {
	case f.New:
		return []string{"new file mode 100644", "index 0000000..1111111"}
	case f.Deleted:
		return []string{"deleted file mode 100644", "index 1111111..0000000"}
	case f.OldPath != f.Path:
		return []string{
			"similarity index 90%", "rename from " + f.OldPath, "rename to " + f.Path,
			"index 1111111..2222222 100644",
		}
	}
	return []string{"index 1111111..2222222 100644"}
}

func (d Diff) body(l Line) string {
	switch l.Kind {
	case Added:
		return "+" + l.Content
	case Removed:
		return "-" + l.Content
	}
	if l.Content == "" && d.BareEmptyContext {
		return ""
	}
	return " " + l.Content
}

// hunkRange writes "start,count", omitting ",1" the way git does.
func hunkRange(start, count int) string {
	if count == 1 {
		return strconv.Itoa(start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}
