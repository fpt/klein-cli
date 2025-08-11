package client

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// Test structures
type SimpleResponse struct {
	Message string `json:"message" jsonschema:"minLength=1,maxLength=100"`
	Code    int    `json:"code" jsonschema:"minimum=100,maximum=599"`
	Success bool   `json:"success"`
}

// Mock ToolCallingLLM for testing
type mockToolCallingLLM struct {
	setToolManagerFunc     func(domain.ToolManager)
	chatWithToolChoiceFunc func(context.Context, []message.Message, domain.ToolChoice, bool, chan<- string) (message.Message, error)
	chatFunc               func(context.Context, []message.Message, bool, chan<- string) (message.Message, error)
}

func (m *mockToolCallingLLM) SetToolManager(toolManager domain.ToolManager) {
	if m.setToolManagerFunc != nil {
		m.setToolManagerFunc(toolManager)
	}
}

func (m *mockToolCallingLLM) ChatWithToolChoice(ctx context.Context, messages []message.Message, toolChoice domain.ToolChoice, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	if m.chatWithToolChoiceFunc != nil {
		return m.chatWithToolChoiceFunc(ctx, messages, toolChoice, enableThinking, thinkingChan)
	}

	// Return a mock tool call response
	mockArgs := map[string]any{
		"message": "Test response",
		"code":    200,
		"success": true,
	}

	return message.NewToolCallMessage("respond", mockArgs), nil
}

func (m *mockToolCallingLLM) Chat(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	if m.chatFunc != nil {
		return m.chatFunc(ctx, messages, enableThinking, thinkingChan)
	}
	return message.NewChatMessage(message.MessageTypeAssistant, "mock response"), nil
}

func (m *mockToolCallingLLM) ModelID() string { return "mock-llm" }

func TestNewToolCallingStructuredClient(t *testing.T) {
	mockClient := &mockToolCallingLLM{}

	structuredClient := NewToolCallingStructuredClient[SimpleResponse](mockClient)

	if structuredClient == nil {
		t.Fatal("Expected non-nil structured client")
	}

	// Note: Cannot directly compare due to interface wrapper, but we can verify functionality

	if structuredClient.generator == nil {
		t.Error("Client should have a JSON schema generator")
	}
}

func TestToolCallingStructuredClient_IsToolCapable(t *testing.T) {
	mockClient := &mockToolCallingLLM{}
	structuredClient := NewToolCallingStructuredClient[SimpleResponse](mockClient)

	// Tool calling structured client should always report as tool capable
	if !structuredClient.IsToolCapable() {
		t.Error("Tool calling structured client should be tool capable")
	}
}

func TestToolCallingStructuredClient_ChatWithStructure(t *testing.T) {
	var capturedToolManager domain.ToolManager
	var capturedToolChoice domain.ToolChoice

	mockClient := &mockToolCallingLLM{
		setToolManagerFunc: func(tm domain.ToolManager) {
			capturedToolManager = tm
		},
		chatWithToolChoiceFunc: func(ctx context.Context, messages []message.Message, toolChoice domain.ToolChoice, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
			capturedToolChoice = toolChoice

			// Verify the tool choice is correct
			if toolChoice.Type != domain.ToolChoiceTool || toolChoice.Name != "respond" {
				t.Errorf("Expected tool choice for 'respond' tool, got %+v", toolChoice)
			}

			// Return a mock tool call response
			mockArgs := map[string]any{
				"message": "Test response",
				"code":    200,
				"success": true,
			}

			return message.NewToolCallMessage("respond", mockArgs), nil
		},
	}

	structuredClient := NewToolCallingStructuredClient[SimpleResponse](mockClient)

	// Test messages
	messages := []message.Message{
		message.NewChatMessage(message.MessageTypeUser, "Test request"),
	}

	// Call ChatWithStructure
	result, err := structuredClient.ChatWithStructure(context.Background(), messages, false, nil)
	if err != nil {
		t.Fatalf("ChatWithStructure failed: %v", err)
	}

	// Verify the result
	expected := SimpleResponse{
		Message: "Test response",
		Code:    200,
		Success: true,
	}

	if result != expected {
		t.Errorf("ChatWithStructure result = %+v, expected %+v", result, expected)
	}

	// Verify tool manager was set
	if capturedToolManager == nil {
		t.Error("Tool manager should have been set")
	}

	// Verify tools were registered
	tools := capturedToolManager.GetTools()
	if len(tools) != 1 {
		t.Errorf("Expected 1 tool, got %d", len(tools))
	}

	respondTool, exists := tools["respond"]
	if !exists {
		t.Error("Expected 'respond' tool to be registered")
	}

	if respondTool.Name() != "respond" {
		t.Errorf("Expected tool name 'respond', got %s", respondTool.Name())
	}

	// Verify tool choice was captured
	if capturedToolChoice.Type != domain.ToolChoiceTool {
		t.Errorf("Expected tool choice type 'tool', got %s", capturedToolChoice.Type)
	}
}

