package openai

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/option"
	"github.com/openai/openai-go/v2/responses"
	"github.com/openai/openai-go/v2/shared"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

const defaultReasoningEffort = shared.ReasoningEffortLow // Default reasoning effort for OpenAI models

// OpenAICore holds shared resources for OpenAI clients
type OpenAICore struct {
	client    *openai.Client
	model     string
	maxTokens int
	// streamingUnsupported is set to true when the API rejects streaming
	// (e.g., org not verified). Subsequent calls will avoid streaming.
	streamingUnsupported bool
}

// OpenAIClient implements ToolCallingLLM and VisionLLM interfaces
type OpenAIClient struct {
	*OpenAICore
	toolManager domain.ToolManager

	// Telemetry and caching/session hints
	lastUsage message.TokenUsage
	sessionID string
	cacheOpts domain.ModelSideCacheOptions
}

// NewOpenAIClient creates a new OpenAI client with configurable maxTokens
// maxTokens = 0 means default
func NewOpenAIClient(model string, maxTokens int) (*OpenAIClient, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY environment variable not set")
	}

	// Setup client options
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}

	// Support custom base URL (for Azure OpenAI, etc.)
	if baseURL := os.Getenv("OPENAI_BASE_URL"); baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}

	client := openai.NewClient(opts...)

	// Validate and map model name
	openaiModel := getOpenAIModel(model)

	// Use default maxTokens if not specified
	if maxTokens <= 0 {
		maxTokens = getModelCapabilities(openaiModel).MaxTokens
	}

	core := &OpenAICore{
		client:               &client,
		model:                openaiModel,
		maxTokens:            maxTokens,
		streamingUnsupported: false,
	}

	return &OpenAIClient{
		OpenAICore: core,
	}, nil
}

// NewOpenAIClientFromCore creates a new client instance from existing core (for factory pattern)
func NewOpenAIClientFromCore(core *OpenAICore) domain.ToolCallingLLM {
	return &OpenAIClient{
		OpenAICore: core,
	}
}

// ModelIdentifier implementation
func (c *OpenAIClient) ModelID() string { return c.model }

// ContextWindowProvider implementation
func (c *OpenAIClient) MaxContextTokens() int {
	caps := getModelCapabilities(c.model)
	if caps.MaxContextWindow > 0 {
		return caps.MaxContextWindow
	}
	// Conservative fallback aligned with ReAct's estimate for OpenAI
	return 128000
}

// TokenUsageProvider implementation (best-effort; populated when available)
func (c *OpenAIClient) LastTokenUsage() (message.TokenUsage, bool) {
	if c.lastUsage.InputTokens != 0 || c.lastUsage.OutputTokens != 0 || c.lastUsage.TotalTokens != 0 {
		return c.lastUsage, true
	}
	return message.TokenUsage{}, false
}

// SessionAware implementation
func (c *OpenAIClient) SetSessionID(id string) { c.sessionID = id }
func (c *OpenAIClient) SessionID() string      { return c.sessionID }

// ModelSideCacheConfigurator implementation (store hints for later use)
func (c *OpenAIClient) ConfigureModelSideCache(opts domain.ModelSideCacheOptions) {
	c.cacheOpts = opts
}

