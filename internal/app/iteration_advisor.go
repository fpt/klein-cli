package app

import (
	"fmt"
	"strings"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
	"github.com/fpt/klein-cli/pkg/message"
)

// Package-level logger for iteration advisor operations
var iterationAdvisorLogger = pkgLogger.NewComponentLogger("iteration-advisor")

// IterationAdvisor injects situational context messages during ReAct iterations.
// Handles behavioral nudges (iteration limits, tool result guidance) and
// runtime state from tool managers (web cache, todo counts, edit failures) so that
// tool descriptions stay static and Anthropic prompt caching can hit.
type IterationAdvisor struct {
	toolState domain.ToolStateProvider // optional; nil if no state tracking needed
}

// NewIterationAdvisor creates a new IterationAdvisor.
// Pass a ToolStateProvider (e.g. CompositeToolManager) to include dynamic tool state
// in situation messages; pass nil to disable state injection.
func NewIterationAdvisor(toolState domain.ToolStateProvider) *IterationAdvisor {
	return &IterationAdvisor{toolState: toolState}
}

func (a *IterationAdvisor) InjectMessage(state domain.State, curIter, iterLimit int) {
	// Shortcut for last iteration message
	if curIter >= iterLimit-1 {
		systemMessage := fmt.Sprintf("IMPORTANT: This is iteration %d/%d. Conclude your response based on the knowledge so far.",
			curIter, iterLimit)
		state.AddMessage(message.NewSituationSystemMessage(systemMessage))
		return
	}

	var messages []string

	// Include dynamic tool state (web cache entries, todo counts, edit failures) if available.
	// This keeps the information ephemeral (removed each iteration) and out of
	// tool descriptions, so Anthropic can cache the stable tool list.
	if a.toolState != nil {
		if s := a.toolState.GetToolState(); s != "" {
			messages = append(messages, s)
		}
	}

	// if the last message is a tool response, we prepend a special system message
	if lastMsg := state.GetLastMessage(); lastMsg != nil && lastMsg.Type() == message.MessageTypeToolResult {
		iterationAdvisorLogger.DebugWithIntention(pkgLogger.IntentionTool, "Found tool result, prepending system message")

		if len(lastMsg.Images()) > 0 {
			messages = append(messages, "You received a tool result with visual content (images). IMPORTANT: You must analyze the images and provide a comprehensive visual analysis based on what you can see in the images. Focus on the user's original request and describe the visual content thoroughly. Do not call additional tools - provide your final analysis based on the visual information.")
		} else {
			messages = append(messages, "You received a tool result. Analyze it and decide next steps to respond to original user request.")

			content := lastMsg.Content()
			if strings.Contains(content, "All validation checks passed") || strings.Contains(content, "Code compiles successfully") {
				messages = append(messages, "Validation indicates success. If the user's request appears fully satisfied, provide a final concise response now and avoid further tool calls.")
			}
		}
	}

	if len(messages) > 0 {
		systemMessage := strings.Join(messages, "\n")
		state.AddMessage(message.NewSituationSystemMessage(systemMessage))
	}
}
