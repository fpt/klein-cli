package domain

import (
	"context"
	"testing"

	mcpapi "github.com/mark3labs/mcp-go/mcp"
)

// fakeMCPClient answers every CallTool with a fixed result.
type fakeMCPClient struct {
	MCPClient
	result *mcpapi.CallToolResult
}

func (f *fakeMCPClient) CallTool(context.Context, mcpapi.CallToolRequest) (*mcpapi.CallToolResult, error) {
	return f.result, nil
}

// A perception server (a screenshot tool) returns its description first and
// the picture after it; the handler must keep both, or a vision model is blind.
func TestMCPToolHandlerKeepsImages(t *testing.T) {
	client := &fakeMCPClient{result: &mcpapi.CallToolResult{Content: []mcpapi.Content{
		mcpapi.NewTextContent("Screen 1336x752. frame_id=f1"),
		mcpapi.NewImageContent("iVBORw0KGgoAAAA", "image/png"),
	}}}
	tool := NewMCPToolAdapter(mcpapi.Tool{Name: "android_observe"}, "nautilus", client)

	res, err := tool.Handler()(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Screen 1336x752. frame_id=f1" {
		t.Errorf("text = %q", res.Text)
	}
	if len(res.Images) != 1 || res.Images[0] != "iVBORw0KGgoAAAA" {
		t.Errorf("images = %v, want the one PNG", res.Images)
	}
}

func TestMCPToolHandlerTextOnlyHasNoImages(t *testing.T) {
	client := &fakeMCPClient{result: &mcpapi.CallToolResult{Content: []mcpapi.Content{
		mcpapi.NewTextContent("Tapped (0.5, 0.5)."),
	}}}
	res, err := NewMCPToolAdapter(mcpapi.Tool{Name: "android_tap"}, "nautilus", client).Handler()(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Tapped (0.5, 0.5)." || len(res.Images) != 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestExtractImagesFromMCPResultOrderAndPointers(t *testing.T) {
	img2 := mcpapi.NewImageContent("BBBB", "image/jpeg")
	got := extractImagesFromMCPResult(&mcpapi.CallToolResult{Content: []mcpapi.Content{
		mcpapi.NewImageContent("AAAA", "image/png"),
		mcpapi.NewTextContent("between"),
		&img2,
	}})
	if len(got) != 2 || got[0] != "AAAA" || got[1] != "BBBB" {
		t.Errorf("images = %v", got)
	}
	if extractImagesFromMCPResult(nil) != nil {
		t.Error("nil result should give no images")
	}
}
