package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/internal/repository"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// Default maximum iterations for agents
const DefaultAgentMaxIterations = 30

// Settings represents the main application settings
type Settings struct {
	LLM   LLMSettings   `json:"llm"`
	MCP   MCPSettings   `json:"mcp"`
	Agent AgentSettings `json:"agent"`
	Bash  BashSettings  `json:"bash,omitempty"`

	// BaseDir is the root for shared per-user state (sessions, memory, the
	// schedule store). Empty resolves to ~/.klein. It is env-expanded on load.
	// Both the CLI and the `klein claw` gateway derive their paths from it, so
	// pointing --settings at a file with a different base_dir yields a fully
	// isolated instance.
	BaseDir string `json:"base_dir,omitempty"`

	// Claw carries the gateway configuration as an opaque block. It is parsed by
	// internal/gateway (which owns its schema) so this package stays free of the
	// gateway's heavy dependencies (discordgo, connect, cron).
	Claw json.RawMessage `json:"claw,omitempty"`

	// Codex configures the codex app-server backend (used only when
	// llm.backend == "codex"). Model/effort still come from the llm block.
	Codex CodexSettings `json:"codex,omitempty"`

	// Kessel configures the kessel app-server backend (used only when
	// llm.backend == "kessel"). Kessel owns its own model configuration.
	Kessel KesselSettings `json:"kessel,omitempty"`

	// Repository for persistence (nil for in-memory only)
	settingsRepository repository.SettingsRepository `json:"-"`
}

// ResolvedBaseDir returns the env-expanded base directory, defaulting to
// ~/.klein when unset.
func (s *Settings) ResolvedBaseDir() string {
	base := os.ExpandEnv(s.BaseDir)
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".klein")
	}
	return base
}

// SessionsDir is <base>/sessions — per-session persistence files.
func (s *Settings) SessionsDir() string {
	return filepath.Join(s.ResolvedBaseDir(), "sessions")
}

// MemoryDir is <base>/memory — MemorySearch/Get/Write plus daily/ and runs/.
func (s *Settings) MemoryDir() string {
	return filepath.Join(s.ResolvedBaseDir(), "memory")
}

// SchedulesFile is <base>/schedules.json — the dynamic schedule store the
// Schedule* tools write and the gateway scheduler watches.
func (s *Settings) SchedulesFile() string {
	return filepath.Join(s.ResolvedBaseDir(), "schedules.json")
}

// MemoryDBFile is <base>/memory/memory.sqlite — the versioned long-term memory
// store backing the Remember/Recall/Reinforce tools (memorydb).
func (s *Settings) MemoryDBFile() string {
	return filepath.Join(s.MemoryDir(), "memory.sqlite")
}

// LLMSettings contains LLM client configuration
type LLMSettings struct {
	Backend   string `json:"backend"`              // "ollama", "anthropic", "openai", or "gemini"
	Model     string `json:"model"`                // model name
	BaseURL   string `json:"base_url,omitempty"`   // for ollama or openai (Azure)
	Thinking  bool   `json:"thinking,omitempty"`   // enable thinking mode
	MaxTokens int    `json:"max_tokens,omitempty"` // maximum tokens for model responses (0 = use model default)
	// Effort sets the reasoning effort for reasoning-capable models (primarily
	// OpenAI GPT-5). Empty = backend default. The full vocabulary is in
	// ValidEfforts, but actual support is model-dependent — e.g. gpt-5.4 accepts
	// none/low/medium/high/xhigh but not minimal. Ignored by models without
	// reasoning effort.
	Effort string `json:"effort,omitempty"`
}

// ValidEfforts lists every reasoning-effort value accepted by the OpenAI API
// (empty = backend default). Per the OpenAI reasoning guide these are
// model-dependent; a given model may accept only a subset.
var ValidEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh"}

// IsValidEffort reports whether e is empty (backend default) or a recognized
// reasoning-effort value.
func IsValidEffort(e string) bool {
	return e == "" || slices.Contains(ValidEfforts, e)
}

