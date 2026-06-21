package duckdb

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildPrelude_FilesPresent confirms the prelude emits real
// read_json_auto views when both files exist on disk.
func TestBuildPrelude_FilesPresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(`{"id":"x","published_at":"2026-06-21T00:00:00Z"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "narratives.json"), []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := BuildPrelude(dir)
	if err != nil {
		t.Fatalf("BuildPrelude: %v", err)
	}
	for _, want := range []string{
		"SET TimeZone = 'Asia/Tokyo'",
		"CREATE OR REPLACE VIEW events",
		"CREATE OR REPLACE VIEW narratives",
		"events.jsonl",
		"format='nd'",    // newline-delimited for events
		"format='array'", // top-level array for narratives
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prelude missing %q\n--- got ---\n%s", want, got)
		}
	}
}

// TestBuildPrelude_MissingFilesCreateStubViews verifies that a totally empty
// data dir still produces a usable prelude — the views exist but are empty.
// This is the right UX for first-time users who try a query before
// ResearcherFetch.
func TestBuildPrelude_MissingFilesCreateStubViews(t *testing.T) {
	dir := t.TempDir() // empty
	got, err := BuildPrelude(dir)
	if err != nil {
		t.Fatalf("BuildPrelude: %v", err)
	}
	if !strings.Contains(got, "events.jsonl not found") {
		t.Error("prelude should comment when events.jsonl is absent")
	}
	if !strings.Contains(got, "WHERE 1 = 0") {
		t.Error("prelude should emit empty-view stubs (WHERE 1 = 0)")
	}
}

// TestBuildPrelude_EscapesSingleQuotes guards against a data_dir that
// contains a single quote breaking the SQL string literal.
func TestBuildPrelude_EscapesSingleQuotes(t *testing.T) {
	dir := t.TempDir()
	weird := filepath.Join(dir, "user's data")
	if err := os.MkdirAll(weird, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(weird, "events.jsonl"), []byte(`{}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := BuildPrelude(weird)
	if err != nil {
		t.Fatalf("BuildPrelude: %v", err)
	}
	// The path "user's data" should become "user''s data" inside the SQL literal.
	if !strings.Contains(got, "user''s data") {
		t.Errorf("single quote not escaped:\n%s", got)
	}
}

// TestQuery_LiveAgainstRealData runs an actual SQL query end-to-end. Skipped
// when duckdb isn't installed (CI without the CLI) so package tests stay
// green everywhere; locally on the dev machine duckdb is present so this
// exercises the real path.
func TestQuery_LiveAgainstRealData(t *testing.T) {
	if _, err := exec.LookPath("duckdb"); err != nil {
		t.Skip("duckdb CLI not installed; skipping live test")
	}

	dir := t.TempDir()
	// Three events spread over two days, with different intakes — enough
	// for a meaningful aggregation.
	events := `{"id":"a","source":"src-a","intake":"government-us","role":"signal","trust_tier":"primary","weight":1.0,"title":"A","url":"https://example.com/a","published_at":"2026-06-19T10:00:00Z","fetched_at":"2026-06-21T00:00:00Z"}
{"id":"b","source":"src-b","intake":"government-uk","role":"signal","trust_tier":"primary","weight":1.0,"title":"B","url":"https://example.com/b","published_at":"2026-06-19T12:00:00Z","fetched_at":"2026-06-21T00:00:00Z"}
{"id":"c","source":"src-c","intake":"corporate","role":"signal","trust_tier":"corporate","weight":0.7,"title":"C","url":"https://example.com/c","published_at":"2026-06-20T09:00:00Z","fetched_at":"2026-06-21T00:00:00Z"}
`
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "narratives.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := Query(context.Background(), dir,
		`SELECT DATE_TRUNC('day', published_at) AS day, COUNT(*) AS n
		 FROM events
		 GROUP BY day
		 ORDER BY day;`)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	// Both day buckets should appear.
	if !strings.Contains(out, "2026-06-19") || !strings.Contains(out, "2026-06-20") {
		t.Errorf("expected both days in output, got:\n%s", out)
	}
	// Day 1 has 2 events, day 2 has 1.
	if !strings.Contains(out, "| 2 ") || !strings.Contains(out, "| 1 ") {
		t.Errorf("expected counts of 2 and 1 in output, got:\n%s", out)
	}
}

// TestQuery_TimestampsAreUsableWithoutCast is a regression for the
// "DATE_TRUNC on VARCHAR" papercut: read_json_auto reads ISO timestamps as
// VARCHAR, so the prelude must cast published_at/fetched_at in the view.
// This test verifies a DATE_TRUNC query works without an explicit ::TIMESTAMP
// in the user's SQL.
func TestQuery_TimestampsAreUsableWithoutCast(t *testing.T) {
	if _, err := exec.LookPath("duckdb"); err != nil {
		t.Skip("duckdb CLI not installed; skipping live test")
	}
	dir := t.TempDir()
	events := `{"id":"a","intake":"government-us","published_at":"2026-06-19T10:00:00Z","fetched_at":"2026-06-21T00:00:00Z"}
{"id":"b","intake":"government-us","published_at":"2026-06-19T11:00:00Z","fetched_at":"2026-06-21T00:00:00Z"}
`
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}

	// Note: no ::TIMESTAMP cast in the user SQL. The view must do it.
	out, err := Query(context.Background(), dir,
		`SELECT DATE_TRUNC('day', published_at) AS day, COUNT(*) AS n
		 FROM events GROUP BY day;`)
	if err != nil {
		t.Fatalf("Query (should not require explicit cast): %v", err)
	}
	if !strings.Contains(out, "2026-06-19") {
		t.Errorf("expected day bucket in output, got:\n%s", out)
	}
}

// TestQuery_NotInstalled is a smoke test that the not-installed path returns
// the friendly error message. Skipped when duckdb IS installed (we can't fake
// the lookup easily).
func TestQuery_NotInstalled(t *testing.T) {
	if _, err := exec.LookPath("duckdb"); err == nil {
		t.Skip("duckdb is installed; can't test the missing-binary path")
	}
	_, err := Query(context.Background(), t.TempDir(), "SELECT 1")
	if err != ErrNotInstalled {
		t.Errorf("expected ErrNotInstalled, got: %v", err)
	}
}
