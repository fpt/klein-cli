package agentserver

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// acpConfig is the subset of an ACP server's config TOML (e.g. rs-gallium's
// configs/gemma4.toml) that this backend actually uses.
//
// Such a file is primarily the server's own frontend config — tables like
// [[mcpServers]] and the agent's systemPromptPath/skillPaths are consumed by the
// server's REPL and are irrelevant to a klein-driven app-server turn, so they are
// simply not modeled; the decoder ignores keys absent here. The app-server reads
// none of this itself: it is configured purely by environment variables, so klein
// translates the [llm]/[agent] settings into the env it expects (see acpEnv).
//
// Pointers distinguish "absent" from "present but empty/zero" — an explicit
// `baseURL = ""` means "no base URL" (local model), which differs from omitting it.
//
//nolint:tagliatelle // key names are the server's config schema (baseURL, …), not ours
type acpConfig struct {
	LLM struct {
		ModelPath       *string  `toml:"modelPath"`
		BaseURL         *string  `toml:"baseURL"`
		Model           *string  `toml:"model"`
		APIKey          *string  `toml:"apiKey"`
		Temperature     *float64 `toml:"temperature"`
		MaxTokens       *int     `toml:"maxTokens"`
		ReasoningEffort *string  `toml:"reasoningEffort"`
		InferenceEngine *string  `toml:"inferenceEngine"`
	} `toml:"llm"`
	Agent struct {
		MaxTurns *int `toml:"maxTurns"`
	} `toml:"agent"`
}

// acpEnv reads the ACP server config at path and returns the environment
// overrides for the spawned app-server, mapping the file's TOML keys onto the
// env vars the server reads at startup.
//
// apiKey is only exported when the file sets a non-empty value: these configs
// conventionally leave it blank and expect OPENAI_API_KEY from the ambient
// environment, which the child inherits.
func acpEnv(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read acp config %s: %w", path, err)
	}
	var cfg acpConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse acp config %s: %w", path, err)
	}

	var env []string
	set := func(key string, val *string) {
		if val != nil {
			env = append(env, key+"="+*val)
		}
	}
	if cfg.LLM.ModelPath != nil {
		env = append(env, "MODEL_PATH="+resolveModelPath(filepath.Dir(path), *cfg.LLM.ModelPath))
	}
	set("LLM_BASE_URL", cfg.LLM.BaseURL)
	set("LLM_MODEL", cfg.LLM.Model)
	set("REASONING_EFFORT", cfg.LLM.ReasoningEffort)
	set("INFERENCE_ENGINE", cfg.LLM.InferenceEngine)
	if cfg.LLM.APIKey != nil && *cfg.LLM.APIKey != "" {
		env = append(env, "OPENAI_API_KEY="+*cfg.LLM.APIKey)
	}
	if cfg.LLM.Temperature != nil {
		env = append(env, "LLM_TEMPERATURE="+strconv.FormatFloat(*cfg.LLM.Temperature, 'g', -1, 64))
	}
	if cfg.LLM.MaxTokens != nil {
		env = append(env, "MAX_TOKENS="+strconv.Itoa(*cfg.LLM.MaxTokens))
	}
	if cfg.Agent.MaxTurns != nil {
		env = append(env, "MAX_REACT_ITERATIONS="+strconv.Itoa(*cfg.Agent.MaxTurns))
	}
	return env, nil
}

// resolveModelPath makes a config's modelPath meaningful from klein's cwd. A
// server resolves a relative modelPath in its config file against the config's
// own directory, but reads the MODEL_PATH env var relative to the process cwd —
// so translating one into the other has to do that resolution here, or a config
// like `modelPath = "../models/x.gguf"` would break the moment klein is run from
// a different directory. An `hf:` spec names a HuggingFace repo, not a path, and
// is passed through untouched.
func resolveModelPath(configDir, spec string) string {
	if strings.HasPrefix(spec, "hf:") || spec == "" || filepath.IsAbs(spec) {
		return spec
	}
	return filepath.Join(configDir, spec)
}
