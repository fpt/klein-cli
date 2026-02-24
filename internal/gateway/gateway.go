package gateway

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"connectrpc.com/connect"
	agentv1 "github.com/fpt/klein-cli/internal/gen/agentv1"
	"github.com/fpt/klein-cli/internal/gen/agentv1/agentv1connect"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// Gateway is the main orchestrator for klein-claw.
type Gateway struct {
	config    *GatewayConfig
	bus       *MessageBus
	sessions  *SessionManager
	memory    *MemoryManager
	heartbeat *Heartbeat
	adapters  map[string]Adapter
	client    agentv1connect.AgentServiceClient
	logger    *pkgLogger.Logger
}

// NewGateway creates a gateway connected to the klein agent via Connect RPC.
func NewGateway(cfg *GatewayConfig, logger *pkgLogger.Logger) (*Gateway, error) {
	client := agentv1connect.NewAgentServiceClient(http.DefaultClient, cfg.AgentAddr)

	bus := NewMessageBus(64)
	sessions := NewSessionManager(client, cfg, logger)
	memory := NewMemoryManager(cfg.Memory)

	if err := memory.EnsureDirectories(); err != nil {
		logger.Warn("Failed to create memory directories", "error", err)
	}

	gw := &Gateway{
		config:   cfg,
		bus:      bus,
		sessions: sessions,
		memory:   memory,
		adapters: make(map[string]Adapter),
		client:   client,
		logger:   logger.WithComponent("gateway"),
	}

	// Initialize Discord adapter if configured
	if cfg.Discord.Token != "" {
		discord, err := NewDiscordAdapter(bus, cfg.Discord, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to create discord adapter: %w", err)
		}
		gw.adapters["discord"] = discord
	}

	gw.heartbeat = NewHeartbeat(cfg.Heartbeat, bus, logger)

	return gw, nil
}

// Run starts all adapters and processes messages. Blocks until ctx is cancelled.
func (gw *Gateway) Run(ctx context.Context) error {
	// Start adapters
	for name, a := range gw.adapters {
		gw.logger.Info("Starting adapter", "adapter", name)
		go func(n string, ad Adapter) {
			if err := ad.Start(ctx); err != nil {
				gw.logger.Error("Adapter failed", "adapter", n, "error", err)
			}
		}(name, a)
	}

	// Start heartbeat
	go gw.heartbeat.Start(ctx)

	// Start outbound dispatcher
	go gw.dispatchOutbound(ctx)

	gw.logger.Info("Gateway running, processing messages")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg := <-gw.bus.Inbound:
			go gw.handleInbound(ctx, msg)
		}
	}
}

func (gw *Gateway) handleInbound(ctx context.Context, msg InboundMessage) {
	// Handle commands
	if strings.HasPrefix(msg.Text, "!") {
		gw.handleCommand(ctx, msg)
		return
	}

	key := SessionKey{
		ChannelType: msg.ChannelType,
		ChannelID:   msg.ChannelID,
		PeerID:      msg.PeerID,
	}

	session, err := gw.sessions.GetOrCreateSession(ctx, key)
	if err != nil {
		gw.logger.Error("Failed to get session", "error", err, "peer", msg.PeerName)
		return
	}

	// Send typing indicator
	if a, ok := gw.adapters[msg.ChannelType]; ok {
		_ = a.SendTyping(ctx, msg.ChannelID)
	}

	// Inject session log file path if available
	enrichedText := msg.Text
	if gw.config.SessionsDir != "" {
		sessionFile := gw.sessionFilePath(key)
		if _, err := os.Stat(sessionFile); err == nil {
			enrichedText = fmt.Sprintf("[SESSION LOG: %s]\n", sessionFile) + enrichedText
		}
	}

	// Inject memory context
	if memoryPrompt := gw.memory.BuildMemoryPrompt(); memoryPrompt != "" {
		enrichedText = memoryPrompt + enrichedText
	}

	// Invoke agent via Connect RPC streaming
	stream, err := gw.client.Invoke(ctx, connect.NewRequest(&agentv1.InvokeRequest{
		SessionId:      session.AgentSessionID,
		Scenario:       session.Skill,
		UserInput:      enrichedText,
		EnableThinking: true,
		Images:         msg.Images,
	}))
	if err != nil {
		gw.logger.Error("Failed to invoke agent", "error", err)
		gw.sendError(msg, "Sorry, I encountered an error connecting to the agent.")
		return
	}
	defer stream.Close()

	// Consume stream, extract final response
	var responseText string
	for stream.Receive() {
		event := stream.Msg()
		switch e := event.Event.(type) {
		case *agentv1.InvokeEvent_Final:
			responseText = e.Final.Text
		case *agentv1.InvokeEvent_Status:
			// Refresh typing indicator on tool calls
			if e.Status.State == agentv1.InvokeState_RUN_TOOL {
				if a, ok := gw.adapters[msg.ChannelType]; ok {
					_ = a.SendTyping(ctx, msg.ChannelID)
				}
			}
		case *agentv1.InvokeEvent_Error:
			responseText = fmt.Sprintf("Error: %s", e.Error)
		}
	}
	if err := stream.Err(); err != nil {
		gw.logger.Error("Stream error", "error", err)
		gw.sendError(msg, "Sorry, I encountered an error processing your request.")
		return
	}

	if responseText != "" {
		gw.bus.Outbound <- OutboundMessage{
			ChannelType: msg.ChannelType,
			ChannelID:   msg.ChannelID,
			Text:        responseText,
			ReplyToID:   msg.ReplyToID,
		}
	}
}

