package react

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/agent/events"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
	"github.com/fpt/klein-cli/pkg/message"
	"github.com/pkg/errors"
)

var ErrWaitingForApproval = errors.New("waiting for user approval for tool call")

// ReAct is a simple ReAct implementation that uses LLM and tools
// It handles tool calls and manages the message state
//
// This implementation is designed to be simple and straightforward,
// focusing on the core functionality of ReAct with LLM and tools.

type ReAct struct {
	llmClient        domain.LLM
	state            domain.State
	toolManager      domain.ToolManager
	situation        domain.Situation
	maxIterations    int                 // configurable loop limit
	eventEmitter     events.EventEmitter // emitter for agent events
	thinkingChan     chan string         // channel for streaming thinking chunks
	status           domain.AgentStatus
	currentIteration int // current iteration count
	pendingToolCall  message.Message
}

// Ensure ReAct implements domain.ReAct interface
var _ domain.ReAct = (*ReAct)(nil)

// component logger for agent messages in ReAct
var reactLogger = pkgLogger.NewComponentLogger("react")

func NewReAct(llmClient domain.LLM, toolManager domain.ToolManager, sharedState domain.State, situation domain.Situation, maxIterations int) (*ReAct, events.EventEmitter) {
	eventEmitter := events.NewSimpleEventEmitter()
	reactClient := &ReAct{
		llmClient:     llmClient,
		toolManager:   toolManager,
		state:         sharedState,
		situation:     situation,
		maxIterations: maxIterations,
		eventEmitter:  eventEmitter,
	}
	return reactClient, eventEmitter
}

// GetLastMessage returns the last message in the conversation without exposing state
func (r *ReAct) GetLastMessage() message.Message {
	return r.state.GetLastMessage()
}

// ClearHistory clears the conversation history without exposing state
func (r *ReAct) ClearHistory() {
	r.state.Clear()
}

// GetConversationSummary returns a summary of the recent conversation for context
// This helps with action selection by providing conversational context
func (r *ReAct) GetConversationSummary() string {
	messages := r.state.GetMessages()
	if len(messages) == 0 {
		return "This is the start of a new conversation."
	}

	// Build a summary of recent user-assistant exchanges
	var summary strings.Builder
	summary.WriteString("Recent conversation:\n")

	// Get the last few messages to provide context
	start := 0
	if len(messages) > 6 { // Keep last 6 messages for context
		start = len(messages) - 6
	}

	for i := start; i < len(messages); i++ {
		msg := messages[i]
		switch msg.Type() {
		case message.MessageTypeUser:
			summary.WriteString(fmt.Sprintf("User: %s\n", msg.Content()))
		case message.MessageTypeAssistant:
			// Only include assistant responses, not tool calls/results
			if len(msg.Content()) > 0 && !strings.Contains(msg.Content(), "Tool result:") {
				content := msg.Content()
				if len(content) > 100 {
					content = content[:100] + "..."
				}
				summary.WriteString(fmt.Sprintf("Assistant: %s\n", content))
			}
		}
	}

	return summary.String()
}

// chatWithThinkingIfSupported uses thinking if the LLM client supports it
func (r *ReAct) chatWithThinkingIfSupported(ctx context.Context, messages []message.Message, thinkingChan chan<- string) (message.Message, error) {
	return r.llmClient.Chat(ctx, messages, true, thinkingChan)
}

// chatWithToolChoice uses tool choice control if the LLM client supports it
func (r *ReAct) chatWithToolChoice(ctx context.Context, messages []message.Message, toolChoice domain.ToolChoice, thinkingChan chan<- string) (message.Message, error) {
	// Check if the client supports tool calling with tool choice
	if toolClient, ok := r.llmClient.(domain.ToolCallingLLM); ok {
		return toolClient.ChatWithToolChoice(ctx, messages, toolChoice, true, thinkingChan)
	}

	// If the client doesn't support tool choice, fall back to regular chat
	// This ensures compatibility with non-tool-calling clients
	return r.llmClient.Chat(ctx, messages, true, thinkingChan)
}

