package agentserver

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/pmenglund/codex-sdk-go/rpc"

	"github.com/fpt/klein-cli/pkg/agent/events"
)

const (
	testThread   = "thr_1"
	stInProgress = "inProgress"
	stCompleted  = "completed"
)

// capturedEvent records one emitted progress event for assertions.
type capturedEvent struct {
	Data any
	Type events.EventType
}

// newProgress returns a turnProgress whose emitted events are appended to the
// returned slice.
func newProgress() (*turnProgress, *[]capturedEvent) {
	var got []capturedEvent
	tp := &turnProgress{
		announced: map[string]bool{},
		emit: func(t events.EventType, d any) {
			got = append(got, capturedEvent{Type: t, Data: d})
		},
	}
	return tp, &got
}

// itm builds a ThreadItem with the shared id/type fields plus variant extras.
func itm(id, typ string, extra map[string]any) map[string]any {
	m := map[string]any{"id": id, "type": typ}
	maps.Copy(m, extra)
	return m
}

// noteFor wraps a ThreadItem in a notification for the given method and thread.
func noteFor(method, thread string, item map[string]any) rpc.Notification {
	raw, _ := json.Marshal(map[string]any{"threadId": thread, "item": item})
	return rpc.Notification{Method: method, Raw: raw}
}

func started(item map[string]any) rpc.Notification { return noteFor("item/started", testThread, item) }
func completed(item map[string]any) rpc.Notification {
	return noteFor("item/completed", testThread, item)
}

// feed runs notifications through classifyNote against progress, returning the
// last non-empty assistant text seen.
func feed(progress *turnProgress, notes ...rpc.Notification) string {
	final := ""
	for _, n := range notes {
		if text, _ := classifyNote(n, testThread, progress); text != "" {
			final = text
		}
	}
	return final
}

func cmd(id, command, status string, exitCode int, output string) map[string]any {
	return itm(id, "commandExecution", map[string]any{
		argCommand: command, "status": status, "exitCode": exitCode, "aggregatedOutput": output,
	})
}

func toolResults(got []capturedEvent) []events.ToolResultData {
	var out []events.ToolResultData
	for _, e := range got {
		if d, ok := e.Data.(events.ToolResultData); ok {
			out = append(out, d)
		}
	}
	return out
}

func TestProgress_CommandExecution_StartedThenCompleted(t *testing.T) {
	t.Parallel()
	progress, got := newProgress()

	feed(progress,
		started(cmd("item_1", "go build ./...", stInProgress, 0, "")),
		completed(cmd("item_1", "go build ./...", stCompleted, 0, "ok\n")),
	)

	// Exactly one ToolCallStart (announced at started, not repeated at completed)
	// followed by one ToolResult.
	if len(*got) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(*got), *got)
	}
	if (*got)[0].Type != events.EventTypeToolCallStart {
		t.Fatalf("event 0 = %+v, want ToolCallStart", (*got)[0])
	}
	start := (*got)[0].Data.(events.ToolCallStartData)
	if start.ToolName != toolExec || start.Arguments[argCommand] != "go build ./..." {
		t.Errorf("ToolCallStart = %+v", start)
	}
	if (*got)[1].Type != events.EventTypeToolResult {
		t.Fatalf("event 1 = %+v, want ToolResult", (*got)[1])
	}
	res := (*got)[1].Data.(events.ToolResultData)
	if res.IsError || res.Content == "" {
		t.Errorf("exit 0 should be a non-error result carrying output: %+v", res)
	}
}

func TestProgress_CommandExecution_CompletedOnly_StillAnnounces(t *testing.T) {
	t.Parallel()
	progress, got := newProgress()

	// Some backends may only emit item/completed. The command must still be shown.
	feed(progress, completed(cmd("item_9", "ls", stCompleted, 0, "")))

	if len(*got) != 2 || (*got)[0].Type != events.EventTypeToolCallStart {
		t.Fatalf("completed-only should still announce the call: %+v", *got)
	}
}

func TestProgress_CommandExecution_NonZeroExitIsError(t *testing.T) {
	t.Parallel()
	progress, got := newProgress()

	feed(progress, completed(cmd("item_2", "go test ./...", statusFailed, 1, "FAIL\n")))

	results := toolResults(*got)
	if len(results) != 1 || !results[0].IsError {
		t.Errorf("exit 1 should produce one error result: %+v", results)
	}
}

func TestProgress_Reasoning_EmitsThinking(t *testing.T) {
	t.Parallel()
	progress, got := newProgress()

	feed(progress, completed(itm("item_3", "reasoning", map[string]any{
		"summary": []any{"Plan the build", "Then run it"},
	})))

	if len(*got) != 1 || (*got)[0].Type != events.EventTypeThinkingChunk {
		t.Fatalf("reasoning should emit one thinking chunk: %+v", *got)
	}
	if d := (*got)[0].Data.(events.ThinkingChunkData); d.Content == "" {
		t.Errorf("thinking content should join the summary lines")
	}
}

func TestProgress_FileChange_EmitsPatch(t *testing.T) {
	t.Parallel()
	progress, got := newProgress()

	feed(progress, completed(itm("item_4", "fileChange", map[string]any{
		"status":  stCompleted,
		"changes": []any{map[string]any{"path": "main.go", "kind": "update", "diff": "..."}},
	})))

	if len(*got) != 2 || (*got)[0].Type != events.EventTypeToolCallStart {
		t.Fatalf("fileChange should announce + report: %+v", *got)
	}
	if start := (*got)[0].Data.(events.ToolCallStartData); start.ToolName != toolApplyPatch {
		t.Errorf("fileChange tool = %q, want %q", start.ToolName, toolApplyPatch)
	}
}

func TestProgress_AgentMessage_IsReturnedNotRendered(t *testing.T) {
	t.Parallel()
	progress, got := newProgress()

	final := feed(progress, completed(itm("item_5", "agentMessage", map[string]any{"text": "All done."})))

	if final != "All done." {
		t.Errorf("agentMessage text = %q, want returned as final", final)
	}
	if len(*got) != 0 {
		t.Errorf("agentMessage should not be rendered as progress: %+v", *got)
	}
}

func TestProgress_OtherThreadIgnored(t *testing.T) {
	t.Parallel()
	progress, got := newProgress()

	n := noteFor("item/started", "other", cmd("x", "rm -rf /", stInProgress, 0, ""))
	classifyNote(n, testThread, progress)

	if len(*got) != 0 {
		t.Errorf("notifications for other threads must be ignored: %+v", *got)
	}
}
