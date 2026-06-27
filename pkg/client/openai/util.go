package openai

import (
	"strings"

	"github.com/openai/openai-go/v3/shared"
)

// defaultModel is used when an unknown model name is supplied.
const defaultModel = shared.ChatModelGPT5_4Mini

// getOpenAIModel maps user-supplied model names to actual OpenAI model identifiers.
// Unknown names fall back to the default.
func getOpenAIModel(model string) string {
	if isValidOpenAIModel(model) {
		return model
	}
	return defaultModel
}

// isValidOpenAIModel checks if model belongs to the GPT-5 family using prefix
// matching, so every gpt-5.x release (gpt-5, gpt-5.4, gpt-5.5, gpt-5.6, …),
// their -mini/-nano variants, and dated variants (e.g.
// "gpt-5.5-mini-2026-03-17") are accepted automatically without a code change.
// The whole family shares one capability profile (reasoning models), so this
// is scoped to gpt-5 rather than all gpt- models.
func isValidOpenAIModel(model string) bool {
	return strings.HasPrefix(model, "gpt-5")
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

// getModelCapabilities returns the capability profile for a model. Any GPT-5
// "nano" variant (gpt-5.4-nano, gpt-5.5-nano, …) gets the smaller-output
// profile; everything else uses the standard GPT-5 profile.
func getModelCapabilities(model string) ModelCapabilities {
	switch {
	case strings.Contains(model, "nano"):
		return capGPT5Nano
	default:
		return capGPT5
	}
}
