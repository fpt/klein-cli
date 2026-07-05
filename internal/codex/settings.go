package codex

import (
	"context"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/tool"
)

// NewRunnerFromSettings builds a Runner from klein settings + a working dir.
// Model/effort come from the llm block; approval/sandbox/codex_path from the
// optional "codex" block. Two sets of tools are made reachable to codex-backed
// turns:
//   - klein's configured external MCP servers (translated to codex config), and
//   - klein's native tools (memory, schedule) registered as codex dynamic tools,
//     serviced in-process over the app-server JSON-RPC connection — so codex hits
//     the same live tool-manager instances (same files, same locks).
func NewRunnerFromSettings(ctx context.Context, settings *config.Settings, workingDir string) (*Runner, error) {
	nativeTools := tool.NewCompositeToolManager(
		tool.NewMemoryToolManager(settings.MemoryDir()),
		tool.NewScheduleToolManager(settings.SchedulesFile()),
	)

	return NewRunner(ctx, Config{
		CodexPath:      settings.Codex.CodexPath,
		Model:          settings.LLM.Model,
		Effort:         settings.LLM.Effort,
		ApprovalPolicy: settings.Codex.ApprovalPolicy,
		SandboxMode:    settings.Codex.SandboxMode,
		Cwd:            workingDir,
		MCPServers:     MCPServersConfig(settings.MCP.Servers),
		Tools:          nativeTools,
	})
}