// annotateAndLogUsage attaches token usage (when available) to the response message
// and prints a concise usage line for quick visibility.
func (r *ReAct) annotateAndLogUsage(resp message.Message) {
	// Only log usage for assistant/reasoning messages to avoid repeating the
	// same usage for tool call placeholders (no new model tokens consumed yet).
	switch resp.Type() {
	case message.MessageTypeToolCall, message.MessageTypeToolCallBatch:
		return
	}

	// Get token usage if available
	if usageProvider, ok := r.llmClient.(domain.TokenUsageProvider); ok {
		if usage, ok2 := usageProvider.LastTokenUsage(); ok2 {
			// Attach to message for persistence in state
			resp.SetTokenUsage(usage.InputTokens, usage.OutputTokens, usage.TotalTokens)
			// Note: Token and context display moved to context display below input prompt
		}
	}
}

// Run processes input using the configured maxIterations.
// Optional images are base64-encoded strings attached to the user message for vision-capable models.
func (r *ReAct) Run(ctx context.Context, input string, images ...string) (message.Message, error) {
	// Create internal thinking channel to convert string chunks to ThinkingChunk events
	r.thinkingChan = make(chan string, 10)
	go func() {
		for chunk := range r.thinkingChan {
			// Only emit non-empty chunks - empty strings were used for end signaling
			if chunk != "" {
				r.eventEmitter.EmitEvent(events.EventTypeThinkingChunk, events.ThinkingChunkData{
					Content: chunk,
				})
			}
		}
	}()

	// Add user message to state (enriched with todos if available)
	var userMessage message.Message
	if len(images) > 0 {
		userMessage = message.NewChatMessageWithImages(message.MessageTypeUser, input, images)
	} else {
		userMessage = message.NewChatMessage(message.MessageTypeUser, input)
	}
	r.state.AddMessage(userMessage)

	r.status = domain.AgentStatusRunning
	msg, err := r.runInternal(ctx)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to run internal processing")
	}

	return msg, nil
}

// Resume processes input using the configured maxIterations
func (r *ReAct) Resume(ctx context.Context) (message.Message, error) {
	r.status = domain.AgentStatusRunning

	if r.pendingToolCall != nil {
		resp := r.pendingToolCall
		r.pendingToolCall = nil

		done, err := r.processResponse(ctx, r.currentIteration, resp)
		if err != nil {
			return nil, err
		}
		if done {
			r.status = domain.AgentStatusCompleted
			return resp, nil
		}
	}

	msg, err := r.runInternal(ctx)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to run internal processing")
	}

	return msg, nil
}

func (r *ReAct) Close() {
	close(r.thinkingChan)
}

func (r *ReAct) GetStatus() domain.AgentStatus {
	return r.status
}

func (r *ReAct) GetPendingToolCall() message.Message {
	return r.pendingToolCall
}

func (r *ReAct) CancelPendingToolCall() {
	if r.pendingToolCall != nil {
		if toolCall, ok := r.pendingToolCall.(*message.ToolCallMessage); ok {
			// Create a declined tool result message to complete the tool call/result pair
			declinedResult := message.NewToolResultMessage(
				toolCall.ID(),
				"",
				"Operation cancelled by user",
			)

			// Add the declined result to state to complete the pair
			r.state.AddMessage(declinedResult)
		}

		r.pendingToolCall = nil
	}
	r.status = domain.AgentStatusRunning
}

