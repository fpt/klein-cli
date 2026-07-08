package connectrpc

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/gen/agentv1/agentv1connect"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// buildServer constructs the h2c HTTP server for the agent service. Addr is left
// unset so callers can either ListenAndServe (StartServer) or bind a listener
// themselves (StartServerListener).
func buildServer(
	settings *config.Settings, mcpToolManagers map[string]domain.ToolManager, logger *pkgLogger.Logger,
	sessionsDir string, agentBackend domain.AgentBackend,
) *http.Server {
	server := NewAgentServer(settings, mcpToolManagers, logger, sessionsDir, agentBackend)

	path, handler := agentv1connect.NewAgentServiceHandler(server)
	mux := http.NewServeMux()
	mux.Handle(path, handler)

	return &http.Server{
		Handler: h2c.NewHandler(mux, &http2.Server{}),
	}
}

// shutdownOnCancel gracefully shuts srv down when ctx is cancelled.
func shutdownOnCancel(ctx context.Context, srv *http.Server) {
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

// StartServer starts the Connect-gRPC HTTP/2 server and blocks until ctx is cancelled.
func StartServer(
	ctx context.Context, addr string, settings *config.Settings, mcpToolManagers map[string]domain.ToolManager,
	logger *pkgLogger.Logger, sessionsDir string, agentBackend domain.AgentBackend,
) error {
	srv := buildServer(settings, mcpToolManagers, logger, sessionsDir, agentBackend)
	srv.Addr = addr

	go shutdownOnCancel(ctx, srv)

	logger.Info("Connect-gRPC server listening", "addr", addr)
	fmt.Printf("klein agent server listening on %s\n", addr)

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}

// StartServerListener binds addr, serves the agent in a background goroutine, and
// returns the actual listen address. Pass "127.0.0.1:0" for an ephemeral local
// port — right for an in-process embedded server (e.g. `klein claw`) where the
// caller dials the returned address. Serving stops when ctx is cancelled.
func StartServerListener(
	ctx context.Context, addr string, settings *config.Settings, mcpToolManagers map[string]domain.ToolManager,
	logger *pkgLogger.Logger, sessionsDir string, agentBackend domain.AgentBackend,
) (string, error) {
	srv := buildServer(settings, mcpToolManagers, logger, sessionsDir, agentBackend)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	go shutdownOnCancel(ctx, srv)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.Error("Embedded agent server error", "error", err)
		}
	}()

	bound := ln.Addr().String()
	logger.Info("Connect-gRPC server listening (embedded)", "addr", bound)
	return bound, nil
}
