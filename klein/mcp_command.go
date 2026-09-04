package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fpt/klein-cli/internal/app"
	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/config/tomledit"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/agent/mcp"
)

// editSettings applies edit to the settings file's text and writes it back.
//
// The file is spliced rather than decoded and re-encoded, because it is a file
// people write by hand: re-encoding would hand it back stripped of every comment
// and reordered into struct order. A missing file is an empty document, which is
// how `klein mcp add` works before any settings exist.
func editSettings(path string, edit func([]byte) ([]byte, error)) error {
	src, err := os.ReadFile(path) //nolint:gosec // the path is the user's own settings file
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	out, err := edit(src)
	if err != nil {
		return err
	}

	//nolint:gosec // path is the settings file the user named (--settings) or klein's default
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), mkErr)
	}
	//nolint:gosec // same: this is the file the command exists to edit
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// takeSettingsFlag pulls an optional `--settings <path>` out of args, falling
// back to the default location. It is hand-parsed rather than given to a
// FlagSet because `mcp add` ends in a `--` separated command line that a FlagSet
// would try to interpret.
func takeSettingsFlag(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break // everything after this belongs to the server being added
		}
		if args[i] != "--settings" {
			continue
		}
		if i+1 >= len(args) {
			break // no value; fall through to the default and let usage explain
		}
		rest := append(append([]string{}, args[:i]...), args[i+2:]...)
		return args[i+1], rest
	}
	return defaultSettingsPath(), args
}

// runMCPCommand implements the `klein mcp <add|list|remove>` subcommands, which
// edit the MCP servers in the settings file (default ~/.klein/settings.toml).
// The `add` form mirrors Claude Code:
//
//	klein mcp add browser-sandbox -- docker run -i --rm chromedp-container-mcp:latest
//	klein mcp add docs --url https://example.com/mcp
//	klein mcp add x -e API_KEY=... -- my-server
func runMCPCommand(args []string) int {
	settingsPath, args := takeSettingsFlag(args)
	if len(args) == 0 {
		fmt.Println(mcpUsage)
		return 1
	}

	switch args[0] {
	case "add":
		return mcpAdd(settingsPath, args[1:])
	case "list", "ls":
		return mcpList(settingsPath)
	case "remove", "rm", "delete":
		return mcpRemove(settingsPath, args[1:])
	case "login":
		return mcpLogin(settingsPath, args[1:])
	case "logout":
		return mcpLogout(settingsPath, args[1:])
	default:
		fmt.Printf("Unknown mcp subcommand %q.\n\n%s\n", args[0], mcpUsage)
		return 1
	}
}

const mcpUsage = `Usage:
  klein mcp add <name> [-e KEY=VAL ...] [--url <url>] [-t stdio|sse] -- <command> [args...]
  klein mcp list
  klein mcp remove <name>
  klein mcp login <name> [--paste] [--no-browser]
  klein mcp logout <name>

Examples:
  klein mcp add browser-sandbox -- docker run -i --rm --init --shm-size 1g chromedp-container-mcp:latest
  klein mcp add docs --url https://example.com/mcp
  klein mcp login datadog

Edits the [mcp.*] tables in ~/.klein/settings.toml, or in the file named by
--settings. Comments and formatting elsewhere in the file are left alone.

login runs the OAuth authorization-code flow for a server whose table has an
[mcp.<name>.oauth] block, and stores the token under <base_dir>/mcp-auth. It is
a one-time step: after it, refresh happens on its own.

--no-browser prints the URL instead of opening one. --paste skips the loopback
listener entirely and asks you to type back the code the server showed you --
use it for a server that displays a one-time code rather than redirecting, or
when the browser is on a different machine than klein.`

func defaultSettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".klein", "settings.toml")
	}
	return filepath.Join(home, ".klein", "settings.toml")
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
			replaced = true
			break
		}
	}

	body, err := config.MCPServerTOML(srv)
	if err != nil {
		fmt.Printf("%v\n", err)
		return 1
	}
	if err := editSettings(settingsPath, func(src []byte) ([]byte, error) {
		return tomledit.SetTable(src, "mcp."+name, body)
	}); err != nil {
		fmt.Printf("Failed to update settings %s: %v\n", settingsPath, err)
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
	found := false
	for _, s := range settings.MCP.Servers {
		if s.Name == name {
			found = true
			break
		}
	}
	if !found {
		fmt.Printf("No MCP server named %q in %s\n", name, settingsPath)
		return 1
	}

	if err := editSettings(settingsPath, func(src []byte) ([]byte, error) {
		out, _, err := tomledit.DeleteTable(src, "mcp."+name)
		if err != nil {
			return nil, fmt.Errorf("removing the server's table: %w", err)
		}
		return out, nil
	}); err != nil {
		fmt.Printf("Failed to update settings %s: %v\n", settingsPath, err)
		return 1
	}
	fmt.Printf("Removed MCP server %q from %s\n", name, settingsPath)
	return 0
}

// findOAuthServer locates a server by name and checks it is one login can act
// on, returning the config with its credential-store directory filled in.
func findOAuthServer(settingsPath, name string) (domain.MCPServerConfig, error) {
	settings, err := config.LoadSettings(settingsPath)
	if err != nil {
		return domain.MCPServerConfig{}, fmt.Errorf("failed to load settings %s: %w", settingsPath, err)
	}
	for _, s := range settings.MCPServersWithAuthDir() {
		if s.Name != name {
			continue
		}
		if s.OAuth == nil || !s.OAuth.Enabled {
			return domain.MCPServerConfig{}, fmt.Errorf(
				"MCP server %q has no [mcp.%s.oauth] block; add one to use OAuth", name, name)
		}
		return s, nil
	}
	return domain.MCPServerConfig{}, fmt.Errorf("no MCP server named %q in %s", name, settingsPath)
}

func mcpLogin(settingsPath string, args []string) int {
	var name string
	noBrowser, paste := false, false
	for _, a := range args {
		switch {
		case a == "--no-browser":
			noBrowser = true
		case a == "--paste", a == "--manual":
			paste = true
		case strings.HasPrefix(a, "-"):
			// Unknown flag; ignore, matching the other mcp subcommands.
		case name == "":
			name = a
		}
	}
	if name == "" {
		fmt.Println("Usage: klein mcp login <name> [--paste] [--no-browser]")
		return 1
	}

	srv, err := findOAuthServer(settingsPath, name)
	if err != nil {
		fmt.Printf("%v\n", err)
		return 1
	}
	if err := app.RunMCPLogin(context.Background(), srv, app.MCPLoginOptions{
		Paste: paste, NoBrowser: noBrowser, In: os.Stdin, Out: os.Stdout,
	}); err != nil {
		fmt.Printf("Login failed: %v\n", err)
		return 1
	}
	return 0
}

func mcpLogout(settingsPath string, args []string) int {
	if len(args) == 0 {
		fmt.Println("Usage: klein mcp logout <name>")
		return 1
	}
	name := args[0]

	srv, err := findOAuthServer(settingsPath, name)
	if err != nil {
		fmt.Printf("%v\n", err)
		return 1
	}

	// The whole file goes, client registration included. A logout that kept the
	// registration would leave klein re-authorizing as a client whose consent
	// the user just withdrew.
	path := mcp.NewCredentialStore(srv.OAuth.StoreDir, name).Path()
	//nolint:gosec // G703: the path is <base>/mcp-auth/<sanitized name>.json, built by the store
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("MCP server %q was not logged in\n", name)
			return 0
		}
		fmt.Printf("Failed to remove %s: %v\n", path, err)
		return 1
	}
	fmt.Printf("Logged out of MCP server %q (removed %s)\n", name, path)
	return 0
}
