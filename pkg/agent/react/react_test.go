package react

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pkg/errors"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/agent/state"
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

func (m *mockToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	// Not needed for tests
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

// Execute executes a tool and returns a message (mock implementation)
func (m *mockToolManager) Execute(name message.ToolName, arguments message.ToolArgumentValues) (message.Message, error) {
	// Call the tool and return the result as a message
	result, err := m.CallTool(context.Background(), name, arguments)
	if err != nil {
		return nil, err
	}

	// Handle structured tool result
	var content string
	if result.Error != "" {
		content = result.Error
	} else {
		content = result.Text
	}

	return message.NewChatMessage(message.MessageTypeAssistant, content), nil
}

// Mock Situation
type mockSituation struct {
	injectMessageFunc func(state domain.State, currentStep, maxStep int)
}

func (m *mockSituation) InjectMessage(state domain.State, currentStep, maxStep int) {
	if m.injectMessageFunc != nil {
		m.injectMessageFunc(state, currentStep, maxStep)
	}
	// Default: no situation message (do nothing)
}

func TestNewReAct(t *testing.T) {
	mockLLM := &mockLLM{}
	mockToolManager := &mockToolManager{}

	react, _ := NewReAct(mockLLM, mockToolManager, state.NewMessageState(), &mockSituation{}, 10)

	if react == nil {
		t.Fatal("NewReAct returned nil")
	}

	// Note: llmClient is private, so we can't directly compare it
	// The functionality will be tested through the public methods

	// Can't directly compare interfaces, so we'll skip this check
	// The important thing is that the react instance was created successfully

	if react.state == nil {
		t.Error("State not initialized")
	}
}

func TestReAct_MessageStateEncapsulation(t *testing.T) {
	mockLLM := &mockLLM{}
	mockToolManager := &mockToolManager{}

	react, _ := NewReAct(mockLLM, mockToolManager, state.NewMessageState(), &mockSituation{}, 10)

	// Test that ReAct properly encapsulates state - we can only test through public methods
	// Add a message and verify we can get it back through GetLastMessage
	react.state.AddMessage(message.NewChatMessage(message.MessageTypeUser, "test"))
	lastMessage := react.GetLastMessage()

	if lastMessage == nil {
		t.Error("GetLastMessage() returned nil")
	}

	if lastMessage.Content() != "test" {
		t.Error("GetLastMessage() returned wrong content")
	}

	// Test ClearHistory
	react.ClearHistory()
	lastMessageAfterClear := react.GetLastMessage()
	if lastMessageAfterClear != nil {
		t.Error("Expected nil after ClearHistory()")
	}
}

func TestReAct_Invoke_ChatMessage(t *testing.T) {
	mockLLM := &mockLLM{}
	mockToolManager := &mockToolManager{}

	expectedResponse := message.NewChatMessage(message.MessageTypeAssistant, "Hello, I'm an AI assistant")

	mockLLM.chatFunc = func(ctx context.Context, messages []message.Message) (message.Message, error) {
		// Verify user message was added
		if len(messages) != 1 {
			t.Errorf("Expected 1 message, got %d", len(messages))
		}

		if messages[0].Type() != message.MessageTypeUser {
			t.Errorf("Expected user message, got %v", messages[0].Type())
		}

		if messages[0].Content() != "Hello" {
			t.Errorf("Expected 'Hello', got '%s'", messages[0].Content())
		}

		return expectedResponse, nil
	}

	react, _ := NewReAct(mockLLM, mockToolManager, state.NewMessageState(), &mockSituation{}, 10)

	ctx := context.Background()
	result, err := react.Run(ctx, "Hello")

	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}

	if result == nil {
		t.Fatal("Invoke returned nil result")
	}

	if result.Content() != "Hello, I'm an AI assistant" {
		t.Errorf("Expected 'Hello, I'm an AI assistant', got '%s'", result.Content())
	}

	// Verify state contains both user and assistant messages
	messages := react.state.GetMessages()
	if len(messages) != 2 {
		t.Errorf("Expected 2 messages in state, got %d", len(messages))
	}
}

