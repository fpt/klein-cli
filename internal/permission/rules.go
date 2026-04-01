// Package permission manages persistent allow/deny rules for tool approval.
//
// Rules are loaded from three JSON files in order of increasing priority:
//
//	~/.klein/permissions.json              (user-wide defaults)
//	{workingDir}/.klein/permissions.json   (project-specific, committable)
//	{workingDir}/.klein/permissions.local.json (project-local, gitignore this)
//
// Within each file rules are evaluated top-to-bottom; the first matching rule
// wins.  Higher-priority files are checked before lower-priority ones, so a
// local rule can override a user-wide rule.
package permission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// RuleBehavior is the outcome when a rule matches a tool call.
type RuleBehavior string

const (
	RuleAllow RuleBehavior = "allow"
	RuleDeny  RuleBehavior = "deny"
)

// PermissionRule describes a single allow or deny entry.
//
// Tool is the tool name (e.g. "Write", "Bash").
// Pattern is an optional glob matched against the tool's primary argument
// (file path for Write/Edit, command string for Bash).
// An empty Pattern matches every invocation of the tool.
//
// Pattern syntax:
//   - ""         — matches all calls to this tool
//   - "src/**"   — matches any path inside src/
//   - "go build *" — prefix wildcard: any string starting with "go build "
//   - "src/*.go" — standard glob (filepath.Match semantics)
type PermissionRule struct {
	Tool     string       `json:"tool"`
	Pattern  string       `json:"pattern,omitempty"`
	Behavior RuleBehavior `json:"behavior"`
}

// ruleFile is the on-disk JSON format.
type ruleFile struct {
	Rules []PermissionRule `json:"rules"`
}

// RuleSet is an ordered list of rules. Evaluation is first-match-wins.
type RuleSet struct {
	Rules []PermissionRule
}

// Check looks up toolName + arg against the rule set.
// Returns (behavior, true) for the first matching rule, or ("", false) if
// no rule matches (caller should fall through to the interactive dialog).
func (rs *RuleSet) Check(toolName, arg string) (RuleBehavior, bool) {
	if rs == nil {
		return "", false
	}
	for _, r := range rs.Rules {
		if r.Tool != toolName {
			continue
		}
		if matchPattern(r.Pattern, arg) {
			return r.Behavior, true
		}
	}
	return "", false
}

// matchPattern reports whether value matches pattern.
//
// Matching rules:
//   - ""         → always true (matches any value)
//   - "**"       → always true
//   - "foo/**"   → true when value == "foo" or strings.HasPrefix(value, "foo/")
//   - "prefix*"  → true when strings.HasPrefix(value, "prefix") (trailing * only)
//   - otherwise  → filepath.Match semantics
func matchPattern(pattern, value string) bool {
	if pattern == "" || pattern == "**" {
		return true
	}
	// Normalise separators so rules written with / work on all platforms.
	pattern = filepath.ToSlash(pattern)
	value = filepath.ToSlash(value)

	// "dir/**" — matches the dir itself and anything beneath it.
	if strings.HasSuffix(pattern, "/**") {
		base := strings.TrimSuffix(pattern, "/**")
		return value == base || strings.HasPrefix(value, base+"/")
	}

	// Simple trailing wildcard: "npm *", "go build *", "src/foo*".
	// Only applies when there is exactly one `*` and it is at the end.
	if strings.HasSuffix(pattern, "*") && strings.Count(pattern, "*") == 1 {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(value, prefix)
	}

	// Standard glob via filepath.Match (handles *, ?, [...]).
	matched, err := filepath.Match(pattern, value)
	if err != nil {
		// Malformed glob — fall back to exact match.
		return pattern == value
	}
	return matched
}

// LoadForProject loads and merges the three permission rule files for the
// given working directory.  Missing files are silently ignored.
// Higher-priority rules are placed first so Check finds them first.
func LoadForProject(workingDir string) *RuleSet {
	// Load each source; ignore errors (missing file is fine).
	userRules := loadFileSilent(userPermissionsPath())
	projectRules := loadFileSilent(filepath.Join(workingDir, ".klein", "permissions.json"))
	localRules := loadFileSilent(filepath.Join(workingDir, ".klein", "permissions.local.json"))

	// Merge: local (highest) → project → user (lowest).
	// first-match-wins evaluation means highest-priority first.
	merged := make([]PermissionRule, 0, len(localRules)+len(projectRules)+len(userRules))
	merged = append(merged, localRules...)
	merged = append(merged, projectRules...)
	merged = append(merged, userRules...)

	return &RuleSet{Rules: merged}
}

// userPermissionsPath returns the path to the user-wide permissions file.
func userPermissionsPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".klein", "permissions.json")
}

// AppendToProjectFile appends rule to {workingDir}/.klein/permissions.json,
// creating the file (and directory) if necessary.
func AppendToProjectFile(workingDir string, rule PermissionRule) error {
	path := filepath.Join(workingDir, ".klein", "permissions.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	existing := loadFileSilent(path)
	existing = append(existing, rule)
	data, err := json.MarshalIndent(ruleFile{Rules: existing}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

// loadFileSilent loads rules from a JSON file, returning nil on any error.
func loadFileSilent(path string) []PermissionRule {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil // file absent or unreadable — not an error
	}
	var f ruleFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil // malformed JSON — silent skip to avoid breaking the agent
	}
	return f.Rules
}
