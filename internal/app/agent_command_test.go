package app

import (
	"strings"
	"testing"

	"github.com/fpt/klein-cli/internal/skill"
)

// Hoisted because goconst counts string literals package-wide.
const (
	cmdNameHelp  = "help"
	cmdNameClear = "clear"
	nameExplore  = "explore"
)

// A definition must never be able to take over a REPL command the user needs to
// get out of trouble.
func TestIsBuiltinSlashCommand(t *testing.T) {
	t.Parallel()

	for _, name := range []string{cmdNameHelp, cmdNameClear, "quit", cmdGoal, cmdLoop} {
		if !isBuiltinSlashCommand(name) {
			t.Errorf("%q should be recognized as a built-in", name)
		}
	}
	if isBuiltinSlashCommand(nameExplore) {
		t.Error("explore is not a built-in and must be dispatchable as /explore")
	}
}

// The palette lists what /<name> will actually reach, so a name shadowed by a
// built-in is left out rather than advertised and then ignored.
func TestSlashCandidates_IncludesAgentsExceptShadowedNames(t *testing.T) {
	t.Parallel()

	a := newCatalogTestAgent()
	a.definitions = skill.DefinitionMap{
		nameExplore:  {Name: nameExplore, Kind: skill.KindAgent, Modes: []skill.Mode{skill.ModeStartup}},
		cmdNameHelp:  {Name: cmdNameHelp, Kind: skill.KindRole},   // collides with the built-in
		catNameSkill: {Name: catNameSkill, Kind: skill.KindSkill}, // no startup mode
	}

	var agentEntries []string
	helpCount := 0
	for _, c := range slashCandidates(a) {
		if c.Description == agentCommandDescription {
			agentEntries = append(agentEntries, c.Name)
		}
		if c.Name == cmdNameHelp {
			helpCount++
		}
	}

	if strings.Join(agentEntries, ",") != nameExplore {
		t.Errorf("agent entries = %v, want only explore", agentEntries)
	}
	if helpCount != 1 {
		t.Errorf("help listed %d times, want once (the built-in only)", helpCount)
	}
}