func (gw *Gateway) handleCommand(ctx context.Context, msg InboundMessage) {
	parts := strings.Fields(msg.Text)
	cmd := strings.TrimPrefix(parts[0], "!")

	key := SessionKey{
		ChannelType: msg.ChannelType,
		ChannelID:   msg.ChannelID,
		PeerID:      msg.PeerID,
	}

	var response string
	switch cmd {
	case "clear":
		_ = gw.sessions.ClearSession(ctx, key)
		response = "Conversation cleared. Starting fresh."
	case "skill":
		if len(parts) > 1 {
			session, err := gw.sessions.GetOrCreateSession(ctx, key)
			if err == nil {
				session.mu.Lock()
				session.Skill = parts[1]
				session.mu.Unlock()
				response = fmt.Sprintf("Switched to skill: %s", parts[1])
			} else {
				response = fmt.Sprintf("Error: %v", err)
			}
		} else {
			response = "Usage: !skill <name>\nAvailable: code, respond, claw"
		}
	case "memory":
		mem, _ := gw.memory.GetMemoryContext()
		if mem == "" {
			response = "No memory stored yet."
		} else {
			if len(mem) > 1800 {
				mem = mem[:1800] + "\n...(truncated)"
			}
			response = fmt.Sprintf("**Current Memory:**\n\n%s", mem)
		}
	case "help":
		response = "**Available commands:**\n" +
			"`!clear` — Clear conversation\n" +
			"`!skill <name>` — Switch skill (code, respond, claw)\n" +
			"`!memory` — Show stored memory\n" +
			"`!help` — Show this help"
	default:
		response = fmt.Sprintf("Unknown command: !%s. Use !help for available commands.", cmd)
	}

	gw.bus.Outbound <- OutboundMessage{
		ChannelType: msg.ChannelType,
		ChannelID:   msg.ChannelID,
		Text:        response,
		ReplyToID:   msg.ReplyToID,
	}
}

func (gw *Gateway) dispatchOutbound(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-gw.bus.Outbound:
			if a, ok := gw.adapters[msg.ChannelType]; ok {
				if err := a.Send(ctx, msg); err != nil {
					gw.logger.Error("Failed to send outbound message", "error", err)
				}
			}
		}
	}
}

func (gw *Gateway) sendError(orig InboundMessage, text string) {
	gw.bus.Outbound <- OutboundMessage{
		ChannelType: orig.ChannelType,
		ChannelID:   orig.ChannelID,
		Text:        text,
		ReplyToID:   orig.ReplyToID,
	}
}

var gwNonAlphanumericRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// sessionFilePath computes the session persistence file path for a given key.
// Must match the sanitization logic in internal/connectrpc/server.go.
func (gw *Gateway) sessionFilePath(key SessionKey) string {
	safe := gwNonAlphanumericRe.ReplaceAllString(key.PersistenceKeyString(), "_")
	if len(safe) > 128 {
		safe = safe[:128]
	}
	return filepath.Join(gw.config.SessionsDir, safe+".json")
}

// Close shuts down all adapters.
func (gw *Gateway) Close() error {
	for _, a := range gw.adapters {
		_ = a.Stop()
	}
	return nil
}
