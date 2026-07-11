package memorydb

import (
	"context"
	"testing"
)

func TestRecallBM25Ordering(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	mustRemember(t, s, "the quick brown fox jumps once", 0.5)
	mustRemember(t, s, "a slow green turtle walks", 0.5)
	mustRemember(t, s, "quick quick quick fox fox fox", 0.5)

	hits, err := s.Recall(ctx, "quick fox", nil, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	// The memory repeating the terms most is the strongest lexical match.
	if hits[0].Content != "quick quick quick fox fox fox" {
		t.Fatalf("top hit = %q", hits[0].Content)
	}
	if hits[0].Score < hits[1].Score {
		t.Fatalf("scores not descending: %.3f then %.3f", hits[0].Score, hits[1].Score)
	}
}

func TestRecallImportanceBreaksLexicalTie(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	// Equal lexical match on "alpha" (same length, same term frequency).
	low, _ := s.Remember(ctx, "alpha beta", "fact", 0.2, nil, "")
	high, _ := s.Remember(ctx, "alpha gamma", "fact", 0.9, nil, "")

	hits, _ := s.Recall(ctx, "alpha", nil, 10)
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].ID != high.ID {
		t.Fatalf("top hit id = %d, want %d (higher importance)", hits[0].ID, high.ID)
	}
	_ = low
}

func TestRecallEntityBoostAndSurfacing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	// Two lexically-equal memories; only one is tagged with the queried entity.
	plain, _ := s.Remember(ctx, "the project uses a database", "fact", 0.5, nil, "")
	tagged, _ := s.Remember(ctx, "the project uses a datastore", "fact", 0.5, []string{"SQLite"}, "")

	hits, _ := s.Recall(ctx, "project uses", []string{"sqlite"}, 10)
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].ID != tagged.ID {
		t.Fatalf("entity-tagged memory should rank first, got id %d", hits[0].ID)
	}
	if hits[0].EntityHit != 1.0 {
		t.Fatalf("entityHit = %.2f, want 1.0", hits[0].EntityHit)
	}
	_ = plain

	// Entity match alone surfaces a memory even when the lexical query misses it.
	entOnly, _ := s.Remember(ctx, "completely unrelated wording", "fact", 0.5, []string{"Kagome"}, "")
	hits2, _ := s.Recall(ctx, "project uses", []string{"kagome"}, 10)
	var found bool
	for _, h := range hits2 {
		if h.ID == entOnly.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("entity-only memory #%d not surfaced by entity match", entOnly.ID)
	}
}

func TestRecallJapaneseBigram(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	// Japanese has no spaces; the bigram tokenizer must still recall these.
	mustRemember(t, s, "ユーザーはGoでReActエージェントを開発している", 0.6)
	mustRemember(t, s, "今日の天気はとても良い", 0.5) // distractor

	hits, err := s.Recall(ctx, "エージェント 開発", nil, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("Japanese query returned no hits")
	}
	if hits[0].Content != "ユーザーはGoでReActエージェントを開発している" {
		t.Fatalf("top Japanese hit = %q", hits[0].Content)
	}

	// A query about an unrelated Japanese topic should not match the agent memory.
	for _, h := range mustRecall(t, s, "天気") {
		if h.Content == "ユーザーはGoでReActエージェントを開発している" {
			t.Fatal("unrelated Japanese query matched the agent memory")
		}
	}
}

func TestRecallMixedScriptToken(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	mustRemember(t, s, "Go言語でエージェントを実装する", 0.5)

	// The Latin token inside Japanese text is recallable on its own.
	if hits := mustRecall(t, s, "Go"); len(hits) == 0 {
		t.Fatal("mixed-script Latin token 'Go' not recalled")
	}
	// And a Japanese fragment is recallable too.
	if hits := mustRecall(t, s, "実装"); len(hits) == 0 {
		t.Fatal("mixed-script Japanese fragment '実装' not recalled")
	}
}

func TestRecallEmptyQueryNoEntities(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	mustRemember(t, s, "some memory", 0.5)
	if hits, err := s.Recall(context.Background(), "   ", nil, 10); err != nil || len(hits) != 0 {
		t.Fatalf("empty query hits = %d, err %v", len(hits), err)
	}
}

func mustRemember(t *testing.T, s *Store, content string, importance float64) Memory {
	t.Helper()
	m, err := s.Remember(context.Background(), content, "fact", importance, nil, "")
	if err != nil {
		t.Fatalf("Remember(%q): %v", content, err)
	}
	return m
}

func mustRecall(t *testing.T, s *Store, query string) []Hit {
	t.Helper()
	hits, err := s.Recall(context.Background(), query, nil, 10)
	if err != nil {
		t.Fatalf("Recall(%q): %v", query, err)
	}
	return hits
}
