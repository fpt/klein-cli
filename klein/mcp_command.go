package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/pkg/agent/domain"
)

// runMCPCommand implements the `klein mcp <add|list|remove>` subcommands, which
// edit the MCP servers in the settings file (default ~/.klein/settings.json).
// The `add` form mirrors Claude Code:
//
//	klein mcp add browser-sandbox -- docker run -i --rm chromedp-container-mcp:latest
//	klein mcp add docs --url https://example.com/mcp
//	klein mcp add x -e API_KEY=... -- my-server
func runMCPCommand(args []string) int {
	if len(args) == 0 {
		fmt.Println(mcpUsage)
		return 1
	}

	settingsPath := defaultSettingsPath()
	switch args[0] {
	case "add":
		return mcpAdd(settingsPath, args[1:])
	case "list", "ls":
		return mcpList(settingsPath)
	case "remove", "rm", "delete":
		return mcpRemove(settingsPath, args[1:])
	default:
		fmt.Printf("Unknown mcp subcommand %q.\n\n%s\n", args[0], mcpUsage)
		return 1
	}
}

const mcpUsage = `Usage:
  klein mcp add <name> [-e KEY=VAL ...] [--url <url>] [-t stdio|sse] -- <command> [args...]
  klein mcp list
  klein mcp remove <name>

Examples:
  klein mcp add browser-sandbox -- docker run -i --rm --init --shm-size 1g chromedp-container-mcp:latest
  klein mcp add docs --url https://example.com/mcp

Edits the "mcp" block in ~/.klein/settings.json.`

func defaultSettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".klein", "settings.json")
	}
	return filepath.Join(home, ".klein", "settings.json")
}

// parseMCPAdd turns `mcp add` arguments into an MCPServerConfig (no I/O).
func parseMCPAdd(args []string) (domain.MCPServerConfig, error) {
	var name, url, transport string
	var env []string
	var cmd []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			cmd = args[i+1:]
			i = len(args)
		case a == "-e" || a == "--env":
			if i+1 < len(args) {
				i++
				env = append(env, args[i])
			}
		case a == "--url":
			if i+1 < len(args) {
				i++
				url = args[i]
			}
		case a == "-t" || a == "--transport" || a == "--type":
			if i+1 < len(args) {
				i++
				transport = args[i]
			}
		case strings.HasPrefix(a, "-"):
			// Unknown flag; ignore (keeps parsing forgiving).
		default:
			if name == "" {
				name = a
			}
		}
	}

	if name == "" {
		return domain.MCPServerConfig{}, fmt.Errorf("server name is required")
	}
	srv := domain.MCPServerConfig{Name: name, Enabled: true, Env: env}
	switch {
	case len(cmd) > 0:
		srv.Type = domain.MCPServerTypeStdio
		srv.Command = cmd[0]
		srv.Args = cmd[1:]
	case url != "":
		srv.Type = domain.MCPServerTypeSSE
		srv.URL = url
	default:
		return domain.MCPServerConfig{}, fmt.Errorf("provide a command after `--` (stdio) or `--url <url>` (sse)")
	}
	if transport != "" {
		srv.Type = domain.MCPServerType(transport)
	}
	return srv, nil
}

func mcpAdd(settingsPath string, args []string) int {
	srv, err := parseMCPAdd(args)
	if err != nil {
		fmt.Printf("%v\n\n%s\n", err, mcpUsage)
		return 1
	}
	name := srv.Name

	settings, err := config.LoadSettings(settingsPath)
	if err != nil {
		fmt.Printf("Failed to load settings %s: %v\n", settingsPath, err)
		return 1
	}

	replaced := false
	for i := range settings.MCP.Servers {
		if settings.MCP.Servers[i].Name == name {
			settings.MCP.Servers[i] = srv
			replaced = true
			break
		}
	}
	if !replaced {
		settings.MCP.Servers = append(settings.MCP.Servers, srv)
	}

	if err := settings.Save(); err != nil {
		fmt.Printf("Failed to save settings %s: %v\n", settingsPath, err)
		return 1
	}

	verb := "Added"
	if replaced {
		verb = "Updated"
	}
	target := srv.Command
	if srv.Type == domain.MCPServerTypeSSE {
		target = srv.URL
	}
	fmt.Printf("%s MCP server %q (%s: %s) in %s\n", verb, name, srv.Type, target, settingsPath)
	return 0
}

func mcpList(settingsPath string) int {
	settings, err := config.LoadSettings(settingsPath)
	if err != nil {
		fmt.Printf("Failed to load settings %s: %v\n", settingsPath, err)
		return 1
	}
	if len(settings.MCP.Servers) == 0 {
		fmt.Printf("No MCP servers configured in %s\n", settingsPath)
		return 0
	}
	fmt.Printf("MCP servers in %s:\n", settingsPath)
	for _, s := range settings.MCP.Servers {
		status := ""
		if !s.Enabled {
			status = " (disabled)"
		}
		target := s.Command + " " + strings.Join(s.Args, " ")
		if s.Type == domain.MCPServerTypeSSE || s.Type == domain.MCPServerTypeHTTP {
			target = s.URL
		}
		fmt.Printf("  %s [%s]%s: %s\n", s.Name, s.Type, status, strings.TrimSpace(target))
	}
	return 0
}

func mcpRemove(settingsPath string, args []string) int {
	if len(args) == 0 {
		fmt.Println("Usage: klein mcp remove <name>")
		return 1
	}
	name := args[0]
	settings, err := config.LoadSettings(settingsPath)
	if err != nil {
		fmt.Printf("Failed to load settings %s: %v\n", settingsPath, err)
		return 1
	}
	out := settings.MCP.Servers[:0]
	removed := false
	for _, s := range settings.MCP.Servers {
		if s.Name == name {
			removed = true
			continue
		}
		out = append(out, s)
	}
	if !removed {
		fmt.Printf("No MCP server named %q in %s\n", name, settingsPath)
		return 1
	}
	settings.MCP.Servers = out
	if err := settings.Save(); err != nil {
		fmt.Printf("Failed to save settings %s: %v\n", settingsPath, err)
		return 1
	}
	fmt.Printf("Removed MCP server %q from %s\n", name, settingsPath)
	return 0
}
