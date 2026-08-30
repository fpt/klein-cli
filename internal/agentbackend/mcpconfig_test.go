package agentbackend

import (
	"reflect"
	"testing"

	"github.com/fpt/klein-cli/pkg/agent/domain"
)

// Server names shared by the proxy tests: one klein can run locally, one only
// reachable as a url.
const (
	localServer  = "local"
	remoteServer = "remote"
	remoteURL    = "https://example.com/mcp"
)

// TestMCPServersConfig confirms klein's MCP server configs translate into
// codex's mcp_servers table: stdio → command/args/env, url → url, disabled
// servers dropped.
func TestMCPServersConfig(t *testing.T) {
	t.Parallel()
	servers := []domain.MCPServerConfig{
		{
			Name:    "browser-sandbox",
			Enabled: true,
			Type:    domain.MCPServerTypeStdio,
			Command: "docker",
			Args:    []string{"run", "-i", "--rm", "img"},
			Env:     []string{"API_KEY=secret", "MODE=fast"},
		},
		{Name: "docs", Enabled: true, Type: domain.MCPServerTypeSSE, URL: remoteURL},
		{Name: "off", Enabled: false, Command: "nope"},
		{Name: "", Enabled: true, Command: "noname"},
	}

	got := MCPServersConfig(servers, nil)

	want := map[string]any{
		"browser-sandbox": map[string]any{
			"command": "docker",
			"args":    []string{"run", "-i", "--rm", "img"},
			"env":     map[string]string{"API_KEY": "secret", "MODE": "fast"},
		},
		"docs": map[string]any{"url": remoteURL},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MCPServersConfig mismatch:\n got=%#v\nwant=%#v", got, want)
	}
}

// TestMCPServersConfigEmpty returns nil (not an empty map) when nothing enabled,
// so ThreadStartOptions.Config stays unset.
func TestMCPServersConfigEmpty(t *testing.T) {
	t.Parallel()
	if got := MCPServersConfig(nil, nil); got != nil {
		t.Errorf("expected nil for no servers, got %#v", got)
	}
	if got := MCPServersConfig([]domain.MCPServerConfig{{Name: "x", Enabled: false}}, nil); got != nil {
		t.Errorf("expected nil when all disabled, got %#v", got)
	}
}

// TestMCPServersConfigSkipsProxied confirms a server klein proxies as dynamic
// tools is left out of the table the backend launches from. Shipping both would
// have the same tool names arrive twice, from two different processes.
func TestMCPServersConfigSkipsProxied(t *testing.T) {
	t.Parallel()
	servers := []domain.MCPServerConfig{
		{Name: localServer, Enabled: true, Command: "godevmcp"},
		{Name: remoteServer, Enabled: true, URL: remoteURL},
	}

	got := MCPServersConfig(servers, []string{localServer})

	want := map[string]any{remoteServer: map[string]any{"url": remoteURL}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("proxied server not skipped:\n got=%#v\nwant=%#v", got, want)
	}
}

// TestMCPServersConfigWildcardSkipsAll confirms "*" proxies everything, leaving
// the backend nothing to launch itself.
func TestMCPServersConfigWildcardSkipsAll(t *testing.T) {
	t.Parallel()
	servers := []domain.MCPServerConfig{
		{Name: localServer, Enabled: true, Command: "godevmcp"},
		{Name: remoteServer, Enabled: true, URL: remoteURL},
	}

	if got := MCPServersConfig(servers, []string{"*"}); got != nil {
		t.Errorf("expected nil when every server is proxied, got %#v", got)
	}
}
