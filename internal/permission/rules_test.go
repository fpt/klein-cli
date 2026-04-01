package permission

import (
	"os"
	"path/filepath"
	"testing"
)

// ---- matchPattern ----

func TestMatchPattern_EmptyMatchesAll(t *testing.T) {
	for _, v := range []string{"", "src/main.go", "go build ./..."} {
		if !matchPattern("", v) {
			t.Errorf("empty pattern should match %q", v)
		}
	}
}

func TestMatchPattern_GlobStarMatchesAll(t *testing.T) {
	if !matchPattern("**", "anything/nested/deeply") {
		t.Error("** should match any value")
	}
}

func TestMatchPattern_DirGlobStar(t *testing.T) {
	cases := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"src/**", "src", true},
		{"src/**", "src/main.go", true},
		{"src/**", "src/pkg/foo.go", true},
		{"src/**", "other/main.go", false},
		{"src/**", "srcextra/main.go", false},
	}
	for _, c := range cases {
		got := matchPattern(c.pattern, c.value)
		if got != c.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", c.pattern, c.value, got, c.want)
		}
	}
}

func TestMatchPattern_TrailingWildcard(t *testing.T) {
	cases := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"go build *", "go build ./...", true},
		{"go build *", "go build -v .", true},
		{"go build *", "go test ./...", false},
		{"npm *", "npm install", true},
		{"npm *", "npm run build", true},
		{"npm *", "npx something", false},
	}
	for _, c := range cases {
		got := matchPattern(c.pattern, c.value)
		if got != c.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", c.pattern, c.value, got, c.want)
		}
	}
}

func TestMatchPattern_StandardGlob(t *testing.T) {
	cases := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"src/*.go", "src/main.go", true},
		{"src/*.go", "src/sub/main.go", false}, // * doesn't cross /
		{"*.json", "config.json", true},
		{"*.json", "dir/config.json", false},
		{"src/main.go", "src/main.go", true},
		{"src/main.go", "src/other.go", false},
	}
	for _, c := range cases {
		got := matchPattern(c.pattern, c.value)
		if got != c.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", c.pattern, c.value, got, c.want)
		}
	}
}

// ---- RuleSet.Check ----

func TestRuleSet_Check_NoRules(t *testing.T) {
	rs := &RuleSet{}
	_, matched := rs.Check("Write", "src/main.go")
	if matched {
		t.Error("empty rule set should not match")
	}
}

func TestRuleSet_Check_NilRuleSet(t *testing.T) {
	var rs *RuleSet
	_, matched := rs.Check("Write", "src/main.go")
	if matched {
		t.Error("nil rule set should not match")
	}
}

func TestRuleSet_Check_ToolMismatch(t *testing.T) {
	rs := &RuleSet{Rules: []PermissionRule{
		{Tool: "Bash", Pattern: "", Behavior: RuleAllow},
	}}
	_, matched := rs.Check("Write", "src/main.go")
	if matched {
		t.Error("tool mismatch should not match")
	}
}

func TestRuleSet_Check_AllowRule(t *testing.T) {
	rs := &RuleSet{Rules: []PermissionRule{
		{Tool: "Write", Pattern: "src/**", Behavior: RuleAllow},
	}}
	behavior, matched := rs.Check("Write", "src/main.go")
	if !matched {
		t.Fatal("expected match")
	}
	if behavior != RuleAllow {
		t.Errorf("expected RuleAllow, got %q", behavior)
	}
}

func TestRuleSet_Check_DenyRule(t *testing.T) {
	rs := &RuleSet{Rules: []PermissionRule{
		{Tool: "Bash", Pattern: "rm -rf *", Behavior: RuleDeny},
	}}
	behavior, matched := rs.Check("Bash", "rm -rf /")
	if !matched {
		t.Fatal("expected match")
	}
	if behavior != RuleDeny {
		t.Errorf("expected RuleDeny, got %q", behavior)
	}
}

func TestRuleSet_Check_FirstMatchWins(t *testing.T) {
	// deny comes first in the list → should win even though allow follows
	rs := &RuleSet{Rules: []PermissionRule{
		{Tool: "Write", Pattern: "src/**", Behavior: RuleDeny},
		{Tool: "Write", Pattern: "src/**", Behavior: RuleAllow},
	}}
	behavior, matched := rs.Check("Write", "src/main.go")
	if !matched {
		t.Fatal("expected match")
	}
	if behavior != RuleDeny {
		t.Errorf("first-match-wins: expected RuleDeny, got %q", behavior)
	}
}

func TestRuleSet_Check_EmptyPattern_MatchesAll(t *testing.T) {
	rs := &RuleSet{Rules: []PermissionRule{
		{Tool: "Write", Pattern: "", Behavior: RuleAllow},
	}}
	for _, path := range []string{"anything.go", "deep/nested/file.txt"} {
		b, ok := rs.Check("Write", path)
		if !ok || b != RuleAllow {
			t.Errorf("empty pattern should match %q", path)
		}
	}
}