func TestReAct_Invoke_ToolCall(t *testing.T) {
	mockLLM := &mockLLM{}
	mockToolManager := &mockToolManager{}

	toolCallMessage := message.NewToolCallMessage(
		message.ToolName("test_tool"),
		message.ToolArgumentValues{"arg1": "value1"},
	)

	callCount := 0
	mockLLM.chatFunc = func(ctx context.Context, messages []message.Message) (message.Message, error) {
		callCount++
		if callCount == 1 {
			// First call: return tool call
			return toolCallMessage, nil
		} else {
			// Second call: return final chat message
			return message.NewChatMessage(message.MessageTypeAssistant, "Task completed using test tool"), nil
		}
	}

	mockToolManager.callToolFunc = func(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
		if name != "test_tool" {
			t.Errorf("Expected tool name 'test_tool', got '%s'", name)
		}

		if args["arg1"] != "value1" {
			t.Errorf("Expected arg1='value1', got '%v'", args["arg1"])
		}

		return message.NewToolResultText("tool result"), nil
	}

	// Create mock situation that injects system message after tool result
	mockAlign := &mockSituation{
		injectMessageFunc: func(state domain.State, currentStep, maxStep int) {
			if lastMsg := state.GetLastMessage(); lastMsg != nil && lastMsg.Type() == message.MessageTypeToolResult {
				state.AddMessage(message.NewSituationSystemMessage("You received a tool result. Use this result to answer the user's original question directly."))
			}
		},
	}
	react, _ := NewReAct(mockLLM, mockToolManager, state.NewMessageState(), mockAlign, 10)

	ctx := context.Background()
	result, err := react.Run(ctx, "Use test tool")

	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}

	if result == nil {
		t.Fatal("Invoke returned nil result")
	}

	// Should return a ChatMessage (the final assistant response)
	if result.Type() != message.MessageTypeAssistant {
		t.Errorf("Expected ChatMessage, got %v", result.Type())
	}

	// Verify state contains user message, tool call message, tool result message, and final assistant message
	// Note: Situation messages are now removed by CleanupMandatory() before final response
	messages := react.state.GetMessages()
	if len(messages) != 4 {
		t.Errorf("Expected 4 messages in state, got %d", len(messages))
		for i, msg := range messages {
			t.Errorf("Message %d: type=%v, content=%q", i, msg.Type(), msg.Content())
		}
		return
	}

	// Verify the message sequence (situation message removed by CleanupMandatory)
	if messages[0].Type() != message.MessageTypeUser {
		t.Errorf("Expected first message to be user, got %v", messages[0].Type())
	}
	if messages[1].Type() != message.MessageTypeToolCall {
		t.Errorf("Expected second message to be tool call, got %v", messages[1].Type())
	}
	if messages[2].Type() != message.MessageTypeToolResult {
		t.Errorf("Expected third message to be tool result, got %v", messages[2].Type())
	}
	if messages[3].Type() != message.MessageTypeAssistant {
		t.Errorf("Expected fourth message to be assistant, got %v", messages[3].Type())
	}
}

