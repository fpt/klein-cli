package app

import (
	"strings"
	"testing"
)

// strip removes ANSI escapes so a test can assert on what is drawn rather than
// how it is colored.
func strip(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// A backend's report supersedes klein's estimate, and the percentage is drawn
// from the last prompt against the window.
func TestShowStatusLineWithBackend_DrawsAPercentageWhenTheWindowIsKnown(t *testing.T) {
	t.Parallel()
	cd := NewContextDisplay()

	got := strip(cd.ShowStatusLineWithBackend(nil, nil, "",
		&BackendUsage{InputTokens: 12288, Window: 24576}))

	if !strings.Contains(got, "12k/24k") {
		t.Errorf("expected the backend's own numbers, got %q", got)
	}
	if !strings.Contains(got, "(50%)") {
		t.Errorf("expected 50%%, got %q", got)
	}
}

// The rule this feature turns on: a backend that will not name a window gets no
// gauge. A percentage of an assumed constant reads exactly like a measurement
// and is not one.
func TestShowStatusLineWithBackend_NoWindowMeansNoPercentage(t *testing.T) {
	t.Parallel()
	cd := NewContextDisplay()

	got := strip(cd.ShowStatusLineWithBackend(nil, nil, "",
		&BackendUsage{InputTokens: 9000, Window: 0}))

	if strings.Contains(got, "%") {
		t.Errorf("a percentage was drawn without a window: %q", got)
	}
	if !strings.Contains(got, "9k") {
		t.Errorf("the known token count should still be shown, got %q", got)
	}
}

// The count is right-aligned, which is where the indicator belongs.
func TestShowStatusLineWithBackend_IsRightAligned(t *testing.T) {
	t.Parallel()
	cd := NewContextDisplay()

	got := strip(cd.ShowStatusLineWithBackend(nil, nil, "",
		&BackendUsage{InputTokens: 12288, Window: 24576}))

	if strings.HasPrefix(got, "Context:") {
		t.Errorf("the indicator should be padded to the right edge, got %q", got)
	}
	if strings.TrimRight(got, " ") != got {
		t.Errorf("nothing should follow the indicator: %q", got)
	}
}

// A task summary shares the line, dimmed on the left, indicator on the right.
func TestShowStatusLineWithBackend_KeepsTheTaskSummaryOnTheLeft(t *testing.T) {
	t.Parallel()
	cd := NewContextDisplay()

	got := strip(cd.ShowStatusLineWithBackend(nil, nil, "2 tasks in progress",
		&BackendUsage{InputTokens: 12288, Window: 24576}))

	if !strings.HasPrefix(got, "2 tasks in progress") {
		t.Errorf("task summary should lead, got %q", got)
	}
	if !strings.Contains(got, "12k/24k") {
		t.Errorf("indicator should still be present, got %q", got)
	}
}

// Nothing known and nothing to say draws no line at all, rather than an empty
// bar suggesting an empty context.
func TestShowStatusLineWithBackend_EmptyWhenNothingIsKnown(t *testing.T) {
	t.Parallel()
	cd := NewContextDisplay()

	if got := cd.ShowStatusLineWithBackend(nil, nil, "", &BackendUsage{}); got != "" {
		t.Errorf("expected no status line, got %q", got)
	}
}

// A short conversation must not read as an empty one. Integer-dividing by 1000
// renders everything under 1k as "0k", which is the gauge looking broken exactly
// when a user first glances at it.
func TestFormatTokens_ShowsSubThousandCountsExactly(t *testing.T) {
	t.Parallel()

	if got := formatTokens(432); got != "432" {
		t.Errorf("formatTokens(432) = %q, want %q", got, "432")
	}
	if got := formatTokens(12288); got != "12k" {
		t.Errorf("formatTokens(12288) = %q, want %q", got, "12k")
	}
}

// A prompt larger than the window (a backend advertising a window it cannot
// actually allocate) clamps rather than printing over 100%.
func TestShowStatusLineWithBackend_ClampsAboveTheWindow(t *testing.T) {
	t.Parallel()
	cd := NewContextDisplay()

	got := strip(cd.ShowStatusLineWithBackend(nil, nil, "",
		&BackendUsage{InputTokens: 40000, Window: 24576}))

	if !strings.Contains(got, "(100%)") {
		t.Errorf("expected a clamped 100%%, got %q", got)
	}
}
