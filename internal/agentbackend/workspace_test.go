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
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/agentserver"
	"github.com/fpt/klein-cli/pkg/message"
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
	settings := isolatedSettings(t)
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
		{toolMemorySearch, false},
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
	managers := offeredManagers(settings, t.TempDir(), nil)
	return specNames(newToolHost(tool.NewCompositeToolManager(managers...)))
}

// isolatedSettings returns settings whose base dir is this test's own.
//
// Every offered-tool set includes the memory tools, and building one opens (and
// migrates) the sqlite store under the base dir. Left at the default that store
// is the developer's real ~/.klein/memory/memory.sqlite — shared by every test in
// a package that runs them in parallel, and shared with whatever klein session
// happens to be running. Two migrations racing there fail, the failure is
// swallowed by design (offeredManagers cannot fail backend startup over it), and
// the tool set silently comes back short: a flake in whichever test compares two
// of them, on a machine that is not this one.
func isolatedSettings(t *testing.T) *config.Settings {
	t.Helper()
	s := config.NewSettings()
	s.BaseDir = t.TempDir()
	return s
}

// settingsWithWorkspaceTools returns settings for a *spawned* generic backend,
// with the workspace set explicitly on or off.
func settingsWithWorkspaceTools(t *testing.T, workspaceTools bool) *config.Settings {
	t.Helper()
	s := isolatedSettings(t)
	s.LLM.Backend = config.BackendAppServer
	s.AppServer.Command = appServerBin
	s.AppServer.WorkspaceTools = &workspaceTools
	return s
}

// dialedSettings returns settings for a backend klein reaches over the network,
// where the server lends no tools of its own.
func dialedSettings(t *testing.T) *config.Settings {
	t.Helper()
	s := isolatedSettings(t)
	s.LLM.Backend = config.BackendAppServer
	s.AppServer.Address = dialedAddress
	return s
}

// Off by default for a server klein spawns, and off means absent — not
// registered-but-unused. That server has klein's own privileges and working
// tools of its own; klein's would only shadow them.
func TestOfferedManagers_WorkspaceToolsAreOptInForASpawnedServer(t *testing.T) {
	t.Parallel()

	without := offeredNames(t, settingsWithWorkspaceTools(t, false))
	for _, name := range conventionalToolNames {
		if slices.Contains(without, name) {
			t.Errorf("%s is offered without appserver.workspace_tools", name)
		}
	}
	// klein's own stores are offered either way: they are what this path has
	// always registered, and they are not a substitution for anything.
	if !slices.Contains(without, toolMemorySearch) {
		t.Errorf("the native tools stopped being offered: %v", without)
	}

	with := offeredNames(t, settingsWithWorkspaceTools(t, true))
	for _, name := range conventionalToolNames {
		if !slices.Contains(with, name) {
			t.Errorf("%s is missing with appserver.workspace_tools on: %v", name, with)
		}
	}
	if !slices.Contains(with, toolMemorySearch) {
		t.Error("turning on the workspace tools dropped the native ones")
	}
}

// The setting lives in the appserver block and means nothing anywhere else.
// codex brings its own shell and apply_patch; shadowing those with klein's is
// not something an [appserver] key should be able to do.
func TestWantsWorkspaceTools_IsAppServerOnly(t *testing.T) {
	t.Parallel()

	codex := settingsWithWorkspaceTools(t, true)
	codex.LLM.Backend = config.BackendCodex
	if wantsWorkspaceTools(codex) {
		t.Error("appserver.workspace_tools must not reach the codex backend")
	}
	if !wantsWorkspaceTools(settingsWithWorkspaceTools(t, true)) {
		t.Error("appserver.workspace_tools did not take effect for its own backend")
	}
}

