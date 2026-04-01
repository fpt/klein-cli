package tool

import (
	"context"
	"fmt"

	"github.com/fpt/klein-cli/pkg/message"
)

// PlanModeState tracks the current plan mode lifecycle.
type PlanModeState int

const (
	PlanModeOff      PlanModeState = iota // default: no plan mode
	PlanModeActive                         // exploring; writes blocked
	PlanModeApproved                       // user approved; execution allowed
)

// PlanApprovalHandler is called when ExitPlanMode is invoked.
// Returns (approved, clearContext, err):
//   - approved=true  → proceed with implementation
//   - clearContext=true → also clear planning conversation history before implementing
//   - approved=false → plan rejected; model should revise
//
// In non-interactive mode the handler should auto-approve without clearing.
type PlanApprovalHandler func(plan string) (approved bool, clearContext bool, err error)

// PlanToolManager provides EnterPlanMode and ExitPlanMode tools.
type PlanToolManager struct {
	state               *PlanModeState      // shared with PlanModeGuard
	approvalHandler     PlanApprovalHandler // nil → auto-approve
	clearContextHandler func()              // called when user chooses "clear context"
	tools               map[message.ToolName]message.Tool
}

// NewPlanToolManager creates a PlanToolManager sharing the given state pointer.
func NewPlanToolManager(state *PlanModeState) *PlanToolManager {
	m := &PlanToolManager{
		state: state,
		tools: make(map[message.ToolName]message.Tool),
	}
	m.registerTools()
	return m
}

// SetApprovalHandler configures the interactive approval handler.
func (m *PlanToolManager) SetApprovalHandler(h PlanApprovalHandler) {
	m.approvalHandler = h
}

// SetClearContextHandler configures the callback invoked when the user
// chooses "Approve and clear planning context".
func (m *PlanToolManager) SetClearContextHandler(h func()) {
	m.clearContextHandler = h
}

func (m *PlanToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	t, ok := m.tools[name]
	return t, ok
}

func (m *PlanToolManager) GetTools() map[message.ToolName]message.Tool { return m.tools }

func (m *PlanToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	t, ok := m.tools[name]
	if !ok {
		return message.NewToolResultError(fmt.Sprintf("tool %q not found", name)), nil
	}
	return t.Handler()(ctx, args)
}

// RegisterTool registers a tool dynamically. PlanToolManager registers its own tools at
// construction time; this method supports the domain.ToolManager interface.
func (m *PlanToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	m.tools[name] = &genericTool{
		name:        name,
		description: description,
		arguments:   arguments,
		handler:     handler,
	}
}

func (m *PlanToolManager) registerTools() {
	m.tools["EnterPlanMode"] = &enterPlanModeTool{manager: m}
	m.tools["ExitPlanMode"] = &exitPlanModeTool{manager: m}
}

// enterPlanModeTool implements message.Tool for EnterPlanMode.
type enterPlanModeTool struct {
	manager *PlanToolManager
}

func (t *enterPlanModeTool) RawName() message.ToolName { return "EnterPlanMode" }
func (t *enterPlanModeTool) Name() message.ToolName    { return "EnterPlanMode" }

func (t *enterPlanModeTool) Description() message.ToolDescription {
	return "Enter plan mode to explore the codebase before making changes. " +
		"File writes, edits, and state-modifying shell commands are blocked during plan mode. " +
		"Use ExitPlanMode with your plan when exploration is complete."
}

func (t *enterPlanModeTool) Arguments() []message.ToolArgument {
	return []message.ToolArgument{
		{
			Name:        "title",
			Description: "Optional title for the planning session",
			Required:    false,
			Type:        "string",
		},
	}
}

func (t *enterPlanModeTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
		*t.manager.state = PlanModeActive
		return message.ToolResult{
			Text: "You are now in PLAN MODE. File writes, edits, and state-modifying shell commands are blocked.\n\n" +
				"Explore freely using: Read, LS, Glob, Grep, Bash (read-only commands), WebFetch, WebSearch.\n\n" +
				"When your exploration is complete, call ExitPlanMode with your plan.",
		}, nil
	}
}

// exitPlanModeTool implements message.Tool for ExitPlanMode.
type exitPlanModeTool struct {
	manager *PlanToolManager
}

func (t *exitPlanModeTool) RawName() message.ToolName { return "ExitPlanMode" }
func (t *exitPlanModeTool) Name() message.ToolName    { return "ExitPlanMode" }

func (t *exitPlanModeTool) Description() message.ToolDescription {
	return "Exit plan mode by presenting an implementation plan for user approval. " +
		"The plan will be shown to the user who can approve or reject it. " +
		"If approved, file writes and edits will be unblocked for implementation."
}

func (t *exitPlanModeTool) Arguments() []message.ToolArgument {
	return []message.ToolArgument{
		{
			Name:        "plan",
			Description: "The implementation plan to present for approval",
			Required:    true,
			Type:        "string",
		},
	}
}

func (t *exitPlanModeTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
		plan, _ := args["plan"].(string)
		if plan == "" {
			return message.NewToolResultError("ExitPlanMode: 'plan' argument is required"), nil
		}

		if t.manager.approvalHandler != nil {
			approved, clearCtx, err := t.manager.approvalHandler(plan)
			if err != nil {
				return message.NewToolResultError(fmt.Sprintf("ExitPlanMode: approval failed: %v", err)), nil
			}
			if approved {
				*t.manager.state = PlanModeApproved
				if clearCtx && t.manager.clearContextHandler != nil {
					t.manager.clearContextHandler()
				}
				return message.ToolResult{Text: "Plan approved. You may now implement the changes."}, nil
			}
			*t.manager.state = PlanModeOff
			return message.ToolResult{Text: "Plan rejected. Revise your approach and call ExitPlanMode again when ready."}, nil
		}

		// Non-interactive: auto-approve
		*t.manager.state = PlanModeApproved
		return message.ToolResult{
			Text: fmt.Sprintf("Plan approved (non-interactive mode). Implementing:\n\n%s", plan),
		}, nil
	}
}