// ---- LoadForProject ----

func TestLoadForProject_MissingFiles(t *testing.T) {
	// Should not panic or error when no files exist.
	rs := LoadForProject(t.TempDir())
	if rs == nil {
		t.Fatal("LoadForProject should never return nil")
	}
	if len(rs.Rules) != 0 {
		t.Errorf("expected 0 rules for empty dir, got %d", len(rs.Rules))
	}
}

func writeJSON(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestLoadForProject_PriorityOrder(t *testing.T) {
	dir := t.TempDir()
	kleinDir := filepath.Join(dir, ".klein")

	// Project file: deny Write in src/
	writeJSON(t, filepath.Join(kleinDir, "permissions.json"), `{
		"rules": [{"tool":"Write","pattern":"src/**","behavior":"deny"}]
	}`)
	// Local file: allow Write in src/ (overrides project deny)
	writeJSON(t, filepath.Join(kleinDir, "permissions.local.json"), `{
		"rules": [{"tool":"Write","pattern":"src/**","behavior":"allow"}]
	}`)

	rs := LoadForProject(dir)
	behavior, matched := rs.Check("Write", "src/main.go")
	if !matched {
		t.Fatal("expected a match")
	}
	// local (allow) should win over project (deny)
	if behavior != RuleAllow {
		t.Errorf("local rule should override project rule: expected allow, got %q", behavior)
	}
}

func TestLoadForProject_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, ".klein", "permissions.json"), `not valid json`)
	// Should silently ignore the bad file.
	rs := LoadForProject(dir)
	if len(rs.Rules) != 0 {
		t.Errorf("malformed JSON should produce 0 rules, got %d", len(rs.Rules))
	}
}

func TestLoadForProject_MergesAllFiles(t *testing.T) {
	dir := t.TempDir()
	kleinDir := filepath.Join(dir, ".klein")

	writeJSON(t, filepath.Join(kleinDir, "permissions.json"), `{
		"rules": [{"tool":"Bash","pattern":"go build *","behavior":"allow"}]
	}`)
	writeJSON(t, filepath.Join(kleinDir, "permissions.local.json"), `{
		"rules": [{"tool":"Write","pattern":"src/**","behavior":"allow"}]
	}`)

	rs := LoadForProject(dir)
	if len(rs.Rules) != 2 {
		t.Errorf("expected 2 merged rules, got %d", len(rs.Rules))
	}
	// Both tools should be findable.
	if b, ok := rs.Check("Bash", "go build ./..."); !ok || b != RuleAllow {
		t.Error("Bash rule not found after merge")
	}
	if b, ok := rs.Check("Write", "src/main.go"); !ok || b != RuleAllow {
		t.Error("Write rule not found after merge")
	}
}

// ---- AppendToProjectFile ----

func TestAppendToProjectFile_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	rule := PermissionRule{Tool: "Write", Pattern: "src/**", Behavior: RuleAllow}
	if err := AppendToProjectFile(dir, rule); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rs := LoadForProject(dir)
	if b, ok := rs.Check("Write", "src/main.go"); !ok || b != RuleAllow {
		t.Error("saved rule not found after reload")
	}
}

func TestAppendToProjectFile_Accumulates(t *testing.T) {
	dir := t.TempDir()
	r1 := PermissionRule{Tool: "Write", Pattern: "src/**", Behavior: RuleAllow}
	r2 := PermissionRule{Tool: "Bash", Pattern: "go build *", Behavior: RuleAllow}
	if err := AppendToProjectFile(dir, r1); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := AppendToProjectFile(dir, r2); err != nil {
		t.Fatalf("second append: %v", err)
	}
	rs := LoadForProject(dir)
	if len(rs.Rules) != 2 {
		t.Errorf("expected 2 rules, got %d", len(rs.Rules))
	}
}

func TestAppendToProjectFile_ExistingFilePreserved(t *testing.T) {
	dir := t.TempDir()
	kleinDir := filepath.Join(dir, ".klein")
	writeJSON(t, filepath.Join(kleinDir, "permissions.json"), `{
		"rules": [{"tool":"Edit","pattern":"","behavior":"allow"}]
	}`)
	rule := PermissionRule{Tool: "Write", Pattern: "src/**", Behavior: RuleAllow}
	if err := AppendToProjectFile(dir, rule); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rs := LoadForProject(dir)
	if len(rs.Rules) != 2 {
		t.Errorf("expected 2 rules after append, got %d", len(rs.Rules))
	}
	if _, ok := rs.Check("Edit", "any"); !ok {
		t.Error("original Edit rule was lost")
	}
	if _, ok := rs.Check("Write", "src/foo.go"); !ok {
		t.Error("new Write rule not found")
	}
}
