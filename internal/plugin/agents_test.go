package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// Agent names used across these tests. goconst counts literals package-wide.
const (
	agentExplore        = "explore"
	agentPlan           = "plan"
	agentGeneralPurpose = "general-purpose"
)

// writeAgent creates dir/<name>.md containing the given frontmatter body.
func writeAgent(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// agentMD builds a minimal agent definition.
func agentMD(name, description, body string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + body
}

// isolatedHome points os.UserHomeDir at an empty directory so a real
// ~/.claude/agents on the developer's machine cannot leak into a test.
// t.Setenv forbids t.Parallel, which is why these tests run serially.
func isolatedHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

//nolint:paralleltest // isolatedHome uses t.Setenv, which forbids t.Parallel
func TestLoadAgents_BuiltInsArePresent(t *testing.T) {
	isolatedHome(t)

	agents, err := LoadAgents(t.TempDir())
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}

	for _, name := range []string{agentExplore, agentPlan, agentGeneralPurpose} {
		ag, ok := agents[name]
		if !ok {
			t.Errorf("built-in agent %q not loaded", name)
			continue
		}
		if ag.Description == "" {
			t.Errorf("agent %q has no description; it would render blank in the Task listing", name)
		}
		if ag.Content == "" {
			t.Errorf("agent %q has an empty body; it would have no system prompt", name)
		}
		if ag.PluginName != "" {
			t.Errorf("agent %q: PluginName = %q, want empty for a built-in", name, ag.PluginName)
		}
		if ag.Priority != 0 {
			t.Errorf("agent %q: Priority = %d, want 0 so any local definition overrides it", name, ag.Priority)
		}
	}
}

// explore must not be able to mutate the repo — that is what makes it safe to
// fan out in parallel.
//
//nolint:paralleltest // isolatedHome uses t.Setenv, which forbids t.Parallel
func TestLoadAgents_ExploreIsReadOnly(t *testing.T) {
	isolatedHome(t)

	agents, err := LoadAgents(t.TempDir())
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	ag, ok := agents[agentExplore]
	if !ok {
		t.Fatal("explore agent not loaded")
	}
	if len(ag.Tools) == 0 {
		t.Fatal("explore declares no tools, so it inherits all of them including Write and Bash")
	}

	mutating := map[string]bool{"Write": true, "Edit": true, "MultiEdit": true, "Bash": true}
	for _, name := range ag.Tools {
		if mutating[name] {
			t.Errorf("explore is allowed the mutating tool %q", name)
		}
	}
}

//nolint:paralleltest // isolatedHome uses t.Setenv, which forbids t.Parallel
func TestLoadAgents_ProjectOverridesBuiltIn(t *testing.T) {
	isolatedHome(t)

	workDir := t.TempDir()
	writeAgent(t, filepath.Join(workDir, ".claude", agentsDirName), agentExplore,
		agentMD(agentExplore, "project override", "custom body"))

	agents, err := LoadAgents(workDir)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	ag := agents[agentExplore]
	if ag == nil {
		t.Fatal("explore missing after project override")
	}
	if ag.Description != "project override" {
		t.Errorf("description: got %q, want the project definition to win", ag.Description)
	}
	// The other built-ins must survive an override of one of them.
	if _, ok := agents[agentPlan]; !ok {
		t.Error("overriding explore also dropped the built-in plan agent")
	}
}

//nolint:paralleltest // isolatedHome uses t.Setenv, which forbids t.Parallel
func TestLoadAgents_HigherPriorityDirWins(t *testing.T) {
	isolatedHome(t)

	const name = "surveyor"
	workDir := t.TempDir()
	// .claude/agents is priority 4, .agents/agents is 5.
	writeAgent(t, filepath.Join(workDir, ".claude", agentsDirName), name, agentMD(name, "lower", "b"))
	writeAgent(t, filepath.Join(workDir, ".agents", agentsDirName), name, agentMD(name, "higher", "b"))

	agents, err := LoadAgents(workDir)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	if got := agents[name].Description; got != "higher" {
		t.Errorf("description: got %q, want %q (.agents must outrank .claude)", got, "higher")
	}
}

//nolint:paralleltest // isolatedHome uses t.Setenv, which forbids t.Parallel
func TestLoadAgents_SkipsMalformedAndNonMarkdown(t *testing.T) {
	isolatedHome(t)

	workDir := t.TempDir()
	dir := filepath.Join(workDir, ".claude", agentsDirName)
	writeAgent(t, dir, "good", agentMD("good", "a usable agent", "body"))

	// Unparseable frontmatter must not sink the directory.
	if err := os.WriteFile(filepath.Join(dir, "broken.md"),
		[]byte("---\nname: [unclosed\n---\nbody"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Non-markdown files and subdirectories are ignored.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	writeAgent(t, filepath.Join(dir, "nested"), "buried", agentMD("buried", "nested", "body"))

	agents, err := LoadAgents(workDir)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	if _, ok := agents["good"]; !ok {
		t.Error("the valid agent was dropped because a sibling was malformed")
	}
	if _, ok := agents["buried"]; ok {
		t.Error("agents in subdirectories must not be loaded")
	}
}

//nolint:paralleltest // isolatedHome uses t.Setenv, which forbids t.Parallel
func TestLoadAgents_MissingDirsAreNotAnError(t *testing.T) {
	isolatedHome(t)

	agents, err := LoadAgents(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("LoadAgents on a nonexistent working dir: %v", err)
	}
	if len(agents) == 0 {
		t.Error("built-ins should still load when no filesystem directory exists")
	}
}
