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
