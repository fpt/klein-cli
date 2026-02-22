package connectrpc

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/gen/agentv1/agentv1connect"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// StartServer starts the Connect-gRPC HTTP/2 server and blocks until ctx is cancelled.
func StartServer(ctx context.Context, addr string, settings *config.Settings, mcpToolManagers map[string]domain.ToolManager, logger *pkgLogger.Logger) error {
	server := NewAgentServer(settings, mcpToolManagers, logger)

	path, handler := agentv1connect.NewAgentServiceHandler(server)
	mux := http.NewServeMux()
	mux.Handle(path, handler)

	srv := &http.Server{
		Addr:    addr,
		Handler: h2c.NewHandler(mux, &http2.Server{}),
	}

	// Graceful shutdown on context cancellation
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Info("Connect-gRPC server listening", "addr", addr)
	fmt.Printf("klein agent server listening on %s\n", addr)

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}
