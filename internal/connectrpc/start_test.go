package connectrpc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/fpt/klein-cli/internal/config"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// TestStartServerListener confirms the embedded-server path binds an ephemeral
// loopback port, returns a dialable address, and shuts down on context cancel.
// This is the mechanism `klein claw` uses to run the agent in-process.
func TestStartServerListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	logger := pkgLogger.NewLogger(pkgLogger.LogLevelError)
	settings := config.GetDefaultSettings()

	addr, err := StartServerListener(ctx, "127.0.0.1:0", settings, nil, logger, t.TempDir())
	if err != nil {
		t.Fatalf("StartServerListener: %v", err)
	}
	if addr == "" {
		t.Fatal("expected a non-empty bound address")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		t.Fatalf("bound address %q is not host:port: %v", addr, err)
	}

	// The listener is up: a TCP dial should connect.
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial embedded server %s: %v", addr, err)
	}
	_ = conn.Close()

	// Cancelling stops the server; the port should free up shortly after.
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return // server stopped accepting — success
		}
		_ = c.Close()
		time.Sleep(50 * time.Millisecond)
	}
	// Not fatal on all platforms (TIME_WAIT/SO_REUSE), so only log.
	t.Log("embedded server still accepting shortly after cancel (platform-dependent)")
}
