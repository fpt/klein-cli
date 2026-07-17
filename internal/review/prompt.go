package review

import (
	"fmt"
	"strings"
)

// Request is the JSON contract between the harness (GHA) and `klein review`.
type Request struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Diff  string `json:"diff"`
}

// BuildPrompt assembles the user prompt for the review skill: PR metadata,
// the enriched diff, and the concrete task instructions. The review policy
// itself (what to look for, tone) lives in the review skill's SKILL.md.
// language is a code or name ("en", "ja", "Japanese", …); empty means "en".
func BuildPrompt(req Request, enrichedDiff, language string) string {
	var b strings.Builder
	b.WriteString("Review the following pull request.\n\n")
	fmt.Fprintf(&b, "# PR Title\n%s\n\n", strings.TrimSpace(req.Title))
	if body := strings.TrimSpace(req.Body); body != "" {
		fmt.Fprintf(&b, "# PR Description\n%s\n\n", body)
	}
	b.WriteString("# Changed Files (annotated diff)\n")
	b.WriteString("Lines shown as `[  123] +` or `[  123]  ` are part of the diff and commentable; ")
	b.WriteString("lines marked `ctx` are surrounding context for understanding only and MUST NOT be comment targets.\n\n")
	b.WriteString(enrichedDiff)
	b.WriteString("\n# Your Task\n")
	b.WriteString("1. Understand the change; Read surrounding code to verify each suspicion before reporting it.\n")
	b.WriteString("2. Report each verified finding with AddInlineReview (path + the bracketed line number).\n")
	b.WriteString("3. Write the overall assessment with AddSummaryReview.\n")
	b.WriteString("4. Call FinalizeReview to complete the review.\n")
	if language = strings.TrimSpace(language); language == "" {
		language = "en"
	}
	fmt.Fprintf(&b, "\nWrite the summary and all inline review comments in this language: %s\n", language)
	return b.String()
}
