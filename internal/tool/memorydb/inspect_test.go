package memorydb

import (
	"context"
	"errors"
	"testing"
)

func TestListStatCount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	a, _ := s.Remember(ctx, "alpha fact one", "fact", 0.5, nil, "")
	b, _ := s.Remember(ctx, "beta fact two", "fact", 0.5, nil, "")

	// Reference the memories: recall b (bumps access), reinforce a (useful).
	mustRecall(t, s, "beta")
	if _, err := s.Reinforce(ctx, a.ID, 0.5); err != nil {
		t.Fatalf("Reinforce: %v", err)
	}

	byID := statByID(t, s, false)
	if len(byID) != 2 {
		t.Fatalf("list len = %d, want 2", len(byID))
	}
	// b was recalled → access recorded; a was reinforced → useful + utility.
	assertRefs(t, byID[b.ID], refExpect{access: 1, active: true, lastUsed: true})
	assertRefs(t, byID[a.ID], refExpect{access: 0, useful: true, utility: true, active: true, lastUsed: true})

	st, err := s.Stat(ctx, b.ID)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	assertRefs(t, st, refExpect{access: 1, active: true, lastUsed: true})
	assertCount(t, s, 2, 2)
}

func TestListInactiveVisibility(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	keep, _ := s.Remember(ctx, "keep this", "fact", 0.5, nil, "")
	drop, _ := s.Remember(ctx, "drop this", "fact", 0.5, nil, "")
	if err := s.Forget(ctx, drop.ID); err != nil {
		t.Fatalf("Forget: %v", err)
	}

	// Default list hides forgotten memories.
	active := statByID(t, s, false)
	if len(active) != 1 {
		t.Fatalf("active list = %+v", active)
	}
	if _, ok := active[keep.ID]; !ok {
		t.Fatalf("kept memory #%d missing from active list", keep.ID)
	}

	// includeInactive surfaces it, flagged not-active.
	all := statByID(t, s, true)
	dropped, ok := all[drop.ID]
	if len(all) != 2 || !ok {
		t.Fatalf("all list = %+v", all)
	}
	if dropped.Active {
		t.Error("forgotten memory should be marked inactive")
	}
	assertCount(t, s, 1, 2)
}

type refExpect struct {
	access                            int
	useful, utility, active, lastUsed bool
}

func assertRefs(t *testing.T, st MemoryStat, want refExpect) {
	t.Helper()
	if st.AccessCount != want.access {
		t.Errorf("#%d access = %d, want %d", st.ID, st.AccessCount, want.access)
	}
	if want.useful && st.UsefulCount <= 0 {
		t.Errorf("#%d useful = %.2f, want > 0", st.ID, st.UsefulCount)
	}
	if want.utility && st.UtilityEMA <= 0 {
		t.Errorf("#%d utility = %.2f, want > 0", st.ID, st.UtilityEMA)
	}
	if st.Active != want.active {
		t.Errorf("#%d active = %v, want %v", st.ID, st.Active, want.active)
	}
	if want.lastUsed && st.LastUsedAt.IsZero() {
		t.Errorf("#%d LastUsedAt should be set", st.ID)
	}
}

func statByID(t *testing.T, s *Store, includeInactive bool) map[int64]MemoryStat {
	t.Helper()
	items, err := s.List(context.Background(), includeInactive, 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byID := make(map[int64]MemoryStat, len(items))
	for _, it := range items {
		byID[it.ID] = it
	}
	return byID
}

func assertCount(t *testing.T, s *Store, wantActive, wantTotal int) {
	t.Helper()
	active, total, err := s.Count(context.Background())
	if err != nil || active != wantActive || total != wantTotal {
		t.Fatalf("Count = %d/%d err %v, want %d/%d", active, total, err, wantActive, wantTotal)
	}
}

func TestStatNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if _, err := s.Stat(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
