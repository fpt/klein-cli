package client

import (
	"context"
	"testing"

	"github.com/pkg/errors"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// Mock LLM client
type mockLLM struct {
	chatFunc               func(ctx context.Context, messages []message.Message) (message.Message, error)
	chatWithToolChoiceFunc func(ctx context.Context, messages []message.Message, toolChoice domain.ToolChoice) (message.Message, error)
	setToolManagerFunc     func(toolManager domain.ToolManager)
	getToolManagerFunc     func() domain.ToolManager
	toolManager            domain.ToolManager // Store the tool manager
}

func (m *mockLLM) Chat(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	if m.chatFunc != nil {
		return m.chatFunc(ctx, messages)
	}
	return nil, errors.New("mock not configured")
}

func (m *mockLLM) ModelID() string { return "mock-llm" }

// SetToolManager sets the tool manager (mock implementation)
func (m *mockLLM) SetToolManager(toolManager domain.ToolManager) {
	m.toolManager = toolManager
	if m.setToolManagerFunc != nil {
		m.setToolManagerFunc(toolManager)
	}
}

// GetToolManager returns the tool manager (mock implementation)
func (m *mockLLM) GetToolManager() domain.ToolManager {
	if m.getToolManagerFunc != nil {
		return m.getToolManagerFunc()
	}
	return m.toolManager
}

// ChatWithToolChoice sends a message with tool choice control (mock implementation)
func (m *mockLLM) ChatWithToolChoice(ctx context.Context, messages []message.Message, toolChoice domain.ToolChoice, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	if m.chatWithToolChoiceFunc != nil {
		return m.chatWithToolChoiceFunc(ctx, messages, toolChoice)
	}
	// Fall back to regular chat for mock
	return m.Chat(ctx, messages, false, thinkingChan)
}

// Mock ToolManager
type mockToolManager struct {
	getToolsFunc func() map[message.ToolName]message.Tool
	callToolFunc func(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error)
}

func (m *mockToolManager) GetTools() map[message.ToolName]message.Tool {
	if m.getToolsFunc != nil {
		return m.getToolsFunc()
	}
	return make(map[message.ToolName]message.Tool)
}

func (m *mockToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	if m.callToolFunc != nil {
		return m.callToolFunc(ctx, name, args)
	}
	return message.NewToolResultError("mock not configured"), nil
}

func (m *mockToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	// Mock implementation - no-op
}

func TestNewClientWithToolManager(t *testing.T) {
	mockLLM := &mockLLM{}
	mockToolManager := &mockToolManager{}

	client, err := NewClientWithToolManager(mockLLM, mockToolManager)

	if err != nil {
		t.Fatalf("NewClientWithToolManager returned error: %v", err)
	}

	if client == nil {
		t.Fatal("NewClientWithToolManager returned nil client")
	}

	// Check if the client supports GetToolManager (if it has the method)
	if getter, ok := client.(interface{ GetToolManager() domain.ToolManager }); ok {
		if getter.GetToolManager() != mockToolManager {
			t.Error("ToolManager not set correctly")
		}
	}
}

func TestClientWithTool_Chat(t *testing.T) {
	mockLLM := &mockLLM{}
	mockToolManager := &mockToolManager{}

	expectedResponse := message.NewChatMessage(message.MessageTypeAssistant, "Hello, I'm an AI assistant")
	mockLLM.chatFunc = func(ctx context.Context, messages []message.Message) (message.Message, error) {
		return expectedResponse, nil
	}

	client, err := NewClientWithToolManager(mockLLM, mockToolManager)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	ctx := context.Background()
	userMessage := message.NewChatMessage(message.MessageTypeUser, "Hello")
	messages := []message.Message{userMessage}

	result, err := client.Chat(ctx, messages, false, nil)

	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	if result != expectedResponse {
		t.Errorf("Expected response not returned correctly")
	}
}

func TestClientWithTool_Chat_Error(t *testing.T) {
	mockLLM := &mockLLM{}
	mockToolManager := &mockToolManager{}

	expectedError := errors.New("LLM error")
	mockLLM.chatFunc = func(ctx context.Context, messages []message.Message) (message.Message, error) {
		return nil, expectedError
	}

	client, err := NewClientWithToolManager(mockLLM, mockToolManager)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	ctx := context.Background()
	userMessage := message.NewChatMessage(message.MessageTypeUser, "Hello")
	messages := []message.Message{userMessage}

	result, err := client.Chat(ctx, messages, false, nil)

	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if result != nil {
		t.Error("Expected nil result on error")
	}

	if !errors.Is(err, expectedError) {
		t.Errorf("Expected wrapped LLM error, got %v", err)
	}
}

func TestClientWithTool_SetToolManager(t *testing.T) {
	mockLLM := &mockLLM{}
	mockToolManager1 := &mockToolManager{}
	mockToolManager2 := &mockToolManager{}

	// Track if SetToolManager was called on underlying client
	var underlyingToolManager domain.ToolManager
	mockLLM.setToolManagerFunc = func(toolManager domain.ToolManager) {
		underlyingToolManager = toolManager
	}

	client, err := NewClientWithToolManager(mockLLM, mockToolManager1)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Verify initial tool manager (if client supports GetToolManager)
	if getter, ok := client.(interface{ GetToolManager() domain.ToolManager }); ok {
		if getter.GetToolManager() != mockToolManager1 {
			t.Error("Initial tool manager not set correctly")
		}
	}

	// Set new tool manager
	client.SetToolManager(mockToolManager2)

	// Verify new tool manager (if client supports GetToolManager)
	if getter, ok := client.(interface{ GetToolManager() domain.ToolManager }); ok {
		if getter.GetToolManager() != mockToolManager2 {
			t.Error("New tool manager not set correctly")
		}
	}

	// Verify underlying client was updated
	if underlyingToolManager != mockToolManager2 {
		t.Error("Underlying client tool manager not updated")
	}
}

func TestClientWithTool_ChatWithToolChoice(t *testing.T) {
	mockLLM := &mockLLM{}
	mockToolManager := &mockToolManager{}

	expectedResponse := message.NewChatMessage(message.MessageTypeAssistant, "Tool choice response")
	expectedToolChoice := domain.NewToolChoiceAuto()

	mockLLM.chatWithToolChoiceFunc = func(ctx context.Context, messages []message.Message, toolChoice domain.ToolChoice) (message.Message, error) {
		if toolChoice != expectedToolChoice {
			t.Errorf("Expected tool choice %v, got %v", expectedToolChoice, toolChoice)
		}
		return expectedResponse, nil
	}

	client, err := NewClientWithToolManager(mockLLM, mockToolManager)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	ctx := context.Background()
	userMessage := message.NewChatMessage(message.MessageTypeUser, "Hello")
	messages := []message.Message{userMessage}

	result, err := client.ChatWithToolChoice(ctx, messages, expectedToolChoice, false, nil)

	if err != nil {
		t.Fatalf("ChatWithToolChoice returned error: %v", err)
	}

	if result != expectedResponse {
		t.Error("Expected response not returned correctly")
	}
}

func TestClientWithTool_UnderlyingClient(t *testing.T) {
	base := &mockLLM{}
	mockToolManager := &mockToolManager{}

	client, err := NewClientWithToolManager(base, mockToolManager)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// For mock clients, the factory should return the same client as it's already a ToolCallingLLM
	if underlying, ok := client.(*mockLLM); !ok || underlying != base {
		t.Error("NewClientWithToolManager should return the same mock client instance")
	}
}

func TestClientWithTool_GetToolManager(t *testing.T) {
	mockLLM := &mockLLM{}
	mockToolManager := &mockToolManager{}

	client, err := NewClientWithToolManager(mockLLM, mockToolManager)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Check if the client supports GetToolManager
	if getter, ok := client.(interface{ GetToolManager() domain.ToolManager }); ok {
		toolManager := getter.GetToolManager()
		if toolManager != mockToolManager {
			t.Error("GetToolManager didn't return the correct tool manager")
		}
	} else {
		t.Error("Client doesn't support GetToolManager")
	}
}
