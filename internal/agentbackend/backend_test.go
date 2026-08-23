package agentbackend

import (
	"reflect"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/pkg/agentserver"
)

// Stand-ins for a real app-server binary; "appserver" names a protocol, so these
// tests only care that whatever path is configured is the one that comes back.
const (
	appServerBin     = "gallium"
	appServerBinPath = "/opt/gallium"
	codexBinPath     = "/opt/codex"
	// An app-server somewhere else; never dialed, only ever configured.
	dialedAddress = "10.0.0.2:4711"
	// The settings that configure a server klein spawns, by their TOML names.
	fieldArgs   = "args"
	fieldEnv    = "env"
	fieldConfig = "config"
)

// TestDialectFor confirms only codex is driven as codex. Everything else — a
// conforming server, an id this build has never heard of — is driven through the
// protocol subset alone, which is what makes the subset a contract rather than a
// list of programs klein recognizes.
func TestDialectFor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		backend string
		want    agentserver.Dialect
	}{
		{config.BackendCodex, agentserver.DialectCodex},
		{config.BackendAppServer, agentserver.DialectGeneric},
		{"some-future-server", agentserver.DialectGeneric},
		{"", agentserver.DialectGeneric},
	} {
		if got := dialectFor(tc.backend); got != tc.want {
			t.Errorf("dialectFor(%q) = %v, want %v", tc.backend, got, tc.want)
		}
	}
}

// TestCommandDefaults confirms codex falls back to its binary name on PATH and
// that the app-server subcommand is the default entry point for both backends.
func TestCommandDefaults(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		backend  string
		command  string
		wantPath string
	}{
		{backend: config.BackendCodex, wantPath: config.BackendCodex},
		{backend: config.BackendAppServer, command: appServerBin, wantPath: appServerBin},
	} {
		s := &config.Settings{}
		s.LLM.Backend = tc.backend
		s.AppServer.Command = tc.command

		path, args, err := command(s)
		if err != nil {
			t.Fatalf("%s: %v", tc.backend, err)
		}
		if path != tc.wantPath {
			t.Errorf("%s: path = %q, want %q", tc.backend, path, tc.wantPath)
		}
		if !reflect.DeepEqual(args, []string{"app-server"}) {
			t.Errorf("%s: args = %v", tc.backend, args)
		}
	}
}

// TestCommandAppServerRequiresCommand confirms the generic backend refuses to
// guess a binary: "appserver" names a protocol, not an implementation, so an
// unset appserver.command must fail loudly rather than spawn something arbitrary.
func TestCommandAppServerRequiresCommand(t *testing.T) {
	t.Parallel()
	s := &config.Settings{}
	s.LLM.Backend = config.BackendAppServer

	if _, _, err := command(s); err == nil {
		t.Fatal("expected an error when appserver.command is unset")
	}
}

// TestCommandAppServerArgsOverride confirms a server that spells its app-server mode
// differently can say so.
func TestCommandAppServerArgsOverride(t *testing.T) {
	t.Parallel()
	s := &config.Settings{}
	s.LLM.Backend = config.BackendAppServer
	s.AppServer.Command = "some-agent"
	s.AppServer.Args = []string{"serve", "--rpc"}

	_, args, err := command(s)
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if !reflect.DeepEqual(args, []string{"serve", "--rpc"}) {
		t.Errorf("args = %v, want the configured override", args)
	}
}

// TestCommandExplicitPaths confirms a configured binary path overrides the
// PATH lookup, and that each backend reads its own settings block.
func TestCommandExplicitPaths(t *testing.T) {
	t.Parallel()

	codexSettings := &config.Settings{}
	codexSettings.LLM.Backend = config.BackendCodex
	codexSettings.Codex.CodexPath = codexBinPath
	codexSettings.AppServer.Command = appServerBinPath // must be ignored

	if path, _, err := command(codexSettings); err != nil || path != codexBinPath {
		t.Errorf("codex path = %q, err = %v", path, err)
	}

	appServerSettings := &config.Settings{}
	appServerSettings.LLM.Backend = config.BackendAppServer
	appServerSettings.Codex.CodexPath = codexBinPath // must be ignored
	appServerSettings.AppServer.Command = appServerBinPath

	if path, _, err := command(appServerSettings); err != nil || path != appServerBinPath {
		t.Errorf("appserver path = %q, err = %v", path, err)
	}
}

