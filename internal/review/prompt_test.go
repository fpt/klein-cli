package review

import (
	"strings"
	"testing"
)

// A diff carrying Harmony control tokens must reach the model with the pipes
// rewritten, plus the note explaining the substitution — otherwise OpenAI
// rejects the request with 400 invalid_prompt before any review happens.
func TestBuildPromptSanitizesControlTokens(t *testing.T) {
	t.Parallel()

	req := Request{Title: "t", Diff: "d"}
	got := BuildPrompt(req, "[  29] +  | //! <|channel|>analysis<|message|>R<|end|>", "en")

	if strings.Contains(got, "<|channel|>analysis") {
		t.Error("prompt still contains the blocked token sequence")
	}
	if !strings.Contains(got, "<｜channel｜>analysis") {
		t.Error("prompt lost the token; expected a fullwidth-pipe rewrite")
	}
	if !strings.Contains(got, "Note on notation") {
		t.Error("prompt does not explain the substitution to the model")
	}
}

// Diffs without control tokens must not carry the note; it would invite the
// model to look for substitutions that aren't there.
func TestBuildPromptOmitsNoteWhenNothingRewritten(t *testing.T) {
	t.Parallel()

	got := BuildPrompt(Request{Title: "t", Diff: "d"}, "[  1] +  | func main() {}", "en")
	if strings.Contains(got, "Note on notation") {
		t.Error("note added to a prompt that needed no sanitizing")
	}
}
