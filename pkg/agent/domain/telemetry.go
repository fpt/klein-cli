package domain

import (
	"github.com/fpt/klein-cli/pkg/message"
)

// TokenUsageProvider is an optional extension that LLM clients can implement
// to expose token accounting information from the most recent API call.
//
// Implementations should return (usage, true) when token usage was available
// for the last Chat/ChatWithToolChoice/ChatWithStructure invocation, and
// (message.TokenUsage{}, false) if unavailable.
//
// Callers should treat this as a best-effort signal and not rely on it for
// strict billing; backends may omit or delay usage reporting.
type TokenUsageProvider interface {
	LastTokenUsage() (message.TokenUsage, bool)
}

// ContextWindowProvider is an optional extension that LLM clients can implement
// to expose the model's maximum context window (input token capacity).
//
// This pairs naturally with TokenUsageProvider: callers can read the last
// input token usage and divide by MaxContextTokens() to compute utilization.
// Implementations should return a conservative best‑known value.
type ContextWindowProvider interface {
	// MaxContextTokens returns the maximum number of input tokens supported
	// by the model's context window.
	MaxContextTokens() int
}

// ModelIdentifier is an optional extension that clients can implement to
// return a stable identifier for the underlying model. This can be used for
// telemetry and to compose cache keys.
type ModelIdentifier interface {
	ModelID() string
}
