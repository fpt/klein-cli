package agentserver

import (
	"testing"

	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/pkg/message"
)

// TestToInputSchema confirms klein tool arguments render as a JSON Schema object
// with typed properties and a required list.
func TestToInputSchema(t *testing.T) {
	t.Parallel()
	args := []message.ToolArgument{
		{Name: "query", Type: "string", Required: true, Description: "the query"},
		{Name: "max", Type: "number", Required: false},
		{Name: "flag", Type: "boolean"},
	}
	s := toInputSchema(args)
	if s["type"] != "object" {
		t.Fatalf("type: got %v", s["type"])
	}
	props := s["properties"].(map[string]any)
	if props["query"].(map[string]any)["type"] != "string" {
		t.Errorf("query type wrong: %v", props["query"])
	}
	if props["max"].(map[string]any)["type"] != "number" {
		t.Errorf("max type wrong: %v", props["max"])
	}
	req, _ := s["required"].([]string)
	if len(req) != 1 || req[0] != "query" {
		t.Errorf("required: got %v want [query]", req)
	}
}

// TestBuildDynamicTools confirms a real tool manager's tools become codex
// function specs (type/name/description/inputSchema), which is what thread/start
// registers so codex calls back for them.
func TestBuildDynamicTools(t *testing.T) {
	t.Parallel()
	sched := tool.NewScheduleToolManager(t.TempDir() + "/s.json")
	specs := buildDynamicTools(sched)
	if len(specs) == 0 {
		t.Fatal("expected specs for the schedule tools")
	}
	byName := map[string]map[string]any{}
	for _, s := range specs {
		if s["type"] != "function" {
			t.Errorf("spec type: got %v want function", s["type"])
		}
		if _, ok := s["inputSchema"].(map[string]any); !ok {
			t.Errorf("spec %v missing inputSchema", s["name"])
		}
		byName[s["name"].(string)] = s
	}
	if _, ok := byName["ScheduleCreate"]; !ok {
		t.Errorf("ScheduleCreate not registered; got %v", keys(byName))
	}
	if _, ok := byName["ScheduleList"]; !ok {
		t.Errorf("ScheduleList not registered; got %v", keys(byName))
	}
}

// TestBuildDynamicToolsNil returns nil for a nil manager (no tools to register).
func TestBuildDynamicToolsNil(t *testing.T) {
	t.Parallel()
	if buildDynamicTools(nil) != nil {
		t.Error("expected nil for nil tool manager")
	}
}

func keys(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
