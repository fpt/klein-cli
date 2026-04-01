package tool

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/fpt/klein-cli/pkg/message"
)

// injectionPattern pairs a compiled regex with a human-readable description.
type injectionPattern struct {
	re     *regexp.Regexp
	reason string
}

// injectionPatterns lists shell constructs that enable command injection.
// These are checked unconditionally before whitelist or approval logic.
var injectionPatterns = []injectionPattern{
	{regexp.MustCompile(`\$\(`), "command substitution $()"},
	{regexp.MustCompile("`"), "backtick substitution"},
	{regexp.MustCompile(`\$\{[^}]*\}`), "parameter expansion ${}"},
	{regexp.MustCompile(`<\(`), "process substitution <()"},
	{regexp.MustCompile(`[0-9]<<`), "heredoc substitution"},
}

// checkInjection returns a ToolResult error if the command contains a
// dangerous shell injection construct. Returns nil when the command is clean.
// This runs before any whitelist or approval check.
func checkInjection(command string) *message.ToolResult {
	for _, p := range injectionPatterns {
		if p.re.MatchString(command) {
			r := message.NewToolResultError(fmt.Sprintf(
				"SECURITY: Command blocked — contains %s.\n"+
					"If this is intentional, add an explicit allow rule in .klein/permissions.json.",
				p.reason,
			))
			return &r
		}
	}
	return nil
}

// builtinDenyRule is a hardcoded pattern that is always blocked, regardless
// of any user-configured allow rules.
type builtinDenyRule struct {
	substring string // matched case-insensitively anywhere in the command
	reason    string
}

// builtinDenyRules lists commands that must never be executed, even if a
// user allow rule would otherwise permit them.
var builtinDenyRules = []builtinDenyRule{
	{"rm -rf /", "rm -rf / (filesystem wipe)"},
	{"rm -rf /*", "rm -rf /* (filesystem wipe)"},
	{":(){:|:&};:", "fork bomb"},
}

// checkBuiltinDeny returns a ToolResult error if the command matches any
// hardcoded deny rule. These blocks cannot be overridden by configuration.
func checkBuiltinDeny(command string) *message.ToolResult {
	lower := strings.ToLower(command)
	for _, rule := range builtinDenyRules {
		if strings.Contains(lower, strings.ToLower(rule.substring)) {
			r := message.NewToolResultError(fmt.Sprintf(
				"SECURITY: Command blocked — matches deny rule: %s.",
				rule.reason,
			))
			return &r
		}
	}
	return nil
}