// Chat implements the basic LLM interface with thinking control
func (c *OpenAIClient) Chat(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	// Also respect cached fallback state from previous attempts
	if c.OpenAICore != nil && c.streamingUnsupported {
		enableThinking = false
	}

	// Use streaming for progressive display when thinking is enabled
	if enableThinking {
		return c.chatWithStreaming(ctx, messages, true, thinkingChan)
	}

	// Convert messages to proper structured input
	inputItems := c.convertMessagesToResponsesInputItems(messages)

	// Create response parameters
	params := responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: inputItems,
		},
		Model: shared.ChatModel(c.model),
	}

	// Add max tokens if specified
	if c.maxTokens > 0 {
		params.MaxOutputTokens = openai.Int(int64(c.maxTokens))
	}

	// Add reasoning effort for thinking models
	caps := getModelCapabilities(c.model)
	if caps.SupportsThinking {
		// Enable reasoning for GPT-5 models to see thinking process
		params.Reasoning = shared.ReasoningParam{
			Effort: defaultReasoningEffort,
		}

	}

	// Add tools support
	if c.toolManager != nil {
		domainTools := c.toolManager.GetTools()
		tools := convertTools(domainTools)

		if len(tools) > 0 {
			params.Tools = tools
			// Note: Basic chat doesn't use tool choice
			// Tool choice will be handled in the tool calling specific method
		}
	}

	// Call Responses API
	resp, err := c.client.Responses.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("Responses API call failed: %w", err)
	}

	// Capture token usage if provided
	if resp.Usage.JSON.InputTokens.Valid() || resp.Usage.JSON.OutputTokens.Valid() || resp.Usage.JSON.TotalTokens.Valid() {
		c.lastUsage = message.TokenUsage{
			InputTokens:  int(resp.Usage.InputTokens),
			OutputTokens: int(resp.Usage.OutputTokens),
			TotalTokens:  int(resp.Usage.TotalTokens),
			CachedTokens: int(resp.Usage.InputTokensDetails.CachedTokens),
		}
	}

	// Extract response text and reasoning content
	outputText := resp.OutputText()
	var reasoningContent string

	// Check for reasoning content in the response
	for _, outputItem := range resp.Output {
		if variant, ok := outputItem.AsAny().(responses.ResponseReasoningItem); ok {
			// Extract reasoning content if available
			if len(variant.Content) > 0 {
				var reasoningParts []string
				for _, content := range variant.Content {
					if content.Text != "" {
						reasoningParts = append(reasoningParts, content.Text)
						if os.Getenv("DEBUG_TOOLS") == "1" {
							fmt.Printf("DEBUG: Non-streaming reasoning content found: '%s'\n", content.Text)
						}
					}
				}
				reasoningContent = strings.Join(reasoningParts, "\n")
			}
			// Also debug the summary content
			if len(variant.Summary) > 0 {
				for _, summary := range variant.Summary {
					if summary.Text != "" && os.Getenv("DEBUG_TOOLS") == "1" {
						fmt.Printf("DEBUG: Non-streaming reasoning summary found: '%s'\n", summary.Text)
					}
				}
			}
		}
	}

	if outputText == "" {
		// Debug: Check what's in the response
		if os.Getenv("DEBUG_TOOLS") == "1" {
			fmt.Printf("DEBUG: Empty OutputText - Response ID: %s, Output items: %d\n", resp.ID, len(resp.Output))
			for i, item := range resp.Output {
				fmt.Printf("DEBUG: Output[%d] Type: %s\n", i, item.Type)
			}
		}
		return nil, fmt.Errorf("empty response from Responses API")
	}

	// Create response message with thinking content if available
	var responseMessage message.Message
	if reasoningContent != "" {
		responseMessage = message.NewChatMessageWithThinking(message.MessageTypeAssistant, outputText, reasoningContent)
	} else {
		responseMessage = message.NewChatMessage(message.MessageTypeAssistant, outputText)
	}

	// TODO: Handle tool calls when implementing tool support

	return responseMessage, nil
}

