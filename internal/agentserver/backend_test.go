package agentserver

import (
	"reflect"
	"testing"

	"github.com/fpt/klein-cli/internal/config"
)

// Stand-ins for a real app-server binary; "appserver" names a protocol, so these
// tests only care that whatever path is configured is the one that comes back.
const (
	appServerBin     = "gallium"
	appServerBinPath = "/opt/gallium"
	codexBinPath     = "/opt/codex"
)

// TestIsAgentBackend confirms only the whole-agent backends are recognized;
// chat backends must keep running through klein's own ReAct loop.
func TestIsAgentBackend(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		backend string
		want    bool
	}{
		{BackendCodex, true},
		{BackendAppServer, true},
		{"openai", false},
		{"anthropic", false},
		{"gemini", false},
		{"", false},
	} {
		if got := IsAgentBackend(tc.backend); got != tc.want {
			t.Errorf("IsAgentBackend(%q) = %v, want %v", tc.backend, got, tc.want)
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
		{backend: BackendCodex, wantPath: BackendCodex},
		{backend: BackendAppServer, command: appServerBin, wantPath: appServerBin},
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
	s.LLM.Backend = BackendAppServer

	if _, _, err := command(s); err == nil {
		t.Fatal("expected an error when appserver.command is unset")
	}
}

// TestCommandAppServerArgsOverride confirms a server that spells its app-server mode
// differently can say so.
func TestCommandAppServerArgsOverride(t *testing.T) {
	t.Parallel()
	s := &config.Settings{}
	s.LLM.Backend = BackendAppServer
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
	codexSettings.LLM.Backend = BackendCodex
	codexSettings.Codex.CodexPath = codexBinPath
	codexSettings.AppServer.Command = appServerBinPath // must be ignored

	if path, _, err := command(codexSettings); err != nil || path != codexBinPath {
		t.Errorf("codex path = %q, err = %v", path, err)
	}

	appServerSettings := &config.Settings{}
	appServerSettings.LLM.Backend = BackendAppServer
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
	appSrv.LLM.Backend = BackendAppServer
	appSrv.AppServer.ApprovalPolicy = ApprovalOnRequest
	if got := approvalPolicy(appSrv, RunnerOptions{ApprovalPolicy: ApprovalNever}); got != ApprovalOnRequest {
		t.Errorf("appserver explicit policy: got %q", got)
	}

	// Absent setting falls back to the mode default.
	appSrv.AppServer.ApprovalPolicy = ""
	if got := approvalPolicy(appSrv, RunnerOptions{ApprovalPolicy: ApprovalNever}); got != ApprovalNever {
		t.Errorf("appserver default policy: got %q", got)
	}

	// An appserver thread must not pick up codex's policy.
	appSrv.Codex.ApprovalPolicy = ApprovalOnRequest
	if got := approvalPolicy(appSrv, RunnerOptions{ApprovalPolicy: ApprovalNever}); got != ApprovalNever {
		t.Errorf("appserver must ignore codex.approval_policy: got %q", got)
	}

	// ...and codex still reads its own.
	codex := &config.Settings{}
	codex.LLM.Backend = BackendCodex
	codex.Codex.ApprovalPolicy = ApprovalOnRequest
	if got := approvalPolicy(codex, RunnerOptions{ApprovalPolicy: ApprovalNever}); got != ApprovalOnRequest {
		t.Errorf("codex explicit policy: got %q", got)
	}
}

// TestProbeReadySkipsNonCodex confirms the codex account probe never runs for
// the generic backend. `account/read` is outside the protocol subset klein requires,
// so probing it would reject a conforming server that has no account concept.
// A nil client is the assertion: reaching the RPC call at all would panic.
func TestProbeReadySkipsNonCodex(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{BackendAppServer, "some-other-agent", ""} {
		if err := probeReady(t.Context(), nil, backend); err != nil {
			t.Errorf("probeReady(%q) = %v, want nil (no probe for non-codex)", backend, err)
		}
	}
}