// A dialed server lends no tools of its own, so klein's are not optional there:
// unset means on. Getting this wrong produces a session that connects, starts a
// turn, and then cannot read a file — with nothing in the configuration
// admitting to it.
func TestOfferedManagers_WorkspaceToolsAreOnByDefaultWhenDialed(t *testing.T) {
	t.Parallel()

	offered := offeredNames(t, dialedSettings(t))
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
			settings := isolatedSettings(t)
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

// nativeOnlyTools is what klein offered before this feature existed, and what a
// wiring regression would offer again: memory and scheduling, nothing that
// touches a file.
type nativeOnlyTools struct{}

func (nativeOnlyTools) Specs() []agentserver.ToolSpec {
	names := []string{
		"MemorySearch", "MemoryGet", "MemoryWrite", "Forget", "Recall", "Remember",
		"Reinforce", "Revise", "MemoryHistory", "ScheduleCreate", "ScheduleList", "ScheduleDelete",
	}
	specs := make([]agentserver.ToolSpec, 0, len(names))
	for _, name := range names {
		specs = append(specs, agentserver.ToolSpec{Name: name, Description: "a native tool"})
	}
	return specs
}

func (nativeOnlyTools) Call(context.Context, string, map[string]any) (string, error) {
	return "", nil
}

// The check the server cannot make. A backend on the other end of a socket sees
// a list of dynamic tools and cannot tell what any of them do, so twelve memory
// and scheduling tools look exactly like a full workspace — which is how this
// reached a user: the model was offered memory, offered to list a directory, and
// found out mid-turn that it could not.
func TestVerifyWorkspaceToolsOffered(t *testing.T) {
	t.Parallel()

	// The regression it exists to catch: dialed, tools registered, none of them
	// the ones that matter.
	err := verifyWorkspaceToolsOffered(dialedAddress, nativeOnlyTools{})
	if err == nil {
		t.Fatal("a dialed backend with no workspace tools was allowed to start")
	}
	if !strings.Contains(err.Error(), dialedAddress) {
		t.Errorf("error does not name the address: %v", err)
	}

	// Nothing registered at all is the same failure, more obviously.
	if verifyWorkspaceToolsOffered(dialedAddress, nil) == nil {
		t.Error("a dialed backend with no tools at all was allowed to start")
	}

	// A spawned backend is not this check's business: it has tools of its own.
	if err := verifyWorkspaceToolsOffered("", nativeOnlyTools{}); err != nil {
		t.Errorf("a spawned backend was refused: %v", err)
	}

	// And the real thing passes, which is what stops this from being a check
	// that only ever fires.
	actual := newToolHost(tool.NewCompositeToolManager(offeredManagers(dialedSettings(t), t.TempDir(), nil)...))
	if err := verifyWorkspaceToolsOffered(dialedAddress, actual); err != nil {
		t.Errorf("the tools klein actually offers a dialed backend were refused: %v", err)
	}
}

// The production list and the contract list in this file must not drift apart:
// the second is what pins the names against a rename, and it is worthless if the
// first can be renamed without it noticing.
func TestWorkspaceToolNamesMatchTheContract(t *testing.T) {
	t.Parallel()

	got := slices.Clone(workspaceToolNames)
	slices.Sort(got)
	if !slices.Equal(got, conventionalToolNames) {
		t.Errorf("workspaceToolNames = %v, want %v", got, conventionalToolNames)
	}
}

// stubToolManager stands in for a connected MCP server: a manager holding tools
// klein did not build, offered to the backend as its own.
//
// Calls are recorded in the tool's handler rather than in CallTool because that
// is the seam the real path uses. CompositeToolManager dispatches by calling
// tool.Handler() directly, never the owning manager's CallTool, and an MCP tool's
// handler closes over its own live client — which is what makes a proxied call
// land on klein's connection. Recording in CallTool would test a method nothing
// upstream reaches.
type stubToolManager struct {
	tools map[message.ToolName]message.Tool
	calls *[]message.ToolName
}

func newStubToolManager(names ...string) *stubToolManager {
	calls := &[]message.ToolName{}
	m := &stubToolManager{tools: map[message.ToolName]message.Tool{}, calls: calls}
	for _, n := range names {
		m.tools[message.ToolName(n)] = &stubTool{name: message.ToolName(n), calls: calls}
	}
	return m
}

func (m *stubToolManager) GetTools() map[message.ToolName]message.Tool { return m.tools }

func (m *stubToolManager) CallTool(
	ctx context.Context, name message.ToolName, args message.ToolArgumentValues,
) (message.ToolResult, error) {
	t, ok := m.tools[name]
	if !ok {
		return message.NewToolResultError("not found"), nil
	}
	return t.Handler()(ctx, args)
}

func (m *stubToolManager) RegisterTool(
	message.ToolName,
	message.ToolDescription,
	[]message.ToolArgument,
	func(context.Context, message.ToolArgumentValues) (message.ToolResult, error),
) {
}

type stubTool struct {
	calls *[]message.ToolName
	name  message.ToolName
}

func (t *stubTool) RawName() message.ToolName            { return t.name }
func (t *stubTool) Name() message.ToolName               { return t.name }
func (t *stubTool) Description() message.ToolDescription { return "stub" }
func (t *stubTool) Arguments() []message.ToolArgument    { return nil }
func (t *stubTool) Handler() func(context.Context, message.ToolArgumentValues) (message.ToolResult, error) {
	return func(context.Context, message.ToolArgumentValues) (message.ToolResult, error) {
		*t.calls = append(*t.calls, t.name)
		return message.NewToolResultText("ok"), nil
	}
}

// The point of the whole feature: a proxied MCP manager reaches the backend as
// dynamic tools, alongside the ones settings can describe.
func TestOfferedManagers_ProxiedToolsAreOffered(t *testing.T) {
	t.Parallel()

	proxied := newStubToolManager("tree_dir", "search_local_files")
	managers := offeredManagers(dialedSettings(t), t.TempDir(), []domain.ToolManager{proxied})
	names := specNames(newToolHost(tool.NewCompositeToolManager(managers...)))

	for _, want := range []string{"tree_dir", "search_local_files"} {
		if !slices.Contains(names, want) {
			t.Errorf("proxied tool %q was not offered; got %v", want, names)
		}
	}
	// And it did not cost the workspace tools, which share the same list.
	if !slices.Contains(names, toolRead) {
		t.Errorf("proxying displaced the workspace tools; got %v", names)
	}
}

// A proxied call has to land on klein's live manager. Registering the schema
// while executing somewhere else is the failure this feature exists to avoid.
func TestOfferedManagers_ProxiedCallReachesTheLiveManager(t *testing.T) {
	t.Parallel()

	proxied := newStubToolManager("tree_dir")
	managers := offeredManagers(dialedSettings(t), t.TempDir(), []domain.ToolManager{proxied})
	host := newToolHost(tool.NewCompositeToolManager(managers...))

	if _, err := host.Call(context.Background(), "tree_dir", map[string]any{}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !slices.Contains(*proxied.calls, "tree_dir") {
		t.Errorf("the call did not reach the proxied manager; saw %v", *proxied.calls)
	}
}

// Nothing proxied is the default, and it must not disturb the existing set.
func TestOfferedManagers_NoProxiedToolsIsUnchanged(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	without := specNames(newToolHost(tool.NewCompositeToolManager(offeredManagers(dialedSettings(t), dir, nil)...)))
	empty := specNames(newToolHost(tool.NewCompositeToolManager(
		offeredManagers(dialedSettings(t), dir, []domain.ToolManager{})...)))

	if !slices.Equal(without, empty) {
		t.Errorf("an empty proxy list changed the offered set:\n nil=%v\nempty=%v", without, empty)
	}
}

// A typed-nil manager in the list is the isNil trap again: as an interface it is
// not nil, and enumerating it would panic at thread start rather than at the
// call site that passed it.
func TestOfferedManagers_TypedNilProxiedToolIsSkipped(t *testing.T) {
	t.Parallel()

	var absent *stubToolManager
	managers := offeredManagers(dialedSettings(t), t.TempDir(), []domain.ToolManager{absent})

	names := specNames(newToolHost(tool.NewCompositeToolManager(managers...)))
	if !slices.Contains(names, toolRead) {
		t.Errorf("a typed-nil proxied manager broke the offered set; got %v", names)
	}
}

// toolMemorySearch is klein's, not the workspace set's, but it is the one
// non-workspace tool these tests name repeatedly: the check that klein's own
// tools stay advertised needs a tool from a different manager to be meaningful.
const toolMemorySearch = "MemorySearch"

// deferSettings returns dialed settings that proxy MCP tools and defer them.
func deferSettings(t *testing.T) *config.Settings {
	t.Helper()
	s := dialedSettings(t)
	s.AppServer.DeferMCPTools = true
	return s
}

// specsByName indexes a host's specs so a test can ask about one tool.
func specsByName(tools agentserver.DynamicTools) map[string]agentserver.ToolSpec {
	out := map[string]agentserver.ToolSpec{}
	for _, spec := range tools.Specs() {
		out[spec.Name] = spec
	}
	return out
}

// The point of deferral: proxied MCP tools are still registered — the backend can
// route to them — but marked so it need not spend the model's attention on them.
func TestDeferMCPTools_ProxiedToolsAreRegisteredButDeferred(t *testing.T) {
	t.Parallel()

	proxied := newStubToolManager("tree_dir", "search_local_files")
	advertised, deferred := splitOfferedManagers(deferSettings(t), t.TempDir(), []domain.ToolManager{proxied})
	specs := specsByName(newSplitToolHost(
		tool.NewCompositeToolManager(advertised...), tool.NewCompositeToolManager(deferred...)))

	for _, name := range []string{"tree_dir", "search_local_files"} {
		spec, ok := specs[name]
		if !ok {
			t.Errorf("%q was not registered at all", name)
			continue
		}
		if !spec.Deferred {
			t.Errorf("%q was registered but not deferred", name)
		}
	}
}

// klein's own tools are never deferred. A turn that has to discover Read before
// reading a file has already paid more than the schema ever cost.
func TestDeferMCPTools_KleinsOwnToolsStayAdvertised(t *testing.T) {
	t.Parallel()

	proxied := newStubToolManager("tree_dir")
	advertised, deferred := splitOfferedManagers(deferSettings(t), t.TempDir(), []domain.ToolManager{proxied})
	specs := specsByName(newSplitToolHost(
		tool.NewCompositeToolManager(advertised...), tool.NewCompositeToolManager(deferred...)))

	for _, name := range []string{toolRead, toolBash, toolMemorySearch} {
		spec, ok := specs[name]
		if !ok {
			t.Errorf("%q went missing", name)
			continue
		}
		if spec.Deferred {
			t.Errorf("%q was deferred; klein's own tools must stay advertised", name)
		}
	}
}

// Deferral governs what the model is told about, never what it may reach. A
// backend that routes a call to a deferred tool must get the tool.
func TestDeferMCPTools_DeferredToolIsStillCallable(t *testing.T) {
	t.Parallel()

	proxied := newStubToolManager("tree_dir")
	advertised, deferred := splitOfferedManagers(deferSettings(t), t.TempDir(), []domain.ToolManager{proxied})
	host := newSplitToolHost(
		tool.NewCompositeToolManager(advertised...), tool.NewCompositeToolManager(deferred...))

	if _, err := host.Call(context.Background(), "tree_dir", map[string]any{}); err != nil {
		t.Fatalf("a deferred tool must still be callable: %v", err)
	}
	if !slices.Contains(*proxied.calls, "tree_dir") {
		t.Errorf("the call did not reach the proxied manager; saw %v", *proxied.calls)
	}
}

// Off by default. Without the setting the split is empty and every tool is
// advertised, which is what every existing deployment already has.
func TestDeferMCPTools_OffByDefault(t *testing.T) {
	t.Parallel()

	proxied := newStubToolManager("tree_dir")
	advertised, deferred := splitOfferedManagers(dialedSettings(t), t.TempDir(), []domain.ToolManager{proxied})

	if len(deferred) != 0 {
		t.Errorf("deferral happened without the setting: %d deferred managers", len(deferred))
	}
	specs := specsByName(newSplitToolHost(tool.NewCompositeToolManager(advertised...), nil))
	if spec, ok := specs["tree_dir"]; !ok || spec.Deferred {
		t.Errorf("tree_dir should be advertised by default; got %+v (present=%v)", spec, ok)
	}
}

// The setting is appserver-only: codex brings its own tools and its own ideas
// about them, and this flag is not part of the protocol it speaks.
func TestDeferMCPTools_IgnoredForCodexBackend(t *testing.T) {
	t.Parallel()

	s := isolatedSettings(t)
	s.LLM.Backend = config.BackendCodex
	s.AppServer.DeferMCPTools = true

	if defersMCPTools(s) {
		t.Error("defer_mcp_tools should not apply to the codex backend")
	}
}

// offeredManagers is the union of both halves, so callers that only want to know
// what klein offers at all are unaffected by the split.
func TestSplitOfferedManagers_UnionMatchesOfferedManagers(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	proxied := newStubToolManager("tree_dir")
	// One settings value for both halves. Two would be two base dirs and two
	// memory stores, which is a difference the comparison would report as the
	// split having lost a tool.
	settings := deferSettings(t)
	union := specNames(newToolHost(tool.NewCompositeToolManager(
		offeredManagers(settings, dir, []domain.ToolManager{proxied})...)))

	advertised, deferred := splitOfferedManagers(settings, dir, []domain.ToolManager{proxied})
	split := specNames(newSplitToolHost(
		tool.NewCompositeToolManager(advertised...), tool.NewCompositeToolManager(deferred...)))

	if !slices.Equal(union, split) {
		t.Errorf("the split lost or gained a tool:\nunion=%v\nsplit=%v", union, split)
	}
}
