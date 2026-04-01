package state

import (
	"context"
	"strconv"
	"strings"
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
	_, err := state.CompactIfNeeded(context.Background(), mockLLM, 10000, 70.0)
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
	_, err := state.CompactIfNeeded(context.Background(), mockLLM, 20000, 70.0)
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
	_, err := state.CompactIfNeeded(context.Background(), mockLLM, 0, 70.0)
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
	// Build a history of 10 user turns, each with an image.
	// keepRecentTurns=5 means turns 0-4 (old) get images stripped,
	// turns 5-9 (recent) keep their images.
	state := NewMessageState()
	const totalTurns = 10

	for i := 0; i < totalTurns; i++ {
		state.AddMessage(message.NewChatMessageWithImages(
			message.MessageTypeUser, "turn message", []string{"base64img"},
		))
	}

	if err := state.CleanupMandatory(); err != nil {
		t.Fatalf("CleanupMandatory failed: %v", err)
	}

	msgs := state.GetMessages()
	if len(msgs) != totalTurns {
		t.Fatalf("expected %d messages, got %d", totalTurns, len(msgs))
	}

	// Old turns (before boundary at keepRecentTurns=5 from end) lose images.
	oldCount := totalTurns - keepRecentTurns // 5
	for i := 0; i < oldCount; i++ {
		if len(msgs[i].Images()) != 0 {
			t.Errorf("turn %d (old): expected images stripped, still present", i)
		}
	}
	// Recent turns keep their images.
	for i := oldCount; i < totalTurns; i++ {
		if len(msgs[i].Images()) == 0 {
			t.Errorf("turn %d (recent): expected images preserved, got none", i)
		}
	}
}

func TestMicroCompaction_LargeToolResults(t *testing.T) {
	// Interleave user turns with tool call/result pairs.
	// Old turns should have large tool results micro-compacted;
	// recent turns should keep their results verbatim.
	state := NewMessageState()

	largeContent := strings.Repeat("x", microCompactMinChars+1)
	const totalTurns = 8

	for i := 0; i < totalTurns; i++ {
		// User message starts the turn
		state.AddMessage(message.NewChatMessage(message.MessageTypeUser, "request"))
		// Tool result for this turn
		state.AddMessage(message.NewToolResultMessage("id-"+strconv.Itoa(i), largeContent, ""))
	}

	if err := state.CleanupMandatory(); err != nil {
		t.Fatalf("CleanupMandatory failed: %v", err)
	}

	msgs := state.GetMessages()
	// totalTurns * 2 messages (user + tool result each)
	if len(msgs) != totalTurns*2 {
		t.Fatalf("expected %d messages, got %d", totalTurns*2, len(msgs))
	}

	oldTurns := totalTurns - keepRecentTurns // 3
	for i := 0; i < oldTurns; i++ {
		toolResultIdx := i*2 + 1
		result := msgs[toolResultIdx].(*message.ToolResultMessage).Result
		if !strings.HasPrefix(result, "[Content cleared") {
			t.Errorf("turn %d tool result should be micro-compacted, got: %.60s", i, result)
		}
	}
	for i := oldTurns; i < totalTurns; i++ {
		toolResultIdx := i*2 + 1
		result := msgs[toolResultIdx].(*message.ToolResultMessage).Result
		if strings.HasPrefix(result, "[Content cleared") {
			t.Errorf("turn %d tool result should be preserved, got stub", i)
		}
	}
}

func TestMicroCompaction_SkipsOffloadedStubs(t *testing.T) {
	state := NewMessageState()

	// Fill enough turns that old ones would normally be compacted
	stub := "[Result offloaded to disk: /some/path (99999 chars total)]\nPreview:\n..."
	for i := 0; i < keepRecentTurns+2; i++ {
		state.AddMessage(message.NewChatMessage(message.MessageTypeUser, "q"))
		state.AddMessage(message.NewToolResultMessage("id-"+strconv.Itoa(i), stub, ""))
	}

	if err := state.CleanupMandatory(); err != nil {
		t.Fatalf("CleanupMandatory failed: %v", err)
	}

	for _, msg := range state.GetMessages() {
		if tr, ok := msg.(*message.ToolResultMessage); ok {
			if tr.Result != stub {
				t.Errorf("already-offloaded stub should not be re-stubbed, got: %.80s", tr.Result)
			}
		}
	}
}

func TestMicroCompaction_SmallResultsUntouched(t *testing.T) {
	state := NewMessageState()
	small := strings.Repeat("s", microCompactMinChars-1)
	for i := 0; i < keepRecentTurns+2; i++ {
		state.AddMessage(message.NewChatMessage(message.MessageTypeUser, "q"))
		state.AddMessage(message.NewToolResultMessage("id-"+strconv.Itoa(i), small, ""))
	}

	if err := state.CleanupMandatory(); err != nil {
		t.Fatalf("CleanupMandatory failed: %v", err)
	}

	for _, msg := range state.GetMessages() {
		if tr, ok := msg.(*message.ToolResultMessage); ok {
			if tr.Result != small {
				t.Errorf("small result should be untouched, got: %.80s", tr.Result)
			}
		}
	}
}
