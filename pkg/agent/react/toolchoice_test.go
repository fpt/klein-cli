package react

import (
	"context"
	"testing"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/agent/state"
	"github.com/fpt/klein-cli/pkg/message"
)

func TestReAct_chatWithToolChoice(t *testing.T) {
	mockLLM := &mockLLM{}
	mockToolManager := &mockToolManager{}

	expectedResponse := message.NewChatMessage(message.MessageTypeAssistant, "Tool choice response")
	expectedToolChoice := domain.NewToolChoiceAny()

	// Mock the ChatWithToolChoice method
	mockLLM.chatWithToolChoiceFunc = func(ctx context.Context, messages []message.Message, toolChoice domain.ToolChoice) (message.Message, error) {
		if toolChoice.Type != expectedToolChoice.Type {
			t.Errorf("Expected tool choice type %v, got %v", expectedToolChoice.Type, toolChoice.Type)
		}
		return expectedResponse, nil
	}

	react, _ := NewReAct(mockLLM, mockToolManager, state.NewMessageState(), &mockSituation{}, 10)

	ctx := context.Background()
	userMessage := message.NewChatMessage(message.MessageTypeUser, "Hello")
	messages := []message.Message{userMessage}

	result, err := react.chatWithToolChoice(ctx, messages, expectedToolChoice, nil)

	if err != nil {
		t.Fatalf("chatWithToolChoice returned error: %v", err)
	}

	if result != expectedResponse {
		t.Error("Expected response not returned correctly")
	}
}

func TestReAct_chatWithToolChoice_Fallback(t *testing.T) {
	// Test with a mock LLM that doesn't support ToolCallingLLM interface
	mockLLM := &mockLLM{}
	mockToolManager := &mockToolManager{}

	expectedResponse := message.NewChatMessage(message.MessageTypeAssistant, "Fallback response")

	// Mock only the regular chat method (no ChatWithToolChoice)
	mockLLM.chatFunc = func(ctx context.Context, messages []message.Message) (message.Message, error) {
		return expectedResponse, nil
	}

	react, _ := NewReAct(mockLLM, mockToolManager, state.NewMessageState(), &mockSituation{}, 10)

	ctx := context.Background()
	userMessage := message.NewChatMessage(message.MessageTypeUser, "Hello")
	messages := []message.Message{userMessage}
	toolChoice := domain.NewToolChoiceAny()

	result, err := react.chatWithToolChoice(ctx, messages, toolChoice, nil)

	if err != nil {
		t.Fatalf("chatWithToolChoice returned error: %v", err)
	}

	if result != expectedResponse {
		t.Error("Expected fallback response not returned correctly")
	}
}
