package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

func TestTaskAgentTool_CallsCallback(t *testing.T) {
	mgr := NewTaskAgentToolManager()

	var gotSubagent, gotPrompt string
	mgr.SetCallback(func(_ context.Context, subagent, prompt string) (string, error) {
		gotSubagent = subagent
		gotPrompt = prompt
		return "subagent-response-text", nil
	})

	res, err := mgr.CallTool(context.Background(), "Task", message.ToolArgumentValues{
		"subagent_type": "docs-for-ai:repo-searcher",
		"description":   "Search cart docs",
		"prompt":        "Find auth model in docs_for_ai/cart-service",
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("tool returned error: %s", res.Error)
	}
	if res.Text != "subagent-response-text" {
		t.Errorf("text: got %q want %q", res.Text, "subagent-response-text")
	}
	if gotSubagent != "docs-for-ai:repo-searcher" {
		t.Errorf("subagent_type: got %q", gotSubagent)
	}
	if gotPrompt != "Find auth model in docs_for_ai/cart-service" {
		t.Errorf("prompt: got %q", gotPrompt)
	}
}

func TestTaskAgentTool_RequiresArgs(t *testing.T) {
	mgr := NewTaskAgentToolManager()
	mgr.SetCallback(func(context.Context, string, string) (string, error) {
		t.Fatal("callback should not run when args invalid")
		return "", nil
	})

	cases := []struct {
		name string
		args message.ToolArgumentValues
		want string // substring of error
	}{
		{"missing subagent_type", message.ToolArgumentValues{"prompt": "x"}, "subagent_type"},
		{"missing prompt", message.ToolArgumentValues{"subagent_type": "x"}, "prompt"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			res, _ := mgr.CallTool(context.Background(), "Task", tt.args)
			if !strings.Contains(res.Error, tt.want) {
				t.Errorf("error %q does not mention %q", res.Error, tt.want)
			}
		})
	}
}

func TestTaskAgentTool_UnwiredCallback(t *testing.T) {
	mgr := NewTaskAgentToolManager() // SetCallback NOT called
	res, _ := mgr.CallTool(context.Background(), "Task", message.ToolArgumentValues{
		"subagent_type": "x",
		"prompt":        "y",
	})
	if !strings.Contains(res.Error, "not available") {
		t.Errorf("expected 'not available' error, got %q", res.Error)
	}
}
