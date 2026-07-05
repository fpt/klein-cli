package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadCodexModelNotLeaked confirms a codex settings file with no model does
// not inherit the ollama chat-model default (the base Settings is seeded from
// defaults before unmarshal, so an omitted model must be cleared for codex —
// codex rejects a chat model like gpt-oss).
func TestLoadCodexModelNotLeaked(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(p, []byte(`{"llm":{"backend":"codex"},"base_dir":"`+dir+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.LLM.Backend != "codex" {
		t.Fatalf("backend: got %q", s.LLM.Backend)
	}
	if s.LLM.Model != "" {
		t.Errorf("codex model should be empty (codex-owned), got %q", s.LLM.Model)
	}
}

// TestLoadCodexModelExplicitKept confirms an explicit codex model survives.
func TestLoadCodexModelExplicitKept(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(p, []byte(`{"llm":{"backend":"codex","model":"gpt-5.4"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.LLM.Model != "gpt-5.4" {
		t.Errorf("explicit codex model dropped: got %q", s.LLM.Model)
	}
}

// TestValidateCodexBackend confirms the codex backend is accepted and, unlike
// the API backends, does not require a model or an API key (codex owns its own
// model/auth via the codex CLI).
func TestValidateCodexBackend(t *testing.T) {
	t.Parallel()
	s := GetDefaultSettings()
	s.LLM = LLMSettings{Backend: "codex"} // no model, no key
	if err := ValidateSettings(s); err != nil {
		t.Errorf("codex backend with empty model should validate, got: %v", err)
	}
}

// TestCodexBackendDefault confirms `-b codex` resolves to the codex backend with
// an empty model (codex uses its configured default).
func TestCodexBackendDefault(t *testing.T) {
	t.Parallel()
	llm := GetDefaultLLMSettingsForBackend("codex")
	if llm.Backend != "codex" {
		t.Errorf("backend: got %q want codex", llm.Backend)
	}
	if llm.Model != "" {
		t.Errorf("codex default model should be empty (codex-owned), got %q", llm.Model)
	}
}
