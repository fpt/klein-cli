package skill

import (
	"strings"
	"testing"
)

// A file that says nothing about modes inherits the set implied by where it
// came from, so no existing role, skill, or agent has to be edited.
func TestDefaultModesByKind(t *testing.T) {
	t.Parallel()

	const src = "---\nname: x\ndescription: d\n---\nbody"

	cases := []struct {
		name string
		want []Mode
		kind Kind
	}{
		{"role starts a session", []Mode{ModeStartup}, KindRole},
		{"agent is delegated to", []Mode{ModeSubagent}, KindAgent},
		// Subagent too: that is exactly what the deleted spawn_agent did.
		{"skill is inline and delegable", []Mode{ModeInline, ModeSubagent}, KindSkill},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d, err := ParseDefinition([]byte(src), "x.md", 0, tt.kind)
			if err != nil {
				t.Fatalf("ParseDefinition: %v", err)
			}
			if strings.Join(d.ModeNames(), ",") != strings.Join(modeStrings(tt.want), ",") {
				t.Errorf("modes = %v, want %v", d.ModeNames(), tt.want)
			}
			for _, m := range ValidModes {
				want := contains(tt.want, m)
				if got := d.Permits(m); got != want {
					t.Errorf("Permits(%s) = %v, want %v", m, got, want)
				}
			}
		})
	}
}

func modeStrings(modes []Mode) []string {
	out := make([]string, len(modes))
	for i, m := range modes {
		out[i] = string(m)
	}
	return out
}

func contains(modes []Mode, m Mode) bool {
	for _, x := range modes {
		if x == m {
			return true
		}
	}
	return false
}

// Declaring modes widens (or narrows) whatever the file kind implied.
func TestParseModes_ExplicitOverridesDefault(t *testing.T) {
	t.Parallel()

	src := "---\nname: x\ndescription: d\nmodes: [startup, subagent]\n---\nbody"
	d, err := ParseDefinition([]byte(src), "x.md", 0, KindAgent)
	if err != nil {
		t.Fatalf("ParseDefinition: %v", err)
	}
	if !d.Permits(ModeStartup) || !d.Permits(ModeSubagent) {
		t.Errorf("modes = %v, want both startup and subagent", d.ModeNames())
	}
	if d.Permits(ModeInline) {
		t.Errorf("inline not declared but permitted: %v", d.ModeNames())
	}
}

func TestParseModes_AcceptsCommaStringAndCase(t *testing.T) {
	t.Parallel()

	src := "---\nname: x\ndescription: d\nmodes: Startup, INLINE\n---\nbody"
	d, err := ParseDefinition([]byte(src), "x.md", 0, KindSkill)
	if err != nil {
		t.Fatalf("ParseDefinition: %v", err)
	}
	if !d.Permits(ModeStartup) || !d.Permits(ModeInline) || d.Permits(ModeSubagent) {
		t.Errorf("modes = %v", d.ModeNames())
	}
}

// A typo must not silently leave the definition on its defaults, which would
// give the author no signal at all.
func TestParseModes_UnknownIsAnError(t *testing.T) {
	t.Parallel()

	src := "---\nname: x\ndescription: d\nmodes: [startup, sub-agent]\n---\nbody"
	_, err := ParseDefinition([]byte(src), "x.md", 0, KindRole)
	if err == nil {
		t.Fatal("expected an error for an unknown mode")
	}
	for _, want := range []string{"sub-agent", "startup", "subagent", "inline"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

// A Definition built as a struct literal (tests, callers) must behave as its
// Kind implies rather than permitting nothing.
func TestPermits_ZeroModesFallsBackToKind(t *testing.T) {
	t.Parallel()

	if !(&Definition{Kind: KindRole}).Permits(ModeStartup) {
		t.Error("bare role literal should permit startup")
	}
	if !(&Definition{Kind: KindAgent}).Permits(ModeSubagent) {
		t.Error("bare agent literal should permit subagent")
	}
	if (&Definition{Kind: KindAgent}).Permits(ModeStartup) {
		t.Error("bare agent literal should not permit startup")
	}
}

func TestNamesPermitting(t *testing.T) {
	t.Parallel()

	defs := DefinitionMap{
		"zrole":  {Name: "zrole", Kind: KindRole},
		"arole":  {Name: "arole", Kind: KindRole},
		"askill": {Name: "askill", Kind: KindSkill},
		"aagent": {Name: "aagent", Kind: KindAgent},
	}
	if got := strings.Join(NamesPermitting(defs, ModeStartup), ","); got != "arole,zrole" {
		t.Errorf("startup = %q, want sorted roles only", got)
	}
	if got := strings.Join(NamesPermitting(defs, ModeSubagent), ","); got != "aagent,askill" {
		t.Errorf("subagent = %q, want the agent and the skill", got)
	}
}
