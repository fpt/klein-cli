package app

import (
	"testing"

	pluginpkg "github.com/fpt/klein-cli/internal/plugin"
	"github.com/fpt/klein-cli/internal/skill"
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

func TestAgentCatalog_EmptyWithoutPlugins(t *testing.T) {
	t.Parallel()

	a := newCatalogTestAgent()
	if entries := a.AgentCatalog(); len(entries) != 0 {
		t.Errorf("got %+v, want no entries", entries)
	}
}