// MCPSettings contains MCP server configuration. On the wire it uses the
// Claude-Code/Cursor "map of name → server" shape:
//
//	"mcp": {
//	  "browser-sandbox": { "command": "docker", "args": ["run", "..."] },
//	  "docs":            { "url": "https://example.com/mcp" }
//	}
//
// enabled defaults to true and type is inferred (command → stdio, url → sse).
// Internally it is kept as a slice for the rest of the code.
type MCPSettings struct {
	Servers []domain.MCPServerConfig `json:"-"`
}

// mcpServerSpec is the per-server on-disk shape (Claude Code style). enabled is
// a pointer so an omitted value defaults to true; env is a map like Claude Code.
type mcpServerSpec struct {
	Enabled            *bool                `json:"enabled,omitempty"`
	Type               domain.MCPServerType `json:"type,omitempty"`
	Command            string               `json:"command,omitempty"`
	Args               []string             `json:"args,omitempty"`
	Env                map[string]string    `json:"env,omitempty"`
	URL                string               `json:"url,omitempty"`
	AuthorizationToken string               `json:"authorization_token,omitempty"`
	Headers            map[string]string    `json:"headers,omitempty"`
	AllowedTools       []string             `json:"allowed_tools,omitempty"`
}

func (s mcpServerSpec) toConfig(name string) domain.MCPServerConfig {
	enabled := true
	if s.Enabled != nil {
		enabled = *s.Enabled
	}
	typ := s.Type
	if typ == "" {
		switch {
		case s.Command != "":
			typ = domain.MCPServerTypeStdio
		case s.URL != "":
			typ = domain.MCPServerTypeSSE
		}
	}
	return domain.MCPServerConfig{
		Name:               name,
		Enabled:            enabled,
		Type:               typ,
		Command:            s.Command,
		Args:               s.Args,
		Env:                envMapToSlice(s.Env),
		URL:                s.URL,
		AuthorizationToken: s.AuthorizationToken,
		Headers:            s.Headers,
		AllowedTools:       s.AllowedTools,
	}
}

func specFromConfig(c domain.MCPServerConfig) mcpServerSpec {
	s := mcpServerSpec{
		Type:               c.Type,
		Command:            c.Command,
		Args:               c.Args,
		Env:                envSliceToMap(c.Env),
		URL:                c.URL,
		AuthorizationToken: c.AuthorizationToken,
		Headers:            c.Headers,
		AllowedTools:       c.AllowedTools,
	}
	if !c.Enabled { // omit when true (the default); emit only explicit disable
		disabled := false
		s.Enabled = &disabled
	}
	return s
}

// UnmarshalJSON decodes the Claude-Code/Cursor map shape into Servers.
func (m *MCPSettings) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic order
	for _, name := range names {
		var spec mcpServerSpec
		if err := json.Unmarshal(raw[name], &spec); err != nil {
			return fmt.Errorf("mcp server %q: %w — expected Claude Code map style, e.g. \"mcp\": { %q: { \"command\": \"...\" } }", name, err, name)
		}
		m.Servers = append(m.Servers, spec.toConfig(name))
	}
	return nil
}

// MarshalJSON writes the map shape so a re-saved config round-trips.
func (m MCPSettings) MarshalJSON() ([]byte, error) {
	out := make(map[string]mcpServerSpec, len(m.Servers))
	for _, c := range m.Servers {
		out[c.Name] = specFromConfig(c)
	}
	return json.Marshal(out)
}

