package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// ScheduleConfig defines one recurring agent invocation. A schedule fires
// either on a fixed Interval (Go duration) OR daily at a wall-clock time (At +
// Timezone) when At is set. Multiple schedules run in the same gateway, each on
// its own goroutine.
type ScheduleConfig struct {
	Name    string `json:"name"` // Human-readable id (unique; used for logs + reconciliation)
	Enabled bool   `json:"enabled"`
	// Cron is a standard 5-field cron expression ("0 8 * * 1-5" = weekdays at
	// 08:00), evaluated in Timezone. Required — the legacy at/interval timing
	// fields are retired ("08:00 daily" = "0 8 * * *"; "every 6h" = "0 */6 * * *").
	Cron string `json:"cron"`
	// Timezone is a required IANA name ("Asia/Tokyo") that Cron is evaluated
	// in. Required so schedules never silently depend on server-local time.
	Timezone    string `json:"timezone"`
	Prompt      string `json:"prompt"`       // The user-message the scheduled agent will see
	Skill       string `json:"skill"`        // Skill to invoke under (e.g. "report")
	Silent      bool   `json:"silent"`       // If true, run the prompt but never post the response
	ChannelType string `json:"channel_type"` // Output channel — required unless Silent
	ChannelID   string `json:"channel_id"`
	RunAtStart  bool   `json:"run_at_start"` // If true, fire once immediately when (re)started
}

// scheduleSig is a change signature: reconciliation restarts a job only when
// one of these fields changes.
func scheduleSig(c ScheduleConfig) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%t|%s|%s", c.Cron, c.Timezone,
		c.Prompt, c.Skill, c.ChannelType, c.Silent, c.ChannelID, c.Name)
}

// Scheduler owns a set of schedules and runs each on its own goroutine. It
// supports a static set (from config) plus a dynamic store file (written by the
// agent's Schedule* tools) that it polls and reconciles without a restart.
type Scheduler struct {
	bus    *MessageBus
	logger *pkgLogger.Logger

	static       []ScheduleConfig // from config.json (+ legacy heartbeat)
	storePath    string           // optional dynamic store file (JSON array)
	pollInterval time.Duration

	mu      sync.Mutex
	running map[string]runningJob // by schedule name
}

type runningJob struct {
	cancel context.CancelFunc
	sig    string
}

// NewScheduler builds a scheduler with the static (config) schedules.
func NewScheduler(static []ScheduleConfig, bus *MessageBus, logger *pkgLogger.Logger) *Scheduler {
	return &Scheduler{
		bus:          bus,
		logger:       logger.WithComponent("scheduler"),
		static:       static,
		pollInterval: 20 * time.Second,
		running:      make(map[string]runningJob),
	}
}

// SetStorePath enables the dynamic store file (agent-created schedules). The
// gateway sets this to ~/.klein/claw/schedules.json.
func (s *Scheduler) SetStorePath(path string) { s.storePath = path }

// Start reconciles the initial schedule set, then polls the store file for
// changes and reconciles live. Blocks until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	s.reconcile(ctx, s.desired())

	// Without a dynamic store there is nothing to watch. Any jobs started above
	// keep running on their own goroutines (tied to ctx), so just return —
	// this keeps an empty/static-only scheduler from blocking a goroutine.
	if s.storePath == "" {
		return
	}

	var lastMod time.Time
	if fi, err := os.Stat(s.storePath); err == nil {
		lastMod = fi.ModTime()
	}
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.stopAll()
			return
		case <-ticker.C:
			fi, err := os.Stat(s.storePath)
			if err != nil {
				continue // store may not exist yet
			}
			if fi.ModTime().After(lastMod) {
				lastMod = fi.ModTime()
				s.logger.Info("Schedule store changed; reconciling", "path", s.storePath)
				s.reconcile(ctx, s.desired())
			}
		}
	}
}

// desired returns the merged, enabled schedule set (static config + store).
func (s *Scheduler) desired() []ScheduleConfig {
	out := append([]ScheduleConfig(nil), s.static...)
	if s.storePath != "" {
		if loaded, err := loadScheduleStore(s.storePath); err != nil {
			s.logger.Warn("Failed to read schedule store", "path", s.storePath, "error", err)
		} else {
			out = append(out, loaded...)
		}
	}
	return out
}

