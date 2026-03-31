package app

import (
	"testing"

	"github.com/fpt/klein-cli/internal/permission"
)

// ---- inferPattern ----

func TestInferPattern_FileInSubdir(t *testing.T) {
	cases := []struct {
		tool, arg, want string
	}{
		{"write_file", "src/foo/bar.go", "src/**"},
		{"edit_file", "pkg/agent/react/react.go", "pkg/**"},
		{"multi_edit", "internal/tool/task.go", "internal/**"},
		{"write_file", "./src/main.go", "src/**"}, // strip leading ./
	}
	for _, c := range cases {
		got := inferPattern(c.tool, c.arg)
		if got != c.want {
			t.Errorf("inferPattern(%q, %q) = %q, want %q", c.tool, c.arg, got, c.want)
		}
	}
}

func TestInferPattern_RootLevelFile(t *testing.T) {
	cases := []struct {
		arg, want string
	}{
		{"main.go", "*.go"},
		{"README.md", "*.md"},
		{"Makefile", "*"}, // no extension
	}
	for _, c := range cases {
		got := inferPattern("write_file", c.arg)
		if got != c.want {
			t.Errorf("inferPattern(write_file, %q) = %q, want %q", c.arg, got, c.want)
		}
	}
}

func TestInferPattern_Bash(t *testing.T) {
	cases := []struct {
		arg, want string
	}{
		{"go build ./...", "go build *"},
		{"go test ./...", "go test *"},
		{"npm install", "npm install *"},
		{"make build-all", "make build-all *"},
		{"ls", "ls *"},
	}
	for _, c := range cases {
		got := inferPattern("bash", c.arg)
		if got != c.want {
			t.Errorf("inferPattern(bash, %q) = %q, want %q", c.arg, got, c.want)
		}
	}
}

func TestInferPattern_EmptyArg(t *testing.T) {
	for _, tool := range []string{"write_file", "bash", "multi_edit"} {
		if got := inferPattern(tool, ""); got != "*" {
			t.Errorf("inferPattern(%q, \"\") = %q, want *", tool, got)
		}
	}
}

func TestInferPattern_UnknownTool(t *testing.T) {
	if got := inferPattern("unknown_tool", "anything"); got != "*" {
		t.Errorf("expected * for unknown tool, got %q", got)
	}
}

// ---- session rules precedence ----

func TestSessionRules_AllowOverridesPrompt(t *testing.T) {
	// Build a RuleSet as session rules would look after "Yes, for src/**"
	rs := &permission.RuleSet{
		Rules: []permission.PermissionRule{
			{Tool: "write_file", Pattern: "src/**", Behavior: permission.RuleAllow},
		},
	}

	behavior, matched := rs.Check("write_file", "src/main.go")
	if !matched {
		t.Fatal("expected match for src/main.go against src/**")
	}
	if behavior != permission.RuleAllow {
		t.Errorf("expected allow, got %q", behavior)
	}

	// A path outside src/ should not match
	_, matched2 := rs.Check("write_file", "other/main.go")
	if matched2 {
		t.Error("src/** should not match other/main.go")
	}
}

func TestSessionRules_BlanketAllow(t *testing.T) {
	// "Always (this session)" adds empty-pattern rule for the specific tool
	rs := &permission.RuleSet{
		Rules: []permission.PermissionRule{
			{Tool: "write_file", Pattern: "", Behavior: permission.RuleAllow},
		},
	}

	for _, path := range []string{"src/main.go", "other/file.txt", "Makefile"} {
		b, ok := rs.Check("write_file", path)
		if !ok || b != permission.RuleAllow {
			t.Errorf("blanket allow should match %q", path)
		}
	}
	// A different tool must NOT match
	_, ok := rs.Check("bash", "rm -rf /")
	if ok {
		t.Error("write_file blanket allow must not cover bash")
	}
}

func TestNewSessionRules_NonInteractive(t *testing.T) {
	rs := newSessionRules(false)
	for _, tool := range []string{"write_file", "edit_file", "multi_edit", "bash"} {
		b, ok := rs.Check(tool, "anything")
		if !ok || b != permission.RuleAllow {
			t.Errorf("non-interactive: tool %q should be pre-approved", tool)
		}
	}
}

func TestNewSessionRules_Interactive(t *testing.T) {
	rs := newSessionRules(true)
	if len(rs.Rules) != 0 {
		t.Errorf("interactive: session rules should start empty, got %d rules", len(rs.Rules))
	}
}

func TestSessionRules_ToolScoped(t *testing.T) {
	// Each "Always (this session)" adds a rule for exactly the pending tool
	rs := &permission.RuleSet{
		Rules: []permission.PermissionRule{
			{Tool: "write_file", Pattern: "", Behavior: permission.RuleAllow},
			// bash NOT added
		},
	}
	_, bashMatched := rs.Check("bash", "go build ./...")
	if bashMatched {
		t.Error("bash should still require approval when only write_file was blanket-approved")
	}
}
