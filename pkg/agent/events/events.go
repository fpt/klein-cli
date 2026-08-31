package events

import (
	"time"

	"github.com/fpt/klein-cli/pkg/message"
)

// EventType represents different types of agent events
type EventType string

const (
	EventTypeThinkingChunk EventType = "thinking_chunk"
	EventTypeToolCallStart EventType = "tool_call_start"
	EventTypeToolCallEnd   EventType = "tool_call_end"
	EventTypeToolResult    EventType = "tool_result"
	EventTypeResponse      EventType = "response"
	EventTypeError         EventType = "error"
	EventTypeSubAgentStart EventType = "sub_agent_start"
	EventTypeSubAgentEnd   EventType = "sub_agent_end"
	// EventTypeTokenUsage carries context accounting a whole-agent backend
	// reported. The native ReAct loop does not emit it: there klein builds the
	// prompt and can measure it directly, while a backend's prompt is built on
	// the other side of a connection and only it knows the size.
	EventTypeTokenUsage EventType = "token_usage"
)

// AgentEvent represents a structured event from the agent
type AgentEvent struct {
	Type      EventType   `json:"type"`
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data"`
	// Metadata fields
	Iteration *IterationInfo `json:"iteration,omitempty"` // Optional iteration context
}

// IterationInfo contains iteration context for events
type IterationInfo struct {
	Current int `json:"current"` // Current iteration number (0-based)
	Maximum int `json:"maximum"` // Maximum iterations allowed
}

// ThinkingChunkData contains thinking content
type ThinkingChunkData struct {
	Content string `json:"content"`
}

// ToolCallStartData contains information about a tool call starting
type ToolCallStartData struct {
	ToolName  string                     `json:"tool_name"`
	Arguments message.ToolArgumentValues `json:"arguments"`
	CallID    string                     `json:"call_id,omitempty"`
}

// ToolCallEndData contains information about a tool call completing
type ToolCallEndData struct {
	ToolName string        `json:"tool_name"`
	CallID   string        `json:"call_id,omitempty"`
	Duration time.Duration `json:"duration"`
}

// ToolResultData contains tool execution results
type ToolResultData struct {
	ToolName string `json:"tool_name"`
	CallID   string `json:"call_id,omitempty"`
	Content  string `json:"content"`
	IsError  bool   `json:"is_error"`
}

// TokenUsageData is what a backend turn reported about context occupancy.
//
// InputTokens is the most recent model call's prompt, not a running total: it is
// the number that belongs against ContextWindow, since the total keeps climbing
// after the context has stopped growing.
//
// ContextWindow of 0 means the backend would not name one, and a display must
// then show counts or nothing — never a percentage of an assumed window.
//
// among them would make any future marshaling of this stream mixed.
//
//nolint:tagliatelle // snake_case matches every sibling here; a camelCase type
type TokenUsageData struct {
	InputTokens   int `json:"input_tokens"`
	OutputTokens  int `json:"output_tokens"`
	TotalTokens   int `json:"total_tokens"`
	ContextWindow int `json:"context_window"`
}

// ResponseData contains the final agent response
type ResponseData struct {
	Message message.Message `json:"message"`
}

// ErrorData contains error information
type ErrorData struct {
	Error   error  `json:"error"`
	Context string `json:"context,omitempty"`
}

// SubAgentStartData contains information about a sub-agent starting
type SubAgentStartData struct {
	Task  string `json:"task"`
	Skill string `json:"skill"`
}

// SubAgentEndData contains information about a sub-agent completing
type SubAgentEndData struct {
	Task    string `json:"task"`
	Skill   string `json:"skill"`
	Result  string `json:"result"`
	IsError bool   `json:"is_error,omitempty"`
}

// EventHandler is a function that processes agent events
type EventHandler func(event AgentEvent)

// EventEmitter provides methods for emitting agent events
type EventEmitter interface {
	EmitEvent(eventType EventType, data interface{})
	AddHandler(handler EventHandler)
	RemoveHandler(handler EventHandler)
}

// SimpleEventEmitter is a basic implementation of EventEmitter
type SimpleEventEmitter struct {
	handlers []EventHandler
}

// NewSimpleEventEmitter creates a new simple event emitter
func NewSimpleEventEmitter() *SimpleEventEmitter {
	return &SimpleEventEmitter{
		handlers: make([]EventHandler, 0),
	}
}

// EmitEvent emits an event to all registered handlers
func (e *SimpleEventEmitter) EmitEvent(eventType EventType, data interface{}) {
	event := AgentEvent{
		Type:      eventType,
		Timestamp: time.Now(),
		Data:      data,
	}

	for _, handler := range e.handlers {
		handler(event)
	}
}

// AddHandler adds an event handler
func (e *SimpleEventEmitter) AddHandler(handler EventHandler) {
	e.handlers = append(e.handlers, handler)
}

// RemoveHandler removes an event handler
func (e *SimpleEventEmitter) RemoveHandler(handler EventHandler) {
	for i, h := range e.handlers {
		// Compare function pointers (this is a simple approach)
		if &h == &handler {
			e.handlers = append(e.handlers[:i], e.handlers[i+1:]...)
			break
		}
	}
}

// GetHandlers returns the list of handlers (for internal use)
func (e *SimpleEventEmitter) GetHandlers() []EventHandler {
	return e.handlers
}
