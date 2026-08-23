package agentbackend

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/pkg/agentserver"
)

// The names a server resolves against its own built-ins. They are a contract
// with the backend, not klein's to rename freely: a server matching a call to
// the first exact name match will run klein's tool in place of the one it
// shipped with only if the spelling agrees, and models emit these spellings from
// habit.
var conventionalToolNames = []string{
	toolBash, toolEdit, toolGlob, toolGrep, "LS", toolMultiEdit, toolRead, toolWrite,
}

// workspaceHost returns the workspace tools as the backend sees them.
func workspaceHost(t *testing.T, workingDir string) agentserver.DynamicTools {
	t.Helper()
	settings := config.NewSettings()
	return newToolHost(newWorkspaceTools(settings, workingDir))
}

// specNames returns the offered tool names, sorted.
func specNames(tools agentserver.DynamicTools) []string {
	specs := tools.Specs()
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	slices.Sort(names)
	return names
}

// The whole feature rests on the spelling. A rename here — or a manager dropped
// from the set — leaves the model calling a tool nothing serves, on a backend
// that has switched its own off.
func TestNewWorkspaceTools_OffersTheConventionalNames(t *testing.T) {
	t.Parallel()

	got := specNames(workspaceHost(t, t.TempDir()))
	if !slices.Equal(got, conventionalToolNames) {
		t.Errorf("offered %v, want %v", got, conventionalToolNames)
	}
}

// Every offered tool needs a description: it is the only thing telling a model
// what the tool is for, and an empty one is indistinguishable from a tool that
// does nothing.
func TestNewWorkspaceTools_EveryToolDescribesItself(t *testing.T) {
	t.Parallel()

	for _, spec := range workspaceHost(t, t.TempDir()).Specs() {
		if strings.TrimSpace(spec.Description) == "" {
			t.Errorf("%s is offered with no description", spec.Name)
		}
	}
}

// MultiEdit takes an array of edit objects, and the element schema is the only
// thing that says what an element looks like. It used to be dropped on the way
// to the backend, leaving a bare {"type":"array"} and a model guessing at field
// names — the failure looks like a model that cannot use the tool.
func TestNewWorkspaceTools_MultiEditCarriesItsElementSchema(t *testing.T) {
	t.Parallel()

	var edits *agentserver.Parameter
	for _, spec := range workspaceHost(t, t.TempDir()).Specs() {
		if spec.Name != "MultiEdit" {
			continue
		}
		for i, param := range spec.Parameters {
			if param.Name == "edits" {
				edits = &spec.Parameters[i]
			}
		}
	}
	if edits == nil {
		t.Fatal("MultiEdit is offered without its edits parameter")
	}
	items, ok := edits.Schema["items"].(map[string]any)
	if !ok {
		t.Fatalf("edits carries no element schema: %+v", edits.Schema)
	}
	props, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("the element schema describes no properties: %+v", items)
	}
	// The three fields an edit cannot be made without.
	for _, field := range []string{"file_path", "old_string", "new_string"} {
		if _, ok := props[field]; !ok {
			t.Errorf("an edit element does not describe %q", field)
		}
	}
}

