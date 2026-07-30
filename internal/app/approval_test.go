package app

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/fpt/klein-cli/internal/permission"
)

// ---- inferPattern ----

func TestInferPattern_FileInSubdir(t *testing.T) {
	cases := []struct {
		tool, arg, want string
	}{
		{"Write", "src/foo/bar.go", "src/**"},
		{"Edit", "pkg/agent/react/react.go", "pkg/**"},
		{"MultiEdit", "internal/tool/task.go", "internal/**"},
		{"Write", "./src/main.go", "src/**"}, // strip leading ./
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
		got := inferPattern("Write", c.arg)
		if got != c.want {
			t.Errorf("inferPattern(Write, %q) = %q, want %q", c.arg, got, c.want)
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
		got := inferPattern("Bash", c.arg)
		if got != c.want {
			t.Errorf("inferPattern(Bash, %q) = %q, want %q", c.arg, got, c.want)
		}
	}
}

func TestInferPattern_EmptyArg(t *testing.T) {
	for _, tool := range []string{"Write", "Bash", "MultiEdit"} {
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

// ---- describePendingToolCall ----

// The prompt must describe the call awaiting approval — the header used to be a
// hardcoded "About to write file(s)" followed by the *previous* tool result,
// which for an offloaded result showed a storage path instead of the target.
func TestDescribePendingToolCall(t *testing.T) {
	t.Parallel()
	cases := []struct {
		tool, arg string
		want      string
	}{
		{"Write", "src/main.go", "About to write file:\n   ↳ src/main.go"},
		{"Edit", "doc/README.md", "About to edit file:\n   ↳ doc/README.md"},
		{"MultiEdit", "a.go", "About to edit file:\n   ↳ a.go"},
		{"Bash", "go build ./...", "About to run command:\n   ↳ go build ./..."},
		{"Fetch", "https://example.com", "About to run Fetch:\n   ↳ https://example.com"},
		{"Write", "", "About to write file:"},
		{"", "", "About to run a tool (details unavailable):"},
	}
	for _, c := range cases {
		if got := describePendingToolCall(c.tool, c.arg); got != c.want {
			t.Errorf("describePendingToolCall(%q, %q) = %q, want %q", c.tool, c.arg, got, c.want)
		}
	}
}

func TestDescribePendingToolCall_MultibyteArgIsNotMangled(t *testing.T) {
	t.Parallel()
	arg := "cat " + strings.Repeat("研究資料", 100) + ".md"
	got := describePendingToolCall("Bash", arg)
	if !utf8.ValidString(got) {
		t.Error("description contains invalid UTF-8: argument was cut mid-rune")
	}
	if !strings.HasPrefix(got, "About to run command:\n   ↳ cat 研究資料") {
		t.Errorf("unexpected rendering: %q", got)
	}
}

// ---- session rules precedence ----

func TestSessionRules_AllowOverridesPrompt(t *testing.T) {
	// Build a RuleSet as permRules would look after "Always (save to project)" for src/**
	rs := &permission.RuleSet{
		Rules: []permission.PermissionRule{
			{Tool: "Write", Pattern: "src/**", Behavior: permission.RuleAllow},
		},
	}

	behavior, matched := rs.Check("Write", "src/main.go")
	if !matched {
		t.Fatal("expected match for src/main.go against src/**")
	}
	if behavior != permission.RuleAllow {
		t.Errorf("expected allow, got %q", behavior)
	}

	// A path outside src/ should not match
	_, matched2 := rs.Check("Write", "other/main.go")
	if matched2 {
		t.Error("src/** should not match other/main.go")
	}
}

func TestSessionRules_BlanketAllow(t *testing.T) {
	// "Always (save to project)" with empty pattern covers the specific tool broadly
	rs := &permission.RuleSet{
		Rules: []permission.PermissionRule{
			{Tool: "Write", Pattern: "", Behavior: permission.RuleAllow},
		},
	}

	for _, path := range []string{"src/main.go", "other/file.txt", "Makefile"} {
		b, ok := rs.Check("Write", path)
		if !ok || b != permission.RuleAllow {
			t.Errorf("blanket allow should match %q", path)
		}
	}
	// A different tool must NOT match
	_, ok := rs.Check("Bash", "rm -rf /")
	if ok {
		t.Error("Write blanket allow must not cover bash")
	}
}

func TestNewSessionRules_NonInteractive(t *testing.T) {
	rs := newSessionRules(false)
	for _, tool := range []string{"Write", "Edit", "MultiEdit", "Bash"} {
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
	// A saved rule covers exactly the tool it was created for
	rs := &permission.RuleSet{
		Rules: []permission.PermissionRule{
			{Tool: "Write", Pattern: "", Behavior: permission.RuleAllow},
			// Bash NOT added
		},
	}
	_, bashMatched := rs.Check("Bash", "go build ./...")
	if bashMatched {
		t.Error("bash should still require approval when only Write was blanket-approved")
	}
}
