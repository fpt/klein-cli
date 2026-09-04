package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/fpt/klein-cli/pkg/agent/domain"
)

const (
	cmdMCP     = "mcp"
	cmdMCPList = "list"
)

// handleMCPCommand implements the REPL's /mcp command.
//
// Login belongs in the REPL and not only in `klein mcp login` because that is
// where the need shows up: a session tells the user a server needs authorizing,
// and making them quit, run a second command and start over loses the
// conversation. The credentials land in the same store either way, so a server
// authorized here works everywhere.
//
// What it cannot do is retrofit the tools onto the running session. MCP servers
// are connected once at startup, so a freshly authorized server is reachable
// from the next run, not this one — and saying so is the point of the closing
// message.
func handleMCPCommand(a *Agent, args string) {
	out := a.OutWriter()
	fields := strings.Fields(args)

	switch {
	case len(fields) > 0 && fields[0] == cmdMCPList:
		listMCPServers(a)
	case len(fields) > 0 && fields[0] == "login":
		mcpLoginCommand(a, fields[1:])
	default:
		fmt.Fprintln(out, mcpCommandUsage)
	}
}

const mcpCommandUsage = "Usage: /mcp login <server> [--paste] [--no-browser]\n" +
	"       /mcp list"

// mcpLoginCommand parses the login arguments and runs the flow.
func mcpLoginCommand(a *Agent, args []string) {
	out := a.OutWriter()

	name, paste, noBrowser := parseLoginArgs(args)
	if name == "" {
		fmt.Fprintln(out, mcpCommandUsage)
		return
	}

	srv, ok := a.oauthMCPServer(name)
	if !ok {
		fmt.Fprintf(out, "❌ No MCP server named %q with an [mcp.%s.oauth] block.\n", name, name)
		listMCPServers(a)
		return
	}

	// os.Stdin rather than a reader of the REPL's own: the REPL's reader is
	// mid-loop on this terminal, and taking a line from underneath it would
	// consume the user's next prompt instead of the pasted code.
	if err := RunMCPLogin(context.Background(), srv, MCPLoginOptions{
		Paste: paste, NoBrowser: noBrowser, In: os.Stdin, Out: out,
	}); err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return
	}
	fmt.Fprintf(out, "💡 Restart klein to connect %q with the new credentials.\n", name)
}

// parseLoginArgs pulls the server name and flags out of the argument list.
func parseLoginArgs(args []string) (name string, paste, noBrowser bool) {
	for _, a := range args {
		switch a {
		case "--paste", "--manual":
			paste = true
		case "--no-browser":
			noBrowser = true
		default:
			if !strings.HasPrefix(a, "-") && name == "" {
				name = a
			}
		}
	}
	return name, paste, noBrowser
}

// listMCPServers shows which servers are configured and which of them use OAuth.
func listMCPServers(a *Agent) {
	out := a.OutWriter()
	servers := a.mcpServers()
	if len(servers) == 0 {
		fmt.Fprintln(out, "No MCP servers configured.")
		return
	}
	fmt.Fprintln(out, "MCP servers:")
	for _, s := range servers {
		note := ""
		if s.OAuth != nil && s.OAuth.Enabled {
			note = " (oauth)"
		}
		if !s.Enabled {
			note += " (disabled)"
		}
		fmt.Fprintf(out, "  %s [%s]%s\n", s.Name, s.Type, note)
	}
}

// mcpServers returns the configured servers with their credential-store paths
// resolved, or nothing when the agent was built without settings.
func (a *Agent) mcpServers() []domain.MCPServerConfig {
	if a.settings == nil {
		return nil
	}
	return a.settings.MCPServersWithAuthDir()
}

// oauthMCPServer finds a named server that login can act on.
func (a *Agent) oauthMCPServer(name string) (domain.MCPServerConfig, bool) {
	for _, s := range a.mcpServers() {
		if s.Name == name && s.OAuth != nil && s.OAuth.Enabled {
			return s, true
		}
	}
	return domain.MCPServerConfig{}, false
}
