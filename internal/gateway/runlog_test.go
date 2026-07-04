package gateway

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestAppendRunLog(t *testing.T) {
	dir := t.TempDir()
	m := NewMemoryManager(MemoryConfig{BaseDir: dir})

	at := time.Date(2026, 7, 3, 8, 0, 0, 0, time.UTC)
	if err := m.AppendRunLog(at, "morning-market", "report", "日経平均は上昇。半導体が牽引。"); err != nil {
		t.Fatal(err)
	}
	// Second run the same day appends to the same file.
	at2 := time.Date(2026, 7, 3, 16, 0, 0, 0, time.UTC)
	if err := m.AppendRunLog(at2, "market-summary", "report", "Closing summary."); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(m.RunLogPath(at))
	if err != nil {
		t.Fatalf("run log not created: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"## 08:00 morning-market (skill: report)",
		"日経平均は上昇。半導体が牽引。",
		"## 16:00 market-summary (skill: report)",
		"Closing summary.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("run log missing %q:\n%s", want, got)
		}
	}
	// Ordered: morning entry before evening entry.
	if strings.Index(got, "morning-market") > strings.Index(got, "market-summary") {
		t.Error("entries out of order")
	}
	// Different day → different file.
	nextDay := time.Date(2026, 7, 4, 8, 0, 0, 0, time.UTC)
	if m.RunLogPath(nextDay) == m.RunLogPath(at) {
		t.Error("run log path should be per-day")
	}
}
