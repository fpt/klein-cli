package agentserver

import (
	"context"
	"testing"
)

// TestToInputSchema confirms a tool's parameters render as a JSON Schema object
// with typed properties and a required list.
func TestToInputSchema(t *testing.T) {
	t.Parallel()
	params := []Parameter{
		{Name: argQuery, Type: jsonTypeString, Required: true, Description: "the query"},
		{Name: "max", Type: jsonTypeNumber, Required: false},
		{Name: "flag", Type: jsonTypeBoolean},
	}
	s := toInputSchema(params)
	if s["type"] != jsonTypeObject {
		t.Fatalf("type: got %v", s["type"])
	}
	props := s["properties"].(map[string]any)
	if props[argQuery].(map[string]any)["type"] != jsonTypeString {
		t.Errorf("query type wrong: %v", props[argQuery])
	}
	if props["max"].(map[string]any)["type"] != jsonTypeNumber {
		t.Errorf("max type wrong: %v", props["max"])
	}
	req, _ := s["required"].([]string)
	if len(req) != 1 || req[0] != argQuery {
		t.Errorf("required: got %v want [query]", req)
	}
}

// stubTools is a DynamicTools that reports fixed specs and records what it was
// asked to run.
type stubTools struct {
	called *string
	specs  []ToolSpec
}

func (s stubTools) Specs() []ToolSpec { return s.specs }

func (s stubTools) Call(_ context.Context, name string, _ map[string]any) (string, error) {
	if s.called != nil {
		*s.called = name
	}
	return "ran " + name, nil
}

// TestBuildDynamicTools confirms offered tools become codex function specs
// (type/name/description/inputSchema), which is what thread/start registers so
// the backend can call back for them.
func TestBuildDynamicTools(t *testing.T) {
	t.Parallel()
	tools := stubTools{specs: []ToolSpec{
		{Name: "ScheduleCreate", Description: "make one", Parameters: []Parameter{
			{Name: "cron", Type: jsonTypeString, Required: true},
		}},
		{Name: "ScheduleList", Description: "list them"},
	}}

	specs := buildDynamicTools(tools)
	if len(specs) != 2 {
		t.Fatalf("want 2 specs, got %d", len(specs))
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
	// A tool with no parameters still gets a schema, or the backend has nothing
	// to validate its call against.
	list, ok := byName["ScheduleList"]
	if !ok {
		t.Fatalf("ScheduleList not registered; got %v", keys(byName))
	}
	if _, ok := list["inputSchema"].(map[string]any)["properties"]; !ok {
		t.Error("a parameterless tool should still carry an empty properties object")
	}
}

// TestBuildDynamicToolsNil returns nil when there are no tools to offer.
func TestBuildDynamicToolsNil(t *testing.T) {
	t.Parallel()
	if buildDynamicTools(nil) != nil {
		t.Error("expected nil when no tools are offered")
	}
}

func keys(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
