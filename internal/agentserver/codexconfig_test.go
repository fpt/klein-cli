package agentserver

import (
	"slices"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/internal/config"
)

// netAccessOn is the override the gh case turns on, and the one most of these
// tests reach for.
const netAccessOn = "sandbox_workspace_write.network_access=true"

func ptr[T any](v T) *T { return &v }

// codexWith returns a codex-backend Settings with c as its codex block.
func codexWith(c config.CodexSettings) *config.Settings {
	s := &config.Settings{}
	s.LLM.Backend = config.BackendCodex
	s.Codex = c
	return s
}

// The gh case: workspace-write keeps the file restrictions, network_access
// lifts the one thing blocking a tool from reaching the network, and inherit
// lets the token the shell exported through.
func TestCodexConfigArgs_NetworkAndInherit(t *testing.T) {
	t.Parallel()

	got := codexConfigArgs(config.CodexSettings{
		SandboxWorkspaceWrite:  config.SandboxWorkspaceWriteSettings{NetworkAccess: ptr(true)},
		ShellEnvironmentPolicy: config.ShellEnvironmentPolicySettings{Inherit: "all"},
	})
	want := []string{
		"-c", netAccessOn,
		"-c", `shell_environment_policy.inherit="all"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("codexConfigArgs =\n  %q\nwant\n  %q", got, want)
	}
}

// An unset field must produce no override at all. Emitting codex's own default
// would silently overrule whatever the user's ~/.codex/config.toml says.
func TestCodexConfigArgs_UnsetFieldsEmitNothing(t *testing.T) {
	t.Parallel()

	if got := codexConfigArgs(config.CodexSettings{}); len(got) != 0 {
		t.Errorf("an empty codex block should add no arguments, got %q", got)
	}
	// A codex block with only the thread-scoped fields set is still empty here:
	// those travel on thread/start, not the launch line.
	only := config.CodexSettings{CodexPath: "/x", SandboxMode: "read-only", ApprovalPolicy: "never"}
	if got := codexConfigArgs(only); len(got) != 0 {
		t.Errorf("thread-scoped fields must not become -c overrides, got %q", got)
	}
}

// false is a value the user chose, and must be distinguishable from unset —
// which is the whole reason these fields are pointers.
func TestCodexConfigArgs_ExplicitFalseIsEmitted(t *testing.T) {
	t.Parallel()

	got := codexConfigArgs(config.CodexSettings{
		SandboxWorkspaceWrite: config.SandboxWorkspaceWriteSettings{NetworkAccess: ptr(false)},
	})
	want := []string{"-c", "sandbox_workspace_write.network_access=false"}
	if !slices.Equal(got, want) {
		t.Errorf("codexConfigArgs = %q, want %q", got, want)
	}
}

func TestCodexConfigArgs_AllFields(t *testing.T) {
	t.Parallel()

	got := codexConfigArgs(config.CodexSettings{
		SandboxWorkspaceWrite: config.SandboxWorkspaceWriteSettings{
			NetworkAccess:       ptr(true),
			ExcludeTmpdirEnvVar: ptr(true),
			ExcludeSlashTmp:     ptr(false),
			WritableRoots:       []string{"/srv/cache", "/opt/build"},
		},
		ShellEnvironmentPolicy: config.ShellEnvironmentPolicySettings{
			Inherit:               "core",
			IgnoreDefaultExcludes: ptr(true),
			Exclude:               []string{"AWS_*"},
			IncludeOnly:           []string{"PATH", "HOME"},
			Set:                   map[string]string{"GH_TOKEN": "t", "CI": "1"},
		},
	})
	want := []string{
		"-c", netAccessOn,
		"-c", "sandbox_workspace_write.exclude_tmpdir_env_var=true",
		"-c", "sandbox_workspace_write.exclude_slash_tmp=false",
		"-c", `sandbox_workspace_write.writable_roots=["/srv/cache","/opt/build"]`,
		"-c", `shell_environment_policy.inherit="core"`,
		"-c", "shell_environment_policy.ignore_default_excludes=true",
		"-c", `shell_environment_policy.exclude=["AWS_*"]`,
		"-c", `shell_environment_policy.include_only=["PATH","HOME"]`,
		// set is emitted in sorted key order, not map order.
		"-c", `shell_environment_policy.set.CI="1"`,
		"-c", `shell_environment_policy.set.GH_TOKEN="t"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("codexConfigArgs =\n  %q\nwant\n  %q", got, want)
	}
}

