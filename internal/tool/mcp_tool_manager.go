package tool

import (
	"context"
	"fmt"
	"sync"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/agent/mcp"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
	"github.com/fpt/klein-cli/pkg/message"
	mcpapi "github.com/mark3labs/mcp-go/mcp"
)

// MCPEnhancedToolManager provides MCP server support with basic tool management
type MCPEnhancedToolManager struct {
	// Basic tool management
	tools map[message.ToolName]message.Tool

	// MCP server management
	servers map[string]*mcp.MCPClientWrapper
	configs map[string]domain.MCPServerConfig

	// Thread safety
	mu sync.RWMutex

	// MCP tools cache
	mcpTools map[string][]message.Tool // serverName -> tools
}

// NewMCPEnhancedToolManager creates a new tool manager with MCP support
func NewMCPEnhancedToolManager() *MCPEnhancedToolManager {
	return &MCPEnhancedToolManager{
		tools:    make(map[message.ToolName]message.Tool),
		servers:  make(map[string]*mcp.MCPClientWrapper),
		configs:  make(map[string]domain.MCPServerConfig),
		mcpTools: make(map[string][]message.Tool),
	}
}

// AddServer adds and connects to an MCP server
func (m *MCPEnhancedToolManager) AddServer(ctx context.Context, config domain.MCPServerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if server already exists
	if _, exists := m.servers[config.Name]; exists {
		return fmt.Errorf("server %s already exists", config.Name)
	}

	// Create MCP client
	mcpClient, err := mcp.NewMCPClient(config)
	if err != nil {
		return fmt.Errorf("failed to create MCP client for %s: %w", config.Name, err)
	}

	// Start the client connection
	if err := mcpClient.Start(ctx); err != nil {
		return fmt.Errorf("failed to start MCP server %s: %w", config.Name, err)
	}

	// Store the client and config
	m.servers[config.Name] = mcpClient
	m.configs[config.Name] = config

	// Load tools from the server
	if err := m.loadToolsFromServer(ctx, config.Name, mcpClient); err != nil {
		logger.Warn("Failed to load tools from MCP server",
			"server", config.Name, "error", err)
	}

	return nil
}

// RemoveServer removes an MCP server
func (m *MCPEnhancedToolManager) RemoveServer(serverName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Close the client connection
	if client, exists := m.servers[serverName]; exists {
		if err := client.Close(); err != nil {
			logger.Warn("Error closing MCP server connection",
				"server", serverName, "error", err)
		}
	}

	// Remove from maps
	delete(m.servers, serverName)
	delete(m.configs, serverName)
	delete(m.mcpTools, serverName)

	// Remove all tools from this server from the main tool manager
	m.removeMCPToolsFromServer(serverName)

	logger.DebugWithIntention(pkgLogger.IntentionStatus, "MCP server removed", "server", serverName)
	return nil
}

// ListServers returns a list of connected MCP servers
func (m *MCPEnhancedToolManager) ListServers() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	servers := make([]string, 0, len(m.servers))
	for serverName := range m.servers {
		servers = append(servers, serverName)
	}
	return servers
}

// GetServerInfo returns information about an MCP server
func (m *MCPEnhancedToolManager) GetServerInfo(serverName string) (*domain.MCPServerConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	config, exists := m.configs[serverName]
	if !exists {
		return nil, false
	}
	return &config, true
}

// RefreshTools refreshes tools from all connected MCP servers
func (m *MCPEnhancedToolManager) RefreshTools(ctx context.Context) error {
	m.mu.RLock()
	servers := make([]string, 0, len(m.servers))
	for serverName := range m.servers {
		servers = append(servers, serverName)
	}
	m.mu.RUnlock()

	var lastError error
	for _, serverName := range servers {
		if err := m.refreshToolsFromServer(ctx, serverName); err != nil {
			logger.Warn("Failed to refresh tools from MCP server",
				"server", serverName, "error", err)
			lastError = err
		}
	}

	return lastError
}

// GetMCPTools returns tools from a specific MCP server
func (m *MCPEnhancedToolManager) GetMCPTools(serverName string) ([]message.Tool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tools, exists := m.mcpTools[serverName]
	if !exists {
		return nil, fmt.Errorf("server %s not found", serverName)
	}

	return tools, nil
}

// ListMCPResources lists resources from an MCP server
func (m *MCPEnhancedToolManager) ListMCPResources(ctx context.Context, serverName string) ([]domain.MCPResource, error) {
	m.mu.RLock()
	client, exists := m.servers[serverName]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("server %s not found", serverName)
	}

	// List resources from MCP server
	request := mcpapi.ListResourcesRequest{}
	result, err := client.ListResources(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to list resources from server %s: %w", serverName, err)
	}

	// Convert to domain format
	resources := make([]domain.MCPResource, 0, len(result.Resources))
	for _, res := range result.Resources {
		resource := domain.MCPResource{
			URI:         res.URI,
			Name:        res.Name,
			Description: res.Description,
			MimeType:    res.MIMEType,
			ServerName:  serverName,
		}

		// Convert annotations (skip for now due to complex structure)
		resource.Annotations = make(map[string]string)

		resources = append(resources, resource)
	}

	return resources, nil
}

