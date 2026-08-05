package skill

import (
	"strings"
	"testing"
)

// Tool and definition names here are deliberately synthetic: the routing under
// test does not care what a tool is called, and reusing real names would push
// this package's shared literals past the goconst threshold.
const (
	toolAlpha    = "AlphaTool"
	toolBeta     = "BetaTool"
	alphaAndBeta = toolAlpha + "," + toolBeta
	nameSkill    = "skill-under-test"
	nameRole     = "role-under-test"
	nameAgent    = "agent-under-test"
)

// Legacy `allowed-tools:` means two different things depending on the file it
// came from: a hard cap on an agent, only a visibility hint on a role or skill.
// Preserving that split is the whole reason ParseDefinition takes a Kind.
func TestParseDefinition_LegacyAllowedToolsMapsByKind(t *testing.T) {
	t.Parallel()

	src := "---\nname: x\ndescription: d\nallowed-tools: " + toolAlpha + ", " + toolBeta + "\n---\nbody"

	cases := []struct {
		name        string
		wantTools   []string // hard cap
		wantPreload []string // visibility hint
		kind        Kind
	}{
		{"agent gets a hard cap", []string{toolAlpha, toolBeta}, nil, KindAgent},
		{"role gets a hint only", nil, []string{toolAlpha, toolBeta}, KindRole},
		{"skill gets a hint only", nil, []string{toolAlpha, toolBeta}, KindSkill},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d, err := ParseDefinition([]byte(src), "x.md", 0, tt.kind)
			if err != nil {
				t.Fatalf("ParseDefinition: %v", err)
			}
			if strings.Join(d.Tools, ",") != strings.Join(tt.wantTools, ",") {
				t.Errorf("Tools: got %v, want %v", d.Tools, tt.wantTools)
			}
			if strings.Join(d.Preload, ",") != strings.Join(tt.wantPreload, ",") {
				t.Errorf("Preload: got %v, want %v", d.Preload, tt.wantPreload)
			}
			// Either way the declared list is what callers see.
			if got := strings.Join(d.EffectiveTools(), ","); got != alphaAndBeta {
				t.Errorf("EffectiveTools: got %q, want %q", got, alphaAndBeta)
			}
		})
	}
}

// An explicit `tools:` is a hard cap regardless of where the definition came
// from — that is what keeps a read-only agent read-only in any mode.
func TestParseDefinition_ExplicitToolsIsAlwaysAHardCap(t *testing.T) {
	t.Parallel()

	const src = "---\nname: x\ndescription: d\ntools:\n  - " + toolAlpha + "\n  - " + toolBeta + "\n---\nbody"

	for _, kind := range []Kind{KindSkill, KindRole, KindAgent} {
		d, err := ParseDefinition([]byte(src), "x.md", 0, kind)
		if err != nil {
			t.Fatalf("ParseDefinition(kind=%d): %v", kind, err)
		}
		if strings.Join(d.Tools, ",") != alphaAndBeta {
			t.Errorf("kind=%d: Tools = %v, want the hard cap", kind, d.Tools)
		}
	}
}

// Explicit fields win over the legacy one rather than being merged with it.
func TestParseDefinition_ExplicitFieldsBeatLegacy(t *testing.T) {
	t.Parallel()

	const src = "---\nname: x\ndescription: d\nallowed-tools: GammaTool\n" +
		"tools: " + toolAlpha + "\npreload: " + toolBeta + "\n---\nbody"

	d, err := ParseDefinition([]byte(src), "x.md", 0, KindAgent)
	if err != nil {
		t.Fatalf("ParseDefinition: %v", err)
	}
	if strings.Join(d.Tools, ",") != toolAlpha {
		t.Errorf("Tools: got %v, want the explicit tools list, which must beat allowed-tools", d.Tools)
	}
	if strings.Join(d.Preload, ",") != toolBeta {
		t.Errorf("Preload: got %v, want the explicit preload list", d.Preload)
	}
}

func TestParseDefinition_ParsesAgentOnlyFields(t *testing.T) {
	t.Parallel()

	const src = "---\nname: x\ndescription: d\nbackground: true\ncolor: blue\n" +
		"disallowedTools:\n  - GammaTool\n---\nbody"

	d, err := ParseDefinition([]byte(src), "x.md", 0, KindAgent)
	if err != nil {
		t.Fatalf("ParseDefinition: %v", err)
	}
	if !d.Background {
		t.Error("background not parsed")
	}
	if d.Color != "blue" {
		t.Errorf("color: got %q", d.Color)
	}
	if strings.Join(d.DisallowedTools, ",") != "GammaTool" {
		t.Errorf("disallowedTools: got %v", d.DisallowedTools)
	}
}

func TestDefinition_KindPredicates(t *testing.T) {
	t.Parallel()

	// The zero value is a skill, which is what a bare Definition{} literal has
	// always behaved as.
	var zero Definition
	if zero.IsRole() || zero.IsAgent() {
		t.Error("zero Definition should be neither role nor agent")
	}
	if !(&Definition{Kind: KindRole}).IsRole() {
		t.Error("KindRole should report IsRole")
	}
	if !(&Definition{Kind: KindAgent}).IsAgent() {
		t.Error("KindAgent should report IsAgent")
	}
}

// The ReadSkill catalog is about mid-session capabilities, so neither startup
// prompts nor delegation targets belong in it.
func TestBuildSkillCatalog_ExcludesRolesAndAgents(t *testing.T) {
	t.Parallel()

	catalog := BuildSkillCatalog(DefinitionMap{
		nameSkill: {Name: nameSkill, Description: "read pdfs", Kind: KindSkill},
		nameRole:  {Name: nameRole, Description: "coding role", Kind: KindRole},
		nameAgent: {Name: nameAgent, Description: "search agent", Kind: KindAgent},
	})

	if !strings.Contains(catalog, nameSkill) {
		t.Errorf("skill missing from catalog:\n%s", catalog)
	}
	for _, excluded := range []string{nameRole, nameAgent} {
		if strings.Contains(catalog, excluded) {
			t.Errorf("%q should not appear in the skill catalog:\n%s", excluded, catalog)
		}
	}
}
