package state

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/fpt/klein-cli/pkg/message"
	"github.com/fpt/klein-cli/pkg/testhelper/messagegen"
)

// splitBreaksPair is the reference for a safe split, stated pair by pair: a
// split at s breaks a pair when a tool call sits before s and the result that
// carries its ID sits at or after s. Unanswered calls belong to no pair, so
// they never make a split unsafe.
func splitBreaksPair(messages []message.Message, s int) bool {
	for i := 0; i < s; i++ {
		if messages[i].Type() != message.MessageTypeToolCall {
			continue
		}
		for j := s; j < len(messages); j++ {
			if messages[j].Type() == message.MessageTypeToolResult && messages[j].ID() == messages[i].ID() {
				return true
			}
		}
	}
	return false
}

func refSafe(messages []message.Message, s int) bool {
	return s > 0 && s < len(messages) && !splitBreaksPair(messages, s)
}

// TestIsSafeSplitPoint_MatchesPairs checks S1: a split is reported safe exactly
// when it is strictly inside the history and separates no call from its result.
func TestIsSafeSplitPoint_MatchesPairs(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		msgs := messagegen.History().Draw(t, "msgs")
		for s := -1; s <= len(msgs)+1; s++ {
			if got, want := isSafeSplitPoint(msgs, s), refSafe(msgs, s); got != want {
				t.Fatalf("isSafeSplitPoint(split=%d) = %v, want %v", s, got, want)
			}
		}
	})
}

// TestFindSafeSplitPoint_LatestSafe checks S2: the split found is the latest safe
// one that still keeps preserveRecent messages, or 0 when there is none.
func TestFindSafeSplitPoint_LatestSafe(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		msgs := messagegen.History().Draw(t, "msgs")
		keep := rapid.IntRange(0, len(msgs)+1).Draw(t, "preserveRecent")
		want := 0
		for s := len(msgs) - keep; s > 0; s-- {
			if refSafe(msgs, s) {
				want = s
				break
			}
		}
		if got := findSafeSplitPoint(msgs, keep); got != want {
			t.Fatalf("findSafeSplitPoint(preserveRecent=%d) = %d, want %d", keep, got, want)
		}
	})
}

// TestFindSafeSplitPoint_RecentHasNoOrphanResult checks S3: after a split, every
// tool result kept in the recent part still has its call there too — the
// condition the Anthropic API rejects a request for.
func TestFindSafeSplitPoint_RecentHasNoOrphanResult(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		msgs := messagegen.History().Draw(t, "msgs")
		keep := rapid.IntRange(0, len(msgs)).Draw(t, "preserveRecent")
		s := findSafeSplitPoint(msgs, keep)
		if s == 0 {
			return // nothing is split, so nothing can be orphaned
		}
		calls := map[string]bool{}
		for _, m := range msgs[s:] {
			switch m.Type() {
			case message.MessageTypeToolCall:
				calls[m.ID()] = true
			case message.MessageTypeToolResult:
				if !calls[m.ID()] {
					t.Fatalf("split=%d leaves result %q without its call", s, m.ID())
				}
			}
		}
	})
}
