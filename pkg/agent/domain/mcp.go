package domain

import (
	"context"
	"fmt"
	"slices"

	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
	"github.com/fpt/klein-cli/pkg/message"
	mcpapi "github.com/mark3labs/mcp-go/mcp"
)

// Package-level logger for MCP domain operations
var logger = pkgLogger.NewComponentLogger("mcp-domain")

// MCPClient represents an MCP (Model Context Protocol) client connection
type MCPClient interface {
	// Connection management
	Start(ctx context.Context) error
	Close() error
	IsInitialized() bool

	// Tool operations
	ListTools(ctx context.Context, request mcpapi.ListToolsRequest) (*mcpapi.ListToolsResult, error)
	CallTool(ctx context.Context, request mcpapi.CallToolRequest) (*mcpapi.CallToolResult, error)

	// Resource operations
	ListResources(ctx context.Context, request mcpapi.ListResourcesRequest) (*mcpapi.ListResourcesResult, error)
	ReadResource(ctx context.Context, request mcpapi.ReadResourceRequest) (*mcpapi.ReadResourceResult, error)

	// Server information
	GetServerCapabilities() mcpapi.ServerCapabilities
	GetSessionId() string
}

// MCPServerConfig represents configuration for connecting to an MCP server
type MCPServerConfig struct {
	// Server identification
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`

	// Connection configuration
	Type    MCPServerType `json:"type"`              // stdio, http, sse
	Command string        `json:"command,omitempty"` // For stdio servers
	Args    []string      `json:"args,omitempty"`    // Command arguments
	Env     []string      `json:"env,omitempty"`     // Environment variables
	URL     string        `json:"url,omitempty"`     // For HTTP/SSE servers

	// Authentication for HTTP/SSE servers. The token is sent as
	// "Authorization: Bearer <token>"; additional headers in Headers are
	// added verbatim. For a server that speaks OAuth instead, see OAuth —
	// the three are alternatives, and OAuth wins when it is enabled.
	AuthorizationToken string            `json:"authorization_token,omitempty"`
	Headers            map[string]string `json:"headers,omitempty"`

	// OAuth turns on the OAuth 2.1 authorization-code flow for an HTTP/SSE
	// server, replacing the static credentials above. Nil (the common case)
	// leaves the server on headers.
	OAuth *MCPOAuthConfig `json:"oauth,omitempty"`

	// Tool filtering
	AllowedTools []string `json:"allowed_tools,omitempty"` // If specified, only these tools will be loaded
}

// MCPOAuthConfig configures the OAuth 2.1 authorization-code flow (PKCE, and
// dynamic client registration when the server offers it) for an HTTP or SSE MCP
// server.
//
// The interactive half is deliberately not automatic: a session that discovers
// it needs authorization says so and stops, and the user runs `klein mcp login
// <server>` once. Anything else would have a scheduled `klein claw` run open a
// browser on a machine nobody is looking at, and block the turn until it timed
// out. Once a token is stored, refresh is automatic and needs no browser.
//
//nolint:tagliatelle // json keys mirror the [mcp.<name>.oauth] TOML keys so the two stay greppable together
type MCPOAuthConfig struct {
	// ClientID identifies a client registered with the authorization server
	// ahead of time. Empty asks for dynamic client registration (RFC 7591) at
	// login, which is what a public CLI client normally wants; the id it is
	// issued is then persisted alongside the token and reused.
	ClientID string `json:"client_id,omitempty"`
	// ClientSecret is for the rare confidential client. A server advertising
	// token_endpoint_auth_methods_supported: ["none"] wants this empty.
	ClientSecret string `json:"client_secret,omitempty"`
	// StoreDir is where the credentials file lives. It is not read from the
	// settings file — the caller fills it from the resolved base dir, so the
	// CLI, the gateway and `klein mcp login` all agree on one location.
	StoreDir string `json:"-"`
	// Scopes requested at authorization. Empty sends no scope parameter and
	// lets the server apply its default.
	Scopes []string `json:"scopes,omitempty"`
	// RedirectPort is the loopback port the login callback listens on. It has
	// to be stable rather than ephemeral: a dynamically registered client is
	// bound to the exact redirect_uri it registered with, so a port that moved
	// between logins would invalidate the registration. Zero means
	// DefaultOAuthRedirectPort.
	RedirectPort int `json:"redirect_port,omitempty"`
	// Enabled is what actually turns the flow on. Kept explicit so a server can
	// carry a configured-but-off oauth block.
	Enabled bool `json:"enabled"`
}

// MCPServerType represents the type of MCP server connection
type MCPServerType string

const (
	MCPServerTypeStdio MCPServerType = "stdio"
	MCPServerTypeSSE   MCPServerType = "sse"
	MCPServerTypeHTTP  MCPServerType = "http"
)

// MCPToolManager manages tools from multiple MCP servers
type MCPToolManager interface {
	ToolManager

	// MCP-specific operations
	AddServer(ctx context.Context, config MCPServerConfig) error
	RemoveServer(serverName string) error
	ListServers() []string
	GetServerInfo(serverName string) (*MCPServerConfig, bool)

	// Tool discovery from MCP servers
	RefreshTools(ctx context.Context) error
	GetMCPTools(serverName string) ([]message.Tool, error)

	// Resource operations
	ListMCPResources(ctx context.Context, serverName string) ([]MCPResource, error)
	ReadMCPResource(ctx context.Context, serverName, resourceURI string) (*MCPResourceContent, error)
}

// MCPResource represents a resource available from an MCP server
type MCPResource struct {
	URI         string            `json:"uri"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	MimeType    string            `json:"mimeType,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	ServerName  string            `json:"serverName"`
}

// MCPResourceContent represents the content of an MCP resource
type MCPResourceContent struct {
	URI        string      `json:"uri"`
	Content    interface{} `json:"content"` // Text, blob, or embedded resource
	MimeType   string      `json:"mimeType,omitempty"`
	ServerName string      `json:"serverName"`
}

// MCPToolAdapter adapts MCP tools to the domain Tool interface
type MCPToolAdapter struct {
	mcpTool    mcpapi.Tool
	serverName string
	client     MCPClient
}

// NewMCPToolAdapter creates a new adapter for an MCP tool
func NewMCPToolAdapter(mcpTool mcpapi.Tool, serverName string, client MCPClient) *MCPToolAdapter {
	return &MCPToolAdapter{
		mcpTool:    mcpTool,
		serverName: serverName,
		client:     client,
	}
}

// RawName returns the original tool name without server prefix
func (a *MCPToolAdapter) RawName() message.ToolName {
	return message.ToolName(a.mcpTool.Name)
}

// Name returns the raw tool name for better LLM compatibility
// Server isolation is handled at the tool manager level
func (a *MCPToolAdapter) Name() message.ToolName {
	return message.ToolName(a.mcpTool.Name)
}

// Description returns the tool description with server context
func (a *MCPToolAdapter) Description() message.ToolDescription {
	return message.ToolDescription(fmt.Sprintf("[%s] %s", a.serverName, a.mcpTool.Description))
}

// Arguments returns the tool arguments converted from MCP schema
func (a *MCPToolAdapter) Arguments() []message.ToolArgument {
	// Convert MCP tool schema to domain ToolArgument format
	var args []message.ToolArgument

	// Extract properties from MCP input schema
	if a.mcpTool.InputSchema.Properties != nil {
		for propName, propSchema := range a.mcpTool.InputSchema.Properties {
			arg := message.ToolArgument{
				Name:        message.ToolName(propName),
				Description: message.ToolDescription(getSchemaDescription(propSchema)),
				Type:        getSchemaType(propSchema),
				Required:    isRequired(propName, a.mcpTool.InputSchema.Required),
			}
			args = append(args, arg)
		}
	}

	return args
}

// Handler returns a handler function that calls the MCP tool
func (a *MCPToolAdapter) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
		// Create MCP CallToolRequest
		request := mcpapi.CallToolRequest{
			Params: mcpapi.CallToolParams{
				Name:      a.mcpTool.Name,
				Arguments: args,
			},
		}

		// Call the MCP tool
		result, err := a.client.CallTool(ctx, request)
		if err != nil {
			return message.NewToolResultError(err.Error()), nil
		}

		// Extract text content from result
		textContent := extractTextFromMCPResult(result)
		return message.NewToolResultText(textContent), nil
	}
}

