package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/infra"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// newOptionsAgent builds an Agent in a temp working directory, letting a test
// vary just the option under study.
func newOptionsAgent(t *testing.T, workingDir string, mutate func(*AgentOptions)) *Agent {
	t.Helper()
	opts := AgentOptions{
		Settings:          config.GetDefaultSettings(),
		WorkingDir:        workingDir,
		Logger:            pkgLogger.NewLogger(pkgLogger.LogLevelError),
		Out:               io.Discard,
		FsRepo:            infra.NewOSFilesystemRepository(),
		IsInteractiveMode: false,
		LLMClient:         &stubLLM{},
	}
	if mutate != nil {
		mutate(&opts)
	}
	a, cleanup, err := NewAgentWithOptions(context.Background(), opts)
	if err != nil {
		t.Fatalf("NewAgentWithOptions: %v", err)
	}
	t.Cleanup(cleanup)
	return a
}

// writeContextFile drops an AGENTS.md into dir and returns its marker text.
func writeContextFile(t *testing.T, dir string) string {
	t.Helper()
	const marker = "PROJECT_CONTEXT_MARKER"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(marker), 0o600); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	return marker
}

// contextInjected reports whether any message carries the marker.
func contextInjected(a *Agent, marker string) bool {
	for _, m := range a.GetMessageState().GetMessages() {
		if strings.Contains(m.Content(), marker) {
			return true
		}
	}
	return false
}

func TestInjectContextFile_LoadsAgentsMDByDefault(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	marker := writeContextFile(t, dir)

	a := newOptionsAgent(t, dir, nil)
	a.InjectContextFile()

	if !contextInjected(a, marker) {
		t.Fatal("AGENTS.md should be injected when SkipContextFile is false")
	}
}

func TestInjectContextFile_SkippedByOption(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	marker := writeContextFile(t, dir)

	a := newOptionsAgent(t, dir, func(o *AgentOptions) { o.SkipContextFile = true })
	a.InjectContextFile()

	if contextInjected(a, marker) {
		t.Fatal("--no-agents-md must suppress the AGENTS.md injection")
	}
}

func TestSkillDirs_LoadDefinitionsFromExtraDirectory(t *testing.T) {
	t.Parallel()

	skillsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(skillsRoot, "greet"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: greet\ndescription: extra-dir skill\n---\nSay hello.\n"
	if err := os.WriteFile(filepath.Join(skillsRoot, "greet", "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	// Absent without the flag, present with it — the flag is the only difference.
	if _, ok := newOptionsAgent(t, t.TempDir(), nil).definitions["greet"]; ok {
		t.Fatal("greet must not load without --skills")
	}

	a := newOptionsAgent(t, t.TempDir(), func(o *AgentOptions) { o.SkillDirs = []string{skillsRoot} })
	d, ok := a.definitions["greet"]
	if !ok {
		t.Fatal("--skills directory should contribute its SKILL.md definitions")
	}
	if d.Description != "extra-dir skill" {
		t.Errorf("Description = %q, want the extra dir's copy", d.Description)
	}
}
