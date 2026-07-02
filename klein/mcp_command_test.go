package main

import (
	"strings"
	"testing"

	"github.com/fpt/klein-cli/pkg/agent/domain"
)

func TestParseMCPAdd(t *testing.T) {
	// Claude-Code style: name -- command args...
	srv, err := parseMCPAdd(strings.Fields("browser-sandbox -- docker run -i --rm chromedp-container-mcp:latest"))
	if err != nil {
		t.Fatalf("stdio: %v", err)
	}
	if srv.Name != "browser-sandbox" || srv.Type != domain.MCPServerTypeStdio ||
		srv.Command != "docker" || len(srv.Args) != 4 || !srv.Enabled {
		t.Errorf("stdio parse wrong: %+v", srv)
	}

	// SSE via --url.
	srv, err = parseMCPAdd([]string{"docs", "--url", "https://example.com/mcp"})
	if err != nil || srv.Type != domain.MCPServerTypeSSE || srv.URL != "https://example.com/mcp" {
		t.Errorf("sse parse wrong: %+v err %v", srv, err)
	}

	// env flags.
	srv, err = parseMCPAdd([]string{"x", "-e", "A=1", "-e", "B=2", "--", "srv"})
	if err != nil || len(srv.Env) != 2 || srv.Env[0] != "A=1" {
		t.Errorf("env parse wrong: %+v err %v", srv, err)
	}

	// Errors: no name, no command/url.
	if _, err := parseMCPAdd([]string{"--", "docker"}); err == nil {
		t.Error("expected error when name missing")
	}
	if _, err := parseMCPAdd([]string{"x"}); err == nil {
		t.Error("expected error when neither command nor url given")
	}
}