func TestToolCallingStructuredClient_Chat(t *testing.T) {
	mockClient := &mockToolCallingLLM{
		setToolManagerFunc: func(tm domain.ToolManager) {
			// No-op for this test
		},
		chatWithToolChoiceFunc: func(ctx context.Context, messages []message.Message, toolChoice domain.ToolChoice, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
			// Return a mock tool call response
			mockArgs := map[string]any{
				"message": "Test response",
				"code":    200,
				"success": true,
			}

			return message.NewToolCallMessage("respond", mockArgs), nil
		},
	}

	structuredClient := NewToolCallingStructuredClient[SimpleResponse](mockClient)

	// Test messages
	messages := []message.Message{
		message.NewChatMessage(message.MessageTypeUser, "Test request"),
	}

	// Call Chat (should delegate to ChatWithStructure)
	result, err := structuredClient.Chat(context.Background(), messages, false, nil)
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	// Result should be a JSON-marshaled version of the structured response
	if result.Type() != message.MessageTypeAssistant {
		t.Errorf("Expected assistant message, got %s", result.Type())
	}

	// Parse the JSON content
	var parsed SimpleResponse
	if err := json.Unmarshal([]byte(result.Content()), &parsed); err != nil {
		t.Fatalf("Failed to parse JSON response: %v", err)
	}

	expected := SimpleResponse{
		Message: "Test response",
		Code:    200,
		Success: true,
	}

	if parsed != expected {
		t.Errorf("Parsed result = %+v, expected %+v", parsed, expected)
	}
}

func TestRespondTool_Implementation(t *testing.T) {
	// Test that respondTool implements the Tool interface correctly
	tool := &respondTool{
		name:        "test_tool",
		description: "Test tool description",
		arguments: []message.ToolArgument{
			{
				Name:        "arg1",
				Description: "Argument 1",
				Type:        "string",
				Required:    true,
			},
		},
		handler: func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
			return message.NewToolResultText("test result"), nil
		},
	}

	// Test interface methods
	if tool.Name() != "test_tool" {
		t.Errorf("Name() = %s, expected test_tool", tool.Name())
	}

	if tool.RawName() != "test_tool" {
		t.Errorf("RawName() = %s, expected test_tool", tool.RawName())
	}

	if tool.Description() != "Test tool description" {
		t.Errorf("Description() = %s, expected Test tool description", tool.Description())
	}

	args := tool.Arguments()
	if len(args) != 1 {
		t.Errorf("Expected 1 argument, got %d", len(args))
	}

	if args[0].Name != "arg1" {
		t.Errorf("Argument name = %s, expected arg1", args[0].Name)
	}

	// Test handler
	handler := tool.Handler()
	if handler == nil {
		t.Error("Handler should not be nil")
	}

	result, err := handler(context.Background(), nil)
	if err != nil {
		t.Errorf("Handler error: %v", err)
	}

	if result.Text != "test result" {
		t.Errorf("Handler result = %s, expected test result", result.Text)
	}
}