func TestReAct_Invoke_LLMError(t *testing.T) {
	mockLLM := &mockLLM{}
	mockToolManager := &mockToolManager{}

	expectedError := errors.New("LLM error")

	mockLLM.chatFunc = func(ctx context.Context, messages []message.Message) (message.Message, error) {
		return nil, expectedError
	}

	react, _ := NewReAct(mockLLM, mockToolManager, state.NewMessageState(), &mockSituation{}, 10)

	ctx := context.Background()
	result, err := react.Run(ctx, "Hello")

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

func TestReAct_Invoke_ToolError(t *testing.T) {
	mockLLM := &mockLLM{}
	mockToolManager := &mockToolManager{}

	toolCallMessage := message.NewToolCallMessage(
		message.ToolName("test_tool"),
		message.ToolArgumentValues{"arg1": "value1"},
	)

	mockLLM.chatFunc = func(ctx context.Context, messages []message.Message) (message.Message, error) {
		return toolCallMessage, nil
	}

	expectedError := errors.New("tool error")

	mockToolManager.callToolFunc = func(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
		return message.ToolResult{}, expectedError
	}

	react, _ := NewReAct(mockLLM, mockToolManager, state.NewMessageState(), &mockSituation{}, 10)

	ctx := context.Background()
	result, err := react.Run(ctx, "Use test tool")

	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if result != nil {
		t.Error("Expected nil result on error")
	}

	// With the new behavior, tool errors should be captured in the response, not cause agent failure
	// Check that agent continues running and eventually hits max iterations
	if !strings.Contains(err.Error(), "exceeded maximum loop limit") {
		t.Errorf("Expected error to contain 'exceeded maximum loop limit', got '%v'", err)
	}
}

// Mock message type that doesn't match ChatMessage or ToolCallMessage
type unexpectedMessage struct {
	id      string
	content string
}

func (u *unexpectedMessage) ID() string                    { return u.id }
func (u *unexpectedMessage) Type() message.MessageType     { return message.MessageTypeSystem }
func (u *unexpectedMessage) Content() string               { return u.content }
func (u *unexpectedMessage) Timestamp() time.Time          { return time.Now() }
func (u *unexpectedMessage) Thinking() string              { return "" }
func (u *unexpectedMessage) Images() []string              { return nil }
func (u *unexpectedMessage) Source() message.MessageSource { return message.MessageSourceDefault }
func (u *unexpectedMessage) String() string                { return fmt.Sprintf("unexpectedMessage: %s", u.content) }
func (u *unexpectedMessage) TruncatedString() string {
	return fmt.Sprintf("[unexpected] %s", u.content)
}
func (u *unexpectedMessage) Metadata() map[string]any {
	return nil
}

// Token usage methods (required by Message interface)
func (u *unexpectedMessage) InputTokens() int                                         { return 0 }
func (u *unexpectedMessage) OutputTokens() int                                        { return 0 }
func (u *unexpectedMessage) TotalTokens() int                                         { return 0 }
func (u *unexpectedMessage) SetTokenUsage(inputTokens, outputTokens, totalTokens int) {}

func TestReAct_Invoke_UnexpectedResponseType(t *testing.T) {
	mockLLM := &mockLLM{}
	mockToolManager := &mockToolManager{}

	// Create a message with unexpected type that doesn't match the switch cases
	unexpectedMsg := &unexpectedMessage{
		id:      "test-id",
		content: "unexpected message",
	}

	mockLLM.chatFunc = func(ctx context.Context, messages []message.Message) (message.Message, error) {
		return unexpectedMsg, nil
	}

	react, _ := NewReAct(mockLLM, mockToolManager, state.NewMessageState(), &mockSituation{}, 10)

	ctx := context.Background()
	result, err := react.Run(ctx, "Hello")

	if err == nil {
		t.Fatal("Expected error for unexpected response type, got nil")
	}

	if result != nil {
		t.Error("Expected nil result on error")
	}

	expectedError := "unexpected response type: *react.unexpectedMessage"
	if !strings.Contains(err.Error(), expectedError) {
		t.Errorf("Expected error to contain '%s', got '%s'", expectedError, err.Error())
	}
}

func TestReAct_handleToolCall(t *testing.T) {
	mockLLM := &mockLLM{}
	mockToolManager := &mockToolManager{}

	toolCallMessage := message.NewToolCallMessage(
		message.ToolName("test_tool"),
		message.ToolArgumentValues{"arg1": "value1"},
	)

	mockToolManager.callToolFunc = func(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
		if name != "test_tool" {
			t.Errorf("Expected tool name 'test_tool', got '%s'", name)
		}

		if args["arg1"] != "value1" {
			t.Errorf("Expected arg1='value1', got '%v'", args["arg1"])
		}

		return message.NewToolResultText("tool result"), nil
	}

	react, _ := NewReAct(mockLLM, mockToolManager, state.NewMessageState(), &mockSituation{}, 10)

	ctx := context.Background()
	result, err := react.handleToolCall(ctx, toolCallMessage)

	if err != nil {
		t.Fatalf("handleToolCall returned error: %v", err)
	}

	if result == nil {
		t.Fatal("handleToolCall returned nil result")
	}

	if result.Type() != message.MessageTypeToolResult {
		t.Errorf("Expected ToolResultMessage, got %v", result.Type())
	}

	// Check if it's a ToolResultMessage and verify content
	if toolResultMsg, ok := result.(*message.ToolResultMessage); ok {
		if toolResultMsg.Result != "tool result" {
			t.Errorf("Expected 'tool result', got '%s'", toolResultMsg.Result)
		}

		if toolResultMsg.Error != "" {
			t.Errorf("Expected empty error, got '%s'", toolResultMsg.Error)
		}
	} else {
		t.Error("Result is not a ToolResultMessage")
	}
}

func TestReAct_handleToolCall_Error(t *testing.T) {
	mockLLM := &mockLLM{}
	mockToolManager := &mockToolManager{}

	toolCallMessage := message.NewToolCallMessage(
		message.ToolName("test_tool"),
		message.ToolArgumentValues{"arg1": "value1"},
	)

	expectedError := errors.New("tool execution failed")

	mockToolManager.callToolFunc = func(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
		return message.ToolResult{}, expectedError
	}

	react, _ := NewReAct(mockLLM, mockToolManager, state.NewMessageState(), &mockSituation{}, 10)

	ctx := context.Background()
	result, err := react.handleToolCall(ctx, toolCallMessage)

	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if result == nil {
		t.Fatal("Expected result, got nil")
	}

	// Check that the result contains the error message
	if !strings.Contains(result.Content(), "Tool execution failed: tool execution failed") {
		t.Errorf("Expected result to contain 'Tool execution failed: tool execution failed', got %v", result.Content())
	}
}

// Test that state is properly managed across multiple invocations
func TestReAct_StateManagement(t *testing.T) {
	mockLLM := &mockLLM{}
	mockToolManager := &mockToolManager{}

	callCount := 0
	mockLLM.chatFunc = func(ctx context.Context, messages []message.Message) (message.Message, error) {
		callCount++
		// Verify messages accumulate correctly
		// Each call adds a user message first, then calls LLM with all messages
		// So on call 1: 1 user message
		// On call 2: 1 user + 1 assistant + 1 user = 3 messages
		expectedCount := callCount*2 - 1
		if len(messages) != expectedCount {
			t.Errorf("Call %d: Expected %d messages, got %d", callCount, expectedCount, len(messages))
		}

		return message.NewChatMessage(message.MessageTypeAssistant, "response "+string(rune(callCount+'0'))), nil
	}

	react, _ := NewReAct(mockLLM, mockToolManager, state.NewMessageState(), &mockSituation{}, 10)

	ctx := context.Background()

	// First invocation
	_, err := react.Run(ctx, "Hello")
	if err != nil {
		t.Fatalf("First invoke error: %v", err)
	}

	// Second invocation - should have previous messages
	_, err = react.Run(ctx, "How are you?")
	if err != nil {
		t.Fatalf("Second invoke error: %v", err)
	}

	// Verify final state
	messages := react.state.GetMessages()
	if len(messages) != 4 { // 2 user + 2 assistant
		t.Errorf("Expected 4 messages in final state, got %d", len(messages))
	}
}

func TestReAct_compaction(t *testing.T) {
	tests := []struct {
		name              string
		initialMessages   int
		expectedCompacted bool
		expectedFinal     int // Expected number of messages after compaction
		description       string
	}{
		{
			name:              "SmallConversationCompacted",
			initialMessages:   30,
			expectedCompacted: true, // Changed: token-based compaction detects high usage
			expectedFinal:     11,   // Changed: 1 summary + 10 recent
			description:       "Should compact when token usage is high regardless of message count",
		},
		{
			name:              "CompactionTriggered",
			initialMessages:   60,
			expectedCompacted: true,
			expectedFinal:     11, // 1 summary + 10 recent
			description:       "Should compact when messages > 50",
		},
		{
			name:              "EdgeCaseHighUsage",
			initialMessages:   50,
			expectedCompacted: true, // Changed: token-based compaction detects high usage
			expectedFinal:     11,   // Changed: 1 summary + 10 recent
			description:       "Should compact when token usage is high (50 messages * 1500 tokens = 75K in 25K context)",
		},
		{
			name:              "LargeMessageSet",
			initialMessages:   100,
			expectedCompacted: true,
			expectedFinal:     11, // 1 summary + 10 recent
			description:       "Should compact large message sets correctly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock LLM with summary support for compaction
			mockLLM := &mockLLM{
				chatFunc: func(ctx context.Context, messages []message.Message) (message.Message, error) {
					// Check if this is a summarization request
					if len(messages) > 0 && strings.Contains(messages[0].Content(), "Please create a concise summary") {
						return message.NewChatMessage(message.MessageTypeAssistant,
							fmt.Sprintf("Summary of %d messages: User had various interactions with the assistant, including questions and tool usage.", tt.initialMessages-10)), nil
					}
					return message.NewChatMessage(message.MessageTypeAssistant, "mock response"), nil
				},
			}

			// Create mock tool manager
			mockTM := &mockToolManager{
				callToolFunc: func(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
					return message.ToolResult{Text: "mock result"}, nil
				},
			}

			// Create ReAct instance
			react, _ := NewReAct(mockLLM, mockTM, state.NewMessageState(), &mockSituation{}, 10)

			// Populate with test messages
			for i := 0; i < tt.initialMessages; i++ {
				var msg message.Message
				if i%2 == 0 {
					msg = message.NewChatMessage(message.MessageTypeUser, fmt.Sprintf("User message %d", i+1))
				} else {
					msg = message.NewChatMessage(message.MessageTypeAssistant, fmt.Sprintf("Assistant message %d", i+1))
				}
				react.state.AddMessage(msg)
			}

			// Verify initial state
			initialCount := len(react.state.GetMessages())
			if initialCount != tt.initialMessages {
				t.Fatalf("Initial setup failed: expected %d messages, got %d", tt.initialMessages, initialCount)
			}

			// Set up high token usage to trigger compaction
			// Add token usage to messages to exceed threshold
			messages := react.state.GetMessages()
			for _, msg := range messages {
				msg.SetTokenUsage(1000, 500, 1500) // Each message uses 1500 tokens
			}

			// Perform compaction with low max tokens to trigger it
			ctx := context.Background()
			maxTokens := len(messages) * 500 // Much lower than actual usage to force compaction
			err := react.state.CompactIfNeeded(ctx, mockLLM, maxTokens, 70.0)
			if err != nil {
				t.Fatalf("Compaction failed: %v", err)
			}

			// Verify results
			finalMessages := react.state.GetMessages()
			finalCount := len(finalMessages)

			if finalCount != tt.expectedFinal {
				t.Errorf("Expected %d messages after compaction, got %d", tt.expectedFinal, finalCount)
			}

			// If compaction was expected, verify structure
			if tt.expectedCompacted {
				if finalCount < 2 {
					t.Fatalf("Expected at least 2 messages after compaction (summary + recent), got %d", finalCount)
				}

				// First message should be system summary
				firstMsg := finalMessages[0]
				if firstMsg.Type() != message.MessageTypeSystem {
					t.Errorf("Expected first message to be system summary, got %s", firstMsg.Type())
				}

				// Should contain summary text
				content := firstMsg.Content()
				if !strings.Contains(content, "Previous Conversation Summary") {
					t.Errorf("Expected summary message to contain 'Previous Conversation Summary', got: %s", content)
				}

				// Remaining messages should be the recent ones (last 10 from original)
				expectedRecentStart := tt.initialMessages - 10
				for i := 1; i < finalCount; i++ {
					msg := finalMessages[i]
					expectedIdx := expectedRecentStart + (i - 1) // -1 because we skip the summary message

					// Verify the content matches what we expect from recent messages
					if expectedIdx%2 == 0 {
						expectedContent := fmt.Sprintf("User message %d", expectedIdx+1)
						if !strings.Contains(msg.Content(), expectedContent) {
							t.Errorf("Recent message %d content mismatch. Expected to contain '%s', got '%s'",
								i, expectedContent, msg.Content())
						}
					}
				}
			} else {
				// No compaction should mean identical messages
				if finalCount != initialCount {
					t.Errorf("No compaction expected but message count changed: %d -> %d", initialCount, finalCount)
				}
			}
		})
	}
}