// envMapToSlice converts a {"KEY":"VAL"} map into sorted "KEY=VAL" entries.
func envMapToSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(env))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// envSliceToMap converts "KEY=VAL" entries back into a map.
func envSliceToMap(env []string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for _, e := range env {
		if i := indexByte(e, '='); i >= 0 {
			out[e[:i]] = e[i+1:]
		} else {
			out[e] = ""
		}
	}
	return out
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// AgentSettings contains agent behavior configuration
type AgentSettings struct {
	MaxIterations int    `json:"max_iterations"`
	LogLevel      string `json:"log_level"`
}

// CodexSettings configures the codex app-server backend. Model and effort are
// taken from LLMSettings; these fields cover codex-specific behavior. All are
// optional with headless-friendly defaults.
type CodexSettings struct {
	CodexPath      string `json:"codex_path,omitempty"`      // path to the codex binary ("" → "codex" on PATH)
	ApprovalPolicy string `json:"approval_policy,omitempty"` //nolint:tagliatelle // never|on-request|untrusted|granular
	SandboxMode    string `json:"sandbox_mode,omitempty"`    // read-only|workspace-write|danger-full-access ("" → workspace-write)
}

// KesselSettings configures the kessel app-server backend (llm.backend ==
// "kessel"). Kessel runs its own agent loop and owns its model configuration,
// so klein only needs to know how to launch it and whether to be consulted
// before it mutates anything. It has no sandbox of its own.
type KesselSettings struct {
	KesselPath     string `json:"kessel_path,omitempty"`     // path to the kessel binary ("" → "kessel-cli" on PATH)
	ApprovalPolicy string `json:"approval_policy,omitempty"` // never|on-request ("" → the mode default)
}

// BashSettings contains bash tool configuration
type BashSettings struct {
	WhitelistedCommands []string `json:"whitelisted_commands,omitempty"` // Commands that don't require approval
}

// NewSettings creates new settings with in-memory repository
func NewSettings() *Settings {
	return NewSettingsWithRepository(infra.NewInMemorySettingsRepository())
}

// NewSettingsWithRepository creates new settings with injected repository
func NewSettingsWithRepository(settingsRepository repository.SettingsRepository) *Settings {
	settings := GetDefaultSettings()
	settings.settingsRepository = settingsRepository
	return settings
}

// NewSettingsWithPath creates new settings with file-based repository
func NewSettingsWithPath(configPath string) *Settings {
	repo := infra.NewFileSettingsRepository(configPath)
	return NewSettingsWithRepository(repo)
}

// Load loads settings from the repository
func (s *Settings) Load() error {
	if s.settingsRepository == nil {
		return fmt.Errorf("no settings repository configured")
	}

	data, err := s.settingsRepository.Load()
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}

	if err := json.Unmarshal(data, s); err != nil {
		return fmt.Errorf("failed to parse settings: %w", err)
	}

	// Apply defaults for missing fields
	applyDefaults(s)
	return nil
}

