package app

import (
	"context"
	"errors"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// stubLLM satisfies domain.LLM without touching a provider. Tests that only need
// an Agent to *construct* (no model call) inject this via AgentOptions.LLMClient,
// so they stay hermetic — building a real client from settings would require a
// provider API key and fail in CI.
type stubLLM struct{}

var _ domain.LLM = (*stubLLM)(nil)

func (*stubLLM) Chat(
	_ context.Context, _ []message.Message, _ bool, _ chan<- string,
) (message.Message, error) {
	return nil, errors.New("stubLLM: Chat must not be called in these tests")
}

func (*stubLLM) ModelID() string { return "stub" }
