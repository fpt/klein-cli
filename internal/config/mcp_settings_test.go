package config

import (
	"encoding/json"
	"testing"
)

// TestMCPMapShape verifies the Claude-Code/Cursor map shape parses, with
// enabled defaulting to true and type inferred from command/url.
func TestMCPMapShape(t *testing.T) {
	data := `{
	  "browser-sandbox": { "command": "docker", "args": ["run", "-i", "--rm"] },
	  "docs":            { "url": "https://example.com/mcp" },
	  "off":             { "command": "x", "enabled": false }
	}`
	var m MCPSettings
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	byName := map[string]int{}
	for i, s := range m.Servers {
		byName[s.Name] = i
	}
	if len(m.Servers) != 3 {
		t.Fatalf("got %d servers, want 3: %+v", len(m.Servers), m.Servers)
	}

	bs := m.Servers[byName["browser-sandbox"]]
	if bs.Command != "docker" || string(bs.Type) != "stdio" || !bs.Enabled {
		t.Errorf("browser-sandbox wrong: %+v", bs)
	}
	docs := m.Servers[byName["docs"]]
	if docs.URL != "https://example.com/mcp" || string(docs.Type) != "sse" || !docs.Enabled {
		t.Errorf("docs wrong: %+v", docs)
	}
	if m.Servers[byName["off"]].Enabled {
		t.Errorf("explicit enabled:false should be respected")
	}
}

// TestMCPEnvMap verifies env is accepted as a Claude-Code map and converted to
// "KEY=VAL" entries.
func TestMCPEnvMap(t *testing.T) {
	var m MCPSettings
	if err := json.Unmarshal([]byte(`{"x":{"command":"s","env":{"A":"1","B":"2"}}}`), &m); err != nil {
		t.Fatal(err)
	}
	env := m.Servers[0].Env
	if len(env) != 2 || env[0] != "A=1" || env[1] != "B=2" {
		t.Errorf("env: got %v, want [A=1 B=2] (sorted)", env)
	}
}

// TestMCPRoundTrip verifies marshaling emits the map shape again.
func TestMCPRoundTrip(t *testing.T) {
	var m MCPSettings
	_ = json.Unmarshal([]byte(`{"x":{"command":"docker","args":["run"]}}`), &m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out) || !contains(string(out), `"x"`) || !contains(string(out), `"stdio"`) {
		t.Errorf("round-trip did not emit map shape: %s", out)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && indexOf(s, sub) >= 0 }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
