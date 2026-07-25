package client

import (
	"context"
	"fmt"

	"github.com/fpt/klein-cli/pkg/message"
)

// agentStubLLM is a placeholder domain.LLM for the whole-agent backends (codex,
// appserver). Those backends do not use the chat interface — app.Agent routes their
// turns to an agentserver.Runner (see internal/agentserver) — so Chat must never
// be called. The stub exists only so agent construction has a valid,
// model-identifying client.
type agentStubLLM struct {
	backend string
	model   string
}

func (c *agentStubLLM) Chat(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	return nil, fmt.Errorf("%s backend does not use the chat interface; turns are routed to its app-server", c.backend)
}

func (c *agentStubLLM) ModelID() string {
	if c.model == "" {
		return c.backend
	}
	return c.backend + ":" + c.model
}
