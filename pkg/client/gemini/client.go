package gemini

import (
	"context"
	"fmt"
	"os"
	"strings"

	"google.golang.org/genai"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
	"github.com/fpt/klein-cli/pkg/message"
)

var geminiLogger = pkgLogger.NewComponentLogger("gemini-client")

// GeminiCore holds shared resources for Gemini clients
type GeminiCore struct {
	client    *genai.Client
	model     string
	maxTokens int
}

// GeminiClient implements ToolCallingLLM and VisionLLM interfaces
type GeminiClient struct {
	*GeminiCore
	toolManager domain.ToolManager

	// Telemetry and caching/session hints
	lastUsage message.TokenUsage
	sessionID string
	cacheOpts domain.ModelSideCacheOptions
}

// NewGeminiClient creates a new Gemini client with the specified model
func NewGeminiClient(model string) (*GeminiClient, error) {
	return NewGeminiClientWithTokens(model, 0) // 0 = use default
}

// NewGeminiClientWithTokens creates a new Gemini client with configurable maxTokens
func NewGeminiClientWithTokens(model string, maxTokens int) (*GeminiClient, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY environment variable not set")
	}

	ctx := context.Background()
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	// Validate and map model name
	geminiModel := getGeminiModel(model)

	// Use default maxTokens if not specified
	if maxTokens <= 0 {
		maxTokens = getModelCapabilities(geminiModel).MaxTokens
	}

	core := &GeminiCore{
		client:    client,
		model:     geminiModel,
		maxTokens: maxTokens,
	}

	return &GeminiClient{
		GeminiCore: core,
	}, nil
}

// NewGeminiClientFromCore creates a new client instance from existing core (for factory pattern)
func NewGeminiClientFromCore(core *GeminiCore) *GeminiClient {
	return &GeminiClient{
		GeminiCore: core,
	}
}

// ModelIdentifier implementation
func (c *GeminiClient) ModelID() string { return c.model }

// ContextWindowProvider implementation
func (c *GeminiClient) MaxContextTokens() int {
	caps := getModelCapabilities(c.model)
	if caps.MaxContextWindow > 0 {
		return caps.MaxContextWindow
	}
	// Conservative default for Gemini 2.5 family
	return 1048576
}

// TokenUsageProvider implementation
func (c *GeminiClient) LastTokenUsage() (message.TokenUsage, bool) {
	if c.lastUsage.InputTokens != 0 || c.lastUsage.OutputTokens != 0 || c.lastUsage.TotalTokens != 0 {
		return c.lastUsage, true
	}
	return message.TokenUsage{}, false
}

// SessionAware implementation
func (c *GeminiClient) SetSessionID(id string) { c.sessionID = id }
func (c *GeminiClient) SessionID() string      { return c.sessionID }

// ModelSideCacheConfigurator implementation (store hints for later use)
func (c *GeminiClient) ConfigureModelSideCache(opts domain.ModelSideCacheOptions) { c.cacheOpts = opts }

