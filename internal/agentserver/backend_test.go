package agentserver

import (
	"reflect"
	"testing"

	"github.com/fpt/klein-cli/internal/config"
)

// TestIsAgentBackend confirms only the whole-agent backends are recognized;
// chat backends must keep running through klein's own ReAct loop.
func TestIsAgentBackend(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		backend string
		want    bool
	}{
		{"codex", true},
		{"kessel", true},
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

// TestCommandDefaults confirms each backend falls back to its binary name on
// PATH, and that both are launched with the `app-server` subcommand.
func TestCommandDefaults(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		backend  string
		wantPath string
	}{
		{"codex", "codex"},
		{"kessel", "kessel-cli"},
	} {
		s := &config.Settings{}
		s.LLM.Backend = tc.backend

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

// TestCommandExplicitPaths confirms a configured binary path overrides the
// PATH lookup, and that each backend reads its own settings block.
func TestCommandExplicitPaths(t *testing.T) {
	t.Parallel()

	codexSettings := &config.Settings{}
	codexSettings.LLM.Backend = "codex"
	codexSettings.Codex.CodexPath = "/opt/codex"
	codexSettings.Kessel.KesselPath = "/opt/kessel" // must be ignored

	if path, _, err := command(codexSettings); err != nil || path != "/opt/codex" {
		t.Errorf("codex path = %q, err = %v", path, err)
	}

	kesselSettings := &config.Settings{}
	kesselSettings.LLM.Backend = "kessel"
	kesselSettings.Codex.CodexPath = "/opt/codex" // must be ignored
	kesselSettings.Kessel.KesselPath = "/opt/kessel"

	if path, _, err := command(kesselSettings); err != nil || path != "/opt/kessel" {
		t.Errorf("kessel path = %q, err = %v", path, err)
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
	kessel := &config.Settings{}
	kessel.LLM.Backend = "kessel"
	kessel.Kessel.ApprovalPolicy = "on-request"
	if got := approvalPolicy(kessel, RunnerOptions{ApprovalPolicy: ApprovalNever}); got != "on-request" {
		t.Errorf("kessel explicit policy: got %q", got)
	}

	// Absent setting falls back to the mode default.
	kessel.Kessel.ApprovalPolicy = ""
	if got := approvalPolicy(kessel, RunnerOptions{ApprovalPolicy: ApprovalNever}); got != ApprovalNever {
		t.Errorf("kessel default policy: got %q", got)
	}

	// A kessel thread must not pick up codex's policy.
	kessel.Codex.ApprovalPolicy = "on-request"
	if got := approvalPolicy(kessel, RunnerOptions{ApprovalPolicy: ApprovalNever}); got != ApprovalNever {
		t.Errorf("kessel must ignore codex.approval_policy: got %q", got)
	}

	// ...and codex still reads its own.
	codex := &config.Settings{}
	codex.LLM.Backend = "codex"
	codex.Codex.ApprovalPolicy = "on-request"
	if got := approvalPolicy(codex, RunnerOptions{ApprovalPolicy: ApprovalNever}); got != "on-request" {
		t.Errorf("codex explicit policy: got %q", got)
	}
}