// TestCommandRejectsChatBackend confirms a chat backend never resolves to an
// app-server binary — Start/Select gate on IsAgentBackend, and command() is the
// backstop if that gate is ever bypassed.
func TestCommandRejectsChatBackend(t *testing.T) {
	t.Parallel()
	s := &config.Settings{}
	s.LLM.Backend = "openai"

	if _, _, err := command(s); err == nil {
		t.Fatal("expected an error for a non-app-server backend")
	}
}

// TestApprovalPolicyPrefersBackendBlock confirms each backend reads approval
// policy from its own settings block, falling back to the mode default.
func TestApprovalPolicyPrefersBackendBlock(t *testing.T) {
	t.Parallel()

	// Explicit setting wins over the mode default.
	appSrv := &config.Settings{}
	appSrv.LLM.Backend = config.BackendAppServer
	appSrv.AppServer.ApprovalPolicy = agentserver.ApprovalOnRequest
	got := approvalPolicy(appSrv, RunnerOptions{ApprovalPolicy: agentserver.ApprovalNever})
	if got != agentserver.ApprovalOnRequest {
		t.Errorf("appserver explicit policy: got %q", got)
	}

	// Absent setting falls back to the mode default.
	appSrv.AppServer.ApprovalPolicy = ""
	got = approvalPolicy(appSrv, RunnerOptions{ApprovalPolicy: agentserver.ApprovalNever})
	if got != agentserver.ApprovalNever {
		t.Errorf("appserver default policy: got %q", got)
	}

	// An appserver thread must not pick up codex's policy.
	appSrv.Codex.ApprovalPolicy = agentserver.ApprovalOnRequest
	got = approvalPolicy(appSrv, RunnerOptions{ApprovalPolicy: agentserver.ApprovalNever})
	if got != agentserver.ApprovalNever {
		t.Errorf("appserver must ignore codex.approval_policy: got %q", got)
	}

	// ...and codex still reads its own.
	codex := &config.Settings{}
	codex.LLM.Backend = config.BackendCodex
	codex.Codex.ApprovalPolicy = agentserver.ApprovalOnRequest
	got = approvalPolicy(codex, RunnerOptions{ApprovalPolicy: agentserver.ApprovalNever})
	if got != agentserver.ApprovalOnRequest {
		t.Errorf("codex explicit policy: got %q", got)
	}
}

// TestDialAddressIsAppServerOnly confirms only the generic backend can name an
// address: codex is a local CLI klein launches, and would silently ignore one.
func TestDialAddressIsAppServerOnly(t *testing.T) {
	t.Parallel()

	appServer := &config.Settings{}
	appServer.LLM.Backend = config.BackendAppServer
	appServer.AppServer.Address = dialedAddress
	if got := dialAddress(appServer); got != dialedAddress {
		t.Errorf("appserver address = %q", got)
	}

	codex := &config.Settings{}
	codex.LLM.Backend = config.BackendCodex
	codex.AppServer.Address = dialedAddress // belongs to the other block
	if got := dialAddress(codex); got != "" {
		t.Errorf("codex must not dial: got %q", got)
	}
}

// TestValidateDialedAppServerRejectsSpawnSettings is the guard against the
// quietest way this feature could go wrong: a user sets appserver.env believing
// they picked a model, when the process reading it runs on another machine and
// was configured there. Silence would look like success.
func TestValidateDialedAppServerRejectsSpawnSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		want     string
		settings config.AppServerSettings
	}{
		{name: argCommand, want: argCommand, settings: config.AppServerSettings{Command: appServerBin}},
		{name: fieldArgs, want: fieldArgs, settings: config.AppServerSettings{
			Args: []string{appServerSubcommand},
		}},
		{name: fieldEnv, want: fieldEnv, settings: config.AppServerSettings{
			Env: map[string]string{"MODEL_PATH": "/m.gguf"},
		}},
		{name: fieldConfig, want: fieldConfig, settings: config.AppServerSettings{
			Config: "/etc/agent.toml",
		}},
		{name: "address alone", want: "", settings: config.AppServerSettings{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := tc.settings
			s.Address = dialedAddress

			err := validateDialedAppServer(s)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want an error naming appserver.%s", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
			// It must also name the address, or the user cannot tell which half
			// of the configuration to drop.
			if !strings.Contains(err.Error(), s.Address) {
				t.Errorf("error %q does not name the address", err)
			}
		})
	}
}
