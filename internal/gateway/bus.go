package gateway

import "time"

// InboundMessage represents a message arriving from any channel adapter.
//
// Silent messages run through the agent normally but the gateway will NOT
// post the final response to the originating channel. This is how scheduled
// data-collection jobs (e.g. periodic ResearcherFetch) run without
// spamming Discord. The PeerName + scheduler name still appear in the
// gateway log so the run is auditable.
type InboundMessage struct {
	ChannelType string // "discord", "telegram", or "scheduler" for internal jobs
	ChannelID   string // channel/chat identifier (may be empty for silent jobs)
	PeerID      string // user identifier
	PeerName    string // display name
	Text        string
	ReplyToID   string // original message ID for threading
	Timestamp   time.Time
	Images      [][]byte
	Silent      bool   // when true, skip posting the final response back to the channel
	Skill       string // when non-empty, overrides the session's skill for THIS turn only
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
