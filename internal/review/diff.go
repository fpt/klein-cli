// Package review implements the diff-side of `klein review`: parsing a unified
// diff into per-file hunks, deriving the new-side line ranges that inline review
// comments may target, and enriching hunks with surrounding context read from
// the PR-head checkout. It never touches git — the harness supplies the diff.
package review

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// LineKind classifies a line within a hunk body.
type LineKind int

// Line kinds within a hunk body.
const (
	LineContext LineKind = iota
	LineAdded
	LineRemoved
)

const devNull = "/dev/null"

// Line is one line of a hunk body. NewNumber is the line number in the new
// file (0 for removed lines); OldNumber is the number in the old file (0 for
// added lines).
type Line struct {
	Content   string // without the leading +/-/space marker
	Kind      LineKind
	OldNumber int
	NewNumber int
}

// Hunk is one @@ -a,b +c,d @@ section of a file diff.
type Hunk struct {
	Lines              []Line
	OldStart, OldLines int
	NewStart, NewLines int
}

// FileDiff is the diff of a single file.
type FileDiff struct {
	Path      string // new path (b/ side)
	OldPath   string // old path (a/ side); differs from Path on rename
	Hunks     []Hunk
	IsNew     bool
	IsDeleted bool
}

// LineRange is an inclusive new-side line range.
type LineRange struct {
	Start, End int
}

func (r LineRange) String() string {
	if r.Start == r.End {
		return strconv.Itoa(r.Start)
	}
	return fmt.Sprintf("%d-%d", r.Start, r.End)
}

// Ranges maps a file path to the new-side line ranges its diff covers —
// the only lines an inline review comment may target.
type Ranges map[string][]LineRange

