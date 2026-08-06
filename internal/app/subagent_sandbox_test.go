package app

import (
	"context"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/internal/skill"
)

func TestIntersectTools(t *testing.T) {
	t.Parallel()
	bound := []string{catToolRead, catToolGrep, catToolGlob, "LS", "Task"}

	// A capped definition keeps only what the bound permits.
	got := intersectTools([]string{catToolRead, "LS", catToolGlob, catToolGrep, "ToolSearch"}, bound)
	if want := []string{catToolRead, "LS", catToolGlob, catToolGrep}; !equalStringSlices(got, want) {
		t.Errorf("capped: got %v, want %v", got, want)
	}

	// An uncapped definition (empty = all tools) collapses to the bound itself,
	// NOT to "no cap".
	got = intersectTools(nil, bound)
	if !equalStringSlices(got, bound) {
		t.Errorf("uncapped: got %v, want the bound %v", got, bound)
	}

	// Disjoint lists intersect to empty — the caller must treat that as an
	// error, never pass it to buildSubAgentToolManager (empty = unrestricted).
	if got = intersectTools([]string{toolBash, toolWrite}, bound); len(got) != 0 {
		t.Errorf("disjoint: got %v, want empty", got)
	}
}

// A subagent whose tools are entirely outside the parent's hard override must
// be refused, not silently granted the full tool set (buildSubAgentToolManager
// reads an empty allowlist as "no cap").
func TestRunSubagent_DisjointFromSandboxRefused(t *testing.T) {
	t.Parallel()
	a := &Agent{definitions: skill.DefinitionMap{}}
	a.SetAllowedToolsOverride([]string{catToolRead, catToolGlob, "LS"})

	def := &skill.Definition{
		Name:  "shell-runner",
		Kind:  skill.KindAgent,
		Tools: []string{toolBash, toolWrite},
	}
	_, err := a.RunSubagent(context.Background(), def, "do something", nil, 0)
	if err == nil {
		t.Fatal("expected an error for a subagent with no permitted tools")
	}
	for _, want := range []string{"shell-runner", "sandbox"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

// A caller-supplied per-dispatch tools override must not pierce the sandbox
// either.
func TestRunSubagent_CallerOverrideStillBounded(t *testing.T) {
	t.Parallel()
	a := &Agent{definitions: skill.DefinitionMap{}}
	a.SetAllowedToolsOverride([]string{catToolRead})

	def := &skill.Definition{Name: "helper", Kind: skill.KindAgent}
	_, err := a.RunSubagent(context.Background(), def, "task", []string{toolBash}, 0)
	if err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("caller override outside the sandbox should be refused, got %v", err)
	}
}

// A background run must resolve its tools on the dispatching goroutine. The
// sandbox is turn-scoped mutable state (InvokeCommand swaps one in for the
// length of a plugin command), so a run that resolved its own tools later could
// find the override already restored and escape the boundary. Refusing at
// dispatch — synchronously, before the run is even registered — is what proves
// resolution has not drifted back into the goroutine.
func TestStartBackgroundAgent_ResolvesSandboxAtDispatch(t *testing.T) {
	t.Parallel()
	a := &Agent{definitions: skill.DefinitionMap{}}
	a.SetAllowedToolsOverride([]string{catToolRead, catToolGlob})

	def := &skill.Definition{
		Name:  "shell-runner",
		Kind:  skill.KindAgent,
		Tools: []string{toolBash, toolWrite},
	}
	if _, err := a.StartBackgroundAgent(def, "do something"); err == nil {
		t.Fatal("expected a synchronous refusal at dispatch")
	} else if !strings.Contains(err.Error(), "sandbox") {
		t.Errorf("error %q should mention the sandbox", err)
	}
}

// The snapshot a background dispatch captures survives the sandbox being
// restored afterwards: re-resolving it inside the run is idempotent, and with
// the override gone the snapshot itself is what still bounds the run.
func TestResolveSubagentTools_SnapshotSurvivesSandboxRestore(t *testing.T) {
	t.Parallel()
	a := &Agent{definitions: skill.DefinitionMap{}}
	a.SetAllowedToolsOverride([]string{catToolRead, catToolGlob})

	// Dispatch time: an uncapped definition collapses to the sandbox.
	def := &skill.Definition{Name: "helper", Kind: skill.KindAgent}
	snapshot, err := a.resolveSubagentTools(def, nil)
	if err != nil {
		t.Fatalf("resolve at dispatch: %v", err)
	}
	if want := []string{catToolRead, catToolGlob}; !equalStringSlices(snapshot, want) {
		t.Fatalf("snapshot: got %v, want %v", snapshot, want)
	}

	// The turn that dispatched it ends and restores the sandbox.
	a.SetAllowedToolsOverride(nil)

	// The run re-resolves with the snapshot as its override and stays bounded —
	// it must NOT widen back to the uncapped definition's full tool set.
	got, err := a.resolveSubagentTools(def, snapshot)
	if err != nil {
		t.Fatalf("resolve in run: %v", err)
	}
	if !equalStringSlices(got, snapshot) {
		t.Errorf("run escaped its captured sandbox: got %v, want %v", got, snapshot)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
