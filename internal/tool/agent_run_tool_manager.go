package tool

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fpt/klein-cli/pkg/message"
)

// AgentRunInfo is one background agent, for AgentList.
type AgentRunInfo struct {
	ID         string
	Label      string
	Task       string
	Status     string
	OutputPath string
	Elapsed    time.Duration
}

// AgentRunOutput is a background agent's transcript and outcome.
type AgentRunOutput struct {
	ID         string
	Label      string
	Status     string
	OutputPath string
	Transcript string
	Result     string
	Error      string
	Elapsed    time.Duration
}

// AgentRunCallbacks are provided by the owning *app.Agent (two-phase init, as
// with the Task dispatcher).
type AgentRunCallbacks struct {
	List   func() []AgentRunInfo
	Output func(ctx context.Context, id string, block bool, timeout time.Duration) (AgentRunOutput, error)
	Stop   func(id string) (string, error)
}

// AgentRunToolManager exposes AgentList / AgentOutput / AgentStop.
//
// The names are prefixed rather than matching Claude Code's TaskList/TaskOutput
// /TaskStop because TaskCreate/TaskUpdate/TaskList/TaskGet are already taken by
// klein's todo-style task manager, and testsuite config files reference those
// names literally.
type AgentRunToolManager struct {
	cb    AgentRunCallbacks
	tools map[message.ToolName]message.Tool
}

// NewAgentRunToolManager constructs a manager with no callbacks wired.
func NewAgentRunToolManager() *AgentRunToolManager {
	m := &AgentRunToolManager{tools: make(map[message.ToolName]message.Tool)}
	m.registerTools()
	return m
}

// SetCallbacks wires the registry operations.
func (m *AgentRunToolManager) SetCallbacks(cb AgentRunCallbacks) { m.cb = cb }

// GetTool returns a tool by name.
func (m *AgentRunToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	t, ok := m.tools[name]
	return t, ok
}

// GetTools returns every tool this manager exposes.
func (m *AgentRunToolManager) GetTools() map[message.ToolName]message.Tool { return m.tools }

// CallTool invokes a tool by name.
func (m *AgentRunToolManager) CallTool(
	ctx context.Context, name message.ToolName, args message.ToolArgumentValues,
) (message.ToolResult, error) {
	t, ok := m.tools[name]
	if !ok {
		return message.NewToolResultError(fmt.Sprintf("tool %q not found", name)), nil
	}
	return t.Handler()(ctx, args)
}

// RegisterTool adds a tool to this manager.
func (m *AgentRunToolManager) RegisterTool(
	name message.ToolName, description message.ToolDescription,
	arguments []message.ToolArgument,
	handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error),
) {
	m.tools[name] = &genericTool{name: name, description: description, arguments: arguments, handler: handler}
}

const defaultOutputTimeout = 30 * time.Second

// Argument type names, hoisted because goconst counts these literals across the
// whole package and every tool schema repeats them.
const (
	argTypeString  = "string"
	argTypeBoolean = "boolean"
	argTypeNumber  = "number"
)

func (m *AgentRunToolManager) registerTools() {
	m.RegisterTool("AgentList",
		"List the background agents started this session, with their status and how long they have been running. "+
			"Use this when you have lost track of what is still in flight.",
		nil, m.handleList)

	m.RegisterTool("AgentOutput",
		"Read a background agent's result and transcript by id. Set block=true to wait for it to finish "+
			"(up to timeout_seconds) instead of returning whatever it has so far. Prefer reading the result "+
			"once it has completed rather than polling a running agent — the transcript is its tool noise, "+
			"which is what backgrounding kept out of your context.",
		[]message.ToolArgument{
			{
				Name:        "agent_id",
				Description: "The background agent's id, as returned by Task or AgentList.",
				Required:    true, Type: argTypeString,
			},
			{
				Name:        "block",
				Description: "Wait for the agent to finish before returning (default false).",
				Required:    false, Type: argTypeBoolean,
			},
			{
				Name:        "timeout_seconds",
				Description: "How long to wait when block is true (default 30, max 600).",
				Required:    false, Type: argTypeNumber,
			},
		}, m.handleOutput)

	m.RegisterTool("AgentStop",
		"Stop a running background agent by id. Stopping one that already finished is not an error.",
		[]message.ToolArgument{
			{
				Name: "agent_id", Description: "The background agent's id.",
				Required: true, Type: argTypeString,
			},
		}, m.handleStop)
}

func (m *AgentRunToolManager) handleList(
	_ context.Context, _ message.ToolArgumentValues,
) (message.ToolResult, error) {
	if m.cb.List == nil {
		return message.NewToolResultError("AgentList: not available in this context"), nil
	}
	runs := m.cb.List()
	if len(runs) == 0 {
		return message.NewToolResultText("No background agents have been started this session."), nil
	}
	var b strings.Builder
	for _, r := range runs {
		fmt.Fprintf(&b, "%s  %-16s  %-9s  %s  %s\n",
			r.ID, r.Label, r.Status, r.Elapsed, truncateLine(r.Task, 60))
	}
	return message.NewToolResultText(b.String()), nil
}

func (m *AgentRunToolManager) handleOutput(
	ctx context.Context, args message.ToolArgumentValues,
) (message.ToolResult, error) {
	if m.cb.Output == nil {
		return message.NewToolResultError("AgentOutput: not available in this context"), nil
	}
	id, _ := args["agent_id"].(string)
	if id == "" {
		return message.NewToolResultError("AgentOutput: 'agent_id' is required"), nil
	}
	block, _ := args["block"].(bool)

	timeout := defaultOutputTimeout
	switch v := args["timeout_seconds"].(type) {
	case float64:
		timeout = time.Duration(v) * time.Second
	case int:
		timeout = time.Duration(v) * time.Second
	}
	if timeout > 10*time.Minute {
		timeout = 10 * time.Minute
	}

	out, err := m.cb.Output(ctx, id, block, timeout)
	if err != nil {
		// A bad id is the model's mistake to correct, not a tool failure to
		// propagate: it is reported in the result so the next turn can retry.
		//nolint:nilerr // reported to the model, not propagated to the caller
		return message.NewToolResultError("AgentOutput: " + err.Error()), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "agent: %s (%s)\nstatus: %s\nelapsed: %s\n",
		out.ID, out.Label, out.Status, out.Elapsed)
	if out.OutputPath != "" {
		fmt.Fprintf(&b, "transcript_file: %s\n", out.OutputPath)
	}
	if out.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", out.Error)
	}
	if out.Result != "" {
		fmt.Fprintf(&b, "\n<result>\n%s\n</result>\n", out.Result)
	} else {
		fmt.Fprintf(&b, "\n<transcript>\n%s\n</transcript>\n", out.Transcript)
	}
	return message.NewToolResultText(b.String()), nil
}

func (m *AgentRunToolManager) handleStop(
	_ context.Context, args message.ToolArgumentValues,
) (message.ToolResult, error) {
	if m.cb.Stop == nil {
		return message.NewToolResultError("AgentStop: not available in this context"), nil
	}
	id, _ := args["agent_id"].(string)
	if id == "" {
		return message.NewToolResultError("AgentStop: 'agent_id' is required"), nil
	}
	msg, err := m.cb.Stop(id)
	if err != nil {
		//nolint:nilerr // reported to the model, not propagated to the caller
		return message.NewToolResultError("AgentStop: " + err.Error()), nil
	}
	return message.NewToolResultText(msg), nil
}

// truncateLine keeps a listing row to one line of bounded width.
func truncateLine(s string, n int) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", "")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