// runInternal processes input using the configured maxIterations
func (r *ReAct) runInternal(ctx context.Context) (message.Message, error) {
	for ; r.currentIteration < r.maxIterations; r.currentIteration++ {
		// Check for context cancellation (e.g., Ctrl+C)
		select {
		case <-ctx.Done():
			// Context was cancelled; log and bubble up cancellation without adding messages
			reactLogger.InfoWithIntention(pkgLogger.IntentionCancel, "Operation cancelled by user. History preserved.")
			return nil, ctx.Err()
		default:
			// Continue with normal execution
		}

		// Remove any previous situation messages to avoid context contamination
		if removedCount := r.state.RemoveMessagesBySource(message.MessageSourceSituation); removedCount > 0 {
			reactLogger.DebugWithIntention(pkgLogger.IntentionDebug, "Removed previous situation messages", "count", removedCount)
		}

		r.situation.InjectMessage(r.state, r.currentIteration, r.maxIterations)

		// Apply mandatory cleanup (remove images, situation messages) every iteration
		if err := r.state.CleanupMandatory(); err != nil {
			return nil, fmt.Errorf("failed to perform mandatory cleanup: %w", err)
		}

		// Apply compaction only if token usage exceeds 70% threshold
		// This preserves conversation context until we approach token limits
		maxTokensEstimate := r.estimateContextWindow()
		const compactionThreshold = 70.0 // 70% threshold
		if err := r.state.CompactIfNeeded(ctx, r.llmClient, maxTokensEstimate, compactionThreshold); err != nil {
			return nil, fmt.Errorf("failed to compact messages when needed: %w", err)
		}
		messages := r.state.GetMessages()

		// Use tool calling if available, otherwise fall back to thinking/regular chat
		var resp message.Message
		var err error

		// Check if we have tools available and should use tool calling
		if r.toolManager != nil && len(r.toolManager.GetTools()) > 0 {
			// Use tool choice auto to let the LLM decide when to use tools
			resp, err = r.chatWithToolChoice(ctx, messages, domain.ToolChoice{Type: domain.ToolChoiceAuto}, r.thinkingChan)
		} else {
			// Fall back to thinking if supported, otherwise regular chat
			resp, err = r.chatWithThinkingIfSupported(ctx, messages, r.thinkingChan)
		}

		if err != nil {
			// Check if the error is due to context cancellation
			if ctx.Err() == context.Canceled {
				reactLogger.InfoWithIntention(pkgLogger.IntentionCancel, "Operation cancelled by user during LLM call. History preserved.")
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("failed to get response from LLM client: %w", err)
		}

		// Clear waiting indicator and show minified response
		fmt.Print("\r                    \r") // Clear the "Thinking..." line
		// Annotate and log token usage when available
		r.annotateAndLogUsage(resp)

		// Check tool call if it requires user's approval (file writing operations and bash commands)
		if toolCall, ok := resp.(*message.ToolCallMessage); ok {
			toolName := string(toolCall.ToolName())

			// Check for file operations that require approval
			requiresApproval := toolName == "write_file" || toolName == "edit_file" || toolName == "multi_edit"

			// Check for bash commands that may require approval
			if !requiresApproval && (toolName == "bash") {
				requiresApproval = r.bashCommandRequiresApproval(toolCall)
			}

			if requiresApproval {
				r.pendingToolCall = toolCall
				r.status = domain.AgentStatusWaitingForApproval
				return nil, ErrWaitingForApproval
			}
		}

		done, err := r.processResponse(ctx, r.currentIteration, resp)
		if err != nil {
			return nil, err
		}
		if done {
			r.status = domain.AgentStatusCompleted
			return resp, nil
		}
	}

	// TBD: If it exhausted with tool calls, we might want to drop it to prevent Anthropic's error.
	return nil, fmt.Errorf("exceeded maximum loop limit (%d) without a valid response", r.maxIterations)
}

// processResponse processes input using the configured maxIterations
func (r *ReAct) processResponse(ctx context.Context, currentIter int, resp message.Message) (bool, error) {
	var done bool

	switch resp := resp.(type) {
	case *message.ChatMessage:
		// Add assistant response to state
		r.state.AddMessage(resp)
		// Check if this is reasoning (intermediate thinking) vs final answer
		if resp.Type() == message.MessageTypeReasoning {
			// Continue the ReAct loop for reasoning messages
			// (Debug logging removed for cleaner output - flow continues automatically)
		} else {
			// Return for final answers (MessageTypeAssistant)
			// (Debug logging removed for cleaner output - final answer reached)
			r.emitEventWithIteration(events.EventTypeResponse, events.ResponseData{
				Message: resp,
			}, currentIter, r.maxIterations)
			done = true
		}

	case *message.ToolCallMessage:
		// Record the tool call message in state
		r.state.AddMessage(resp)
		toolCall := resp

		// Check for cancellation before tool execution
		select {
		case <-ctx.Done():
			reactLogger.InfoWithIntention(pkgLogger.IntentionCancel, "Operation cancelled by user during tool execution. History preserved.")
			return done, ctx.Err()
		default:
		}

		// Emit tool call start event
		r.eventEmitter.EmitEvent(events.EventTypeToolCallStart, events.ToolCallStartData{
			ToolName:  string(toolCall.ToolName()),
			Arguments: r.summarizeToolArgs(toolCall.ToolArguments()),
			CallID:    "", // Could add call ID if needed
		})
		msg, err := r.handleToolCall(ctx, toolCall)
		if err != nil {
			return done, fmt.Errorf("failed to handle tool call: %w", err)
		}

		// Show truncated tool result
		r.printTruncatedToolResult(msg)

		// Add tool result to state
		r.state.AddMessage(msg)

		// Continue to next iteration to process the tool result

	case *message.ToolCallBatchMessage:
		// Execute multiple tools within a single model turn to reduce loops
		batch := resp
		calls := batch.Calls()
		for _, call := range calls {
			// Check for cancellation before each tool in the batch
			select {
			case <-ctx.Done():
				reactLogger.InfoWithIntention(pkgLogger.IntentionCancel, "Operation cancelled by user during batch tool execution. History preserved.")
				return done, ctx.Err()
			default:
			}

			// Add each tool call message to state for transcript consistency
			r.state.AddMessage(call)
			// Emit tool call start event for batch call
			r.eventEmitter.EmitEvent(events.EventTypeToolCallStart, events.ToolCallStartData{
				ToolName:  string(call.ToolName()),
				Arguments: r.summarizeToolArgs(call.ToolArguments()),
				CallID:    "", // Could add call ID if needed
			})
			msg, err := r.handleToolCall(ctx, call)
			if err != nil {
				return done, fmt.Errorf("failed to handle tool call (batch): %w", err)
			}
			r.printTruncatedToolResult(msg)
			r.state.AddMessage(msg)
		}
		// After executing the batch, continue the loop to let the model consume results
	default:
		return done, fmt.Errorf("unexpected response type: %T", resp)
	}

	return done, nil
}

func (r *ReAct) handleToolCall(ctx context.Context, toolCall *message.ToolCallMessage) (message.Message, error) {
	id := toolCall.ID()
	toolName := toolCall.ToolName()
	toolArgs := toolCall.ToolArguments()

	// Execute tool and get structured result
	toolResult, err := r.toolManager.CallTool(ctx, toolName, toolArgs)
	if err != nil {
		// Don't return an error - create a tool result message with the error instead
		// This allows the agent to continue and let the LLM see the error message
		return message.NewToolResultMessage(id, "", fmt.Sprintf("Tool execution failed: %v", err)), nil
	}

	// Handle structured tool result
	var resp message.Message
	if len(toolResult.Images) > 0 {
		resp = message.NewToolResultMessageWithImages(id, toolResult.Text, toolResult.Images, toolResult.Error)
	} else if toolResult.Error != "" {
		resp = message.NewToolResultMessage(id, "", toolResult.Error)
	} else {
		resp = message.NewToolResultMessage(id, toolResult.Text, "")
	}

	return resp, nil
}

// printTruncatedToolResult emits tool result events
func (r *ReAct) printTruncatedToolResult(msg message.Message) {
	content := strings.TrimRight(msg.Content(), "\n")
	isError := strings.HasPrefix(content, "Error:")

	// Emit tool result event
	r.eventEmitter.EmitEvent(events.EventTypeToolResult, events.ToolResultData{
		ToolName: "", // Tool name would need to be tracked separately
		CallID:   "", // Call ID would need to be tracked separately
		Content:  content,
		IsError:  isError,
	})
}

// summarizeToolArgs produces a log-friendly version of tool arguments by truncating
// large strings and collapsing deeply nested or large collections.
func (r *ReAct) summarizeToolArgs(args message.ToolArgumentValues) message.ToolArgumentValues {
	const (
		maxStringLen  = 120 // max characters for string values
		maxArrayItems = 8   // max items to display from arrays/slices
		maxMapEntries = 12  // max entries to display from maps
		maxDepth      = 2   // max recursion depth
	)

	var summarize func(v any, depth int) any
	summarize = func(v any, depth int) any {
		if depth > maxDepth {
			return "…"
		}
		switch t := v.(type) {
		case string:
			if len(t) <= maxStringLen {
				return t
			}
			return t[:maxStringLen-3] + "..."
		case []byte:
			s := string(t)
			if len(s) <= maxStringLen {
				return s
			}
			return s[:maxStringLen-3] + "..."
		case []string:
			n := len(t)
			limit := n
			if limit > maxArrayItems {
				limit = maxArrayItems
			}
			out := make([]any, 0, limit)
			for i := 0; i < limit; i++ {
				out = append(out, summarize(t[i], depth+1))
			}
			if n > limit {
				out = append(out, fmt.Sprintf("…+%d more", n-limit))
			}
			return out
		case []any:
			n := len(t)
			limit := n
			if limit > maxArrayItems {
				limit = maxArrayItems
			}
			out := make([]any, 0, limit)
			for i := 0; i < limit; i++ {
				out = append(out, summarize(t[i], depth+1))
			}
			if n > limit {
				out = append(out, fmt.Sprintf("…+%d more", n-limit))
			}
			return out
		case map[string]any:
			out := make(map[string]any)
			count := 0
			for k, val := range t {
				if count >= maxMapEntries {
					out["…"] = fmt.Sprintf("+%d more", len(t)-count)
					break
				}
				out[k] = summarize(val, depth+1)
				count++
			}
			return out
		default:
			// Numbers, bools, and other simple types
			return t
		}
	}

	result := summarize(map[string]any(args), 0)
	if summarizedMap, ok := result.(map[string]any); ok {
		return message.ToolArgumentValues(summarizedMap)
	}
	// Fallback to original args if something went wrong
	return args
}

// emitEventWithIteration emits an event with iteration context
func (r *ReAct) emitEventWithIteration(eventType events.EventType, data interface{}, currentIteration, maxIterations int) {
	event := events.AgentEvent{
		Type:      eventType,
		Timestamp: time.Now(),
		Data:      data,
		Iteration: &events.IterationInfo{
			Current: currentIteration,
			Maximum: maxIterations,
		},
	}

	for _, handler := range r.eventEmitter.(*events.SimpleEventEmitter).GetHandlers() {
		handler(event)
	}
}

// estimateContextWindow estimates the context window size based on common model patterns
func (r *ReAct) estimateContextWindow() int {
	// This is a conservative estimation based on common model types
	// In the future, this should be replaced with dynamic model capability detection

	// Try to get client type information if possible
	clientType := fmt.Sprintf("%T", r.llmClient)

	switch {
	case strings.Contains(clientType, "anthropic"):
		return 200000 // Claude models typically have 200k+ context windows
	case strings.Contains(clientType, "openai"):
		return 128000 // GPT-4o models have 128k context windows
	case strings.Contains(clientType, "gemini"):
		return 1000000 // Gemini models have very large context windows (1M+)
	case strings.Contains(clientType, "ollama"):
		return 128000 // Most modern Ollama models support 128k context
	default:
		return 100000 // Conservative fallback for unknown models
	}
}

// bashCommandRequiresApproval checks if a bash command requires user approval
func (r *ReAct) bashCommandRequiresApproval(toolCall *message.ToolCallMessage) bool {
	toolName := string(toolCall.ToolName())
	args := toolCall.ToolArguments()

	var command string

	// Extract command based on tool type
	switch toolName {
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			command = cmd
		}
	default:
		return false
	}

	if command == "" {
		return false
	}

	// Check if command is whitelisted using default whitelist
	// Note: This uses a hardcoded whitelist that should match the settings defaults
	// In the future, this could be improved by accessing the actual settings
	return r.isCommandNotWhitelisted(command)
}

// isCommandNotWhitelisted checks if a command is not in the default whitelist
// This is a simplified version that should match the BashToolManager logic
func (r *ReAct) isCommandNotWhitelisted(command string) bool {
	// Default whitelist (should match settings defaults)
	defaultWhitelist := []string{
		"go build", "go test", "go run", "go mod tidy", "go fmt", "go vet",
		"git status", "git log", "git diff",
		"ls", "pwd", "cat", "head", "tail", "grep", "find", "echo", "which",
		"make", "npm install", "npm run", "npm test",
	}

	command = strings.TrimSpace(command)

	for _, whitelisted := range defaultWhitelist {
		if strings.HasPrefix(command, whitelisted) {
			// Make sure it's a complete word match
			if len(command) == len(whitelisted) {
				return false
			}
			// Check if next character is a space (allowing arguments)
			if len(command) > len(whitelisted) {
				nextChar := command[len(whitelisted)]
				if nextChar == ' ' || nextChar == '\t' {
					return false
				}
			}
		}
	}

	// Command not in whitelist, requires approval
	return true
}
