package message

import (
	"fmt"
	"strings"
	"time"
)

// Chat message with neutral format for multi-backend support
type ChatMessage struct {
	id         string
	typ        MessageType
	content    string
	thinking   string
	images     []string // Base64 encoded images
	timestamp  time.Time
	source     MessageSource // Source of the message (e.g., user, situation, etc.)
	metadata   map[string]any
	tokenUsage TokenUsage // Token usage information for this message
}

// NewChatMessage creates a new chat message with current timestamp
func NewChatMessage(msgType MessageType, content string) *ChatMessage {
	return &ChatMessage{
		id:         generateMessageID(),
		typ:        msgType,
		content:    content,
		timestamp:  time.Now(),
		source:     MessageSourceDefault,
		tokenUsage: TokenUsage{}, // Initialize empty token usage
	}
}

func NewSystemMessage(content string) *ChatMessage {
	return &ChatMessage{
		id:         generateMessageID(),
		typ:        MessageTypeSystem,
		content:    content,
		timestamp:  time.Now(),
		source:     MessageSourceDefault,
		tokenUsage: TokenUsage{}, // Initialize empty token usage
	}
}

func NewSituationSystemMessage(content string) *ChatMessage {
	return &ChatMessage{
		id:         generateMessageID(),
		typ:        MessageTypeSystem,
		content:    content,
		timestamp:  time.Now(),
		source:     MessageSourceSituation,
		tokenUsage: TokenUsage{}, // Initialize empty token usage
	}
}

func NewSummarySystemMessage(content string) *ChatMessage {
	return &ChatMessage{
		id:         generateMessageID(),
		typ:        MessageTypeSystem,
		content:    content,
		timestamp:  time.Now(),
		source:     MessageSourceSummary,
		tokenUsage: TokenUsage{}, // Initialize empty token usage
	}
}

// NewChatMessageWithThinking creates a new chat message with thinking content
func NewChatMessageWithThinking(msgType MessageType, content, thinking string) *ChatMessage {
	return &ChatMessage{
		id:         generateMessageID(),
		typ:        msgType,
		content:    content,
		thinking:   thinking,
		timestamp:  time.Now(),
		source:     MessageSourceDefault,
		tokenUsage: TokenUsage{}, // Initialize empty token usage
	}
}

// NewChatMessageWithImages creates a new chat message with images (Base64 encoded)
func NewChatMessageWithImages(msgType MessageType, content string, images []string) *ChatMessage {
	return &ChatMessage{
		id:         generateMessageID(),
		typ:        msgType,
		content:    content,
		images:     images,
		timestamp:  time.Now(),
		source:     MessageSourceDefault,
		tokenUsage: TokenUsage{}, // Initialize empty token usage
	}
}

// NewReasoningMessage creates a new reasoning message (intermediate thinking)
func NewReasoningMessage(content string) *ChatMessage {
	return &ChatMessage{
		id:         generateMessageID(),
		typ:        MessageTypeReasoning,
		content:    content,
		timestamp:  time.Now(),
		source:     MessageSourceDefault,
		tokenUsage: TokenUsage{}, // Initialize empty token usage
	}
}

func (c *ChatMessage) ID() string {
	return c.id
}

func (c *ChatMessage) Type() MessageType {
	return c.typ
}

func (c *ChatMessage) Content() string {
	return c.content
}

func (c *ChatMessage) Timestamp() time.Time {
	return c.timestamp
}

func (c *ChatMessage) Thinking() string {
	return c.thinking
}

func (c *ChatMessage) Images() []string {
	return c.images
}

func (c *ChatMessage) String() string {
	tokensInfo := ""
	if c.tokenUsage.TotalTokens > 0 {
		tokensInfo = fmt.Sprintf(", Tokens: %d (in:%d out:%d)",
			c.tokenUsage.TotalTokens, c.tokenUsage.InputTokens, c.tokenUsage.OutputTokens)
	}
	return fmt.Sprintf("Message(ID: %s, Type: %s, Content: %q, Thinking: %q, Images: %d, Timestamp: %s, Source: %s%s)",
		c.id, c.typ, c.content, c.thinking, len(c.images), c.timestamp.Format(time.RFC3339), c.source, tokensInfo)
}

func (c *ChatMessage) Source() MessageSource {
	return c.source
}

// Token usage methods
func (c *ChatMessage) InputTokens() int {
	return c.tokenUsage.InputTokens
}

func (c *ChatMessage) OutputTokens() int {
	return c.tokenUsage.OutputTokens
}

func (c *ChatMessage) TotalTokens() int {
	return c.tokenUsage.TotalTokens
}

func (c *ChatMessage) SetTokenUsage(inputTokens, outputTokens, totalTokens int) {
	c.tokenUsage = TokenUsage{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  totalTokens,
	}
}

// Metadata returns the metadata map for the message
func (c *ChatMessage) Metadata() map[string]any {
	if c.metadata == nil {
		return make(map[string]any)
	}
	return c.metadata
}

// SetMetadata sets a key-value pair in the metadata map
func (c *ChatMessage) SetMetadata(key string, value any) {
	if c.metadata == nil {
		c.metadata = make(map[string]any)
	}
	c.metadata[key] = value
}

// TruncatedString returns a truncated, user-friendly representation for conversation previews
func (c *ChatMessage) TruncatedString() string {
	content := c.content

	switch c.typ {
	case MessageTypeUser:
		// Extract original user request from scenario prompts
		if strings.Contains(content, "User Request: ") {
			lines := strings.Split(content, "\n")
			for _, line := range lines {
				if after, found := strings.CutPrefix(line, "User Request: "); found {
					content = after
					break
				}
			}
		}

		// Truncate long user messages
		if len(content) > 150 {
			content = content[:150] + "..."
		}
		return fmt.Sprintf("👤 You: %s", content)

	case MessageTypeAssistant:
		// Truncate long assistant messages
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		return fmt.Sprintf("🤖 Assistant: %s", content)

	case MessageTypeToolResult:
		// Show truncated tool results
		if len(content) > 100 {
			// For tool results, show first line or first 100 chars
			lines := strings.Split(content, "\n")
			if len(lines) > 0 && len(lines[0]) <= 100 {
				content = lines[0] + "..."
			} else {
				content = content[:100] + "..."
			}
		}
		return fmt.Sprintf("   ↳ %s", content)

	case MessageTypeSystem:
		// Skip system messages in conversation previews
		return ""

	default:
		// For other message types, show type and truncated content
		if len(content) > 100 {
			content = content[:100] + "..."
		}
		return fmt.Sprintf("[%s] %s", c.typ, content)
	}
}

// generateMessageID generates a unique message ID
func generateMessageID() string {
	return fmt.Sprintf("msg_%d", time.Now().UnixNano())
}
