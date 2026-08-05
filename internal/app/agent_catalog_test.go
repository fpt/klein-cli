package app

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/infra"
	pluginpkg "github.com/fpt/klein-cli/internal/plugin"
	"github.com/fpt/klein-cli/internal/skill"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// Tool names are constants because goconst counts string literals across the
// whole package and these already appear in other app tests.
const (
	catToolRead = "Read"
	catToolGrep = "Grep"
	catToolGlob = "Glob"
)

// newCatalogTestAgent returns an Agent with just enough state for
// RegisterPlugins to run: the skill map must be non-nil because plugin skills
// are merged into it.
func newCatalogTestAgent() *Agent {
	return &Agent{skills: skill.SkillMap{}}
}

// agentsNamed builds an agents map keyed by name, deriving each definition's
// Name from its key so a name appears exactly once per call site.
func agentsNamed(pluginName string, names ...string) map[string]*pluginpkg.Agent {
	out := make(map[string]*pluginpkg.Agent, len(names))
	for _, n := range names {
		out[n] = &pluginpkg.Agent{
			Name:        n,
			PluginName:  pluginName,
			Description: pluginName + " " + n,
		}
	}
	return out
}

func plug(name string, agents map[string]*pluginpkg.Agent) *pluginpkg.Plugin {
	return &pluginpkg.Plugin{Name: name, Agents: agents}
}

func TestAgentCatalog_PrefersBareNameAndDeduplicates(t *testing.T) {
	t.Parallel()

	const (
		searcher = "repo-searcher"
		descr    = "Searches a repo for prior art"
	)
	a := newCatalogTestAgent()
	a.RegisterPlugins([]*pluginpkg.Plugin{
		plug("docs-for-ai", map[string]*pluginpkg.Agent{
			searcher: {
				Name:        searcher,
				PluginName:  "docs-for-ai",
				Description: descr,
				Tools:       []string{catToolRead, catToolGrep, catToolGlob},
			},
		}),
	})

	entries := a.AgentCatalog()
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (scoped+bare must dedupe): %+v", len(entries), entries)
	}
	if entries[0].Name != searcher {
		t.Errorf("name: got %q, want bare name %q", entries[0].Name, searcher)
	}
	if entries[0].Description != descr {
		t.Errorf("description: got %q", entries[0].Description)
	}
	if len(entries[0].Tools) != 3 {
		t.Errorf("tools: got %v, want 3 entries", entries[0].Tools)
	}
}

func TestAgentCatalog_FallsBackToScopedNameWhenAmbiguous(t *testing.T) {
	t.Parallel()

	const contested = "watcher"
	a := newCatalogTestAgent()
	a.RegisterPlugins([]*pluginpkg.Plugin{
		plug("alpha", agentsNamed("alpha", contested)),
		plug("beta", agentsNamed("beta", contested)),
	})

	entries := a.AgentCatalog()
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}
	// Sorted, so alpha precedes beta. Both must be scoped: the bare "watcher"
	// is ambiguous and would not resolve.
	want := []string{"alpha:" + contested, "beta:" + contested}
	for i, w := range want {
		if entries[i].Name != w {
			t.Errorf("entry %d: got %q, want %q", i, entries[i].Name, w)
		}
	}
}

func TestAgentCatalog_SortedForStableToolDescription(t *testing.T) {
	t.Parallel()

	want := []string{"apple", "cherry", "mango", "zebra"}
	a := newCatalogTestAgent()
	a.RegisterPlugins([]*pluginpkg.Plugin{
		plug("p", agentsNamed("p", "zebra", "apple", "mango", "cherry")),
	})

	// Map iteration is randomized, so repeat to catch unsorted output.
	for range 20 {
		entries := a.AgentCatalog()
		if len(entries) != len(want) {
			t.Fatalf("got %d entries, want %d", len(entries), len(want))
		}
		for i, w := range want {
			if entries[i].Name != w {
				t.Fatalf("entry %d: got %q, want %q (catalog not sorted)", i, entries[i].Name, w)
			}
		}
	}
}

