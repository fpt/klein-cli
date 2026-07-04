package gateway

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSchedulerReconcilesFromStore proves the end-to-end store path: a schedule
// written to the JSON store (the same file the agent's Schedule* tools write) is
// loaded, reconciled, and fired by the scheduler.
func TestSchedulerReconcilesFromStore(t *testing.T) {
	store := filepath.Join(t.TempDir(), "schedules.json")
	// Shape matches internal/tool.scheduleEntry (shared JSON contract).
	if err := os.WriteFile(store, []byte(`[
		{"name":"boot","enabled":true,"cron":"0 * * * *","timezone":"Asia/Tokyo","prompt":"go",
		 "skill":"claw","channel_type":"discord","channel_id":"9","run_at_start":true}
	]`), 0o644); err != nil {
		t.Fatal(err)
	}

	bus := NewMessageBus(8)
	s := NewScheduler(nil, bus, newTestLogger())
	s.SetStorePath(store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Start(ctx)

	msg, ok := drainOne(bus, 1*time.Second)
	if !ok {
		t.Fatal("expected a fired message from the store schedule (run_at_start)")
	}
	if msg.ChannelID != "9" || msg.Skill != "claw" || msg.PeerID != "scheduler:boot" {
		t.Errorf("unexpected fired message: %+v", msg)
	}
}
