package mcp

import (
	"context"
	"fmt"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
	"github.com/mark3labs/mcp-go/client"
	mcpapi "github.com/mark3labs/mcp-go/mcp"
)

// Package-level logger for MCP client operations
var logger = pkgLogger.NewComponentLogger("mcp-client")

// MCPClientWrapper wraps the mcp-go client to implement domain.MCPClient
type MCPClientWrapper struct {
	client *client.Client
	config domain.MCPServerConfig
}

// NewMCPClient creates a new MCP client based on the server configuration
func NewMCPClient(config domain.MCPServerConfig) (*MCPClientWrapper, error) {
	var mcpClient *client.Client
	var err error

	switch config.Type {
	case domain.MCPServerTypeStdio:
		mcpClient, err = client.NewStdioMCPClient(config.Command, config.Env, config.Args...)
		if err != nil {
			return nil, fmt.Errorf("failed to create stdio MCP client: %w", err)
		}

	case domain.MCPServerTypeSSE:
		if config.URL == "" {
			return nil, fmt.Errorf("URL is required for SSE MCP server")
		}
		mcpClient, err = client.NewSSEMCPClient(config.URL)
		if err != nil {
			return nil, fmt.Errorf("failed to create SSE MCP client: %w", err)
		}

	default:
		return nil, fmt.Errorf("unsupported MCP server type: %s", config.Type)
	}

	return &MCPClientWrapper{
		client: mcpClient,
		config: config,
	}, nil
}

// Start initializes the MCP client connection
func (w *MCPClientWrapper) Start(ctx context.Context) error {
	// Start the client connection
	if err := w.client.Start(ctx); err != nil {
		return fmt.Errorf("failed to start MCP client: %w", err)
	}

	// Initialize the client
	initRequest := mcpapi.InitializeRequest{
		Params: mcpapi.InitializeParams{
			ProtocolVersion: "2024-11-05",
			Capabilities:    mcpapi.ClientCapabilities{
				// Leave empty for now
			},
			ClientInfo: mcpapi.Implementation{
				Name:    "klein",
				Version: "1.0.0",
			},
		},
	}

	_, err := w.client.Initialize(ctx, initRequest)
	if err != nil {
		return fmt.Errorf("failed to initialize MCP client: %w", err)
	}

	logger.InfoWithIntention(pkgLogger.IntentionSuccess, "Successfully connected to MCP server", "server", w.config.Name)
	return nil
}

// Close closes the MCP client connection
func (w *MCPClientWrapper) Close() error {
	return w.client.Close()
}

// IsInitialized returns true if the client is initialized
func (w *MCPClientWrapper) IsInitialized() bool {
	return w.client.IsInitialized()
}

// ListTools lists available tools from the MCP server
func (w *MCPClientWrapper) ListTools(ctx context.Context, request mcpapi.ListToolsRequest) (*mcpapi.ListToolsResult, error) {
	return w.client.ListTools(ctx, request)
}

// CallTool calls a tool on the MCP server
func (w *MCPClientWrapper) CallTool(ctx context.Context, request mcpapi.CallToolRequest) (*mcpapi.CallToolResult, error) {
	return w.client.CallTool(ctx, request)
}

// ListResources lists available resources from the MCP server
func (w *MCPClientWrapper) ListResources(ctx context.Context, request mcpapi.ListResourcesRequest) (*mcpapi.ListResourcesResult, error) {
	return w.client.ListResources(ctx, request)
}

// ReadResource reads a resource from the MCP server
func (w *MCPClientWrapper) ReadResource(ctx context.Context, request mcpapi.ReadResourceRequest) (*mcpapi.ReadResourceResult, error) {
	return w.client.ReadResource(ctx, request)
}

// GetServerCapabilities returns the server capabilities
func (w *MCPClientWrapper) GetServerCapabilities() mcpapi.ServerCapabilities {
	return w.client.GetServerCapabilities()
}

// GetSessionId returns the session ID
func (w *MCPClientWrapper) GetSessionId() string {
	return w.client.GetSessionId()
}

// GetConfig returns the server configuration
func (w *MCPClientWrapper) GetConfig() domain.MCPServerConfig {
	return w.config
}