// Helper functions for schema conversion
func getSchemaDescription(schema interface{}) string {
	if schemaMap, ok := schema.(map[string]interface{}); ok {
		if desc, ok := schemaMap["description"].(string); ok {
			return desc
		}
	}
	return ""
}

func getSchemaType(schema interface{}) string {
	if schemaMap, ok := schema.(map[string]interface{}); ok {
		if schemaType, ok := schemaMap["type"].(string); ok {
			return schemaType
		}
	}
	return "string" // Default to string
}

func isRequired(propName string, required []string) bool {
	return slices.Contains(required, propName)
}

func extractTextFromMCPResult(result *mcpapi.CallToolResult) string {
	if result == nil {
		return ""
	}

	// Handle different content types
	if len(result.Content) > 0 {
		// Try to extract text from the first content item
		firstContent := result.Content[0]

		// Try different content types from MCP API
		switch content := firstContent.(type) {
		case mcpapi.TextContent:
			return content.Text
		default:
			// Also try to access Text field if it exists (for different content implementations)
			if hasText, ok := firstContent.(interface{ GetText() string }); ok {
				return hasText.GetText()
			}

			// For other content types, log and return formatted string
			logger.Warn("Unhandled MCP content type, attempting string conversion", "type", fmt.Sprintf("%T", firstContent))
			return fmt.Sprintf("%v", firstContent)
		}
	}

	// Check if it's an error result
	if result.IsError {
		return "Error: Tool execution failed"
	}

	return ""
}
