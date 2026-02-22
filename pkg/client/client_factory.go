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
		return openai.NewOpenAIClient(settings.Model, settings.MaxTokens)
	case "gemini":
		return gemini.NewGeminiClientWithTokens(settings.Model, settings.MaxTokens)
	default:
		return ollama.NewOllamaClient(settings.Model, settings.MaxTokens, settings.Thinking)
	}
}

// NewClientWithToolManager creates a tool calling client appropriate for the underlying LLM client
// Takes a base LLM client and adds tool management capabilities to it
func NewClientWithToolManager(client domain.LLM, toolManager domain.ToolManager) (domain.ToolCallingLLM, error) {
	// Check if the client is already a tool calling client
	if toolCallingClient, ok := client.(domain.ToolCallingLLM); ok {
		// Set the tool manager and return
		toolCallingClient.SetToolManager(toolManager)
		return toolCallingClient, nil
	}

	// Determine the appropriate tool calling client based on the client type
	switch c := client.(type) {
	case *ollama.OllamaClient:
		// For Ollama clients, use the embedded OllamaCore to create a new tool calling client
		// This will automatically choose between native tool calling or schema-based based on model capabilities
		toolClient := ollama.NewOllamaClientFromCore(c.OllamaCore)
		toolClient.SetToolManager(toolManager)
		return toolClient, nil
	case *anthropic.AnthropicClient:
		// For Anthropic clients, use the embedded AnthropicCore to create a new tool calling client
		toolClient := anthropic.NewAnthropicClientFromCore(c.AnthropicCore)
		toolClient.SetToolManager(toolManager)
		return toolClient, nil
	case *openai.OpenAIClient:
		// For OpenAI clients, use the embedded OpenAICore to create a new tool calling client
		toolClient := openai.NewOpenAIClientFromCore(c.OpenAICore)
		toolClient.SetToolManager(toolManager)
		return toolClient, nil
	case *gemini.GeminiClient:
		// For Gemini clients, use the embedded GeminiCore to create a new tool calling client
		toolClient := gemini.NewGeminiClientFromCore(c.GeminiCore)
		toolClient.SetToolManager(toolManager)
		return toolClient, nil
	default:
		// For unknown clients, we cannot create a tool calling client
		// since we need specific core implementations
		return nil, fmt.Errorf("unsupported client type for tool calling: %T", client)
	}
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
		// For Ollama clients, check if the model supports JSON Schema or native tool calling
		if ollama.IsJSONSchemaCapableModel(c.Model()) {
			// Use JSON Schema-based structured client
			return ollama.NewOllamaStructuredClient[T](c.OllamaCore), nil
		} else if ollama.IsToolCapableModel(c.Model()) {
			// Use generic tool calling-based structured client
			return NewToolCallingStructuredClient[T](c), nil
		} else {
			// Model doesn't support either JSON Schema or tool calling
			return nil, fmt.Errorf("model %s does not support structured output", c.Model())
		}
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
