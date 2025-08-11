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
	Type    MCPServerType `json:"type"`              // stdio, http, sse, oauth
	Command string        `json:"command,omitempty"` // For stdio servers
	Args    []string      `json:"args,omitempty"`    // Command arguments
	Env     []string      `json:"env,omitempty"`     // Environment variables
	URL     string        `json:"url,omitempty"`     // For HTTP/SSE servers

	// Tool filtering
	AllowedTools []string `json:"allowed_tools,omitempty"` // If specified, only these tools will be loaded
}

// MCPServerType represents the type of MCP server connection
type MCPServerType string

const (
	MCPServerTypeStdio MCPServerType = "stdio"
	MCPServerTypeSSE   MCPServerType = "sse"
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
