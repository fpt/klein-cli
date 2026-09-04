package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	agentmcp "github.com/fpt/klein-cli/pkg/agent/mcp"
)

// MCPLoginOptions selects how a login presents itself.
type MCPLoginOptions struct {
	In  io.Reader
	Out io.Writer
	// Paste skips the loopback listener and asks the user to type the code back.
	Paste bool
	// NoBrowser prints the authorization URL without launching anything.
	NoBrowser bool
}

// RunMCPLogin drives one OAuth login and reports on it.
//
// It lives here rather than in the CLI command so `klein mcp login` and the
// REPL's /mcp login are literally the same code — two implementations of an
// interactive credential flow would drift, and the one used less often would be
// the one that broke.
func RunMCPLogin(ctx context.Context, srv domain.MCPServerConfig, opts MCPLoginOptions) error {
	if srv.OAuth == nil || !srv.OAuth.Enabled {
		return fmt.Errorf("MCP server %q is not configured for OAuth", srv.Name)
	}

	// Printing the URL is not only the NoBrowser path: opening a browser reports
	// that something was launched, not that anyone saw it, so a browser that
	// opened on another desktop — or not at all — would otherwise leave a command
	// that appears to hang with no way forward.
	loginOpts := agentmcp.LoginOptions{OpenURL: func(authURL string) error {
		fmt.Fprintf(opts.Out, "\nAuthorize klein for %q by opening:\n\n  %s\n\n", srv.Name, authURL)
		if opts.NoBrowser {
			return nil
		}
		if err := agentmcp.OpenBrowser(authURL); err != nil {
			fmt.Fprintf(opts.Out, "Could not open a browser (%v). Open the URL above by hand.\n", err)
		}
		return nil
	}}

	if opts.Paste {
		loginOpts.ReadCode = consoleCodeReader(opts.In, opts.Out)
	} else {
		fmt.Fprintf(opts.Out, "Waiting for the browser to complete the login (listening on %s)...\n",
			agentmcp.OAuthRedirectURI(srv.OAuth.RedirectPort))
	}

	if err := agentmcp.Login(ctx, srv, loginOpts); err != nil {
		return fmt.Errorf("logging in to MCP server %q: %w", srv.Name, err)
	}

	fmt.Fprintf(opts.Out, "Logged in to MCP server %q. Credentials stored in %s\n",
		srv.Name, agentmcp.NewCredentialStore(srv.OAuth.StoreDir, srv.Name).Path())
	return nil
}

// consoleCodeReader prompts for the code the authorization server displayed.
func consoleCodeReader(in io.Reader, out io.Writer) func() (string, error) {
	reader := bufio.NewReader(in)
	return func() (string, error) {
		fmt.Fprint(out, "Paste the code (or the whole URL you landed on), then press Enter:\n> ")
		line, err := reader.ReadString('\n')
		// EOF with text already read is a complete final line, not a failure —
		// that is what a code piped in rather than typed looks like.
		if err != nil && (!errors.Is(err, io.EOF) || strings.TrimSpace(line) == "") {
			return "", fmt.Errorf("reading the pasted code: %w", err)
		}
		return line, nil
	}
}