// The tools have to act where klein is, not where the model is reasoning: a
// write goes to klein's working directory, and a read comes back from it.
func TestNewWorkspaceTools_ActOnTheWorkingDirectory(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	host := workspaceHost(t, workingDir)
	ctx := context.Background()

	target := filepath.Join(workingDir, "notes.txt")
	if _, err := host.Call(ctx, toolWrite, map[string]any{
		argFilePath: target,
		"content":   "written by klein\n",
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// On klein's disk, not described back to a model that then believes it.
	onDisk, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("the file the tool reported writing is not there: %v", err)
	}
	if !strings.Contains(string(onDisk), "written by klein") {
		t.Errorf("file contains %q", onDisk)
	}

	out, err := host.Call(ctx, toolRead, map[string]any{argFilePath: target})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(out, "written by klein") {
		t.Errorf("Read returned %q", out)
	}
}

// klein's allowlist is the boundary, and it is klein's precisely because these
// tools run here. A path outside the working directory is refused whoever asked
// for it — the backend has no more reach than the native loop does.
func TestNewWorkspaceTools_RefuseAPathOutsideTheWorkingDirectory(t *testing.T) {
	t.Parallel()

	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	if err := os.WriteFile(outside, []byte("not yours\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	host := workspaceHost(t, t.TempDir())
	ctx := context.Background()
	if out, err := host.Call(ctx, toolRead, map[string]any{argFilePath: outside}); err == nil {
		t.Errorf("reading outside the working directory succeeded: %q", out)
	}

	// Search is the same boundary by another route, and the easier one to leave
	// open: Glob and Grep hand their path to rg/find, which answer about
	// anywhere. A Grep that can report what is in a file Read refuses discloses
	// it just the same.
	outsideDir := filepath.Dir(outside)
	for _, tc := range []struct {
		args map[string]any
		tool string
	}{
		{map[string]any{"pattern": "not yours", "path": outsideDir}, toolGrep},
		{map[string]any{"pattern": "*.txt", "path": outsideDir}, toolGlob},
	} {
		if out, err := host.Call(ctx, tc.tool, tc.args); err == nil {
			t.Errorf("%s searched outside the working directory: %q", tc.tool, out)
		}
	}
}

// recordingTools is a DynamicTools that records what it was asked to run, so a
// test can tell "declined" from "ran anyway".
type recordingTools struct{ called []string }

func (r *recordingTools) Specs() []agentserver.ToolSpec {
	return []agentserver.ToolSpec{{Name: toolBash, Description: "run a command"}}
}

func (r *recordingTools) Call(_ context.Context, name string, _ map[string]any) (string, error) {
	r.called = append(r.called, name)
	return "ran " + name, nil
}

// Which tools are put to the approver, and which are not. Read-only tools are
// deliberately absent: klein has never prompted to read a file, and prompting
// here would be a new interruption rather than a preserved one. klein's own
// stores are absent for the same reason they always were.
func TestWithApproval_AsksAboutMutationOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tool string
		want bool
	}{
		{toolBash, true},
		{toolWrite, true},
		{toolEdit, true},
		{toolMultiEdit, true},
		{toolRead, false},
		{"LS", false},
		{toolGlob, false},
		{toolGrep, false},
		{"MemorySearch", false},
	}
	for _, tc := range tests {
		t.Run(tc.tool, func(t *testing.T) {
			t.Parallel()
			asked := false
			approver := recordingApprover(func(agentserver.ApprovalRequest) bool {
				asked = true
				return true
			})
			inner := &recordingTools{}

			if _, err := withApproval(inner, approver).Call(context.Background(), tc.tool, nil); err != nil {
				t.Fatalf("Call: %v", err)
			}
			if asked != tc.want {
				t.Errorf("asked = %v, want %v", asked, tc.want)
			}
			if len(inner.called) != 1 {
				t.Errorf("an approved tool should have run: %v", inner.called)
			}
		})
	}
}

// A declined tool must not run. The error is what the model sees, so it names
// the tool and says who declined — enough to choose something else rather than
// retry the same call.
func TestWithApproval_DeclinedToolDoesNotRun(t *testing.T) {
	t.Parallel()

	inner := &recordingTools{}
	declining := recordingApprover(func(agentserver.ApprovalRequest) bool { return false })

	_, err := withApproval(inner, declining).Call(context.Background(), toolBash, map[string]any{
		argCommandKey: "rm -rf /",
	})
	if err == nil {
		t.Fatal("a declined tool call must fail")
	}
	if !strings.Contains(err.Error(), toolBash) || !strings.Contains(err.Error(), "declined") {
		t.Errorf("error does not say what happened: %v", err)
	}
	if len(inner.called) != 0 {
		t.Errorf("the tool ran despite being declined: %v", inner.called)
	}
}

// The command has to travel in Commands, not only in the summary, or
// auto_approve_commands cannot answer for it — and the same allowlist must mean
// the same thing whichever side of the connection runs the command.
func TestWithApproval_BashCarriesTheCommandForTheAllowlist(t *testing.T) {
	t.Parallel()

	approver, prompted, _ := approverFor(t, []string{"go test"})
	inner := &recordingTools{}

	out, err := withApproval(inner, approver).Call(context.Background(), toolBash, map[string]any{
		argCommandKey: "go test ./...",
	})
	if err != nil {
		t.Fatalf("an allowlisted command should run: %v", err)
	}
	if out != "ran Bash" {
		t.Errorf("output = %q", out)
	}
	if *prompted {
		t.Error("an allowlisted command should not have reached the prompt")
	}
}

// A file-change prompt about "file changes" tells the user nothing. It names the
// file, or for a batch, how big the batch is.
func TestApprovalFor_FileChangeNamesWhatChanges(t *testing.T) {
	t.Parallel()

	req, needed := approvalFor(toolWrite, map[string]any{argFilePath: "/tmp/x.go"})
	if !needed || !strings.Contains(req.Summary, "/tmp/x.go") {
		t.Errorf("Write summary = %q", req.Summary)
	}

	req, _ = approvalFor(toolMultiEdit, map[string]any{"edits": []any{1, 2, 3}})
	if !strings.Contains(req.Summary, "3") {
		t.Errorf("MultiEdit summary does not say how many edits: %q", req.Summary)
	}
}

// Headless surfaces have nobody to ask, and that is not a reason to stop: a nil
// approver runs everything, exactly as it does for the approval requests a
// backend sends.
func TestWithApproval_NilApproverRunsEverything(t *testing.T) {
	t.Parallel()

	inner := &recordingTools{}
	wrapped := withApproval(inner, nil)

	if _, err := wrapped.Call(context.Background(), toolBash, map[string]any{argCommandKey: "ls"}); err != nil {
		t.Fatalf("headless Call: %v", err)
	}
	if len(inner.called) != 1 {
		t.Error("a headless call should run unasked")
	}
}

// A typed-nil approver is the trap isNil exists for elsewhere in this package:
// as an interface it is not nil, so an `== nil` guard would wrap it and the
// first mutating tool would panic mid-turn.
func TestWithApproval_TypedNilApproverIsStillHeadless(t *testing.T) {
	t.Parallel()

	var absent *autoApprover
	inner := &recordingTools{}

	if _, err := withApproval(inner, absent).Call(context.Background(), toolBash, nil); err != nil {
		t.Fatalf("a typed-nil approver should behave as headless: %v", err)
	}
	if len(inner.called) != 1 {
		t.Error("the tool did not run")
	}
}

// offeredNames returns what a backend would be offered under these settings.
func offeredNames(t *testing.T, settings *config.Settings) []string {
	t.Helper()
	managers := offeredManagers(settings, t.TempDir())
	return specNames(newToolHost(tool.NewCompositeToolManager(managers...)))
}

// settingsWithWorkspaceTools returns settings for a *spawned* generic backend,
// with the workspace set explicitly on or off.
func settingsWithWorkspaceTools(workspaceTools bool) *config.Settings {
	s := config.NewSettings()
	s.LLM.Backend = config.BackendAppServer
	s.AppServer.Command = appServerBin
	s.AppServer.WorkspaceTools = &workspaceTools
	return s
}

// dialedSettings returns settings for a backend klein reaches over the network,
// where the server lends no tools of its own.
func dialedSettings() *config.Settings {
	s := config.NewSettings()
	s.LLM.Backend = config.BackendAppServer
	s.AppServer.Address = dialedAddress
	return s
}

// Off by default for a server klein spawns, and off means absent — not
// registered-but-unused. That server has klein's own privileges and working
// tools of its own; klein's would only shadow them.
func TestOfferedManagers_WorkspaceToolsAreOptInForASpawnedServer(t *testing.T) {
	t.Parallel()

	without := offeredNames(t, settingsWithWorkspaceTools(false))
	for _, name := range conventionalToolNames {
		if slices.Contains(without, name) {
			t.Errorf("%s is offered without appserver.workspace_tools", name)
		}
	}
	// klein's own stores are offered either way: they are what this path has
	// always registered, and they are not a substitution for anything.
	if !slices.Contains(without, "MemorySearch") {
		t.Errorf("the native tools stopped being offered: %v", without)
	}

	with := offeredNames(t, settingsWithWorkspaceTools(true))
	for _, name := range conventionalToolNames {
		if !slices.Contains(with, name) {
			t.Errorf("%s is missing with appserver.workspace_tools on: %v", name, with)
		}
	}
	if !slices.Contains(with, "MemorySearch") {
		t.Error("turning on the workspace tools dropped the native ones")
	}
}

// The setting lives in the appserver block and means nothing anywhere else.
// codex brings its own shell and apply_patch; shadowing those with klein's is
// not something an [appserver] key should be able to do.
func TestWantsWorkspaceTools_IsAppServerOnly(t *testing.T) {
	t.Parallel()

	codex := settingsWithWorkspaceTools(true)
	codex.LLM.Backend = config.BackendCodex
	if wantsWorkspaceTools(codex) {
		t.Error("appserver.workspace_tools must not reach the codex backend")
	}
	if !wantsWorkspaceTools(settingsWithWorkspaceTools(true)) {
		t.Error("appserver.workspace_tools did not take effect for its own backend")
	}
}

// A dialed server lends no tools of its own, so klein's are not optional there:
// unset means on. Getting this wrong produces a session that connects, starts a
// turn, and then cannot read a file — with nothing in the configuration
// admitting to it.
func TestOfferedManagers_WorkspaceToolsAreOnByDefaultWhenDialed(t *testing.T) {
	t.Parallel()

	offered := offeredNames(t, dialedSettings())
	for _, name := range conventionalToolNames {
		if !slices.Contains(offered, name) {
			t.Errorf("%s is missing from a dialed backend's tools: %v", name, offered)
		}
	}
}

// The three states of the setting, and which transport each one lands on.
func TestWantsWorkspaceTools(t *testing.T) {
	t.Parallel()

	on, off := true, false
	tests := []struct {
		explicit *bool
		name     string
		address  string
		want     bool
	}{
		{name: "spawned, unset", want: false},
		{name: "spawned, on", explicit: &on, want: true},
		{name: "spawned, off", explicit: &off, want: false},
		{name: "dialed, unset", address: dialedAddress, want: true},
		{name: "dialed, on", address: dialedAddress, explicit: &on, want: true},
		// Honored here and refused at startup — see validateWorkspaceTools.
		{name: "dialed, off", address: dialedAddress, explicit: &off, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			settings := config.NewSettings()
			settings.LLM.Backend = config.BackendAppServer
			settings.AppServer.Address = tc.address
			settings.AppServer.WorkspaceTools = tc.explicit

			if got := wantsWorkspaceTools(settings); got != tc.want {
				t.Errorf("wantsWorkspaceTools = %v, want %v", got, tc.want)
			}
		})
	}
}

// The combination nothing can rescue: a server reached over the network, told
// not to expect klein's tools. It has none of its own to fall back on.
func TestValidateWorkspaceTools_RefusesADialedServerWithNoTools(t *testing.T) {
	t.Parallel()

	off, on := false, true
	dialed := config.AppServerSettings{Address: dialedAddress, WorkspaceTools: &off}
	err := validateWorkspaceTools(dialed)
	if err == nil {
		t.Fatal("want an error for a dialed server with no tools on either side")
	}
	if !strings.Contains(err.Error(), dialedAddress) {
		t.Errorf("error does not name the address: %v", err)
	}

	// Every other combination is fine, including a spawned server with the tools
	// switched off — that one still has its own.
	for _, ok := range []config.AppServerSettings{
		{Address: dialedAddress},
		{Address: dialedAddress, WorkspaceTools: &on},
		{Command: appServerBin, WorkspaceTools: &off},
	} {
		if err := validateWorkspaceTools(ok); err != nil {
			t.Errorf("%+v was refused: %v", ok, err)
		}
	}
}
