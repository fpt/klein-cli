package codex

import (
	"context"

	"github.com/fpt/klein-cli/internal/config"
)

// NewRunnerFromSettings builds a Runner from klein settings + a working dir.
// Model/effort come from the llm block; approval/sandbox/codex_path from the
// optional "codex" block; and MCP servers are translated from the shared MCP
// config so codex-backed turns reach them.
func NewRunnerFromSettings(ctx context.Context, settings *config.Settings, workingDir string) (*Runner, error) {
	return NewRunner(ctx, Config{
		CodexPath:      settings.Codex.CodexPath,
		Model:          settings.LLM.Model,
		Effort:         settings.LLM.Effort,
		ApprovalPolicy: settings.Codex.ApprovalPolicy,
		SandboxMode:    settings.Codex.SandboxMode,
		Cwd:            workingDir,
		MCPServers:     MCPServersConfig(settings.MCP.Servers),
	})
}
