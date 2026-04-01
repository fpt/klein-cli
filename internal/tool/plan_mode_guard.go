package tool

import (
	"context"
	"strings"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

const planModeBlockedMsg = "PLAN_MODE: This operation is blocked during the planning phase. Complete your exploration and call ExitPlanMode to proceed."

// blockedInPlanMode lists tool names that are write operations.
var blockedInPlanMode = map[message.ToolName]bool{
	"Write":     true,
	"Edit":      true,
	"MultiEdit": true,
}

// PlanModeGuard wraps a ToolManager and blocks destructive operations
// while plan mode is active.
type PlanModeGuard struct {
	inner     domain.ToolManager
	planState *PlanModeState
}

// NewPlanModeGuard wraps inner with plan-mode blocking.
func NewPlanModeGuard(inner domain.ToolManager, planState *PlanModeState) *PlanModeGuard {
	return &PlanModeGuard{inner: inner, planState: planState}
}

func (g *PlanModeGuard) isActive() bool {
	return g.planState != nil && *g.planState == PlanModeActive
}

func (g *PlanModeGuard) GetTools() map[message.ToolName]message.Tool {
	return g.inner.GetTools()
}

// RegisterTool delegates to the inner tool manager.
func (g *PlanModeGuard) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	g.inner.RegisterTool(name, description, arguments, handler)
}

func (g *PlanModeGuard) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	if g.isActive() {
		if blockedInPlanMode[name] {
			return message.NewToolResultError(planModeBlockedMsg), nil
		}
		// Block write-like Bash commands
		if name == "Bash" {
			if cmd, ok := args["command"].(string); ok && bashLooksLikeWrite(cmd) {
				return message.NewToolResultError(planModeBlockedMsg), nil
			}
		}
	}
	return g.inner.CallTool(ctx, name, args)
}

// bashLooksLikeWrite returns true when a bash command appears to modify state.
func bashLooksLikeWrite(cmd string) bool {
	lower := strings.ToLower(strings.TrimSpace(cmd))
	writeIndicators := []string{
		">", ">>",
		"rm ", "rm\t",
		"mv ", "mv\t",
		"cp ", "cp\t",
		"mkdir ",
		"touch ",
		"chmod ",
		"chown ",
		"git commit",
		"git push",
		"git checkout",
		"git reset",
		"git rm",
		"go build",
		"go install",
		"make ",
		"install ",
		"tee ",
	}
	for _, ind := range writeIndicators {
		if strings.Contains(lower, ind) {
			return true
		}
	}
	return false
}
