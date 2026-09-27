package openai

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

const (
	defaultReasoningEffort = shared.ReasoningEffortLow // Default reasoning effort for OpenAI models

	// Minimum token drop between consecutive calls to be considered truncation.
	// Small decreases can happen from our own compaction; this threshold filters noise.
	truncationDetectionThreshold = 1000
)

// OpenAICore holds shared resources for OpenAI clients
type OpenAICore struct {
	client    *openai.Client
	model     string
	maxTokens int
	// reasoningEffort controls the reasoning depth for reasoning-capable models
	// (GPT-5 family). Defaults to defaultReasoningEffort when unset.
	reasoningEffort shared.ReasoningEffort
	// streamingUnsupported is set to true when the API rejects streaming
	// (e.g., org not verified). Subsequent calls will avoid streaming.
	streamingUnsupported bool

	// Cross-call telemetry/state shared by all wrappers built from this core,
	// so token usage reported via the original client stays accurate even when
	// per-invocation wrappers make the actual API calls.
	lastUsage       message.TokenUsage
	prevInputTokens int // previous call's input tokens for truncation detection
}

// OpenAIClient implements ToolCallingLLM and VisionLLM interfaces
type OpenAIClient struct {
	*OpenAICore
	toolManager domain.ToolManager

	// Caching/session hints
	sessionID string
	cacheOpts domain.ModelSideCacheOptions
}

// NewOpenAIClient creates a new OpenAI client with configurable maxTokens and
// reasoning effort. maxTokens = 0 means default; effort = "" means
// defaultReasoningEffort. effort is one of: none, minimal, low, medium, high, xhigh.
func NewOpenAIClient(model string, maxTokens int, effort string) (*OpenAIClient, error) {
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
		reasoningEffort:      parseReasoningEffort(effort),
		streamingUnsupported: false,
	}

	return &OpenAIClient{
		OpenAICore: core,
	}, nil
}

