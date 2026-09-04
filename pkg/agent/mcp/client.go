package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpapi "github.com/mark3labs/mcp-go/mcp"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
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
		if mcpClient, err = newSSEClient(config); err != nil {
			return nil, err
		}

	case domain.MCPServerTypeHTTP:
		if mcpClient, err = newHTTPClient(config); err != nil {
			return nil, err
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
	// Checked before dialing: an OAuth server with nothing stored fails deep in
	// the transport with "authorization required", which reads like a rejected
	// credential rather than a missing one. The action is different — log in
	// once, rather than fix the settings — so it is worth saying plainly.
	if usesOAuth(w.config) && !HasOAuthCredentials(ctx, w.config) {
		return fmt.Errorf("MCP server %q: %w", w.config.Name, ErrOAuthLoginRequired)
	}

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

// newSSEClient builds the SSE transport, with OAuth when the server asks for it.
//
// Headers reach both branches. This transport used to send none at all, so an
// sse server with an authorization_token was documented as authenticating and
// quietly did not — and under OAuth the same omission would have dropped the
// API-gateway headers that buildAuthHeaders goes out of its way to keep.
func newSSEClient(config domain.MCPServerConfig) (*client.Client, error) {
	return buildClient(config, "SSE",
		transport.WithHeaders, client.NewOAuthSSEClient, client.NewSSEMCPClient)
}

// newHTTPClient builds the streamable-HTTP transport.
func newHTTPClient(config domain.MCPServerConfig) (*client.Client, error) {
	return buildClient(config, "HTTP",
		transport.WithHTTPHeaders, client.NewOAuthStreamableHttpClient, client.NewStreamableHttpClient)
}

// buildClient is the shape both transports share: require a URL, assemble the
// headers, then pick the OAuth or the plain constructor.
//
// Generic over the option type only because mcp-go gives SSE and streamable
// HTTP separate ones; nothing about the decision differs between them, and
// writing it twice is how the SSE branch silently lost its headers to begin
// with.
func buildClient[O any](
	config domain.MCPServerConfig,
	kind string,
	withHeaders func(map[string]string) O,
	newOAuth func(string, client.OAuthConfig, ...O) (*client.Client, error),
	newPlain func(string, ...O) (*client.Client, error),
) (*client.Client, error) {
	if config.URL == "" {
		return nil, fmt.Errorf("URL is required for %s MCP server", kind)
	}

	var opts []O
	if headers := buildAuthHeaders(config); len(headers) > 0 {
		opts = append(opts, withHeaders(headers))
	}

	var (
		mcpClient *client.Client
		err       error
	)
	if usesOAuth(config) {
		oauthCfg, _, cfgErr := oauthConfig(config)
		if cfgErr != nil {
			return nil, cfgErr
		}
		mcpClient, err = newOAuth(config.URL, oauthCfg, opts...)
	} else {
		mcpClient, err = newPlain(config.URL, opts...)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create %s MCP client: %w", kind, err)
	}
	return mcpClient, nil
}

// usesOAuth reports whether a server should authenticate with the OAuth flow
// rather than with the static credentials.
func usesOAuth(config domain.MCPServerConfig) bool {
	return config.OAuth != nil && config.OAuth.Enabled
}

// buildAuthHeaders composes the HTTP header map from the server config:
// AuthorizationToken is sent as a Bearer token; Headers entries are added verbatim.
// Returns nil when nothing is set.
//
// Under OAuth the static token is dropped rather than merged: the OAuth handler
// sets Authorization from the token it manages, and a second, stale value for
// the same header is the kind of conflict that produces a 401 nobody can
// explain. Other headers are kept — a server can want both an API-gateway header
// and an OAuth token.
func buildAuthHeaders(config domain.MCPServerConfig) map[string]string {
	token := config.AuthorizationToken
	if usesOAuth(config) {
		token = ""
	}
	if token == "" && len(config.Headers) == 0 {
		return nil
	}
	headers := make(map[string]string, len(config.Headers)+1)
	for k, v := range config.Headers {
		headers[k] = v
	}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return headers
}
