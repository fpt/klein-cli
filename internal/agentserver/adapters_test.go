package agentserver

import (
	"strings"
	"testing"

	"github.com/fpt/klein-cli/pkg/agent/events"
	"github.com/fpt/klein-cli/pkg/message"
)

// The adapters are the only place klein's types meet the client's, so the
// interfaces have to be satisfied here or nowhere.
var _ Observer = eventObserver{}

// emitted records what eventObserver put on klein's event stream.
type emitted struct {
	data any
	typ  events.EventType
}

func newEventObserver() (eventObserver, *[]emitted) {
	var got []emitted
	return eventObserver{emit: func(t events.EventType, d any) {
		got = append(got, emitted{typ: t, data: d})
	}}, &got
}

// Summarization moved out of the client and onto this side, which is what makes
// it apply to every call. It used to reach dynamic and MCP calls only.
func TestEventObserver_SummarizesArguments(t *testing.T) {
	t.Parallel()
	obs, got := newEventObserver()

	obs.ToolCallStarted(ToolCall{
		Name:      "Remember",
		Arguments: map[string]any{"body": strings.Repeat("x", 500)},
	})

	if len(*got) != 1 || (*got)[0].typ != events.EventTypeToolCallStart {
		t.Fatalf("want one ToolCallStart, got %+v", *got)
	}
	v, _ := (*got)[0].data.(events.ToolCallStartData).Arguments["body"].(string)
	if n := len([]rune(v)); n == 0 || n >= 500 {
		t.Fatalf("long argument was not summarized: %d runes", n)
	}
	if !strings.HasSuffix(v, "…") {
		t.Errorf("a truncated value should end with an ellipsis: %q", v)
	}
}

// The behavior change worth naming: an exec command is a tool argument like any
// other now. It used to reach the display in full while a dynamic tool's
// argument of the same length was cut, for no reason a user could see —
// message.SummarizeToolArgs exists precisely so backends report input alike.
func TestEventObserver_SummarizesCommandsToo(t *testing.T) {
	t.Parallel()
	obs, got := newEventObserver()

	obs.ToolCallStarted(ToolCall{
		Name:      toolExec,
		Arguments: map[string]any{argCommand: strings.Repeat("y", 500)},
	})

	v, _ := (*got)[0].data.(events.ToolCallStartData).Arguments[argCommand].(string)
	if n := len([]rune(v)); n >= 500 {
		t.Errorf("an exec command should be summarized like any other argument: %d runes", n)
	}
}

// Nothing about a result is klein's to reshape; it goes across as it arrived.
func TestEventObserver_ResultCrossesUnchanged(t *testing.T) {
	t.Parallel()
	obs, got := newEventObserver()

	obs.ToolCallCompleted(ToolCallResult{Name: "Bash", Content: "exit 1", IsError: true})

	if len(*got) != 1 || (*got)[0].typ != events.EventTypeToolResult {
		t.Fatalf("want one ToolResult, got %+v", *got)
	}
	d := (*got)[0].data.(events.ToolResultData)
	if d.ToolName != "Bash" || d.Content != "exit 1" || !d.IsError {
		t.Errorf("result was reshaped in transit: %+v", d)
	}
}

// The trailing newline is klein's display choice, applied here rather than in
// the client — which hands over the text and says nothing about separation.
func TestEventObserver_ReasoningGetsKleinsSeparator(t *testing.T) {
	t.Parallel()
	obs, got := newEventObserver()

	obs.ReasoningSummary("Plan the build")

	d, ok := (*got)[0].data.(events.ThinkingChunkData)
	if !ok || (*got)[0].typ != events.EventTypeThinkingChunk {
		t.Fatalf("reasoning should land on the thinking stream: %+v", *got)
	}
	if d.Content != "Plan the build\n" {
		t.Errorf("content = %q, want the text plus a newline", d.Content)
	}
}

// A caller wanting no progress passes a nil emit, and that must stay nil all the
// way down rather than become an observer that calls it.
func TestTurnRunner_NilEmitDoesNotBecomeAnObserver(t *testing.T) {
	t.Parallel()

	var obs Observer
	if emit := (func(events.EventType, any))(nil); emit != nil {
		obs = eventObserver{emit: emit}
	}
	if obs != nil {
		t.Error("a nil emit must not be wrapped into a live observer")
	}
}

// SummarizeToolArgs is klein's, and the adapter has to hand it klein's type.
// This pins that the conversion is a conversion and not a re-summarization.
func TestEventObserver_ArgumentsKeepTheirValues(t *testing.T) {
	t.Parallel()
	obs, got := newEventObserver()

	obs.ToolCallStarted(ToolCall{Name: "Lookup", Arguments: map[string]any{argQuery: "needle"}})

	args := (*got)[0].data.(events.ToolCallStartData).Arguments
	if _, isKleinType := any(args).(message.ToolArgumentValues); !isKleinType {
		t.Errorf("arguments should reach the event stream as klein's type, got %T", args)
	}
	if args[argQuery] != "needle" {
		t.Errorf("short values must pass through untouched: %+v", args)
	}
}
