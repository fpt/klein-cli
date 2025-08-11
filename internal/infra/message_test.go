package infra

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/fpt/klein-cli/internal/repository"
	"github.com/fpt/klein-cli/pkg/message"
)

func TestMessageHistoryRepositorySerialization(t *testing.T) {
	// Create test messages
	messages := []message.Message{
		message.NewChatMessage(message.MessageTypeUser, "Hello"),
		message.NewChatMessageWithThinking(message.MessageTypeAssistant, "Response", "I need to think about this"),
		message.NewChatMessageWithImages(message.MessageTypeUser, "Look at this", []string{"base64data"}),
	}

	// Add tool messages
	toolCall := message.NewToolCallMessage("test_tool", message.ToolArgumentValues{"arg": "value"})
	messages = append(messages, toolCall)

	toolResult := message.NewToolResultMessage(toolCall.ID(), "Success", "")
	messages = append(messages, toolResult)

	// Test serialization conversion
	serializedMessages := make([]repository.MessageHistory, len(messages))
	for i, msg := range messages {
		serializedMessages[i] = messageToSerializable(msg)
	}

	// Test JSON marshaling
	data, err := json.MarshalIndent(serializedMessages, "", "  ")
	if err != nil {
		t.Fatalf("JSON marshaling failed: %v", err)
	}

	// Test JSON unmarshaling
	var unmarshaled []repository.MessageHistory
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("JSON unmarshaling failed: %v", err)
	}

	if len(unmarshaled) != 5 {
		t.Fatalf("Expected 5 messages, got %d", len(unmarshaled))
	}

	// Test deserialization conversion
	deserializedMessages := make([]message.Message, len(unmarshaled))
	for i, serialized := range unmarshaled {
		deserializedMessages[i] = serializableToMessage(serialized)
	}

	// Verify message content preservation
	if deserializedMessages[0].Type() != message.MessageTypeUser {
		t.Fatal("First message type not preserved")
	}
	if deserializedMessages[0].Content() != "Hello" {
		t.Fatal("First message content not preserved")
	}

	if deserializedMessages[1].Thinking() != "I need to think about this" {
		t.Fatal("Thinking content not preserved")
	}

	if len(deserializedMessages[2].Images()) != 1 {
		t.Fatal("Images not preserved")
	}

	if deserializedMessages[3].Type() != message.MessageTypeToolCall {
		t.Fatal("Tool call type not preserved")
	}

	if deserializedMessages[4].Type() != message.MessageTypeToolResult {
		t.Fatal("Tool result type not preserved")
	}
}

func TestMessageHistoryRepositoryFileOperations(t *testing.T) {
	// Create temporary directory for testing
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "test_messages.json")

	// Create repository
	repo := NewMessageHistoryRepository(filePath)

	// Create test messages
	originalMessages := []message.Message{
		message.NewChatMessage(message.MessageTypeUser, "Hello"),
		message.NewChatMessage(message.MessageTypeAssistant, "Hi there"),
		message.NewSystemMessage("System message"),
	}

	// Test save
	if err := repo.Save(originalMessages); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatal("File was not created")
	}

	// Test load
	loadedMessages, err := repo.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Verify loaded messages
	if len(loadedMessages) != len(originalMessages) {
		t.Fatalf("Expected %d messages, got %d", len(originalMessages), len(loadedMessages))
	}

	for i, loaded := range loadedMessages {
		original := originalMessages[i]
		if loaded.Type() != original.Type() {
			t.Fatalf("Message %d type mismatch: expected %v, got %v", i, original.Type(), loaded.Type())
		}
		if loaded.Content() != original.Content() {
			t.Fatalf("Message %d content mismatch: expected %q, got %q", i, original.Content(), loaded.Content())
		}
	}
}

