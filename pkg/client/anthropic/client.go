package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

const (
	defaultMaxTokens = 8192
)

// AnthropicCore contains shared Anthropic client resources and core functionality
// This allows efficient resource sharing between different Anthropic client types
type AnthropicCore struct {
	client    *anthropic.Client
	model     string
	maxTokens int
}

// NewAnthropicCore creates a new Anthropic core with shared resources
func NewAnthropicCore(model string) (*AnthropicCore, error) {
	return NewAnthropicCoreWithTokens(model, 0) // 0 = use default
}

// NewAnthropicCoreWithTokens creates a new Anthropic core with configurable maxTokens
func NewAnthropicCoreWithTokens(model string, maxTokens int) (*AnthropicCore, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY environment variable not set")
	}

	client := anthropic.NewClient(
		option.WithAPIKey(apiKey),
	)

	// Use default if maxTokens is 0 or negative
	// NOTE: Anthropic requires minimum tokens.
	if maxTokens <= 0 || maxTokens < defaultMaxTokens {
		maxTokens = defaultMaxTokens
	}

	return &AnthropicCore{
		client:    &client,
		model:     model,
		maxTokens: maxTokens,
	}, nil
}

// AnthropicClient handles communication with Claude models
// Implements domain.ToolCallingLLM interfaces for tool calling
type AnthropicClient struct {
	*AnthropicCore
	toolManager domain.ToolManager

	// Telemetry and caching/session hints
	lastUsage message.TokenUsage
	sessionID string
	cacheOpts domain.ModelSideCacheOptions
}

// NewAnthropicClient creates a new Anthropic client with tool calling and thinking capabilities
func NewAnthropicClient(model string) (domain.ToolCallingLLM, error) {
	return NewAnthropicClientWithTokens(model, 0) // 0 = use default
}

// NewAnthropicClientWithTokens creates a new Anthropic client with configurable maxTokens
func NewAnthropicClientWithTokens(model string, maxTokens int) (domain.ToolCallingLLM, error) {
	core, err := NewAnthropicCoreWithTokens(model, maxTokens)
	if err != nil {
		return nil, err
	}

	// Return as domain.ToolCallingLLM interface
	return &AnthropicClient{
		AnthropicCore: core,
	}, nil
}

// NewAnthropicClientFromCore creates a new Anthropic client from shared core
func NewAnthropicClientFromCore(core *AnthropicCore) domain.ToolCallingLLM {
	return &AnthropicClient{
		AnthropicCore: core,
	}
}

// ModelIdentifier implementation
func (c *AnthropicClient) ModelID() string { return c.model }

// ContextWindowProvider implementation
func (c *AnthropicClient) MaxContextTokens() int {
	return getModelContextWindow(c.model)
}

// TokenUsageProvider implementation (populated from Message.Usage when available)
func (c *AnthropicClient) LastTokenUsage() (message.TokenUsage, bool) {
	if c.lastUsage.InputTokens != 0 || c.lastUsage.OutputTokens != 0 || c.lastUsage.TotalTokens != 0 {
		return c.lastUsage, true
	}
	return message.TokenUsage{}, false
}

// SessionAware implementation
func (c *AnthropicClient) SetSessionID(id string) { c.sessionID = id }
func (c *AnthropicClient) SessionID() string      { return c.sessionID }

// ModelSideCacheConfigurator implementation (store hints for later use)
func (c *AnthropicClient) ConfigureModelSideCache(opts domain.ModelSideCacheOptions) {
	c.cacheOpts = opts
}

// IsToolCapable checks if the Anthropic client supports native tool calling
func (c *AnthropicClient) IsToolCapable() bool {
	// Anthropic API always supports native tool calling
	return true
}

