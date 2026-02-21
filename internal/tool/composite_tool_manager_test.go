package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// mockStateProvider implements domain.ToolStateProvider for testing.
type mockStateProvider struct {
	state string
}

func (m *mockStateProvider) GetToolState() string {
	return m.state
}

// mockToolManager is a simple tool manager for testing.
type mockToolManager struct {
	tools map[message.ToolName]message.Tool
}

func newMockToolManager(names ...string) *mockToolManager {
	m := &mockToolManager{tools: make(map[message.ToolName]message.Tool)}
	for _, name := range names {
		m.tools[message.ToolName(name)] = &webTool{
			name:        message.ToolName(name),
			description: message.ToolDescription("Description for " + name),
			handler: func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
				return message.NewToolResultText("ok"), nil
			},
		}
	}
	return m
}

func (m *mockToolManager) GetTools() map[message.ToolName]message.Tool { return m.tools }
func (m *mockToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	t, ok := m.tools[name]
	if !ok {
		return message.NewToolResultError("not found"), nil
	}
	return t.Handler()(ctx, args)
}
func (m *mockToolManager) RegisterTool(name message.ToolName, desc message.ToolDescription, args []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	m.tools[name] = &webTool{name: name, description: desc, handler: handler}
}

// mockStatefulToolManager is both a ToolManager and ToolStateProvider.
type mockStatefulToolManager struct {
	*mockToolManager
	*mockStateProvider
}

// Compile-time check that mockStatefulToolManager implements both interfaces.
var _ domain.ToolManager = (*mockStatefulToolManager)(nil)
var _ domain.ToolStateProvider = (*mockStatefulToolManager)(nil)

func TestCompositeToolManager_NoStateProviders(t *testing.T) {
	mgr := newMockToolManager("Read", "Write")
	composite := NewCompositeToolManager(mgr)

	tools := composite.GetTools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	// Descriptions should be unchanged.
	for _, tool := range tools {
		desc := tool.Description().String()
		if strings.Contains(desc, "(") {
			t.Errorf("expected no annotation in description, got: %s", desc)
		}
	}

	// GetToolState should return empty string when no providers.
	if s := composite.GetToolState(); s != "" {
		t.Errorf("expected empty state, got: %q", s)
	}
}

func TestCompositeToolManager_WithStateProvider(t *testing.T) {
	mgr := &mockStatefulToolManager{
		mockToolManager:   newMockToolManager("WebFetch", "WebFetchBlock", "Read"),
		mockStateProvider: &mockStateProvider{state: "Web cache: https://example.com (30s ago)"},
	}

	composite := NewCompositeToolManager(mgr)

	// Tool descriptions must remain static — no dynamic annotations.
	tools := composite.GetTools()
	wf := tools["WebFetch"]
	if wf == nil {
		t.Fatal("WebFetch not found")
	}
	desc := wf.Description().String()
	if strings.Contains(desc, "cached") {
		t.Errorf("tool description should be static, got: %s", desc)
	}
	if desc != "Description for WebFetch" {
		t.Errorf("expected original description, got: %s", desc)
	}

	// State is exposed via GetToolState, not in descriptions.
	state := composite.GetToolState()
	if !strings.Contains(state, "https://example.com") {
		t.Errorf("expected state to contain URL, got: %q", state)
	}
}

func TestCompositeToolManager_StateProviderDynamic(t *testing.T) {
	sp := &mockStateProvider{state: ""}
	mgr := &mockStatefulToolManager{
		mockToolManager:   newMockToolManager("WebFetch"),
		mockStateProvider: sp,
	}

	composite := NewCompositeToolManager(mgr)

	// First call: no state.
	if s := composite.GetToolState(); s != "" {
		t.Errorf("expected empty state initially, got: %q", s)
	}
	// Tool description is unchanged regardless of state.
	desc := composite.GetTools()["WebFetch"].Description().String()
	if strings.Contains(desc, "cached") {
		t.Errorf("tool description should not contain state: %s", desc)
	}

	// Update state dynamically.
	sp.state = "Web cache: https://test.com (10s ago)"

	// Second call: should now return state.
	s2 := composite.GetToolState()
	if !strings.Contains(s2, "https://test.com") {
		t.Errorf("expected updated state, got: %q", s2)
	}
	// Tool description must still be static.
	desc2 := composite.GetTools()["WebFetch"].Description().String()
	if strings.Contains(desc2, "cached") {
		t.Errorf("tool description should remain static after state update: %s", desc2)
	}
}

func TestCompositeToolManager_MultipleStateProviders(t *testing.T) {
	mgr1 := &mockStatefulToolManager{
		mockToolManager:   newMockToolManager("WebFetch"),
		mockStateProvider: &mockStateProvider{state: "Web cache: https://example.com (5s ago)"},
	}
	mgr2 := &mockStatefulToolManager{
		mockToolManager:   newMockToolManager("todo_write"),
		mockStateProvider: &mockStateProvider{state: "Todo list: 3 items (1 in_progress, 2 pending)"},
	}

	composite := NewCompositeToolManager(mgr1, mgr2)

	state := composite.GetToolState()
	if !strings.Contains(state, "Web cache") {
		t.Errorf("expected web cache state, got: %q", state)
	}
	if !strings.Contains(state, "Todo list") {
		t.Errorf("expected todo state, got: %q", state)
	}
}

func TestCompositeToolManager_CallToolUnaffectedByState(t *testing.T) {
	mgr := &mockStatefulToolManager{
		mockToolManager:   newMockToolManager("WebFetch"),
		mockStateProvider: &mockStateProvider{state: "Web cache: https://test.com (10s ago)"},
	}

	composite := NewCompositeToolManager(mgr)

	// CallTool should work normally regardless of state.
	result, err := composite.CallTool(context.Background(), "WebFetch", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Text != "ok" {
		t.Errorf("expected 'ok', got: %s", result.Text)
	}
}
