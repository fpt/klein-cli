package openai

import (
	"testing"
)

// TestGetOpenAIModel pins the adoption policy: only the gpt-5.6 line
// (sol/terra/luna) is accepted. Everything else — including older gpt-5.x and
// gpt-4 — falls back to the default (luna).
func TestGetOpenAIModel(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		input    string
		expected string
	}{
		{ModelLuna, ModelLuna},
		{ModelSol, ModelSol},
		{ModelTerra, ModelTerra},
		{ModelLuna + "-2026-05-01", ModelLuna + "-2026-05-01"}, // dated variant of an adopted model
		// Not adopted → default fallback.
		{"gpt-5.4", defaultModel},
		{"gpt-5.4-mini", defaultModel},
		{"gpt-5.4-nano", defaultModel},
		{"gpt-5.5", defaultModel},
		{"gpt-5", defaultModel},
		{"gpt-4o", defaultModel},
		{"unknown-model", defaultModel},
		{"", defaultModel},
	}

	for _, tc := range testCases {
		if result := getOpenAIModel(tc.input); result != tc.expected {
			t.Errorf("getOpenAIModel(%q) = %q, expected %q", tc.input, result, tc.expected)
		}
	}
}

func TestDefaultModelIsLuna(t *testing.T) {
	t.Parallel()
	if defaultModel != ModelLuna {
		t.Errorf("defaultModel = %q, want %q", defaultModel, ModelLuna)
	}
}

// TestGetModelCapabilities: the adopted models share one capability profile.
func TestGetModelCapabilities(t *testing.T) {
	t.Parallel()
	for _, model := range SupportedModels {
		caps := getModelCapabilities(model)

		if !caps.SupportsVision {
			t.Errorf("Model %s: expected vision support", model)
		}
		if !caps.SupportsToolCalling {
			t.Errorf("Model %s: expected tool calling support", model)
		}
		if !caps.SupportsStructured {
			t.Errorf("Model %s: expected structured output support", model)
		}
		if !caps.SupportsSystemPrompt {
			t.Errorf("Model %s: expected system prompt support", model)
		}
		if !caps.SupportsThinking {
			t.Errorf("Model %s: expected reasoning-effort support", model)
		}
		if caps.MaxTokens != 32768 {
			t.Errorf("Model %s max tokens: got %d, expected 32768", model, caps.MaxTokens)
		}
	}
}

func TestNewOpenAIClient_NoAPIKey(t *testing.T) {
	_, err := NewOpenAIClient(ModelLuna, 0, "")
	if err == nil {
		t.Skip("OPENAI_API_KEY is set in environment, skipping test")
	}

	expectedErr := "OPENAI_API_KEY environment variable not set"
	if err.Error() != expectedErr {
		t.Errorf("Expected error %q, got %q", expectedErr, err.Error())
	}
}

func TestIsToolCapable(t *testing.T) {
	t.Parallel()
	core := &OpenAICore{model: ModelLuna}
	client := NewOpenAIClientFromCore(core)

	concreteClient, ok := client.(*OpenAIClient)
	if !ok {
		t.Fatal("Expected *OpenAIClient type")
	}
	if !concreteClient.IsToolCapable() {
		t.Errorf("Expected %s to support tool calling", ModelLuna)
	}
}

func TestSupportsVision(t *testing.T) {
	t.Parallel()
	core := &OpenAICore{model: ModelLuna}
	client, ok := NewOpenAIClientFromCore(core).(*OpenAIClient)
	if !ok {
		t.Fatal("Expected *OpenAIClient type")
	}
	if !client.SupportsVision() {
		t.Errorf("Expected %s to support vision", ModelLuna)
	}
}