// ChatWithToolChoice sends a message to Claude with tool choice control
func (c *AnthropicClient) ChatWithToolChoice(ctx context.Context, messages []message.Message, toolChoice domain.ToolChoice, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	// Convert messages to Anthropic format
	anthropicMessages := toAnthropicMessages(messages)

	// Use the provided model or default to Claude Sonnet 4
	claudeModel := getAnthropicModel(c.model)

	// Get tools from tool manager if available
	var tools []anthropic.ToolUnionParam
	if c.toolManager != nil {
		tools = convertToolsToAnthropic(c.toolManager.GetTools())
	}

	// Create message params
	messageParams := anthropic.MessageNewParams{
		MaxTokens: int64(c.maxTokens),
		Messages:  anthropicMessages,
		Model:     claudeModel,
		Tools:     tools,
	}

	// Set tool choice based on the provided configuration
	if len(tools) > 0 {
		anthropicToolChoice := convertToolChoiceToAnthropic(toolChoice)
		messageParams.ToolChoice = anthropicToolChoice
	}

	// Determine if we should enable thinking (only for supported models)
	shouldEnableThinking := supportsThinking(c.model)

	// Add thinking configuration if supported
	if shouldEnableThinking {
		messageParams.Thinking = anthropic.ThinkingConfigParamUnion{
			OfEnabled: &anthropic.ThinkingConfigEnabledParam{
				BudgetTokens: int64(2048), // Set a reasonable thinking budget (minimum 1024)
			},
		}
	}

	// Always use streaming for all models (thinking display only if enabled and no tool results)
	return c.chatWithStreaming(ctx, messageParams, shouldEnableThinking, enableThinking, thinkingChan)
}

// SetToolManager sets the tool manager for dynamic tool definitions
func (c *AnthropicClient) SetToolManager(toolManager domain.ToolManager) {
	c.toolManager = toolManager
}

// SupportsVision checks if the Anthropic client supports vision/image analysis
func (c *AnthropicClient) SupportsVision() bool {
	// All Anthropic Claude models support vision
	return true
}

// ChatWithThinking sends a message to Claude with thinking control
func (c *AnthropicClient) Chat(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	// Convert messages to Anthropic format
	anthropicMessages := toAnthropicMessages(messages)

	// Use the provided model or default to Claude Sonnet 4
	claudeModel := getAnthropicModel(c.model)

	// Get tools from tool manager if available
	var tools []anthropic.ToolUnionParam
	if c.toolManager != nil {
		tools = convertToolsToAnthropic(c.toolManager.GetTools())
	}

	// Create message params with thinking enabled
	messageParams := anthropic.MessageNewParams{
		MaxTokens: int64(c.maxTokens),
		Messages:  anthropicMessages,
		Model:     claudeModel,
		Tools:     tools,
	}

	// Determine if we should enable thinking (only for supported models)
	shouldEnableThinking := enableThinking && supportsThinking(c.model)

	// Add thinking configuration if requested and supported
	if shouldEnableThinking {
		messageParams.Thinking = anthropic.ThinkingConfigParamUnion{
			OfEnabled: &anthropic.ThinkingConfigEnabledParam{
				BudgetTokens: int64(2048), // Set a reasonable thinking budget (minimum 1024)
			},
		}
	}

	// Use streaming for all models (thinking display only if enabled and supported)
	return c.chatWithStreaming(ctx, messageParams, shouldEnableThinking, enableThinking, thinkingChan)
}

