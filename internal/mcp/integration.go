package mcp

import (
	"context"
	"fmt"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
	"github.com/fpt/klein-cli/pkg/message"
)

// Package-level logger for MCP integration operations
var logger = pkgLogger.NewComponentLogger("mcp-integration")

// Integration manages MCP server connections and integrates them with the tool system
type Integration struct {
	toolManager *tool.MCPEnhancedToolManager
}

// NewIntegration creates a new MCP integration
func NewIntegration() *Integration {
	return &Integration{
		toolManager: tool.NewMCPEnhancedToolManager(),
	}
}

// GetToolManager returns the MCP-enhanced tool manager
func (i *Integration) GetToolManager() domain.ToolManager {
	return i.toolManager
}

// AddServer dynamically adds a new MCP server
func (i *Integration) AddServer(ctx context.Context, serverConfig domain.MCPServerConfig) error {
	// Validate configuration
	if err := config.ValidateMCPServerConfig(serverConfig); err != nil {
		return fmt.Errorf("invalid server configuration: %w", err)
	}

	// Add server to tool manager
	if err := i.toolManager.AddServer(ctx, serverConfig); err != nil {
		return fmt.Errorf("failed to add MCP server: %w", err)
	}

	// Optionally save to configuration file
	// This could be expanded to persist the new server configuration

	return nil
}

// RemoveServer removes an MCP server
func (i *Integration) RemoveServer(serverName string) error {
	return i.toolManager.RemoveServer(serverName)
}

// RefreshTools refreshes tools from all connected MCP servers
func (i *Integration) RefreshTools(ctx context.Context) error {
	return i.toolManager.RefreshTools(ctx)
}

// ListServers returns a list of connected MCP servers
func (i *Integration) ListServers() []string {
	return i.toolManager.ListServers()
}

// GetServerInfo returns information about a specific MCP server
func (i *Integration) GetServerInfo(serverName string) (*domain.MCPServerConfig, bool) {
	return i.toolManager.GetServerInfo(serverName)
}

// ListMCPResources lists resources from a specific MCP server
func (i *Integration) ListMCPResources(ctx context.Context, serverName string) ([]domain.MCPResource, error) {
	return i.toolManager.ListMCPResources(ctx, serverName)
}

// ReadMCPResource reads a resource from an MCP server
func (i *Integration) ReadMCPResource(ctx context.Context, serverName, resourceURI string) (*domain.MCPResourceContent, error) {
	return i.toolManager.ReadMCPResource(ctx, serverName, resourceURI)
}

// GetMCPTools returns tools from a specific MCP server
func (i *Integration) GetMCPTools(serverName string) ([]message.Tool, error) {
	return i.toolManager.GetMCPTools(serverName)
}

// Close closes all MCP server connections
func (i *Integration) Close() error {
	servers := i.toolManager.ListServers()
	for _, serverName := range servers {
		if err := i.toolManager.RemoveServer(serverName); err != nil {
			logger.Warn("Error closing MCP server", "server", serverName, "error", err)
		}
	}
	return nil
}

// GetStats returns statistics about the MCP integration
func (i *Integration) GetStats() MCPStats {
	servers := i.toolManager.ListServers()
	stats := MCPStats{
		ConnectedServers: len(servers),
		ServerNames:      servers,
	}

	// Count total tools from all MCP servers
	for _, serverName := range servers {
		tools, err := i.toolManager.GetMCPTools(serverName)
		if err == nil {
			stats.TotalTools += len(tools)
		}
	}

	return stats
}

// MCPStats represents statistics about the MCP integration
type MCPStats struct {
	ConnectedServers int      `json:"connectedServers"`
	TotalTools       int      `json:"totalTools"`
	ServerNames      []string `json:"serverNames"`
}
