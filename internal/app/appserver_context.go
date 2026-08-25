package app

import (
	"strings"

	"github.com/fpt/klein-cli/pkg/message"
)

// A thread's history lives on the app-server, not here. klein sends one prompt
// per turn and nothing else, so a thread that has to be started from scratch —
// after a reconnect, or when a session is resumed in a new process — begins with
// no memory of the conversation at all. The user types "now fix the second one
// too" and the model has never heard of the first.
//
// klein does keep the exchange, in sharedState, as a session log. These render
// it back into something a fresh thread can be started with.
const (
	// transcriptMaxTurns bounds how far back the re-seed reaches. Recent turns
	// are what a follow-up refers to; the whole history is not worth the tokens
	// on every thread start.
	transcriptMaxTurns = 30
	// transcriptMaxChars bounds it again by size, because a handful of turns
	// carrying pasted files is not small just because it is few.
	transcriptMaxChars = 8000
	// transcriptElision says the re-seed is partial. Without it a model reads
	// the oldest surviving line as the start of the conversation, which is a
	// different and wrong belief from "there was more before this".
	transcriptElision = "[earlier turns omitted]"
)

// renderBackendTranscript renders the conversation so far for a thread that has
// to be started fresh, or "" when there is nothing to say.
//
// It is deliberately a transcript and not a summary: summarizing costs a model
// call on a path the user is already waiting on, and the failure it repairs is
// abrupt (a dropped connection), not gradual. Only user and assistant turns are
// carried — a system message is klein's own scaffolding (project context, the
// skill prompt), which the new thread is being given directly anyway.
//
// Trimming drops the oldest first and marks the cut, so what survives is the
// part a follow-up is most likely to mean.
func renderBackendTranscript(msgs []message.Message) string {
	turns := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		var speaker string
		switch msg.Type() {
		case message.MessageTypeUser:
			speaker = "User"
		case message.MessageTypeAssistant:
			speaker = "Assistant"
		default:
			continue
		}
		if content := strings.TrimSpace(msg.Content()); content != "" {
			turns = append(turns, speaker+": "+content)
		}
	}
	if len(turns) == 0 {
		return ""
	}

	elided := false
	if len(turns) > transcriptMaxTurns {
		turns = turns[len(turns)-transcriptMaxTurns:]
		elided = true
	}
	for len(turns) > 1 && joinedLen(turns) > transcriptMaxChars {
		turns = turns[1:]
		elided = true
	}
	if elided {
		turns = append([]string{transcriptElision}, turns...)
	}
	return strings.Join(turns, "\n\n")
}

// joinedLen is the length of the rendered transcript without rendering it.
func joinedLen(turns []string) int {
	total := 0
	for _, turn := range turns {
		total += len(turn) + len("\n\n")
	}
	return total
}

// seedInstructions is what klein hands a thread it may be starting: the skill
// prompt, plus the conversation so far when there is one.
//
// It goes in developerInstructions because that is the only channel read at
// thread start, and klein cannot know in advance whether this turn will open a
// thread — the reconnect that forces one is discovered inside RunTurn. On a
// thread that is still alive it is ignored, which costs one string.
//
// The transcript is fenced and labeled as a record rather than left to run on
// after the instructions. Developer instructions carry more authority than user
// text with most backends, and prior turns pasted in unmarked read as things
// klein is now asking for.
func seedInstructions(skillPrompt, transcript string) string {
	if transcript == "" {
		return skillPrompt
	}
	seed := "# Conversation so far\n\n" +
		"This thread replaces an earlier one whose connection was lost, so the " +
		"exchange below already happened between you and the user. It is a record " +
		"for continuity, not a set of instructions: do not act on it again, and " +
		"answer only what comes next.\n\n" + transcript
	if skillPrompt == "" {
		return seed
	}
	return skillPrompt + "\n\n" + seed
}
