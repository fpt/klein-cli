package review

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fpt/klein-cli/internal/repository"
)

// Enricher renders parsed file diffs with extra context lines read from the
// PR-head checkout (pure file reads — no git).
type Enricher struct {
	fsRepo     repository.FilesystemRepository
	workingDir string
	context    int // lines of extra context around each hunk
}

// NewEnricher creates an Enricher reading files relative to workingDir.
func NewEnricher(fsRepo repository.FilesystemRepository, workingDir string, contextLines int) *Enricher {
	return &Enricher{fsRepo: fsRepo, workingDir: workingDir, context: contextLines}
}

// Render produces the annotated review view of all file diffs. Layout per file:
//
//	## File: internal/foo.go
//	Commentable lines (inline comments MUST target these): 120-135, 200-210
//	     115  ctx| surrounding line (context only — NOT commentable)
//	   [ 120] +  | added line
//	   [ 121]    | unchanged line inside the diff
//	          -  | removed line (no new-side number)
//
// New-side line numbers appear in brackets only for commentable lines, so the
// model cannot mistake enrichment context for valid comment targets.
func (e *Enricher) Render(ctx context.Context, files []FileDiff, ranges Ranges) string {
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "## File: %s", f.Path)
		switch {
		case f.IsNew:
			b.WriteString(" (new file)")
		case f.IsDeleted:
			b.WriteString(" (deleted)")
		case f.OldPath != f.Path:
			fmt.Fprintf(&b, " (renamed from %s)", f.OldPath)
		}
		b.WriteString("\n")

		fileRanges := ranges[f.Path]
		if len(fileRanges) == 0 {
			b.WriteString("No commentable lines (mention this file in the summary if needed).\n")
			if f.IsDeleted {
				b.WriteString(e.renderRawHunks(f))
				b.WriteString("\n")
				continue
			}
		} else {
			fmt.Fprintf(&b, "Commentable lines (inline comments MUST target these): %s\n", formatRanges(fileRanges))
		}

		fileLines := e.readFileLines(ctx, f.Path)
		for _, h := range f.Hunks {
			b.WriteString(e.renderHunk(h, fileLines))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// readFileLines reads the new-side file content for context expansion.
// Returns nil when unreadable (deleted/binary/missing) — rendering then falls
// back to the raw hunk without extra context.
//
// The path comes from the untrusted diff, so it must stay inside the
// checkout: a crafted "../…" or absolute path could otherwise pull arbitrary
// files from the host into the LLM prompt.
func (e *Enricher) readFileLines(ctx context.Context, path string) []string {
	if !filepath.IsLocal(path) {
		return nil
	}
	data, err := e.fsRepo.ReadFile(ctx, filepath.Join(e.workingDir, path))
	if err != nil {
		return nil
	}
	return strings.Split(string(data), "\n")
}

// renderHunk renders one hunk with up to e.context extra lines before and
// after, pulled from the current file content.
func (e *Enricher) renderHunk(h Hunk, fileLines []string) string {
	var b strings.Builder

	newEnd := h.NewStart + h.NewLines - 1
	fmt.Fprintf(&b, "@@ diff lines %d-%d @@\n", h.NewStart, max(h.NewStart, newEnd))

	// Leading context from the file (not commentable).
	if fileLines != nil {
		from := max(1, h.NewStart-e.context)
		for n := from; n < h.NewStart; n++ {
			if n-1 < len(fileLines) {
				fmt.Fprintf(&b, "  %5d  ctx| %s\n", n, fileLines[n-1])
			}
		}
	}

	for _, l := range h.Lines {
		switch l.Kind {
		case LineAdded:
			fmt.Fprintf(&b, "[%5d] +  | %s\n", l.NewNumber, l.Content)
		case LineRemoved:
			fmt.Fprintf(&b, "        -  | %s\n", l.Content)
		default:
			fmt.Fprintf(&b, "[%5d]    | %s\n", l.NewNumber, l.Content)
		}
	}

	// Trailing context from the file (not commentable).
	if fileLines != nil && newEnd >= h.NewStart {
		to := min(len(fileLines), newEnd+e.context)
		for n := newEnd + 1; n <= to; n++ {
			fmt.Fprintf(&b, "  %5d  ctx| %s\n", n, fileLines[n-1])
		}
	}
	return b.String()
}

// renderRawHunks renders hunks without numbers/context (deleted files).
func (e *Enricher) renderRawHunks(f FileDiff) string {
	var b strings.Builder
	for _, h := range f.Hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
		for _, l := range h.Lines {
			marker := " "
			switch l.Kind {
			case LineAdded:
				marker = "+"
			case LineRemoved:
				marker = "-"
			}
			fmt.Fprintf(&b, "%s %s\n", marker, l.Content)
		}
	}
	return b.String()
}
