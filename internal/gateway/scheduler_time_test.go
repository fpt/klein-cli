package gateway

import (
	"os"
	"testing"
	"time"
)

func TestNextDailyFire(t *testing.T) {
	jst, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatalf("load JST: %v", err)
	}

	// now = 2026-06-26 06:00 JST → next 08:00 is later the same day.
	now := time.Date(2026, 6, 26, 6, 0, 0, 0, jst)
	got, err := nextDailyFire(now, "08:00", "Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 6, 26, 8, 0, 0, 0, jst)
	if !got.Equal(want) {
		t.Errorf("morning: got %v, want %v", got, want)
	}

	// now = 2026-06-26 09:00 JST → 08:00 already passed → next is tomorrow.
	now = time.Date(2026, 6, 26, 9, 0, 0, 0, jst)
	got, _ = nextDailyFire(now, "08:00", "Asia/Tokyo")
	want = time.Date(2026, 6, 27, 8, 0, 0, 0, jst)
	if !got.Equal(want) {
		t.Errorf("afternoon: got %v, want %v", got, want)
	}

	// Exactly at the fire time → next is tomorrow (strictly after now).
	now = time.Date(2026, 6, 26, 8, 0, 0, 0, jst)
	got, _ = nextDailyFire(now, "08:00", "Asia/Tokyo")
	if !got.After(now) || got.Day() != 27 {
		t.Errorf("exact: got %v, want next day", got)
	}
}

func TestNextDailyFireErrors(t *testing.T) {
	now := time.Now()
	for _, bad := range []struct{ at, tz string }{
		{"25:00", "Asia/Tokyo"},  // hour out of range
		{"08:70", "Asia/Tokyo"},  // minute out of range
		{"8am", "Asia/Tokyo"},    // wrong format
		{"08:00", "Mars/Phobos"}, // bad timezone
	} {
		if _, err := nextDailyFire(now, bad.at, bad.tz); err == nil {
			t.Errorf("expected error for at=%q tz=%q", bad.at, bad.tz)
		}
	}
}

func TestNextWait(t *testing.T) {
	// Interval mode floors sub-5m to 5m.
	d, err := nextWait(time.Now(), ScheduleConfig{Interval: "1m"})
	if err != nil || d != 5*time.Minute {
		t.Errorf("floor: got %v err %v, want 5m", d, err)
	}
	d, err = nextWait(time.Now(), ScheduleConfig{Interval: "6h"})
	if err != nil || d != 6*time.Hour {
		t.Errorf("interval: got %v err %v, want 6h", d, err)
	}
	// At mode yields a positive wait (< 24h).
	d, err = nextWait(time.Now(), ScheduleConfig{At: "08:00", Timezone: "Asia/Tokyo"})
	if err != nil || d <= 0 || d > 24*time.Hour {
		t.Errorf("at-mode wait out of range: got %v err %v", d, err)
	}
}

func TestLoadScheduleStore(t *testing.T) {
	dir := t.TempDir()
	// Missing file → nil, no error.
	if got, err := loadScheduleStore(dir + "/missing.json"); err != nil || got != nil {
		t.Errorf("missing: got %v err %v", got, err)
	}
	// Valid array round-trips.
	p := dir + "/schedules.json"
	if err := os.WriteFile(p, []byte(`[{"name":"m","enabled":true,"at":"08:00","timezone":"Asia/Tokyo","prompt":"hi","skill":"claw","channel_type":"discord","channel_id":"1"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadScheduleStore(p)
	if err != nil || len(got) != 1 || got[0].At != "08:00" || got[0].ChannelID != "1" {
		t.Errorf("load: got %+v err %v", got, err)
	}
}
