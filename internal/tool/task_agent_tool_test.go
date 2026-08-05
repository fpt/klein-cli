package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

func TestTaskAgentTool_CallsCallback(t *testing.T) {
	mgr := NewTaskAgentToolManager()

	var got TaskRequest
	mgr.SetCallback(func(_ context.Context, req TaskRequest) (string, error) {
		got = req
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
	if got.SubagentType != "docs-for-ai:repo-searcher" {
		t.Errorf("subagent_type: got %q", got.SubagentType)
	}
	if got.Prompt != "Find auth model in docs_for_ai/cart-service" {
		t.Errorf("prompt: got %q", got.Prompt)
	}
	if got.Background {
		t.Error("Background should default to false")
	}
}

func TestTaskAgentTool_RequiresArgs(t *testing.T) {
	mgr := NewTaskAgentToolManager()
	mgr.SetCallback(func(context.Context, TaskRequest) (string, error) {
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

// Tool names are constants because goconst counts string literals across the
// whole package and these already appear in other tool tests.
const (
	catToolRead = "Read"
	catToolGrep = "Grep"
	catToolGlob = "Glob"
)

func TestTaskAgentTool_DescriptionListsAgents(t *testing.T) {
	t.Parallel()

	mgr := NewTaskAgentToolManager()
	mgr.SetCatalogProvider(func() []AgentCatalogEntry {
		return []AgentCatalogEntry{
			{
				Name:        "explore",
				Description: "Read-only search agent",
				Tools:       []string{catToolRead, catToolGrep, catToolGlob},
			},
			{Name: "github-watcher:pr-watcher", Description: "Watches a PR for review activity"},
		}
	})

	tool, ok := mgr.GetTool("Task")
	if !ok {
		t.Fatal("Task tool not registered")
	}
	desc := string(tool.Description())

	for _, want := range []string{
		"- explore: Read-only search agent (Tools: Read, Grep, Glob)",
		"- github-watcher:pr-watcher: Watches a PR for review activity (Tools: All tools)",
		// The wider dispatch set is named even when the listing is populated.
		"A skill name also works as subagent_type",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("description missing %q\ngot:\n%s", want, desc)
		}
	}
}

func TestTaskAgentTool_DescriptionWhenNoAgentsLoaded(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*TaskAgentToolManager){
		"no provider wired": func(*TaskAgentToolManager) {},
		"provider returns none": func(m *TaskAgentToolManager) {
			m.SetCatalogProvider(func() []AgentCatalogEntry { return nil })
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			mgr := NewTaskAgentToolManager()
			setup(mgr)

			tool, _ := mgr.GetTool("Task")
			desc := string(tool.Description())

			if !strings.Contains(desc, "No named agents are loaded") {
				t.Errorf("description should say no agents are loaded, got:\n%s", desc)
			}
			if strings.Contains(desc, "Available agents") {
				t.Errorf("description should not advertise an empty listing, got:\n%s", desc)
			}
			// Dispatch accepts any definition permitting subagent mode, so an
			// empty listing must not read as "this tool is unusable" — skills
			// are still valid subagent_type values.
			if !strings.Contains(desc, "skill name as subagent_type") {
				t.Errorf("description must still point at skill dispatch, got:\n%s", desc)
			}
			if strings.Contains(desc, "cannot be used") {
				t.Errorf("description wrongly claims the tool is unusable, got:\n%s", desc)
			}
		})
	}
}

// The catalog is read on every Description() call so agents registered after
// the tool manager is constructed (plugins load later) still show up.
func TestTaskAgentTool_DescriptionReflectsLateRegistration(t *testing.T) {
	t.Parallel()

	mgr := NewTaskAgentToolManager()
	var entries []AgentCatalogEntry
	mgr.SetCatalogProvider(func() []AgentCatalogEntry { return entries })

	tool, _ := mgr.GetTool("Task")
	if strings.Contains(string(tool.Description()), "late-agent") {
		t.Fatal("agent listed before registration")
	}

	entries = []AgentCatalogEntry{{Name: "late-agent", Description: "registered later"}}
	if !strings.Contains(string(tool.Description()), "late-agent") {
		t.Error("agent registered after construction is not listed")
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

// run_in_background must reach the dispatcher; without it the flag would be
// silently dropped and every request would run in the foreground.
func TestTaskAgentTool_ForwardsRunInBackground(t *testing.T) {
	t.Parallel()

	mgr := NewTaskAgentToolManager()
	var got TaskRequest
	mgr.SetCallback(func(_ context.Context, req TaskRequest) (string, error) {
		got = req
		return "launched", nil
	})

	if _, err := mgr.CallTool(context.Background(), "Task", message.ToolArgumentValues{
		"subagent_type":     "pr-watcher",
		"prompt":            "watch PR 42",
		"run_in_background": true,
	}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !got.Background {
		t.Error("run_in_background did not reach the dispatcher")
	}
}
