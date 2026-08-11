package agentbackend

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/internal/config"
)

// envLLMModel is the var the config's `model` key translates to, and the one the
// precedence test sets from both sources.
const envLLMModel = "LLM_MODEL"

// writeConfig writes an app-server config TOML to a temp file and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "appserver.toml")
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

// TestAppServerEnv_LocalModel covers a local-model config (rs-gallium's gemma4.toml
// shape): an hf: modelPath, an explicitly empty baseURL, and REPL-only keys that
// must be ignored rather than rejected.
func TestAppServerEnv_LocalModel(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, `
[llm]
modelPath = "hf:unsloth/gemma-4-E4B-it-GGUF/gemma-4-E4B-it-Q4_K_M.gguf"
baseURL = ""
model = "gemma4:e4b"
inferenceEngine = "candle"
temperature = 0.7
maxTokens = 2048

[agent]
systemPromptPath = "system-prompt.md"
maxTurns = 50
skillPaths = ["../skills"]

[[mcpServers]]
command = "godevmcp"
args = ["serve"]
`)
	env, err := appServerEnv(path)
	if err != nil {
		t.Fatalf("appServerEnv: %v", err)
	}
	got := parseEnvKVs(env)

	want := map[string]string{
		"MODEL_PATH":           "hf:unsloth/gemma-4-E4B-it-GGUF/gemma-4-E4B-it-Q4_K_M.gguf",
		"LLM_MODEL":            "gemma4:e4b",
		"INFERENCE_ENGINE":     "candle",
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

// TestAppServerEnv_RemoteModel covers an OpenAI-style config with reasoningEffort and
// a blank apiKey (the convention for "read it from the environment").
func TestAppServerEnv_RemoteModel(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, `
[llm]
baseURL = "https://api.openai.com/v1"
model = "gpt-5.6-luna"
apiKey = ""
maxTokens = 8192
reasoningEffort = "high"

[agent]
maxTurns = 50
`)
	env, err := appServerEnv(path)
	if err != nil {
		t.Fatalf("appServerEnv: %v", err)
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

func TestAppServerEnv_ExportsExplicitAPIKey(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "[llm]\napiKey = \"sk-test\"\n")
	got := parseEnvKVs(mustEnv(t, path))
	if got["OPENAI_API_KEY"] != "sk-test" {
		t.Errorf("OPENAI_API_KEY = %q, want sk-test", got["OPENAI_API_KEY"])
	}
}

// TestAppServerEnv_RelativeModelPath is the crux of translating a config file into env:
// the server resolves a relative modelPath against the config's directory, but
// reads MODEL_PATH relative to its cwd, so klein must anchor it here.
func TestAppServerEnv_RelativeModelPath(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "[llm]\nmodelPath = \"../models/x.gguf\"\n")
	got := parseEnvKVs(mustEnv(t, path))

	want := filepath.Join(filepath.Dir(path), "../models/x.gguf")
	if got["MODEL_PATH"] != want {
		t.Errorf("MODEL_PATH = %q, want %q (anchored at the config's dir)", got["MODEL_PATH"], want)
	}
	if !filepath.IsAbs(got["MODEL_PATH"]) {
		t.Errorf("MODEL_PATH = %q, want an absolute path", got["MODEL_PATH"])
	}
}

// TestAppServerEnv_AbsoluteModelPath confirms an absolute path is passed through as-is.
func TestAppServerEnv_AbsoluteModelPath(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "[llm]\nmodelPath = \"/models/x.gguf\"\n")
	if got := parseEnvKVs(mustEnv(t, path)); got["MODEL_PATH"] != "/models/x.gguf" {
		t.Errorf("MODEL_PATH = %q, want /models/x.gguf", got["MODEL_PATH"])
	}
}

func TestAppServerEnv_Errors(t *testing.T) {
	t.Parallel()
	if _, err := appServerEnv(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Error("missing file should error")
	}
	if _, err := appServerEnv(writeConfig(t, "[llm\nmodel = ")); err == nil {
		t.Error("malformed toml should error")
	}
}

func mustEnv(t *testing.T, path string) []string {
	t.Helper()
	env, err := appServerEnv(path)
	if err != nil {
		t.Fatalf("appServerEnv: %v", err)
	}
	return env
}

