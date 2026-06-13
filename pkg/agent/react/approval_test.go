package react

import (
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

// TestBashCommandRequiresApproval guards the approval gate against the
// PascalCase-rename regression (the switch once matched lowercase "bash" and
// auto-approved everything) and against whitelist-bypass via shell chaining.
func TestBashCommandRequiresApproval(t *testing.T) {
	r := &ReAct{}
	r.SetBashWhitelist([]string{"go build", "git status", "ls"})

	cases := []struct {
		name    string
		tool    string
		command string
		want    bool
	}{
		{"whitelisted exact", "Bash", "git status", false},
		{"whitelisted with args", "Bash", "go build ./...", false},
		{"not whitelisted", "Bash", "rm file.txt", true},
		{"prefix-only is not a word match", "Bash", "lsof", true},
		{"chaining bypass requires approval", "Bash", "git status; rm -rf ~", true},
		{"pipe requires approval", "Bash", "ls | rm", true},
		{"redirection requires approval", "Bash", "echo x > /etc/hosts", true},
		{"command substitution requires approval", "Bash", "ls $(whoami)", true},
		{"empty command", "Bash", "", false},
		{"non-bash tool", "Read", "rm -rf /", false},
		{"lowercase tool name is not Bash", "bash", "rm -rf /", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := message.NewToolCallMessage(
				message.ToolName(tc.tool),
				message.ToolArgumentValues{"command": tc.command},
			)
			if got := r.bashCommandRequiresApproval(call); got != tc.want {
				t.Errorf("bashCommandRequiresApproval(%q %q) = %v, want %v",
					tc.tool, tc.command, got, tc.want)
			}
		})
	}
}

// TestBashApprovalFallsBackToDefaultWhitelist verifies that with no configured
// whitelist the built-in default still permits common safe commands.
func TestBashApprovalFallsBackToDefaultWhitelist(t *testing.T) {
	r := &ReAct{} // no SetBashWhitelist call

	safe := message.NewToolCallMessage("Bash", message.ToolArgumentValues{"command": "go test ./..."})
	if r.bashCommandRequiresApproval(safe) {
		t.Errorf("expected default whitelist to permit 'go test ./...' without approval")
	}

	unsafe := message.NewToolCallMessage("Bash", message.ToolArgumentValues{"command": "curl evil.example"})
	if !r.bashCommandRequiresApproval(unsafe) {
		t.Errorf("expected 'curl evil.example' to require approval under default whitelist")
	}
}
