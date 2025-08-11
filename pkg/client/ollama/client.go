package ollama

import (
	"context"
	"fmt"
	"strings"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
	"github.com/ollama/ollama/api"
	"github.com/pkg/errors"
)

const temperature = 0.1 // Default temperature for Ollama chat requests

// OllamaCore contains shared Ollama client resources and core functionality
// This allows efficient resource sharing between different Ollama client types
type OllamaCore struct {
	client    *api.Client
	model     string
	maxTokens int
	thinking  bool // Settings-based thinking control
	// Telemetry
	lastUsage message.TokenUsage
}

// NewOllamaCore creates a new Ollama core with shared resources
func NewOllamaCore(model string) (*OllamaCore, error) {
	return NewOllamaCoreWithOptions(model, 0, true) // 0 = use default, true = enable thinking
}

// NewOllamaCoreWithTokens creates a new Ollama core with configurable maxTokens
func NewOllamaCoreWithTokens(model string, maxTokens int) (*OllamaCore, error) {
	return NewOllamaCoreWithOptions(model, maxTokens, true) // true = enable thinking
}

// NewOllamaCoreWithOptions creates a new Ollama core with configurable maxTokens and thinking
func NewOllamaCoreWithOptions(model string, maxTokens int, thinking bool) (*OllamaCore, error) {
	client, err := api.ClientFromEnvironment()
	if err != nil {
		return nil, fmt.Errorf("failed to create Ollama client: %w", err)
	}

	// Use default maxTokens if not specified
	if maxTokens <= 0 {
		maxTokens = 4096 // Default for Ollama models
	}

	return &OllamaCore{
		client:    client,
		model:     model,
		maxTokens: maxTokens,
		thinking:  thinking,
	}, nil
}

// Client returns the underlying Ollama API client
func (c *OllamaCore) Client() *api.Client {
	return c.client
}

// Model returns the model name
func (c *OllamaCore) Model() string {
	return c.model
}

func (c *OllamaCore) chat(ctx context.Context, chatRequest *api.ChatRequest, thinkingChan chan<- string) (api.Message, error) {
	var result api.Message
	var contentBuilder strings.Builder
	var thinkingBuilder strings.Builder

	err := c.client.Chat(ctx, chatRequest, func(resp api.ChatResponse) error {
		// Accumulate content and thinking from streaming responses
		if resp.Message.Content != "" {
			contentBuilder.WriteString(resp.Message.Content)
		}

		if resp.Message.Thinking != "" {
			// Send thinking content to channel if enabled
			if shouldShowThinking(c.thinking, chatRequest.Think) && thinkingChan != nil {
				message.SendThinkingContent(thinkingChan, resp.Message.Thinking)
			}

			thinkingBuilder.WriteString(resp.Message.Thinking)
		}

		if resp.Done {
			// Signal end of thinking if we accumulated thinking content
			if thinkingBuilder.Len() > 0 && thinkingChan != nil {
				message.EndThinking(thinkingChan)
			}

			// Capture token usage counts on final chunk when available
			// Ollama typically exposes prompt_eval_count (input) and eval_count (output)
			// Convert to our TokenUsage format for telemetry and logging
			// These fields may be zero if the backend doesn't supply them.
			c.lastUsage = message.TokenUsage{}
			// Use reflection-like safe access via documented fields
			// Direct struct fields are used here; if API changes, counts will stay zero.
			c.lastUsage.InputTokens = int(resp.PromptEvalCount)
			c.lastUsage.OutputTokens = int(resp.EvalCount)
			c.lastUsage.TotalTokens = c.lastUsage.InputTokens + c.lastUsage.OutputTokens

			// Combine accumulated content and thinking
			result = api.Message{
				Role:     resp.Message.Role,
				Content:  contentBuilder.String(),
				Thinking: thinkingBuilder.String(),
			}
			// Copy other fields from the final response
			if len(resp.Message.ToolCalls) > 0 {
				result.ToolCalls = resp.Message.ToolCalls
			}
		}

		return nil
	})

	return result, errors.Wrap(err, "ollama chat error")
}

