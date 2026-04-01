package tool_test

import (
	"context"
	"testing"

	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/pkg/message"
)

func TestEnterPlanMode_SetsActiveState(t *testing.T) {
	state := new(tool.PlanModeState)
	m := tool.NewPlanToolManager(state)

	result, err := m.CallTool(context.Background(), "EnterPlanMode", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected tool error: %s", result.Error)
	}
	if *state != tool.PlanModeActive {
		t.Errorf("expected PlanModeActive (%d), got %d", tool.PlanModeActive, *state)
	}
	if result.Text == "" {
		t.Error("expected non-empty result text")
	}
}

func TestExitPlanMode_AutoApproves_NonInteractive(t *testing.T) {
	state := new(tool.PlanModeState)
	m := tool.NewPlanToolManager(state)
	// No approval handler set → auto-approve

	args := message.ToolArgumentValues{"plan": "Step 1: do something\nStep 2: do more"}
	result, err := m.CallTool(context.Background(), "ExitPlanMode", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected tool error: %s", result.Error)
	}
	if *state != tool.PlanModeApproved {
		t.Errorf("expected PlanModeApproved (%d), got %d", tool.PlanModeApproved, *state)
	}
}

func TestExitPlanMode_CallsApprovalHandler_Approved(t *testing.T) {
	state := new(tool.PlanModeState)
	m := tool.NewPlanToolManager(state)

	handlerCalled := false
	m.SetApprovalHandler(func(plan string) (bool, bool, error) {
		handlerCalled = true
		return true, false, nil
	})

	args := message.ToolArgumentValues{"plan": "My plan"}
	result, err := m.CallTool(context.Background(), "ExitPlanMode", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected tool error: %s", result.Error)
	}
	if !handlerCalled {
		t.Error("expected approval handler to be called")
	}
	if *state != tool.PlanModeApproved {
		t.Errorf("expected PlanModeApproved (%d), got %d", tool.PlanModeApproved, *state)
	}
}

func TestExitPlanMode_CallsApprovalHandler_Rejected(t *testing.T) {
	state := new(tool.PlanModeState)
	m := tool.NewPlanToolManager(state)

	m.SetApprovalHandler(func(plan string) (bool, bool, error) {
		return false, false, nil
	})

	args := message.ToolArgumentValues{"plan": "My plan"}
	result, err := m.CallTool(context.Background(), "ExitPlanMode", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected tool error: %s", result.Error)
	}
	if *state != tool.PlanModeOff {
		t.Errorf("expected PlanModeOff (%d) after rejection, got %d", tool.PlanModeOff, *state)
	}
}

func TestExitPlanMode_ClearContext_CalledOnApprove(t *testing.T) {
	state := new(tool.PlanModeState)
	m := tool.NewPlanToolManager(state)

	clearCalled := false
	m.SetClearContextHandler(func() { clearCalled = true })
	m.SetApprovalHandler(func(plan string) (bool, bool, error) {
		return true, true, nil // approved + clear
	})

	args := message.ToolArgumentValues{"plan": "My plan"}
	_, err := m.CallTool(context.Background(), "ExitPlanMode", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !clearCalled {
		t.Error("expected clearContextHandler to be called when clearContext=true")
	}
	if *state != tool.PlanModeApproved {
		t.Errorf("expected PlanModeApproved, got %d", *state)
	}
}

func TestExitPlanMode_ClearContext_NotCalledOnApproveWithoutClear(t *testing.T) {
	state := new(tool.PlanModeState)
	m := tool.NewPlanToolManager(state)

	clearCalled := false
	m.SetClearContextHandler(func() { clearCalled = true })
	m.SetApprovalHandler(func(plan string) (bool, bool, error) {
		return true, false, nil // approved, no clear
	})

	args := message.ToolArgumentValues{"plan": "My plan"}
	_, err := m.CallTool(context.Background(), "ExitPlanMode", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if clearCalled {
		t.Error("clearContextHandler must NOT be called when clearContext=false")
	}
}

// mockToolManager is a minimal ToolManager for testing the guard.
type mockToolManager struct {
	callCount map[message.ToolName]int
}

func newMockToolManager() *mockToolManager {
	return &mockToolManager{callCount: make(map[message.ToolName]int)}
}

func (m *mockToolManager) GetTool(name message.ToolName) (message.Tool, bool) { return nil, false }
func (m *mockToolManager) GetTools() map[message.ToolName]message.Tool {
	return map[message.ToolName]message.Tool{}
}
func (m *mockToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	m.callCount[name]++
	return message.ToolResult{Text: "ok"}, nil
}
func (m *mockToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
}

func TestPlanModeGuard_BlocksWrite(t *testing.T) {
	state := new(tool.PlanModeState)
	*state = tool.PlanModeActive

	inner := newMockToolManager()
	guard := tool.NewPlanModeGuard(inner, state)

	result, err := guard.CallTool(context.Background(), "Write", message.ToolArgumentValues{"path": "foo.go", "content": "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected a plan mode block error for Write")
	}
	if inner.callCount["Write"] != 0 {
		t.Error("inner tool should not have been called")
	}
}

func TestPlanModeGuard_BlocksBash_WriteCmd(t *testing.T) {
	state := new(tool.PlanModeState)
	*state = tool.PlanModeActive

	inner := newMockToolManager()
	guard := tool.NewPlanModeGuard(inner, state)

	args := message.ToolArgumentValues{"command": "rm -rf /tmp/test"}
	result, err := guard.CallTool(context.Background(), "Bash", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected a plan mode block error for destructive Bash command")
	}
}

func TestPlanModeGuard_AllowsRead(t *testing.T) {
	state := new(tool.PlanModeState)
	*state = tool.PlanModeActive

	inner := newMockToolManager()
	guard := tool.NewPlanModeGuard(inner, state)

	_, err := guard.CallTool(context.Background(), "Read", message.ToolArgumentValues{"file_path": "foo.go"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.callCount["Read"] != 1 {
		t.Errorf("expected inner Read to be called once, got %d", inner.callCount["Read"])
	}
}

func TestPlanModeGuard_AllowsWriteWhenOff(t *testing.T) {
	state := new(tool.PlanModeState)
	*state = tool.PlanModeOff

	inner := newMockToolManager()
	guard := tool.NewPlanModeGuard(inner, state)

	result, err := guard.CallTool(context.Background(), "Write", message.ToolArgumentValues{"path": "foo.go"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Errorf("expected no error when plan mode is off, got: %s", result.Error)
	}
	if inner.callCount["Write"] != 1 {
		t.Errorf("expected inner Write to be called once, got %d", inner.callCount["Write"])
	}
}

func TestPlanModeGuard_AllowsBashReadOnly(t *testing.T) {
	state := new(tool.PlanModeState)
	*state = tool.PlanModeActive

	inner := newMockToolManager()
	guard := tool.NewPlanModeGuard(inner, state)

	args := message.ToolArgumentValues{"command": "cat foo.go"}
	_, err := guard.CallTool(context.Background(), "Bash", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.callCount["Bash"] != 1 {
		t.Errorf("expected inner Bash to be called once for read-only command, got %d", inner.callCount["Bash"])
	}
}
