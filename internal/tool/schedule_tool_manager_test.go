package tool

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

func TestScheduleCreateListDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.json")
	m := NewScheduleToolManager(path)
	ctx := context.Background()

	// Create a daily 08:00 JST schedule.
	r, _ := m.CallTool(ctx, "ScheduleCreate", message.ToolArgumentValues{
		"name": "morning-market", "prompt": "今朝のマーケットイベント",
		"at": "08:00", "timezone": "Asia/Tokyo",
		"channel_type": "discord", "channel_id": "123",
	})
	if r.Error != "" {
		t.Fatalf("create: %s", r.Error)
	}

	// List shows it.
	l, _ := m.CallTool(ctx, "ScheduleList", nil)
	if !strings.Contains(l.Text, "morning-market") || !strings.Contains(l.Text, "08:00") {
		t.Errorf("list missing entry: %q", l.Text)
	}

	// Reusing the name updates (not duplicates).
	_, _ = m.CallTool(ctx, "ScheduleCreate", message.ToolArgumentValues{
		"name": "morning-market", "prompt": "updated", "at": "09:00",
		"timezone": "Asia/Tokyo", "channel_type": "discord", "channel_id": "123",
	})
	entries, _ := m.load()
	if len(entries) != 1 || entries[0].At != "09:00" {
		t.Errorf("upsert failed: %+v", entries)
	}

	// Delete.
	d, _ := m.CallTool(ctx, "ScheduleDelete", message.ToolArgumentValues{"name": "morning-market"})
	if r.Error != "" {
		t.Fatalf("delete: %s", d.Error)
	}
	if e, _ := m.load(); len(e) != 0 {
		t.Errorf("expected empty after delete, got %+v", e)
	}
}

func TestScheduleCreateValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	m := NewScheduleToolManager(path)
	ctx := context.Background()

	cases := []struct {
		name string
		args message.ToolArgumentValues
	}{
		{"no timing", message.ToolArgumentValues{"name": "a", "prompt": "p", "channel_id": "1"}},
		{"both timing", message.ToolArgumentValues{"name": "a", "prompt": "p", "at": "08:00", "interval": "6h", "channel_id": "1"}},
		{"bad time", message.ToolArgumentValues{"name": "a", "prompt": "p", "at": "25:99", "channel_id": "1"}},
		{"no channel", message.ToolArgumentValues{"name": "a", "prompt": "p", "at": "08:00"}},
		{"no name", message.ToolArgumentValues{"prompt": "p", "at": "08:00", "channel_id": "1"}},
	}
	for _, tc := range cases {
		r, _ := m.CallTool(ctx, "ScheduleCreate", tc.args)
		if r.Error == "" {
			t.Errorf("%s: expected validation error, got success: %q", tc.name, r.Text)
		}
	}
}

func TestScheduleCreateInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	m := NewScheduleToolManager(path)
	r, _ := m.CallTool(context.Background(), "ScheduleCreate", message.ToolArgumentValues{
		"name": "poll", "prompt": "check CI", "interval": "6h",
		"channel_type": "discord", "channel_id": "9",
	})
	if r.Error != "" {
		t.Fatalf("interval create: %s", r.Error)
	}
	entries, _ := m.load()
	// Default skill is the headless "report" skill.
	if len(entries) != 1 || entries[0].Interval != "6h" || entries[0].Skill != "report" {
		t.Errorf("interval entry wrong: %+v", entries)
	}
}

func TestScheduleCreateCron(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	m := NewScheduleToolManager(path)
	ctx := context.Background()

	// Weekday-only cron schedule.
	r, _ := m.CallTool(ctx, "ScheduleCreate", message.ToolArgumentValues{
		"name": "weekday-briefing", "prompt": "今朝の日本・米国市場の主要イベントを簡潔にまとめて",
		"cron": "0 8 * * 1-5", "channel_type": "discord", "channel_id": "1",
	})
	if r.Error != "" {
		t.Fatalf("cron create: %s", r.Error)
	}
	entries, _ := m.load()
	if len(entries) != 1 || entries[0].Cron != "0 8 * * 1-5" || entries[0].Timezone != "Asia/Tokyo" {
		t.Errorf("cron entry wrong: %+v", entries)
	}

	// Invalid cron rejected.
	r, _ = m.CallTool(ctx, "ScheduleCreate", message.ToolArgumentValues{
		"name": "bad", "prompt": "do work", "cron": "not-a-cron", "channel_id": "1",
	})
	if r.Error == "" {
		t.Error("invalid cron should be rejected")
	}

	// cron + at together rejected.
	r, _ = m.CallTool(ctx, "ScheduleCreate", message.ToolArgumentValues{
		"name": "both", "prompt": "do work", "cron": "0 8 * * *", "at": "08:00", "channel_id": "1",
	})
	if r.Error == "" {
		t.Error("cron+at together should be rejected")
	}
}

func TestScheduleCreateRejectsMetaPrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	m := NewScheduleToolManager(path)
	ctx := context.Background()

	// Recurrence phrasing in the prompt (the observed failure) is rejected.
	for _, bad := range []string{
		"毎朝8時に、その日のマーケットに関係する重要イベントを短くまとめて送ってください。",
		"Every morning at 8, send me a market briefing.",
	} {
		r, _ := m.CallTool(ctx, "ScheduleCreate", message.ToolArgumentValues{
			"name": "x", "prompt": bad, "cron": "0 8 * * 1-5", "channel_id": "1",
		})
		if r.Error == "" {
			t.Errorf("meta prompt should be rejected: %q", bad)
		}
	}

	// A clean actionable prompt passes.
	r, _ := m.CallTool(ctx, "ScheduleCreate", message.ToolArgumentValues{
		"name": "ok", "prompt": "今朝の主要マーケットイベントをまとめて", "cron": "0 8 * * 1-5", "channel_id": "1",
	})
	if r.Error != "" {
		t.Errorf("actionable prompt rejected: %s", r.Error)
	}
}
