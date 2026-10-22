package gemini

import "google.golang.org/genai"

// Google Gemini Models
// https://ai.google.dev/gemini-api/docs/models
//
// klein targets the Gemini 4 generation. Its thinking depth is set with
// thinkingLevel (LOW, MEDIUM, HIGH; MINIMAL is rejected), not a token budget.

const modelGemini4Argon = "gemini-4-argon"

// getGeminiModel returns the model to request: the default when unset, the
// "argon" alias resolved, and any other name passed through unchanged so an
// invalid name surfaces as an API error rather than being silently swapped.
func getGeminiModel(model string) string {
	switch model {
	case "", "argon":
		return modelGemini4Argon
	default:
		return model
	}
}

// thinkingLevelFor maps klein's effort onto a Gemini thinking level. The
// lowest level is LOW, so none/minimal land there; xhigh/max land on HIGH.
// Empty leaves the API default.
func thinkingLevelFor(effort string) genai.ThinkingLevel {
	switch effort {
	case "":
		return ""
	case "none", "minimal", "low":
		return genai.ThinkingLevelLow
	case "medium":
		return genai.ThinkingLevelMedium
	default:
		return genai.ThinkingLevelHigh
	}
}

// ModelCapabilities represents the capabilities of a Gemini model
type ModelCapabilities struct {
	SupportsVision      bool
	SupportsToolCalling bool
	SupportsStructured  bool
	MaxTokens           int
	// MaxContextWindow is the approximate input context window size
	// Gemini 4 models support ~1,048,576 token input contexts.
	MaxContextWindow     int
	SupportsSystemPrompt bool
	SupportsMultimodal   bool
	IsReasoningModel     bool
}

// getModelCapabilities returns the capability profile for a model. Every
// model is treated as the Gemini 4 generation (values from models.get for
// gemini-4-argon).
func getModelCapabilities(_ string) ModelCapabilities {
	return ModelCapabilities{
		SupportsVision:       true,
		SupportsToolCalling:  true,  // Function calling; requires thought signatures echoed back
		SupportsStructured:   true,  // Structured output supported
		MaxTokens:            32768, // Output token limit: 32,768 (Input limit: 1,048,576)
		MaxContextWindow:     1048576,
		SupportsSystemPrompt: true,
		SupportsMultimodal:   true, // Text, images, video, audio, PDF
		IsReasoningModel:     true, // Thinking supported via thinkingLevel
	}
}
