package react

import (
	"context"
	"testing"

	"github.com/fpt/klein-cli/pkg/agent/state"
	"github.com/fpt/klein-cli/pkg/message"
)

// Run must add the user message to state before any cancellation check.
//
// app.Invoke depends on this: it prepends finished background agents'
// results to the turn's input and treats entering Run as the point the
// notifications have been delivered. If Run ever started bailing out on a
// canceled context before recording the message, a Ctrl+C would drop those
// results with no way to get them back — so this ordering is a contract, not
// an implementation detail.
func TestRun_AddsUserMessageBeforeCancellationCheck(t *testing.T) {
	t.Parallel()

	st := state.NewMessageState()
	r, _ := NewReAct(&mockLLM{}, &mockToolManager{}, st, &mockSituation{}, 10)
	defer r.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled before the turn even starts

	const input = "<agent-notification>result</agent-notification>\nwhat next?"
	if _, err := r.Run(ctx, input); err == nil {
		t.Fatal("expected a cancellation error")
	}

	var found bool
	for _, m := range st.GetMessages() {
		cm, ok := m.(*message.ChatMessage)
		if ok && cm.Type() == message.MessageTypeUser && cm.Content() == input {
			found = true
		}
	}
	if !found {
		t.Fatal("Run returned on a canceled context without recording the user message; " +
			"app.Invoke's notification-delivery boundary is no longer safe")
	}
}
