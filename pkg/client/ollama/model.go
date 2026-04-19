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

	// Per-model sampling parameters (zero values = use global defaults)
	Temperature float64 `json:"temperature,omitempty"`
	TopP        float64 `json:"top_p,omitempty"`
	TopK        int     `json:"top_k,omitempty"`

	// UseThinkToken: model uses <|think|> token at the start of the system prompt
	// to enable thinking, instead of the Think API parameter (e.g. gemma4).
	UseThinkToken bool `json:"use_think_token,omitempty"`
}

// SamplingParams holds per-model sampling configuration returned by GetModelSamplingParams.
type SamplingParams struct {
	Temperature float64
	TopP        float64 // 0 = not set
	TopK        int     // 0 = not set
}

const defaultTemperature = 0.1

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
	// gemma4 family — native function calling + thinking via <|think|> system prompt token.
	// Official sampling params from https://ai.google.dev/gemma/docs/core/model_card_4:
	// temperature=1.0, top_p=0.95, top_k=64.
	{Name: "gemma4", Tool: true, Think: true, Vision: true, Context: 128000,
		Temperature: 1.0, TopP: 0.95, TopK: 64, UseThinkToken: true},
	// qwen3.5 family
	{Name: "qwen3.5:0.6b", Tool: true, Think: true, Vision: true, Context: 256000},
	{Name: "qwen3.5:0.8b", Tool: true, Think: true, Vision: true, Context: 256000},
	{Name: "qwen3.5:2b", Tool: true, Think: true, Vision: true, Context: 256000},
	{Name: "qwen3.5:4b", Tool: true, Think: true, Vision: true, Context: 256000},
	{Name: "qwen3.5:9b", Tool: true, Think: true, Vision: true, Context: 256000},
	{Name: "qwen3.5:latest", Tool: true, Think: true, Vision: true, Context: 256000},
	{Name: "qwen3.5:27b", Tool: true, Think: true, Vision: true, Context: 256000},
	{Name: "qwen3.5:35b", Tool: true, Think: true, Vision: true, Context: 256000},
	{Name: "qwen3.5:122b", Tool: true, Think: true, Vision: true, Context: 256000},
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

// GetModelSamplingParams returns the recommended sampling parameters for a model.
// Models without explicit settings get the global default temperature.
func GetModelSamplingParams(model string) SamplingParams {
	modelLower := strings.ToLower(model)
	for _, m := range ollamaModels {
		if strings.Contains(modelLower, strings.ToLower(m.Name)) {
			if m.Temperature != 0 {
				return SamplingParams{
					Temperature: m.Temperature,
					TopP:        m.TopP,
					TopK:        m.TopK,
				}
			}
		}
	}
	return SamplingParams{Temperature: defaultTemperature}
}

// UseThinkTokenModel returns true if the model uses the <|think|> system prompt token
// for thinking instead of the Think API parameter (e.g. gemma4 family).
func UseThinkTokenModel(model string) bool {
	modelLower := strings.ToLower(model)
	for _, m := range ollamaModels {
		if strings.Contains(modelLower, strings.ToLower(m.Name)) {
			return m.UseThinkToken
		}
	}
	return false
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