func TestMessageHistoryRepositoryLoadFromNonExistentFile(t *testing.T) {
	// Create repository with non-existent file
	repo := NewMessageHistoryRepository("/non/existent/file.json")

	// Should return empty messages, not error
	messages, err := repo.Load()
	if err != nil {
		t.Fatalf("Load from non-existent file should not error: %v", err)
	}

	if len(messages) != 0 {
		t.Fatalf("Expected empty messages, got %d", len(messages))
	}
}

func TestMessageHistoryRepositoryComplexToolScenario(t *testing.T) {
	// Create comprehensive test messages including complex tool scenarios
	messages := []message.Message{}

	// Add system message with situation source
	situationMsg := message.NewSituationSystemMessage("Situation guidance")
	messages = append(messages, situationMsg)

	// Add summary message
	summaryMsg := message.NewSummarySystemMessage("Previous conversation summary")
	messages = append(messages, summaryMsg)

	// Add tool call with complex arguments
	complexArgs := message.ToolArgumentValues{
		"simple_string": "test",
		"number":        42,
		"boolean":       true,
		"array":         []interface{}{"item1", "item2"},
		"object": map[string]interface{}{
			"nested_field": "nested_value",
			"nested_num":   123,
		},
	}
	toolCall := message.NewToolCallMessage("complex_tool", complexArgs)
	messages = append(messages, toolCall)

	// Add tool result with error
	toolResultWithError := message.NewToolResultMessage(toolCall.ID(), "", "Tool execution failed")
	messages = append(messages, toolResultWithError)

	// Test round-trip serialization
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "complex_test.json")
	repo := NewMessageHistoryRepository(filePath)

	// Save messages
	if err := repo.Save(messages); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Load messages
	loadedMessages, err := repo.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Verify message count
	if len(loadedMessages) != len(messages) {
		t.Fatalf("Expected %d messages, got %d", len(messages), len(loadedMessages))
	}

	// Verify situation message
	if loadedMessages[0].Source() != message.MessageSourceSituation {
		t.Fatal("Situation message source not preserved")
	}

	// Verify summary message
	if loadedMessages[1].Source() != message.MessageSourceSummary {
		t.Fatal("Summary message source not preserved")
	}

	// Verify tool call with complex arguments
	if loadedMessages[2].Type() != message.MessageTypeToolCall {
		t.Fatal("Tool call type not preserved")
	}
	if toolCallMsg, ok := loadedMessages[2].(*message.ToolCallMessage); ok {
		args := toolCallMsg.ToolArguments()
		if args["simple_string"] != "test" {
			t.Fatal("Simple string argument not preserved")
		}
		if args["number"] != float64(42) { // JSON unmarshaling converts numbers to float64
			t.Fatal("Number argument not preserved")
		}
		if args["boolean"] != true {
			t.Fatal("Boolean argument not preserved")
		}
	} else {
		t.Fatal("Could not cast to ToolCallMessage")
	}

	// Verify tool result with error
	if loadedMessages[3].Type() != message.MessageTypeToolResult {
		t.Fatal("Tool result type not preserved")
	}
	if toolResultMsg, ok := loadedMessages[3].(*message.ToolResultMessage); ok {
		if toolResultMsg.Error != "Tool execution failed" {
			t.Fatal("Tool result error not preserved")
		}
	} else {
		t.Fatal("Could not cast to ToolResultMessage")
	}
}

func TestMessageSerializationEdgeCases(t *testing.T) {
	// Test nil message handling
	nilResult := messageToSerializable(nil)
	if nilResult.ID != "" || nilResult.Content != "" {
		t.Fatal("Nil message should serialize to empty MessageHistory")
	}

	// Test empty messages
	emptyMsg := message.NewChatMessage(message.MessageTypeUser, "")
	serialized := messageToSerializable(emptyMsg)
	deserialized := serializableToMessage(serialized)

	if deserialized.Content() != "" {
		t.Fatal("Empty content not preserved")
	}
	if deserialized.Type() != message.MessageTypeUser {
		t.Fatal("Message type not preserved for empty message")
	}
}
