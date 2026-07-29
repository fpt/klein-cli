package skill

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Role names these tests reach for repeatedly.
const (
	roleCAD  = "cad"
	roleClaw = "claw"
)

// writeDef writes a definition file (ROLE.md or SKILL.md) for one named
// definition under dir.
func writeDef(t *testing.T, dir, name, fileName, body string) {
	t.Helper()
	defDir := filepath.Join(dir, name)
	if err := os.MkdirAll(defDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: test " + name + "\n---\n\n" + body
	if err := os.WriteFile(filepath.Join(defDir, fileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The four built-in roles are the session entry points: -r for code/cad,
// the gateway for claw, and `klein review` for review.
func TestLoadBuiltinRoles_AreTheFourEntryPoints(t *testing.T) {
	t.Parallel()

	roles, err := LoadBuiltinRoles()
	if err != nil {
		t.Fatalf("LoadBuiltinRoles: %v", err)
	}
	for _, want := range []string{"code", roleClaw, roleCAD, "review"} {
		r, ok := roles[want]
		if !ok {
			t.Errorf("built-in role %q missing", want)
			continue
		}
		if !r.IsRole {
			t.Errorf("role %q is not marked IsRole", want)
		}
		if r.Description == "" {
			t.Errorf("role %q has no description", want)
		}
	}
}

// Roles and skills are separate sets. A name in one must not appear in the
// other, or `-r` validation and the skill catalog would disagree about it.
func TestRolesAndSkillsAreDisjoint(t *testing.T) {
	t.Parallel()

	roles, err := LoadBuiltinRoles()
	if err != nil {
		t.Fatal(err)
	}
	skills, err := LoadBuiltinSkills()
	if err != nil {
		t.Fatal(err)
	}
	for name := range roles {
		if _, clash := skills[name]; clash {
			t.Errorf("%q is both a role and a skill", name)
		}
	}
	// The skills that stayed skills are reached per-turn, never with -r.
	for _, want := range []string{"pdf", "github", "report", "market-narratives"} {
		if _, ok := skills[want]; !ok {
			t.Errorf("expected %q to remain a skill", want)
		}
		if _, wrong := roles[want]; wrong {
			t.Errorf("%q should not be a role", want)
		}
	}
}

// Roles use the same priority ladder as skills, so a personal or project
// ROLE.md can override a built-in one of the same name.
func TestLoadRoles_ProjectOverridesBuiltin(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	writeDef(t, filepath.Join(tmp, ".claude", "roles"), "code", roleFileName, "project override")

	roles, err := LoadRoles(tmp)
	if err != nil {
		t.Fatalf("LoadRoles: %v", err)
	}
	code := roles["code"]
	if code == nil {
		t.Fatal("code role missing")
	}
	if code.Priority != 4 {
		t.Errorf("project role should win at priority 4, got %d", code.Priority)
	}
	if strings.TrimSpace(code.Content) != "project override" {
		t.Errorf("expected the project ROLE.md content, got %q", code.Content)
	}
}

// A ROLE.md is only read from roles/, and a SKILL.md only from skills/ —
// dropping one in the wrong directory must not silently register it.
func TestLoaders_IgnoreTheOtherKindsFile(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	writeDef(t, filepath.Join(tmp, ".claude", "roles"), "stray-skill", skillFileName, "x")
	writeDef(t, filepath.Join(tmp, ".claude", "skills"), "stray-role", roleFileName, "x")

	roles, err := LoadRoles(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := roles["stray-skill"]; ok {
		t.Error("a SKILL.md under roles/ must not become a role")
	}
	skills, err := LoadSkills(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := skills["stray-role"]; ok {
		t.Error("a ROLE.md under skills/ must not become a skill")
	}
}

// The agent resolves every name through one registry, so both kinds have to be
// in it — Invoke doesn't care which it got.
func TestLoadRolesAndSkills_HoldsBoth(t *testing.T) {
	t.Parallel()

	defs, err := LoadRolesAndSkills(t.TempDir())
	if err != nil {
		t.Fatalf("LoadRolesAndSkills: %v", err)
	}
	if r := defs[roleCAD]; r == nil || !r.IsRole {
		t.Error("expected the cad role in the combined registry")
	}
	if s := defs["pdf"]; s == nil || s.IsRole {
		t.Error("expected the pdf skill in the combined registry, not marked as a role")
	}
}

// The catalog tells the model what it can reach with ReadSkill mid-session. A
// role is the prompt the session already opened with, so listing it would
// invite the model to load a second startup prompt on top of the first.
func TestBuildSkillCatalog_ExcludesRoles(t *testing.T) {
	t.Parallel()

	defs, err := LoadRolesAndSkills(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	catalog := BuildSkillCatalog(defs)

	for _, role := range []string{"code", roleClaw, roleCAD, "review"} {
		if containsEntry(catalog, role) {
			t.Errorf("catalog should not list the %q role:\n%s", role, catalog)
		}
	}
	if !containsEntry(catalog, "pdf") {
		t.Errorf("catalog should list the pdf skill:\n%s", catalog)
	}
}

// A registry of nothing but roles yields no catalog at all, rather than a
// header promising skills that aren't there.
func TestBuildSkillCatalog_RolesOnlyIsEmpty(t *testing.T) {
	t.Parallel()

	roles, err := LoadBuiltinRoles()
	if err != nil {
		t.Fatal(err)
	}
	if got := BuildSkillCatalog(roles); got != "" {
		t.Errorf("expected an empty catalog, got:\n%s", got)
	}
}

// RoleNames feeds the "roles: …" list in the -r error message.
func TestRoleNames_SortedRolesOnly(t *testing.T) {
	t.Parallel()

	defs, err := LoadRolesAndSkills(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got := RoleNames(defs)
	want := []string{roleCAD, roleClaw, "code", "review"}
	if !slices.Equal(got, want) {
		t.Errorf("RoleNames = %v, want %v", got, want)
	}
}

// containsEntry reports whether the catalog has a bullet for name. Matching the
// full "- **name**:" shape rather than the bare name keeps a description that
// happens to mention "code" from reading as an entry.
func containsEntry(catalog, name string) bool {
	return strings.Contains(catalog, "- **"+name+"**:")
}
