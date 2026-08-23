package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/internal/repository"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// Default maximum iterations for agents
const DefaultAgentMaxIterations = 30

// Settings represents the main application settings
type Settings struct {
	LLM   LLMSettings   `toml:"llm"`
	MCP   MCPSettings   `toml:"mcp"`
	Agent AgentSettings `toml:"agent"`
	Bash  BashSettings  `toml:"bash,omitempty"`

	// BaseDir is the root for shared per-user state (sessions, memory, the
	// schedule store). Empty resolves to ~/.klein. It is env-expanded on load.
	// Both the CLI and the `klein claw` gateway derive their paths from it, so
	// pointing --settings at a file with a different base_dir yields a fully
	// isolated instance.
	BaseDir string `toml:"base_dir,omitempty"`

	// Claw carries the gateway configuration as an opaque block. It is parsed by
	// internal/gateway (which owns its schema) so this package stays free of the
	// gateway's heavy dependencies (discordgo, connect, cron).
	//
	// TOML has no json.RawMessage: the decoder hands back decoded values, not the
	// bytes they came from. A generic map is the honest equivalent — this package
	// stays ignorant of the schema, and gateway.ParseClawConfig re-encodes the map
	// and decodes it into its own types.
	Claw map[string]any `toml:"claw,omitempty"`

	// Codex configures the codex app-server backend (used only when
	// llm.backend == "codex"). Model/effort still come from the llm block.
	Codex CodexSettings `toml:"codex,omitempty"`

	// AppServer configures the generic app-server backend (used only when
	// llm.backend == "appserver"). The server owns its own model configuration.
	AppServer AppServerSettings `toml:"appserver,omitempty"`

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
	AutoApproveCommands []string `toml:"auto_approve_commands,omitempty"`

	// Repository for persistence (nil for in-memory only)
	settingsRepository repository.SettingsRepository `toml:"-"`
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
type LLMSettings struct {
	Backend   string `toml:"backend"`              // "openai", "anthropic", "gemini", "codex", or "appserver"
	Model     string `toml:"model"`                // model name
	BaseURL   string `toml:"base_url,omitempty"`   // optional provider base URL (OpenAI/Azure-compatible)
	Thinking  bool   `toml:"thinking,omitempty"`   // enable thinking mode
	MaxTokens int    `toml:"max_tokens,omitempty"` // maximum tokens for model responses (0 = use model default)
	// Effort sets the reasoning effort for reasoning-capable models (primarily
	// OpenAI GPT-5). Empty = backend default. The full vocabulary is in
	// ValidEfforts, but actual support is model-dependent — e.g. gpt-5.6-luna accepts
	// none/low/medium/high/xhigh but not minimal. Ignored by models without
	// reasoning effort.
	Effort string `toml:"effort,omitempty"`
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

// MCPSettings contains MCP server configuration. On disk it is a table per
// server, keyed by name — the same shape Claude Code and Cursor use, so a server
// definition is recognizable when moved between them:
//
//	[mcp.browser-sandbox]
//	command = "docker"
//	args    = ["run", "..."]
//
//	[mcp.docs]
//	url = "https://example.com/mcp"
//
// enabled defaults to true and type is inferred (command → stdio, url → sse).
// Internally it is kept as a slice for the rest of the code.
type MCPSettings struct {
	Servers []domain.MCPServerConfig `toml:"-"`
}

// mcpServerSpec is the per-server on-disk shape (Claude Code style). enabled is
// a pointer so an omitted value defaults to true; env is a map like Claude Code.
type mcpServerSpec struct {
	Enabled            *bool                `toml:"enabled,omitempty"`
	Type               domain.MCPServerType `toml:"type,omitempty"`
	Command            string               `toml:"command,omitempty"`
	Args               []string             `toml:"args,omitempty"`
	Env                map[string]string    `toml:"env,omitempty"`
	URL                string               `toml:"url,omitempty"`
	AuthorizationToken string               `toml:"authorization_token,omitempty"`
	Headers            map[string]string    `toml:"headers,omitempty"`
	AllowedTools       []string             `toml:"allowed_tools,omitempty"`
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

// MCPServerTOML renders one server as the body of its [mcp.<name>] table, ready
// to hand to tomledit.SetTable.
//
// It encodes the whole `[mcp.<name>]` table and then drops the header line,
// rather than encoding the spec on its own. That is not a detail: a spec encoded
// alone emits its env map as a bare `[env]` header, which under the table would
// read as a *top-level* `[env]` and silently move the server's environment out
// of the server. Encoding at the full path makes the encoder write
// `[mcp.<name>.env]`, which is where it belongs.
func MCPServerTOML(c domain.MCPServerConfig) ([]byte, error) {
	block := map[string]any{"mcp": map[string]any{c.Name: specFromConfig(c)}}

	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	enc.Indent = "" // the body is spliced under a header, not nested in one
	if err := enc.Encode(block); err != nil {
		return nil, fmt.Errorf("rendering mcp server %q: %w", c.Name, err)
	}

	// Drop the `[mcp.<name>]` header the encoder wrote; SetTable supplies it.
	// Matched by name rather than by position: the encoder puts a blank line
	// before the header, so taking "everything after the first line" silently
	// leaves the header in the body and defines the table twice.
	header := "[mcp." + c.Name + "]"
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			return []byte(strings.Join(lines[i+1:], "\n") + "\n"), nil
		}
	}
	return nil, fmt.Errorf("rendering mcp server %q: encoder wrote no %s table", c.Name, header)
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

// UnmarshalTOML turns each [mcp.<name>] table into a server config.
//
// The decoder hands over the already-decoded block, so this re-encodes each
// server's table and decodes it into mcpServerSpec rather than reading the map
// field by field. That keeps one definition of the on-disk shape — the struct
// tags — instead of a second one written in type switches here.
func (m *MCPSettings) UnmarshalTOML(data any) error {
	raw, ok := data.(map[string]any)
	if !ok {
		return fmt.Errorf(`the "mcp" block must be a table of servers, e.g. [mcp.godoc] with command = "..."`)
	}

	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic order

	for _, name := range names {
		table, ok := raw[name].(map[string]any)
		if !ok {
			return fmt.Errorf("mcp server %q must be a table, e.g. [mcp.%s] with command = \"...\"", name, name)
		}
		var spec mcpServerSpec
		if err := DecodeBlock(table, &spec); err != nil {
			return fmt.Errorf("mcp server %q: %w", name, err)
		}
		m.Servers = append(m.Servers, spec.toConfig(name))
	}
	return nil
}

// DecodeBlock decodes an already-decoded TOML block into v.
//
// The TOML decoder hands back values, not the bytes they came from, so a block
// held generically (Settings.Claw, an [mcp.*] table) cannot simply be re-parsed.
// Re-encoding it and decoding into the target type keeps the struct tags as the
// single description of the shape, instead of a second one written by hand in
// type assertions.
func DecodeBlock(block map[string]any, v any) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(block); err != nil {
		return fmt.Errorf("re-encoding block: %w", err)
	}
	if _, err := toml.Decode(buf.String(), v); err != nil {
		return fmt.Errorf("decoding block: %w", err)
	}
	return nil
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
	MaxIterations int    `toml:"max_iterations"`
	LogLevel      string `toml:"log_level"`

	// MaxToolResultRunes caps how much of a single tool result stays inline in
	// the conversation; anything longer is offloaded to a file under the
	// project's tool_results/ directory and replaced with a stub the model can
	// read back. Counted in runes, so CJK text gets the same budget as ASCII.
	// 0 selects tool.DefaultMaxToolResultRunes.
	MaxToolResultRunes int `toml:"max_tool_result_runes,omitempty"`
}

// CodexSettings configures the codex app-server backend. Model and effort are
// taken from LLMSettings; these fields cover codex-specific behavior. All are
// optional with headless-friendly defaults.
type CodexSettings struct {
	CodexPath      string `toml:"codex_path,omitempty"`      // path to the codex binary ("" → "codex" on PATH)
	ApprovalPolicy string `toml:"approval_policy,omitempty"` // never|on-request|untrusted|granular
	SandboxMode    string `toml:"sandbox_mode,omitempty"`    // read-only|workspace-write|danger-full-access ("" → workspace-write)
	// SandboxWorkspaceWrite and ShellEnvironmentPolicy mirror the codex config
	// tables of the same names. Unlike the three fields above — which klein
	// states per-thread on thread/start — these have no thread-scoped equivalent,
	// so klein passes them to the child as `-c` overrides on its launch line
	// (see agentserver.codexConfigArgs). Every field is optional and an unset one
	// is not passed at all, leaving ~/.codex/config.toml in charge of it.
	//
	// Every tag below spells a codex config key verbatim, which is why they are
	// snake_case: these names are codex's, not klein's.
	SandboxWorkspaceWrite  SandboxWorkspaceWriteSettings  `toml:"sandbox_workspace_write,omitempty"`
	ShellEnvironmentPolicy ShellEnvironmentPolicySettings `toml:"shell_environment_policy,omitempty"`
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
	NetworkAccess       *bool `toml:"network_access,omitempty"`
	ExcludeTmpdirEnvVar *bool `toml:"exclude_tmpdir_env_var,omitempty"`
	ExcludeSlashTmp     *bool `toml:"exclude_slash_tmp,omitempty"`
	// WritableRoots are extra paths writable beyond the workspace itself.
	// Last by fieldalignment: a slice's pointer sits at its front, so ending on
	// one leaves the trailing len/cap words outside the GC's scan range.
	WritableRoots []string `toml:"writable_roots,omitempty"`
}

// ShellEnvironmentPolicySettings mirrors codex's [shell_environment_policy]
// table, which decides what environment the commands codex runs actually see.
// codex filters the inherited environment by default, so a token the shell
// exported does not necessarily reach a tool.
type ShellEnvironmentPolicySettings struct {
	// Inherit is core|all|none — the exact set codex accepts. "" leaves it unset.
	Inherit string `toml:"inherit,omitempty"`
	// IgnoreDefaultExcludes disables codex's built-in filtering of names that
	// look like secrets (*KEY*, *TOKEN*, *SECRET*).
	IgnoreDefaultExcludes *bool `toml:"ignore_default_excludes,omitempty"`
	// Set adds or overrides variables outright.
	Set map[string]string `toml:"set,omitempty"`
	// Exclude and IncludeOnly are case-insensitive glob patterns over variable
	// names, applied after Inherit. Last by fieldalignment, as above.
	Exclude     []string `toml:"exclude,omitempty"`
	IncludeOnly []string `toml:"include_only,omitempty"`
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
	// WorkspaceTools offers klein's own workspace tools — Read, Write, Edit,
	// MultiEdit, LS, Glob, Grep, Bash — to the server as dynamic tools, so they
	// run here, in klein's process, against klein's working directory.
	//
	// The default depends on how the server is reached, because the server's own
	// behavior does:
	//
	//   - Dialed (see Address): on, and not really optional. A listening
	//     rs-gallium lends no filesystem or shell tools at all — its built-ins
	//     would run as the user *it* was started as, and a socket carrying no
	//     authentication cannot say who is asking. So a dialed server has no
	//     hands unless klein supplies them, and an explicit false is refused
	//     rather than obeyed into a session that cannot do anything.
	//   - Spawned: off. That server is klein's own child, running with klein's
	//     privileges and its own tools already work. Turning it on there is a
	//     substitution — klein's tools replace the built-ins sharing a name — and
	//     it earns its keep when klein's allowlist, blacklist and approval
	//     prompts are the ones that should apply.
	//
	// A pointer so those three states stay distinct: unset takes the default for
	// the transport, where an explicit value is honored or refused on its own
	// terms. First by fieldalignment for the same reason — it is the only
	// pointer here — not because it is the field to read first; that is Command.
	WorkspaceTools *bool `toml:"workspace_tools,omitempty"`
	// Command is the app-server binary (e.g. "gallium", or an absolute path).
	// Required — this backend has no default binary — unless Address is set, in
	// which case klein starts nothing and Command must be empty.
	Command string `toml:"command,omitempty"`
	// Address dials an app-server already listening on "host:port" (e.g. a
	// `gallium app-server --listen …` on a GPU box) instead of spawning
	// one locally. The agent then runs there while klein's dynamic tools keep
	// running here, on the user's machine.
	//
	// It is mutually exclusive with Command, and with everything that configures
	// a spawned child (Args, Env, Config): that process is started and
	// configured wherever it runs.
	//
	// There is deliberately no default. The connection carries no
	// authentication and no TLS, so anything that can reach the port runs tools
	// as that user on that machine — point it at loopback, an SSH tunnel, or a
	// Tailscale/WireGuard address where the overlay does the authenticating.
	Address string `toml:"address,omitempty"`
	// ApprovalPolicy is never|on-request ("" → the mode default).
	ApprovalPolicy string `toml:"approval_policy,omitempty"`
	// Config is a path to the server's own TOML config (e.g.
	// ../rs-gallium/configs/gemma4.toml), whose [llm]/[agent] tables klein
	// translates into the child's environment.
	//
	// Deprecated: use Env, or pass the file to the server itself via Args (e.g.
	// ["app-server", "--config", "…"]) if it accepts one. The translation behind
	// this field is a hand-written map of one server's config keys onto env vars,
	// so it falls behind that server every time the server grows an option —
	// which is exactly what happened. Still honored, and still layered under Env.
	Config string `toml:"config,omitempty"`
	// Env is passed to the spawned server verbatim, as a [appserver.env]
	// sub-table. This is the general way to reach a server option klein has never
	// heard of: klein is not the authority on any given server's option set, so
	// keys are neither validated nor resolved — a typo is silently ignored, and a
	// path value is whatever the server makes of it (notably NOT rebased onto a
	// config file's directory the way Config's modelPath is).
	Env map[string]string `toml:"env,omitempty"`
	// Args overrides the subcommand used to enter app-server mode.
	// Empty → ["app-server"], the protocol's conventional entry point.
	// Late by fieldalignment: the only one carrying a slice header.
	Args []string `toml:"args,omitempty"`
}

// EnvSlice renders the env table as sorted "KEY=VAL" entries, the shape the
// app-server client takes. Sorted so a spawn is reproducible: Go map order is
// randomized, and an env slice that shuffles between runs makes a failure that
// depends on override order impossible to reproduce.
func (s AppServerSettings) EnvSlice() []string {
	return envMapToSlice(s.Env)
}

// BashSettings contains bash tool configuration
type BashSettings struct {
	WhitelistedCommands []string `toml:"whitelisted_commands,omitempty"` // Commands that don't require approval
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

	if _, err := toml.Decode(string(data), s); err != nil {
		return fmt.Errorf("failed to parse settings: %w", err)
	}

	// Apply defaults for missing fields
	applyDefaults(s)
	return nil
}

// LoadSettings loads application settings from a TOML file
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
		warnAboutLegacyJSONSettings()
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

// The whole-agent app-server backend ids — the values settings.toml may name.
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

// findSettingsFile searches for settings.toml in order of preference:
// 1. .agents/settings.toml in current directory
// 2. $HOME/.klein/settings.toml
// Returns empty string if none found
func findSettingsFile() string {
	// Check .agents in current directory
	currentDirPath := filepath.Join(".agents", "settings.toml")
	if _, err := os.Stat(currentDirPath); err == nil {
		return currentDirPath
	}

	// Check $HOME/.klein
	homeDir, err := os.UserHomeDir()
	if err == nil {
		homeDirPath := filepath.Join(homeDir, ".klein", "settings.toml")
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

// warnAboutLegacyJSONSettings says something when a settings.json is sitting
// where a settings.toml should be.
//
// klein reads TOML now and does not read the old file — that is the intended
// change, not an oversight. But scaffolding a fresh default on top of someone's
// existing configuration without a word would look exactly like klein having
// forgotten it: same command, different backend, no explanation. One line costs
// nothing and turns a mystery into a chore.
func warnAboutLegacyJSONSettings() {
	for _, legacy := range legacySettingsPaths() {
		if _, err := os.Stat(legacy); err != nil {
			continue
		}
		pkgLogger.NewComponentLogger("settings").WarnWithIntention(
			pkgLogger.IntentionConfig,
			"found a settings.json from an older klein; settings are TOML now and this file is not read",
			"path", legacy,
			"hint", "port it to "+strings.TrimSuffix(legacy, ".json")+".toml",
		)
		return
	}
}

// legacySettingsPaths are the places the JSON settings file used to live.
func legacySettingsPaths() []string {
	paths := []string{filepath.Join(".agents", "settings.json")}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".klein", "settings.json"))
	}
	return paths
}

