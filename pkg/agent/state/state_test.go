package state

import (
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

func TestNewMessageState(t *testing.T) {
	state := NewMessageState()
	if state == nil {
		t.Fatal("NewMessageState() returned nil")
	}
	if state.Messages == nil {
		t.Fatal("Messages slice should be initialized")
	}
	if state.Metadata == nil {
		t.Fatal("Metadata map should be initialized")
	}
	if len(state.Messages) != 0 {
		t.Fatalf("Expected empty messages slice, got %d messages", len(state.Messages))
	}
}

func TestAddMessage(t *testing.T) {
	state := NewMessageState()
	msg := message.NewChatMessage(message.MessageTypeUser, "Hello")

	state.AddMessage(msg)

	if len(state.Messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(state.Messages))
	}
	if state.Messages[0].Content() != "Hello" {
		t.Fatalf("Expected 'Hello', got '%s'", state.Messages[0].Content())
	}
}

func TestGetLastMessage(t *testing.T) {
	state := NewMessageState()

	// Test empty state
	lastMsg := state.GetLastMessage()
	if lastMsg != nil {
		t.Fatal("Expected nil for empty state")
	}

	// Test with messages
	msg1 := message.NewChatMessage(message.MessageTypeUser, "First")
	msg2 := message.NewChatMessage(message.MessageTypeAssistant, "Second")

	state.AddMessage(msg1)
	state.AddMessage(msg2)

	lastMsg = state.GetLastMessage()
	if lastMsg == nil {
		t.Fatal("Expected non-nil last message")
	}
	if lastMsg.Content() != "Second" {
		t.Fatalf("Expected 'Second', got '%s'", lastMsg.Content())
	}
}

func TestClear(t *testing.T) {
	state := NewMessageState()
	state.AddMessage(message.NewChatMessage(message.MessageTypeUser, "Test"))

	if len(state.Messages) != 1 {
		t.Fatal("Message should have been added")
	}

	state.Clear()

	if len(state.Messages) != 0 {
		t.Fatalf("Expected empty messages after Clear(), got %d messages", len(state.Messages))
	}
}

func TestRemoveMessagesBySource(t *testing.T) {
	state := NewMessageState()

	// Add messages with different sources
	situationMsg := message.NewSituationSystemMessage("Situation guidance")
	summaryMsg := message.NewSummarySystemMessage("Previous conversation summary")

	regularMsg := message.NewChatMessage(message.MessageTypeUser, "Regular message")
	// regularMsg has MessageSourceDefault by default

	state.AddMessage(situationMsg)
	state.AddMessage(summaryMsg)
	state.AddMessage(regularMsg)

	if len(state.Messages) != 3 {
		t.Fatalf("Expected 3 messages, got %d", len(state.Messages))
	}

	// Remove situation messages
	removed := state.RemoveMessagesBySource(message.MessageSourceSituation)

	if removed != 1 {
		t.Fatalf("Expected 1 message removed, got %d", removed)
	}
	if len(state.Messages) != 2 {
		t.Fatalf("Expected 2 messages remaining, got %d", len(state.Messages))
	}

	// Check that the correct messages remain
	foundSummary := false
	foundRegular := false
	for _, msg := range state.Messages {
		if msg.Source() == message.MessageSourceSummary {
			foundSummary = true
		}
		if msg.Source() == message.MessageSourceDefault {
			foundRegular = true
		}
	}
	if !foundSummary || !foundRegular {
		t.Fatal("Wrong messages were removed")
	}
}

func TestGetValidConversationHistory(t *testing.T) {
	state := NewMessageState()

	// Add various message types
	userMsg := message.NewChatMessage(message.MessageTypeUser, "Hello")
	assistantMsg := message.NewChatMessage(message.MessageTypeAssistant, "Hi there")

	// Add complete tool call/result pair
	toolCall := message.NewToolCallMessage("test_tool", message.ToolArgumentValues{"arg": "value"})
	toolResult := message.NewToolResultMessage(toolCall.ID(), "Tool executed successfully", "")

	// Add orphaned tool call (no result)
	orphanedCall := message.NewToolCallMessage("orphaned_tool", message.ToolArgumentValues{"arg": "value"})

	state.AddMessage(userMsg)
	state.AddMessage(assistantMsg)
	state.AddMessage(toolCall)
	state.AddMessage(toolResult)
	state.AddMessage(orphanedCall)

	// Get valid conversation history
	validMsgs := state.GetValidConversationHistory(10)

	// Should exclude the orphaned tool call
	if len(validMsgs) != 4 {
		t.Fatalf("Expected 4 valid messages (excluding orphaned call), got %d", len(validMsgs))
	}

	// Check that the orphaned call is not included
	for _, msg := range validMsgs {
		if msg.Type() == message.MessageTypeToolCall && msg.ID() == orphanedCall.ID() {
			t.Fatal("Orphaned tool call should not be included in valid history")
		}
	}
}

// Serialization tests moved to infra/message_test.go

// File operations tests moved to infra/message_test.go

// Load from non-existent file tests moved to infra/message_test.go

func TestGetMessages(t *testing.T) {
	state := NewMessageState()
	msg := message.NewChatMessage(message.MessageTypeUser, "Test")
	state.AddMessage(msg)

	messages := state.GetMessages()
	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}

	if messages[0].Content() != "Test" {
		t.Fatal("GetMessages returned incorrect content")
	}
}