// Validate reports whether [line, endLine] on path falls entirely within one
// commentable range. endLine <= 0 means a single-line comment.
func (rs Ranges) Validate(path string, line, endLine int) error {
	ranges, ok := rs[path]
	if !ok {
		paths := make([]string, 0, len(rs))
		for p := range rs {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		return fmt.Errorf("file %q is not part of the diff; files in the diff: %s",
			path, strings.Join(paths, ", "))
	}
	if len(ranges) == 0 {
		return fmt.Errorf("file %q has no commentable lines (deleted file); mention it in the summary instead", path)
	}
	if endLine <= 0 {
		endLine = line
	}
	if line <= 0 || endLine < line {
		return fmt.Errorf("invalid line range %d-%d", line, endLine)
	}
	for _, r := range ranges {
		if line >= r.Start && endLine <= r.End {
			return nil
		}
	}
	return fmt.Errorf("lines %d-%d of %q are outside the diff; commentable lines: %s",
		line, endLine, path, formatRanges(ranges))
}

func formatRanges(ranges []LineRange) string {
	parts := make([]string, len(ranges))
	for i, r := range ranges {
		parts[i] = r.String()
	}
	return strings.Join(parts, ", ")
}

// CommentableRanges derives per-file new-side ranges from parsed diffs.
// A deleted file is present with an empty slice so Validate can distinguish
// "not in diff" from "in diff but not commentable".
func CommentableRanges(files []FileDiff) Ranges {
	rs := make(Ranges, len(files))
	for _, f := range files {
		ranges := rs[f.Path]
		if f.IsDeleted {
			rs[f.Path] = ranges
			continue
		}
		for _, h := range f.Hunks {
			if h.NewLines == 0 {
				continue
			}
			ranges = append(ranges, LineRange{Start: h.NewStart, End: h.NewStart + h.NewLines - 1})
		}
		rs[f.Path] = ranges
	}
	return rs
}

// ParseUnifiedDiff parses the text of a unified diff (git or plain format)
// into per-file hunks. Unrecognized header lines (index, mode, similarity…)
// are skipped. Binary files produce a FileDiff with no hunks.
func ParseUnifiedDiff(diff string) ([]FileDiff, error) {
	p := &diffParser{}
	lines := strings.Split(diff, "\n")
	for i, line := range lines {
		if err := p.feed(line, i+1, i == len(lines)-1); err != nil {
			return nil, err
		}
	}
	p.flushFile()

	// Fill Path for pure deletions (only OldPath known) and normalize.
	for i := range p.files {
		if p.files[i].Path == "" {
			p.files[i].Path = p.files[i].OldPath
		}
		if p.files[i].OldPath == "" {
			p.files[i].OldPath = p.files[i].Path
		}
	}
	if len(p.files) == 0 {
		return nil, errors.New("no file diffs found in input")
	}
	return p.files, nil
}

// diffParser accumulates state while scanning a unified diff line by line.
type diffParser struct {
	cur     *FileDiff
	curHunk *Hunk
	files   []FileDiff
	oldNo   int
	newNo   int
}

// feed dispatches one input line. lineNo is 1-based, for error messages.
func (p *diffParser) feed(line string, lineNo int, isLast bool) error {
	switch {
	case strings.HasPrefix(line, "diff --git "):
		p.startGitFile(line)
	case strings.HasPrefix(line, "new file mode"):
		if p.cur != nil {
			p.cur.IsNew = true
		}
	case strings.HasPrefix(line, "deleted file mode"):
		if p.cur != nil {
			p.cur.IsDeleted = true
		}
	case strings.HasPrefix(line, "--- "):
		p.handleOldHeader(line)
	case strings.HasPrefix(line, "+++ "):
		p.handleNewHeader(line)
	case strings.HasPrefix(line, "@@ "):
		return p.handleHunkHeader(line, lineNo)
	default:
		p.handleBodyLine(line, isLast)
	}
	return nil
}

func (p *diffParser) flushHunk() {
	if p.cur != nil && p.curHunk != nil {
		p.cur.Hunks = append(p.cur.Hunks, *p.curHunk)
	}
	p.curHunk = nil
}

func (p *diffParser) flushFile() {
	p.flushHunk()
	if p.cur != nil {
		p.files = append(p.files, *p.cur)
	}
	p.cur = nil
}

func (p *diffParser) startGitFile(line string) {
	p.flushFile()
	p.cur = &FileDiff{}
	// Paths here may be quoted or contain spaces; prefer the ---/+++
	// lines below, but keep these as a fallback.
	if a, b, ok := parseDiffGitPaths(line); ok {
		p.cur.OldPath, p.cur.Path = a, b
	}
}

// handleOldHeader processes a "--- " line. Plain unified diffs have no
// "diff --git" line, so a "--- " header may also start a new file section.
func (p *diffParser) handleOldHeader(line string) {
	if p.cur == nil || p.curHunk != nil {
		p.flushFile()
		p.cur = &FileDiff{}
	}
	if path := stripDiffPath(line[4:], "a/"); path == devNull {
		p.cur.IsNew = true
	} else {
		p.cur.OldPath = path
	}
}

func (p *diffParser) handleNewHeader(line string) {
	if p.cur == nil {
		p.cur = &FileDiff{}
	}
	if path := stripDiffPath(line[4:], "b/"); path == devNull {
		p.cur.IsDeleted = true
	} else {
		p.cur.Path = path
	}
}

func (p *diffParser) handleHunkHeader(line string, lineNo int) error {
	if p.cur == nil {
		return fmt.Errorf("line %d: hunk header before any file header: %q", lineNo, line)
	}
	p.flushHunk()
	h, err := parseHunkHeader(line)
	if err != nil {
		return fmt.Errorf("line %d: %w", lineNo, err)
	}
	p.curHunk = &h
	p.oldNo, p.newNo = h.OldStart, h.NewStart
	return nil
}

func (p *diffParser) handleBodyLine(line string, isLast bool) {
	if p.curHunk == nil {
		return // header noise (index, mode, similarity, Binary files…)
	}
	if line == "" && isLast {
		return // trailing newline of the diff text
	}
	switch {
	case strings.HasPrefix(line, "+"):
		p.curHunk.Lines = append(p.curHunk.Lines, Line{Kind: LineAdded, Content: line[1:], NewNumber: p.newNo})
		p.newNo++
	case strings.HasPrefix(line, "-"):
		p.curHunk.Lines = append(p.curHunk.Lines, Line{Kind: LineRemoved, Content: line[1:], OldNumber: p.oldNo})
		p.oldNo++
	case strings.HasPrefix(line, `\`):
		// "\ No newline at end of file" — not a content line.
	default:
		// A context line: normally prefixed with a space, but some diffs
		// render empty context lines as "" instead of " ".
		content := strings.TrimPrefix(line, " ")
		p.curHunk.Lines = append(p.curHunk.Lines,
			Line{Kind: LineContext, Content: content, OldNumber: p.oldNo, NewNumber: p.newNo})
		p.oldNo++
		p.newNo++
	}
}

// parseHunkHeader parses "@@ -a[,b] +c[,d] @@ ...".
func parseHunkHeader(line string) (Hunk, error) {
	rest := strings.TrimPrefix(line, "@@ ")
	head, _, found := strings.Cut(rest, " @@")
	if !found {
		return Hunk{}, fmt.Errorf("malformed hunk header: %q", line)
	}
	fields := strings.Fields(head)
	if len(fields) != 2 || !strings.HasPrefix(fields[0], "-") || !strings.HasPrefix(fields[1], "+") {
		return Hunk{}, fmt.Errorf("malformed hunk header: %q", line)
	}
	oldStart, oldLines, err := parseHunkRange(fields[0][1:])
	if err != nil {
		return Hunk{}, fmt.Errorf("malformed hunk header %q: %w", line, err)
	}
	newStart, newLines, err := parseHunkRange(fields[1][1:])
	if err != nil {
		return Hunk{}, fmt.Errorf("malformed hunk header %q: %w", line, err)
	}
	return Hunk{OldStart: oldStart, OldLines: oldLines, NewStart: newStart, NewLines: newLines}, nil
}

// parseHunkRange parses "start[,count]"; count defaults to 1.
func parseHunkRange(s string) (start, count int, err error) {
	count = 1
	if head, tail, found := strings.Cut(s, ","); found {
		if count, err = strconv.Atoi(tail); err != nil {
			return 0, 0, fmt.Errorf("parse hunk count: %w", err)
		}
		s = head
	}
	if start, err = strconv.Atoi(s); err != nil {
		return 0, 0, fmt.Errorf("parse hunk start: %w", err)
	}
	return start, count, nil
}

// parseDiffGitPaths extracts a/ and b/ paths from a "diff --git a/x b/x" line.
// Best-effort: paths with spaces are resolved by the ---/+++ lines instead.
func parseDiffGitPaths(line string) (oldPath, newPath string, ok bool) {
	rest := strings.TrimPrefix(line, "diff --git ")
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return "", "", false
	}
	return strings.TrimPrefix(fields[0], "a/"), strings.TrimPrefix(fields[len(fields)-1], "b/"), true
}

// stripDiffPath cleans a path from a ---/+++ header: drops an optional prefix
// (a/ or b/), a trailing tab-separated timestamp, and surrounding quotes.
func stripDiffPath(s, prefix string) string {
	if tab := strings.IndexByte(s, '\t'); tab >= 0 {
		s = s[:tab]
	}
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"`)
	if s == devNull {
		return s
	}
	return strings.TrimPrefix(s, prefix)
}
