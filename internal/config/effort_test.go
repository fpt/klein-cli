package config

import "testing"

func TestIsValidEffort(t *testing.T) {
	valid := []string{"", "none", "minimal", "low", "medium", "high", "xhigh"}
	for _, e := range valid {
		if !IsValidEffort(e) {
			t.Errorf("IsValidEffort(%q) = false, want true", e)
		}
	}
	invalid := []string{"lowish", "HIGH", "max", "1", "default"}
	for _, e := range invalid {
		if IsValidEffort(e) {
			t.Errorf("IsValidEffort(%q) = true, want false", e)
		}
	}
}

func TestOpenAIDefaultEffort(t *testing.T) {
	s := GetDefaultLLMSettingsForBackend("openai")
	if s.Effort != "low" {
		t.Errorf("openai default effort = %q, want \"low\"", s.Effort)
	}
	if !IsValidEffort(s.Effort) {
		t.Errorf("openai default effort %q is not valid", s.Effort)
	}
}

func TestValidateSettingsRejectsBadEffort(t *testing.T) {
	s := GetDefaultSettings()
	s.LLM.Backend = "ollama"
	s.LLM.Effort = "turbo"
	if err := ValidateSettings(s); err == nil {
		t.Error("ValidateSettings accepted invalid effort \"turbo\", want error")
	}
}
