package plugin

import (
	"strings"
)

// splitFrontmatter splits a markdown file into its YAML frontmatter block
// (between leading "---" delimiters) and the remaining body. Returns
// (yamlBlock, body). If no frontmatter is present, yamlBlock is empty and
// the entire input is returned as body.
//
// This mirrors the parser in internal/skill.ParseSkillMD so plugin commands
// and agents accept the same file format as SKILL.md.
func splitFrontmatter(content string) (yamlBlock, body string) {
	trimmed := strings.TrimLeft(content, " \t\n\r")
	if !strings.HasPrefix(trimmed, "---") {
		return "", content
	}

	afterFirst := trimmed[3:]
	idx := strings.Index(afterFirst, "\n")
	if idx < 0 {
		// Only a "---" delimiter, no content after.
		return "", ""
	}
	afterFirst = afterFirst[idx+1:]

	closingIdx := strings.Index(afterFirst, "\n---")
	if closingIdx < 0 {
		// Unterminated frontmatter — treat as no frontmatter so we don't drop body.
		return "", content
	}

	yamlBlock = afterFirst[:closingIdx]

	rest := afterFirst[closingIdx+4:] // skip "\n---"
	nlIdx := strings.Index(rest, "\n")
	if nlIdx >= 0 {
		body = rest[nlIdx+1:]
	} else {
		body = ""
	}
	return yamlBlock, body
}

// parseStringList accepts a frontmatter list field that may be either:
//   - a YAML sequence (parsed into []string by yaml.v3 → []any of strings), or
//   - a comma-separated string ("Read, Write, Bash"), or
//   - a single string (treated as one entry).
//
// Returns a flat []string with whitespace trimmed and empty entries dropped.
func parseStringList(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		var out []string
		for _, part := range strings.Split(t, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	case []string:
		out := make([]string, 0, len(t))
		for _, s := range t {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
