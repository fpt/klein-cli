package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

// mockAnnotator implements domain.ToolAnnotator for testing.
type mockAnnotator struct {
	annotations map[message.ToolName]string
}

func (m *mockAnnotator) AnnotateTools() map[message.ToolName]string {
	return m.annotations
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

// mockAnnotatingToolManager is both a ToolManager and ToolAnnotator.
type mockAnnotatingToolManager struct {
	*mockToolManager
	*mockAnnotator
}

func TestCompositeToolManager_NoAnnotators(t *testing.T) {
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
}

func TestCompositeToolManager_WithAnnotator(t *testing.T) {
	mgr := &mockAnnotatingToolManager{
		mockToolManager: newMockToolManager("WebFetch", "WebFetchBlock", "Read"),
		mockAnnotator: &mockAnnotator{
			annotations: map[message.ToolName]string{
				"WebFetch":      "cached: https://example.com 30s ago",
				"WebFetchBlock": "cached: https://example.com 30s ago",
			},
		},
	}

	composite := NewCompositeToolManager(mgr)
	tools := composite.GetTools()

	// WebFetch should have annotation.
	wf := tools["WebFetch"]
	if wf == nil {
		t.Fatal("WebFetch not found")
	}
	desc := wf.Description().String()
	if !strings.Contains(desc, "cached: https://example.com 30s ago") {
		t.Errorf("expected annotation in WebFetch description, got: %s", desc)
	}

	// Read should NOT have annotation.
	rd := tools["Read"]
	if rd == nil {
		t.Fatal("Read not found")
	}
	rdDesc := rd.Description().String()
	if strings.Contains(rdDesc, "cached") {
		t.Errorf("Read should not be annotated, got: %s", rdDesc)
	}
}

func TestCompositeToolManager_AnnotatorDynamic(t *testing.T) {
	ann := &mockAnnotator{annotations: nil}
	mgr := &mockAnnotatingToolManager{
		mockToolManager: newMockToolManager("WebFetch"),
		mockAnnotator:   ann,
	}

	composite := NewCompositeToolManager(mgr)

	// First call: no annotations.
	tools1 := composite.GetTools()
	desc1 := tools1["WebFetch"].Description().String()
	if strings.Contains(desc1, "cached") {
		t.Errorf("expected no annotation initially, got: %s", desc1)
	}

	// Change annotations dynamically.
	ann.annotations = map[message.ToolName]string{
		"WebFetch": "cached: https://test.com 10s ago",
	}

	// Second call: should now have annotation.
	tools2 := composite.GetTools()
	desc2 := tools2["WebFetch"].Description().String()
	if !strings.Contains(desc2, "cached: https://test.com 10s ago") {
		t.Errorf("expected dynamic annotation, got: %s", desc2)
	}
}

func TestCompositeToolManager_CallToolUnaffectedByAnnotation(t *testing.T) {
	mgr := &mockAnnotatingToolManager{
		mockToolManager: newMockToolManager("WebFetch"),
		mockAnnotator: &mockAnnotator{
			annotations: map[message.ToolName]string{
				"WebFetch": "cached: test",
			},
		},
	}

	composite := NewCompositeToolManager(mgr)

	// CallTool should still work (uses toolsMap, not annotated wrapper).
	result, err := composite.CallTool(context.Background(), "WebFetch", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Text != "ok" {
		t.Errorf("expected 'ok', got: %s", result.Text)
	}
}
