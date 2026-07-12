package agentserver

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// kesselConfig is the subset of a kessel config YAML (e.g. rs-kessel's
// configs/gemma4.yaml) that the app-server backend actually uses.
//
// The file is primarily a frontend config — its tts/stt/watcher sections are
// consumed by the Swift/C# apps and are irrelevant here, so they are simply not
// modeled. `kessel-cli app-server` reads none of this itself: it is configured
// purely by environment variables, so klein translates the llm/agent settings
// into the env it expects (see kesselEnv).
//
// Pointers distinguish "absent" from "present but empty/zero" — an explicit
// `baseURL: ""` means "no base URL" (local model), which differs from omitting it.
//
//nolint:tagliatelle // key names are kessel's config schema (baseURL, …), not ours
type kesselConfig struct {
	LLM struct {
		ModelPath       *string  `yaml:"modelPath"`
		BaseURL         *string  `yaml:"baseURL"`
		Model           *string  `yaml:"model"`
		APIKey          *string  `yaml:"apiKey"`
		Temperature     *float64 `yaml:"temperature"`
		MaxTokens       *int     `yaml:"maxTokens"`
		ReasoningEffort *string  `yaml:"reasoningEffort"`
	} `yaml:"llm"`
	Agent struct {
		MaxTurns *int `yaml:"maxTurns"`
	} `yaml:"agent"`
}

// kesselEnv reads the kessel config at path and returns the environment
// overrides for the spawned `kessel-cli app-server`, mapping its YAML keys onto
// the env vars kessel's EnvConfig::from_env reads.
//
// apiKey is only exported when the file sets a non-empty value: kessel's configs
// conventionally leave it blank and expect OPENAI_API_KEY from the ambient
// environment, which the child inherits.
func kesselEnv(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read kessel config %s: %w", path, err)
	}
	var cfg kesselConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse kessel config %s: %w", path, err)
	}

	var env []string
	set := func(key string, val *string) {
		if val != nil {
			env = append(env, key+"="+*val)
		}
	}
	set("MODEL_PATH", cfg.LLM.ModelPath)
	set("LLM_BASE_URL", cfg.LLM.BaseURL)
	set("LLM_MODEL", cfg.LLM.Model)
	set("REASONING_EFFORT", cfg.LLM.ReasoningEffort)
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
