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
	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// Tool names are constants because goconst counts string literals across the
// whole package and these already appear in other app tests.
const (
	catToolRead  = "Read"
	catToolGrep  = "Grep"
	catToolGlob  = "Glob"
	catNameRole  = "code"
	catNameSkill = "pdf"
	catNameAgent = nameExplore
	nameWidened  = "widened-role"
)

// newCatalogTestAgent returns an Agent with just enough state for
// RegisterPlugins to run: the skill map must be non-nil because plugin skills
// are merged into it.
func newCatalogTestAgent() *Agent {
	return &Agent{definitions: skill.DefinitionMap{}}
}

// agentsNamed builds an agents map keyed by name, deriving each definition's
// Name from its key so a name appears exactly once per call site.
func agentsNamed(pluginName string, names ...string) map[string]*pluginpkg.Agent {
	out := make(map[string]*pluginpkg.Agent, len(names))
	for _, n := range names {
		out[n] = &pluginpkg.Agent{
			Kind:        skill.KindAgent,
			Modes:       []skill.Mode{skill.ModeSubagent},
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
				Kind:        skill.KindAgent,
				Modes:       []skill.Mode{skill.ModeSubagent},
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

	const contested = catNameAgent
	local := &pluginpkg.Agent{
		Kind:  skill.KindAgent,
		Modes: []skill.Mode{skill.ModeSubagent},
		Name:  contested, Description: "built-in explore",
	}
	a := newCatalogTestAgent()
	a.definitions = map[string]*pluginpkg.Agent{contested: local}
	a.RegisterPlugins([]*pluginpkg.Plugin{
		plug("someplugin", agentsNamed("someplugin", contested)),
	})

	got, ambiguous := a.ResolveSubagent(contested)
	if ambiguous {
		t.Fatal("bare name reported ambiguous; the local agent should win outright")
	}
	if got != local {
		t.Errorf("bare %q resolved to %+v, want the local definition", contested, got)
	}
	// The plugin's agent is still reachable, just only when scoped.
	scoped, ambiguous := a.ResolveSubagent("someplugin:" + contested)
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

	if _, ambiguous := a.ResolveSubagent(contested); !ambiguous {
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

// Roles, skills, and agents share one map now, so the gates that keep them
// apart are lookupInvocable and ResolveAgent rather than separate registries.
func TestRegistry_KindGatesLookup(t *testing.T) {
	t.Parallel()

	a := newCatalogTestAgent()
	a.definitions = skill.DefinitionMap{
		catNameRole:  {Name: catNameRole, Kind: skill.KindRole, Modes: []skill.Mode{skill.ModeStartup}},
		catNameSkill: {Name: catNameSkill, Kind: skill.KindSkill, Modes: []skill.Mode{skill.ModeInline}},
		catNameAgent: {Name: catNameAgent, Kind: skill.KindAgent, Modes: []skill.Mode{skill.ModeSubagent}},
	}

	cases := []struct {
		name          string
		wantInvocable bool
		wantAgent     bool
	}{
		{catNameRole, true, false},  // a role runs via Invoke, not via Task
		{catNameSkill, true, false}, // ditto a skill
		{catNameAgent, false, true}, // an agent is reachable only via Task
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, ok := a.lookupInvocable(tt.name); ok != tt.wantInvocable {
				t.Errorf("lookupInvocable(%q) = %v, want %v", tt.name, ok, tt.wantInvocable)
			}
			ag, _ := a.ResolveSubagent(tt.name)
			if (ag != nil) != tt.wantAgent {
				t.Errorf("ResolveAgent(%q) found = %v, want %v", tt.name, ag != nil, tt.wantAgent)
			}
		})
	}
}

// One namespace means a name claimed twice needs a defined winner rather than
// a coin flip.
func TestMergeDefinitions_CollisionHasADefinedWinner(t *testing.T) {
	t.Parallel()

	logger := pkgLogger.NewLogger(pkgLogger.LogLevelError)

	t.Run("higher priority agent wins", func(t *testing.T) {
		t.Parallel()

		merged := mergeDefinitions(
			logger,
			skill.DefinitionMap{"x": {Name: "x", Kind: skill.KindSkill, Priority: 0}},
			pluginpkg.AgentMap{"x": {Name: "x", Kind: skill.KindAgent, Priority: 4}},
		)
		if !merged["x"].IsAgent() {
			t.Error("higher-priority agent should win the name")
		}
	})

	t.Run("tie keeps the non-agent", func(t *testing.T) {
		t.Parallel()

		merged := mergeDefinitions(
			logger,
			skill.DefinitionMap{"x": {Name: "x", Kind: skill.KindSkill, Priority: 4}},
			pluginpkg.AgentMap{"x": {Name: "x", Kind: skill.KindAgent, Priority: 4}},
		)
		if merged["x"].IsAgent() {
			t.Error("on a tie the role/skill keeps the name, preserving Invoke behavior")
		}
	})

	t.Run("no collision keeps both", func(t *testing.T) {
		t.Parallel()

		merged := mergeDefinitions(
			logger,
			skill.DefinitionMap{"a": {Name: "a", Kind: skill.KindSkill}},
			pluginpkg.AgentMap{"b": {Name: "b", Kind: skill.KindAgent}},
		)
		if len(merged) != 2 {
			t.Errorf("got %d entries, want both kept", len(merged))
		}
	})
}

// The Task listing must show agents only — the registry now also holds every
// role and skill.
func TestAgentCatalog_ExcludesRolesAndSkills(t *testing.T) {
	t.Parallel()

	a := newCatalogTestAgent()
	a.definitions = skill.DefinitionMap{
		catNameRole: {
			Name: catNameRole, Description: "coding role", Kind: skill.KindRole,
			Modes: []skill.Mode{skill.ModeStartup},
		},
		catNameSkill: {
			Name: catNameSkill, Description: "pdf skill", Kind: skill.KindSkill,
			Modes: []skill.Mode{skill.ModeInline},
		},
		catNameAgent: {
			Name: catNameAgent, Description: "search agent", Kind: skill.KindAgent,
			Modes: []skill.Mode{skill.ModeSubagent},
		},
	}

	entries := a.AgentCatalog()
	if len(entries) != 1 || entries[0].Name != catNameAgent {
		t.Errorf("catalog = %+v, want only the agent", entries)
	}
}

// Dispatch membership is by declared mode, not by which file a definition came
// from — that is what lets Task absorb the deleted spawn_agent tool.
func TestResolveSubagent_ByModeNotKind(t *testing.T) {
	t.Parallel()

	a := newCatalogTestAgent()
	a.definitions = skill.DefinitionMap{
		catNameRole:  {Name: catNameRole, Kind: skill.KindRole},   // startup only
		catNameSkill: {Name: catNameSkill, Kind: skill.KindSkill}, // inline + subagent
		catNameAgent: {Name: catNameAgent, Kind: skill.KindAgent}, // subagent
		nameWidened: {
			Name: nameWidened, Kind: skill.KindRole,
			Modes: []skill.Mode{skill.ModeStartup, skill.ModeSubagent},
		},
	}

	cases := map[string]bool{
		catNameRole:  false, // a plain role is not dispatchable
		catNameSkill: true,  // what spawn_agent used to do
		catNameAgent: true,
		nameWidened:  true, // a role that declares subagent is
	}
	for name, want := range cases {
		got, ambiguous := a.ResolveSubagent(name)
		if ambiguous {
			t.Errorf("%q reported ambiguous", name)
			continue
		}
		if (got != nil) != want {
			t.Errorf("ResolveSubagent(%q) found = %v, want %v", name, got != nil, want)
		}
	}
}

// The Task listing stays curated even though dispatch is wider: skills are
// dispatchable but are not written to be delegated to.
func TestAgentCatalog_ListsAgentsOnlyThoughSkillsDispatch(t *testing.T) {
	t.Parallel()

	a := newCatalogTestAgent()
	a.definitions = skill.DefinitionMap{
		catNameSkill: {Name: catNameSkill, Description: "a skill", Kind: skill.KindSkill},
		catNameAgent: {Name: catNameAgent, Description: "an agent", Kind: skill.KindAgent},
	}

	entries := a.AgentCatalog()
	if len(entries) != 1 || entries[0].Name != catNameAgent {
		t.Errorf("catalog = %+v, want only the agent", entries)
	}
	// ...but the skill is still dispatchable.
	if d, _ := a.ResolveSubagent(catNameSkill); d == nil {
		t.Error("skill should still be dispatchable even though it is unlisted")
	}
}

// Invoke drives the session's own turn, so a subagent-only definition must not
// be reachable there.
func TestLookupInvocable_ExcludesSubagentOnly(t *testing.T) {
	t.Parallel()

	a := newCatalogTestAgent()
	a.definitions = skill.DefinitionMap{
		catNameRole:  {Name: catNameRole, Kind: skill.KindRole},
		catNameSkill: {Name: catNameSkill, Kind: skill.KindSkill},
		catNameAgent: {Name: catNameAgent, Kind: skill.KindAgent},
	}

	for name, want := range map[string]bool{catNameRole: true, catNameSkill: true, catNameAgent: false} {
		if _, ok := a.lookupInvocable(name); ok != want {
			t.Errorf("lookupInvocable(%q) = %v, want %v", name, ok, want)
		}
	}
}

// Asking for a mode a definition does not permit must say what it is, not
// pretend the name is unknown.
func TestDispatchTask_RejectsWrongModeWithARealMessage(t *testing.T) {
	t.Parallel()

	a := newCatalogTestAgent()
	a.definitions = skill.DefinitionMap{
		catNameRole: {Name: catNameRole, Kind: skill.KindRole},
	}

	_, err := a.DispatchTask(context.Background(), tool.TaskRequest{SubagentType: catNameRole, Prompt: "do a thing"})
	if err == nil {
		t.Fatal("expected an error dispatching to a startup-only definition")
	}
	for _, want := range []string{catNameRole, "cannot run as a subagent", "startup"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}

	_, err = a.DispatchTask(context.Background(), tool.TaskRequest{SubagentType: "no-such-thing", Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown name should say not found, got %v", err)
	}
}

// The Task listing stays curated by *declaration*: an agent is listed, and so
// is anything whose frontmatter names subagent mode, but a skill that merely
// inherits the mode is not.
func TestAgentCatalog_ListsDeclaredSubagentsNotInheritedOnes(t *testing.T) {
	t.Parallel()

	a := newCatalogTestAgent()
	a.definitions = skill.DefinitionMap{
		// Inherits [inline, subagent] from its Kind — dispatchable, not listed.
		catNameSkill: {Name: catNameSkill, Description: "a skill", Kind: skill.KindSkill},
		// Agent-kind — always listed.
		catNameAgent: {Name: catNameAgent, Description: "an agent", Kind: skill.KindAgent},
		// A role that opts in explicitly — listed, because declaring it is the
		// author saying "delegate to this".
		nameWidened: {
			Name: nameWidened, Description: "a widened role", Kind: skill.KindRole,
			Modes: []skill.Mode{skill.ModeStartup, skill.ModeSubagent},
		},
		// A plain role — neither listed nor dispatchable.
		catNameRole: {Name: catNameRole, Description: "a role", Kind: skill.KindRole},
	}

	var listed []string
	for _, e := range a.AgentCatalog() {
		listed = append(listed, e.Name)
	}
	if strings.Join(listed, ",") != catNameAgent+","+nameWidened {
		t.Errorf("catalog = %v, want the agent and the widened role only", listed)
	}

	// Dispatch stays wider than the listing.
	if d, _ := a.ResolveSubagent(catNameSkill); d == nil {
		t.Error("an unlisted skill must still be dispatchable")
	}
	if d, _ := a.ResolveSubagent(catNameRole); d != nil {
		t.Error("a plain role must not be dispatchable")
	}
}

// /<name> and -r reach the same set: whatever permits startup.
func TestLookupStartup(t *testing.T) {
	t.Parallel()

	a := newCatalogTestAgent()
	a.definitions = skill.DefinitionMap{
		catNameRole:  {Name: catNameRole, Kind: skill.KindRole},
		catNameSkill: {Name: catNameSkill, Kind: skill.KindSkill},
		catNameAgent: {Name: catNameAgent, Kind: skill.KindAgent},
		nameWidened: {
			Name: nameWidened, Kind: skill.KindAgent,
			Modes: []skill.Mode{skill.ModeStartup, skill.ModeSubagent},
		},
	}

	for name, want := range map[string]bool{
		catNameRole:  true,  // a role opens a session
		catNameSkill: false, // a skill is inline/subagent, not a session prompt
		catNameAgent: false, // a plain agent is subagent-only
		nameWidened:  true,  // ...until it declares startup
	} {
		if _, ok := a.LookupStartup(name); ok != want {
			t.Errorf("LookupStartup(%q) = %v, want %v", name, ok, want)
		}
	}

	if got := strings.Join(a.StartupNames(), ","); got != catNameRole+","+nameWidened {
		t.Errorf("StartupNames = %q, want the role and the widened agent", got)
	}
}