// parseReasoningEffort maps a settings effort string to the SDK enum, falling
// back to defaultReasoningEffort for empty or unrecognized values.
func parseReasoningEffort(effort string) shared.ReasoningEffort {
	switch effort {
	case "none":
		return shared.ReasoningEffortNone
	case "minimal":
		return shared.ReasoningEffortMinimal
	case "low":
		return shared.ReasoningEffortLow
	case "medium":
		return shared.ReasoningEffortMedium
	case "high":
		return shared.ReasoningEffortHigh
	case "xhigh":
		return shared.ReasoningEffortXhigh
	default:
		return defaultReasoningEffort
	}
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

// SupportsServerSideCompaction implements ServerSideCompactionLLM.
// OpenAI Responses API uses truncation: "auto" to handle context overflow.
func (c *OpenAIClient) SupportsServerSideCompaction() bool { return true }

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
		Model:      shared.ChatModel(c.model),
		Truncation: responses.ResponseNewParamsTruncationAuto,
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
			Effort: c.reasoningEffort,
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
		c.detectTruncation(int(resp.Usage.InputTokens))
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
		Model:      shared.ChatModel(c.model),
		Truncation: responses.ResponseNewParamsTruncationAuto,
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
			Effort: c.reasoningEffort,
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
				c.detectTruncation(int(resp.Usage.InputTokens))
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
			if images := msg.Images(); len(images) > 0 {
				contentList := buildImageContentList(images, msg.Content())
				inputItem := responses.ResponseInputItemParamOfInputMessage(contentList, "user")
				inputItems = append(inputItems, inputItem)
			} else {
				inputItem := responses.ResponseInputItemParamOfMessage(msg.Content(), responses.EasyInputMessageRoleUser)
				inputItems = append(inputItems, inputItem)
			}

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
				// If the tool result contains images, inject a user message with the image
				// (FunctionCallOutput is text-only, so images must go in a separate message)
				if images := msg.Images(); len(images) > 0 {
					contentList := buildImageContentList(images, "")
					imageItem := responses.ResponseInputItemParamOfInputMessage(contentList, "user")
					inputItems = append(inputItems, imageItem)
				}
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

// buildImageContentList creates a multi-part content list with base64 images and optional text.
func buildImageContentList(images []string, text string) responses.ResponseInputMessageContentListParam {
	var parts responses.ResponseInputMessageContentListParam
	for _, img := range images {
		dataURL := "data:image/jpeg;base64," + img
		// Detect PNG by base64 magic bytes (iVBORw0KGgo = PNG header)
		if strings.HasPrefix(img, "iVBORw0KGgo") {
			dataURL = "data:image/png;base64," + img
		}
		parts = append(parts, responses.ResponseInputContentUnionParam{
			OfInputImage: &responses.ResponseInputImageParam{
				Detail:   responses.ResponseInputImageDetailAuto,
				ImageURL: param.NewOpt(dataURL),
			},
		})
	}
	if text != "" {
		parts = append(parts, responses.ResponseInputContentParamOfInputText(text))
	}
	return parts
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
		Model:      shared.ChatModel(c.model),
		Truncation: responses.ResponseNewParamsTruncationAuto,
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
			Effort: c.reasoningEffort,
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
	var final *responses.Response

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

		// The terminal event carries the whole response: every output item,
		// the tool calls, and usage. It is all this turn needs.
		switch event.Type {
		case "response.completed":
			r := event.AsResponseCompleted().Response
			final = &r
		case "response.incomplete":
			// Stopped early (max_output_tokens, content filter): still an answer,
			// and the one a follow-up request would have regenerated anyway.
			r := event.AsResponseIncomplete().Response
			final = &r
		case "response.failed":
			return nil, fmt.Errorf("responses API response failed: %s", event.AsResponseFailed().Response.Error.Message)
		}
		if final != nil {
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

	// No second request: the completed event already holds what one would
	// return. Re-requesting generated every turn twice — billed twice on
	// OpenAI, and on a server that only answers SSE (gallium's responses-api)
	// the non-streaming decode failed outright. The two answers could even
	// disagree: the text shown was streamed from the first, the tool calls
	// came from the second.
	if final == nil {
		return nil, errors.New("responses API stream ended without a completed response")
	}
	return c.messageFromResponse(final, responseBuilder.String(), reasoningBuilder.String())
}

// ResponseToolCall represents a tool call from the Responses API
// TODO: Define proper structure based on Responses API tool call format
type ResponseToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// SupportsVision implements VisionLLM interface. Every adopted model is
// vision-capable, so this follows the model's capability profile.
func (c *OpenAIClient) SupportsVision() bool {
	return getModelCapabilities(c.model).SupportsVision
}

// detectTruncation checks if the API silently truncated input and logs a warning.
// When truncation: "auto" is active, the API drops items from the beginning.
// We detect this by comparing: if input_tokens decreased while we expected growth,
// the API must have dropped older items.
func (c *OpenAIClient) detectTruncation(inputTokens int) {
	prev := c.prevInputTokens
	c.prevInputTokens = inputTokens

	// Skip first call (no baseline) and calls with missing data
	if prev == 0 || inputTokens == 0 {
		return
	}

	// If input tokens dropped significantly from last call, truncation likely occurred.
	// A small drop could be normal (compaction on our side), so use a threshold.
	if inputTokens < prev-truncationDetectionThreshold {
		fmt.Fprintf(os.Stderr, "OpenAI: context truncation detected (input tokens: %d → %d, dropped ~%d tokens from beginning)\n",
			prev, inputTokens, prev-inputTokens)
	}
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

// chatWithToolChoiceNonStreaming performs a single non-streaming request. Used
// when streaming is disabled or unsupported.
func (c *OpenAIClient) chatWithToolChoiceNonStreaming(
	ctx context.Context, params responses.ResponseNewParams, _ bool, _ chan<- string,
) (message.Message, error) {
	resp, err := c.client.Responses.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("failed to get complete response (non-streaming tool mode): %w", err)
	}
	return c.messageFromResponse(resp, "", "")
}

// messageFromResponse turns a finished response into the turn's message: its
// tool calls when there are any (a batch for several), its text otherwise.
// streamedText and streamedReasoning are what a streaming call already
// accumulated from deltas, preferred over re-reading the output items; empty
// for a non-streaming call.
func (c *OpenAIClient) messageFromResponse(
	resp *responses.Response, streamedText, streamedReasoning string,
) (message.Message, error) {
	c.recordUsage(resp)

	toolCalls, itemReasoning := collectOutputItems(resp)
	switch len(toolCalls) {
	case 0:
	case 1:
		return toolCalls[0], nil
	default:
		return message.NewToolCallBatch(toolCalls), nil
	}

	text := streamedText
	if text == "" {
		text = resp.OutputText()
	}
	if text == "" {
		return nil, fmt.Errorf("empty response from Responses API (status %q, %d output items)",
			resp.Status, len(resp.Output))
	}
	reasoning := streamedReasoning
	if reasoning == "" {
		reasoning = itemReasoning
	}
	if reasoning != "" {
		return message.NewChatMessageWithThinking(message.MessageTypeAssistant, text, reasoning), nil
	}
	return message.NewChatMessage(message.MessageTypeAssistant, text), nil
}

// collectOutputItems gathers a response's function calls and the text of its
// reasoning items. Other item kinds (hosted tools klein never offers) are
// ignored.
func collectOutputItems(resp *responses.Response) ([]*message.ToolCallMessage, string) {
	var toolCalls []*message.ToolCallMessage
	var reasoningParts []string
	for _, item := range resp.Output {
		switch v := item.AsAny().(type) {
		case responses.ResponseFunctionToolCall:
			if v.Name != "" {
				toolCalls = append(toolCalls, message.NewToolCallMessageWithID(
					v.CallID, message.ToolName(v.Name), convertOpenAIArgsToToolArgs(v.Arguments), time.Now(),
				))
			}
		case responses.ResponseReasoningItem:
			for _, content := range v.Content {
				if content.Text != "" {
					reasoningParts = append(reasoningParts, content.Text)
				}
			}
		default:
			if os.Getenv("DEBUG_TOOLS") == "1" {
				fmt.Printf("DEBUG: Ignoring output item type: %s\n", item.Type)
			}
		}
	}
	return toolCalls, strings.Join(reasoningParts, "\n")
}

// recordUsage keeps the response's token usage, when it reported any.
func (c *OpenAIClient) recordUsage(resp *responses.Response) {
	u := resp.Usage.JSON
	if !u.InputTokens.Valid() && !u.OutputTokens.Valid() && !u.TotalTokens.Valid() {
		return
	}
	c.lastUsage = message.TokenUsage{
		InputTokens:  int(resp.Usage.InputTokens),
		OutputTokens: int(resp.Usage.OutputTokens),
		TotalTokens:  int(resp.Usage.TotalTokens),
		CachedTokens: int(resp.Usage.InputTokensDetails.CachedTokens),
	}
	c.detectTruncation(int(resp.Usage.InputTokens))
}
