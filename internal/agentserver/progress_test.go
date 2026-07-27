package agentserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/pmenglund/codex-sdk-go/rpc"

	"github.com/fpt/klein-cli/pkg/agent/events"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

const (
	testThread   = "thr_1"
	stInProgress = "inProgress"
	stCompleted  = "completed"

	kTool = "tool"
	kArgs = "arguments"
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

func firstStart(t *testing.T, got []capturedEvent) events.ToolCallStartData {
	t.Helper()
	for _, e := range got {
		if d, ok := e.Data.(events.ToolCallStartData); ok {
			return d
		}
	}
	t.Fatal("no ToolCallStart event emitted")
	return events.ToolCallStartData{}
}

func TestProgress_ToolCall_ShowsArgumentsAndResult(t *testing.T) {
	t.Parallel()
	progress, got := newProgress()

	feed(progress, completed(itm("t1", "dynamicToolCall", map[string]any{
		kTool:    "Recall",
		"status": stCompleted,
		kArgs:    map[string]any{argQuery: "sqlite"},
		"result": "#1 [preference]: prefers modernc sqlite",
	})))

	// The call announces its input arguments (not an empty map).
	start := firstStart(t, *got)
	if start.ToolName != "Recall" || start.Arguments[argQuery] != "sqlite" {
		t.Fatalf("ToolCallStart = %+v", start)
	}
	// The result carries the tool's real output (not just "completed").
	res := toolResults(*got)
	if len(res) != 1 || !strings.Contains(res[0].Content, "modernc sqlite") {
		t.Fatalf("ToolResult = %+v", res)
	}
}

func TestProgress_ToolCall_MCPContentItems(t *testing.T) {
	t.Parallel()
	progress, got := newProgress()

	feed(progress, completed(itm("t2", "mcpToolCall", map[string]any{
		"server": "godoc",
		kTool:    "search",
		"status": stCompleted,
		kArgs:    map[string]any{"q": "io.Reader"},
		"content": []any{
			map[string]any{keyType: keyText, keyText: "Reader is the interface..."},
		},
	})))

	if name := firstStart(t, *got).ToolName; name != "godoc/search" {
		t.Errorf("tool name = %q, want godoc/search", name)
	}
	res := toolResults(*got)
	if len(res) != 1 || !strings.Contains(res[0].Content, "Reader is the interface") {
		t.Fatalf("MCP content items not extracted: %+v", res)
	}
}

func TestProgress_ToolCall_FallsBackToStatus(t *testing.T) {
	t.Parallel()
	progress, got := newProgress()

	// No arguments/result present: empty args + status content, i.e. the prior
	// behavior — no regression when a backend omits these fields.
	feed(progress, completed(itm("t3", "dynamicToolCall", map[string]any{
		kTool: "Ping", "status": stCompleted,
	})))

	if args := firstStart(t, *got).Arguments; len(args) != 0 {
		t.Errorf("expected empty args, got %+v", args)
	}
	res := toolResults(*got)
	if len(res) != 1 || res[0].Content != stCompleted {
		t.Fatalf("expected status fallback, got %+v", res)
	}
}

func TestProgress_ToolCall_TruncatesLongArgValue(t *testing.T) {
	t.Parallel()
	progress, got := newProgress()

	long := strings.Repeat("x", 500)
	feed(progress, started(itm("t4", "dynamicToolCall", map[string]any{
		kTool: "Remember",
		kArgs: map[string]any{"content": long},
	})))

	// Arguments are summarized via message.SummarizeToolArgs, so a long value is
	// truncated well below its original length and ends with an ellipsis.
	v, _ := firstStart(t, *got).Arguments["content"].(string)
	if n := len([]rune(v)); n == 0 || n >= 500 {
		t.Fatalf("long arg not truncated: %d runes", n)
	}
	if !strings.HasSuffix(v, "…") {
		t.Errorf("truncated value should end with ellipsis: %q", v)
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

// newProgressWithLogger returns a turnProgress that reports unrendered item
// types into the returned buffer, so the reports can be asserted on.
func newProgressWithLogger() (*turnProgress, *bytes.Buffer) {
	var buf bytes.Buffer
	tp := &turnProgress{
		announced: map[string]bool{},
		reported:  map[string]bool{},
		logger:    pkgLogger.NewLoggerWithConsoleWriter(pkgLogger.LogLevelWarn, &buf),
		emit:      func(events.EventType, any) {},
	}
	return tp, &buf
}

// An item type nothing renders is the signature of a backend and a client that
// have drifted apart. Dropping it silently is what let fpt/rs-gallium#49 sit
// unnoticed, so it has to reach the log.
func TestProgress_UnrenderedItemType_IsReported(t *testing.T) {
	t.Parallel()
	progress, buf := newProgressWithLogger()

	feed(progress, completed(itm("i1", "toolResult", map[string]any{"text": "hi"})))

	if !strings.Contains(buf.String(), "toolResult") {
		t.Errorf("unrendered type was not reported: %q", buf.String())
	}
}

// A backend that sends an unhandled variant on every item must cost one line
// per turn, not one per item.
func TestProgress_UnrenderedItemType_IsReportedOncePerTurn(t *testing.T) {
	t.Parallel()
	progress, buf := newProgressWithLogger()

	for i := range 5 {
		feed(progress, completed(itm(fmt.Sprintf("i%d", i), "toolResult", nil)))
	}

	if n := strings.Count(buf.String(), "toolResult"); n != 1 {
		t.Errorf("want 1 report, got %d: %q", n, buf.String())
	}
}

// Types this renderer knows and deliberately skips are not drift, and must not
// be reported — otherwise the signal drowns in codex's ordinary bookkeeping.
func TestProgress_KnownUnrenderedTypes_AreNotReported(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{"agentMessage", "plan", "sleep", "reviewMode"} {
		progress, buf := newProgressWithLogger()
		feed(progress, completed(itm("i1", typ, nil)))
		if buf.Len() != 0 {
			t.Errorf("%s should be silent, logged: %q", typ, buf.String())
		}
	}
}

// A type the switch handles is obviously not drift.
func TestProgress_RenderedItemType_IsNotReported(t *testing.T) {
	t.Parallel()
	progress, buf := newProgressWithLogger()

	feed(progress, completed(itm("i1", "dynamicToolCall", map[string]any{
		kTool: "memory", "status": stCompleted, "result": "ok",
	})))

	if buf.Len() != 0 {
		t.Errorf("a rendered type should be silent, logged: %q", buf.String())
	}
}

// The logger is optional, and every existing caller leaves it nil.
func TestProgress_NilLogger_DoesNotPanic(t *testing.T) {
	t.Parallel()
	progress, _ := newProgress()

	feed(progress, completed(itm("i1", "toolResult", nil)))
}
