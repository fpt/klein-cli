package gateway

import (
	"context"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"

	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// DiscordAdapter implements the Adapter interface for Discord.
type DiscordAdapter struct {
	session     *discordgo.Session
	bus         *MessageBus
	config      DiscordConfig
	logger      *pkgLogger.Logger
	botUserID   string
	allowGuilds map[string]bool
	allowChans  map[string]bool
	allowUsers  map[string]bool
}

// NewDiscordAdapter creates a Discord adapter.
func NewDiscordAdapter(bus *MessageBus, cfg DiscordConfig, logger *pkgLogger.Logger) (*DiscordAdapter, error) {
	dg, err := discordgo.New("Bot " + cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to create discord session: %w", err)
	}

	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentMessageContent

	a := &DiscordAdapter{
		session:     dg,
		bus:         bus,
		config:      cfg,
		logger:      logger.WithComponent("discord"),
		allowGuilds: toSet(cfg.AllowedGuildIDs),
		allowChans:  toSet(cfg.AllowedChannelIDs),
		allowUsers:  toSet(cfg.AllowedUserIDs),
	}

	dg.AddHandler(a.handleMessage)
	dg.AddHandler(a.handleReady)

	return a, nil
}

func (a *DiscordAdapter) handleReady(s *discordgo.Session, r *discordgo.Ready) {
	a.botUserID = r.User.ID
	a.logger.Info("Discord bot connected", "user", r.User.Username, "discriminator", r.User.Discriminator)
}

func (a *DiscordAdapter) handleMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignore own messages
	if m.Author.ID == a.botUserID {
		return
	}

	// Ignore bot messages
	if m.Author.Bot {
		return
	}

	// Check user allowlist
	if len(a.allowUsers) > 0 && !a.allowUsers[m.Author.ID] {
		return
	}

	// Check guild allowlist
	if m.GuildID != "" && len(a.allowGuilds) > 0 && !a.allowGuilds[m.GuildID] {
		return
	}

	// Check channel allowlist
	if len(a.allowChans) > 0 && !a.allowChans[m.ChannelID] {
		return
	}

	// In guild channels with mention_only, only respond if bot is mentioned
	if m.GuildID != "" && a.config.MentionOnly {
		if !isBotMentioned(m.Mentions, a.botUserID) {
			return
		}
	}

	// Strip bot mention from message text
	text := m.Content
	if a.botUserID != "" {
		text = strings.ReplaceAll(text, "<@"+a.botUserID+">", "")
		text = strings.ReplaceAll(text, "<@!"+a.botUserID+">", "")
		text = strings.TrimSpace(text)
	}

	if text == "" {
		return
	}

	// Push to message bus
	a.bus.Inbound <- InboundMessage{
		ChannelType: "discord",
		ChannelID:   m.ChannelID,
		PeerID:      m.Author.ID,
		PeerName:    m.Author.Username,
		Text:        text,
		ReplyToID:   m.ID,
		Timestamp:   m.Timestamp,
	}
}

// Start connects to Discord and blocks until ctx is cancelled.
func (a *DiscordAdapter) Start(ctx context.Context) error {
	a.logger.Info("Starting Discord adapter")

	if err := a.session.Open(); err != nil {
		return fmt.Errorf("failed to open discord connection: %w", err)
	}

	// Block until context is done
	<-ctx.Done()
	return a.session.Close()
}

// Stop closes the Discord connection.
func (a *DiscordAdapter) Stop() error {
	return a.session.Close()
}

// Send sends a message to a Discord channel, splitting if over 2000 chars.
func (a *DiscordAdapter) Send(ctx context.Context, msg OutboundMessage) error {
	chunks := splitMessage(msg.Text, 2000)
	for _, chunk := range chunks {
		_, err := a.session.ChannelMessageSend(msg.ChannelID, chunk)
		if err != nil {
			return fmt.Errorf("failed to send discord message: %w", err)
		}
	}
	return nil
}

// SendTyping shows a typing indicator.
func (a *DiscordAdapter) SendTyping(ctx context.Context, channelID string) error {
	return a.session.ChannelTyping(channelID)
}

// splitMessage splits text into chunks at newline boundaries, respecting maxLen.
func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	for len(text) > 0 {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}

		// Find last newline within limit
		cutAt := maxLen
		if idx := strings.LastIndex(text[:maxLen], "\n"); idx > 0 {
			cutAt = idx + 1
		}

		chunks = append(chunks, text[:cutAt])
		text = text[cutAt:]
	}
	return chunks
}

// FetchChannelContext fetches recent messages (and thread starter if applicable)
// from a Discord channel for injection into the agent prompt.
func (a *DiscordAdapter) FetchChannelContext(ctx context.Context, channelID string, limit int) (string, error) {
	if limit <= 0 {
		limit = 10
	}

	// Get channel info to determine if this is a thread
	channel, err := a.session.Channel(channelID)
	if err != nil {
		return "", fmt.Errorf("failed to get channel info: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("[THREAD CONTEXT]\n")

	// If this is a thread, fetch the starter message
	if isThreadChannel(channel.Type) {
		if channel.Name != "" {
			sb.WriteString(fmt.Sprintf("Thread: \"%s\"\n", channel.Name))
		}
		// The thread starter message ID equals the thread (channel) ID
		starterMsg, err := a.session.ChannelMessage(channel.ParentID, channelID)
		if err == nil && starterMsg != nil {
			author := "unknown"
			if starterMsg.Author != nil {
				author = starterMsg.Author.Username
			}
			sb.WriteString(fmt.Sprintf("Original post by @%s (%s):\n  %s\n\n",
				author, starterMsg.Timestamp.Format("2006-01-02 15:04"), starterMsg.Content))
		}
	}

	// Fetch recent messages
	messages, err := a.session.ChannelMessages(channelID, limit, "", "", "")
	if err != nil {
		return "", fmt.Errorf("failed to fetch channel messages: %w", err)
	}

	if len(messages) > 0 {
		sb.WriteString("Recent messages (oldest first):\n")
		// ChannelMessages returns newest first, so reverse
		for i := len(messages) - 1; i >= 0; i-- {
			msg := messages[i]
			author := "unknown"
			if msg.Author != nil {
				author = msg.Author.Username
			}
			content := msg.Content
			if len(content) > 300 {
				content = content[:300] + "..."
			}
			sb.WriteString(fmt.Sprintf("  @%s (%s): %s\n",
				author, msg.Timestamp.Format("15:04"), content))
		}
	}

	sb.WriteString("[END THREAD CONTEXT]\n\n")
	return sb.String(), nil
}

func isThreadChannel(ct discordgo.ChannelType) bool {
	return ct == discordgo.ChannelTypeGuildPublicThread ||
		ct == discordgo.ChannelTypeGuildPrivateThread ||
		ct == discordgo.ChannelTypeGuildNewsThread
}

func isBotMentioned(mentions []*discordgo.User, botID string) bool {
	for _, u := range mentions {
		if u.ID == botID {
			return true
		}
	}
	return false
}

func toSet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}
