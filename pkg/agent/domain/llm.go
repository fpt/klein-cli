package domain

import (
	"context"

	"github.com/pkg/errors"

	"github.com/fpt/klein-cli/pkg/message"
)

var ErrInvalidClientType = errors.New("invalid client type for tool calling")

// LLM represents the base language model interface for basic chat functionality
type LLM interface {
	// Chat sends a message to the LLM and returns the response
	Chat(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (message.Message, error)
	// ModelID returns a stable identifier for the underlying model
	ModelID() string
}

// ToolCallingLLM extends LLM with tool calling capabilities
type ToolCallingLLM interface {
	LLM

	// SetToolManager sets the tool manager for this client
	SetToolManager(toolManager ToolManager)

	// ChatWithToolChoice sends a message to the LLM with tool choice control
	ChatWithToolChoice(ctx context.Context, messages []message.Message, toolChoice ToolChoice, enableThinking bool, thinkingChan chan<- string) (message.Message, error)
}

// StructuredLLM represents the base language model interface for structured responses
type StructuredLLM[T any] interface {
	LLM

	// Chat sends a message to the LLM and returns the structured response
	ChatWithStructure(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (T, error)
}

// VisionLLM extends LLM with vision capabilities for image analysis
type VisionLLM interface {
	LLM

	// SupportsVision returns true if this client supports vision/image analysis
	SupportsVision() bool
}
