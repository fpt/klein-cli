package state

import (
	"context"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

// Mock LLM for testing
type mockLLM struct {
	chatFunc func(ctx context.Context, messages []message.Message) (message.Message, error)
}

func (m *mockLLM) Chat(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	if m.chatFunc != nil {
		return m.chatFunc(ctx, messages)
	}
	return message.NewChatMessage(message.MessageTypeAssistant, "Mock summary"), nil
}

func (m *mockLLM) ModelID() string { return "mock-llm" }

func TestCleanupMandatory(t *testing.T) {
	state := NewMessageState()

	// Add various message types
	userMsg := message.NewChatMessage(message.MessageTypeUser, "Hello")
	situationMsg := message.NewSituationSystemMessage("Situation guidance")
	assistantMsg := message.NewChatMessage(message.MessageTypeAssistant, "Hi there")
	summaryMsg := message.NewSummarySystemMessage("Previous summary")

	state.AddMessage(userMsg)
	state.AddMessage(situationMsg)
	state.AddMessage(assistantMsg)
	state.AddMessage(summaryMsg)

	if len(state.GetMessages()) != 4 {
		t.Errorf("Expected 4 messages before cleanup, got %d", len(state.GetMessages()))
	}

	// Run mandatory cleanup
	err := state.CleanupMandatory()
	if err != nil {
		t.Errorf("CleanupMandatory failed: %v", err)
	}

	// Should remove situation and summary messages
	messages := state.GetMessages()
	if len(messages) != 2 {
		t.Errorf("Expected 2 messages after cleanup, got %d", len(messages))
	}

	// Verify remaining messages are user and assistant
	if messages[0].Type() != message.MessageTypeUser {
		t.Errorf("Expected first message to be user, got %v", messages[0].Type())
	}
	if messages[1].Type() != message.MessageTypeAssistant {
		t.Errorf("Expected second message to be assistant, got %v", messages[1].Type())
	}
}

func TestGetTotalTokenUsage(t *testing.T) {
	state := NewMessageState()

	// Initially should be zero
	inputTokens, outputTokens, totalTokens := state.GetTotalTokenUsage()
	if inputTokens != 0 || outputTokens != 0 || totalTokens != 0 {
		t.Errorf("Expected zero tokens initially, got input=%d, output=%d, total=%d",
			inputTokens, outputTokens, totalTokens)
	}

	// Add messages with token usage
	msg1 := message.NewChatMessage(message.MessageTypeUser, "Hello")
	msg1.SetTokenUsage(100, 0, 100)

	msg2 := message.NewChatMessage(message.MessageTypeAssistant, "Hi")
	msg2.SetTokenUsage(50, 25, 75)

	state.AddMessage(msg1)
	state.AddMessage(msg2)

	// Check total usage
	inputTokens, outputTokens, totalTokens = state.GetTotalTokenUsage()
	expectedInput := 150 // 100 + 50
	expectedOutput := 25 // 0 + 25
	expectedTotal := 175 // 100 + 75

	if inputTokens != expectedInput {
		t.Errorf("Expected input tokens %d, got %d", expectedInput, inputTokens)
	}
	if outputTokens != expectedOutput {
		t.Errorf("Expected output tokens %d, got %d", expectedOutput, outputTokens)
	}
	if totalTokens != expectedTotal {
		t.Errorf("Expected total tokens %d, got %d", expectedTotal, totalTokens)
	}
}

func TestCompactIfNeeded_BelowThreshold(t *testing.T) {
	state := NewMessageState()
	mockLLM := &mockLLM{}

	// Add messages with token usage below threshold
	msg := message.NewChatMessage(message.MessageTypeUser, "Hello")
	msg.SetTokenUsage(1000, 500, 1500) // 1500 total tokens
	state.AddMessage(msg)

	initialCount := len(state.GetMessages())

	// Test with 10000 max tokens and 70% threshold (7000 tokens)
	// 1500 tokens is below threshold, should not compact
	err := state.CompactIfNeeded(context.Background(), mockLLM, 10000, 70.0)
	if err != nil {
		t.Errorf("CompactIfNeeded failed: %v", err)
	}

	// Should not have compacted
	if len(state.GetMessages()) != initialCount {
		t.Errorf("Expected no compaction, but message count changed from %d to %d",
			initialCount, len(state.GetMessages()))
	}
}

func TestCompactIfNeeded_AboveThreshold(t *testing.T) {
	state := NewMessageState()
	mockLLM := &mockLLM{
		chatFunc: func(ctx context.Context, messages []message.Message) (message.Message, error) {
			return message.NewChatMessage(message.MessageTypeAssistant, "Test summary"), nil
		},
	}

	// Add many messages to trigger compaction by message count
	for i := 0; i < 60; i++ {
		msg := message.NewChatMessage(message.MessageTypeUser, "Test message")
		msg.SetTokenUsage(200, 100, 300) // Each message uses 300 tokens
		state.AddMessage(msg)
	}

	// Total: 60 * 300 = 18000 tokens
	// Test with 20000 max tokens and 70% threshold (14000 tokens)
	// 18000 tokens is above threshold, should compact
	err := state.CompactIfNeeded(context.Background(), mockLLM, 20000, 70.0)
	if err != nil {
		t.Errorf("CompactIfNeeded failed: %v", err)
	}

	// Should have compacted (will compact due to message count threshold in performCompaction)
	finalCount := len(state.GetMessages())
	if finalCount >= 60 {
		t.Errorf("Expected compaction to reduce message count, but got %d messages", finalCount)
	}

	// Should have a summary message
	messages := state.GetMessages()
	if len(messages) == 0 || messages[0].Type() != message.MessageTypeSystem {
		t.Errorf("Expected first message after compaction to be summary (system), got %v",
			messages[0].Type())
	}
}

func TestCompactIfNeeded_NoMaxTokens(t *testing.T) {
	state := NewMessageState()
	mockLLM := &mockLLM{}

	// Add message with high token usage
	msg := message.NewChatMessage(message.MessageTypeUser, "Hello")
	msg.SetTokenUsage(50000, 25000, 75000) // Very high token usage
	state.AddMessage(msg)

	initialCount := len(state.GetMessages())

	// Test with 0 max tokens (no limit specified)
	err := state.CompactIfNeeded(context.Background(), mockLLM, 0, 70.0)
	if err != nil {
		t.Errorf("CompactIfNeeded failed: %v", err)
	}

	// Should not compact when no limit specified
	if len(state.GetMessages()) != initialCount {
		t.Errorf("Expected no compaction with no token limit, but message count changed from %d to %d",
			initialCount, len(state.GetMessages()))
	}
}

func TestVisionContentTruncation(t *testing.T) {
	state := NewMessageState()

	// Add messages with images - first 5 are "old", last 5 are "recent"
	for i := 0; i < 15; i++ {
		var msg message.Message
		if i < 10 {
			// Older messages with images (should be truncated)
			msg = message.NewChatMessageWithImages(message.MessageTypeUser, "Image message", []string{"base64image"})
		} else {
			// Recent messages with images (should be preserved)
			msg = message.NewChatMessageWithImages(message.MessageTypeUser, "Recent image", []string{"base64recent"})
		}
		state.AddMessage(msg)
	}

	// Verify all messages initially have images
	messages := state.GetMessages()
	for i, msg := range messages {
		if len(msg.Images()) == 0 {
			t.Errorf("Message %d should have images initially", i)
		}
	}

	// Run mandatory cleanup
	err := state.CleanupMandatory()
	if err != nil {
		t.Errorf("CleanupMandatory failed: %v", err)
	}

	// Check vision content truncation
	messages = state.GetMessages()
	if len(messages) != 15 {
		t.Errorf("Expected 15 messages after cleanup, got %d", len(messages))
		return
	}

	// First 5 messages (older) should have images removed
	for i := 0; i < 5; i++ {
		if len(messages[i].Images()) > 0 {
			t.Errorf("Message %d should have images removed by vision truncation", i)
		}
	}

	// Last 10 messages (recent) should still have images
	for i := 5; i < 15; i++ {
		if len(messages[i].Images()) == 0 {
			t.Errorf("Message %d should still have images (recent message)", i)
		}
	}
}
