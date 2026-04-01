package tool

import (
	"context"
	"strings"
	"testing"
)

// ---- checkInjection ----

func TestCheckInjection_CommandSubstitution(t *testing.T) {
	cases := []struct {
		cmd  string
		want string // substring expected in error; empty = expect nil
	}{
		{`git log --format=$(cat /etc/passwd)`, "command substitution $()"},
		{"echo `whoami`", "backtick substitution"},
		{`echo ${HOME}`, "parameter expansion ${}"},
		{`diff <(ls /tmp) <(ls /var)`, "process substitution <()"},
		{"bash 0<<EOF\nrm -rf /\nEOF", "heredoc substitution"},
	}
	for _, c := range cases {
		result := checkInjection(c.cmd)
		if c.want == "" {
			if result != nil {
				t.Errorf("checkInjection(%q): expected nil, got error %q", c.cmd, result.Error)
			}
		} else {
			if result == nil {
				t.Errorf("checkInjection(%q): expected block for %q, got nil", c.cmd, c.want)
				continue
			}
			if !strings.Contains(result.Error, c.want) {
				t.Errorf("checkInjection(%q): error %q does not mention %q", c.cmd, result.Error, c.want)
			}
			if !strings.Contains(result.Error, "SECURITY:") {
				t.Errorf("checkInjection(%q): error should start with SECURITY:", c.cmd)
			}
		}
	}
}

func TestCheckInjection_SafeCommands(t *testing.T) {
	safe := []string{
		"go build ./...",
		"git status",
		"ls -la",
		"go test ./internal/...",
		"make build",
		"echo hello world",
	}
	for _, cmd := range safe {
		if result := checkInjection(cmd); result != nil {
			t.Errorf("checkInjection(%q): safe command incorrectly blocked: %s", cmd, result.Error)
		}
	}
}

// ---- checkBuiltinDeny ----

func TestCheckBuiltinDeny_Blocked(t *testing.T) {
	cases := []struct {
		cmd    string
		reason string
	}{
		{"rm -rf /", "filesystem wipe"},
		{"rm -rf /*", "filesystem wipe"},
		{"  RM -RF /  ", "filesystem wipe"}, // trimmed + case-insensitive
		{":(){:|:&};:", "fork bomb"},
	}
	for _, c := range cases {
		result := checkBuiltinDeny(c.cmd)
		if result == nil {
			t.Errorf("checkBuiltinDeny(%q): expected block for %q, got nil", c.cmd, c.reason)
			continue
		}
		if !strings.Contains(result.Error, "SECURITY:") {
			t.Errorf("checkBuiltinDeny(%q): error should start with SECURITY:", c.cmd)
		}
		if !strings.Contains(result.Error, c.reason) {
			t.Errorf("checkBuiltinDeny(%q): error %q does not mention %q", c.cmd, result.Error, c.reason)
		}
	}
}

func TestCheckBuiltinDeny_AllowedCommands(t *testing.T) {
	allowed := []string{
		"rm -rf ./build",     // subdirectory, not root
		"rm -f file.txt",     // non-recursive
		"go build ./...",
		"git push origin main",
	}
	for _, cmd := range allowed {
		if result := checkBuiltinDeny(cmd); result != nil {
			t.Errorf("checkBuiltinDeny(%q): safe command incorrectly blocked: %s", cmd, result.Error)
		}
	}
}

// ---- integration: handleBash via BashToolManager ----

func newTestBashManager() *BashToolManager {
	return NewBashToolManager(BashConfig{WorkingDir: "/tmp"})
}

func TestBashManager_InjectionBlocked(t *testing.T) {
	m := newTestBashManager()
	result, err := m.CallTool(context.Background(), "Bash", map[string]any{
		"command": "git log --format=$(cat /etc/passwd)",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected injection to be blocked")
	}
	if !strings.Contains(result.Error, "SECURITY:") {
		t.Errorf("expected SECURITY: prefix, got: %s", result.Error)
	}
}

func TestBashManager_BuiltinDenyBlocked(t *testing.T) {
	m := newTestBashManager()
	result, err := m.CallTool(context.Background(), "Bash", map[string]any{
		"command": "rm -rf /",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected rm -rf / to be blocked")
	}
	if !strings.Contains(result.Error, "SECURITY:") {
		t.Errorf("expected SECURITY: prefix, got: %s", result.Error)
	}
}
