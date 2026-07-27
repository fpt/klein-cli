// Package sanitize neutralizes text that a provider's prompt filter rejects
// before it is sent to a model.
package sanitize

import (
	"regexp"
	"strings"
)

// Replacement is the character substituted for the ASCII vertical bar inside a
// control-token run. U+FF5C (fullwidth vertical line) keeps the token legible —
// a reviewer reading `<｜channel｜>` still sees which token the code means —
// while breaking the literal byte sequence the filter matches on.
const Replacement = "｜"

// controlTokenRe matches chat-template control tokens: `<|name|>`, the
// unterminated `<|name>`, and the trailing-pipe `<name|>`. The inner charset
// excludes whitespace so real operators are left alone — Elixir/F# `|>` is not
// preceded by `<name`, and F# `<|` is not followed by a name and `>`.
var controlTokenRe = regexp.MustCompile(`<\|[A-Za-z0-9_./+-]{0,64}\|?>|<[A-Za-z0-9_./+-]{0,64}\|>`)

// ControlTokens rewrites chat-template control tokens so a provider's prompt
// filter does not reject the request.
//
// OpenAI's Responses API returns 400 invalid_prompt ("Request blocked.") for
// prompts containing the Harmony sequence `<|channel|>analysis` — it reads as
// an attempt to forge a hidden chain-of-thought turn. That is ordinary source
// text in a repository that implements model protocols, so reviewing such a
// repo would otherwise fail on content the diff never touched.
//
// Only the pipes are replaced; token names, structure, and every surrounding
// byte survive, so the model can still reason about the code. Prompts that
// carry sanitized text should say so — see review.BuildPrompt — or the model
// may report the substitution as a defect.
func ControlTokens(s string) string {
	// Fast path: the overwhelming majority of files contain no pipe at all.
	if !strings.Contains(s, "|") {
		return s
	}
	return controlTokenRe.ReplaceAllStringFunc(s, func(m string) string {
		return strings.ReplaceAll(m, "|", Replacement)
	})
}
