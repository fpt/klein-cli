package agentbackend

import (
	"strings"

	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
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
//
// proxied names the servers klein offers as dynamic tools instead (see
// AppServerSettings.ProxyMCPServers). Those are omitted here: a proxied server
// is already reachable through klein's own connection, and describing it here
// as well would have the backend start a second copy exporting the same tool
// names — a collision resolved by whichever the server happened to see first.
func MCPServersConfig(servers []domain.MCPServerConfig, proxied []string) map[string]any {
	skip := proxySkipper(proxied)
	out := map[string]any{}
	for _, s := range servers {
		if !s.Enabled || s.Name == "" || skip(s.Name) {
			continue
		}
		if entry := serverEntry(s); entry != nil {
			out[s.Name] = entry
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// proxySkipper reports which servers to leave out of the launch table because
// klein proxies them instead. The wildcard short-circuits: it means every
// server, so no per-name bookkeeping can change the answer.
func proxySkipper(proxied []string) func(string) bool {
	skip := make(map[string]bool, len(proxied))
	for _, name := range proxied {
		if name == tool.SelectServersWildcard {
			return func(string) bool { return true }
		}
		skip[name] = true
	}
	return func(name string) bool { return skip[name] }
}

// serverEntry renders one server as codex's launch table entry, or nil for a
// server that describes neither a command nor a url and so cannot be started.
func serverEntry(s domain.MCPServerConfig) map[string]any {
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
		return nil
	}
	return entry
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

// warnUnproxiedStdioServers reports the combination that starts cleanly and then
// answers about the wrong machine: klein dialing an app-server elsewhere, with a
// stdio MCP server passed along as config for that server to launch itself.
//
// It launches there. The binary has to exist on that host, it runs as whoever
// started the server, and a tool like a filesystem search then reports on the
// remote machine while the user reads it as an answer about theirs. Nothing
// fails, which is what makes it worth a line at startup.
//
// A warning rather than an error: a remote host that genuinely has the server
// installed is a real setup, and klein cannot tell the two apart from here.
func warnUnproxiedStdioServers(
	logger *pkgLogger.Logger, servers []domain.MCPServerConfig, proxied []string, address string,
) {
	if logger == nil || address == "" {
		return
	}
	skip := proxySkipper(proxied)
	var remote []string
	for _, s := range servers {
		if launchedRemotely(s, skip) {
			remote = append(remote, s.Name)
		}
	}
	if len(remote) == 0 {
		return
	}
	logger.Warn(
		"stdio MCP servers will be launched by the app-server on its own machine, not here — "+
			"add them to appserver.proxy_mcp_servers to run them locally instead",
		"servers", strings.Join(remote, " "), "address", address)
}

// launchedRemotely reports whether this server would be started by the backend
// on its own machine: enabled, startable, stdio (a url server is reached over
// the network from wherever it is dialed), and not proxied by klein.
func launchedRemotely(s domain.MCPServerConfig, skip func(string) bool) bool {
	return s.Enabled && s.Name != "" && s.Command != "" && !skip(s.Name)
}
