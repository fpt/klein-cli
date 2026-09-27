package tool

import (
	"context"
	"fmt"
	"strings"

	"github.com/fpt/klein-cli/internal/review"
	"github.com/fpt/klein-cli/pkg/message"
)

// fullDiffTruncated marks a ReadFullDiff answer cut to the byte budget.
const fullDiffTruncated = "... [truncated — Read the file on disk for the rest]\n"

// WithFullDiff registers ReadFullDiff over the complete PR diff (base...head).
// Only an incremental round needs it: its prompt shows the changes since the
// last round, and without this a reviewer that meets code outside that
// increment cannot tell "added earlier in this PR" from "was always there" —
// or, worse, concludes a helper the PR added two commits ago does not exist.
// maxBytes bounds one answer (0 = unbounded). Returns the receiver for chaining.
func (m *ReviewToolManager) WithFullDiff(files []review.FileDiff, maxBytes int) *ReviewToolManager {
	m.RegisterTool("ReadFullDiff",
		"Read the complete pull request diff (all commits, base...head), not just the increment shown in the prompt. "+
			"Without path: lists every file the PR changes. With path: that file's full PR diff, "+
			"new-side line numbers in brackets. Use it before judging code that is outside the incremental diff — "+
			"it may have been added by an earlier commit of this PR.",
		[]message.ToolArgument{
			{
				Name: reviewArgPath, Required: false, Type: argTypeString,
				Description: "File path as listed (e.g. internal/foo.go); omit to list the changed files",
			},
		},
		func(_ context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
			return readFullDiff(files, strings.TrimSpace(stringArg(args, reviewArgPath)), maxBytes), nil
		})
	return m
}

func readFullDiff(files []review.FileDiff, path string, maxBytes int) message.ToolResult {
	if path == "" {
		return message.NewToolResultText(listFullDiffFiles(files))
	}
	for _, f := range files {
		if f.Path != path && f.OldPath != path {
			continue
		}
		out := fmt.Sprintf("## File: %s%s\n%s", f.Path, fileDiffStatus(f), review.RenderFileDiff(f))
		return message.NewToolResultText(strings.TrimRight(capFullDiff(out, maxBytes), "\n"))
	}
	// Name what would have worked: a wrong path is almost always a typo or a
	// file the PR does not touch, and both are answered by the list.
	return message.NewToolResultError(fmt.Sprintf(
		"%s is not changed by this pull request (generated files are excluded). Changed files:\n%s",
		path, listFullDiffFiles(files)))
}

// capFullDiff bounds out to maxBytes (0 = unbounded), marker included, cut on
// a line boundary so no line — or multi-byte rune — is split. A budget too
// small for the marker gets the bare line-boundary prefix.
func capFullDiff(out string, maxBytes int) string {
	if maxBytes <= 0 || len(out) <= maxBytes {
		return out
	}
	marker := fullDiffTruncated
	if maxBytes < len(marker) {
		marker = ""
	}
	budget := maxBytes - len(marker)
	return out[:strings.LastIndexByte(out[:budget], '\n')+1] + marker
}

// listFullDiffFiles lists the PR's files with their status and line counts.
func listFullDiffFiles(files []review.FileDiff) string {
	if len(files) == 0 {
		return "This pull request changes no reviewable files."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Files changed by this pull request (%d):\n", len(files))
	for _, f := range files {
		added, removed := 0, 0
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				switch l.Kind {
				case review.LineAdded:
					added++
				case review.LineRemoved:
					removed++
				}
			}
		}
		fmt.Fprintf(&b, "- %s%s +%d -%d\n", f.Path, fileDiffStatus(f), added, removed)
	}
	return strings.TrimRight(b.String(), "\n")
}

func fileDiffStatus(f review.FileDiff) string {
	switch {
	case f.IsNew:
		return " (new file)"
	case f.IsDeleted:
		return " (deleted)"
	case f.OldPath != "" && f.OldPath != f.Path:
		return fmt.Sprintf(" (renamed from %s)", f.OldPath)
	}
	return ""
}
