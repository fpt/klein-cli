package agentserver

import (
	"context"
	"strings"
	"testing"

	"github.com/pmenglund/codex-sdk-go/protocol"
)

func strptr(s string) *string { return &s }

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
	if resp.Decision != decisionAccept {
		t.Errorf("nil approver should accept, got %q", resp.Decision)
	}
}

// TestApprovalApproverDecides confirms the approver's verdict maps to the
// decision, and that it receives a summary containing the command + cwd.
func TestApprovalApproverDecides(t *testing.T) {
	t.Parallel()
	var got ApprovalRequest
	deny := &toolHandler{approver: func(r ApprovalRequest) bool { got = r; return false }}
	resp, _ := deny.ItemCommandExecutionRequestApproval(context.Background(),
		protocol.CommandExecutionRequestApprovalParams{Command: strptr("rm -rf build"), Cwd: strptr("/tmp/x")})
	if resp.Decision != decisionDecline {
		t.Errorf("declined approver should decline, got %q", resp.Decision)
	}
	if !strings.Contains(got.Summary, "rm -rf build") || !strings.Contains(got.Summary, "/tmp/x") {
		t.Errorf("summary missing command/cwd: %q", got.Summary)
	}

	allow := &toolHandler{approver: func(ApprovalRequest) bool { return true }}
	resp2, _ := allow.ItemCommandExecutionRequestApproval(context.Background(),
		protocol.CommandExecutionRequestApprovalParams{Command: strptr("ls")})
	if resp2.Decision != decisionAccept {
		t.Errorf("approved approver should accept, got %q", resp2.Decision)
	}
}
