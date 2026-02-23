package gateway

import "context"

// Adapter is the interface all channel adapters implement.
type Adapter interface {
	// Start begins listening for messages. Blocks until ctx is cancelled.
	Start(ctx context.Context) error
	// Stop gracefully shuts down the adapter.
	Stop() error
	// Send sends a message back to the channel.
	Send(ctx context.Context, msg OutboundMessage) error
	// SendTyping shows a typing indicator in the channel.
	SendTyping(ctx context.Context, channelID string) error
}

// ContextProvider is optionally implemented by adapters that can fetch channel history.
// Used to inject thread/channel context on session creation (e.g., after server restart).
type ContextProvider interface {
	FetchChannelContext(ctx context.Context, channelID string, limit int) (string, error)
}