// Chat implements the basic LLM interface with thinking control
func (c *GeminiClient) Chat(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	// Convert internal messages to Gemini format
	geminiContents := make([]*genai.Content, 0)
	var systemInstruction *genai.Content

	for _, msg := range messages {
		switch msg.Type() {
		case message.MessageTypeUser:
			// Handle images if present
			if images := msg.Images(); len(images) > 0 {
				// Create parts with both text and images
				parts := []*genai.Part{}
				if content := msg.Content(); content != "" {
					parts = append(parts, &genai.Part{Text: content})
				}
				// TODO: Implement image handling with proper base64 decoding
				// For now, just add text content
				geminiContents = append(geminiContents, genai.NewContentFromParts(parts, genai.RoleUser))
			} else {
				geminiContents = append(geminiContents, genai.NewContentFromText(msg.Content(), genai.RoleUser))
			}

		case message.MessageTypeAssistant:
			// Add assistant messages as context
			geminiContents = append(geminiContents, genai.NewContentFromText(msg.Content(), genai.RoleModel))

		case message.MessageTypeSystem:
			// Use the last system message as system instruction
			systemInstruction = genai.NewContentFromText(msg.Content(), genai.RoleUser)

			// Skip tool call and result messages for basic chat
		}
	}

	// Prepare configuration with thinking support
	config := &genai.GenerateContentConfig{
		MaxOutputTokens: int32(c.maxTokens),
	}
	if systemInstruction != nil {
		config.SystemInstruction = systemInstruction
	}

	// Enable thinking if requested and model supports it
	if enableThinking && c.isThinkingCapable() {
		config.ThinkingConfig = &genai.ThinkingConfig{
			IncludeThoughts: true,
		}

		// Use streaming for progressive thinking display (no tool handling in basic chat)
		return c.chatWithStreaming(ctx, geminiContents, config, true, false, enableThinking, thinkingChan)
	}

	// Generate content using the Models interface (non-streaming)
	resp, err := c.client.Models.GenerateContent(ctx, c.model, geminiContents, config)
	if err != nil {
		return nil, fmt.Errorf("Gemini API call failed: %w", err)
	}

	// Debug: Log token usage from Gemini API response
	if resp.UsageMetadata != nil {
		maxTokens := c.maxTokens
		inputTokens := resp.UsageMetadata.PromptTokenCount
		outputTokens := resp.UsageMetadata.CandidatesTokenCount
		totalTokens := resp.UsageMetadata.TotalTokenCount
		utilizationPct := float64(outputTokens) / float64(maxTokens) * 100

		// Store token usage for telemetry consumers
		c.lastUsage = message.TokenUsage{
			InputTokens:  int(inputTokens),
			OutputTokens: int(outputTokens),
			TotalTokens:  int(totalTokens),
		}

		geminiLogger.DebugWithIntention(pkgLogger.IntentionStatistics, "Gemini API Usage", "input_tokens", inputTokens, "output_tokens", outputTokens, "total_tokens", totalTokens, "model", c.model)
		geminiLogger.DebugWithIntention(pkgLogger.IntentionStatistics, "Token utilization", "percent", fmt.Sprintf("%.1f", utilizationPct), "output", outputTokens, "max_output", maxTokens)

		// Warn if we're approaching the limit
		if utilizationPct > 90 {
			geminiLogger.Warn("Very high token usage - potential truncation risk!", "percent", fmt.Sprintf("%.1f", utilizationPct))
		} else if utilizationPct > 80 {
			geminiLogger.Warn("High token usage - approaching limit", "percent", fmt.Sprintf("%.1f", utilizationPct))
		}
	}

	if len(resp.Candidates) == 0 {
		return nil, fmt.Errorf("no response from Gemini")
	}

	// Extract text content from response
	responseText := resp.Text()
	if responseText == "" {
		return nil, fmt.Errorf("empty response from Gemini")
	}

	return message.NewChatMessage(message.MessageTypeAssistant, responseText), nil
}

// isThinkingCapable checks if the current model supports thinking
func (c *GeminiClient) isThinkingCapable() bool {
	capabilities := getModelCapabilities(c.model)
	return capabilities.IsReasoningModel
}

// chatWithStreaming handles streaming generation with progressive thinking display
// The handleTools parameter controls whether to process function calls or treat them as regular text
func (c *GeminiClient) chatWithStreaming(ctx context.Context, contents []*genai.Content, config *genai.GenerateContentConfig, showThinking bool, handleTools bool, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	// Create streaming generator
	stream := c.client.Models.GenerateContentStream(ctx, c.model, contents, config)

	var responseText strings.Builder
	var thinkingText strings.Builder
	var toolCalls []*genai.FunctionCall // Collect multiple tool calls

	// Process streaming responses using the iter.Seq2 pattern
	for resp, err := range stream {
		if err != nil {
			return nil, fmt.Errorf("Gemini streaming error: %w", err)
		}

		// Handle content candidates
		if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
			for _, part := range resp.Candidates[0].Content.Parts {
				// Handle function calls if tool handling is enabled
				if handleTools && part.FunctionCall != nil {
					// Collect tool calls for later processing
					toolCalls = append(toolCalls, part.FunctionCall)
					continue
				}

				// Handle regular text content
				if part.Text != "" {
					if showThinking {
						// Send thinking content to channel if enabled
						if enableThinking && thinkingChan != nil {
							message.SendThinkingContent(thinkingChan, part.Text)
						}
					}

					// Accumulate all text
					responseText.WriteString(part.Text)
				}
			}
		}
	}

	// Signal end of thinking if we accumulated thinking content
	if thinkingText.Len() > 0 && enableThinking && thinkingChan != nil {
		message.EndThinking(thinkingChan)
	}

	// Check if we have tool calls to return (only when tool handling is enabled)
	if handleTools && len(toolCalls) > 0 {
		// Build messages for all collected function calls
		if len(toolCalls) == 1 {
			fc := toolCalls[0]
			args := convertGeminiArgsToToolArgs(convertToolArgsToJSON(fc.Args))
			return message.NewToolCallMessage(message.ToolName(fc.Name), args), nil
		}
		calls := make([]*message.ToolCallMessage, 0, len(toolCalls))
		for _, fc := range toolCalls {
			args := convertGeminiArgsToToolArgs(convertToolArgsToJSON(fc.Args))
			calls = append(calls, message.NewToolCallMessage(message.ToolName(fc.Name), args))
		}
		return message.NewToolCallBatch(calls), nil
	}

	finalText := responseText.String()
	if finalText == "" {
		return nil, fmt.Errorf("empty response from Gemini streaming")
	}

	// If we have thinking content, create message with thinking
	if thinkingText.Len() > 0 {
		return message.NewChatMessageWithThinking(
			message.MessageTypeAssistant,
			finalText,
			thinkingText.String(),
		), nil
	}

	return message.NewChatMessage(message.MessageTypeAssistant, finalText), nil
}

