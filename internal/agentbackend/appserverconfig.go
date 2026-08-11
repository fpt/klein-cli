package agentbackend

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/fpt/klein-cli/internal/config"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// appServerConfig is the subset of an app-server's config TOML (e.g.
// rs-gallium's configs/gemma4.toml) that this backend actually uses.
//
// Such a file is primarily the server's own frontend config — tables like
// [[mcpServers]] and the agent's systemPromptPath/skillPaths are consumed by the
// server's REPL and are irrelevant to a klein-driven app-server turn, so they are
// simply not modeled; the decoder ignores keys absent here. The app-server reads
// none of this itself: it is configured purely by environment variables, so klein
// translates the [llm]/[agent] settings into the env it expects (see appServerEnv).
//
// Pointers distinguish "absent" from "present but empty/zero" — an explicit
// `baseURL = ""` means "no base URL" (local model), which differs from omitting it.
//
// This struct is **frozen**. It is a hand-written map of one server's config keys
// onto env vars, so it falls behind that server every time the server grows an
// option — by the time [appserver.env] was added, five of rs-gallium's env vars
// (GALLIUM_CPU_MOE, GALLIUM_GPU_LAYERS, MMPROJ_PATH, CONTEXT_WINDOW,
// GALLIUM_TOKENIZER_REPO) were unreachable through it. Chasing that is the
// treadmill the env table exists to get off, so a new option goes there, not
// here. Existing keys stay: configs in the wild still set them.
//
//nolint:tagliatelle // key names are the server's config schema (baseURL, …), not ours
type appServerConfig struct {
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

// deprecatedConfigWarning is emitted once per spawn when appserver.config is
// set. The setting still works — this is a nudge, not a removal.
const deprecatedConfigWarning = "appserver.config is deprecated: klein hand-maps a fixed set of the " +
	"server's config keys onto env vars, so options the server adds later are unreachable through it. " +
	"Prefer an [appserver.env] table, or hand the file to the server itself via " +
	`appserver.args (e.g. ["app-server", "--config", "…"]) if it accepts one.`

// appServerEnvironment assembles the environment overrides for the spawned
// app-server, layered least- to most-specific:
//
//  1. the deprecated appserver.config translation, then
//  2. the explicit [appserver.env] table.
//
// The table wins because it names the variable outright, where the translation
// only infers one — and because it is the escape hatch for exactly the case the
// translation cannot express, which would be worthless if the translation could
// shadow it. childEnv (pkg/agentserver) resolves duplicates last-wins, so
// appending in this order is the whole implementation of that precedence.
//
// Both are appserver-only: codex is configured by `-c` launch flags, not env.
// logger may be nil, which is silent.
//
//nolint:staticcheck // SA1019: this function *is* the deprecated field's honored path.
func appServerEnvironment(settings *config.Settings, logger *pkgLogger.Logger) ([]string, error) {
	if settings.LLM.Backend != config.BackendAppServer {
		return nil, nil
	}
	var env []string
	if settings.AppServer.Config != "" {
		if logger != nil {
			logger.Warn(deprecatedConfigWarning, "config", settings.AppServer.Config)
		}
		var err error
		if env, err = appServerEnv(settings.AppServer.Config); err != nil {
			return nil, err
		}
	}
	return append(env, settings.AppServer.EnvSlice()...), nil
}

// appServerEnv reads the app-server config at path and returns the environment
// overrides for the spawned app-server, mapping the file's TOML keys onto the
// env vars the server reads at startup.
//
// apiKey is only exported when the file sets a non-empty value: these configs
// conventionally leave it blank and expect OPENAI_API_KEY from the ambient
// environment, which the child inherits.
func appServerEnv(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read app-server config %s: %w", path, err)
	}
	var cfg appServerConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse app-server config %s: %w", path, err)
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