// Save saves settings to the repository
func (s *Settings) Save() error {
	if s.settingsRepository == nil {
		return fmt.Errorf("no settings repository configured")
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	return s.settingsRepository.Save(data)
}

// LoadSettings loads application settings from a JSON file
func LoadSettings(configPath string) (*Settings, error) {
	// Create settings with file repository
	settings := NewSettingsWithPath(configPath)

	// Resolve the settings file path: the explicit path, or a search of the
	// standard locations (.agents/, ~/.klein/).
	path := configPath
	if path == "" {
		foundPath, _ := settings.settingsRepository.FindSettingsFile()
		path = foundPath
	}

	// No settings file exists yet.
	if _, statErr := os.Stat(path); path == "" || os.IsNotExist(statErr) {
		if configPath != "" {
			// An explicit path was requested but is missing: scaffold it.
			return createSettingsFileAtPath(configPath)
		}
		// Nothing configured anywhere: create a default file and use defaults.
		return createDefaultSettingsFile()
	}

	// The file exists — a load/parse failure here is a real error the user must
	// see. Do NOT silently fall back to defaults (which hides typos) or
	// overwrite the file with defaults.
	if err := settings.Load(); err != nil {
		return nil, fmt.Errorf("invalid settings file %s: %w", path, err)
	}

	return settings, nil
}

// SaveSettings saves application settings to a JSON file
func SaveSettings(configPath string, settings *Settings) error {
	if settings.settingsRepository != nil {
		// Use the injected repository
		return settings.Save()
	}

	// Fallback to direct file operations (for backward compatibility)
	if configPath == "" {
		// Try to find existing settings file first
		configPath = findSettingsFile()
		if configPath == "" {
			// No existing file, save to .agents in current directory
			configPath = filepath.Join(".agents", "settings.json")
		}
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Marshal to JSON with pretty formatting
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	// Write to file
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write settings file: %w", err)
	}

	return nil
}

// GetDefaultSettings returns default application settings
func GetDefaultSettings() *Settings {
	return &Settings{
		LLM: LLMSettings{
			Backend:   "ollama",
			Model:     "gpt-oss:latest",
			BaseURL:   "http://localhost:11434",
			Thinking:  true,
			MaxTokens: 0, // 0 = use model-specific defaults
		},
		Agent: AgentSettings{
			MaxIterations: DefaultAgentMaxIterations,
			LogLevel:      "info",
		},
		Bash: BashSettings{
			WhitelistedCommands: []string{
				"go build",
				"go test",
				"go run",
				"go mod tidy",
				"go fmt",
				"go vet",
				"git status",
				"git log",
				"git diff",
				"ls",
				"pwd",
				"cat",
				"head",
				"tail",
				"grep",
				"find",
				"echo",
				"which",
				"make",
				"npm install",
				"npm run",
				"npm test",
			},
		},
	}
}

// GetDefaultLLMSettingsForBackend returns default LLM settings for a specific backend
func GetDefaultLLMSettingsForBackend(backend string) LLMSettings {
	switch backend {
	case "ollama":
		return LLMSettings{
			Backend:   "ollama",
			Model:     "gpt-oss:latest",
			BaseURL:   "http://localhost:11434",
			Thinking:  true,
			MaxTokens: 0,
		}
	case "anthropic", "claude":
		return LLMSettings{
			Backend:   "anthropic",
			Model:     "claude-sonnet-4-6",
			BaseURL:   "",
			Thinking:  true,
			MaxTokens: 0,
		}
	case "openai":
		return LLMSettings{
			Backend:   "openai",
			Model:     "gpt-5.4-mini",
			BaseURL:   "",
			Thinking:  true,
			MaxTokens: 0,
			Effort:    "low", // preserves prior hardcoded default for GPT-5 reasoning
		}
	case "gemini":
		return LLMSettings{
			Backend:   "gemini",
			Model:     "gemini-2.5-flash-lite",
			BaseURL:   "",
			Thinking:  false, // Gemini doesn't support thinking in our implementation
			MaxTokens: 0,
		}
	case "codex", "kessel":
		// Model is left empty: these backends use the model configured in their
		// own config unless overridden via llm.model / -m.
		return LLMSettings{
			Backend: backend,
		}
	default:
		// Default to ollama settings for unknown backends
		return GetDefaultLLMSettingsForBackend("ollama")
	}
}

// isAgentServerBackend reports whether the backend is a whole-agent app-server
// backend, which owns its own model and credentials. Mirrors
// agentserver.IsAgentBackend, duplicated here because agentserver imports this
// package.
func isAgentServerBackend(backend string) bool {
	return backend == "codex" || backend == "kessel"
}

// applyDefaults fills in missing fields with default values
func applyDefaults(settings *Settings) {
	defaults := GetDefaultSettings()

	// Apply LLM defaults
	if settings.LLM.Backend == "" {
		settings.LLM.Backend = defaults.LLM.Backend
	}
	// An app-server backend owns its own model (via its own config), so it must
	// not inherit a chat-model default. The base Settings is seeded from
	// GetDefaultSettings before the file is unmarshaled, so an omitted model
	// surfaces here as the ollama default — clear that leak (an explicit model
	// is kept).
	if isAgentServerBackend(settings.LLM.Backend) {
		if settings.LLM.Model == defaults.LLM.Model {
			settings.LLM.Model = ""
		}
	} else if settings.LLM.Model == "" {
		settings.LLM.Model = defaults.LLM.Model
	}
	if settings.LLM.BaseURL == "" && settings.LLM.Backend == "ollama" {
		settings.LLM.BaseURL = defaults.LLM.BaseURL
	}

	// Apply MCP defaults (no config_path needed anymore)

	// Apply Agent defaults
	if settings.Agent.MaxIterations == 0 {
		settings.Agent.MaxIterations = defaults.Agent.MaxIterations
	}
	if settings.Agent.LogLevel == "" {
		settings.Agent.LogLevel = defaults.Agent.LogLevel
	}
}

// ValidateSettings validates the settings configuration
func ValidateSettings(settings *Settings) error {
	// Validate LLM settings
	switch settings.LLM.Backend {
	case "ollama", "anthropic", "openai", "gemini", "codex", "kessel":
	default:
		return fmt.Errorf("unsupported LLM backend: %s (must be 'ollama', 'anthropic', 'openai', 'gemini', 'codex', or 'kessel')", settings.LLM.Backend)
	}

	// An app-server backend manages its own model and auth, so an empty model is
	// fine and no API key is required here.
	if !isAgentServerBackend(settings.LLM.Backend) && settings.LLM.Model == "" {
		return fmt.Errorf("LLM model is required")
	}

	if !IsValidEffort(settings.LLM.Effort) {
		return fmt.Errorf("invalid effort %q (must be empty or one of %v; actual support is model-dependent, e.g. gpt-5.4 accepts none/low/medium/high/xhigh but not minimal)", settings.LLM.Effort, ValidEfforts)
	}

	if settings.LLM.Backend == "anthropic" {
		// Check environment variable for API key
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			return fmt.Errorf("Anthropic API key is required (set ANTHROPIC_API_KEY environment variable)")
		}
	}

	if settings.LLM.Backend == "openai" {
		// Check environment variable for API key
		if os.Getenv("OPENAI_API_KEY") == "" {
			return fmt.Errorf("OpenAI API key is required (set OPENAI_API_KEY environment variable)")
		}
	}

	if settings.LLM.Backend == "gemini" {
		// Check environment variable for API key
		if os.Getenv("GEMINI_API_KEY") == "" {
			return fmt.Errorf("Gemini API key is required (set GEMINI_API_KEY environment variable)")
		}
	}

	// Validate Agent settings
	if settings.Agent.MaxIterations <= 0 {
		return fmt.Errorf("max_iterations must be positive")
	}

	// Validate MCP server configurations
	for _, serverConfig := range settings.MCP.Servers {
		if err := ValidateMCPServerConfig(serverConfig); err != nil {
			return fmt.Errorf("invalid MCP server configuration for %s: %w", serverConfig.Name, err)
		}
	}

	return nil
}

