package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/bwmarrin/discordgo"

	"github.com/fpt/klein-cli/internal/tool"
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

	// Download image attachments
	var images [][]byte
	for _, att := range m.Attachments {
		if !isImageContentType(att.ContentType) {
			continue
		}
		data, err := downloadAttachment(att.URL)
		if err != nil {
			a.logger.Warn("Failed to download attachment", "url", att.URL, "error", err)
			continue
		}
		// Resize to keep gRPC payload and LLM context small
		resized, err := tool.ResizeImageToJPEG(data, tool.MaxImageDim, tool.MaxJPEGQuality)
		if err != nil {
			// Fallback to raw data if decode fails (e.g., GIF, SVG)
			images = append(images, data)
			continue
		}
		images = append(images, resized)
	}

	if text == "" && len(images) == 0 {
		return
	}
	if text == "" {
		text = "Please analyze the attached image(s)."
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
		Images:      images,
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
// The first chunk is sent as a reply to the original message (if ReplyToID is set);
// subsequent chunks are sent as plain follow-up messages.
func (a *DiscordAdapter) Send(ctx context.Context, msg OutboundMessage) error {
	chunks := splitMessage(msg.Text, 2000)
	for i, chunk := range chunks {
		var err error
		if i == 0 && msg.ReplyToID != "" {
			ref := &discordgo.MessageReference{MessageID: msg.ReplyToID, ChannelID: msg.ChannelID}
			_, err = a.session.ChannelMessageSendReply(msg.ChannelID, chunk, ref)
		} else {
			_, err = a.session.ChannelMessageSend(msg.ChannelID, chunk)
		}
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

// isImageContentType checks if the content type is an image.
func isImageContentType(ct string) bool {
	return strings.HasPrefix(ct, "image/")
}

// downloadAttachment downloads a Discord attachment URL and returns the raw bytes.
func downloadAttachment(url string) ([]byte, error) {
	resp, err := http.Get(url) //nolint:gosec // Discord CDN URL
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(tool.MaxImageBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("read failed: %w", err)
	}
	if len(data) > tool.MaxImageBytes {
		return nil, fmt.Errorf("attachment exceeds %dMB size limit", tool.MaxImageBytes/1024/1024)
	}
	return data, nil
}
