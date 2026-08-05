package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/infra"
	pluginpkg "github.com/fpt/klein-cli/internal/plugin"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// TestPluginsWireUpEndToEnd loads m6o-plugin-marketplace, constructs a real
// Agent (no LLM call), and verifies command/agent registration round-trips
// through ResolveCommand / ResolveAgent. This is the closest the test layer
// can come to "the user types /github-watcher:watch and it resolves" without
// hitting the LLM.
func TestPluginsWireUpEndToEnd(t *testing.T) {
	root := os.Getenv("KLEIN_TEST_MARKETPLACE")
	if root == "" {
		root = filepath.Join(os.Getenv("HOME"), "Documents", "work", "m6o-plugin-marketplace")
	}
	if _, err := os.Stat(filepath.Join(root, ".claude-plugin", "marketplace.json")); err != nil {
		t.Skipf("m6o-plugin-marketplace not present at %s; skipping", root)
	}

	mp, err := pluginpkg.LoadMarketplace(root)
	if err != nil {
		t.Fatalf("LoadMarketplace: %v", err)
	}
	plugins := make([]*pluginpkg.Plugin, 0, len(mp.Plugins))
	for _, p := range mp.Plugins {
		plugins = append(plugins, p)
	}

	// Real Agent so the full RegisterPlugins path runs.
	workDir := t.TempDir()
	settings := config.GetDefaultSettings()
	logger := pkgLogger.NewLogger(pkgLogger.LogLevelError)
	fsRepo := infra.NewOSFilesystemRepository()

	// RegisterPlugins doesn't need a real LLM; a stub keeps the test hermetic
	// (building from settings would need a provider API key). No AgentBackend,
	// so no external process; cleanup is a noop.
	a, cleanup, err := NewAgentWithOptions(context.Background(), AgentOptions{
		Settings:           settings,
		WorkingDir:         workDir,
		MCPToolManagers:    map[string]domain.ToolManager{},
		Logger:             logger,
		Out:                os.Stdout,
		FsRepo:             fsRepo,
		SkipSessionRestore: true,
		IsInteractiveMode:  false,
		// Inject a stub so the agent constructs without a provider API key.
		LLMClient: &stubLLM{},
	})
	if err != nil {
		t.Fatalf("NewAgentWithOptions: %v", err)
	}
	defer cleanup()
	a.RegisterPlugins(plugins)

	cases := []struct {
		scoped string
		bareOK bool // whether the bare name resolves uniquely
	}{
		{"github-watcher:watch", true},
		{"github-watcher:check", true},
		{"github-watcher:status", true}, // collides with built-in /status; bare won't dispatch but scoped does
		{"docs-for-ai:search-docs", true},
		{"docs-for-ai:join-docs", true},
		{"t-wada-consulting:tdd", true},
		{"t-wada-consulting:review", true},
		{"t-wada-consulting:ask", true},
	}
	for _, tt := range cases {
		t.Run(tt.scoped, func(t *testing.T) {
			cmd, ambiguous := a.ResolveCommand(tt.scoped)
			if ambiguous {
				t.Errorf("scoped command %s reported ambiguous", tt.scoped)
				return
			}
			if cmd == nil {
				t.Errorf("scoped command %s not found", tt.scoped)
			}
		})
	}

	// Plugin agents.
	for _, scoped := range []string{
		"github-watcher:pr-watcher",
		"docs-for-ai:repo-searcher",
	} {
		t.Run(scoped, func(t *testing.T) {
			ag, ambiguous := a.ResolveSubagent(scoped)
			if ambiguous {
				t.Errorf("agent %s ambiguous", scoped)
				return
			}
			if ag == nil {
				t.Errorf("agent %s not registered", scoped)
				return
			}
			if ag.Description == "" {
				t.Errorf("agent %s has empty description", scoped)
			}
		})
	}

	// MCP servers from plugins should be exposed.
	mcps := a.PluginMCPServers()
	if len(mcps) < 2 {
		t.Errorf("expected >=2 MCP servers from plugins (m6o-jira + m6o-datadog + codex-mcp), got %d", len(mcps))
	}
	seenAtlassian := false
	for _, s := range mcps {
		if s.Name == "atlassian" && s.Type == domain.MCPServerTypeHTTP {
			seenAtlassian = true
		}
	}
	if !seenAtlassian {
		t.Error("atlassian HTTP MCP server not surfaced via PluginMCPServers()")
	}
}
