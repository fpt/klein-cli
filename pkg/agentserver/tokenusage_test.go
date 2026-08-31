package agentserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// usageRecorder is an Observer that also wants token accounting.
type usageRecorder struct {
	discardObserver
	got []TokenUsage
}

func (r *usageRecorder) TokenUsageUpdated(u TokenUsage) { r.got = append(r.got, u) }

// usageNote is one thread/tokenUsage/updated payload in gallium's/codex's shape.
// window is rendered as JSON null when nil.
func usageNote(t *testing.T, lastIn, lastOut, total int, window *int) json.RawMessage {
	t.Helper()
	w := "null"
	if window != nil {
		w = string(mustJSON(t, *window))
	}
	return json.RawMessage(`{"threadId":"thread_1","turnId":"turn_1","tokenUsage":{` +
		`"total":{"totalTokens":` + string(mustJSON(t, total)) + `,"inputTokens":0,"outputTokens":0},` +
		`"last":{"totalTokens":0,"inputTokens":` + string(mustJSON(t, lastIn)) +
		`,"outputTokens":` + string(mustJSON(t, lastOut)) + `},` +
		`"modelContextWindow":` + w + `}}`)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// The gauge's numerator is the last call's prompt, not the thread's running
// total. They are different questions, and only one of them fits in the window:
// the total keeps climbing after the context has stopped growing, so a gauge
// drawn on it passes 100% partway through any real conversation.
func TestReportTokenUsage_PrefersLastInputOverRunningTotal(t *testing.T) {
	t.Parallel()
	rec := &usageRecorder{}
	tp := &turnProgress{obs: rec}
	window := 24576

	tp.reportTokenUsage(usageNote(t, 9000, 120, 90000, &window))

	if len(rec.got) != 1 {
		t.Fatalf("want 1 report, got %d", len(rec.got))
	}
	u := rec.got[0]
	if u.LastInputTokens != 9000 {
		t.Errorf("LastInputTokens = %d, want 9000 (the prompt, not the spend)", u.LastInputTokens)
	}
	if u.TotalTokens != 90000 {
		t.Errorf("TotalTokens = %d, want 90000 kept as the spend figure", u.TotalTokens)
	}
	if u.ContextWindow != window {
		t.Errorf("ContextWindow = %d, want %d", u.ContextWindow, window)
	}
}

// A null window means the backend will not vouch for a number. It has to arrive
// as zero and not as a plausible default, because the caller's whole decision is
// whether it may draw a percentage at all.
func TestReportTokenUsage_NullWindowBecomesZero(t *testing.T) {
	t.Parallel()
	rec := &usageRecorder{}
	tp := &turnProgress{obs: rec}

	tp.reportTokenUsage(usageNote(t, 9000, 120, 9000, nil))

	if len(rec.got) != 1 {
		t.Fatalf("want 1 report, got %d", len(rec.got))
	}
	if w := rec.got[0].ContextWindow; w != 0 {
		t.Errorf("ContextWindow = %d, want 0 for a null window", w)
	}
	// The counts still arrive: unknown window, known occupancy.
	if rec.got[0].LastInputTokens != 9000 {
		t.Errorf("a null window should not suppress the token count: %+v", rec.got[0])
	}
}

// An Observer that does not implement TokenUsageObserver is simply not told —
// the point of making it an optional interface rather than a method on Observer.
func TestReportTokenUsage_PlainObserverIsNotConsulted(t *testing.T) {
	t.Parallel()
	tp := &turnProgress{obs: discardObserver{}}
	window := 1000

	// Must not panic, and must not require the observer to know anything.
	tp.reportTokenUsage(usageNote(t, 10, 10, 10, &window))
}

// Malformed accounting must not take the turn down with it: the gauge is
// cosmetic and the turn is not.
func TestReportTokenUsage_MalformedPayloadIsIgnored(t *testing.T) {
	t.Parallel()
	rec := &usageRecorder{}
	tp := &turnProgress{obs: rec}

	tp.reportTokenUsage(json.RawMessage(`{"tokenUsage": "not an object"}`))

	if len(rec.got) != 0 {
		t.Errorf("a malformed payload was reported as usage: %+v", rec.got)
	}
}

// Another thread's accounting must not reach this turn's observer.
//
// The subscription is connection-wide — one app-server is shared across klein's
// sessions, and each session is a thread on it — so a notification for a
// different thread does arrive here. Reading it would put another session's
// occupancy in this one's gauge, and because the display keeps the last value it
// saw, the wrong number would then stay on screen.
//
// Run through the real turn loop rather than the parser, because the parser is
// not where this can go wrong: reportTokenUsage is correct either way, and the
// bug is entirely in whether the loop hands it a notification it should have
// dropped.
func TestRunner_OverTCP_IgnoresAnotherThreadsTokenUsage(t *testing.T) {
	t.Parallel()

	srv := newJSONLServer(t)
	srv.strayUsageThread = "some_other_thread"
	runner := runnerDialing(t, srv, nil)
	obs := &usageRecorder{}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, _, err := runner.RunTurn(ctx, "", "hello", "", obs); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if len(obs.got) != 0 {
		t.Errorf("another thread's accounting reached this turn: %+v", obs.got)
	}
}

// The same turn's accounting still gets through, so the filter above is not
// simply dropping everything.
func TestRunner_OverTCP_DeliversItsOwnTokenUsage(t *testing.T) {
	t.Parallel()

	srv := newJSONLServer(t)
	srv.strayUsageThread = firstThread // the thread this turn actually runs on
	runner := runnerDialing(t, srv, nil)
	obs := &usageRecorder{}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, _, err := runner.RunTurn(ctx, "", "hello", "", obs); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if len(obs.got) != 1 {
		t.Fatalf("want this thread's accounting delivered once, got %+v", obs.got)
	}
	if obs.got[0].LastInputTokens != 999999 {
		t.Errorf("wrong figure delivered: %+v", obs.got[0])
	}
}
