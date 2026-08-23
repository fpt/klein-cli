package agentbackend

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/agentserver"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// workspaceToolTimeout bounds a command klein runs on the backend's behalf. It
// matches the native loop's budget: the same tool, run for a different caller.
const workspaceToolTimeout = 2 * time.Minute

// newWorkspaceTools builds the tools klein offers a backend that has none of its
// own to use — Read, Write, Edit, MultiEdit, LS, Glob, Grep, Bash.
//
// They are klein's ordinary tool managers, built the way the native loop builds
// them, and that is the point: the allowlist, the blacklist, the read-before-write
// rule and the working directory are all klein's, on klein's machine, whatever
// machine the model is reasoning on.
//
// The names are deliberately the conventional ones rather than klein-prefixed.
// Models emit them from habit, and a server's own prompt and profiles talk about
// them by name; a server that resolves a call to the first exact match will use
// klein's in place of its own, which is the substitution this exists to make.
func newWorkspaceTools(settings *config.Settings, workingDir string) domain.ToolManager {
	fsConfig := infra.DefaultFileSystemConfig(workingDir)
	// The memory directory is not under the working directory, and a turn that
	// cannot read the notes klein injects into its own prompt can neither check
	// nor curate them.
	if memoryDir := settings.MemoryDir(); memoryDir != "" {
		fsConfig.AllowedDirectories = append(fsConfig.AllowedDirectories, memoryDir)
	}

	return tool.NewCompositeToolManager(
		tool.NewFileSystemToolManager(infra.NewOSFilesystemRepository(), fsConfig, workingDir),
		// The same allowlist as the filesystem tools: a Grep that can report what
		// is in a file a Read would refuse is the same disclosure by another route.
		tool.NewSearchToolManager(tool.SearchConfig{
			WorkingDir:         workingDir,
			AllowedDirectories: fsConfig.AllowedDirectories,
		}),
		tool.NewBashToolManager(tool.BashConfig{
			WorkingDir:          workingDir,
			MaxDuration:         workspaceToolTimeout,
			WhitelistedCommands: settings.Bash.WhitelistedCommands,
		}),
	)
}

// Tools that change something, and so have to be asked about before they run.
// The read-only rest (Read, LS, Glob, Grep) are not here, matching the native
// loop: klein has never prompted to read a file.
const (
	toolBash      = "Bash"
	toolWrite     = "Write"
	toolEdit      = "Edit"
	toolMultiEdit = "MultiEdit"
	argCommandKey = "command"
	argFilePath   = "file_path"
)

// The read-only tools, never asked about. They are named for the same reason as
// the mutating ones: a tool name is a contract with the backend, and the tests
// pin these against a rename.
const (
	toolRead = "Read"
	toolGlob = "Glob"
	toolGrep = "Grep"
)

// withApproval puts a mutating tool call to the approver before running it.
//
// This is the gate that would otherwise go missing. When the backend owns the
// tools, it is the backend that asks permission (item/*/requestApproval) and
// klein that answers. Hand the tools to klein and nobody asks at all: the
// backend has nothing to request approval for, and klein's dynamic-tool path
// runs whatever it is called with. So enabling workspace tools would silently
// remove the y/N prompt an interactive user gets today before a file is written
// or a command runs — a smaller surface with a quieter failure than the one it
// replaced.
//
// A nil approver is the headless default and means "run it", exactly as it does
// for the approval requests a backend sends. Read-only tools never reach the
// approver, and neither do klein's native stores (memory, schedules): their
// names are not in the mutating set, so the wrapper passes them straight
// through.
func withApproval(tools agentserver.DynamicTools, approver agentserver.Approver) agentserver.DynamicTools {
	if isNil(tools) || isNil(approver) {
		return tools
	}
	return approvingTools{inner: tools, approver: approver}
}

// approvingTools is the DynamicTools withApproval returns.
type approvingTools struct {
	inner    agentserver.DynamicTools
	approver agentserver.Approver
}

func (a approvingTools) Specs() []agentserver.ToolSpec { return a.inner.Specs() }

func (a approvingTools) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	req, needed := approvalFor(name, args)
	if needed && !a.approver.Approve(ctx, req) {
		// An error, not a refusal to answer: the protocol carries one failure bit
		// and a message, and this message is one the model should act on by
		// choosing something else rather than retrying the same call.
		return "", fmt.Errorf("%s was declined by the user", name)
	}
	// Not wrapped: the host underneath already prefixes the tool name, and a
	// second prefix would only make the message the model reads longer.
	return a.inner.Call(ctx, name, args) //nolint:wrapcheck // wrapped one layer down, in toolHost.Call
}

// approvalFor describes what a tool call is about to do, or reports that it does
// not need asking about.
//
// A Bash request carries its command in Commands as well as the summary, which
// is what lets auto_approve_commands answer it without a prompt — the same
// allowlist, applied to the same command, whichever side of the connection runs
// it. See WithAutoApprove.
func approvalFor(name string, args map[string]any) (agentserver.ApprovalRequest, bool) {
	switch name {
	case toolBash:
		command := stringArg(args, argCommandKey)
		return agentserver.ApprovalRequest{
			Kind:     agentserver.ApprovalCommand,
			Summary:  command,
			Commands: []string{command},
		}, true
	case toolWrite, toolEdit, toolMultiEdit:
		return agentserver.ApprovalRequest{
			Kind:    agentserver.ApprovalFileChange,
			Summary: fileChangeSummary(name, args),
		}, true
	default:
		return agentserver.ApprovalRequest{}, false
	}
}

// fileChangeSummary says which file is about to change, so the prompt is about
// something rather than about "file changes". MultiEdit carries a batch instead
// of a path, and naming every file in it would make the prompt unreadable.
func fileChangeSummary(name string, args map[string]any) string {
	if path := stringArg(args, argFilePath); path != "" {
		return name + " " + path
	}
	if edits, ok := args["edits"].([]any); ok {
		return fmt.Sprintf("%s (%d edits)", name, len(edits))
	}
	return name
}

func stringArg(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}

// logOfferedTools records what klein handed the backend.
//
// The half-configured case is the reason: a server told to serve no tools of its
// own, talking to a klein that offers none either, produces a model with no
// hands and a turn that fails for no visible reason. The server logs that on its
// side; this is the same fact from klein's, and the two together say which half
// was misconfigured. Nil logger is silent.
func logOfferedTools(logger *pkgLogger.Logger, tools agentserver.DynamicTools) {
	if logger == nil || isNil(tools) {
		return
	}
	specs := tools.Specs()
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	// Specs come from map iteration, so sort: a log line that shuffles between
	// runs cannot be diffed against the last one.
	sort.Strings(names)
	logger.Info("offering tools to the app-server", "count", len(names), "tools", strings.Join(names, " "))
}