// shouldShowThinking determines if thinking should be displayed based on settings and request parameters
func shouldShowThinking(settingsThinking bool, requestThink *api.ThinkValue) bool {
	if requestThink != nil {
		// Explicit parameter takes precedence (from ChatWithThinking)
		return requestThink.Bool()
	}
	// Use settings-based thinking control (from Chat)
	return settingsThinking
}

// OllamaClient implements tool calling and thinking capabilities for Ollama
// Implements domain.ToolCallingLLMWithThinking interface when both capabilities are available
type OllamaClient struct {
	*OllamaCore
	toolManager domain.ToolManager // For native tool calling
}

// NewOllamaClient creates a new Ollama client with configurable maxTokens and thinking
func NewOllamaClient(model string, maxTokens int, thinking bool) (domain.ToolCallingLLM, error) {
	core, err := NewOllamaCoreWithOptions(model, maxTokens, thinking)
	if err != nil {
		return nil, err
	}

	// Check if this model supports native tool calling
	if IsToolCapableModel(model) {
		return &OllamaClient{
			OllamaCore: core,
		}, nil
	} else {
		// For models that don't support tool calling, still create a client but with limited functionality
		// The main application will handle capability checking and user warnings
		return &OllamaClient{
			OllamaCore: core,
		}, nil
	}
}

// NewOllamaClientFromCore creates a new Ollama client from shared core
// Returns OllamaClient for tool-capable models, or nil for unsupported models
func NewOllamaClientFromCore(core *OllamaCore) domain.ToolCallingLLM {
	// Check if this model supports native tool calling
	if IsToolCapableModel(core.model) {
		return &OllamaClient{
			OllamaCore: core,
		}
	} else {
		// Return nil for models that don't support native tool calling (ollama_gbnf has been removed)
		// This will cause a panic if used, but maintains interface compatibility
		// Callers should check IsToolCapableModel before calling this function
		return nil
	}
}

// IsToolCapable checks if the current model supports native tool calling
func (c *OllamaClient) IsToolCapable() bool {
	capable := IsToolCapableModel(c.model)
	return capable
}

// SetToolManager sets the tool manager for native tool calling
func (c *OllamaClient) SetToolManager(toolManager domain.ToolManager) {
	c.toolManager = toolManager
}

// IsToolCapableModel checks if the current model supports native tool calling
// Deprecated: Use IsToolCapable() instead
func (c *OllamaClient) IsToolCapableModel() bool {
	return c.IsToolCapable()
}

// SupportsVision checks if the current model supports vision/image analysis
func (c *OllamaClient) SupportsVision() bool {
	capable := IsVisionCapableModel(c.model)
	return capable
}

// ModelIdentifier implementation
func (c *OllamaClient) ModelID() string { return c.model }

// ContextWindowProvider implementation
func (c *OllamaClient) MaxContextTokens() int {
	return GetModelContextWindow(c.model)
}

// TokenUsageProvider implementation
func (c *OllamaClient) LastTokenUsage() (message.TokenUsage, bool) {
	if c.lastUsage.InputTokens != 0 || c.lastUsage.OutputTokens != 0 || c.lastUsage.TotalTokens != 0 {
		return c.lastUsage, true
	}
	return message.TokenUsage{}, false
}

