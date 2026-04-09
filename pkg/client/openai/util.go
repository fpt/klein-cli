package openai

import (
	"strings"

	"github.com/openai/openai-go/v3/shared"
)

// defaultModel is used when an unknown model name is supplied.
const defaultModel = shared.ChatModelGPT5Mini

// getOpenAIModel maps user-supplied model names to actual OpenAI model identifiers.
// Unknown names fall back to the default.
func getOpenAIModel(model string) string {
	if isValidOpenAIModel(model) {
		return model
	}
	return defaultModel
}

// isValidOpenAIModel checks if model belongs to a supported family using prefix matching,
// so dated variants (e.g. "gpt-5.4-mini-2026-03-17") are accepted automatically.
func isValidOpenAIModel(model string) bool {
	for _, p := range []string{"gpt-5.4", "gpt-5.3", "gpt-5.2", "gpt-5.1", "gpt-5"} {
		if strings.HasPrefix(model, p) {
			return true
		}
	}
	return false
}

// ModelCapabilities describes the feature set of an OpenAI model.
type ModelCapabilities struct {
	SupportsVision      bool
	SupportsToolCalling bool
	SupportsStructured  bool
	// SupportsThinking indicates reasoning_effort is supported.
	SupportsThinking bool
	// MaxTokens is the default max output tokens for a single generation.
	MaxTokens int
	// MaxContextWindow is the approximate prompt-capacity context window.
	MaxContextWindow     int
	SupportsSystemPrompt bool
}

// capGPT5 is the capability profile for all GPT-5.x variants.
var capGPT5 = ModelCapabilities{
	SupportsVision:       true,
	SupportsToolCalling:  true,
	SupportsStructured:   true,
	SupportsThinking:     true,
	MaxTokens:            32768,
	MaxContextWindow:     128000,
	SupportsSystemPrompt: true,
}

// capGPT5Nano has slightly lower output limits.
var capGPT5Nano = ModelCapabilities{
	SupportsVision:       true,
	SupportsToolCalling:  true,
	SupportsStructured:   true,
	SupportsThinking:     true,
	MaxTokens:            16384,
	MaxContextWindow:     128000,
	SupportsSystemPrompt: true,
}

// getModelCapabilities returns the capability profile for a model.
// Prefix matching handles dated variants automatically.
func getModelCapabilities(model string) ModelCapabilities {
	switch {
	case strings.HasPrefix(model, "gpt-5.4-nano"),
		strings.HasPrefix(model, "gpt-5-nano"):
		return capGPT5Nano
	case strings.HasPrefix(model, "gpt-5"):
		return capGPT5
	default:
		return capGPT5 // default to gpt-5-mini profile
	}
}
