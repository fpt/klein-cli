package gateway

import (
	"os"
	"testing"
	"time"
)

func TestNextWait(t *testing.T) {
	// Cron mode yields a positive wait (< 24h for a daily expression).
	d, err := nextWait(time.Now(), ScheduleConfig{Cron: "0 8 * * *", Timezone: "Asia/Tokyo"})
	if err != nil || d <= 0 || d > 24*time.Hour {
		t.Errorf("cron wait out of range: got %v err %v", d, err)
	}
	// Missing timezone is an error (schedules must not silently use server-local).
	if _, err := nextWait(time.Now(), ScheduleConfig{Cron: "0 8 * * *"}); err == nil {
		t.Error("missing timezone should error")
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
	if err := os.WriteFile(p, []byte(`[{"name":"m","enabled":true,"cron":"0 8 * * 1-5","timezone":"Asia/Tokyo","prompt":"hi","skill":"report","channel_type":"discord","channel_id":"1"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadScheduleStore(p)
	if err != nil || len(got) != 1 || got[0].Cron != "0 8 * * 1-5" || got[0].ChannelID != "1" {
		t.Errorf("load: got %+v err %v", got, err)
	}
}

func TestNextCronFire(t *testing.T) {
	jst, _ := time.LoadLocation("Asia/Tokyo")
	weekdays8 := "0 8 * * 1-5"

	// Friday 2026-07-03 09:00 JST → next weekday 08:00 is Monday 07-06.
	now := time.Date(2026, 7, 3, 9, 0, 0, 0, jst) // Friday, past 08:00
	got, err := nextCronFire(now, weekdays8, "Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 7, 6, 8, 0, 0, 0, jst) // Monday
	if !got.Equal(want) {
		t.Errorf("friday-after: got %v, want %v (Monday — weekend must be skipped)", got, want)
	}

	// Saturday → Monday.
	now = time.Date(2026, 7, 4, 7, 0, 0, 0, jst) // Saturday morning
	got, _ = nextCronFire(now, weekdays8, "Asia/Tokyo")
	if !got.Equal(want) {
		t.Errorf("saturday: got %v, want Monday %v", got, want)
	}

	// Thursday before 08:00 → same day.
	now = time.Date(2026, 7, 2, 6, 0, 0, 0, jst)
	got, _ = nextCronFire(now, weekdays8, "Asia/Tokyo")
	if !got.Equal(time.Date(2026, 7, 2, 8, 0, 0, 0, jst)) {
		t.Errorf("thursday-before: got %v", got)
	}

	// Bad expression / timezone error out.
	if _, err := nextCronFire(now, "nope", "Asia/Tokyo"); err == nil {
		t.Error("bad cron should error")
	}
	if _, err := nextCronFire(now, weekdays8, "Mars/Phobos"); err == nil {
		t.Error("bad tz should error")
	}
}

func TestNextWaitWeekendSkip(t *testing.T) {
	// Friday 09:00 UTC with a weekday cron → next fire is Monday 08:00 (~71h).
	now := time.Date(2026, 7, 3, 9, 0, 0, 0, time.UTC) // Friday
	d, err := nextWait(now, ScheduleConfig{Cron: "0 8 * * 1-5", Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if d < 48*time.Hour {
		t.Errorf("weekend should be skipped; wait = %v", d)
	}
}
