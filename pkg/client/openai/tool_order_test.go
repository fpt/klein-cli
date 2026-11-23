package openai

import (
	"testing"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
	mcpapi "github.com/mark3labs/mcp-go/mcp"
)

// The tool list is part of the prompt prefix. Go randomizes map iteration, so
// without sorting every request carried a different prefix and a local server
// could never reuse its KV cache between agent steps.
func TestConvertToolsIsSortedAndStable(t *testing.T) {
	names := []string{"Write", "Bash", "android_tap", "Read", "Glob", "TodoWrite", "Edit", "android_observe"}
	tools := make(map[message.ToolName]message.Tool, len(names))
	for _, n := range names {
		tools[message.ToolName(n)] = domain.NewMCPToolAdapter(mcpapi.Tool{Name: n}, "test", nil)
	}

	order := func() []string {
		var out []string
		for _, p := range convertTools(tools) {
			out = append(out, p.OfFunction.Name)
		}
		return out
	}
	first := order()
	want := []string{"Bash", "Edit", "Glob", "Read", "TodoWrite", "Write", "android_observe", "android_tap"}
	if len(first) != len(want) {
		t.Fatalf("got %v", first)
	}
	for i := range want {
		if first[i] != want[i] {
			t.Fatalf("order = %v, want %v", first, want)
		}
	}
	for range 20 {
		again := order()
		for i := range first {
			if again[i] != first[i] {
				t.Fatalf("order changed between calls: %v then %v", first, again)
			}
		}
	}
}
