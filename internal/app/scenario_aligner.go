package app

import (
	"fmt"
	"strings"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
	"github.com/fpt/klein-cli/pkg/message"
)

// Package-level logger for agent situation operations
var agentSituationLogger = pkgLogger.NewComponentLogger("agent-situation")

// AgentSituation injects situational context messages during ReAct iterations.
// Handles behavioral nudges (iteration limits, tool result guidance).
// Runtime state like todos and cache is handled via ToolAnnotator instead.
type AgentSituation struct{}

// NewAgentSituation creates a new AgentSituation.
func NewAgentSituation() *AgentSituation {
	return &AgentSituation{}
}

func (a *AgentSituation) InjectMessage(state domain.State, curIter, iterLimit int) {
	// Shortcut for last iteration message
	if curIter >= iterLimit-1 {
		systemMessage := fmt.Sprintf("IMPORTANT: This is iteration %d/%d. Conclude your response based on the knowledge so far.",
			curIter, iterLimit)
		state.AddMessage(message.NewSituationSystemMessage(systemMessage))
		return
	}

	var messages []string

	// if the last message is a tool response, we prepend a special system message
	if lastMsg := state.GetLastMessage(); lastMsg != nil && lastMsg.Type() == message.MessageTypeToolResult {
		agentSituationLogger.DebugWithIntention(pkgLogger.IntentionTool, "Found tool result, prepending system message")

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
