package skill

import (
	"os"
	"strings"
	"testing"
)

func TestRenderContentExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	s := &Skill{Content: "Save to {{home}}/.klein/skills/x/SKILL.md"}
	got := s.RenderContent("", "/work")
	want := home + "/.klein/skills/x/SKILL.md"
	if !strings.Contains(got, want) {
		t.Errorf("RenderContent did not expand {{home}}: got %q, want to contain %q", got, want)
	}
	if strings.Contains(got, "{{home}}") {
		t.Errorf("{{home}} left unexpanded: %q", got)
	}
}