// createDefaultSettingsFile creates a default settings.toml file in ~/.klein/
func createDefaultSettingsFile() (*Settings, error) {
	// Determine where to create the file (prefer home directory)
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return GetDefaultSettings(), nil // Fall back to defaults without file creation
	}

	settingsPath := filepath.Join(homeDir, ".klein", "settings.toml")
	return createSettingsFileAtPath(settingsPath)
}

// defaultSettingsTemplate is the file a first run leaves behind.
//
// It is a written template rather than an encoded struct, which is the one real
// gain of the move to TOML: the file that appears in someone's home directory
// can explain itself. An encoder would emit every zero value with no hint what
// any of them mean.
const defaultSettingsTemplate = `# klein settings — see doc/CONFIGS.md for every field.
# Edit freely; klein only rewrites the [mcp.*] tables, and only via ` + "`klein mcp`" + `.

[llm]
backend = "%s"
model   = "%s"
# thinking   = true
# max_tokens = 8192
# effort     = "medium"   # reasoning models only

[agent]
max_iterations = %d
log_level      = "%s"

# MCP servers, one table each. Add them by hand or with ` + "`klein mcp add`" + `.
# [mcp.godoc]
# command = "godevmcp"
# args    = ["serve"]
`

// createSettingsFileAtPath creates a default settings file at the specified path
func createSettingsFileAtPath(settingsPath string) (*Settings, error) {
	// Create settings with file repository
	settings := NewSettingsWithPath(settingsPath)

	defaults := GetDefaultSettings()
	body := fmt.Sprintf(defaultSettingsTemplate,
		defaults.LLM.Backend, defaults.LLM.Model,
		defaults.Agent.MaxIterations, defaults.Agent.LogLevel)

	if err := settings.settingsRepository.Save([]byte(body)); err != nil {
		// Return defaults without repository if saving fails
		return GetDefaultSettings(), nil
	}

	// Log success message
	pkgLogger.NewComponentLogger("settings").InfoWithIntention(pkgLogger.IntentionConfig, "Created default settings file", "path", settingsPath)
	pkgLogger.NewComponentLogger("settings").InfoWithIntention(pkgLogger.IntentionStatus, "You can edit this file to customize your configuration")

	return settings, nil
}
