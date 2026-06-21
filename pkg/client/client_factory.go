package client

import (
	"fmt"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/client/anthropic"
	"github.com/fpt/klein-cli/pkg/client/gemini"
	"github.com/fpt/klein-cli/pkg/client/ollama"
	"github.com/fpt/klein-cli/pkg/client/openai"
)

// NewLLMClient creates an LLM client based on settings.
func NewLLMClient(settings config.LLMSettings) (domain.LLM, error) {
	switch settings.Backend {
	case "anthropic", "claude":
		return anthropic.NewAnthropicClientWithTokens(settings.Model, settings.MaxTokens)
	case "openai":
		return openai.NewOpenAIClient(settings.Model, settings.MaxTokens, settings.Effort)
	case "gemini":
		return gemini.NewGeminiClientWithTokens(settings.Model, settings.MaxTokens)
	default:
		return ollama.NewOllamaClient(settings.Model, settings.MaxTokens, settings.Thinking)
	}
}

// NewClientWithToolManager creates a tool calling client appropriate for the
// underlying LLM client. It ALWAYS returns a fresh wrapper built from the shared
// core (rather than mutating the passed-in client) so that distinct agents
// (e.g. a parent and a spawned sub-agent) get independent tool managers and do
// not clobber each other mid-run. Cross-call telemetry (token usage) lives on
// the shared core, so the original client still reports accurate usage.
func NewClientWithToolManager(client domain.LLM, toolManager domain.ToolManager) (domain.ToolCallingLLM, error) {
	// Build a fresh wrapper from the shared core based on the concrete type.
	switch c := client.(type) {
	case *ollama.OllamaClient:
		// Ollama automatically chooses native tool calling vs schema-based on
		// model capabilities.
		toolClient := ollama.NewOllamaClientFromCore(c.OllamaCore)
		toolClient.SetToolManager(toolManager)
		return toolClient, nil
	case *anthropic.AnthropicClient:
		toolClient := anthropic.NewAnthropicClientFromCore(c.AnthropicCore)
		toolClient.SetToolManager(toolManager)
		return toolClient, nil
	case *openai.OpenAIClient:
		toolClient := openai.NewOpenAIClientFromCore(c.OpenAICore)
		toolClient.SetToolManager(toolManager)
		return toolClient, nil
	case *gemini.GeminiClient:
		toolClient := gemini.NewGeminiClientFromCore(c.GeminiCore)
		toolClient.SetToolManager(toolManager)
		return toolClient, nil
	}

	// Fallback: an unknown client that already supports tool calling. We cannot
	// clone it, so mutate in place (legacy behavior).
	if toolCallingClient, ok := client.(domain.ToolCallingLLM); ok {
		toolCallingClient.SetToolManager(toolManager)
		return toolCallingClient, nil
	}

	return nil, fmt.Errorf("unsupported client type for tool calling: %T", client)
}

// NewStructuredClient creates a structured client for the given type T and base LLM client
// Uses the most appropriate structured output method for each provider
func NewStructuredClient[T any](client domain.LLM) (domain.StructuredLLM[T], error) {
	// Check if the client already supports structured output
	if structuredClient, ok := client.(domain.StructuredLLM[T]); ok {
		return structuredClient, nil
	}

	// Determine the appropriate structured client based on the client type
	switch c := client.(type) {
	case *ollama.OllamaClient:
		if ollama.IsToolCapableModel(c.Model()) {
			return NewToolCallingStructuredClient[T](c), nil
		}
		return nil, fmt.Errorf("model %s does not support structured output", c.Model())
	case *anthropic.AnthropicClient:
		// For Anthropic, use the generic tool calling-based structured client
		return NewToolCallingStructuredClient[T](c), nil
	case *openai.OpenAIClient:
		// For OpenAI, use the generic tool calling-based structured client
		return NewToolCallingStructuredClient[T](c), nil
	case *gemini.GeminiClient:
		// For Gemini, use native structured output with ResponseMIMEType and ResponseSchema
		return gemini.NewGeminiStructuredClient[T](c.GeminiCore), nil
	default:
		// For unknown clients, we cannot create a structured client
		return nil, fmt.Errorf("unsupported client type for structured output: %T", client)
	}
}
