package openai

import (
	"testing"
)

func TestGetOpenAIModel(t *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		{"gpt-5.4", "gpt-5.4"},
		{"gpt-5.4-mini", "gpt-5.4-mini"},
		{"gpt-5.4-nano-2026-03-17", "gpt-5.4-nano-2026-03-17"}, // dated variant
		{"gpt-5.2-pro", "gpt-5.2-pro"},
		{"gpt-5.1-mini", "gpt-5.1-mini"},
		{"gpt-5", "gpt-5"},
		{"gpt-5-mini", "gpt-5-mini"},
		{"gpt-5-nano", "gpt-5-nano"},
		{"gpt-5-chat-latest", "gpt-5-chat-latest"},
		{"unknown-model", "gpt-5-mini"}, // default fallback
	}

	for _, tc := range testCases {
		result := getOpenAIModel(tc.input)
		if result != tc.expected {
			t.Errorf("getOpenAIModel(%q) = %q, expected %q", tc.input, result, tc.expected)
		}
	}
}

func TestGetModelCapabilities(t *testing.T) {
	testCases := []struct {
		model                string
		expectedVision       bool
		expectedToolCalling  bool
		expectedStructured   bool
		expectedSystemPrompt bool
		expectedThinking     bool
	}{
		{"gpt-5.4", true, true, true, true, true},
		{"gpt-5.4-mini", true, true, true, true, true},
		{"gpt-5.4-nano", true, true, true, true, true},
		{"gpt-5.4-nano-2026-03-17", true, true, true, true, true},
		{"gpt-5.2-pro", true, true, true, true, true},
		{"gpt-5.1-mini", true, true, true, true, true},
		{"gpt-5", true, true, true, true, true},
		{"gpt-5-mini", true, true, true, true, true},
		{"gpt-5-nano", true, true, true, true, true},
		{"unknown-model", true, true, true, true, true}, // default → gpt-5 profile
	}

	for _, tc := range testCases {
		caps := getModelCapabilities(tc.model)

		if caps.SupportsVision != tc.expectedVision {
			t.Errorf("Model %s vision: got %v, expected %v", tc.model, caps.SupportsVision, tc.expectedVision)
		}
		if caps.SupportsToolCalling != tc.expectedToolCalling {
			t.Errorf("Model %s tool calling: got %v, expected %v", tc.model, caps.SupportsToolCalling, tc.expectedToolCalling)
		}
		if caps.SupportsStructured != tc.expectedStructured {
			t.Errorf("Model %s structured: got %v, expected %v", tc.model, caps.SupportsStructured, tc.expectedStructured)
		}
		if caps.SupportsSystemPrompt != tc.expectedSystemPrompt {
			t.Errorf("Model %s system prompt: got %v, expected %v", tc.model, caps.SupportsSystemPrompt, tc.expectedSystemPrompt)
		}
		if caps.SupportsThinking != tc.expectedThinking {
			t.Errorf("Model %s thinking: got %v, expected %v", tc.model, caps.SupportsThinking, tc.expectedThinking)
		}
	}
}

func TestNewOpenAIClient_NoAPIKey(t *testing.T) {
	_, err := NewOpenAIClient("gpt-5-mini", 0)
	if err == nil {
		t.Skip("OPENAI_API_KEY is set in environment, skipping test")
	}

	expectedErr := "OPENAI_API_KEY environment variable not set"
	if err.Error() != expectedErr {
		t.Errorf("Expected error %q, got %q", expectedErr, err.Error())
	}
}

func TestIsToolCapable(t *testing.T) {
	core := &OpenAICore{model: "gpt-5-mini"}
	client := NewOpenAIClientFromCore(core)

	concreteClient, ok := client.(*OpenAIClient)
	if !ok {
		t.Fatal("Expected *OpenAIClient type")
	}
	if !concreteClient.IsToolCapable() {
		t.Error("Expected gpt-5-mini to support tool calling")
	}
}