// chatWithStreaming handles streaming responses using the Responses API
func (c *OpenAIClient) chatWithStreaming(ctx context.Context, messages []message.Message, showThinking bool, thinkingChan chan<- string) (message.Message, error) {
	// Convert messages to proper structured input
	inputItems := c.convertMessagesToResponsesInputItems(messages)

	// Create response parameters
	params := responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: inputItems,
		},
		Model: shared.ChatModel(c.model),
	}

	// Add max tokens if specified
	if c.maxTokens > 0 {
		params.MaxOutputTokens = openai.Int(int64(c.maxTokens))
	}

	// Add reasoning effort for thinking models
	caps := getModelCapabilities(c.model)
	if caps.SupportsThinking && showThinking {
		// Enable reasoning for GPT-5 models to see thinking process
		params.Reasoning = shared.ReasoningParam{
			Effort: defaultReasoningEffort,
		}

	}

	// Add tools support
	if c.toolManager != nil {
		domainTools := c.toolManager.GetTools()
		tools := convertTools(domainTools)

		if len(tools) > 0 {
			params.Tools = tools
			// Note: Basic streaming doesn't use tool choice
			// Tool choice will be handled in the tool calling specific method
		}
	}

	// Create streaming response
	stream := c.client.Responses.NewStreaming(ctx, params)

	var responseBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var completeText string

	// Process streaming chunks
	for stream.Next() {
		event := stream.Current()

		// Check the event type to handle different kinds of deltas appropriately
		switch eventData := event.AsAny().(type) {
		case responses.ResponseTextDeltaEvent:
			// This is regular text content - display and accumulate it
			if eventData.Delta != "" {
				fmt.Print(eventData.Delta)
				responseBuilder.WriteString(eventData.Delta)
			}
		case responses.ResponseFunctionCallArgumentsDeltaEvent:
			// This is tool call arguments - display but don't accumulate as response text
			if eventData.Delta != "" {
				fmt.Print(eventData.Delta)
				// Note: We don't add this to responseBuilder since it's tool call args
			}
		case responses.ResponseReasoningTextDeltaEvent:
			// This is reasoning content - accumulate it for thinking
			if eventData.Delta != "" {
				reasoningBuilder.WriteString(eventData.Delta)
				// Send reasoning content to thinking channel if enabled
				if showThinking && thinkingChan != nil {
					message.SendThinkingContent(thinkingChan, eventData.Delta)
				}
				if os.Getenv("DEBUG_TOOLS") == "1" {
					fmt.Printf("[DEBUG: ReasoningDelta: '%s']", eventData.Delta)
				}
			}
		case responses.ResponseReasoningTextDoneEvent:
			// Reasoning is complete
			if reasoningBuilder.Len() > 0 {
				// Signal end of thinking
				if showThinking && thinkingChan != nil {
					message.EndThinking(thinkingChan)
				}
				if os.Getenv("DEBUG_TOOLS") == "1" {
					fmt.Printf("DEBUG: ReasoningDone, total length: %d\n", reasoningBuilder.Len())
				}
			}
		default:
			// For other event types, try to extract text delta
			if textEvent := event.AsResponseOutputTextDelta(); textEvent.Delta != "" {
				fmt.Print(textEvent.Delta)
				responseBuilder.WriteString(textEvent.Delta)
			}
		}

		// Check if we have a completed response
		if completedEvent := event.AsResponseCompleted(); completedEvent.Type != "" {
			fmt.Println()
			break
		}
	}

	// Check for streaming errors
	if stream.Err() != nil {
		// If streaming isn't allowed (e.g., org not verified), fallback to non-streaming
		if isStreamingUnsupportedError(stream.Err()) {
			// Cache the failure and avoid streaming for the rest of the session
			if c.OpenAICore != nil {
				if !c.streamingUnsupported {
					fmt.Fprintln(os.Stderr, "OpenAI: streaming not permitted; falling back to non-streaming.")
				}
				c.streamingUnsupported = true
			}
			// Perform the same request without streaming
			resp, err := c.client.Responses.New(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("failed non-streaming fallback after streaming unsupported: %w", err)
			}

			// Capture token usage if provided
			if resp.Usage.JSON.InputTokens.Valid() || resp.Usage.JSON.OutputTokens.Valid() || resp.Usage.JSON.TotalTokens.Valid() {
				c.lastUsage = message.TokenUsage{
					InputTokens:  int(resp.Usage.InputTokens),
					OutputTokens: int(resp.Usage.OutputTokens),
					TotalTokens:  int(resp.Usage.TotalTokens),
					CachedTokens: int(resp.Usage.InputTokensDetails.CachedTokens),
				}
			}

			// Extract response text and reasoning content
			outputText := resp.OutputText()
			var reasoningContent string
			for _, outputItem := range resp.Output {
				if variant, ok := outputItem.AsAny().(responses.ResponseReasoningItem); ok {
					if len(variant.Content) > 0 {
						var reasoningParts []string
						for _, content := range variant.Content {
							if content.Text != "" {
								reasoningParts = append(reasoningParts, content.Text)
							}
						}
						reasoningContent = strings.Join(reasoningParts, "\n")
					}
				}
			}

			if outputText == "" {
				return nil, fmt.Errorf("empty response from Responses API (non-streaming fallback)")
			}

			if reasoningContent != "" {
				return message.NewChatMessageWithThinking(message.MessageTypeAssistant, outputText, reasoningContent), nil
			}
			return message.NewChatMessage(message.MessageTypeAssistant, outputText), nil
		}
		return nil, fmt.Errorf("Responses API streaming error: %w", stream.Err())
	}

	// Use complete text if available, otherwise use accumulated deltas
	finalText := completeText
	if finalText == "" {
		finalText = responseBuilder.String()
	}

	if finalText == "" {
		return nil, fmt.Errorf("empty response from Responses API streaming")
	}

	// Create response message with thinking content if available
	var responseMessage message.Message
	if reasoningContent := reasoningBuilder.String(); reasoningContent != "" {
		if os.Getenv("DEBUG_TOOLS") == "1" {
			fmt.Printf("DEBUG: Creating message with streaming reasoning content: '%s'\n", reasoningContent)
		}
		responseMessage = message.NewChatMessageWithThinking(message.MessageTypeAssistant, finalText, reasoningContent)
	} else {
		responseMessage = message.NewChatMessage(message.MessageTypeAssistant, finalText)
		if os.Getenv("DEBUG_TOOLS") == "1" {
			fmt.Printf("DEBUG: Creating message WITHOUT thinking content\n")
		}
	}

	// TODO: Handle tool calls when implementing tool support

	return responseMessage, nil
}