func TestReAct_compactionEdgeCases(t *testing.T) {
	t.Run("EmptyMessageState", func(t *testing.T) {
		mockLLM := &mockLLM{
			chatFunc: func(ctx context.Context, messages []message.Message) (message.Message, error) {
				// Check if this is a summarization request
				if len(messages) > 0 && strings.Contains(messages[0].Content(), "Please create a concise summary") {
					return message.NewChatMessage(message.MessageTypeAssistant, "No previous conversation to summarize."), nil
				}
				return message.NewChatMessage(message.MessageTypeAssistant, "mock response"), nil
			},
		}
		mockTM := &mockToolManager{}
		react, _ := NewReAct(mockLLM, mockTM, state.NewMessageState(), &mockSituation{}, 10)

		// Test compaction with empty state (should not do anything)
		ctx := context.Background()
		err := react.state.CompactIfNeeded(ctx, mockLLM, 100000, 70.0)
		if err != nil {
			t.Fatalf("Compaction of empty state failed: %v", err)
		}

		messages := react.state.GetMessages()
		if len(messages) != 0 {
			t.Errorf("Expected 0 messages after compacting empty state, got %d", len(messages))
		}
	})

	t.Run("InsufficientMessagesForSplit", func(t *testing.T) {
		mockLLM := &mockLLM{
			chatFunc: func(ctx context.Context, messages []message.Message) (message.Message, error) {
				// Check if this is a summarization request
				if len(messages) > 0 && strings.Contains(messages[0].Content(), "Please create a concise summary") {
					return message.NewChatMessage(message.MessageTypeAssistant, "Summary of 50 messages: User sent various messages and received responses."), nil
				}
				return message.NewChatMessage(message.MessageTypeAssistant, "mock response"), nil
			},
		}
		mockTM := &mockToolManager{}
		react, _ := NewReAct(mockLLM, mockTM, state.NewMessageState(), &mockSituation{}, 10)

		// Add exactly 60 messages (> 50 to trigger compaction attempt)
		for i := 0; i < 60; i++ {
			react.state.AddMessage(message.NewChatMessage(message.MessageTypeUser, fmt.Sprintf("Message %d", i+1)))
		}

		// Set up high token usage to trigger compaction
		messages := react.state.GetMessages()
		for _, msg := range messages {
			msg.SetTokenUsage(1000, 500, 1500) // Each message uses 1500 tokens
		}

		ctx := context.Background()
		maxTokens := len(messages) * 500 // Lower than actual usage to force compaction
		err := react.state.CompactIfNeeded(ctx, mockLLM, maxTokens, 70.0)
		if err != nil {
			t.Fatalf("Compaction failed: %v", err)
		}

		// Should compact normally since we have 60 > 50 messages
		finalCount := len(react.state.GetMessages())
		expectedFinal := 11 // 1 summary + 10 recent
		if finalCount != expectedFinal {
			t.Errorf("Expected %d messages after compaction, got %d", expectedFinal, finalCount)
		}
	})
}

