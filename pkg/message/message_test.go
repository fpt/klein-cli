package message

import (
	"strings"
	"testing"
)

func TestTokenUsage(t *testing.T) {
	// Test ChatMessage token usage
	msg := NewChatMessage(MessageTypeUser, "Hello, world!")

	// Initially, token usage should be zero
	if msg.InputTokens() != 0 {
		t.Errorf("Expected InputTokens to be 0, got %d", msg.InputTokens())
	}
	if msg.OutputTokens() != 0 {
		t.Errorf("Expected OutputTokens to be 0, got %d", msg.OutputTokens())
	}
	if msg.TotalTokens() != 0 {
		t.Errorf("Expected TotalTokens to be 0, got %d", msg.TotalTokens())
	}

	// Set token usage
	msg.SetTokenUsage(100, 50, 150)

	// Verify token usage is set correctly
	if msg.InputTokens() != 100 {
		t.Errorf("Expected InputTokens to be 100, got %d", msg.InputTokens())
	}
	if msg.OutputTokens() != 50 {
		t.Errorf("Expected OutputTokens to be 50, got %d", msg.OutputTokens())
	}
	if msg.TotalTokens() != 150 {
		t.Errorf("Expected TotalTokens to be 150, got %d", msg.TotalTokens())
	}
}

func TestToolMessageTokenUsage(t *testing.T) {
	// Test ToolCallMessage token usage
	toolCall := NewToolCallMessage("test_tool", ToolArgumentValues{"arg1": "value1"})

	// Initially, token usage should be zero
	if toolCall.InputTokens() != 0 {
		t.Errorf("Expected InputTokens to be 0, got %d", toolCall.InputTokens())
	}

	// Set token usage
	toolCall.SetTokenUsage(200, 75, 275)

	// Verify token usage is set correctly
	if toolCall.InputTokens() != 200 {
		t.Errorf("Expected InputTokens to be 200, got %d", toolCall.InputTokens())
	}
	if toolCall.OutputTokens() != 75 {
		t.Errorf("Expected OutputTokens to be 75, got %d", toolCall.OutputTokens())
	}
	if toolCall.TotalTokens() != 275 {
		t.Errorf("Expected TotalTokens to be 275, got %d", toolCall.TotalTokens())
	}

	// Test ToolResultMessage token usage
	toolResult := NewToolResultMessage("test_id", "Test result", "")

	// Set token usage
	toolResult.SetTokenUsage(50, 25, 75)

	// Verify token usage is set correctly
	if toolResult.InputTokens() != 50 {
		t.Errorf("Expected InputTokens to be 50, got %d", toolResult.InputTokens())
	}
	if toolResult.OutputTokens() != 25 {
		t.Errorf("Expected OutputTokens to be 25, got %d", toolResult.OutputTokens())
	}
	if toolResult.TotalTokens() != 75 {
		t.Errorf("Expected TotalTokens to be 75, got %d", toolResult.TotalTokens())
	}
}

func TestTokenUsageStruct(t *testing.T) {
	// Test TokenUsage struct directly
	usage := TokenUsage{
		InputTokens:  300,
		OutputTokens: 150,
		TotalTokens:  450,
	}

	if usage.InputTokens != 300 {
		t.Errorf("Expected InputTokens to be 300, got %d", usage.InputTokens)
	}
	if usage.OutputTokens != 150 {
		t.Errorf("Expected OutputTokens to be 150, got %d", usage.OutputTokens)
	}
	if usage.TotalTokens != 450 {
		t.Errorf("Expected TotalTokens to be 450, got %d", usage.TotalTokens)
	}
}

func TestMessageStringWithTokenUsage(t *testing.T) {
	// Test String method with no token usage
	msg := NewChatMessage(MessageTypeUser, "Hello")
	str := msg.String()
	if !strings.Contains(str, "Message(ID:") {
		t.Errorf("String should contain Message(ID:, got: %s", str)
	}
	if strings.Contains(str, "Tokens:") {
		t.Errorf("String should not contain Tokens: when usage is zero, got: %s", str)
	}

	// Test String method with token usage
	msg.SetTokenUsage(100, 50, 150)
	str = msg.String()
	if !strings.Contains(str, "Tokens: 150 (in:100 out:50)") {
		t.Errorf("String should contain token usage info, got: %s", str)
	}
}