// convertMessagesToResponsesInputItems converts internal messages to structured input items for Responses API
func (c *OpenAIClient) convertMessagesToResponsesInputItems(messages []message.Message) responses.ResponseInputParam {
	var inputItems responses.ResponseInputParam

	for _, msg := range messages {
		switch msg.Type() {
		case message.MessageTypeUser:
			// TODO: Should use ResponseInputItemParamOfInputMessage
			inputItem := responses.ResponseInputItemParamOfMessage(msg.Content(), responses.EasyInputMessageRoleUser)
			inputItems = append(inputItems, inputItem)

		case message.MessageTypeAssistant:
			// TODO: Should use ResponseInputItemParamOfOutputMessage and ResponseInputItemParamOfReasoning
			inputItem := responses.ResponseInputItemParamOfMessage(msg.Content(), responses.EasyInputMessageRoleAssistant)
			inputItems = append(inputItems, inputItem)

		case message.MessageTypeSystem:
			// TODO: Should use ResponseInputItemParamOfInputMessage
			inputItem := responses.ResponseInputItemParamOfMessage(msg.Content(), responses.EasyInputMessageRoleSystem)
			inputItems = append(inputItems, inputItem)

		case message.MessageTypeToolCall:
			// Cast to ToolCallMessage to access tool-specific methods
			if toolCallMsg, ok := msg.(*message.ToolCallMessage); ok {
				// Convert tool arguments to JSON string
				argsJSON := convertToolArgsToJSON(toolCallMsg.ToolArguments())

				// Use proper function call input item
				inputItem := responses.ResponseInputItemParamOfFunctionCall(
					argsJSON,
					toolCallMsg.ID(), // Use message ID as call ID
					toolCallMsg.ToolName().String(),
				)
				inputItems = append(inputItems, inputItem)
			} else {
				// Fallback to message representation if cast fails
				inputItem := responses.ResponseInputItemParamOfMessage(
					"[Tool call: "+msg.Content()+"]",
					responses.EasyInputMessageRoleAssistant,
				)
				inputItems = append(inputItems, inputItem)
			}

		case message.MessageTypeToolResult:
			// Cast to ToolResultMessage to access result-specific methods
			if toolResultMsg, ok := msg.(*message.ToolResultMessage); ok {
				// Use proper function call output input item
				inputItem := responses.ResponseInputItemParamOfFunctionCallOutput(
					toolResultMsg.ID(), // Use message ID as call ID (should match the corresponding tool call)
					toolResultMsg.Result,
				)
				inputItems = append(inputItems, inputItem)
			} else {
				// Fallback to message representation if cast fails
				inputItem := responses.ResponseInputItemParamOfMessage(
					"[Tool result: "+msg.Content()+"]",
					responses.EasyInputMessageRoleUser,
				)
				inputItems = append(inputItems, inputItem)
			}

		case message.MessageTypeToolCallBatch:
			// Batch messages are for internal coordination; skip sending them back to the model
			// Individual tool calls/results are already added to the transcript
			continue

		default:
			// Default to user role for unknown message types
			inputItem := responses.ResponseInputItemParamOfMessage(msg.Content(), responses.EasyInputMessageRoleUser)
			inputItems = append(inputItems, inputItem)
		}
	}

	// TODO: When Responses API exposes prompt caching controls in openai-go,
	// we can apply c.cacheOpts (PromptCachingEnabled/PolicyHint/SessionID) to
	// the appropriate input items or request params here.

	return inputItems
}

// SetToolManager implements ToolCallingLLM interface
func (c *OpenAIClient) SetToolManager(toolManager domain.ToolManager) {
	c.toolManager = toolManager
}

// IsToolCapable checks if the OpenAI client supports native tool calling
func (c *OpenAIClient) IsToolCapable() bool {
	// Check if the current model supports tool calling
	caps := getModelCapabilities(c.model)
	return caps.SupportsToolCalling
}

