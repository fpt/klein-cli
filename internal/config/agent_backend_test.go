package config

import (
	"os"
	"path/filepath"
	"testing"
)

// agentBackends are the whole-agent app-server backends. They own their own
// model and credentials, so config treats them alike: no inherited chat-model
// default, no required model, no required API key.
var agentBackends = []string{"codex", "kessel"}

// TestLoadAgentBackendModelNotLeaked confirms a settings file with no model does
// not inherit the ollama chat-model default (the base Settings is seeded from
// defaults before unmarshal, so an omitted model must be cleared — these
// backends reject a chat model like gpt-oss).
func TestLoadAgentBackendModelNotLeaked(t *testing.T) {
	t.Parallel()
	for _, backend := range agentBackends {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			p := filepath.Join(dir, "settings.json")
			body := `{"llm":{"backend":"` + backend + `"},"base_dir":"` + dir + `"}`
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			s, err := LoadSettings(p)
			if err != nil {
				t.Fatal(err)
			}
			if s.LLM.Backend != backend {
				t.Fatalf("backend: got %q", s.LLM.Backend)
			}
			if s.LLM.Model != "" {
				t.Errorf("%s model should be empty (backend-owned), got %q", backend, s.LLM.Model)
			}
		})
	}
}

// TestLoadAgentBackendModelExplicitKept confirms an explicit model survives.
func TestLoadAgentBackendModelExplicitKept(t *testing.T) {
	t.Parallel()
	for _, backend := range agentBackends {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			p := filepath.Join(dir, "settings.json")
			body := `{"llm":{"backend":"` + backend + `","model":"gpt-5.4"}}`
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			s, err := LoadSettings(p)
			if err != nil {
				t.Fatal(err)
			}
			if s.LLM.Model != "gpt-5.4" {
				t.Errorf("explicit %s model dropped: got %q", backend, s.LLM.Model)
			}
		})
	}
}

// TestValidateAgentBackend confirms these backends are accepted and, unlike the
// API backends, do not require a model or an API key (they own their own
// model/auth).
func TestValidateAgentBackend(t *testing.T) {
	t.Parallel()
	for _, backend := range agentBackends {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			s := GetDefaultSettings()
			s.LLM = LLMSettings{Backend: backend} // no model, no key
			if err := ValidateSettings(s); err != nil {
				t.Errorf("%s backend with empty model should validate, got: %v", backend, err)
			}
		})
	}
}

// TestAgentBackendDefault confirms `-b codex` / `-b kessel` resolve to that
// backend with an empty model (the backend uses its configured default).
func TestAgentBackendDefault(t *testing.T) {
	t.Parallel()
	for _, backend := range agentBackends {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			llm := GetDefaultLLMSettingsForBackend(backend)
			if llm.Backend != backend {
				t.Errorf("backend: got %q want %q", llm.Backend, backend)
			}
			if llm.Model != "" {
				t.Errorf("%s default model should be empty (backend-owned), got %q", backend, llm.Model)
			}
		})
	}
}

// TestKesselSettingsRoundTrip confirms the kessel block parses from JSON — a
// wrong tag would silently fall back to "kessel-cli" on PATH.
func TestKesselSettingsRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	body := `{"llm":{"backend":"kessel"},"kessel":{"kessel_path":"/opt/kessel","approval_policy":"on-request"}}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Kessel.KesselPath != "/opt/kessel" {
		t.Errorf("kessel_path: got %q", s.Kessel.KesselPath)
	}
	if s.Kessel.ApprovalPolicy != "on-request" {
		t.Errorf("approval_policy: got %q", s.Kessel.ApprovalPolicy)
	}
}

// TestValidateRejectsUnknownBackend guards the backend allowlist.
func TestValidateRejectsUnknownBackend(t *testing.T) {
	t.Parallel()
	s := GetDefaultSettings()
	s.LLM = LLMSettings{Backend: "kessell", Model: "x"} // typo
	if err := ValidateSettings(s); err == nil {
		t.Error("expected an error for an unknown backend")
	}
}