func TestMessageSourceHandling(t *testing.T) {
	state := NewMessageState()

	// Test default source
	defaultMsg := message.NewChatMessage(message.MessageTypeUser, "Default")
	state.AddMessage(defaultMsg)

	// Test situation source
	situationMsg := message.NewSituationSystemMessage("Situation")
	state.AddMessage(situationMsg)

	// Test summary source
	summaryMsg := message.NewSummarySystemMessage("Summary")
	state.AddMessage(summaryMsg)

	// Test removing by source works
	removed := state.RemoveMessagesBySource(message.MessageSourceSituation)
	if removed != 1 {
		t.Fatalf("Expected 1 situation message removed, got %d", removed)
	}

	if len(state.Messages) != 2 {
		t.Fatalf("Expected 2 messages after removing situation, got %d", len(state.Messages))
	}
}

func TestComplexToolScenario(t *testing.T) {
	state := NewMessageState()

	// Simulate a complex conversation with multiple tool calls
	state.AddMessage(message.NewChatMessage(message.MessageTypeUser, "Please help me with multiple tasks"))

	// Tool call 1 - complete pair
	tool1 := message.NewToolCallMessage("task1", message.ToolArgumentValues{"param": "value1"})
	state.AddMessage(tool1)
	state.AddMessage(message.NewToolResultMessage(tool1.ID(), "Task 1 completed", ""))

	// Tool call 2 - complete pair
	tool2 := message.NewToolCallMessage("task2", message.ToolArgumentValues{"param": "value2"})
	state.AddMessage(tool2)
	state.AddMessage(message.NewToolResultMessage(tool2.ID(), "Task 2 completed", ""))

	// Tool call 3 - orphaned (no result)
	tool3 := message.NewToolCallMessage("task3", message.ToolArgumentValues{"param": "value3"})
	state.AddMessage(tool3)

	// Assistant response
	state.AddMessage(message.NewChatMessage(message.MessageTypeAssistant, "I've completed the available tasks"))

	// Test valid conversation history excludes orphaned tool call
	validMsgs := state.GetValidConversationHistory(20)

	// Should have: user message, tool1, result1, tool2, result2, assistant message (6 total)
	// Should exclude: tool3 (orphaned)
	if len(validMsgs) != 6 {
		t.Fatalf("Expected 6 valid messages, got %d", len(validMsgs))
	}

	// Verify no orphaned tool calls are present
	for _, msg := range validMsgs {
		if msg.Type() == message.MessageTypeToolCall && msg.ID() == tool3.ID() {
			t.Fatal("Orphaned tool call should not be in valid conversation history")
		}
	}

	// Verify complete pairs are preserved
	foundTool1 := false
	foundTool2 := false
	for _, msg := range validMsgs {
		if msg.Type() == message.MessageTypeToolCall {
			if msg.ID() == tool1.ID() {
				foundTool1 = true
			}
			if msg.ID() == tool2.ID() {
				foundTool2 = true
			}
		}
	}
	if !foundTool1 || !foundTool2 {
		t.Fatal("Complete tool call pairs should be preserved")
	}
}

// Serialization round trip tests moved to infra/message_test.go
func TestMessageStateBasicOperations(t *testing.T) {
	// Create a comprehensive test state
	state := NewMessageState()

	// Add metadata
	state.Metadata["session_id"] = "test_session_123"
	state.Metadata["user_id"] = 456

	// Add various message types
	userMsg := message.NewChatMessage(message.MessageTypeUser, "Start conversation")
	state.AddMessage(userMsg)

	assistantMsg := message.NewChatMessage(message.MessageTypeAssistant, "Hello! How can I help?")
	state.AddMessage(assistantMsg)

	thinkingMsg := message.NewChatMessageWithThinking(message.MessageTypeAssistant, "Let me think", "This requires careful consideration")
	state.AddMessage(thinkingMsg)

	imageMsg := message.NewChatMessageWithImages(message.MessageTypeUser, "Look at this", []string{"image1", "image2"})
	state.AddMessage(imageMsg)

	// Add tool interactions
	toolCall := message.NewToolCallMessage("complex_tool", message.ToolArgumentValues{
		"string_param": "test",
		"number_param": 42,
	})
	state.AddMessage(toolCall)

	toolResult := message.NewToolResultMessage(toolCall.ID(), "Complex tool executed successfully", "")
	state.AddMessage(toolResult)

	// Verify state operations
	if len(state.Messages) != 6 {
		t.Fatalf("Expected 6 messages, got %d", len(state.Messages))
	}

	// Verify metadata
	if state.Metadata["session_id"] != "test_session_123" {
		t.Fatal("Session ID metadata not preserved")
	}

	// Verify message content
	if state.Messages[0].Content() != "Start conversation" {
		t.Fatal("First message content not preserved")
	}

	if state.Messages[2].Thinking() != "This requires careful consideration" {
		t.Fatal("Thinking content not preserved")
	}

	if len(state.Messages[3].Images()) != 2 {
		t.Fatal("Images not preserved")
	}

	// Verify tool call functionality
	if state.Messages[4].Type() != message.MessageTypeToolCall {
		t.Fatal("Tool call type not preserved")
	}

	if state.Messages[5].Type() != message.MessageTypeToolResult {
		t.Fatal("Tool result type not preserved")
	}
}
