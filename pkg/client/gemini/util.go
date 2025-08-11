package gemini

// Google Gemini 2.5 Models
// https://ai.google.dev/gemini-api/docs/models

const (
	modelGemini25Pro       = "gemini-2.5-pro"
	modelGemini25Flash     = "gemini-2.5-flash"
	modelGemini25FlashLite = "gemini-2.5-flash-lite"
)

// getGeminiModel maps user-friendly model names to actual Gemini 2.5 model identifiers
func getGeminiModel(model string) string {
	// Normalize the model name to 2.5 series only
	switch model {
	case "gemini-2.5-pro", "gemini-pro", "pro":
		return modelGemini25Pro
	case "gemini-2.5-flash", "gemini-flash", "flash":
		return modelGemini25Flash
	case "gemini-2.5-flash-lite", "gemini-2.5-lite", "gemini-lite", "lite":
		return modelGemini25FlashLite
	default:
		// If it's already a valid Gemini 2.5 model name, return as-is
		if isValidGeminiModel(model) {
			return model
		}
		// Default to Gemini 2.5 Flash for unknown models (most balanced)
		return modelGemini25Flash
	}
}

// isValidGeminiModel checks if a model name is a valid Gemini 2.5 model
func isValidGeminiModel(model string) bool {
	validModels := map[string]bool{
		// Gemini 2.5 models only
		modelGemini25Pro:       true,
		modelGemini25Flash:     true,
		modelGemini25FlashLite: true,
	}
	return validModels[model]
}

// ModelCapabilities represents the capabilities of a Gemini model
type ModelCapabilities struct {
	SupportsVision      bool
	SupportsToolCalling bool
	SupportsStructured  bool
	MaxTokens           int
	// MaxContextWindow is the approximate input context window size
	// Gemini 2.5 models support ~1,048,576 token input contexts.
	MaxContextWindow     int
	SupportsSystemPrompt bool
	SupportsMultimodal   bool
	IsReasoningModel     bool
}

// getModelCapabilities returns the capabilities of a specific Gemini 2.5 model
func getModelCapabilities(model string) ModelCapabilities {
	switch model {
	case modelGemini25Pro:
		return ModelCapabilities{
			SupportsVision:       true,
			SupportsToolCalling:  true,  // Function calling supported
			SupportsStructured:   true,  // Structured output supported
			MaxTokens:            65536, // Output token limit: 65,536 (Input limit: 1,048,576)
			MaxContextWindow:     1048576,
			SupportsSystemPrompt: true,
			SupportsMultimodal:   true, // Audio, images, video, text, PDF
			IsReasoningModel:     true, // Thinking supported, caching, code execution, grounding
		}
	case modelGemini25Flash:
		return ModelCapabilities{
			SupportsVision:       true,
			SupportsToolCalling:  true,  // Function calling supported
			SupportsStructured:   true,  // Structured output supported
			MaxTokens:            65536, // Output token limit: 65,536 (Input limit: 1,048,576)
			MaxContextWindow:     1048576,
			SupportsSystemPrompt: true,
			SupportsMultimodal:   true, // Text, images, video, audio (no PDF)
			IsReasoningModel:     true, // Thinking supported, caching, code execution, grounding
		}
	case modelGemini25FlashLite:
		return ModelCapabilities{
			SupportsVision:       true,
			SupportsToolCalling:  true,  // Function calling supported
			SupportsStructured:   true,  // Structured output supported
			MaxTokens:            65536, // Output token limit: 65,536 (Input limit: 1,048,576)
			MaxContextWindow:     1048576,
			SupportsSystemPrompt: true,
			SupportsMultimodal:   true, // Text, images, video, audio, PDF
			IsReasoningModel:     true, // Thinking supported, caching, code execution, grounding, URL context
		}
	default:
		// Default to Flash Lite capabilities for unknown models
		return ModelCapabilities{
			SupportsVision:       true,
			SupportsToolCalling:  true,  // Function calling supported
			SupportsStructured:   true,  // Structured output supported
			MaxTokens:            65536, // Output token limit: 65,536 (Input limit: 1,048,576)
			MaxContextWindow:     1048576,
			SupportsSystemPrompt: true,
			SupportsMultimodal:   true, // Text, images, video, audio, PDF
			IsReasoningModel:     true, // Thinking supported
		}
	}
}
