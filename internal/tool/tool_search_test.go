package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

// fakeTool is a minimal message.Tool for tests.
type fakeTool struct {
	name message.ToolName
	desc string
}

func (t fakeTool) RawName() message.ToolName            { return t.name }
func (t fakeTool) Name() message.ToolName               { return t.name }
func (t fakeTool) Description() message.ToolDescription { return message.ToolDescription(t.desc) }
func (t fakeTool) Arguments() []message.ToolArgument    { return nil }
func (t fakeTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
		return message.NewToolResultText("ran " + string(t.name)), nil
	}
}

// fakeManager is a minimal domain.ToolManager backed by a map.
type fakeManager struct {
	tools map[message.ToolName]message.Tool
}

func newFakeManager(defs ...fakeTool) *fakeManager {
	m := &fakeManager{tools: map[message.ToolName]message.Tool{}}
	for _, d := range defs {
		m.tools[d.name] = d
	}
	return m
}
func (m *fakeManager) GetTool(n message.ToolName) (message.Tool, bool) {
	t, ok := m.tools[n]
	return t, ok
}
func (m *fakeManager) GetTools() map[message.ToolName]message.Tool { return m.tools }
func (m *fakeManager) CallTool(ctx context.Context, n message.ToolName, a message.ToolArgumentValues) (message.ToolResult, error) {
	if t, ok := m.tools[n]; ok {
		return t.Handler()(ctx, a)
	}
	return message.NewToolResultError("not found"), nil
}
func (m *fakeManager) RegisterTool(message.ToolName, message.ToolDescription, []message.ToolArgument, func(context.Context, message.ToolArgumentValues) (message.ToolResult, error)) {
}

func newTestDeferred() *DeferredToolManager {
	src := newFakeManager(
		fakeTool{"Read", "Read a file"},         // core
		fakeTool{"Bash", "Run a shell command"}, // core
		fakeTool{"WebFetch", "Fetch a webpage and extract content"},
		fakeTool{"WebSearch", "Search the web for information"},
		fakeTool{"MarketQuote", "Latest stock price and day change"},
		fakeTool{"ScheduleCreate", "Create a recurring scheduled message"},
	)
	return NewDeferredToolManager(src)
}

func TestDeferredInitialExposure(t *testing.T) {
	d := newTestDeferred()
	tools := d.GetTools()
	// Core + ToolSearch exposed; deferred hidden.
	for _, want := range []message.ToolName{"Read", "Bash", ToolSearchName} {
		if _, ok := tools[want]; !ok {
			t.Errorf("expected %s exposed initially", want)
		}
	}
	for _, hidden := range []message.ToolName{"WebFetch", "MarketQuote", "ScheduleCreate"} {
		if _, ok := tools[hidden]; ok {
			t.Errorf("expected %s deferred (hidden) initially", hidden)
		}
	}
}

func TestDeferredKeywordSearchActivates(t *testing.T) {
	d := newTestDeferred()
	res, _ := d.CallTool(context.Background(), ToolSearchName, message.ToolArgumentValues{"query": "web"})
	if res.Error != "" || !strings.Contains(res.Text, "WebFetch") || !strings.Contains(res.Text, "WebSearch") {
		t.Fatalf("search result: %q err %q", res.Text, res.Error)
	}
	tools := d.GetTools()
	if _, ok := tools["WebFetch"]; !ok {
		t.Error("WebFetch should be exposed after keyword search")
	}
	if _, ok := tools["MarketQuote"]; ok {
		t.Error("MarketQuote should still be deferred (didn't match 'web')")
	}
}

func TestDeferredSelectActivates(t *testing.T) {
	d := newTestDeferred()
	_, _ = d.CallTool(context.Background(), ToolSearchName, message.ToolArgumentValues{"query": "select:MarketQuote,ScheduleCreate"})
	tools := d.GetTools()
	if _, ok := tools["MarketQuote"]; !ok {
		t.Error("MarketQuote should be exposed after select")
	}
	if _, ok := tools["ScheduleCreate"]; !ok {
		t.Error("ScheduleCreate should be exposed after select")
	}
}

func TestDeferredCatalogHintShrinks(t *testing.T) {
	d := newTestDeferred()
	hint := d.CatalogHint()
	if !strings.Contains(hint, "WebFetch") || !strings.Contains(hint, "MarketQuote") {
		t.Errorf("hint should list deferred tools: %q", hint)
	}
	// After loading WebFetch, it drops out of the hint.
	_, _ = d.CallTool(context.Background(), ToolSearchName, message.ToolArgumentValues{"query": "select:WebFetch"})
	hint = d.CatalogHint()
	if strings.Contains(hint, "WebFetch") {
		t.Errorf("loaded tool should not be in hint: %q", hint)
	}
}

func TestDeferredDelegatesCall(t *testing.T) {
	d := newTestDeferred()
	// Core tool call delegates to source.
	res, _ := d.CallTool(context.Background(), "Read", nil)
	if res.Text != "ran Read" {
		t.Errorf("delegate failed: %q", res.Text)
	}
	// Unknown query returns a helpful message, not a crash.
	res, _ = d.CallTool(context.Background(), ToolSearchName, message.ToolArgumentValues{"query": "nonexistent-xyz"})
	if res.Error != "" || !strings.Contains(res.Text, "No tools matched") {
		t.Errorf("no-match: %q err %q", res.Text, res.Error)
	}
}
