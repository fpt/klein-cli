package review

import (
	"context"
	"regexp"
	"strings"
)

// generatedMarker matches Go's standard "generated code" line. Per
// https://go.dev/s/generatedcode a file is generated when a line matching this
// regexp appears before the first non-comment, non-blank text (the package
// clause). Produced by protoc-gen-go, sqlc, stringer, mockgen, etc.
var generatedMarker = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// IsGenerated reports whether Go source content carries the generated-code
// marker before its package clause.
func IsGenerated(content string) bool {
	return isGeneratedLines(strings.Split(content, "\n"))
}

func isGeneratedLines(lines []string) bool {
	for _, line := range lines {
		// The marker must appear within the leading comment/blank block. A
		// build-tag comment (//go:build) or license header is fine; the first
		// real declaration (e.g. "package foo") ends the search.
		if !isCommentOrBlank(line) {
			return false
		}
		if generatedMarker.MatchString(line) {
			return true
		}
	}
	return false
}

func isCommentOrBlank(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*")
}

// DropGenerated removes files whose current (new-side) content is
// machine-generated, returning the files worth reviewing and the paths that
// were skipped. Files that can't be read (deleted/binary) are kept — there is
// nothing to misread, and a deleted file simply renders as non-commentable.
func (e *Enricher) DropGenerated(ctx context.Context, files []FileDiff) (kept []FileDiff, skipped []string) {
	for _, f := range files {
		if lines := e.readFileLines(ctx, f.Path); lines != nil && isGeneratedLines(lines) {
			skipped = append(skipped, f.Path)
			continue
		}
		kept = append(kept, f)
	}
	return kept, skipped
}
