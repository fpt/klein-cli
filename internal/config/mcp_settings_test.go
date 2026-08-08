package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/fpt/klein-cli/internal/config/tomledit"
	"github.com/fpt/klein-cli/pkg/agent/domain"
)

// decodeMCP parses a settings fragment and returns just its mcp block.
func decodeMCP(t *testing.T, text string) MCPSettings {
	t.Helper()
	var s Settings
	if _, err := toml.Decode(text, &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return s.MCP
}

// TestMCPTableShape verifies a table per server parses, with enabled defaulting
// to true and type inferred from command/url — the same defaults Claude Code
// applies, so a server definition means the same thing in either tool.
func TestMCPTableShape(t *testing.T) {
	t.Parallel()
	m := decodeMCP(t, `
[mcp.browser-sandbox]
command = "docker"
args = ["run", "-i", "--rm"]

[mcp.docs]
url = "https://example.com/mcp"

[mcp.off]
command = "x"
enabled = false
`)

	byName := map[string]domain.MCPServerConfig{}
	for _, s := range m.Servers {
		byName[s.Name] = s
	}
	if len(m.Servers) != 3 {
		t.Fatalf("got %d servers, want 3: %+v", len(m.Servers), m.Servers)
	}

	if bs := byName["browser-sandbox"]; bs.Command != "docker" || string(bs.Type) != "stdio" || !bs.Enabled {
		t.Errorf("browser-sandbox wrong: %+v", bs)
	}
	if docs := byName["docs"]; docs.URL != "https://example.com/mcp" || string(docs.Type) != "sse" || !docs.Enabled {
		t.Errorf("docs wrong: %+v", docs)
	}
	if byName["off"].Enabled {
		t.Errorf("explicit enabled = false should be respected")
	}
}

// TestMCPEnvSubTable verifies env arrives as a sub-table and becomes sorted
// "KEY=VAL" entries.
func TestMCPEnvSubTable(t *testing.T) {
	t.Parallel()
	m := decodeMCP(t, `
[mcp.x]
command = "s"

[mcp.x.env]
B = "2"
A = "1"
`)
	if len(m.Servers) != 1 {
		t.Fatalf("got %d servers, want 1", len(m.Servers))
	}
	if env := m.Servers[0].Env; len(env) != 2 || env[0] != "A=1" || env[1] != "B=2" {
		t.Errorf("env: got %v, want [A=1 B=2] (sorted)", env)
	}
}

// A block that is not a table of tables is the mistake worth naming clearly,
// because TOML will happily parse `mcp = "godoc"` and only this reports it.
func TestMCPRejectsNonTableEntries(t *testing.T) {
	t.Parallel()
	var s Settings
	_, err := toml.Decode("[mcp]\ngodoc = \"not-a-table\"\n", &s)
	if err == nil {
		t.Fatal("a scalar under [mcp] should not decode as a server")
	}
	if !strings.Contains(err.Error(), "godoc") {
		t.Errorf("the error should name the offending server: %v", err)
	}
}

// The round trip that matters: a server decoded from the file, rendered back to
// TOML, and spliced into a document must decode to the same thing. This is what
// `klein mcp add` does, and getting it wrong quietly rewrites someone's server.
func TestMCPServerTOML_RoundTripsThroughTheEditor(t *testing.T) {
	t.Parallel()
	original := `# my settings
[llm]
backend = "openai"

# the Go documentation server
[mcp.godoc]
command = "godevmcp"
args = ["serve"]

[mcp.godoc.env]
TOKEN = "abc"
`
	before := decodeMCP(t, original)
	if len(before.Servers) != 1 {
		t.Fatalf("fixture did not parse: %+v", before.Servers)
	}

	body, err := MCPServerTOML(before.Servers[0])
	if err != nil {
		t.Fatalf("MCPServerTOML: %v", err)
	}
	doc, err := tomledit.SetTable([]byte(original), "mcp.godoc", body)
	if err != nil {
		t.Fatalf("SetTable: %v", err)
	}

	after := decodeMCP(t, string(doc))
	if len(after.Servers) != 1 {
		t.Fatalf("got %d servers after the round trip: %+v", len(after.Servers), after.Servers)
	}
	got, want := after.Servers[0], before.Servers[0]
	if got.Name != want.Name || got.Command != want.Command || string(got.Type) != string(want.Type) {
		t.Errorf("server changed in the round trip:\ngot  %+v\nwant %+v", got, want)
	}
	if len(got.Env) != 1 || got.Env[0] != "TOKEN=abc" {
		t.Errorf("env did not survive as a scoped sub-table: %v\n%s", got.Env, doc)
	}
	// The env table must stay the server's own, not become a top-level [env].
	if !strings.Contains(string(doc), "[mcp.godoc.env]") {
		t.Errorf("env sub-table lost its scope:\n%s", doc)
	}
	// And the author's comments are still there.
	for _, keep := range []string{"# my settings", "# the Go documentation server"} {
		if !strings.Contains(string(doc), keep) {
			t.Errorf("comment %q was lost:\n%s", keep, doc)
		}
	}
}

// The decoder hook has to fire on the path klein actually uses, not only on a
// direct toml.Decode in a test. MCPSettings.Servers is tagged `toml:"-"`, so if
// UnmarshalTOML were ever not invoked — a library change, a signature drift —
// every settings file would load with no MCP servers at all and nothing would
// say so. That failure is silent by construction, which is what earns it a test
// through LoadSettings rather than through the decoder.
func TestLoadSettings_PopulatesMCPServersFromFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "settings.toml")
	body := `[llm]
backend = "anthropic"

[mcp.godoc]
command = "godevmcp"
args = ["serve"]

[mcp.docs]
url = "https://example.com/mcp"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	s, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if len(s.MCP.Servers) != 2 {
		t.Fatalf("LoadSettings dropped the mcp tables: got %d servers, want 2", len(s.MCP.Servers))
	}

	byName := map[string]domain.MCPServerConfig{}
	for _, srv := range s.MCP.Servers {
		byName[srv.Name] = srv
	}
	if got := byName["godoc"]; got.Command != "godevmcp" || string(got.Type) != "stdio" {
		t.Errorf("godoc: %+v", got)
	}
	if got := byName["docs"]; got.URL != "https://example.com/mcp" || string(got.Type) != "sse" {
		t.Errorf("docs: %+v", got)
	}
}
