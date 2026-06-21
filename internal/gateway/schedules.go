package gateway

import (
	"context"
	"fmt"
	"sync"
	"time"

	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// ScheduleConfig defines one recurring agent invocation. Multiple schedules
// can run in the same gateway — each owns its own ticker goroutine. This is
// the right abstraction for time-series collection: a fetcher that runs
// every hour and silently stores events, plus a separate daily digest that
// posts to Discord.
type ScheduleConfig struct {
	Name        string `json:"name"` // Human-readable id for logs (e.g. "researcher-fetch")
	Enabled     bool   `json:"enabled"`
	Interval    string `json:"interval"`     // Go duration string, e.g. "6h", "30m"
	Prompt      string `json:"prompt"`       // The user-message the scheduled agent will see
	Skill       string `json:"skill"`        // Skill to invoke under (e.g. "market-narratives")
	Silent      bool   `json:"silent"`       // If true, run the prompt but never post the response
	ChannelType string `json:"channel_type"` // Output channel — only used when Silent is false
	ChannelID   string `json:"channel_id"`
	RunAtStart  bool   `json:"run_at_start"` // If true, fire once immediately on gateway startup
}

// Scheduler owns a list of ScheduleConfig and runs each on its own ticker.
type Scheduler struct {
	schedules []ScheduleConfig
	bus       *MessageBus
	logger    *pkgLogger.Logger
}

// NewScheduler builds a scheduler. Pass the merged list of explicit Schedules
// plus the legacy single-Heartbeat entry (when the user is still on the old
// config shape).
func NewScheduler(schedules []ScheduleConfig, bus *MessageBus, logger *pkgLogger.Logger) *Scheduler {
	return &Scheduler{
		schedules: schedules,
		bus:       bus,
		logger:    logger.WithComponent("scheduler"),
	}
}

// Start fans out one ticker goroutine per enabled schedule. Blocks until ctx
// is cancelled. Returns immediately when there are no enabled schedules.
func (s *Scheduler) Start(ctx context.Context) {
	enabled := s.enabled()
	if len(enabled) == 0 {
		return
	}

	s.logger.Info("Starting scheduler", "jobs", len(enabled))
	var wg sync.WaitGroup
	for _, cfg := range enabled {
		wg.Add(1)
		go func(c ScheduleConfig) {
			defer wg.Done()
			s.runOne(ctx, c)
		}(cfg)
	}
	wg.Wait()
}

// HeartbeatToSchedule converts the legacy single-heartbeat config shape into
// a ScheduleConfig so users on the old config keep working unchanged. Empty
// (disabled) heartbeats return false.
func HeartbeatToSchedule(h HeartbeatConfig) (ScheduleConfig, bool) {
	if !h.Enabled || h.Prompt == "" {
		return ScheduleConfig{}, false
	}
	return ScheduleConfig{
		Name:        "heartbeat",
		Enabled:     true,
		Interval:    h.Interval,
		Prompt:      h.Prompt,
		Skill:       h.Skill,
		ChannelType: h.ChannelType,
		ChannelID:   h.ChannelID,
		// Legacy heartbeat always posted to a channel — preserve that.
		Silent: false,
	}, true
}

func (s *Scheduler) enabled() []ScheduleConfig {
	out := make([]ScheduleConfig, 0, len(s.schedules))
	for _, c := range s.schedules {
		if !c.Enabled || c.Prompt == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (s *Scheduler) runOne(ctx context.Context, cfg ScheduleConfig) {
	interval, err := time.ParseDuration(cfg.Interval)
	if err != nil {
		s.logger.Warn("Schedule has unparseable interval; using 1h",
			"schedule", cfg.Name, "interval", cfg.Interval, "error", err)
		interval = time.Hour
	}
	// Floor at 5 minutes — anything shorter is almost certainly a typo and
	// would hammer the LLM provider. Tests can pass shorter values directly
	// via ScheduleConfig{Interval: "..."} and observe the same floor.
	if interval < 5*time.Minute {
		s.logger.Warn("Schedule interval too short; flooring to 5m",
			"schedule", cfg.Name, "requested", interval)
		interval = 5 * time.Minute
	}

	logArgs := []any{
		"schedule", cfg.Name,
		"interval", interval,
		"skill", cfg.Skill,
		"silent", cfg.Silent,
	}
	if !cfg.Silent {
		logArgs = append(logArgs, "channel_type", cfg.ChannelType, "channel_id", cfg.ChannelID)
	}
	s.logger.Info("Schedule started", logArgs...)

	if cfg.RunAtStart {
		s.fire(cfg)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Schedule stopped", "schedule", cfg.Name)
			return
		case <-ticker.C:
			s.fire(cfg)
		}
	}
}

func (s *Scheduler) fire(cfg ScheduleConfig) {
	s.logger.Info("Firing schedule", "schedule", cfg.Name, "silent", cfg.Silent)
	s.bus.Inbound <- InboundMessage{
		ChannelType: cfg.ChannelType,
		ChannelID:   cfg.ChannelID,
		PeerID:      "scheduler:" + cfg.Name,
		PeerName:    fmt.Sprintf("scheduler[%s]", cfg.Name),
		Text:        cfg.Prompt,
		Timestamp:   time.Now(),
		Silent:      cfg.Silent,
		Skill:       cfg.Skill,
	}
}
