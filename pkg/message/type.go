package message

import "time"

// TokenUsage holds token usage information for a message
type TokenUsage struct {
	InputTokens         int // Tokens consumed for input (prompt + context)
	OutputTokens        int // Tokens generated in response
	TotalTokens         int // Total tokens (input + output)
	CachedTokens        int // Input tokens served from the provider's prompt cache (subset of InputTokens)
	CacheCreationTokens int // Input tokens written into the cache this call (Anthropic only; billed at 1.25x)
}

type MessageType int

const (
	MessageTypeUser MessageType = iota
	MessageTypeAssistant
	MessageTypeSystem
	MessageTypeToolCall
	MessageTypeToolCallBatch
	MessageTypeToolResult
	MessageTypeReasoning
)

type MessageSource int

const (
	MessageSourceDefault MessageSource = iota
	MessageSourceSituation
	MessageSourceSummary
)

// String returns the string representation of MessageType
func (m MessageType) String() string {
	switch m {
	case MessageTypeUser:
		return "user"
	case MessageTypeAssistant:
		return "assistant"
	case MessageTypeSystem:
		return "system"
	case MessageTypeToolCall:
		return "tool_call"
	case MessageTypeToolResult:
		return "tool_result"
	case MessageTypeReasoning:
		return "reasoning"
	default:
		return "unknown"
	}
}

func (s MessageSource) String() string {
	switch s {
	case MessageSourceDefault:
		return "default"
	case MessageSourceSituation:
		return "situation"
	case MessageSourceSummary:
		return "summary"
	default:
		return "unknown"
	}
}

type Message interface {
	// ID returns the unique identifier of the message
	ID() string

	// Type returns the type of the message (e.g., user, assistant, tool call)
	Type() MessageType

	// Content returns the content of the message
	Content() string

	// Timestamp returns the time when the message was created
	Timestamp() time.Time

	// Thinking returns the thinking content if available (for reasoning models)
	Thinking() string

	// Images returns the images attached to this message (Base64 encoded)
	Images() []string

	// String returns the string representation of the message
	String() string

	// Source returns the source of the message
	Source() MessageSource

	// TruncatedString returns a truncated, user-friendly representation for conversation previews
	TruncatedString() string

	// Token usage information
	InputTokens() int
	OutputTokens() int
	TotalTokens() int

	// SetTokenUsage sets the token usage information for this message
	SetTokenUsage(inputTokens, outputTokens, totalTokens int)

	// Metadata returns the metadata map for the message
	Metadata() map[string]any
}
