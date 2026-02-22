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
