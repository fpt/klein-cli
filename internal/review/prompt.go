package review

import (
	"fmt"
	"strings"

	"github.com/fpt/klein-cli/internal/sanitize"
)

// PreviousComment is one unresolved inline comment from an earlier review
// round, supplied by the harness. ID is opaque to klein (the harness uses
// GraphQL review-thread node ids) — it only round-trips through
// ResolveReviewComment into the result.
type PreviousComment struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Body string `json:"body"`
	Line int    `json:"line"`
}

// Request is the JSON contract between the harness (GHA) and `klein review`.
//
//nolint:tagliatelle // snake_case keys are the harness contract
type Request struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// Diff is the unified diff to review. For an incremental round the
	// harness passes only the changes since the last reviewed commit.
	Diff string `json:"diff"`
	// FullDiff optionally carries the complete PR diff (base...head) when
	// Diff is incremental. Commentable-line validation always uses the full
	// diff — GitHub rejects comments outside it. Empty = Diff is the full diff.
	FullDiff string `json:"full_diff,omitempty"`
	// Mode is "full" (default) or "incremental"; only affects prompt phrasing.
	Mode string `json:"mode,omitempty"`
	// PreviousComments are unresolved inline comments from earlier rounds.
	PreviousComments []PreviousComment `json:"previous_comments,omitempty"`
}

// BuildPrompt assembles the user prompt for the review skill: PR metadata,
// the enriched diff, previous-round comments, and the concrete task
// instructions. The review policy itself (what to look for, tone) lives in
// the review skill's SKILL.md.
// language is a code or name ("en", "ja", "Japanese", …); empty means "en".
func BuildPrompt(req Request, enrichedDiff, language string) string {
	var b strings.Builder
	b.WriteString("Review the following pull request.\n\n")
	fmt.Fprintf(&b, "# PR Title\n%s\n\n", strings.TrimSpace(req.Title))
	if body := strings.TrimSpace(req.Body); body != "" {
		fmt.Fprintf(&b, "# PR Description\n%s\n\n", body)
	}

	if req.Mode == "incremental" {
		b.WriteString("# Review Mode: incremental\n")
		b.WriteString("The diff below contains ONLY the changes since the last review round. ")
		b.WriteString("Earlier changes were already reviewed; the complete current code is on disk for Read.\n\n")
	}

	b.WriteString("# Changed Files (annotated diff)\n")
	b.WriteString("Lines shown as `[  123] +` or `[  123]  ` are part of the diff and commentable; ")
	b.WriteString("lines marked `ctx` are surrounding context for understanding only and MUST NOT be comment targets.\n\n")
	b.WriteString(enrichedDiff)

	writePreviousComments(&b, req.PreviousComments)

	b.WriteString("\n# Your Task\n")
	if len(req.PreviousComments) > 0 {
		b.WriteString("1. For each previous comment above: Read the current code at that location; ")
		b.WriteString("if the issue is fixed, call ResolveReviewComment with its id. ")
		b.WriteString("If it still applies, leave it open and do NOT re-post a duplicate.\n")
		b.WriteString("2. Understand the change; Read surrounding code to verify each new suspicion before reporting it.\n")
		b.WriteString("3. Report each verified NEW finding with AddInlineReview (path + the bracketed line number).\n")
		b.WriteString("4. Write the overall assessment with AddSummaryReview.\n")
		b.WriteString("5. Call FinalizeReview to complete the review.\n")
	} else {
		b.WriteString("1. Understand the change; Read surrounding code to verify each suspicion before reporting it.\n")
		b.WriteString("2. Report each verified finding with AddInlineReview (path + the bracketed line number).\n")
		b.WriteString("3. Write the overall assessment with AddSummaryReview.\n")
		b.WriteString("4. Call FinalizeReview to complete the review.\n")
	}
	if language = strings.TrimSpace(language); language == "" {
		language = "en"
	}
	fmt.Fprintf(&b, "\nWrite the summary and all inline review comments in this language: %s\n", language)

	out := sanitize.ControlTokens(b.String())
	// Only mention the rewrite when it actually happened, so the note doesn't
	// invite the model to hunt for substitutions in a diff that has none.
	if out != b.String() {
		out += controlTokenNote
	}
	return out
}

// controlTokenNote tells the model that control-token pipes were rewritten.
// Without it the reviewer sees `<｜channel｜>` in the code, believes the token
// is misspelled, and files a confident false positive.
const controlTokenNote = "\nNote on notation: chat-template control tokens in the diff and in files you Read " +
	"are shown with a fullwidth vertical bar (`<｜channel｜>`) instead of the ASCII `|`. " +
	"The real source uses ASCII; this substitution keeps the provider's prompt filter from " +
	"rejecting the request. Do NOT report it as a defect, and do not quote it as evidence " +
	"of one — treat `<｜…｜>` as if it read `<|…|>`.\n"

// writePreviousComments renders the unresolved comments from earlier rounds.
func writePreviousComments(b *strings.Builder, comments []PreviousComment) {
	if len(comments) == 0 {
		return
	}
	b.WriteString("\n# Previous Review Comments (unresolved, from earlier rounds)\n")
	b.WriteString("Verify each against the CURRENT code. Resolve fixed ones with ResolveReviewComment(id).\n\n")
	for _, c := range comments {
		fmt.Fprintf(b, "- id=%s %s:%d\n  %s\n", c.ID, c.Path, c.Line, indentBody(c.Body))
	}
}

// indentBody keeps multi-line comment bodies aligned under their list item.
func indentBody(body string) string {
	return strings.ReplaceAll(strings.TrimSpace(body), "\n", "\n  ")
}
