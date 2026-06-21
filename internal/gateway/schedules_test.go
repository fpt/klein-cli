package gateway

import (
	"context"
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
			Interval:   "1h",
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
		{Name: "off", Enabled: false, Interval: "1h", Prompt: "x"},
		{Name: "empty-prompt", Enabled: true, Interval: "1h", Prompt: ""},
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

// TestHeartbeatToSchedule_LegacyConversion confirms an old-style heartbeat
// configuration round-trips into a ScheduleConfig with the right fields and
// is NOT silent (legacy heartbeats always posted to a channel).
func TestHeartbeatToSchedule_LegacyConversion(t *testing.T) {
	got, ok := HeartbeatToSchedule(HeartbeatConfig{
		Enabled:     true,
		Interval:    "24h",
		Prompt:      "Daily digest",
		Skill:       "claw",
		ChannelType: "discord",
		ChannelID:   "12345",
	})
	if !ok {
		t.Fatal("expected legacy heartbeat to convert; got !ok")
	}
	if got.Name != "heartbeat" {
		t.Errorf("name: %q", got.Name)
	}
	if got.Silent {
		t.Error("legacy heartbeat must NOT be silent (always posted to channel)")
	}
	if got.ChannelID != "12345" {
		t.Errorf("channel id: %q", got.ChannelID)
	}

	// A disabled or empty-prompt heartbeat should NOT convert.
	if _, ok := HeartbeatToSchedule(HeartbeatConfig{Enabled: false, Prompt: "x"}); ok {
		t.Error("disabled heartbeat should not convert")
	}
	if _, ok := HeartbeatToSchedule(HeartbeatConfig{Enabled: true, Prompt: ""}); ok {
		t.Error("empty-prompt heartbeat should not convert")
	}
}