func TestReAct_compactionWithToolCalls(t *testing.T) {
	t.Run("ToolCallChainSplit", func(t *testing.T) {
		mockLLM := &mockLLM{
			chatFunc: func(ctx context.Context, messages []message.Message) (message.Message, error) {
				// Check if this is a summarization request
				if len(messages) > 0 && strings.Contains(messages[0].Content(), "Please create a concise summary") {
					return message.NewChatMessage(message.MessageTypeAssistant, "Summary: User interacted with various tools and received responses. Tool was used with results."), nil
				}
				return message.NewChatMessage(message.MessageTypeAssistant, "mock response"), nil
			},
		}
		mockTM := &mockToolManager{
			callToolFunc: func(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
				return message.ToolResult{Text: "mock result"}, nil
			},
		}
		react, _ := NewReAct(mockLLM, mockTM, state.NewMessageState(), &mockSituation{}, 10)

		// Create a scenario where tool calls ARE split across the compaction boundary
		// Strategy: Put tool calls in positions 47-48 (which will be summarized)
		// and regular messages in the last 10 positions (which will be preserved)

		// Add 46 regular messages (positions 0-45)
		for i := 0; i < 46; i++ {
			if i%2 == 0 {
				react.state.AddMessage(message.NewChatMessage(message.MessageTypeUser, fmt.Sprintf("User message %d", i+1)))
			} else {
				react.state.AddMessage(message.NewChatMessage(message.MessageTypeAssistant, fmt.Sprintf("Assistant message %d", i+1)))
			}
		}

		// Add 1 tool call pair at positions 46-47 (will be in "older messages" to summarize)
		toolCall := message.NewToolCallMessage(
			message.ToolName("split_tool"),
			message.ToolArgumentValues{"param": "this_will_be_summarized"},
		)
		toolResult := message.NewToolResultMessage(
			"split_result_id",
			"This tool result will be summarized",
			"",
		)
		react.state.AddMessage(toolCall)   // Position 46
		react.state.AddMessage(toolResult) // Position 47

		// Add 8 more regular messages (positions 48-55) - these will be the "recent" ones
		for i := 0; i < 8; i++ {
			if i%2 == 0 {
				react.state.AddMessage(message.NewChatMessage(message.MessageTypeUser, fmt.Sprintf("Recent user message %d", i+1)))
			} else {
				react.state.AddMessage(message.NewChatMessage(message.MessageTypeAssistant, fmt.Sprintf("Recent assistant message %d", i+1)))
			}
		}

		// Total: 56 messages
		// Split point will be at position 46 (56 - 10 = 46)
		// So positions 0-45 + tool call/result at 46-47 = "older" (48 messages to summarize)
		// Positions 48-55 = "recent" (8 messages to preserve)

		initialCount := len(react.state.GetMessages())
		if initialCount != 56 {
			t.Fatalf("Setup failed: expected 56 messages, got %d", initialCount)
		}

		t.Logf("Before compaction: %d messages total", initialCount)
		t.Logf("Tool call at position 46, tool result at position 47")
		t.Logf("Split point will be at position %d (total - 10)", initialCount-10)

		// Set up high token usage to trigger compaction
		messages := react.state.GetMessages()
		for _, msg := range messages {
			msg.SetTokenUsage(1000, 500, 1500) // Each message uses 1500 tokens
		}

		// Perform compaction
		ctx := context.Background()
		maxTokens := len(messages) * 500 // Lower than actual usage to force compaction
		err := react.state.CompactIfNeeded(ctx, mockLLM, maxTokens, 70.0)
		if err != nil {
			t.Fatalf("Compaction failed: %v", err)
		}

		// Check results
		finalMessages := react.state.GetMessages()
		t.Logf("After compaction: %d messages", len(finalMessages))

		// Debug: Log all message types to understand what was preserved
		for i, msg := range finalMessages {
			content := msg.Content()
			if len(content) > 50 {
				content = content[:50] + "..."
			}
			t.Logf("Message %d: %s - %s", i, msg.Type(), content)
		}

		// Count tool calls and results in final state
		toolCallsAfter := 0
		toolResultsAfter := 0
		for _, msg := range finalMessages {
			if msg.Type() == message.MessageTypeToolCall {
				toolCallsAfter++
			} else if msg.Type() == message.MessageTypeToolResult {
				toolResultsAfter++
			}
		}

		t.Logf("Tool calls after compaction: %d, Tool results: %d", toolCallsAfter, toolResultsAfter)

		// With the fixed compaction, tool call chains should be preserved
		// The compaction should find a safe split point that doesn't break the tool call/result pair

		if toolCallsAfter != toolResultsAfter {
			t.Errorf("Tool call chain broken: %d tool calls vs %d tool results", toolCallsAfter, toolResultsAfter)
		}

		// The tool call/result pair should be preserved to maintain Anthropic API compatibility
		if toolCallsAfter != 1 || toolResultsAfter != 1 {
			t.Errorf("Expected tool call/result pair to be preserved, got %d tool calls and %d tool results",
				toolCallsAfter, toolResultsAfter)
		}
	})

	t.Run("SafeCompactionStillWorks", func(t *testing.T) {
		mockLLM := &mockLLM{
			chatFunc: func(ctx context.Context, messages []message.Message) (message.Message, error) {
				// Check if this is a summarization request
				if len(messages) > 0 && strings.Contains(messages[0].Content(), "Please create a concise summary") {
					return message.NewChatMessage(message.MessageTypeAssistant, "Summary of 50 messages: User and assistant had multiple exchanges with no tool usage."), nil
				}
				return message.NewChatMessage(message.MessageTypeAssistant, "mock response"), nil
			},
		}
		mockTM := &mockToolManager{}
		react, _ := NewReAct(mockLLM, mockTM, state.NewMessageState(), &mockSituation{}, 10)

		// Add 60 regular messages (no tool calls) - compaction should work normally
		for i := 0; i < 60; i++ {
			if i%2 == 0 {
				react.state.AddMessage(message.NewChatMessage(message.MessageTypeUser, fmt.Sprintf("User message %d", i+1)))
			} else {
				react.state.AddMessage(message.NewChatMessage(message.MessageTypeAssistant, fmt.Sprintf("Assistant message %d", i+1)))
			}
		}

		initialCount := len(react.state.GetMessages())
		if initialCount != 60 {
			t.Fatalf("Setup failed: expected 60 messages, got %d", initialCount)
		}

		// Set up high token usage to trigger compaction
		messages := react.state.GetMessages()
		for _, msg := range messages {
			msg.SetTokenUsage(1000, 500, 1500) // Each message uses 1500 tokens
		}

		// Perform compaction
		ctx := context.Background()
		maxTokens := len(messages) * 500 // Lower than actual usage to force compaction
		err := react.state.CompactIfNeeded(ctx, mockLLM, maxTokens, 70.0)
		if err != nil {
			t.Fatalf("Compaction failed: %v", err)
		}

		// Should compact normally since there are no tool calls to worry about
		finalMessages := react.state.GetMessages()
		expectedFinal := 11 // 1 summary + 10 recent
		if len(finalMessages) != expectedFinal {
			t.Errorf("Expected %d messages after compaction, got %d", expectedFinal, len(finalMessages))
		}

		// First message should be summary
		if finalMessages[0].Type() != message.MessageTypeSystem {
			t.Errorf("Expected first message to be system summary, got %s", finalMessages[0].Type())
		}

		// No tool calls should remain
		toolCalls := 0
		toolResults := 0
		for _, msg := range finalMessages {
			if msg.Type() == message.MessageTypeToolCall {
				toolCalls++
			} else if msg.Type() == message.MessageTypeToolResult {
				toolResults++
			}
		}

		if toolCalls != 0 || toolResults != 0 {
			t.Errorf("Expected no tool calls/results, got %d tool calls and %d tool results", toolCalls, toolResults)
		}
	})
}
