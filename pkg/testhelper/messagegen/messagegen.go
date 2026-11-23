// Package messagegen provides rapid generators for conversation histories, for
// property tests over code that walks []message.Message (compaction, cleanup).
package messagegen

import (
	"fmt"
	"time"

	"pgregory.net/rapid"

	"github.com/fpt/klein-cli/pkg/message"
)

// History generates a conversation shaped the way ReAct writes it
// (pkg/agent/react/react.go): a user turn, then zero or more tool batches —
// every call of a batch first, then the results in call order, each result
// carrying its call's ID — then an assistant reply.
//
// Boundaries drawn on purpose:
//   - batches of several calls (C C R R), the only shape where a split can fall
//     between a call and its result with another pair in between;
//   - a batch cut short by cancellation (1 in 8): trailing results missing,
//     leaving calls that are never answered;
//   - a turn with no assistant reply (the last turn of an interrupted run);
//   - histories with no tool use at all.
func History() *rapid.Generator[[]message.Message] {
	return rapid.Custom(func(t *rapid.T) []message.Message {
		var msgs []message.Message
		turns := rapid.IntRange(0, 6).Draw(t, "turns")
		for turn := range turns {
			msgs = append(msgs, message.NewChatMessage(message.MessageTypeUser, fmt.Sprintf("u%d", turn)))
			batches := rapid.IntRange(0, 3).Draw(t, "batches")
			for b := range batches {
				msgs = append(msgs, batch(t, fmt.Sprintf("t%d-b%d", turn, b))...)
			}
			if rapid.IntRange(0, 4).Draw(t, "replyOdds") > 0 {
				msgs = append(msgs, message.NewChatMessage(message.MessageTypeAssistant, fmt.Sprintf("a%d", turn)))
			}
		}
		return msgs
	})
}

func batch(t *rapid.T, prefix string) []message.Message {
	n := rapid.IntRange(1, 3).Draw(t, "calls")
	answered := n
	if rapid.IntRange(0, 7).Draw(t, "cancelOdds") == 0 {
		answered = rapid.IntRange(0, n-1).Draw(t, "answered")
	}
	msgs := make([]message.Message, 0, n+answered)
	ids := make([]string, n)
	for i := range n {
		ids[i] = fmt.Sprintf("%s-c%d", prefix, i)
		msgs = append(msgs, message.NewToolCallMessageWithID(ids[i], "Read", nil, time.Time{}))
	}
	for i := range answered {
		msgs = append(msgs, message.NewToolResultMessage(ids[i], "ok", ""))
	}
	return msgs
}
