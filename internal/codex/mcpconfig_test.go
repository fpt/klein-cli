package codex

import (
	"reflect"
	"testing"

	"github.com/fpt/klein-cli/pkg/agent/domain"
)

// TestMCPServersConfig confirms klein's MCP server configs translate into
// codex's mcp_servers table: stdio → command/args/env, url → url, disabled
// servers dropped.
func TestMCPServersConfig(t *testing.T) {
	servers := []domain.MCPServerConfig{
		{
			Name:    "browser-sandbox",
			Enabled: true,
			Type:    domain.MCPServerTypeStdio,
			Command: "docker",
			Args:    []string{"run", "-i", "--rm", "img"},
			Env:     []string{"API_KEY=secret", "MODE=fast"},
		},
		{Name: "docs", Enabled: true, Type: domain.MCPServerTypeSSE, URL: "https://example.com/mcp"},
		{Name: "off", Enabled: false, Command: "nope"},
		{Name: "", Enabled: true, Command: "noname"},
	}

	got := MCPServersConfig(servers)

	want := map[string]any{
		"browser-sandbox": map[string]any{
			"command": "docker",
			"args":    []string{"run", "-i", "--rm", "img"},
			"env":     map[string]string{"API_KEY": "secret", "MODE": "fast"},
		},
		"docs": map[string]any{"url": "https://example.com/mcp"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MCPServersConfig mismatch:\n got=%#v\nwant=%#v", got, want)
	}
}

// TestMCPServersConfigEmpty returns nil (not an empty map) when nothing enabled,
// so ThreadStartOptions.Config stays unset.
func TestMCPServersConfigEmpty(t *testing.T) {
	if got := MCPServersConfig(nil); got != nil {
		t.Errorf("expected nil for no servers, got %#v", got)
	}
	if got := MCPServersConfig([]domain.MCPServerConfig{{Name: "x", Enabled: false}}); got != nil {
		t.Errorf("expected nil when all disabled, got %#v", got)
	}
}
