package app

import (
	"strconv"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

func chat(t message.MessageType, content string) message.Message {
	return message.NewChatMessage(t, content)
}

// The failure this repairs: a thread started after a reconnect has no history,
// because the history lived on the server. What klein still has is its own
// session log, so the exchange has to come back out of it in the order it
// happened and with the speakers still distinguishable — "now fix the second
// one too" means nothing otherwise.
func TestRenderBackendTranscript_CarriesTheExchangeInOrder(t *testing.T) {
	t.Parallel()

	got := renderBackendTranscript([]message.Message{
		chat(message.MessageTypeUser, "rename the first helper"),
		chat(message.MessageTypeAssistant, "renamed it to parseHeader"),
		chat(message.MessageTypeUser, "now fix the second one too"),
	})

	for _, want := range []string{"User: rename the first helper", "Assistant: renamed it to parseHeader"} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript is missing %q:\n%s", want, got)
		}
	}
	if first, second := strings.Index(got, "rename the first"), strings.Index(got, "now fix the second"); first > second {
		t.Errorf("the exchange came back out of order:\n%s", got)
	}
}

// A system message is klein's own scaffolding — project context, the skill
// prompt — and the new thread is handed those directly. Replaying them as
// conversation would tell the model the user said them.
func TestRenderBackendTranscript_LeavesSystemScaffoldingOut(t *testing.T) {
	t.Parallel()

	got := renderBackendTranscript([]message.Message{
		message.NewSystemMessage("# Project Context\n\nthis is AGENTS.md"),
		chat(message.MessageTypeUser, "hello"),
	})

	if strings.Contains(got, "AGENTS.md") {
		t.Errorf("a system message was replayed as conversation:\n%s", got)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("the user turn did not survive:\n%s", got)
	}
}

// Nothing to re-seed is not the same as something empty to re-seed: a first turn
// must leave the developer instructions exactly as the skill wrote them.
func TestRenderBackendTranscript_EmptyWhenThereIsNoConversation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		msgs []message.Message
	}{
		{"no messages at all", nil},
		{"only scaffolding", []message.Message{message.NewSystemMessage("skill prompt")}},
		{"blank turns", []message.Message{chat(message.MessageTypeUser, "   \n ")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := renderBackendTranscript(tc.msgs); got != "" {
				t.Errorf("expected nothing to seed, got %q", got)
			}
		})
	}
}

// Trimming drops the oldest, because a follow-up refers to what just happened.
// The cut is marked: without it the oldest surviving line reads as the start of
// the conversation, which is a different and wrong belief from "there was more
// before this".
func TestRenderBackendTranscript_TrimsOldestAndSaysSo(t *testing.T) {
	t.Parallel()

	var msgs []message.Message
	for i := range transcriptMaxTurns + 10 {
		msgs = append(msgs, chat(message.MessageTypeUser, "turn-"+strconv.Itoa(i)))
	}
	got := renderBackendTranscript(msgs)

	if !strings.Contains(got, transcriptElision) {
		t.Errorf("the transcript was trimmed without saying so:\n%s", got)
	}
	if !strings.Contains(got, "turn-"+strconv.Itoa(transcriptMaxTurns+9)) {
		t.Errorf("the most recent turn was dropped:\n%s", got)
	}
	if strings.Contains(got, "turn-0\n") {
		t.Errorf("the oldest turn survived instead of being dropped:\n%s", got)
	}
}

// A handful of turns is not small just because it is few: one pasted file blows
// the budget on its own.
func TestRenderBackendTranscript_BoundsBySizeNotJustCount(t *testing.T) {
	t.Parallel()

	huge := strings.Repeat("x", transcriptMaxChars)
	got := renderBackendTranscript([]message.Message{
		chat(message.MessageTypeUser, huge),
		chat(message.MessageTypeAssistant, huge),
		chat(message.MessageTypeUser, "the short one that matters"),
	})

	if !strings.Contains(got, "the short one that matters") {
		t.Errorf("the most recent turn was dropped:\n%s", got[:min(len(got), 200)])
	}
	if len(got) > 2*transcriptMaxChars {
		t.Errorf("the transcript is %d chars, well past the %d budget", len(got), transcriptMaxChars)
	}
}

// A last turn over budget on its own is still the turn the user is asking
// about. Keeping it whole beats sending a thread nothing at all.
func TestRenderBackendTranscript_KeepsTheLastTurnEvenWhenOversized(t *testing.T) {
	t.Parallel()

	huge := strings.Repeat("y", transcriptMaxChars*2)
	got := renderBackendTranscript([]message.Message{chat(message.MessageTypeUser, huge)})
	if !strings.Contains(got, "yyy") {
		t.Errorf("an oversized single turn was dropped entirely, leaving %q", got)
	}
}

// Developer instructions carry more authority than user text with most
// backends, so a replayed exchange has to be fenced and labeled as a record.
// Unmarked, "delete the temp files" from three turns ago reads as something
// klein is asking for now.
func TestSeedInstructions_FencesTheTranscriptAsARecord(t *testing.T) {
	t.Parallel()

	got := seedInstructions("You are a coding assistant.", "User: delete the temp files")

	if !strings.Contains(got, "You are a coding assistant.") {
		t.Errorf("the skill prompt was lost:\n%s", got)
	}
	if !strings.Contains(got, "User: delete the temp files") {
		t.Errorf("the transcript was lost:\n%s", got)
	}
	if !strings.Contains(got, "not a set of instructions") {
		t.Errorf("the transcript is not marked as a record:\n%s", got)
	}
	if strings.Index(got, "coding assistant") > strings.Index(got, "delete the temp files") {
		t.Errorf("the skill prompt came after the replayed turns:\n%s", got)
	}
}

// With nothing to re-seed the instructions must be byte-identical to the skill
// prompt: every ordinary turn goes through here, and a thread started on a first
// turn should not be told a conversation happened.
func TestSeedInstructions_UntouchedWithoutATranscript(t *testing.T) {
	t.Parallel()

	if got := seedInstructions("You are a coding assistant.", ""); got != "You are a coding assistant." {
		t.Errorf("the skill prompt was modified with nothing to seed: %q", got)
	}
}