// A built-in or project agent owns its bare name outright: a plugin claiming
// the same name must not turn it ambiguous and make it unreachable.
func TestRegisterPlugins_LocalAgentBeatsPluginOnBareName(t *testing.T) {
	t.Parallel()

	const contested = "explore"
	local := &pluginpkg.Agent{Name: contested, Description: "built-in explore"}
	a := newCatalogTestAgent()
	a.pluginAgents = map[string]*pluginpkg.Agent{contested: local}
	a.RegisterPlugins([]*pluginpkg.Plugin{
		plug("someplugin", agentsNamed("someplugin", contested)),
	})

	got, ambiguous := a.ResolveAgent(contested)
	if ambiguous {
		t.Fatal("bare name reported ambiguous; the local agent should win outright")
	}
	if got != local {
		t.Errorf("bare %q resolved to %+v, want the local definition", contested, got)
	}
	// The plugin's agent is still reachable, just only when scoped.
	scoped, ambiguous := a.ResolveAgent("someplugin:" + contested)
	if ambiguous || scoped == nil {
		t.Error("plugin agent unreachable under its scoped name")
	}
	if scoped == local {
		t.Error("scoped name resolved to the local agent, not the plugin's")
	}
}

// Two plugins claiming one bare name still has no defined winner.
func TestRegisterPlugins_TwoPluginsStillAmbiguous(t *testing.T) {
	t.Parallel()

	const contested = "watcher"
	a := newCatalogTestAgent()
	a.RegisterPlugins([]*pluginpkg.Plugin{
		plug("alpha", agentsNamed("alpha", contested)),
		plug("beta", agentsNamed("beta", contested)),
	})

	if _, ambiguous := a.ResolveAgent(contested); !ambiguous {
		t.Error("bare name contested by two plugins should be ambiguous")
	}
}

func TestAgentCatalog_EmptyWithoutPlugins(t *testing.T) {
	t.Parallel()

	a := newCatalogTestAgent()
	if entries := a.AgentCatalog(); len(entries) != 0 {
		t.Errorf("got %+v, want no entries", entries)
	}
}

// The whole chain, with no stubs between the loader and the model-facing text:
// LoadAgents finds the embedded built-ins, the constructor stores them, and the
// Task tool's description lists them for the model to pick from.
//
//nolint:paralleltest // t.Setenv isolates HOME, which forbids t.Parallel
func TestBuiltInAgentsReachTheTaskToolDescription(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	a, cleanup, err := NewAgentWithOptions(context.Background(), AgentOptions{
		Settings:           config.GetDefaultSettings(),
		WorkingDir:         t.TempDir(),
		MCPToolManagers:    map[string]domain.ToolManager{},
		Logger:             pkgLogger.NewLogger(pkgLogger.LogLevelError),
		Out:                io.Discard,
		FsRepo:             infra.NewOSFilesystemRepository(),
		SkipSessionRestore: true,
		IsInteractiveMode:  false,
		LLMClient:          &stubLLM{}, // no provider API key needed
	})
	if err != nil {
		t.Fatalf("NewAgentWithOptions: %v", err)
	}
	defer cleanup()

	taskTool, ok := a.allToolManagers.GetTool("Task")
	if !ok {
		t.Fatal("Task tool not registered")
	}
	desc := string(taskTool.Description())

	if strings.Contains(desc, "No subagents are currently loaded") {
		t.Fatalf("Task reports no agents even though built-ins ship in the binary:\n%s", desc)
	}
	for _, name := range []string{"explore", "plan", "general-purpose"} {
		if !strings.Contains(desc, "- "+name+": ") {
			t.Errorf("built-in agent %q missing from the Task description:\n%s", name, desc)
		}
	}
	// explore's read-only tool set must survive into the listing.
	if !strings.Contains(desc, "(Tools: Read, LS, Glob, Grep, ToolSearch)") {
		t.Errorf("explore's tool restriction not shown in the listing:\n%s", desc)
	}
}
