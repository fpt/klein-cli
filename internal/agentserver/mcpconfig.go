package agentserver

import (
	"strings"

	"github.com/fpt/klein-cli/pkg/agent/domain"
)

// MCPServersConfig translates klein's configured MCP servers into codex's
// `mcp_servers` config table (suitable for ThreadStartOptions.Config), so a
// codex-backed turn can reach the same external MCP servers klein uses. Only
// enabled servers are included: stdio servers map to command/args/env; url
// servers map to a url entry.
//
// Note: codex only supports streamable-HTTP url servers (SSE is deprecated);
// an SSE-typed server is still emitted as a url and left to codex to accept or
// reject.
func MCPServersConfig(servers []domain.MCPServerConfig) map[string]any {
	out := map[string]any{}
	for _, s := range servers {
		if !s.Enabled || s.Name == "" {
			continue
		}
		entry := map[string]any{}
		switch {
		case s.Command != "":
			entry["command"] = s.Command
			if len(s.Args) > 0 {
				entry["args"] = s.Args
			}
			if env := envMap(s.Env); len(env) > 0 {
				entry["env"] = env
			}
		case s.URL != "":
			entry["url"] = s.URL
		default:
			continue
		}
		out[s.Name] = entry
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// envMap converts "KEY=VAL" entries into a table (codex expects a map).
func envMap(kv []string) map[string]string {
	if len(kv) == 0 {
		return nil
	}
	m := make(map[string]string, len(kv))
	for _, e := range kv {
		if i := strings.IndexByte(e, '='); i >= 0 {
			m[e[:i]] = e[i+1:]
		} else {
			m[e] = ""
		}
	}
	return m
}
