package gateway

import (
	"strings"
	"testing"
)

// TestScheduledRunPreamble locks in the directives that stop a scheduled run
// from behaving like a user conversation (the observed failure: the agent tried
// to register a schedule and asked a non-existent user for channel info).
func TestScheduledRunPreamble(t *testing.T) {
	got := scheduledRunPreamble("morning-market", "discord", "123")

	for _, want := range []string{
		`[SCHEDULED RUN name="morning-market" channel_type=discord channel_id=123]`,
		"NOT a user",
		"Do not ask questions",
		"Do not create, modify, or offer",
		"this schedule already exists",
		"perform the underlying work now",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("preamble missing %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Error("preamble should end with a blank line separating it from the task")
	}
}
