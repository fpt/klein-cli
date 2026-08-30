package tool

import (
	"context"
	"slices"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

// The seeded fixture: one server with two tools, named after the real thing so a
// failure reads like the setup it stands for.
const (
	seededServer = "godevmcp"
	seededTool   = "tree_dir"
)

// seedServer registers tools under a server name the way loadToolsFromServer
// does: into the flat tools map the manager serves from, and into the per-server
// index SelectServers reads. Going through the real fields rather than a fake
// keeps the test honest about the coupling between the two.
func seedServer(m *MCPEnhancedToolManager, server string, names ...string) {
	var tools []message.Tool
	for _, n := range names {
		t := &mcpTool{
			name:        message.ToolName(n),
			description: message.ToolDescription("from " + server),
			handler: func(context.Context, message.ToolArgumentValues) (message.ToolResult, error) {
				return message.NewToolResultText(server), nil
			},
		}
		tools = append(tools, t)
		m.tools[t.name] = t
	}
	m.mcpTools[server] = tools
}

func newSeededManager() *MCPEnhancedToolManager {
	m := NewMCPEnhancedToolManager()
	seedServer(m, seededServer, seededTool, "search_local_files")
	seedServer(m, "weather", "forecast")
	return m
}

func selectedNames(m *MCPEnhancedToolManager, names ...string) []string {
	sub, _ := m.SelectServers(names)
	out := make([]string, 0)
	for name := range sub.GetTools() {
		out = append(out, string(name))
	}
	slices.Sort(out)
	return out
}

// Selecting one server takes its tools and leaves the other server's behind.
func TestSelectServers_PicksOneServer(t *testing.T) {
	t.Parallel()

	got := selectedNames(newSeededManager(), seededServer)

	want := []string{"search_local_files", seededTool}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// "*" is the escape hatch for a user who wants everything, and has to mean every
// connected server rather than every configured one.
func TestSelectServers_WildcardTakesEverything(t *testing.T) {
	t.Parallel()

	got := selectedNames(newSeededManager(), SelectServersWildcard)

	want := []string{"forecast", "search_local_files", "tree_dir"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Empty must stay empty. The list comes from a config file, and defaulting an
// unset one to "everything" would opt users into a cost they never asked for.
func TestSelectServers_EmptySelectsNothing(t *testing.T) {
	t.Parallel()

	if got := selectedNames(newSeededManager()); len(got) != 0 {
		t.Errorf("an empty selection exposed %v", got)
	}
}

// A name that matches no connected server is reported rather than swallowed:
// it is a typo in settings, and silence there looks exactly like a server that
// failed to start.
func TestSelectServers_ReportsUnknownNames(t *testing.T) {
	t.Parallel()

	sub, unknown := newSeededManager().SelectServers([]string{seededServer, "typo"})

	if !slices.Equal(unknown, []string{"typo"}) {
		t.Errorf("unknown = %v, want [typo]", unknown)
	}
	// The recognized half still works.
	if _, ok := sub.(*mcpServerSubset).GetTool(seededTool); !ok {
		t.Error("a bad name in the list dropped the good one")
	}
}

// The subset bounds execution, not just the catalog. A backend that calls a name
// outside its selection must be refused rather than quietly served.
func TestSelectServers_RefusesAnUnselectedTool(t *testing.T) {
	t.Parallel()

	sub, _ := newSeededManager().SelectServers([]string{seededServer})

	res, err := sub.CallTool(context.Background(), "forecast", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.Error == "" {
		t.Error("an unselected tool ran")
	}
}

// A selected call reaches the underlying manager's live tool.
func TestSelectServers_RunsASelectedTool(t *testing.T) {
	t.Parallel()

	sub, _ := newSeededManager().SelectServers([]string{seededServer})

	res, err := sub.CallTool(context.Background(), seededTool, nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("selected tool errored: %s", res.Error)
	}
}

// The view is a snapshot: a server connected after the selection was handed to a
// backend must not silently appear, because the backend was told the list once
// and nothing re-sends it.
func TestSelectServers_DoesNotGrowAfterConstruction(t *testing.T) {
	t.Parallel()

	m := newSeededManager()
	sub, _ := m.SelectServers([]string{SelectServersWildcard})
	seedServer(m, "late", "arrived_late")

	for name := range sub.GetTools() {
		if name == "arrived_late" {
			t.Error("the subset grew after construction")
		}
	}
}
