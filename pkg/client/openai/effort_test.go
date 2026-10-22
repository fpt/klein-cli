package openai

import (
	"testing"

	"github.com/openai/openai-go/v3/shared"
)

func TestParseReasoningEffort(t *testing.T) {
	cases := []struct {
		in   string
		want shared.ReasoningEffort
	}{
		{"none", shared.ReasoningEffortNone},
		{"minimal", shared.ReasoningEffortMinimal},
		{"low", shared.ReasoningEffortLow},
		{"medium", shared.ReasoningEffortMedium},
		{"high", shared.ReasoningEffortHigh},
		{"xhigh", shared.ReasoningEffortXhigh},
		{"max", shared.ReasoningEffortMax},
		{"", defaultReasoningEffort},      // empty falls back to default
		{"bogus", defaultReasoningEffort}, // unknown falls back to default
		{"HIGH", defaultReasoningEffort},  // case-sensitive: not normalized here
	}
	for _, tc := range cases {
		if got := parseReasoningEffort(tc.in); got != tc.want {
			t.Errorf("parseReasoningEffort(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSupportedEffort(t *testing.T) {
	t.Parallel()

	cases := []struct {
		model string
		in    shared.ReasoningEffort
		want  shared.ReasoningEffort
	}{
		{ModelLuna, shared.ReasoningEffortMinimal, shared.ReasoningEffortNone},
		{ModelLuna, shared.ReasoningEffortNone, shared.ReasoningEffortNone},
		{ModelAstra, shared.ReasoningEffortNone, shared.ReasoningEffortLow},
		{ModelAstra, shared.ReasoningEffortMinimal, shared.ReasoningEffortLow},
		{ModelSol61 + "-2026-10-01", shared.ReasoningEffortNone, shared.ReasoningEffortLow},
		{ModelLuna, shared.ReasoningEffortMax, shared.ReasoningEffortMax},
	}
	for _, tc := range cases {
		if got := supportedEffort(tc.model, tc.in); got != tc.want {
			t.Errorf("supportedEffort(%q, %q) = %q, want %q", tc.model, tc.in, got, tc.want)
		}
	}
}
