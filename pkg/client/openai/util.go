package openai

import (
	"github.com/openai/openai-go/v2/shared"
)

// Model constants
const (
	modelGPT5      = "gpt-5" // TODO: Should use the official OpenAI model names
	modelGPT5Mini  = "gpt-5-mini"
	modelGPT5Nano  = "gpt-5-nano"
	modelGPT4o     = shared.ChatModelGPT4o
	modelGPT4oMini = shared.ChatModelGPT4oMini
)

// getOpenAIModel maps user-friendly model names to actual OpenAI model identifiers
func getOpenAIModel(model string) string {
	// Normalize the model name
	switch model {
	case modelGPT5:
		return modelGPT5
	case modelGPT5Mini:
		return modelGPT5Mini
	case modelGPT5Nano:
		return modelGPT5Nano
	case modelGPT4o:
		return modelGPT4o
	case modelGPT4oMini:
		return modelGPT4oMini

	default:
		// If it's already a valid OpenAI model name, return as-is
		if isValidOpenAIModel(model) {
			return model
		}
		// Default to GPT-5 Mini for unknown models (most versatile option)
		return modelGPT5Mini
	}
}

// isValidOpenAIModel checks if a model name is a valid OpenAI model
func isValidOpenAIModel(model string) bool {
	validModels := map[string]bool{
		"gpt-5":       true,
		"gpt-5-mini":  true,
		"gpt-5-nano":  true,
		"gpt-4o":      true,
		"gpt-4o-mini": true,
	}
	return validModels[model]
}

// getModelCapabilities returns capabilities for a given OpenAI model
type ModelCapabilities struct {
	SupportsVision      bool
	SupportsToolCalling bool
	SupportsStructured  bool
	SupportsThinking    bool // Reasoning models support enhanced thinking with ReasoningEffort parameters
	// MaxTokens configures default max output tokens (per-generation limit)
	MaxTokens int
	// MaxContextWindow is the model's approximate input context window size
	// (prompt capacity). Used for utilization reporting.
	MaxContextWindow     int
	SupportsSystemPrompt bool
}

var modelCapabilities = map[string]ModelCapabilities{
	modelGPT5: {
		SupportsVision:       true,
		SupportsToolCalling:  true,
		SupportsStructured:   true,
		SupportsThinking:     true, // GPT-5 supports reasoning
		MaxTokens:            16384,
		MaxContextWindow:     128000,
		SupportsSystemPrompt: true,
	},
	modelGPT5Mini: {
		SupportsVision:       true,
		SupportsToolCalling:  true,
		SupportsStructured:   true,
		SupportsThinking:     true, // GPT-5-mini supports reasoning
		MaxTokens:            16384,
		MaxContextWindow:     128000,
		SupportsSystemPrompt: true,
	},
	modelGPT5Nano: {
		SupportsVision:       true,
		SupportsToolCalling:  true,
		SupportsStructured:   true,
		SupportsThinking:     true, // GPT-5-nano supports reasoning
		MaxTokens:            8192, // Nano likely has lower token limit
		MaxContextWindow:     128000,
		SupportsSystemPrompt: true,
	},
	modelGPT4o: {
		SupportsVision:       true,
		SupportsToolCalling:  true,
		SupportsStructured:   true,
		SupportsThinking:     false, // GPT-4o does NOT support reasoning_effort
		MaxTokens:            8192,
		MaxContextWindow:     128000,
		SupportsSystemPrompt: true,
	},
	modelGPT4oMini: {
		SupportsVision:       true,
		SupportsToolCalling:  true,
		SupportsStructured:   true,
		SupportsThinking:     false, // GPT-4o-mini does NOT support reasoning_effort
		MaxTokens:            4096,
		MaxContextWindow:     128000,
		SupportsSystemPrompt: true,
	},
}

// getModelCapabilities returns the capabilities of a specific OpenAI model
func getModelCapabilities(model string) ModelCapabilities {
	switch model {
	case modelGPT5:
		return modelCapabilities[modelGPT5]
	case modelGPT5Mini:
		return modelCapabilities[modelGPT5Mini]
	case modelGPT5Nano:
		return modelCapabilities[modelGPT5Nano]
	case modelGPT4o:
		return modelCapabilities[modelGPT4o]
	case modelGPT4oMini:
		return modelCapabilities[modelGPT4oMini]
	default:
		// Default to GPT-5 Mini for unknown models (most versatile option)
		return modelCapabilities[modelGPT5Mini]
	}
}
