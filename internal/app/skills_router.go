package app

import (
	"regexp"
	"strings"
)

var (
	reHTTPURL = regexp.MustCompile(`https?://\S+`)
	reGitHub  = regexp.MustCompile(`https?://github\.com/\S+`)
	rePDFExt  = regexp.MustCompile(`(?i)\b\S+\.pdf\b`)
)

// routerRule maps a prompt-matching predicate to a tool usage hint.
type routerRule struct {
	match       func(prompt string) bool
	hint        string
	skipIfSkill string // suppress this hint when the named skill is already active
}

// SkillsRouter inspects user prompts and returns a tool-usage hint to inject
// as the first situation message of a ReAct run. Rules are purely pattern-based
// (no LLM, no I/O) so the cost is negligible.
type SkillsRouter struct {
	rules []routerRule
}

// NewSkillsRouter returns a SkillsRouter with the default rule set.
func NewSkillsRouter() *SkillsRouter {
	return &SkillsRouter{rules: defaultRules()}
}

func defaultRules() []routerRule {
	return []routerRule{
		{
			// GitHub URL: route to github skill guidance
			match:       func(p string) bool { return reGitHub.MatchString(p) },
			skipIfSkill: "github",
			hint: "The prompt contains a GitHub URL. " +
				"Use get_github_content/search_github_code/tree_github_repo MCP tools when available; " +
				"use web_fetch with raw.githubusercontent.com for raw file access; " +
				"use the gh CLI (bash) for issues, PRs, workflows, and releases. " +
				"Avoid fetching github.com HTML pages with web_fetch. " +
				"The 'github' skill has comprehensive guidance if needed.",
		},
		{
			// Non-GitHub HTTP URL: nudge toward web_fetch
			match: func(p string) bool {
				return reHTTPURL.MatchString(p) && !reGitHub.MatchString(p)
			},
			hint: "The prompt contains a URL. " +
				"Use web_fetch to retrieve the page as markdown text. " +
				"If the intent is to search rather than fetch a specific page, use duckduckgo_search instead.",
		},
		{
			// PDF file reference: steer away from read_file
			match: func(p string) bool {
				lower := strings.ToLower(p)
				return rePDFExt.MatchString(p) || strings.Contains(lower, "pdf file") || strings.Contains(lower, "pdf document")
			},
			hint: "The prompt references a PDF. " +
				"Use pdf_info to inspect metadata and pdf_read to extract text content. " +
				"Do not use read_file on PDF files — it returns binary data.",
		},
	}
}

// Route inspects the prompt and returns a newline-joined set of hints (empty if none match).
// activeSkill is the name of the currently loaded skill; rules with a matching skipIfSkill
// are suppressed to avoid redundancy. Safe to call on a nil receiver.
func (r *SkillsRouter) Route(prompt, activeSkill string) string {
	if r == nil {
		return ""
	}
	var hints []string
	for _, rule := range r.rules {
		if rule.skipIfSkill != "" && rule.skipIfSkill == activeSkill {
			continue
		}
		if rule.match(prompt) {
			hints = append(hints, rule.hint)
		}
	}
	return strings.Join(hints, "\n")
}