// ChatWithToolChoice sends a message to Ollama with tool choice control
func (c *OllamaClient) ChatWithToolChoice(ctx context.Context, messages []message.Message, toolChoice domain.ToolChoice, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	// Convert to Ollama format
	ollamaMessages := toOllamaMessages(messages)

	chatRequest := &api.ChatRequest{
		Model:    c.model,
		Messages: ollamaMessages,
		Options: map[string]any{
			"temperature": temperature,
			"num_predict": c.maxTokens, // Max output tokens for Ollama
		},
	}

	// Handle tool choice for tool-capable models
	if c.IsToolCapable() && c.toolManager != nil {
		tools := convertToOllamaTools(c.toolManager.GetTools())
		if len(tools) > 0 {
			// Apply tool choice logic
			switch toolChoice.Type {
			case domain.ToolChoiceNone:
				// Don't add any tools
			case domain.ToolChoiceAuto:
				// Add tools with encouraging system message
				chatRequest.Tools = tools
				addToolUsageSystemMessage(&ollamaMessages, "You are a helpful assistant. When the user asks you to perform tasks, you should use the available tools to help them. Always use the appropriate tool when one is available for the task at hand.")
			case domain.ToolChoiceAny:
				// Add tools with stronger encouragement
				chatRequest.Tools = tools
				addToolUsageSystemMessage(&ollamaMessages, "You are a helpful assistant. You MUST use at least one of the available tools to help the user with their request. Do not provide a response without using a tool.")
			case domain.ToolChoiceTool:
				chatRequest.Tools = tools
				addToolUsageSystemMessage(&ollamaMessages, fmt.Sprintf("You are a helpful assistant. You MUST use the '%s' tool to help the user with their request. Do not provide a response without using this specific tool.", toolChoice.Name))
			}
		}
	}

	result, err := c.chat(ctx, chatRequest, thinkingChan)
	if err != nil {
		return nil, fmt.Errorf("ollama chat error: %w", err)
	}

	// Centralized conversion (ChatWithToolChoice previously did not include thinking)
	return toDomainMessageFromOllama(result, false), nil
}

// chatWithOptions is a private helper that consolidates chat logic
func (c *OllamaClient) chatWithOptions(ctx context.Context, messages []message.Message, enableThinking *bool, thinkingChan chan<- string) (message.Message, error) {
	// Convert to Ollama format
	ollamaMessages := toOllamaMessages(messages)

	chatRequest := &api.ChatRequest{
		Model:    c.model,
		Messages: ollamaMessages,
		Options: map[string]any{
			"temperature": temperature,
			"num_predict": c.maxTokens, // Max output tokens for Ollama
		},
	}

	// Set thinking parameter if supported
	if IsThinkingCapableModel(c.model) {
		if enableThinking != nil {
			// Use provided thinking setting (from ChatWithThinking)
			chatRequest.Think = &api.ThinkValue{Value: *enableThinking}
		} else {
			// Use settings-based thinking control (from Chat)
			chatRequest.Think = &api.ThinkValue{Value: c.thinking}
		}
	}

	// Add tools if this is a tool-capable model and tool manager is available
	if c.IsToolCapable() && c.toolManager != nil {
		tools := convertToOllamaTools(c.toolManager.GetTools())
		if len(tools) > 0 {
			chatRequest.Tools = tools

			// Add a system message to encourage tool usage
			if len(ollamaMessages) > 0 && ollamaMessages[0].Role != "system" {
				systemMessage := api.Message{
					Role:    "system",
					Content: "You are a helpful assistant. When the user asks you to perform tasks, you should use the available tools to help them. Always use the appropriate tool when one is available for the task at hand.",
				}
				ollamaMessages = append([]api.Message{systemMessage}, ollamaMessages...)
			}
		}
	}

	result, err := c.chat(ctx, chatRequest, thinkingChan)
	if err != nil {
		return nil, fmt.Errorf("ollama chat error: %w", err)
	}

	// Decide whether to include thinking content
	includeThinking := false
	if len(result.Thinking) > 0 {
		if enableThinking != nil {
			includeThinking = *enableThinking
		} else {
			includeThinking = c.thinking
		}
	}

	return toDomainMessageFromOllama(result, includeThinking), nil
}

// Chat sends a message to Ollama with thinking control
func (c *OllamaClient) Chat(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	return c.chatWithOptions(ctx, messages, &enableThinking, thinkingChan)
}