// SetToolManager implements ToolCallingLLM interface
func (c *GeminiClient) SetToolManager(toolManager domain.ToolManager) {
	c.toolManager = toolManager
}

// GetToolManager implements ToolCallingLLM interface
func (c *GeminiClient) GetToolManager() domain.ToolManager {
	return c.toolManager
}

// IsToolCapable checks if the Gemini client supports native tool calling
func (c *GeminiClient) IsToolCapable() bool {
	// All Gemini 1.5+ and 2.0+ models support function calling
	return strings.Contains(c.model, "gemini-1.5") ||
		strings.Contains(c.model, "gemini-2.0") ||
		strings.Contains(c.model, "gemini-2.5")
}

// ChatWithToolChoice implements ToolCallingLLM interface with tool manager integration
func (c *GeminiClient) ChatWithToolChoice(ctx context.Context, messages []message.Message, toolChoice domain.ToolChoice, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	// Convert internal messages to Gemini format
	geminiContents := make([]*genai.Content, 0)
	var systemInstruction *genai.Content

	for _, msg := range messages {
		switch msg.Type() {
		case message.MessageTypeUser:
			// Handle images if present
			if images := msg.Images(); len(images) > 0 {
				// Create parts with both text and images
				parts := []*genai.Part{}
				if content := msg.Content(); content != "" {
					parts = append(parts, &genai.Part{Text: content})
				}
				// TODO: Implement image handling with proper base64 decoding
				// For now, just add text content
				geminiContents = append(geminiContents, genai.NewContentFromParts(parts, genai.RoleUser))
			} else {
				geminiContents = append(geminiContents, genai.NewContentFromText(msg.Content(), genai.RoleUser))
			}

		case message.MessageTypeAssistant:
			// Add assistant messages as context
			geminiContents = append(geminiContents, genai.NewContentFromText(msg.Content(), genai.RoleModel))

		case message.MessageTypeSystem:
			// Use the last system message as system instruction
			systemInstruction = genai.NewContentFromText(msg.Content(), genai.RoleUser)

		case message.MessageTypeToolCall:
			// For tool calls, represent as assistant function calls
			if toolCallMsg, ok := msg.(interface {
				ToolName() message.ToolName
				ToolArguments() message.ToolArgumentValues
			}); ok {
				argsJSON := convertToolArgsToJSON(toolCallMsg.ToolArguments())
				toolCallText := "[Function call: " + string(toolCallMsg.ToolName()) + "(" + argsJSON + ")]"
				geminiContents = append(geminiContents, genai.NewContentFromText(toolCallText, genai.RoleModel))
			}

		case message.MessageTypeToolResult:
			// Represent tool results as user messages
			if toolResultMsg, ok := msg.(interface{ Content() string }); ok {
				resultText := "[Function result: " + toolResultMsg.Content() + "]"
				geminiContents = append(geminiContents, genai.NewContentFromText(resultText, genai.RoleUser))
			}

		case message.MessageTypeToolCallBatch:
			// Skip batch containers; individual calls/results are already in the transcript
			continue
		}
	}

	// Prepare configuration
	config := &genai.GenerateContentConfig{
		MaxOutputTokens: int32(c.maxTokens),
	}
	if systemInstruction != nil {
		config.SystemInstruction = systemInstruction
	}

	// Add tools from tool manager if available
	if c.toolManager != nil {
		tools := convertToolsToGemini(c.toolManager.GetTools())
		if len(tools) > 0 {
			// Add tools to config
			config.Tools = tools

			// Set native tool choice using ToolConfig and FunctionCallingConfig
			toolConfig := convertToolChoiceToGemini(toolChoice, tools)
			if toolConfig != nil {
				config.ToolConfig = toolConfig
			}
		}
	}

	// Enable thinking for tool calling as well
	if c.isThinkingCapable() {
		config.ThinkingConfig = &genai.ThinkingConfig{
			IncludeThoughts: true,
		}

		// Use streaming for progressive thinking display with tool handling enabled
		return c.chatWithStreaming(ctx, geminiContents, config, true, true, enableThinking, thinkingChan)
	}

	// Generate content using the Models interface (non-streaming)
	resp, err := c.client.Models.GenerateContent(ctx, c.model, geminiContents, config)
	if err != nil {
		return nil, fmt.Errorf("Gemini API call failed: %w", err)
	}

	// Debug: Log token usage from Gemini API response
	if resp.UsageMetadata != nil {
		maxTokens := c.maxTokens
		inputTokens := resp.UsageMetadata.PromptTokenCount
		outputTokens := resp.UsageMetadata.CandidatesTokenCount
		totalTokens := resp.UsageMetadata.TotalTokenCount
		utilizationPct := float64(outputTokens) / float64(maxTokens) * 100

		fmt.Printf("DEBUG: Gemini API Usage - Input: %d tokens, Output: %d tokens, Total: %d tokens, Model: %s\n",
			inputTokens, outputTokens, totalTokens, c.model)
		fmt.Printf("DEBUG: Token Utilization - %.1f%% of max output tokens (%d/%d)\n",
			utilizationPct, outputTokens, maxTokens)

		// Warn if we're approaching the limit
		if utilizationPct > 90 {
			fmt.Printf("⚠️  WARNING: Very high token usage (%.1f%%) - potential truncation risk!\n", utilizationPct)
		} else if utilizationPct > 80 {
			fmt.Printf("⚠️  WARNING: High token usage (%.1f%%) - approaching limit\n", utilizationPct)
		}
	}

	// Capture token usage if available
	if resp.UsageMetadata != nil {
		c.lastUsage = message.TokenUsage{
			InputTokens:  int(resp.UsageMetadata.PromptTokenCount),
			OutputTokens: int(resp.UsageMetadata.CandidatesTokenCount),
			TotalTokens:  int(resp.UsageMetadata.TotalTokenCount),
		}
	}

	if len(resp.Candidates) == 0 {
		return nil, fmt.Errorf("no response from Gemini")
	}

	// Check if response contains function calls (collect all)
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil && len(resp.Candidates[0].Content.Parts) > 0 {
		var calls []*message.ToolCallMessage
		for _, part := range resp.Candidates[0].Content.Parts {
			if functionCall := part.FunctionCall; functionCall != nil {
				args := convertGeminiArgsToToolArgs(convertToolArgsToJSON(functionCall.Args))
				calls = append(calls, message.NewToolCallMessage(message.ToolName(functionCall.Name), args))
			}
		}
		if len(calls) == 1 {
			return calls[0], nil
		} else if len(calls) > 1 {
			return message.NewToolCallBatch(calls), nil
		}
	}

	// Extract text content from response
	responseText := resp.Text()
	if responseText == "" {
		return nil, fmt.Errorf("empty response from Gemini")
	}

	return message.NewChatMessage(message.MessageTypeAssistant, responseText), nil
}

// SupportsVision implements VisionLLM interface
func (c *GeminiClient) SupportsVision() bool {
	// Gemini Pro Vision models support vision
	return strings.Contains(c.model, "vision") || strings.Contains(c.model, "gemini-pro-vision") ||
		strings.Contains(c.model, "gemini-2.0") || strings.Contains(c.model, "gemini-1.5")
}