// ReadMCPResource reads a resource from an MCP server
func (m *MCPEnhancedToolManager) ReadMCPResource(ctx context.Context, serverName, resourceURI string) (*domain.MCPResourceContent, error) {
	m.mu.RLock()
	client, exists := m.servers[serverName]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("server %s not found", serverName)
	}

	// Read resource from MCP server
	request := mcpapi.ReadResourceRequest{
		Params: mcpapi.ReadResourceParams{
			URI: resourceURI,
		},
	}

	result, err := client.ReadResource(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to read resource from server %s: %w", serverName, err)
	}

	// Convert to domain format
	content := &domain.MCPResourceContent{
		URI:        resourceURI,
		ServerName: serverName,
	}

	// Extract content from the result
	if len(result.Contents) > 0 {
		firstContent := result.Contents[0]
		content.Content = firstContent

		// Try to extract MIME type
		if textContent, ok := firstContent.(*mcpapi.TextResourceContents); ok {
			content.MimeType = textContent.MIMEType
		} else if blobContent, ok := firstContent.(*mcpapi.BlobResourceContents); ok {
			content.MimeType = blobContent.MIMEType
		}
	}

	return content, nil
}

// loadToolsFromServer loads tools from an MCP server and registers them
func (m *MCPEnhancedToolManager) loadToolsFromServer(ctx context.Context, serverName string, client *mcp.MCPClientWrapper) error {
	// List tools from the server
	request := mcpapi.ListToolsRequest{}
	result, err := client.ListTools(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to list tools: %w", err)
	}

	// Get server config for allowed tools filtering
	config, exists := m.configs[serverName]
	if !exists {
		return fmt.Errorf("server config not found for %s", serverName)
	}

	// Create allow set for efficient lookup
	var allowedToolsSet map[string]bool
	if len(config.AllowedTools) > 0 {
		allowedToolsSet = make(map[string]bool, len(config.AllowedTools))
		for _, toolName := range config.AllowedTools {
			allowedToolsSet[toolName] = true
		}
		logger.DebugWithIntention(pkgLogger.IntentionTool, "MCP server tool filtering enabled",
			"server", serverName,
			"allowed_count", len(config.AllowedTools),
			"allowed_tools", config.AllowedTools)
	}

	// Convert MCP tools to domain tools and register them
	tools := make([]message.Tool, 0, len(result.Tools))
	filteredCount := 0
	for _, mcpTool := range result.Tools {
		// Apply allow list filtering if configured
		if allowedToolsSet != nil && !allowedToolsSet[mcpTool.Name] {
			filteredCount++
			continue
		}

		// Create tool adapter
		adapter := domain.NewMCPToolAdapter(mcpTool, serverName, client)
		tools = append(tools, adapter)

		// Register the tool in the main tool manager
		m.tools[adapter.Name()] = adapter
		logger.DebugWithIntention(pkgLogger.IntentionTool, "MCP tool registered",
			"server", serverName, "tool", adapter.Name())
	}

	// Store tools for this server
	m.mcpTools[serverName] = tools

	if filteredCount > 0 {
		logger.InfoWithIntention(pkgLogger.IntentionTool, "MCP tools loaded with filtering",
			"server", serverName,
			"loaded_count", len(tools),
			"filtered_count", filteredCount)
	} else {
		logger.InfoWithIntention(pkgLogger.IntentionTool, "MCP tools loaded",
			"server", serverName, "count", len(tools))
	}
	return nil
}

// refreshToolsFromServer refreshes tools from a specific server
func (m *MCPEnhancedToolManager) refreshToolsFromServer(ctx context.Context, serverName string) error {
	m.mu.Lock()
	client, exists := m.servers[serverName]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("server %s not found", serverName)
	}

	// Remove existing tools from this server
	m.removeMCPToolsFromServer(serverName)
	m.mu.Unlock()

	// Reload tools
	return m.loadToolsFromServer(ctx, serverName, client)
}

// removeMCPToolsFromServer removes all tools from a specific server
func (m *MCPEnhancedToolManager) removeMCPToolsFromServer(serverName string) {
	// Get the stored tools for this server
	if tools, exists := m.mcpTools[serverName]; exists {
		for _, tool := range tools {
			// Remove the tool by its registered name
			delete(m.tools, tool.Name())
			logger.DebugWithIntention(pkgLogger.IntentionDebug, "MCP tool removed",
				"server", serverName, "tool", tool.Name())
		}
	}
}

// Basic tool manager interface methods

// RegisterTool registers a new tool with the manager
func (m *MCPEnhancedToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Create a simple tool implementation
	tool := &mcpTool{
		name:        name,
		description: description,
		arguments:   arguments,
		handler:     handler,
	}
	m.tools[name] = tool
}

// GetTools returns all available tools (both MCP and locally registered)
func (m *MCPEnhancedToolManager) GetTools() map[message.ToolName]message.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tools
}

// GetTool retrieves a specific tool by name
func (m *MCPEnhancedToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tool, exists := m.tools[name]
	return tool, exists
}

// CallTool executes a tool with given arguments
func (m *MCPEnhancedToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	tool, exists := m.GetTool(name)
	if !exists {
		return message.NewToolResultError(fmt.Sprintf("tool '%s' not found", name)), nil
	}

	handler := tool.Handler()
	return handler(ctx, args)
} // mcpTool implements the domain.Tool interface for locally registered tools
type mcpTool struct {
	name        message.ToolName
	description message.ToolDescription
	arguments   []message.ToolArgument
	handler     func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)
}

func (t *mcpTool) RawName() message.ToolName {
	return t.name
}

func (t *mcpTool) Name() message.ToolName {
	return t.name
}

func (t *mcpTool) Description() message.ToolDescription {
	return t.description
}

func (t *mcpTool) Arguments() []message.ToolArgument {
	return t.arguments
}

func (t *mcpTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return t.handler
}
