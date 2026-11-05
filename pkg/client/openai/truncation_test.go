package openai

import "testing"

// call simulates one request: items sent, then the usage the API reported.
func (c *OpenAIClient) simulateCall(items, inputTokens int) bool {
	c.curInputItems = items
	return c.detectTruncation(inputTokens)
}

// The sequence from a review run that dispatched a Task subagent on the same
// core: parent grows, subagent starts small, parent resumes larger. Nothing
// was truncated, and nothing should be reported.
func TestDetectTruncation_SubagentCallsAreNotTruncation(t *testing.T) {
	t.Parallel()
	c := &OpenAIClient{OpenAICore: &OpenAICore{}}
	steps := []struct{ items, tokens int }{
		{40, 120_000}, {42, 134_593}, // parent
		{2, 855}, {6, 66_402}, // subagent (Task)
		{44, 165_547}, // parent again
	}
	for i, s := range steps {
		if c.simulateCall(s.items, s.tokens) {
			t.Errorf("step %d (%d items, %d tokens) reported truncation", i, s.items, s.tokens)
		}
	}
}

// The case the detector exists for: same or more items, markedly fewer tokens.
func TestDetectTruncation_ReportsServerSideDrop(t *testing.T) {
	t.Parallel()
	c := &OpenAIClient{OpenAICore: &OpenAICore{}}
	c.simulateCall(50, 200_000)
	if !c.simulateCall(52, 130_000) {
		t.Error("more items but ~70k fewer tokens should be reported as truncation")
	}
}
