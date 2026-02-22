package gateway

import (
	"context"
	"time"

	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// HeartbeatConfig defines periodic prompt execution.
type HeartbeatConfig struct {
	Enabled     bool   `json:"enabled"`
	Interval    string `json:"interval"` // Go duration string, e.g. "24h", "1h"
	Prompt      string `json:"prompt"`
	Skill       string `json:"skill"`
	ChannelType string `json:"channel_type"`
	ChannelID   string `json:"channel_id"`
}

// Heartbeat runs periodic prompts through the agent.
type Heartbeat struct {
	config HeartbeatConfig
	bus    *MessageBus
	logger *pkgLogger.Logger
}

// NewHeartbeat creates a heartbeat service.
func NewHeartbeat(cfg HeartbeatConfig, bus *MessageBus, logger *pkgLogger.Logger) *Heartbeat {
	return &Heartbeat{
		config: cfg,
		bus:    bus,
		logger: logger.WithComponent("heartbeat"),
	}
}

// Start runs the heartbeat ticker loop. Blocks until ctx is cancelled.
func (h *Heartbeat) Start(ctx context.Context) {
	if !h.config.Enabled || h.config.Prompt == "" {
		return
	}

	interval, err := time.ParseDuration(h.config.Interval)
	if err != nil || interval < 5*time.Minute {
		interval = 24 * time.Hour
	}

	h.logger.Info("Heartbeat started", "interval", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.execute()
		}
	}
}

func (h *Heartbeat) execute() {
	h.logger.Info("Executing heartbeat prompt")
	h.bus.Inbound <- InboundMessage{
		ChannelType: h.config.ChannelType,
		ChannelID:   h.config.ChannelID,
		PeerID:      "heartbeat",
		PeerName:    "Heartbeat",
		Text:        h.config.Prompt,
		Timestamp:   time.Now(),
	}
}
