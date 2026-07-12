package agentserver

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeConfig writes a kessel config YAML to a temp file and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kessel.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// parseEnvKVs turns KEY=VALUE pairs into a map for assertions.
func parseEnvKVs(kvs []string) map[string]string {
	m := map[string]string{}
	for _, kv := range kvs {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// TestKesselEnv_LocalModel covers a local-model config (rs-kessel's gemma4.yaml
// shape): an hf: modelPath, an explicitly empty baseURL, and Swift-only sections
// that must be ignored rather than rejected.
func TestKesselEnv_LocalModel(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, `
llm:
  modelPath: "hf:unsloth/gemma-4-E4B-it-GGUF/gemma-4-E4B-it-Q4_K_M.gguf"
  baseURL: ""
  model: "gemma4:e4b"
  harmonyTemplate: false
  temperature: 0.7
  maxTokens: 2048

agent:
  systemPromptPath: null
  maxTurns: 50
  skillPaths: ["../skills"]

tts:
  enabled: true
  voice: "com.apple.voice.enhanced.en-US.Zoe"
stt:
  enabled: true
watcher:
  enabled: true
`)
	env, err := kesselEnv(path)
	if err != nil {
		t.Fatalf("kesselEnv: %v", err)
	}
	got := parseEnvKVs(env)

	want := map[string]string{
		"MODEL_PATH":           "hf:unsloth/gemma-4-E4B-it-GGUF/gemma-4-E4B-it-Q4_K_M.gguf",
		"LLM_MODEL":            "gemma4:e4b",
		"LLM_TEMPERATURE":      "0.7",
		"MAX_TOKENS":           "2048",
		"MAX_REACT_ITERATIONS": "50",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	// An explicit empty baseURL must be passed through (it means "local model"),
	// which is different from omitting the key.
	if v, ok := got["LLM_BASE_URL"]; !ok || v != "" {
		t.Errorf("LLM_BASE_URL = %q (present=%v), want present and empty", v, ok)
	}
	// No apiKey in the file → never exported (the child inherits OPENAI_API_KEY).
	if _, ok := got["OPENAI_API_KEY"]; ok {
		t.Error("OPENAI_API_KEY should not be exported when the config has no apiKey")
	}
}

// TestKesselEnv_RemoteModel covers an OpenAI-style config with reasoningEffort
// and a blank apiKey (kessel's convention for "read it from the environment").
func TestKesselEnv_RemoteModel(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, `
llm:
  baseURL: "https://api.openai.com/v1"
  model: "gpt-5.6-luna"
  apiKey: ""
  maxTokens: 8192
  reasoningEffort: "high"
agent:
  maxTurns: 50
`)
	env, err := kesselEnv(path)
	if err != nil {
		t.Fatalf("kesselEnv: %v", err)
	}
	got := parseEnvKVs(env)

	if got["LLM_BASE_URL"] != "https://api.openai.com/v1" || got["LLM_MODEL"] != "gpt-5.6-luna" {
		t.Errorf("base/model = %q/%q", got["LLM_BASE_URL"], got["LLM_MODEL"])
	}
	if got["REASONING_EFFORT"] != "high" || got["MAX_TOKENS"] != "8192" {
		t.Errorf("effort/maxTokens = %q/%q", got["REASONING_EFFORT"], got["MAX_TOKENS"])
	}
	// A blank apiKey means "use the ambient OPENAI_API_KEY" — do not export it.
	if _, ok := got["OPENAI_API_KEY"]; ok {
		t.Error("blank apiKey must not be exported")
	}
	// Absent keys are not exported at all.
	if _, ok := got["MODEL_PATH"]; ok {
		t.Error("MODEL_PATH should be absent when the config omits modelPath")
	}
}

func TestKesselEnv_ExportsExplicitAPIKey(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "llm:\n  apiKey: \"sk-test\"\n")
	got := parseEnvKVs(mustEnv(t, path))
	if got["OPENAI_API_KEY"] != "sk-test" {
		t.Errorf("OPENAI_API_KEY = %q, want sk-test", got["OPENAI_API_KEY"])
	}
}

func TestKesselEnv_Errors(t *testing.T) {
	t.Parallel()
	if _, err := kesselEnv(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("missing file should error")
	}
	if _, err := kesselEnv(writeConfig(t, "llm: [this is not a map")); err == nil {
		t.Error("malformed yaml should error")
	}
}

func mustEnv(t *testing.T, path string) []string {
	t.Helper()
	env, err := kesselEnv(path)
	if err != nil {
		t.Fatalf("kesselEnv: %v", err)
	}
	return env
}

func TestChildEnv_OverridesWinAndInherit(t *testing.T) {
	// t.Setenv marks this test non-parallel.
	t.Setenv("KLEIN_TEST_INHERITED", "keep-me")
	t.Setenv("LLM_MODEL", "from-shell")

	env := childEnv([]string{"LLM_MODEL=from-config", "MODEL_PATH=hf:x/y.gguf"})
	got := parseEnvKVs(env)

	// The config value wins over the ambient shell.
	if got["LLM_MODEL"] != "from-config" {
		t.Errorf("LLM_MODEL = %q, want from-config (config must beat the shell)", got["LLM_MODEL"])
	}
	// Config-only keys are added.
	if got["MODEL_PATH"] != "hf:x/y.gguf" {
		t.Errorf("MODEL_PATH = %q", got["MODEL_PATH"])
	}
	// Unrelated ambient vars (e.g. OPENAI_API_KEY, PATH) are still inherited.
	if got["KLEIN_TEST_INHERITED"] != "keep-me" {
		t.Errorf("ambient env not inherited: %q", got["KLEIN_TEST_INHERITED"])
	}
	// No duplicate keys in the result (exec would otherwise be ambiguous).
	keys := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		keys = append(keys, k)
	}
	slices.Sort(keys)
	n := len(keys)
	if len(slices.Compact(keys)) != n {
		t.Error("child env contains duplicate keys")
	}
}

func TestChildEnv_NilWhenNoOverrides(t *testing.T) {
	t.Parallel()
	if env := childEnv(nil); env != nil {
		t.Errorf("no overrides should yield nil (inherit as-is), got %v", env)
	}
}