// ChatWithToolChoice implements ToolCallingLLM interface with native OpenAI tool calling
func (c *OpenAIClient) ChatWithToolChoice(ctx context.Context, messages []message.Message, toolChoice domain.ToolChoice, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	// Convert messages to proper structured input
	inputItems := c.convertMessagesToResponsesInputItems(messages)

	// Create response parameters
	params := responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: inputItems,
		},
		Model: shared.ChatModel(c.model),
	}

	// Add max tokens if specified
	if c.maxTokens > 0 {
		params.MaxOutputTokens = openai.Int(int64(c.maxTokens))
	}

	// Add reasoning effort for thinking models
	caps := getModelCapabilities(c.model)
	if caps.SupportsThinking {
		// Enable reasoning for GPT-5 models to see thinking process
		params.Reasoning = shared.ReasoningParam{
			Effort: defaultReasoningEffort,
		}

	}

	// Add tools and tool choice
	if c.toolManager != nil {
		domainTools := c.toolManager.GetTools()
		tools := convertTools(domainTools)

		if len(tools) > 0 {
			params.Tools = tools

			// Set tool choice
			toolChoiceParam := convertToolChoice(toolChoice)
			if toolChoiceParam != nil {
				params.ToolChoice = *toolChoiceParam
			}
		}
	}

	if c.OpenAICore != nil && c.OpenAICore.streamingUnsupported {
		// Direct non-streaming path using the same parsing as the streaming fallback
		return c.chatWithToolChoiceNonStreaming(ctx, params, enableThinking, thinkingChan)
	}

	// Use streaming for progressive tool call display
	return c.chatWithToolChoiceStreaming(ctx, params, enableThinking, thinkingChan)
}

