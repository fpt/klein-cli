package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// agentBackends are the whole-agent app-server backends. They own their own
// model and credentials, so config treats them alike: no inherited chat-model
// default, no required model, no required API key.
var agentBackends = []string{BackendCodex, BackendAppServer}

// TestLoadAgentBackendModelNotLeaked confirms a settings file with no model does
// not inherit the default chat model (the base Settings is seeded from
// defaults before unmarshal, so an omitted model must be cleared — these
// backends reject a chat model like gpt-5.6-luna).
func TestLoadAgentBackendModelNotLeaked(t *testing.T) {
	t.Parallel()
	for _, backend := range agentBackends {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			p := filepath.Join(dir, "settings.toml")
			body := "[llm]\nbackend = \"" + backend + "\"\nbase_dir = \"" + dir + "\"\n"
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
			p := filepath.Join(dir, "settings.toml")
			// Deliberately NOT the default model: the leak-clearing heuristic keys off
			// equality with the default, so an explicit model must differ to be testable.
			body := "[llm]\nbackend = \"" + backend + "\"\nmodel = \"gpt-5.6-sol\"\n"
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			s, err := LoadSettings(p)
			if err != nil {
				t.Fatal(err)
			}
			if s.LLM.Model != "gpt-5.6-sol" {
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

// TestAgentBackendDefault confirms `-b codex` / `-b appserver` resolve to that
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

// TestAppServerSettingsRoundTrip confirms the appserver block parses from TOML — a
// wrong tag would silently leave appserver.command empty, which the backend
// rejects at startup.
//
//nolint:staticcheck // SA1019: appserver.config is deprecated but still honored, so still parsed.
func TestAppServerSettingsRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.toml")
	body := "[llm]\nbackend = \"" + BackendAppServer + `"

[appserver]
command = "/opt/gallium"
args = ["serve"]
config = "/etc/agent.toml"
approval_policy = "on-request"

[appserver.env]
GALLIUM_CPU_MOE = "1"
GALLIUM_GPU_LAYERS = "20"
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.AppServer.Command != "/opt/gallium" {
		t.Errorf("command: got %q", s.AppServer.Command)
	}
	if len(s.AppServer.Args) != 1 || s.AppServer.Args[0] != "serve" {
		t.Errorf("args: got %v", s.AppServer.Args)
	}
	if s.AppServer.Config != "/etc/agent.toml" {
		t.Errorf("config: got %q", s.AppServer.Config)
	}
	if s.AppServer.ApprovalPolicy != "on-request" {
		t.Errorf("approval_policy: got %q", s.AppServer.ApprovalPolicy)
	}
	// The env sub-table must survive as a map and render sorted — this is the
	// on-disk contract for reaching a server option klein has no field for.
	wantEnv := []string{"GALLIUM_CPU_MOE=1", "GALLIUM_GPU_LAYERS=20"}
	if got := s.AppServer.EnvSlice(); !slices.Equal(got, wantEnv) {
		t.Errorf("env: got %v, want %v", got, wantEnv)
	}
}

// TestAppServerAddressRoundTrip confirms appserver.address parses. A wrong tag
// would leave it empty and klein would try to spawn a binary that is not there,
// blaming the wrong machine.
func TestAppServerAddressRoundTrip(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "settings.toml")
	body := "[llm]\nbackend = \"" + BackendAppServer + `"

[appserver]
address = "gpubox:4711"
approval_policy = "on-request"
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.AppServer.Address != "gpubox:4711" {
		t.Errorf("address: got %q", s.AppServer.Address)
	}
	// Nothing is spawned in this shape, so command stays empty rather than
	// acquiring a default.
	if s.AppServer.Command != "" {
		t.Errorf("command: got %q, want empty", s.AppServer.Command)
	}
}

// loadCodexBlock writes a settings file carrying codexTOML (the [codex] tables,
// written out in full) and returns the parsed block.
func loadCodexBlock(t *testing.T, codexTOML string) CodexSettings {
	t.Helper()
	p := filepath.Join(t.TempDir(), "settings.toml")
	body := "[llm]\nbackend = \"" + BackendCodex + "\"\n\n" + codexTOML
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	return s.Codex
}

// wantBool asserts an optional bool was parsed (not left nil) and holds want.
// Checking through the pointer matters: a wrong json tag leaves it nil, which a
// plain value comparison would read as "the user said false".
func wantBool(t *testing.T, field string, got *bool, want bool) {
	t.Helper()
	switch {
	case got == nil:
		t.Errorf("%s: not parsed (nil)", field)
	case *got != want:
		t.Errorf("%s: got %v, want %v", field, *got, want)
	}
}

// These tags mirror codex's own config keys, so a wrong one silently drops the
// setting: the launch line simply omits the override and the backend runs with a
// sandbox the user thought they had changed.
func TestSandboxWorkspaceWriteRoundTrip(t *testing.T) {
	t.Parallel()
	c := loadCodexBlock(t, `[codex.sandbox_workspace_write]
network_access = true
exclude_tmpdir_env_var = false
exclude_slash_tmp = true
writable_roots = ["/srv/cache"]
`)

	sw := c.SandboxWorkspaceWrite
	wantBool(t, "network_access", sw.NetworkAccess, true)
	wantBool(t, "exclude_tmpdir_env_var", sw.ExcludeTmpdirEnvVar, false)
	wantBool(t, "exclude_slash_tmp", sw.ExcludeSlashTmp, true)
	if len(sw.WritableRoots) != 1 || sw.WritableRoots[0] != "/srv/cache" {
		t.Errorf("writable_roots: got %v", sw.WritableRoots)
	}
}

func TestShellEnvironmentPolicyRoundTrip(t *testing.T) {
	t.Parallel()
	c := loadCodexBlock(t, `[codex.shell_environment_policy]
inherit = "all"
ignore_default_excludes = true
exclude = ["AWS_*"]
include_only = ["PATH"]

[codex.shell_environment_policy.set]
GH_TOKEN = "t"
`)

	sep := c.ShellEnvironmentPolicy
	if sep.Inherit != "all" {
		t.Errorf("inherit: got %q", sep.Inherit)
	}
	wantBool(t, "ignore_default_excludes", sep.IgnoreDefaultExcludes, true)
	if len(sep.Exclude) != 1 || sep.Exclude[0] != "AWS_*" {
		t.Errorf("exclude: got %v", sep.Exclude)
	}
	if len(sep.IncludeOnly) != 1 || sep.IncludeOnly[0] != "PATH" {
		t.Errorf("include_only: got %v", sep.IncludeOnly)
	}
	if sep.Set["GH_TOKEN"] != "t" {
		t.Errorf("set: got %v", sep.Set)
	}
}

// An absent table must leave every field unset, so klein passes no override for
// it and the user's own ~/.codex/config.toml stays in charge.
func TestCodexConfigTablesDefaultToUnset(t *testing.T) {
	t.Parallel()
	c := loadCodexBlock(t, "")

	if c.SandboxWorkspaceWrite.NetworkAccess != nil {
		t.Errorf("network_access should be unset, got %v", *c.SandboxWorkspaceWrite.NetworkAccess)
	}
	if c.ShellEnvironmentPolicy.Inherit != "" {
		t.Errorf("inherit should be unset, got %q", c.ShellEnvironmentPolicy.Inherit)
	}
}

// TestValidateRejectsUnknownBackend guards the backend allowlist.
func TestValidateRejectsUnknownBackend(t *testing.T) {
	t.Parallel()
	s := GetDefaultSettings()
	s.LLM = LLMSettings{Backend: "acpp", Model: "x"} // typo
	if err := ValidateSettings(s); err == nil {
		t.Error("expected an error for an unknown backend")
	}
}

// TestValidateRejectsRenamedACPBackend confirms the removed `acp` id is not
// silently aliased to BackendAppServer, and that the error names the new id —
// an existing settings.toml is otherwise valid-looking JSON that would just stop
// working with no hint why.
func TestValidateRejectsRenamedACPBackend(t *testing.T) {
	t.Parallel()
	s := GetDefaultSettings()
	s.LLM = LLMSettings{Backend: backendACPRemoved}
	err := ValidateSettings(s)
	if err == nil {
		t.Fatal("expected an error for the renamed acp backend")
	}
	if !strings.Contains(err.Error(), BackendAppServer) {
		t.Errorf("error must name the new backend id, got: %v", err)
	}
}
