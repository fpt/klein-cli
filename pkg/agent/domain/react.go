package domain

import (
	"context"

	"github.com/fpt/klein-cli/pkg/message"
)

type AgentStatus string

const (
	AgentStatusRunning            = AgentStatus("running")
	AgentStatusWaitingForApproval = AgentStatus("waiting_for_approval")
	AgentStatusCompleted          = AgentStatus("completed")
)

type ReAct interface {
	// Run sends a prompt to the ReAct model and returns the response.
	// Optional images are base64-encoded strings for vision-capable models.
	Run(ctx context.Context, prompt string, images ...string) (message.Message, error)
	Resume(ctx context.Context) (message.Message, error)
	CancelPendingToolCall() // Cancel the pending tool call without executing it
	Close()
	GetStatus() AgentStatus
	GetLastMessage() message.Message
	GetPendingToolCall() message.Message // Get the currently pending tool call
	ClearHistory()
	GetConversationSummary() string
}
