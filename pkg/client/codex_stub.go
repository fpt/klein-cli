package client

import (
	"context"
	"fmt"

	"github.com/fpt/klein-cli/pkg/message"
)

// codexStubLLM is a placeholder domain.LLM for the codex backend. The codex
// backend does not use the chat interface — app.Agent routes codex turns to a
// codex.Runner (see internal/codex) — so Chat must never be called. The stub
// exists only so agent construction has a valid, model-identifying client.
type codexStubLLM struct{ model string }

func (c *codexStubLLM) Chat(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	return nil, fmt.Errorf("codex backend does not use the chat interface; turns are routed to the codex app-server")
}

func (c *codexStubLLM) ModelID() string {
	if c.model == "" {
		return "codex"
	}
	return "codex:" + c.model
}