// reconcile starts jobs that are newly-present or changed and stops jobs that
// were removed or changed. Parent ctx cancellation stops everything.
func (s *Scheduler) reconcile(parent context.Context, desired []ScheduleConfig) {
	wanted := make(map[string]ScheduleConfig, len(desired))
	for _, c := range desired {
		if !c.Enabled || c.Prompt == "" || c.Name == "" {
			continue
		}
		// Cron + timezone are both required: cron alone would silently depend
		// on server-local time (the at/interval timing fields are retired).
		if c.Cron == "" || c.Timezone == "" {
			s.logger.Warn("Schedule skipped: cron and timezone are required (at/interval are no longer supported — convert to a cron expression)",
				"schedule", c.Name, "cron", c.Cron, "timezone", c.Timezone)
			continue
		}
		wanted[c.Name] = c // later entries (store) override earlier (static) by name
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Stop removed or changed jobs.
	for name, job := range s.running {
		if w, ok := wanted[name]; !ok || scheduleSig(w) != job.sig {
			job.cancel()
			delete(s.running, name)
			s.logger.Info("Schedule stopped", "schedule", name)
		}
	}
	// Start new (or restarted) jobs.
	for name, c := range wanted {
		if _, ok := s.running[name]; ok {
			continue
		}
		jctx, cancel := context.WithCancel(parent)
		s.running[name] = runningJob{cancel: cancel, sig: scheduleSig(c)}
		go s.runOne(jctx, c)
	}
}

func (s *Scheduler) stopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, job := range s.running {
		job.cancel()
		delete(s.running, name)
	}
}

func (s *Scheduler) runOne(ctx context.Context, cfg ScheduleConfig) {
	s.logger.Info("Schedule started", "schedule", cfg.Name,
		"cron", cfg.Cron, "timezone", cfg.Timezone, "skill", cfg.Skill, "silent", cfg.Silent)

	if cfg.RunAtStart {
		s.fire(cfg)
	}

	for {
		wait, err := nextWait(time.Now(), cfg)
		if err != nil {
			s.logger.Warn("Schedule timing error; retrying in 1h", "schedule", cfg.Name, "error", err)
			wait = time.Hour
		}
		select {
		case <-ctx.Done():
			s.logger.Info("Schedule stopped", "schedule", cfg.Name)
			return
		case <-time.After(wait):
			s.fire(cfg)
		}
	}
}

// nextWait computes the delay until the schedule's next cron fire from now.
func nextWait(now time.Time, cfg ScheduleConfig) (time.Duration, error) {
	next, err := nextCronFire(now, cfg.Cron, cfg.Timezone)
	if err != nil {
		return 0, err
	}
	return next.Sub(now), nil
}

// nextCronFire returns the next fire time of a standard 5-field cron expression
// (minute hour dom month dow), evaluated in the named timezone, strictly after
// now. Example: "0 8 * * 1-5" = weekdays 08:00.
func nextCronFire(now time.Time, expr, tzName string) (time.Time, error) {
	if tzName == "" {
		return time.Time{}, fmt.Errorf("timezone is required for cron schedules")
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return time.Time{}, fmt.Errorf("bad timezone %q: %w", tzName, err)
	}
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return time.Time{}, fmt.Errorf("bad cron expression %q: %w", expr, err)
	}
	next := sched.Next(now.In(loc))
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("cron expression %q never fires", expr)
	}
	return next, nil
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

// loadScheduleStore reads the dynamic schedule store (a JSON array of
// ScheduleConfig). A missing file is not an error (returns nil).
func loadScheduleStore(path string) ([]ScheduleConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	data = trimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	var out []ScheduleConfig
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("invalid schedule store JSON: %w", err)
	}
	return out, nil
}

func trimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\t' || b[i] == '\r') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\t' || b[j-1] == '\r') {
		j--
	}
	return b[i:j]
}
