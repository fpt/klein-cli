package ollama

import (
	"context"
	"fmt"
	"time"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// TestResult represents the result of a capability test
type TestResult struct {
	Model         string        `json:"model"`
	Test          string        `json:"test"`
	Success       bool          `json:"success"`
	Response      string        `json:"response,omitempty"`
	Error         string        `json:"error,omitempty"`
	Duration      time.Duration `json:"duration"`
	ToolCallFound bool          `json:"tool_call_found"`
}

// CapabilityChecker provides methods to test LLM capabilities
type CapabilityChecker struct {
	verbose bool
}

// NewCapabilityChecker creates a new capability checker
func NewCapabilityChecker(verbose bool) *CapabilityChecker {
	return &CapabilityChecker{
		verbose: verbose,
	}
}

// TestOllamaToolCalling tests if an Ollama model supports tool calling by attempting to use a simple tool
func (c *CapabilityChecker) TestOllamaToolCalling(ctx context.Context, model string) TestResult {
	start := time.Now()
	result := TestResult{
		Model:    model,
		Test:     "Tool Calling Capability",
		Duration: 0,
	}

	// Create Ollama core directly to bypass capability checks
	ollamaCore, err := NewOllamaCore(model)
	if err != nil {
		result.Error = fmt.Sprintf("Failed to create Ollama core: %v", err)
		result.Duration = time.Since(start)
		return result
	}

	// Create a basic Ollama client for testing
	testClient := &OllamaClient{
		OllamaCore: ollamaCore,
	}

	// Create dummy tool manager with a basic test tool
	toolManager := newDummyToolManager()

	// Set tool manager
	testClient.SetToolManager(toolManager)

	// Test tool calling with a simple request
	messages := []message.Message{
		message.NewChatMessage(message.MessageTypeUser, "Please test the capability by using the test_tool. Call the test_tool to verify it works."),
	}

	// Try tool calling with explicit tool choice
	toolChoice := domain.ToolChoice{
		Type: domain.ToolChoiceAny, // Force tool usage
	}

	if c.verbose {
		fmt.Printf("🧪 Testing tool calling capability for model: %s\n", model)
	}

	response, err := testClient.ChatWithToolChoice(ctx, messages, toolChoice, false, nil)
	result.Duration = time.Since(start)

	if err != nil {
		result.Error = fmt.Sprintf("Tool calling failed: %v", err)
		return result
	}

	// Check if we got a tool call response
	if response.Type() == message.MessageTypeToolCall {
		result.Success = true
		result.ToolCallFound = true
		result.Response = fmt.Sprintf("Tool call successful: %s", response.Content())

		if c.verbose {
			fmt.Printf("✅ Model %s supports tool calling (tool call found)\n", model)
		}
	} else {
		// No tool call detected - this model does not support tool calling
		result.Success = false
		result.ToolCallFound = false
		result.Error = "Model did not make a tool call - tool calling not supported"
		result.Response = response.Content()

		if c.verbose {
			fmt.Printf("❌ Model %s does not support tool calling (no tool call detected)\n", model)
			fmt.Printf("Response: %s\n", response.Content())
		}
	}

	return result
}

// dummyToolManager is a minimal tool manager implementation for testing
type dummyToolManager struct {
	tools map[message.ToolName]message.Tool
}

// newDummyToolManager creates a minimal tool manager for testing
func newDummyToolManager() domain.ToolManager {
	manager := &dummyToolManager{
		tools: make(map[message.ToolName]message.Tool),
	}

	// Add a simple test tool
	testTool := &dummyTool{
		name:        "test_tool",
		description: "A simple test tool to verify tool calling capability",
		arguments: []message.ToolArgument{
			{
				Name:        "message",
				Description: "Test message to echo back",
				Required:    false,
				Type:        "string",
			},
		},
		handler: func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
			msg := "Tool calling capability confirmed!"
			if msgArg, ok := args["message"].(string); ok && msgArg != "" {
				msg = msgArg
			}
			return message.NewToolResultText(fmt.Sprintf("✅ Test tool executed successfully: %s", msg)), nil
		},
	}

	manager.tools[testTool.name] = testTool
	return manager
}

// Implement domain.ToolManager interface
func (m *dummyToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	tool, exists := m.tools[name]
	return tool, exists
}

func (m *dummyToolManager) GetTools() map[message.ToolName]message.Tool {
	return m.tools
}

func (m *dummyToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	tool, exists := m.tools[name]
	if !exists {
		return message.NewToolResultError(fmt.Sprintf("tool %s not found", name)), nil
	}

	handler := tool.Handler()
	return handler(ctx, args)
}

func (m *dummyToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, args []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	tool := &dummyTool{
		name:        name,
		description: description,
		arguments:   args,
		handler:     handler,
	}
	m.tools[name] = tool
}

// dummyTool is a simple tool implementation for testing
type dummyTool struct {
	name        message.ToolName
	description message.ToolDescription
	arguments   []message.ToolArgument
	handler     func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)
}

func (t *dummyTool) RawName() message.ToolName {
	return t.name
}

func (t *dummyTool) Name() message.ToolName {
	return t.name
}

func (t *dummyTool) Description() message.ToolDescription {
	return t.description
}

func (t *dummyTool) Arguments() []message.ToolArgument {
	return t.arguments
}

func (t *dummyTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return t.handler
}

// DynamicCapabilityCheck performs a live test of the model's capabilities and can be used
// to update the static capability list or override it
func DynamicCapabilityCheck(ctx context.Context, model string, verbose bool) (bool, error) {
	checker := NewCapabilityChecker(verbose)

	// Set a reasonable timeout for the test
	testCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	result := checker.TestOllamaToolCalling(testCtx, model)

	if result.Error != "" {
		return false, fmt.Errorf("capability test failed: %s", result.Error)
	}

	// Only consider it successful if we got an actual tool call
	// Text mentions of tools don't count as tool calling capability
	return result.Success && result.ToolCallFound, nil
}
