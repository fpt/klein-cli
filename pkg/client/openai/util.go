package openai

import (
	"slices"
	"strings"

	"github.com/openai/openai-go/v3/shared"
)

// The only OpenAI models klein adopts (the gpt-6 line).
const (
	ModelAstra = shared.ChatModelGPT6Astra
	ModelLuna  = shared.ChatModelGPT6Luna
	ModelSol   = shared.ChatModelGPT6Sol
	ModelSol61 = shared.ChatModelGPT6_1Sol
)

// SupportedModels lists every OpenAI model klein accepts.
var SupportedModels = []string{ModelAstra, ModelLuna, ModelSol, ModelSol61}

// defaultModel is used when an unknown/unsupported model name is supplied.
const defaultModel = ModelLuna

// getOpenAIModel maps a user-supplied model name to the model actually used.
// Unsupported names fall back to the default.
func getOpenAIModel(model string) string {
	if isValidOpenAIModel(model) {
		return model
	}
	return defaultModel
}

// isValidOpenAIModel reports whether model is one klein adopts: one of
// SupportedModels, or a dated variant of one (e.g. "gpt-6-luna-2026-09-01").
// Anything else — including older gpt-5.x families — is rejected and falls
// back to the default.
func isValidOpenAIModel(model string) bool {
	if slices.Contains(SupportedModels, model) {
		return true
	}
	return slices.ContainsFunc(SupportedModels, func(m string) bool {
		return strings.HasPrefix(model, m+"-")
	})
}

// modelsWithoutNoneEffort reject reasoning effort "none" (probed against the
// API). No adopted model accepts "minimal"; every one accepts low through max.
var modelsWithoutNoneEffort = []string{ModelAstra, ModelSol61}

// supportedEffort clamps effort to a level model accepts, moving up rather
// than failing the request: minimal becomes none, and none becomes low where
// the model has no none level.
func supportedEffort(model string, effort shared.ReasoningEffort) shared.ReasoningEffort {
	if effort == shared.ReasoningEffortMinimal {
		effort = shared.ReasoningEffortNone
	}
	lacksNone := slices.ContainsFunc(modelsWithoutNoneEffort, func(m string) bool {
		return model == m || strings.HasPrefix(model, m+"-")
	})
	if effort == shared.ReasoningEffortNone && lacksNone {
		effort = shared.ReasoningEffortLow
	}
	return effort
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

// capGPT6 is the capability profile shared by the adopted gpt-6 models.
var capGPT6 = ModelCapabilities{
	SupportsVision:       true,
	SupportsToolCalling:  true,
	SupportsStructured:   true,
	SupportsThinking:     true,
	MaxTokens:            32768,
	MaxContextWindow:     128000,
	SupportsSystemPrompt: true,
}

// getModelCapabilities returns the capability profile for a model. All adopted
// models share one profile (they are the same generation); an unsupported name
// has already been mapped to the default by getOpenAIModel.
func getModelCapabilities(_ string) ModelCapabilities {
	return capGPT6
}
