package config

import (
	"encoding/json"
	"errors"
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

	// AppServer configures the generic app-server backend (used only when
	// llm.backend == "appserver"). The server owns its own model configuration.
	AppServer AppServerSettings `json:"appserver,omitempty"`

	// AutoApproveCommands lists command prefixes an app-server backend may run
	// without asking. It sits here rather than in the codex/appserver blocks
	// because it is deliberately one list for both: whether the agent behind the
	// protocol is codex or gallium does not change which commands you trust it to
	// run unattended.
	//
	// Empty (the default) means every request is still asked, i.e. exactly the
	// behavior before this existed. It is intentionally NOT seeded with defaults:
	// bash.whitelisted_commands is a list chosen for klein's own sandbox-free Bash
	// tool, and inheriting it here would auto-approve `go run` and `make` on a
	// surface where approval can mean "run this outside the sandbox".
	//
	// Matching is prefix-on-word-boundary against the command with its shell
	// wrapper removed, and never applies to a command carrying shell chaining or
	// substitution. See agentserver.WithAutoApprove.
	AutoApproveCommands []string `json:"auto_approve_commands,omitempty"` //nolint:tagliatelle

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

// LLMSettings contains LLM client configuration.
//
//nolint:tagliatelle // json keys are the established on-disk settings schema
type LLMSettings struct {
	Backend   string `json:"backend"`              // "openai", "anthropic", "gemini", "codex", or "appserver"
	Model     string `json:"model"`                // model name
	BaseURL   string `json:"base_url,omitempty"`   // optional provider base URL (OpenAI/Azure-compatible)
	Thinking  bool   `json:"thinking,omitempty"`   // enable thinking mode
	MaxTokens int    `json:"max_tokens,omitempty"` // maximum tokens for model responses (0 = use model default)
	// Effort sets the reasoning effort for reasoning-capable models (primarily
	// OpenAI GPT-5). Empty = backend default. The full vocabulary is in
	// ValidEfforts, but actual support is model-dependent — e.g. gpt-5.6-luna accepts
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

	// MaxToolResultRunes caps how much of a single tool result stays inline in
	// the conversation; anything longer is offloaded to a file under the
	// project's tool_results/ directory and replaced with a stub the model can
	// read back. Counted in runes, so CJK text gets the same budget as ASCII.
	// 0 selects tool.DefaultMaxToolResultRunes.
	//nolint:tagliatelle // snake_case matches the established on-disk settings schema
	MaxToolResultRunes int `json:"max_tool_result_runes,omitempty"`
}

// CodexSettings configures the codex app-server backend. Model and effort are
// taken from LLMSettings; these fields cover codex-specific behavior. All are
// optional with headless-friendly defaults.
type CodexSettings struct {
	CodexPath      string `json:"codex_path,omitempty"`      // path to the codex binary ("" → "codex" on PATH)
	ApprovalPolicy string `json:"approval_policy,omitempty"` //nolint:tagliatelle // never|on-request|untrusted|granular
	SandboxMode    string `json:"sandbox_mode,omitempty"`    // read-only|workspace-write|danger-full-access ("" → workspace-write)
	// SandboxWorkspaceWrite and ShellEnvironmentPolicy mirror the codex config
	// tables of the same names. Unlike the three fields above — which klein
	// states per-thread on thread/start — these have no thread-scoped equivalent,
	// so klein passes them to the child as `-c` overrides on its launch line
	// (see agentserver.codexConfigArgs). Every field is optional and an unset one
	// is not passed at all, leaving ~/.codex/config.toml in charge of it.
	//
	// Every json tag below spells a codex config key, so all of them are
	// snake_case whatever klein's own convention would be — hence the nolints.
	SandboxWorkspaceWrite  SandboxWorkspaceWriteSettings  `json:"sandbox_workspace_write,omitempty"`  //nolint:tagliatelle
	ShellEnvironmentPolicy ShellEnvironmentPolicySettings `json:"shell_environment_policy,omitempty"` //nolint:tagliatelle
}

// SandboxWorkspaceWriteSettings mirrors codex's [sandbox_workspace_write] table,
// which tunes the workspace-write sandbox — most usefully network_access, off by
// default, which is what blocks a tool like `gh` from reaching the network.
//
// It applies only while the effective sandbox is workspace-write; under
// read-only or danger-full-access codex ignores the table, so pairing this with
// a different codex.sandbox_mode is inert rather than an error.
//
// The bools are pointers so "unset" stays distinct from "explicitly false":
// codex defaults them to false, and klein passing a false the user never asked
// for would override a true in their own config.
type SandboxWorkspaceWriteSettings struct {
	NetworkAccess       *bool `json:"network_access,omitempty"`         //nolint:tagliatelle
	ExcludeTmpdirEnvVar *bool `json:"exclude_tmpdir_env_var,omitempty"` //nolint:tagliatelle
	ExcludeSlashTmp     *bool `json:"exclude_slash_tmp,omitempty"`      //nolint:tagliatelle
	// WritableRoots are extra paths writable beyond the workspace itself.
	// Last by fieldalignment: a slice's pointer sits at its front, so ending on
	// one leaves the trailing len/cap words outside the GC's scan range.
	WritableRoots []string `json:"writable_roots,omitempty"` //nolint:tagliatelle
}

// ShellEnvironmentPolicySettings mirrors codex's [shell_environment_policy]
// table, which decides what environment the commands codex runs actually see.
// codex filters the inherited environment by default, so a token the shell
// exported does not necessarily reach a tool.
type ShellEnvironmentPolicySettings struct {
	// Inherit is core|all|none — the exact set codex accepts. "" leaves it unset.
	Inherit string `json:"inherit,omitempty"`
	// IgnoreDefaultExcludes disables codex's built-in filtering of names that
	// look like secrets (*KEY*, *TOKEN*, *SECRET*).
	IgnoreDefaultExcludes *bool `json:"ignore_default_excludes,omitempty"` //nolint:tagliatelle
	// Set adds or overrides variables outright.
	Set map[string]string `json:"set,omitempty"`
	// Exclude and IncludeOnly are case-insensitive glob patterns over variable
	// names, applied after Inherit. Last by fieldalignment, as above.
	Exclude     []string `json:"exclude,omitempty"`
	IncludeOnly []string `json:"include_only,omitempty"` //nolint:tagliatelle
}

// ShellEnvironmentInheritModes are the values codex accepts for
// shell_environment_policy.inherit; anything else makes it exit at startup with
// "unknown variant".
var ShellEnvironmentInheritModes = []string{"core", "all", "none"}

// AppServerSettings configures the generic app-server backend (llm.backend ==
// "appserver"): any local agent that speaks the codex-app-server JSON-RPC
// protocol with the `dynamicTools` experimental capability. Such a server runs
// its own agent loop and owns its model configuration, so klein only needs to
// know how to launch it and whether to be consulted before it mutates anything.
// It has no sandbox of its own.
//
// Nothing here names a particular implementation: Command is required precisely
// because there is no single "the" app-server binary.
type AppServerSettings struct {
	// Command is the app-server binary (e.g. "gallium", or an absolute path).
	// Required — this backend has no default binary.
	Command string `json:"command,omitempty"`
	// ApprovalPolicy is never|on-request ("" → the mode default).
	ApprovalPolicy string `json:"approval_policy,omitempty"` //nolint:tagliatelle // established settings schema
	// Config is an optional path to the server's own TOML config (e.g.
	// ../rs-gallium/configs/gemma4.toml). Servers of this kind are configured by
	// environment variables, so klein reads the file's [llm]/[agent] tables and
	// passes them to the child as env; frontend-only tables are ignored. Values
	// here win over the ambient environment.
	Config string `json:"config,omitempty"`
	// Args overrides the subcommand used to enter app-server mode.
	// Empty → ["app-server"], the protocol's conventional entry point.
	// Last field by fieldalignment: the only one carrying a slice header.
	Args []string `json:"args,omitempty"`
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
		LLM: GetDefaultLLMSettingsForBackend(DefaultBackend),
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

// DefaultBackend is the backend used when none is configured.
const DefaultBackend = "openai"

// The whole-agent app-server backend ids — the values settings.json may name.
// These live here rather than beside the app-server client because they are
// settings vocabulary: the client is told which dialect it is driving
// (agentserver.Dialect) and has no interest in what a config file called it.
const (
	BackendCodex     = "codex"
	BackendAppServer = "appserver"
)

// backendACPRemoved is the pre-rename id of BackendAppServer. It named the wrong
// protocol — see the AppServerSettings doc comment — and is rejected with a
// pointer to the new id rather than silently aliased.
const backendACPRemoved = "acp"

// GetDefaultLLMSettingsForBackend returns default LLM settings for a specific backend
func GetDefaultLLMSettingsForBackend(backend string) LLMSettings {
	switch backend {
	case "anthropic", "claude":
		return LLMSettings{
			Backend:   "anthropic",
			Model:     "claude-sonnet-4-6",
			BaseURL:   "",
			Thinking:  true,
			MaxTokens: 0,
		}
	case "gemini":
		return LLMSettings{
			Backend:   "gemini",
			Model:     "gemini-2.5-flash-lite",
			BaseURL:   "",
			Thinking:  false, // Gemini doesn't support thinking in our implementation
			MaxTokens: 0,
		}
	case BackendCodex, BackendAppServer:
		// Model is left empty: these backends use the model configured in their
		// own config unless overridden via llm.model / -m.
		return LLMSettings{
			Backend: backend,
		}
	case "", DefaultBackend:
		// openai — the default backend, and the fallback for an unset one.
		return LLMSettings{
			Backend:   DefaultBackend,
			Model:     "gpt-5.6-luna",
			BaseURL:   "",
			Thinking:  true,
			MaxTokens: 0,
			Effort:    "low", // preserves the prior hardcoded default for GPT-5 reasoning
		}
	default:
		// An unrecognized backend keeps its name so ValidateSettings rejects it,
		// rather than being silently coerced into a working one (which would let
		// e.g. `-b ollama` quietly run openai instead).
		return LLMSettings{Backend: backend}
	}
}

// IsAgentServerBackend reports whether the backend is a whole-agent app-server
// backend, which owns its own model and credentials — as opposed to a chat model
// klein drives through its own ReAct loop.
func IsAgentServerBackend(backend string) bool {
	return backend == BackendCodex || backend == BackendAppServer
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
	// surfaces here as the default chat model — clear that leak (an explicit model
	// is kept).
	if IsAgentServerBackend(settings.LLM.Backend) {
		if settings.LLM.Model == defaults.LLM.Model {
			settings.LLM.Model = ""
		}
	} else if settings.LLM.Model == "" {
		settings.LLM.Model = defaults.LLM.Model
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
	case "openai", "anthropic", "claude", "gemini", BackendCodex, BackendAppServer:
	case backendACPRemoved:
		return fmt.Errorf(
			"unsupported LLM backend: %s — renamed to %q. \"ACP\" is ambiguous: klein speaks the "+
				"codex-app-server protocol, not the agentclientprotocol.com standard. "+
				"Rename llm.backend and the \"acp\" settings block to \"appserver\"",
			settings.LLM.Backend, BackendAppServer)
	default:
		return fmt.Errorf(
			"unsupported LLM backend: %s (must be 'openai', 'anthropic', 'gemini', 'codex', or 'appserver')",
			settings.LLM.Backend)
	}

	// An app-server backend manages its own model and auth, so an empty model is
	// fine and no API key is required here.
	if !IsAgentServerBackend(settings.LLM.Backend) && settings.LLM.Model == "" {
		return fmt.Errorf("LLM model is required")
	}

	if !IsValidEffort(settings.LLM.Effort) {
		return fmt.Errorf(
			"invalid effort %q (must be empty or one of %v; actual support is model-dependent, "+
				"e.g. gpt-5.6-luna accepts none/low/medium/high/xhigh but not minimal)",
			settings.LLM.Effort, ValidEfforts)
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
	// 0 means "use the built-in default"; a negative budget is a typo, not a
	// request to keep every result inline.
	if settings.Agent.MaxToolResultRunes < 0 {
		return errors.New("max_tool_result_runes must be zero (default) or positive")
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