// chatWithToolChoiceStreaming handles streaming tool calls using Responses API
func (c *OpenAIClient) chatWithToolChoiceStreaming(ctx context.Context, params responses.ResponseNewParams, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	// Create streaming response
	stream := c.client.Responses.NewStreaming(ctx, params)

	var responseBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var completeText string

	// Process streaming chunks
	for stream.Next() {
		event := stream.Current()

		// Check the event type to handle different kinds of deltas appropriately
		switch eventData := event.AsAny().(type) {
		case responses.ResponseTextDeltaEvent:
			// This is regular text content - display and accumulate it
			if eventData.Delta != "" {
				fmt.Print(eventData.Delta)
				responseBuilder.WriteString(eventData.Delta)
			}
		case responses.ResponseFunctionCallArgumentsDeltaEvent:
			// This is tool call arguments - display but don't accumulate as response text
			if eventData.Delta != "" {
				fmt.Print(eventData.Delta)
				// Note: We don't add this to responseBuilder since it's tool call args
			}
		case responses.ResponseReasoningTextDeltaEvent:
			// This is reasoning content - accumulate it for thinking
			if eventData.Delta != "" {
				reasoningBuilder.WriteString(eventData.Delta)
				// Send reasoning content to thinking channel if enabled
				if enableThinking && thinkingChan != nil {
					message.SendThinkingContent(thinkingChan, eventData.Delta)
				}
				if os.Getenv("DEBUG_TOOLS") == "1" {
					fmt.Printf("[DEBUG: ReasoningDelta: '%s']", eventData.Delta)
				}
			}
		case responses.ResponseReasoningTextDoneEvent:
			// Reasoning is complete
			if reasoningBuilder.Len() > 0 {
				// Signal end of thinking
				if enableThinking && thinkingChan != nil {
					message.EndThinking(thinkingChan)
				}
				if os.Getenv("DEBUG_TOOLS") == "1" {
					fmt.Printf("DEBUG: ReasoningDone, total length: %d\n", reasoningBuilder.Len())
				}
			}
		default:
			// For other event types, try to extract text delta
			if textEvent := event.AsResponseOutputTextDelta(); textEvent.Delta != "" {
				fmt.Print(textEvent.Delta)
				responseBuilder.WriteString(textEvent.Delta)
			}
		}

		// Check if we have a completed response
		if completedEvent := event.AsResponseCompleted(); completedEvent.Type != "" {
			fmt.Println()
			break
		}
	}

	// Check for streaming errors
	if stream.Err() != nil {
		// If streaming isn't allowed (e.g., org not verified), fallback to non-streaming
		if isStreamingUnsupportedError(stream.Err()) {
			if c.OpenAICore != nil {
				if !c.OpenAICore.streamingUnsupported {
					fmt.Fprintln(os.Stderr, "OpenAI: streaming not permitted; falling back to non-streaming.")
				}
				c.OpenAICore.streamingUnsupported = true
			}
			return c.chatWithToolChoiceNonStreaming(ctx, params, enableThinking, thinkingChan)
		}
		return nil, fmt.Errorf("Responses API streaming error: %w", stream.Err())
	}

	// After streaming is complete, we need to get the final response to check for tool calls
	// The streaming API provides deltas, but we need the complete response for tool calls
	// Let me use a non-streaming call to get the complete response
	resp, err := c.client.Responses.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("failed to get complete response: %w", err)
	}

	// Capture token usage if provided
	if resp.Usage.JSON.InputTokens.Valid() || resp.Usage.JSON.OutputTokens.Valid() || resp.Usage.JSON.TotalTokens.Valid() {
		c.lastUsage = message.TokenUsage{
			InputTokens:  int(resp.Usage.InputTokens),
			OutputTokens: int(resp.Usage.OutputTokens),
			TotalTokens:  int(resp.Usage.TotalTokens),
			CachedTokens: int(resp.Usage.InputTokensDetails.CachedTokens),
		}
	}

	// Check for different types of output items using the variant system
	var reasoningContent string
	var toolCalls []*message.ToolCallMessage

	for _, outputItem := range resp.Output {
		if os.Getenv("DEBUG_TOOLS") == "1" {
			fmt.Printf("DEBUG: Processing output item type: %s\n", outputItem.Type)
		}

		switch variant := outputItem.AsAny().(type) {
		case responses.ResponseOutputMessage:
			if os.Getenv("DEBUG_TOOLS") == "1" {
				fmt.Printf("DEBUG: ResponseOutputMessage - Role: %s, Content: %d items\n",
					variant.Role, len(variant.Content))
			}
			// Regular assistant message - continue processing other items

		case responses.ResponseFileSearchToolCall:
			if os.Getenv("DEBUG_TOOLS") == "1" {
				fmt.Printf("DEBUG: ResponseFileSearchToolCall - ID: %s, Queries: %d, Results: %d\n",
					variant.ID, len(variant.Queries), len(variant.Results))
			}
			// File search tool call - could implement if needed

		case responses.ResponseFunctionToolCall:
			if os.Getenv("DEBUG_TOOLS") == "1" {
				fmt.Printf("DEBUG: ResponseFunctionToolCall - Name: %s, Args: %s, CallID: %s\n",
					variant.Name, variant.Arguments, variant.CallID)
			}
			// Collect all function calls
			if variant.Name != "" {
				toolArgs := convertOpenAIArgsToToolArgs(variant.Arguments)
				toolCalls = append(toolCalls, message.NewToolCallMessageWithID(
					variant.CallID,
					message.ToolName(variant.Name),
					toolArgs,
					time.Now(),
				))
			}

		case responses.ResponseFunctionWebSearch:
			if os.Getenv("DEBUG_TOOLS") == "1" {
				fmt.Printf("DEBUG: ResponseFunctionWebSearch - ID: %s, Status: %s\n",
					variant.ID, variant.Status)
			}
			// Web search tool call - could implement if needed

		case responses.ResponseComputerToolCall:
			if os.Getenv("DEBUG_TOOLS") == "1" {
				fmt.Printf("DEBUG: ResponseComputerToolCall - ID: %s, Status: %s\n",
					variant.ID, variant.Status)
			}
			// Computer use tool call - could implement if needed

		case responses.ResponseReasoningItem:
			if os.Getenv("DEBUG_TOOLS") == "1" {
				fmt.Printf("DEBUG: ResponseReasoningItem - ID: %s, Summary items: %d, Content items: %d, Status: %s\n",
					variant.ID, len(variant.Summary), len(variant.Content), variant.Status)
			}

			// Extract reasoning content for message creation
			if len(variant.Content) > 0 {
				var reasoningParts []string
				for _, content := range variant.Content {
					if content.Text != "" {
						reasoningParts = append(reasoningParts, content.Text)
					}
				}
				if len(reasoningParts) > 0 {
					reasoningContent = strings.Join(reasoningParts, "\n")
				}

				// Display reasoning content if available
				fmt.Printf("🧠 Reasoning:\n")
				for _, content := range variant.Content {
					if content.Text != "" {
						// Display reasoning text with proper formatting
						fmt.Printf("   %s\n", strings.ReplaceAll(content.Text, "\n", "\n   "))
					}
				}
				fmt.Printf("\n")
			}

			// Display reasoning summary if available
			if len(variant.Summary) > 0 {
				fmt.Printf("💭 Reasoning Summary:\n")
				for _, summary := range variant.Summary {
					if summary.Text != "" {
						fmt.Printf("   %s\n", strings.ReplaceAll(summary.Text, "\n", "\n   "))
					}
				}
				fmt.Printf("\n")
			}

			// Continue processing other items

		case responses.ResponseOutputItemImageGenerationCall:
			if os.Getenv("DEBUG_TOOLS") == "1" {
				fmt.Printf("DEBUG: ResponseOutputItemImageGenerationCall - ID: %s, Status: %s\n",
					variant.ID, variant.Status)
			}
			// Image generation tool call - could implement if needed

		case responses.ResponseCodeInterpreterToolCall:
			if os.Getenv("DEBUG_TOOLS") == "1" {
				fmt.Printf("DEBUG: ResponseCodeInterpreterToolCall - ID: %s, Status: %s, Code length: %d\n",
					variant.ID, variant.Status, len(variant.Code))
			}
			// Code interpreter tool call - could implement if needed

		case responses.ResponseOutputItemLocalShellCall:
			if os.Getenv("DEBUG_TOOLS") == "1" {
				fmt.Printf("DEBUG: ResponseOutputItemLocalShellCall - ID: %s, Status: %s\n",
					variant.ID, variant.Status)
			}
			// Local shell tool call - could implement if needed

		case responses.ResponseOutputItemMcpCall:
			if os.Getenv("DEBUG_TOOLS") == "1" {
				fmt.Printf("DEBUG: ResponseOutputItemMcpCall - ID: %s, ServerLabel: %s\n",
					variant.ID, variant.ServerLabel)
			}
			// MCP tool call - could implement if needed

		case responses.ResponseOutputItemMcpListTools:
			if os.Getenv("DEBUG_TOOLS") == "1" {
				fmt.Printf("DEBUG: ResponseOutputItemMcpListTools - ID: %s, Tools: %d\n",
					variant.ID, len(variant.Tools))
			}
			// MCP list tools call - could implement if needed

		case responses.ResponseOutputItemMcpApprovalRequest:
			if os.Getenv("DEBUG_TOOLS") == "1" {
				fmt.Printf("DEBUG: ResponseOutputItemMcpApprovalRequest - ID: %s\n",
					variant.ID)
			}
			// MCP approval request - could implement if needed

		case responses.ResponseCustomToolCall:
			if os.Getenv("DEBUG_TOOLS") == "1" {
				fmt.Printf("DEBUG: ResponseCustomToolCall - ID: %s, Name: %s\n",
					variant.ID, variant.Name)
			}
			// Custom tool call - could implement if needed

		default:
			if os.Getenv("DEBUG_TOOLS") == "1" {
				fmt.Printf("DEBUG: Unknown output item variant: %T\n", variant)
			}
		}
	}

	// Decide what to return based on what we found
	// If we found tool calls, return batch when multiple; single otherwise
	if len(toolCalls) == 1 {
		return toolCalls[0], nil
	} else if len(toolCalls) > 1 {
		return message.NewToolCallBatch(toolCalls), nil
	}

	// No tool calls found, return text response
	finalText := completeText
	if finalText == "" {
		finalText = responseBuilder.String()
	}

	// If we still don't have text, use the response's output text
	if finalText == "" {
		finalText = resp.OutputText()
	}

	if finalText == "" {
		// Debug: Check what's in the response when we have no text
		if os.Getenv("DEBUG_TOOLS") == "1" {
			fmt.Printf("DEBUG: No final text - Response ID: %s, Output items: %d\n", resp.ID, len(resp.Output))
			fmt.Printf("DEBUG: Complete text: '%s', Builder text: '%s'\n", completeText, responseBuilder.String())
			for i, item := range resp.Output {
				fmt.Printf("DEBUG: Output[%d] Type: %s\n", i, item.Type)
			}
		}
		return nil, fmt.Errorf("empty response from Responses API")
	}

	// Debug: Show what we're using as final text when it looks suspicious
	if os.Getenv("DEBUG_TOOLS") == "1" && (strings.Contains(finalText, `{"path"`) || len(finalText) < 50) {
		fmt.Printf("DEBUG: Suspicious final text: '%s'\n", finalText)
		fmt.Printf("DEBUG: OutputText(): '%s'\n", resp.OutputText())
		fmt.Printf("DEBUG: Complete text: '%s'\n", completeText)
		fmt.Printf("DEBUG: Builder text: '%s'\n", responseBuilder.String())
		fmt.Printf("DEBUG: Reasoning content: '%s'\n", reasoningContent)
	}

	// Create response message with thinking content if available
	var responseMessage message.Message

	// Use streaming reasoning content if available, otherwise use non-streaming reasoning content
	finalReasoningContent := reasoningBuilder.String()
	if finalReasoningContent == "" {
		finalReasoningContent = reasoningContent
	}

	if finalReasoningContent != "" {
		if os.Getenv("DEBUG_TOOLS") == "1" {
			fmt.Printf("DEBUG: Tool calling - Creating message with reasoning content: '%s'\n", finalReasoningContent)
		}
		responseMessage = message.NewChatMessageWithThinking(message.MessageTypeAssistant, finalText, finalReasoningContent)
	} else {
		responseMessage = message.NewChatMessage(message.MessageTypeAssistant, finalText)
		if os.Getenv("DEBUG_TOOLS") == "1" {
			fmt.Printf("DEBUG: Tool calling - Creating message WITHOUT thinking content\n")
		}
	}

	return responseMessage, nil
}

