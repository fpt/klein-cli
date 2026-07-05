package config

import "testing"

// TestValidateCodexBackend confirms the codex backend is accepted and, unlike
// the API backends, does not require a model or an API key (codex owns its own
// model/auth via the codex CLI).
func TestValidateCodexBackend(t *testing.T) {
	s := GetDefaultSettings()
	s.LLM = LLMSettings{Backend: "codex"} // no model, no key
	if err := ValidateSettings(s); err != nil {
		t.Errorf("codex backend with empty model should validate, got: %v", err)
	}
}

// TestCodexBackendDefault confirms `-b codex` resolves to the codex backend with
// an empty model (codex uses its configured default).
func TestCodexBackendDefault(t *testing.T) {
	llm := GetDefaultLLMSettingsForBackend("codex")
	if llm.Backend != "codex" {
		t.Errorf("backend: got %q want codex", llm.Backend)
	}
	if llm.Model != "" {
		t.Errorf("codex default model should be empty (codex-owned), got %q", llm.Model)
	}
}