// findSettingsFile searches for settings.json in order of preference:
// 1. .agents/settings.json in current directory
// 2. $HOME/.klein/settings.json
// Returns empty string if none found
func findSettingsFile() string {
	// Check .agents in current directory
	currentDirPath := filepath.Join(".agents", "settings.json")
	if _, err := os.Stat(currentDirPath); err == nil {
		return currentDirPath
	}

	// Check $HOME/.klein
	homeDir, err := os.UserHomeDir()
	if err == nil {
		homeDirPath := filepath.Join(homeDir, ".klein", "settings.json")
		if _, err := os.Stat(homeDirPath); err == nil {
			return homeDirPath
		}
	}

	// No settings file found
	return ""
}

// ValidateMCPServerConfig validates an MCP server configuration
func ValidateMCPServerConfig(config domain.MCPServerConfig) error {
	if config.Name == "" {
		return fmt.Errorf("server name is required")
	}

	switch config.Type {
	case domain.MCPServerTypeStdio:
		if config.Command == "" {
			return fmt.Errorf("command is required for stdio servers")
		}
	case domain.MCPServerTypeSSE, domain.MCPServerTypeHTTP:
		if config.URL == "" {
			return fmt.Errorf("URL is required for HTTP/SSE servers")
		}
	default:
		return fmt.Errorf("unsupported server type: %s", config.Type)
	}

	return nil
}

// createDefaultSettingsFile creates a default settings.json file in ~/.klein/
func createDefaultSettingsFile() (*Settings, error) {
	// Determine where to create the file (prefer home directory)
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return GetDefaultSettings(), nil // Fall back to defaults without file creation
	}

	settingsPath := filepath.Join(homeDir, ".klein", "settings.json")
	return createSettingsFileAtPath(settingsPath)
}

// createSettingsFileAtPath creates a default settings file at the specified path
func createSettingsFileAtPath(settingsPath string) (*Settings, error) {
	// Create settings with file repository
	settings := NewSettingsWithPath(settingsPath)

	// Save default settings to file
	if err := settings.Save(); err != nil {
		// Return defaults without repository if saving fails
		return GetDefaultSettings(), nil
	}

	// Log success message
	pkgLogger.NewComponentLogger("settings").InfoWithIntention(pkgLogger.IntentionConfig, "Created default settings file", "path", settingsPath)
	pkgLogger.NewComponentLogger("settings").InfoWithIntention(pkgLogger.IntentionStatus, "You can edit this file to customize your configuration")

	return settings, nil
}