// appServerSettings builds settings for the appserver backend. The deprecated
// Config field is only reachable from here and from the code under test, so the
// nolint is confined to this one helper rather than repeated per test.
//
//nolint:staticcheck // SA1019: exercising the deprecated field is the point.
func appServerSettings(configPath string, env map[string]string) *config.Settings {
	s := &config.Settings{}
	s.LLM.Backend = config.BackendAppServer
	s.AppServer.Config = configPath
	s.AppServer.Env = env
	return s
}

// TestAppServerEnvironment_EnvTable is the case the table exists for: a server
// option klein has never heard of (rs-gallium's GALLIUM_CPU_MOE) reaching the
// child without a klein struct field to carry it.
func TestAppServerEnvironment_EnvTable(t *testing.T) {
	t.Parallel()
	env, err := appServerEnvironment(appServerSettings("", map[string]string{
		"GALLIUM_CPU_MOE":    "1",
		"GALLIUM_GPU_LAYERS": "20",
	}), nil)
	if err != nil {
		t.Fatalf("appServerEnvironment: %v", err)
	}
	got := parseEnvKVs(env)
	if got["GALLIUM_CPU_MOE"] != "1" || got["GALLIUM_GPU_LAYERS"] != "20" {
		t.Errorf("env = %v, want the table passed through verbatim", got)
	}
}

// TestAppServerEnvironment_TableOverridesConfig pins the precedence: the table
// names the variable outright, the config translation only infers one, so a key
// set in both resolves to the table's value. Order is what encodes this —
// childEnv is last-wins — so the assertion is on position, not just presence.
func TestAppServerEnvironment_TableOverridesConfig(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "[llm]\nmodel = \"from-config\"\nmaxTokens = 2048\n")
	env, err := appServerEnvironment(appServerSettings(path, map[string]string{
		envLLMModel: "from-table",
	}), nil)
	if err != nil {
		t.Fatalf("appServerEnvironment: %v", err)
	}

	var last string
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == envLLMModel {
			last = v
		}
	}
	if last != "from-table" {
		t.Errorf("last LLM_MODEL = %q, want from-table (the table must win)", last)
	}
	// The translation's other keys survive: the table overrides, it does not replace.
	if got := parseEnvKVs(env)["MAX_TOKENS"]; got != "2048" {
		t.Errorf("MAX_TOKENS = %q, want 2048 — the config translation still applies", got)
	}
}

// TestAppServerEnvironment_Sorted guards reproducibility: Go randomizes map
// order, so an unsorted table would shuffle the env between spawns and make an
// override-order failure impossible to reproduce.
func TestAppServerEnvironment_Sorted(t *testing.T) {
	t.Parallel()
	settings := appServerSettings("", map[string]string{"C": "3", "A": "1", "B": "2"})
	for range 10 {
		env, err := appServerEnvironment(settings, nil)
		if err != nil {
			t.Fatalf("appServerEnvironment: %v", err)
		}
		if want := []string{"A=1", "B=2", "C=3"}; !slices.Equal(env, want) {
			t.Fatalf("env = %v, want %v", env, want)
		}
	}
}

// TestAppServerEnvironment_NonAppServerBackend confirms codex is untouched: it
// is configured by `-c` launch flags, and an appserver block left in the file
// from an earlier backend must not leak into its environment.
func TestAppServerEnvironment_NonAppServerBackend(t *testing.T) {
	t.Parallel()
	settings := appServerSettings("", map[string]string{"GALLIUM_CPU_MOE": "1"})
	settings.LLM.Backend = config.BackendCodex
	env, err := appServerEnvironment(settings, nil)
	if err != nil {
		t.Fatalf("appServerEnvironment: %v", err)
	}
	if len(env) != 0 {
		t.Errorf("env = %v, want empty for the codex backend", env)
	}
}

// TestAppServerEnvironment_ConfigError confirms a broken config still fails the
// spawn rather than being silently skipped now that env has a second source.
func TestAppServerEnvironment_ConfigError(t *testing.T) {
	t.Parallel()
	settings := appServerSettings(filepath.Join(t.TempDir(), "nope.toml"), map[string]string{"A": "1"})
	if _, err := appServerEnvironment(settings, nil); err == nil {
		t.Error("a missing config should error, not fall through to the env table")
	}
}