// ResponseToolCall represents a tool call from the Responses API
// TODO: Define proper structure based on Responses API tool call format
type ResponseToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// SupportsVision implements VisionLLM interface
func (c *OpenAIClient) SupportsVision() bool {
	// GPT-4V models support vision
	return strings.Contains(c.model, "gpt-4") && (strings.Contains(c.model, "vision") || strings.Contains(c.model, "gpt-4o"))
}

// isStreamingUnsupportedError checks whether the error indicates that streaming is not allowed
// for the current account/organization (e.g., org not verified to stream this model).
func isStreamingUnsupportedError(err error) bool {
	if err == nil {
		return false
	}
	e := strings.ToLower(err.Error())
	if strings.Contains(e, "must be verified to stream") {
		return true
	}
	// Heuristic for OpenAI 400 error with param "stream" and code "unsupported_value"
	if strings.Contains(e, "\"param\": \"stream\"") && strings.Contains(e, "unsupported_value") {
		return true
	}
	// Generic hint when streaming parameter is rejected
	if strings.Contains(e, "streaming error") && strings.Contains(e, "400") {
		return true
	}
	return false
}

// chatWithToolChoiceNonStreaming mirrors the parsing logic used after streaming completes,
// but performs a single non-streaming request. Used when streaming is disabled or unsupported.
func (c *OpenAIClient) chatWithToolChoiceNonStreaming(ctx context.Context, params responses.ResponseNewParams, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	resp, err := c.client.Responses.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("failed to get complete response (non-streaming tool mode): %w", err)
	}

	// Capture token usage if provided
	if resp.Usage.JSON.InputTokens.Valid() || resp.Usage.JSON.OutputTokens.Valid() || resp.Usage.JSON.TotalTokens.Valid() {
		c.lastUsage = message.TokenUsage{
			InputTokens:  int(resp.Usage.InputTokens),
			OutputTokens: int(resp.Usage.OutputTokens),
			TotalTokens:  int(resp.Usage.TotalTokens),
			CachedTokens: int(resp.Usage.InputTokensDetails.CachedTokens),
		}
	}

	// Check for different types of output items using the variant system
	var reasoningContent string
	var toolCalls []*message.ToolCallMessage

	for _, outputItem := range resp.Output {
		switch variant := outputItem.AsAny().(type) {
		case responses.ResponseFunctionToolCall:
			if variant.Name != "" {
				toolArgs := convertOpenAIArgsToToolArgs(variant.Arguments)
				toolCalls = append(toolCalls, message.NewToolCallMessageWithID(
					variant.CallID,
					message.ToolName(variant.Name),
					toolArgs,
					time.Now(),
				))
			}
		case responses.ResponseReasoningItem:
			if len(variant.Content) > 0 {
				var reasoningParts []string
				for _, content := range variant.Content {
					if content.Text != "" {
						reasoningParts = append(reasoningParts, content.Text)
					}
				}
				reasoningContent = strings.Join(reasoningParts, "\n")
			}
		}
	}

	// If we found tool calls, return them
	if len(toolCalls) == 1 {
		return toolCalls[0], nil
	} else if len(toolCalls) > 1 {
		return message.NewToolCallBatch(toolCalls), nil
	}

	// Otherwise return text answer
	finalText := resp.OutputText()
	if finalText == "" {
		return nil, fmt.Errorf("empty response from Responses API (non-streaming tool mode)")
	}

	if reasoningContent != "" {
		return message.NewChatMessageWithThinking(message.MessageTypeAssistant, finalText, reasoningContent), nil
	}
	return message.NewChatMessage(message.MessageTypeAssistant, finalText), nil
}
