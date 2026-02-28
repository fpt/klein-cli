package ollama

import "strings"

type OllamaModel struct {
	Name string `json:"name"`

	// Tool indicates whether the model supports native tool calling
	Tool bool `json:"tool"`

	// Think indicates whether the model supports thinking
	Think bool `json:"think"`

	// Vision indicates whether the model supports image input (multimodal)
	Vision bool `json:"vision"`

	// Context indicates the context length of the model
	Context int `json:"context"`
}

// This is from https://ollama.com/search
// List must be kept in sync with the Ollama models by human.
var ollamaModels = []OllamaModel{
	{
		Name:    "gpt-oss:latest",
		Tool:    true,   // ✅ Confirmed: supports native tool calling perfectly
		Think:   true,   // ✅ Confirmed: shows thinking tokens in CLI and API
		Vision:  false,  // Unknown vision capability
		Context: 128000, // Conservative context estimate
	},
	{
		Name:    "gpt-oss:20b",
		Tool:    true,   // ✅ Confirmed: supports native tool calling perfectly
		Think:   true,   // ✅ Confirmed: shows thinking tokens in CLI and API
		Vision:  false,  // Unknown vision capability
		Context: 128000, // Conservative context estimate
	},
	{
		Name:    "gpt-oss:120b",
		Tool:    true,
		Think:   true,
		Vision:  false,
		Context: 128000,
	},
	{
		Name:    "gemma3:latest",
		Tool:    false, // No native tool calling
		Think:   false, // No thinking capability
		Vision:  true,  // Has vision capability
		Context: 8192,  // Standard context for Gemma models
	},
	// qwen3 family — confirmed tool-capable via Ollama native tool calling API
	// Think: true so ChatWithToolChoice honours c.thinking (settings JSON) to disable by default
	{Name: "qwen3:0.6b", Tool: true, Think: true, Vision: false, Context: 40960},
	{Name: "qwen3:1.7b", Tool: true, Think: true, Vision: false, Context: 40960},
	{Name: "qwen3:4b", Tool: true, Think: true, Vision: false, Context: 40960},
	{Name: "qwen3:8b", Tool: true, Think: true, Vision: false, Context: 40960},
	{Name: "qwen3:14b", Tool: true, Think: true, Vision: false, Context: 40960},
	{Name: "qwen3:30b", Tool: true, Think: true, Vision: false, Context: 40960},
	{Name: "qwen3:32b", Tool: true, Think: true, Vision: false, Context: 40960},
	// qwen3.5 family — Tool: false due to Ollama bug (issue #14493):
	// Ollama's qwen3.5 pipeline uses the wrong tool-call format (Qwen3 Hermes JSON) but
	// the model was trained on Qwen3-Coder XML format. Sending tools in the API request
	// crashes the model runner with a 500 error. Disable native tool calling until fixed.
	{Name: "qwen3.5:27b", Tool: false, Think: true, Vision: true, Context: 256000},
	{Name: "qwen3.5:35b", Tool: false, Think: true, Vision: true, Context: 256000},
	{Name: "qwen3.5:122b", Tool: false, Think: true, Vision: true, Context: 256000},
	// glm-4.7 family — uses XML tool call format, incompatible with Ollama JSON tool calling API
	{Name: "glm-4.7", Tool: false, Think: false, Vision: false, Context: 128000},
	// GLM-4.5-Air — claims native tool calling + thinking; unconfirmed, added for testing
	{Name: "glm-4.5-air", Tool: true, Think: true, Vision: false, Context: 128000},
}

// IsToolCapableModel checks if a model supports native tool calling
func IsToolCapableModel(model string) bool {
	modelLower := strings.ToLower(model)

	// Check against the structured model list
	for _, ollamaModel := range ollamaModels {
		if strings.Contains(modelLower, strings.ToLower(ollamaModel.Name)) {
			return ollamaModel.Tool
		}
	}

	return false
}

// IsThinkingCapableModel checks if a model supports thinking/reasoning
func IsThinkingCapableModel(model string) bool {
	modelLower := strings.ToLower(model)

	// Check against the structured model list
	for _, ollamaModel := range ollamaModels {
		if strings.Contains(modelLower, strings.ToLower(ollamaModel.Name)) {
			return ollamaModel.Think
		}
	}

	return false
}

// IsVisionCapableModel checks if a model supports vision/image input
func IsVisionCapableModel(model string) bool {
	modelLower := strings.ToLower(model)

	// Check against the structured model list
	for _, ollamaModel := range ollamaModels {
		if strings.Contains(modelLower, strings.ToLower(ollamaModel.Name)) {
			return ollamaModel.Vision
		}
	}

	return false
}

// IsModelInKnownList checks if a model is in our known models list
func IsModelInKnownList(model string) bool {
	modelLower := strings.ToLower(model)

	// Check against the structured model list
	for _, ollamaModel := range ollamaModels {
		if strings.Contains(modelLower, strings.ToLower(ollamaModel.Name)) {
			return true
		}
	}

	return false
}

// GetModelContextWindow returns the known context window for a model.
// If the model isn't in the known list, returns 0 to indicate unknown.
func GetModelContextWindow(model string) int {
	modelLower := strings.ToLower(model)
	for _, ollamaModel := range ollamaModels {
		if strings.Contains(modelLower, strings.ToLower(ollamaModel.Name)) {
			return ollamaModel.Context
		}
	}
	return 0
}

// IsJSONSchemaCapableModel checks if a model supports JSON Schema format for structured output
// JSON Schema is supported by most Ollama models that don't have native tool calling
func IsJSONSchemaCapableModel(model string) bool {
	modelLower := strings.ToLower(model)

	// Check against the structured model list first
	for _, ollamaModel := range ollamaModels {
		if strings.Contains(modelLower, strings.ToLower(ollamaModel.Name)) {
			// Models with native tool calling don't need JSON Schema format for structured output
			return !ollamaModel.Tool
		}
	}

	// For unknown models, assume JSON Schema support (most Ollama models support it)
	// This provides better user experience for new/unlisted models
	return true
}
