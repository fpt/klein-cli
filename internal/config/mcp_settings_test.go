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

// exampleMCPURL is the stand-in URL these tests point url-transport servers at.
const exampleMCPURL = "https://example.com/mcp"

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
	if docs := byName["docs"]; docs.URL != exampleMCPURL || string(docs.Type) != "sse" || !docs.Enabled {
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
	if got := byName["docs"]; got.URL != exampleMCPURL || string(got.Type) != "sse" {
		t.Errorf("docs: %+v", got)
	}
}

// The [mcp.<name>.oauth] table is what a user actually types, so decoding it is
// worth pinning: enabled defaults to true (writing the table is the opt-in), and
// the scopes/port come through for the login flow to use.
func TestMCPSettings_OAuthBlock(t *testing.T) {
	t.Parallel()

	mcp := decodeMCP(t, `
[mcp.datadog]
type = "http"
url  = "https://mcp.datadoghq.com/api/unstable/mcp-server/mcp"

[mcp.datadog.oauth]
scopes        = ["mcp_all"]
redirect_port = 33418
`)
	if len(mcp.Servers) != 1 {
		t.Fatalf("got %d servers, want 1", len(mcp.Servers))
	}
	oauth := mcp.Servers[0].OAuth
	if oauth == nil {
		t.Fatal("the oauth table was dropped")
	}
	if !oauth.Enabled {
		t.Error("writing the table is the opt-in; enabled should default to true")
	}
	if len(oauth.Scopes) != 1 || oauth.Scopes[0] != "mcp_all" {
		t.Errorf("scopes = %v", oauth.Scopes)
	}
	if oauth.RedirectPort != 33418 {
		t.Errorf("redirect_port = %d", oauth.RedirectPort)
	}
}

// A server with no oauth table keeps the static-credential path — nil, not an
// empty-but-enabled block that would send every existing http server into a
// login flow it never asked for.
func TestMCPSettings_NoOAuthBlockLeavesItOff(t *testing.T) {
	t.Parallel()

	mcp := decodeMCP(t, `
[mcp.docs]
url = "https://example.com/mcp"
`)
	if mcp.Servers[0].OAuth != nil {
		t.Error("a server without an oauth table should not get one")
	}
}

// An explicit disable has to survive, so a configured block can be turned off
// without deleting it.
func TestMCPSettings_OAuthCanBeDisabledInPlace(t *testing.T) {
	t.Parallel()

	mcp := decodeMCP(t, `
[mcp.datadog]
type = "http"
url  = "https://example.com/mcp"

[mcp.datadog.oauth]
enabled = false
scopes  = ["mcp_all"]
`)
	if oauth := mcp.Servers[0].OAuth; oauth == nil || oauth.Enabled {
		t.Errorf("enabled = false was not honored: %+v", oauth)
	}
}

// The store directory is derived from the base dir, not the server table, so the
// CLI, the gateway and `klein mcp login` all reach the same credentials.
func TestMCPServersWithAuthDir_FillsTheStoreDir(t *testing.T) {
	t.Parallel()

	var s Settings
	if _, err := toml.Decode(`
base_dir = "/tmp/klein-test-base"

[mcp.datadog]
type = "http"
url  = "https://example.com/mcp"

[mcp.datadog.oauth]
scopes = ["mcp_all"]

[mcp.plain]
command = "godevmcp"
`, &s); err != nil {
		t.Fatalf("decode: %v", err)
	}

	servers := s.MCPServersWithAuthDir()
	byName := map[string]domain.MCPServerConfig{}
	for _, srv := range servers {
		byName[srv.Name] = srv
	}

	got := byName["datadog"].OAuth
	if got == nil || got.StoreDir != filepath.Join(s.ResolvedBaseDir(), "mcp-auth") {
		t.Errorf("store dir = %+v, want <base>/mcp-auth", got)
	}
	if byName["plain"].OAuth != nil {
		t.Error("a non-oauth server should be untouched")
	}
	// The copy matters: filling the shared settings in place would leak one
	// caller's base dir into another's view of the same slice.
	if s.MCP.Servers[0].OAuth != nil && s.MCP.Servers[0].OAuth.StoreDir != "" {
		t.Error("the original settings were mutated")
	}
}

// OAuth on a stdio server is inert, and an inert credential setting looks
// configured. Reject it instead.
func TestValidateMCPServerConfig_RejectsOAuthOnStdio(t *testing.T) {
	t.Parallel()

	err := ValidateMCPServerConfig(domain.MCPServerConfig{
		Name:    "x",
		Type:    domain.MCPServerTypeStdio,
		Command: "godevmcp",
		OAuth:   &domain.MCPOAuthConfig{Enabled: true},
	})
	if err == nil {
		t.Fatal("oauth on a stdio server should be rejected")
	}
}

// MCPServerTOML renders the oauth block under the server, not at the top level.
//
// The same hazard the env map has: a spec encoded on its own emits a bare
// [oauth] header, which spliced under the server table would read as a
// *top-level* [oauth] and move the block out of the server it configures.
func TestMCPServerTOML_NestsTheOAuthTable(t *testing.T) {
	t.Parallel()

	body, err := MCPServerTOML(domain.MCPServerConfig{
		Name:    "datadog",
		Enabled: true,
		Type:    domain.MCPServerTypeHTTP,
		URL:     exampleMCPURL,
		OAuth:   &domain.MCPOAuthConfig{Enabled: true, Scopes: []string{"mcp_all"}},
	})
	if err != nil {
		t.Fatalf("MCPServerTOML: %v", err)
	}
	if !strings.Contains(string(body), "[mcp.datadog.oauth]") {
		t.Fatalf("the oauth table was not nested under the server:\n%s", body)
	}

	// And it survives a round trip back through the decoder.
	doc, err := tomledit.SetTable(nil, "mcp.datadog", body)
	if err != nil {
		t.Fatalf("SetTable: %v", err)
	}
	mcp := decodeMCP(t, string(doc))
	if oauth := mcp.Servers[0].OAuth; oauth == nil || !oauth.Enabled || len(oauth.Scopes) != 1 {
		t.Errorf("round trip lost the oauth block: %+v", oauth)
	}
}
