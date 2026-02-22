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
	// Accumulate tool calls across all chunks: some models (e.g. qwen3) send tool_calls
	// in intermediate streaming chunks (done=false) rather than in the final done=true chunk.
	var accumulatedToolCalls []api.ToolCall

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

		// Collect tool calls from every chunk (intermediate or final).
		// qwen3 sends the full tool_calls list in the first streaming chunk (done=false);
		// other models (gpt-oss) send them in the final done=true chunk.
		if len(resp.Message.ToolCalls) > 0 {
			accumulatedToolCalls = append(accumulatedToolCalls, resp.Message.ToolCalls...)
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
			// Use all accumulated tool calls (works for both qwen3-style intermediate
			// chunks and gpt-oss-style final-chunk tool calls)
			if len(accumulatedToolCalls) > 0 {
				result.ToolCalls = accumulatedToolCalls
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

	return &OllamaClient{OllamaCore: core}, nil
}

// NewOllamaClientFromCore creates a new Ollama client from shared core
func NewOllamaClientFromCore(core *OllamaCore) domain.ToolCallingLLM {
	return &OllamaClient{OllamaCore: core}
}

// IsToolCapable checks if the current model supports native tool calling
func (c *OllamaClient) IsToolCapable() bool {
	return IsToolCapableModel(c.model)
}

// SetToolManager sets the tool manager for native tool calling
func (c *OllamaClient) SetToolManager(toolManager domain.ToolManager) {
	c.toolManager = toolManager
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
			switch toolChoice.Type {
			case domain.ToolChoiceNone:
				// Don't add any tools
			default:
				chatRequest.Tools = tools
			}
		}
	}

	// Apply thinking parameter for models that declare thinking capability.
	// Use c.thinking (from settings JSON) rather than the enableThinking caller arg,
	// because the ReAct agent always passes enableThinking=true regardless of settings.
	if IsThinkingCapableModel(c.model) {
		chatRequest.Think = &api.ThinkValue{Value: c.thinking}
	}

	includeThinking := c.thinking && IsThinkingCapableModel(c.model)
	result, err := c.chat(ctx, chatRequest, thinkingChan)
	if err != nil {
		return nil, fmt.Errorf("ollama chat error: %w", err)
	}

	return toDomainMessageFromOllama(result, includeThinking), nil
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
