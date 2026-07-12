package memorydb

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestOpenCreatesParentDir verifies Open creates a missing parent directory
// (e.g. a fresh <base_dir>/memory/) rather than failing with CANTOPEN.
func TestOpenCreatesParentDir(t *testing.T) {
	t.Parallel()
	// Nested path whose intermediate dirs do not exist yet.
	path := filepath.Join(t.TempDir(), "memory", "sub", "mem.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open with missing parent dir: %v", err)
	}
	defer s.Close()
	if _, err := s.Remember(context.Background(), "fact", "fact", 0.5, nil, ""); err != nil {
		t.Fatalf("Remember after auto-created dir: %v", err)
	}
}

// newTestStore returns a file-backed store with a controllable clock so recency
// and ordering are deterministic.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "mem.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	// Freeze time by default; tests advance it explicitly where needed.
	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	s.clock = func() time.Time { return base }
	return s
}

func TestRememberAndGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	mem, err := s.Remember(ctx, "The user develops a ReAct agent in Go", "project", 0.8,
		[]string{"Go", "ReAct"}, "chat")
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if mem.ID == 0 || mem.Version != 1 || mem.Kind != "project" {
		t.Fatalf("unexpected memory: %+v", mem)
	}

	got, err := s.Get(ctx, mem.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != mem.Content || got.Importance != 0.8 {
		t.Fatalf("Get = %+v", got)
	}
	// Entities are normalized to lowercase.
	if len(got.Entities) != 2 || got.Entities[0] != "go" {
		t.Fatalf("entities = %v", got.Entities)
	}
}

func TestReviseVersioning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	v1, _ := s.Remember(ctx, "The user uses PostgreSQL", "fact", 0.6, nil, "")
	v2, err := s.Revise(ctx, v1.ID, "The user migrated to SQLite", "", 0, nil)
	if err != nil {
		t.Fatalf("Revise: %v", err)
	}
	if v2.Version != 2 || v2.ID == v1.ID {
		t.Fatalf("revise produced %+v", v2)
	}

	hist, err := s.History(ctx, v2.ID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if got := versionNumbers(hist); !equalInts(got, []int{1, 2}) {
		t.Fatalf("history versions = %v, want [1 2]", got)
	}
	if hist[0].Content != "The user uses PostgreSQL" {
		t.Fatalf("v1 content = %q", hist[0].Content)
	}
}

func TestReviseChainAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	v1, _ := s.Remember(ctx, "original fact", "fact", 0.5, nil, "")
	v2, _ := s.Revise(ctx, v1.ID, "updated fact", "", 0, nil)

	// Revising an already-superseded version must fail.
	if _, err := s.Revise(ctx, v1.ID, "x", "", 0, nil); err == nil {
		t.Fatal("revising a superseded memory should error")
	}
	// History is reachable from any id in the chain (old or new).
	fromOld, _ := s.History(ctx, v1.ID)
	fromNew, _ := s.History(ctx, v2.ID)
	if len(fromOld) != 2 || len(fromNew) != 2 {
		t.Fatalf("history lengths: from old %d, from new %d", len(fromOld), len(fromNew))
	}
}

func versionNumbers(ms []Memory) []int {
	out := make([]int, len(ms))
	for i, m := range ms {
		out[i] = m.Version
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRecallExcludesSupersededAndForgotten(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	v1, _ := s.Remember(ctx, "The user uses PostgreSQL database", "fact", 0.5, nil, "")
	_, _ = s.Revise(ctx, v1.ID, "The user uses SQLite database", "", 0, nil)

	// Superseded v1 ("PostgreSQL") must not surface.
	if hits, _ := s.Recall(ctx, "PostgreSQL", nil, 10); len(hits) != 0 {
		t.Fatalf("superseded memory surfaced: %+v", hits)
	}
	hits, _ := s.Recall(ctx, "database", nil, 10)
	if len(hits) != 1 || hits[0].Content != "The user uses SQLite database" {
		t.Fatalf("recall = %+v", hits)
	}

	// Forget the current version; it should drop out of recall.
	if err := s.Forget(ctx, hits[0].ID); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if hits, _ := s.Recall(ctx, "database", nil, 10); len(hits) != 0 {
		t.Fatalf("forgotten memory surfaced: %+v", hits)
	}
}

func TestGetNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if _, err := s.Get(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestMigrationIdempotent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "mem.sqlite")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	m, _ := s1.Remember(context.Background(), "durable fact", "fact", 0.5, nil, "")
	s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	defer s2.Close()
	if got, err := s2.Get(context.Background(), m.ID); err != nil || got.Content != "durable fact" {
		t.Fatalf("reopen get = %+v, err %v", got, err)
	}
}
