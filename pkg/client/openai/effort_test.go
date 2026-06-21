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
