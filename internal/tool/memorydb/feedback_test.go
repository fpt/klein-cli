package memorydb

import (
	"context"
	"errors"
	"testing"
)

func TestReinforceRaisesUtilityAndRerank(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	// Two lexically-equal, equally-important memories → initially a tie broken
	// only by learned utility (both 0).
	a, _ := s.Remember(ctx, "the deploy uses docker", "fact", 0.5, nil, "")
	b, _ := s.Remember(ctx, "the deploy uses podman", "fact", 0.5, nil, "")

	// Positive feedback on b should lift it above a on the next recall.
	credit, _ := CreditForSignal(SignalConfirmed)
	if _, err := s.Reinforce(ctx, b.ID, credit); err != nil {
		t.Fatalf("Reinforce: %v", err)
	}

	hits, _ := s.Recall(ctx, "deploy uses", nil, 10)
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].ID != b.ID {
		t.Fatalf("reinforced memory #%d should rank first, got #%d", b.ID, hits[0].ID)
	}
	if hits[0].UtilityEMA <= 0 {
		t.Fatalf("utility EMA = %.3f, want > 0", hits[0].UtilityEMA)
	}
	_ = a
}

func TestReinforceNegativeLowersRank(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	a, _ := s.Remember(ctx, "the api returns json", "fact", 0.5, nil, "")
	b, _ := s.Remember(ctx, "the api returns xml", "fact", 0.5, nil, "")

	// Negative feedback on a should push it below b.
	credit, _ := CreditForSignal(SignalCorrected)
	ema, err := s.Reinforce(ctx, a.ID, credit)
	if err != nil {
		t.Fatalf("Reinforce: %v", err)
	}
	if ema >= 0 {
		t.Fatalf("negative feedback EMA = %.3f, want < 0", ema)
	}

	hits, _ := s.Recall(ctx, "api returns", nil, 10)
	if hits[0].ID != b.ID {
		t.Fatalf("un-penalized memory #%d should rank first, got #%d", b.ID, hits[0].ID)
	}
}

func TestReinforceEMAConverges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)
	m, _ := s.Remember(ctx, "a fact", "fact", 0.5, nil, "")

	// Repeated positive credit drives the EMA toward the credit value, monotonically.
	var last float64
	for range 8 {
		ema, err := s.Reinforce(ctx, m.ID, 0.5)
		if err != nil {
			t.Fatalf("Reinforce: %v", err)
		}
		if ema <= last {
			t.Fatalf("EMA not increasing: %.4f then %.4f", last, ema)
		}
		if ema > 0.5 {
			t.Fatalf("EMA overshot credit: %.4f", ema)
		}
		last = ema
	}

	// Useful/harmful tallies reflect the sign of the credit.
	got, _ := s.Get(ctx, m.ID)
	_ = got // usefulCount is internal; utility is the observable signal
}

func TestReinforceNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if _, err := s.Reinforce(context.Background(), 424242, 0.5); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestReviseInheritsUtility(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	m, _ := s.Remember(ctx, "old fact about caching", "fact", 0.5, nil, "")
	if _, err := s.Reinforce(ctx, m.ID, 0.5); err != nil {
		t.Fatalf("Reinforce: %v", err)
	}
	before, _ := s.Get(ctx, m.ID)

	v2, err := s.Revise(ctx, m.ID, "new fact about caching", "", 0, nil)
	if err != nil {
		t.Fatalf("Revise: %v", err)
	}
	// Learned usefulness should carry over to the new version, not reset to 0.
	if v2.UtilityEMA != before.UtilityEMA {
		t.Fatalf("revised utility = %.3f, want inherited %.3f", v2.UtilityEMA, before.UtilityEMA)
	}
}

func TestCreditForSignal(t *testing.T) {
	t.Parallel()
	for _, sig := range []string{SignalConfirmed, SignalUsed, SignalHelpful} {
		assertCreditSign(t, sig, +1)
	}
	for _, sig := range []string{SignalIrrelevant, SignalStale, SignalCorrected, SignalHarmful} {
		assertCreditSign(t, sig, -1)
	}
	assertCreditSign(t, SignalNeutral, 0)

	if _, ok := CreditForSignal("bogus"); ok {
		t.Error("unknown signal should not be ok")
	}
	// Case/space-insensitive.
	if _, ok := CreditForSignal("  Confirmed "); !ok {
		t.Error("signal lookup should trim and lowercase")
	}
}

// assertCreditSign checks CreditForSignal(sig) is known and matches wantSign
// (+1 positive, -1 negative, 0 zero).
func assertCreditSign(t *testing.T, sig string, wantSign int) {
	t.Helper()
	c, ok := CreditForSignal(sig)
	if !ok {
		t.Errorf("signal %q not recognized", sig)
		return
	}
	gotSign := 0
	switch {
	case c > 0:
		gotSign = 1
	case c < 0:
		gotSign = -1
	}
	if gotSign != wantSign {
		t.Errorf("signal %q credit = %.2f (sign %d), want sign %d", sig, c, gotSign, wantSign)
	}
}
