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

// A compound parameter's shape has to survive into the rendered schema. Without
// it an array reaches the backend as a bare {"type":"array"} and the model is
// left guessing what an element contains — which shows up as a model that
// "cannot use the tool" rather than as a schema problem.
func TestToInputSchema_CarriesCompoundShapes(t *testing.T) {
	t.Parallel()

	element := map[string]any{
		keyType:      jsonTypeObject,
		"properties": map[string]any{"file_path": map[string]any{keyType: jsonTypeString}},
	}
	schema := toInputSchema([]Parameter{{
		Name:        "edits",
		Type:        jsonTypeArray,
		Description: "edits to apply",
		Required:    true,
		Schema:      map[string]any{"items": element},
	}})

	props := schema["properties"].(map[string]any)
	edits := props["edits"].(map[string]any)
	if edits[keyType] != jsonTypeArray {
		t.Errorf("type = %v", edits["type"])
	}
	// The fields keep their meaning alongside the passthrough.
	if edits["description"] != "edits to apply" {
		t.Errorf("description = %v", edits["description"])
	}
	items, ok := edits["items"].(map[string]any)
	if !ok {
		t.Fatalf("the element schema did not survive: %+v", edits)
	}
	if _, ok := items["properties"].(map[string]any)["file_path"]; !ok {
		t.Errorf("element properties lost: %+v", items)
	}
}

// Schema is an escape hatch, so a key it sets wins over the one rendered from
// the struct's fields — otherwise a caller could describe a shape the struct
// cannot express and then be overruled by the struct's guess at it.
func TestToInputSchema_ExplicitKeysWin(t *testing.T) {
	t.Parallel()

	schema := toInputSchema([]Parameter{{
		Name: "mode",
		Type: jsonTypeString,
		// A string with a fixed set of values: expressible only here.
		Schema: map[string]any{"enum": []string{"files", "count"}, keyType: jsonTypeString},
	}})
	mode := schema["properties"].(map[string]any)["mode"].(map[string]any)
	if mode["enum"] == nil {
		t.Errorf("enum lost: %+v", mode)
	}
}

// A deferred tool goes out with "advertised": false. The backend registers it
// either way; the key is what tells it not to spend the model's attention.
func TestBuildDynamicToolsMarksDeferred(t *testing.T) {
	t.Parallel()
	tools := stubTools{specs: []ToolSpec{
		{Name: "Read", Description: "read a file"},
		{Name: "tree_dir", Description: "walk a tree", Deferred: true},
	}}

	byName := map[string]map[string]any{}
	for _, s := range buildDynamicTools(tools) {
		byName[s["name"].(string)] = s
	}

	if got, ok := byName["tree_dir"]["advertised"]; !ok || got != false {
		t.Errorf(`deferred tool: advertised = %v (present=%v), want false`, got, ok)
	}
	// The deferred tool is still registered — deferral is not omission.
	if byName["tree_dir"]["inputSchema"] == nil {
		t.Error("a deferred tool must still carry its schema; the backend has to call it")
	}
}

// The payload for a caller that defers nothing must be byte-identical to what it
// was before deferral existed. A backend that has never heard of "advertised"
// then behaves exactly as it always did, which is what makes the flag safe to
// send before the other end supports it.
func TestBuildDynamicToolsOmitsAdvertisedWhenNothingIsDeferred(t *testing.T) {
	t.Parallel()
	tools := stubTools{specs: []ToolSpec{
		{Name: "Read", Description: "read a file"},
		{Name: "Bash", Description: "run a command"},
	}}

	for _, s := range buildDynamicTools(tools) {
		if _, ok := s["advertised"]; ok {
			t.Errorf("tool %v carried an advertised key when nothing was deferred", s["name"])
		}
	}
}
