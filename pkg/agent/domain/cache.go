package domain

import (
	"context"
	"github.com/fpt/klein-cli/pkg/message"
)

// CacheKeyProvider is an optional extension that LLM clients can implement to
// provide a deterministic cache key for a given request. The key should
// incorporate all inputs that affect the model's output: model ID/version,
// prompts/messages, tool schemas (if applicable), and any other relevant
// parameters known to the client.
//
// Returning an empty string or an error indicates that a stable cache key could
// not be generated for the given request (e.g., non-deterministic settings). In
// that case, callers should skip caching.
type CacheKeyProvider interface {
	MakeCacheKey(ctx context.Context, messages []message.Message, toolChoice *ToolChoice) (string, error)
}

// CacheStore defines a minimal interface for a pluggable response cache. The
// concrete implementation is outside the domain layer; this interface allows
// application code to depend only on capabilities.
//
// Implementations may ignore TTL or enforce their own expiration policies.
type CacheStore interface {
	Get(ctx context.Context, key string) (message.Message, bool, error)
	Set(ctx context.Context, key string, resp message.Message) error
}

// Model-side caching (provider-native) configuration
//
// Many providers support server-side/session caching (e.g., OpenAI prompt caching).
// These interfaces allow the app to pass cache-related hints without
// implementing a local cache.

// ModelSideCacheOptions carries generic hints for native/provider caches.
type ModelSideCacheOptions struct {
	// Optional logical session identifier to allow providers to group/cache
	// across a sequence of requests.
	SessionID string

	// Enable or disable provider-native prompt caching when available.
	PromptCachingEnabled bool

	// Implementation-specific scope or policy hint (optional). Providers that
	// support segment-based caching can interpret this string (e.g., "prefix").
	PolicyHint string
}

// ModelSideCacheConfigurator can be implemented by clients that expose
// provider-native caching toggles. Implementations should ignore fields not
// applicable to the provider.
type ModelSideCacheConfigurator interface {
	ConfigureModelSideCache(opts ModelSideCacheOptions)
}

// SessionAware allows a client to bind requests to a provider-visible session
// identifier which some providers use to scope caching.
type SessionAware interface {
	SetSessionID(id string)
	SessionID() string
}