// chatWithStreaming handles streaming generation with progressive thinking display using Message.Accumulate pattern
func (c *AnthropicClient) chatWithStreaming(ctx context.Context, messageParams anthropic.MessageNewParams, showThinking bool, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	// Create streaming request
	stream := c.client.Messages.NewStreaming(ctx, messageParams)

	// Use Message.Accumulate pattern for proper streaming handling
	var acc anthropic.Message
	var thinkingBuilder strings.Builder
	var signatureBuilder strings.Builder

	// Process streaming events
	for stream.Next() {
		event := stream.Current()

		// Accumulate the event into the message
		if err := acc.Accumulate(event); err != nil {
			return nil, fmt.Errorf("failed to accumulate streaming event: %w", err)
		}

		// Handle thinking display for progressive feedback
		switch eventData := event.AsAny().(type) {
		case anthropic.ContentBlockDeltaEvent:
			if delta, ok := eventData.Delta.AsAny().(anthropic.ThinkingDelta); ok {
				// Thinking content - show progressively
				if delta.Thinking != "" && showThinking {
					// Send thinking content to channel if enabled
					if enableThinking && thinkingChan != nil {
						message.SendThinkingContent(thinkingChan, delta.Thinking)
					}

					// Accumulate thinking content
					thinkingBuilder.WriteString(delta.Thinking)
				}
			} else if delta, ok := eventData.Delta.AsAny().(anthropic.SignatureDelta); ok {
				// Signature content - accumulate but don't display
				if delta.Signature != "" {
					signatureBuilder.WriteString(delta.Signature)
				}
			}

		case anthropic.ContentBlockStartEvent:
			if block, ok := eventData.ContentBlock.AsAny().(anthropic.ThinkingBlock); ok {
				// Thinking block started - send initial thinking content if present
				if block.Thinking != "" && showThinking {
					// Send thinking content to channel if enabled
					if enableThinking && thinkingChan != nil {
						message.SendThinkingContent(thinkingChan, block.Thinking)
					}
					thinkingBuilder.WriteString(block.Thinking)
				}
			}
		}
	}

	// Check for streaming errors
	if stream.Err() != nil {
		return nil, fmt.Errorf("anthropic streaming error: %w", stream.Err())
	}

	// Signal end of thinking if we accumulated thinking content
	if thinkingBuilder.Len() > 0 && enableThinking && thinkingChan != nil {
		message.EndThinking(thinkingChan)
	}

	// Now process the accumulated message like the non-streaming version
	if len(acc.Content) == 0 {
		return nil, fmt.Errorf("no content in accumulated Anthropic message")
	}

	// Handle different content block types from accumulated message
	var content string
	var toolCalls []anthropic.ToolUseBlock

	for _, contentBlock := range acc.Content {
		switch variant := contentBlock.AsAny().(type) {
		case anthropic.TextBlock:
			content += variant.Text
		case anthropic.ToolUseBlock:
			// Collect tool calls from accumulated message
			toolCalls = append(toolCalls, variant)
		case anthropic.ThinkingBlock:
			// Skip - thinking content captured via streaming events
			continue
		case anthropic.RedactedThinkingBlock:
			// Skip redacted thinking blocks
			continue
		}
	}

	// Get accumulated thinking content and signature from streaming
	finalThinking := thinkingBuilder.String()
	finalSignature := signatureBuilder.String()

	// If we have tool calls, return a batch when multiple; single otherwise
	if len(toolCalls) > 0 {
		if len(toolCalls) == 1 {
			toolCall := toolCalls[0]
			toolArgs := make(map[string]any)
			if toolCall.Input != nil {
				if err := json.Unmarshal(toolCall.Input, &toolArgs); err != nil {
					return nil, fmt.Errorf("failed to parse tool arguments: %w", err)
				}
			}
			if finalThinking != "" && finalSignature != "" {
				return message.NewToolCallMessageWithThinkingAndSignature(
					message.ToolName(toolCall.Name),
					message.ToolArgumentValues(toolArgs),
					finalThinking,
					finalSignature,
				), nil
			} else if finalThinking != "" {
				return message.NewToolCallMessageWithThinking(
					message.ToolName(toolCall.Name),
					message.ToolArgumentValues(toolArgs),
					finalThinking,
				), nil
			}
			return message.NewToolCallMessage(
				message.ToolName(toolCall.Name),
				message.ToolArgumentValues(toolArgs),
			), nil
		}

		// Build a batch of tool calls
		var calls []*message.ToolCallMessage
		for _, tc := range toolCalls {
			args := make(map[string]any)
			if tc.Input != nil {
				if err := json.Unmarshal(tc.Input, &args); err != nil {
					return nil, fmt.Errorf("failed to parse tool arguments: %w", err)
				}
			}
			// No per-call thinking; preserve thinking at batch level via situation/system as needed
			calls = append(calls, message.NewToolCallMessage(
				message.ToolName(tc.Name),
				message.ToolArgumentValues(args),
			))
		}
		return message.NewToolCallBatch(calls), nil
	}

	// Capture token usage from the accumulated message, including cache stats.
	// CacheReadInputTokens: tokens served from cache (savings).
	// CacheCreationInputTokens: tokens written into cache this call (billed at 1.25x).
	c.lastUsage = message.TokenUsage{
		InputTokens:         int(acc.Usage.InputTokens),
		OutputTokens:        int(acc.Usage.OutputTokens),
		TotalTokens:         int(acc.Usage.InputTokens + acc.Usage.OutputTokens),
		CachedTokens:        int(acc.Usage.CacheReadInputTokens),
		CacheCreationTokens: int(acc.Usage.CacheCreationInputTokens),
	}

	// Create response message with thinking content if available
	if thinkingBuilder.Len() > 0 {
		msg := message.NewChatMessageWithThinking(message.MessageTypeAssistant, content, finalThinking)
		if finalSignature != "" {
			msg.SetMetadata("anthropic_thinking_signature", finalSignature)
		}
		return msg, nil
	}

	return message.NewChatMessage(message.MessageTypeAssistant, content), nil
}