// A map's iteration order is random; an argument list that reshuffles between
// launches would make the child's configuration irreproducible.
func TestCodexConfigArgs_SetOrderIsStable(t *testing.T) {
	t.Parallel()

	c := config.CodexSettings{ShellEnvironmentPolicy: config.ShellEnvironmentPolicySettings{
		Set: map[string]string{"D": "4", "A": "1", "C": "3", "B": "2"},
	}}
	first := codexConfigArgs(c)
	for range 20 {
		if got := codexConfigArgs(c); !slices.Equal(got, first) {
			t.Fatalf("argument order is not stable:\n  %q\n  %q", first, got)
		}
	}
}

// A value carrying a quote or a backslash has to survive as a TOML string.
// codex falls back to treating an unparseable value as a literal, so getting
// this wrong corrupts the setting quietly rather than failing.
func TestCodexConfigArgs_EscapesAwkwardValues(t *testing.T) {
	t.Parallel()

	got := codexConfigArgs(config.CodexSettings{
		SandboxWorkspaceWrite: config.SandboxWorkspaceWriteSettings{
			WritableRoots: []string{`C:\build`},
		},
		ShellEnvironmentPolicy: config.ShellEnvironmentPolicySettings{
			Set: map[string]string{"MSG": `say "hi"` + "\n"},
		},
	})
	want := []string{
		"-c", `sandbox_workspace_write.writable_roots=["C:\\build"]`,
		"-c", `shell_environment_policy.set.MSG="say \"hi\"\n"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("codexConfigArgs =\n  %q\nwant\n  %q", got, want)
	}
}

// The overrides ride on the codex launch line, after the subcommand.
func TestCommand_AppendsCodexConfigArgs(t *testing.T) {
	t.Parallel()

	s := codexWith(config.CodexSettings{
		SandboxWorkspaceWrite: config.SandboxWorkspaceWriteSettings{NetworkAccess: ptr(true)},
	})
	path, args, err := command(s)
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	want := []string{appServerSubcommand, "-c", netAccessOn}
	if path != "codex" || !slices.Equal(args, want) {
		t.Fatalf("command = %q %q, want codex %q", path, args, want)
	}
}

// defaultAppServerArgs is package state shared by every call; appending to it in
// place would leak one launch's overrides into the next.
func TestCommand_OverridesDoNotLeakIntoTheDefaultArgs(t *testing.T) {
	t.Parallel()

	withNet := codexWith(config.CodexSettings{
		SandboxWorkspaceWrite: config.SandboxWorkspaceWriteSettings{NetworkAccess: ptr(true)},
	})
	if _, _, err := command(withNet); err != nil {
		t.Fatalf("command: %v", err)
	}
	_, args, err := command(codexWith(config.CodexSettings{}))
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if !slices.Equal(args, []string{appServerSubcommand}) {
		t.Fatalf("a later launch inherited an earlier override: %q", args)
	}
}

// The generic backend has no codex config block, so its launch line is untouched
// even when a codex block happens to be present in the same settings file.
func TestCommand_AppServerBackendIgnoresCodexConfig(t *testing.T) {
	t.Parallel()

	s := &config.Settings{}
	s.LLM.Backend = config.BackendAppServer
	s.AppServer.Command = "/opt/gallium"
	s.Codex.SandboxWorkspaceWrite.NetworkAccess = ptr(true)

	_, args, err := command(s)
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if !slices.Equal(args, []string{appServerSubcommand}) {
		t.Fatalf("appserver launch line picked up codex config: %q", args)
	}
}

// codex validates inherit at startup and exits with "unknown variant", which
// reaches the user as a spawn failure naming neither the setting nor the file.
func TestCommand_RejectsBadInheritMode(t *testing.T) {
	t.Parallel()

	s := codexWith(config.CodexSettings{
		ShellEnvironmentPolicy: config.ShellEnvironmentPolicySettings{Inherit: "everything"},
	})
	_, _, err := command(s)
	if err == nil {
		t.Fatal("an inherit mode codex rejects should not reach the launch line")
	}
	// The message has to name the setting and the accepted values, since the
	// point is to say what codex's own error does not.
	for _, want := range []string{"shell_environment_policy.inherit", "everything", "core", "all", "none"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

func TestCommand_AcceptsEveryInheritModeCodexAccepts(t *testing.T) {
	t.Parallel()

	for _, mode := range config.ShellEnvironmentInheritModes {
		s := codexWith(config.CodexSettings{
			ShellEnvironmentPolicy: config.ShellEnvironmentPolicySettings{Inherit: mode},
		})
		if _, _, err := command(s); err != nil {
			t.Errorf("inherit=%q should be accepted: %v", mode, err)
		}
	}
}
