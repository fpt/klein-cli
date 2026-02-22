package gateway

import "time"

// InboundMessage represents a message arriving from any channel adapter.
type InboundMessage struct {
	ChannelType string // "discord", "telegram", etc.
	ChannelID   string // channel/chat identifier
	PeerID      string // user identifier
	PeerName    string // display name
	Text        string
	ReplyToID   string // original message ID for threading
	Timestamp   time.Time
	Images      [][]byte
}

// OutboundMessage represents a message to send back to a channel.
type OutboundMessage struct {
	ChannelType string
	ChannelID   string
	Text        string
	ReplyToID   string // optional: reply to specific message
}

// MessageBus decouples channel adapters from the agent routing.
type MessageBus struct {
	Inbound  chan InboundMessage
	Outbound chan OutboundMessage
}

// NewMessageBus creates a message bus with buffered channels.
func NewMessageBus(bufferSize int) *MessageBus {
	return &MessageBus{
		Inbound:  make(chan InboundMessage, bufferSize),
		Outbound: make(chan OutboundMessage, bufferSize),
	}
}
