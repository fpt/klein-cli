package anthropic

import (
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestApplyThinking(t *testing.T) {
	t.Parallel()

	cases := map[string]anthropic.OutputConfigEffort{
		"":        "", // API default
		"none":    anthropic.OutputConfigEffortLow,
		"minimal": anthropic.OutputConfigEffortLow,
		"medium":  anthropic.OutputConfigEffortMedium,
		"xhigh":   anthropic.OutputConfigEffortXhigh,
		"max":     anthropic.OutputConfigEffortMax,
	}
	for effort, want := range cases {
		var p anthropic.MessageNewParams
		(&AnthropicCore{effort: effort}).applyThinking(&p)
		if p.Thinking.OfAdaptive == nil || p.Thinking.OfEnabled != nil {
			t.Errorf("effort %q: want adaptive thinking, got %+v", effort, p.Thinking)
		}
		if p.OutputConfig.Effort != want {
			t.Errorf("effort %q: output_config.effort = %q, want %q", effort, p.OutputConfig.Effort, want)
		}
	}
}
