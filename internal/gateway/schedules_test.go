package gateway

import (
	"context"
	"os"
	"testing"
	"time"

	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

func newTestLogger() *pkgLogger.Logger {
	return pkgLogger.NewLogger(pkgLogger.LogLevelError)
}

// drainOne waits up to timeout for a message on bus.Inbound and returns it.
// Returns ok=false on timeout.
func drainOne(bus *MessageBus, timeout time.Duration) (InboundMessage, bool) {
	select {
	case m := <-bus.Inbound:
		return m, true
	case <-time.After(timeout):
		return InboundMessage{}, false
	}
}

// TestScheduler_RunAtStart confirms RunAtStart fires an immediate message
// without waiting for the first tick. This is the path scheduled data-
// collection jobs use when an operator wants to bootstrap the store right
// after the gateway starts.
func TestScheduler_RunAtStart(t *testing.T) {
	bus := NewMessageBus(8)
	s := NewScheduler([]ScheduleConfig{
		{
			Name:       "eh-bootstrap",
			Enabled:    true,
			Cron:       "0 * * * *",
			Timezone:   "Asia/Tokyo",
			Prompt:     "Run ResearcherFetch then ResearcherAnalyze.",
			Skill:      "market-narratives",
			Silent:     true,
			RunAtStart: true,
		},
	}, bus, newTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Start(ctx)

	msg, ok := drainOne(bus, 1*time.Second)
	if !ok {
		t.Fatal("expected immediate message from RunAtStart, got none")
	}
	if !msg.Silent {
		t.Error("Silent flag should propagate from ScheduleConfig to InboundMessage")
	}
	if msg.Skill != "market-narratives" {
		t.Errorf("skill: got %q want %q", msg.Skill, "market-narratives")
	}
	if msg.PeerID != "scheduler:eh-bootstrap" {
		t.Errorf("peer id: got %q", msg.PeerID)
	}
}

// TestScheduler_DisabledOrEmpty confirms the scheduler is a no-op when no
// schedules are enabled. Important so misconfigured deployments don't leak
// background goroutines.
func TestScheduler_DisabledOrEmpty(t *testing.T) {
	bus := NewMessageBus(8)
	s := NewScheduler([]ScheduleConfig{
		{Name: "off", Enabled: false, Cron: "0 8 * * *", Timezone: "Asia/Tokyo", Prompt: "x"},
		{Name: "empty-prompt", Enabled: true, Cron: "0 8 * * *", Timezone: "Asia/Tokyo", Prompt: ""},
		// Retired timing shapes: no cron, or cron without timezone → skipped with a warning.
		{Name: "no-cron", Enabled: true, Prompt: "x"},
		{Name: "no-tz", Enabled: true, Cron: "0 8 * * *", Prompt: "x"},
	}, bus, newTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		s.Start(ctx)
		close(done)
	}()
	// Both schedules are no-ops, so Start should return immediately.
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Start did not return for an empty schedule set")
	}

	// No messages should have been emitted.
	if _, ok := drainOne(bus, 50*time.Millisecond); ok {
		t.Error("expected zero messages from disabled schedules")
	}
}

// TestConfigIgnoresRetiredHeartbeat confirms an old config.json containing the
// retired heartbeat block still parses (unknown JSON fields are ignored) and
// produces no schedules from it.
func TestConfigIgnoresRetiredHeartbeat(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/config.json"
	cfgJSON := `{
		"agent_addr": "http://localhost:50051",
		"heartbeat": {"enabled": true, "interval": "24h", "prompt": "Daily digest", "channel_id": "1"}
	}`
	if err := os.WriteFile(p, []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadGatewayConfig(p)
	if err != nil {
		t.Fatalf("old config with heartbeat should still parse: %v", err)
	}
	if len(cfg.Schedules) != 0 {
		t.Errorf("retired heartbeat must not produce schedules: %+v", cfg.Schedules)
	}
}
