package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/internal/skill"
	"github.com/fpt/klein-cli/pkg/message"
)

// An oversized tool result is written to the tool_results dir and the model is
// handed that path. If the path is not on the filesystem allowlist, Read fails
// and the model's only route back to the content is to re-run the tool — getting
// the same stub, and looping.
func TestBuildAgentTools_ToolResultsDirIsReadable(t *testing.T) {
	t.Parallel()
	workingDir := t.TempDir()
	toolResultsDir := t.TempDir()

	offloaded := filepath.Join(toolResultsDir, "msg_123.txt")
	if err := os.WriteFile(offloaded, []byte("offloaded result body"), 0o600); err != nil {
		t.Fatalf("could not seed offloaded result: %v", err)
	}

	opts := AgentOptions{
		Settings:   config.GetDefaultSettings(),
		FsRepo:     infra.NewOSFilesystemRepository(),
		WorkingDir: workingDir,
	}
	tools := buildAgentTools(opts, skill.SkillMap{}, "", toolResultsDir)

	result, err := tools.all.CallTool(context.Background(), "Read",
		message.ToolArgumentValues{"file_path": offloaded})
	if err != nil {
		t.Fatalf("Read returned an error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("Read was denied: %s", result.Error)
	}
	if !strings.Contains(result.Text, "offloaded result body") {
		t.Errorf("Read did not return the offloaded content, got: %s", result.Text)
	}
}

// The grant is scoped to the tool_results dir; the rest of ~/.klein stays out of
// reach (settings.json there holds API keys and the Discord bot token).
func TestBuildAgentTools_ToolResultsGrantIsScoped(t *testing.T) {
	t.Parallel()
	workingDir := t.TempDir()
	baseDir := t.TempDir()
	toolResultsDir := filepath.Join(baseDir, "tool_results")
	if err := os.MkdirAll(toolResultsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	sibling := filepath.Join(baseDir, "settings.json")
	if err := os.WriteFile(sibling, []byte(`{"llm":{"api_key":"secret"}}`), 0o600); err != nil {
		t.Fatalf("could not seed sibling file: %v", err)
	}

	opts := AgentOptions{
		Settings:   config.GetDefaultSettings(),
		FsRepo:     infra.NewOSFilesystemRepository(),
		WorkingDir: workingDir,
	}
	tools := buildAgentTools(opts, skill.SkillMap{}, "", toolResultsDir)

	result, err := tools.all.CallTool(context.Background(), "Read",
		message.ToolArgumentValues{"file_path": sibling})
	if err != nil {
		t.Fatalf("Read returned an error: %v", err)
	}
	if result.Error == "" {
		t.Errorf("expected a sibling of tool_results to stay denied, got: %s", result.Text)
	}
}
