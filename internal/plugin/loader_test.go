package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadM6oMarketplace is a smoke test against the developer's real
// m6o-plugin-marketplace checkout. It is skipped when the marketplace isn't
// present so CI on other machines stays green.
func TestLoadM6oMarketplace(t *testing.T) {
	root := os.Getenv("KLEIN_TEST_MARKETPLACE")
	if root == "" {
		root = filepath.Join(os.Getenv("HOME"), "Documents", "work", "m6o-plugin-marketplace")
	}
	if _, err := os.Stat(filepath.Join(root, ".claude-plugin", "marketplace.json")); err != nil {
		t.Skipf("m6o-plugin-marketplace not present at %s; skipping", root)
	}

	mp, err := LoadMarketplace(root)
	if err != nil {
		t.Fatalf("LoadMarketplace(%s) failed: %v", root, err)
	}

	if len(mp.Plugins) < 5 {
		t.Errorf("expected at least 5 plugins, got %d", len(mp.Plugins))
	}

	// Spot-check well-known plugins.
	tests := []struct {
		name          string
		wantCommands  int
		wantAgents    int
		wantMCPCount  int
	}{
		{"github-watcher", 3, 1, 0},
		{"docs-for-ai", 2, 1, 0},
		{"m6o-jira", 0, 0, 1},
		{"m6o-datadog", 0, 0, 1},
		{"codex-mcp", 0, 0, 1},
		{"t-wada-consulting", 3, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := mp.Plugins[tt.name]
			if !ok {
				t.Fatalf("plugin %s not loaded", tt.name)
			}
			if len(p.Commands) < tt.wantCommands {
				t.Errorf("commands: got %d want >=%d (%v)", len(p.Commands), tt.wantCommands, keys(p.Commands))
			}
			if len(p.Agents) < tt.wantAgents {
				t.Errorf("agents: got %d want >=%d", len(p.Agents), tt.wantAgents)
			}
			if len(p.MCPServers) != tt.wantMCPCount {
				t.Errorf("mcpServers: got %d want %d", len(p.MCPServers), tt.wantMCPCount)
			}
		})
	}

	// Verify a specific command's frontmatter round-tripped.
	gw := mp.Plugins["github-watcher"]
	if gw == nil {
		t.Fatal("github-watcher missing")
	}
	watch := gw.Commands["watch"]
	if watch == nil {
		t.Fatal("github-watcher:watch missing")
	}
	if watch.ArgumentHint != "<pr-number>" {
		t.Errorf("argument-hint: got %q want %q", watch.ArgumentHint, "<pr-number>")
	}
	wantTools := map[string]bool{
		"Task": true, "Bash": true, "Read": true,
		"Edit": true, "Write": true, "Glob": true, "Grep": true,
	}
	for _, tool := range watch.AllowedTools {
		if !wantTools[tool] {
			t.Errorf("unexpected allowed tool: %q", tool)
		}
		delete(wantTools, tool)
	}
	if len(wantTools) > 0 {
		t.Errorf("missing allowed tools: %v", wantTools)
	}

	// Verify an HTTP MCP server config round-tripped.
	jira := mp.Plugins["m6o-jira"]
	if jira == nil || len(jira.MCPServers) == 0 {
		t.Fatal("m6o-jira missing MCP config")
	}
	atl := jira.MCPServers[0]
	if atl.Name != "atlassian" || atl.Type != "http" || atl.URL == "" {
		t.Errorf("atlassian server malformed: %+v", atl)
	}
}

func TestCommandRender(t *testing.T) {
	c := &Command{Body: "Investigate PR #$ARGUMENTS in {{workingDir}}\n\nFirst arg: $0"}
	got := c.Render("1061 extra", "/repo")
	want := "Investigate PR #1061 extra in /repo\n\nFirst arg: 1061"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	// No $ARGUMENTS in body → append ARGUMENTS line.
	c2 := &Command{Body: "Run the watcher"}
	got = c2.Render("42", "/repo")
	want = "Run the watcher\nARGUMENTS: 42"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
