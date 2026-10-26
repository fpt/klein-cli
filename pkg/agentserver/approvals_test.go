package agentserver

import (
	"context"
	"strings"
	"testing"

	"github.com/pmenglund/codex-sdk-go/protocol"
)

func strptr(s string) *string { return &s }

// stubApprover answers every request the same way and keeps the last one.
type stubApprover struct {
	seen  *ApprovalRequest
	allow bool
}

func (a stubApprover) Approve(_ context.Context, req ApprovalRequest) bool {
	if a.seen != nil {
		*a.seen = req
	}
	return a.allow
}

// TestApprovalNilApproverAccepts confirms a headless handler (no approver)
// auto-accepts command execution — the "never"/claw behavior.
func TestApprovalNilApproverAccepts(t *testing.T) {
	t.Parallel()
	h := &toolHandler{}
	resp, err := h.ItemCommandExecutionRequestApproval(context.Background(),
		protocol.CommandExecutionRequestApprovalParams{Command: strptr("ls")})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Decision.Kind() != protocol.CommandExecutionApprovalDecisionKindAccept {
		t.Errorf("nil approver should accept, got %q", resp.Decision.Kind())
	}
}

// TestApprovalApproverDecides confirms the approver's verdict maps to the
// decision, and that it receives a summary containing the command + cwd.
func TestApprovalApproverDecides(t *testing.T) {
	t.Parallel()
	var got ApprovalRequest
	deny := &toolHandler{approver: stubApprover{seen: &got}}
	resp, _ := deny.ItemCommandExecutionRequestApproval(context.Background(),
		protocol.CommandExecutionRequestApprovalParams{Command: strptr("rm -rf build"), Cwd: strptr("/tmp/x")})
	if resp.Decision.Kind() != protocol.CommandExecutionApprovalDecisionKindDecline {
		t.Errorf("declined approver should decline, got %q", resp.Decision.Kind())
	}
	if !strings.Contains(got.Summary, "rm -rf build") || !strings.Contains(got.Summary, "/tmp/x") {
		t.Errorf("summary missing command/cwd: %q", got.Summary)
	}

	allow := &toolHandler{approver: stubApprover{allow: true}}
	resp2, _ := allow.ItemCommandExecutionRequestApproval(context.Background(),
		protocol.CommandExecutionRequestApprovalParams{Command: strptr("ls")})
	if resp2.Decision.Kind() != protocol.CommandExecutionApprovalDecisionKindAccept {
		t.Errorf("approved approver should accept, got %q", resp2.Decision.Kind())
	}
}

// A writeStdin request names the running process, not the input bound for it, so
// it must not reach an allowlist as that process's command.
func TestApprovalWriteStdinWithholdsCommands(t *testing.T) {
	t.Parallel()
	var got ApprovalRequest
	h := &toolHandler{approver: stubApprover{seen: &got}}
	_, _ = h.ItemCommandExecutionRequestApproval(context.Background(),
		protocol.CommandExecutionRequestApprovalParams{
			Kind:           protocol.CommandExecutionApprovalKindWriteStdin,
			Command:        strptr("python3"),
			CommandActions: []interface{}{map[string]any{"type": "unknown", "command": "python3"}},
		})
	if len(got.Commands) != 0 {
		t.Errorf("writeStdin passed commands to the approver: %v", got.Commands)
	}
	if !strings.Contains(got.Summary, "stdin") || !strings.Contains(got.Summary, "python3") {
		t.Errorf("summary should say it is stdin for python3: %q", got.Summary)
	}
}
