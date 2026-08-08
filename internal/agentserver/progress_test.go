package agentserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/pmenglund/codex-sdk-go/protocol"
	"github.com/pmenglund/codex-sdk-go/rpc"

	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

const (
	testThread   = "thr_1"
	testTurn     = "turn_1"
	stInProgress = "inProgress"
	stCompleted  = "completed"

	kTool = "tool"
	kArgs = "arguments"
)

// eventKind discriminates what a recorded Observer call was, so the assertions
// can keep reading as a flat ordered list of "what the turn reported".
type eventKind int

const (
	kindToolCall eventKind = iota
	kindToolResult
	kindReasoning
)

// capturedEvent records one Observer call for assertions. Data holds the
// argument it carried: a ToolCall, a ToolCallResult, or the reasoning string.
type capturedEvent struct {
	Data any
	Type eventKind
}

// recorder is an Observer that appends every call to a slice.
type recorder struct{ got *[]capturedEvent }

func (r recorder) ToolCallStarted(c ToolCall) {
	*r.got = append(*r.got, capturedEvent{Type: kindToolCall, Data: c})
}

func (r recorder) ToolCallCompleted(res ToolCallResult) {
	*r.got = append(*r.got, capturedEvent{Type: kindToolResult, Data: res})
}

func (r recorder) ReasoningSummary(text string) {
	*r.got = append(*r.got, capturedEvent{Type: kindReasoning, Data: text})
}

// newProgress returns a turnProgress whose observed calls are appended to the
// returned slice.
func newProgress() (*turnProgress, *[]capturedEvent) {
	var got []capturedEvent
	tp := &turnProgress{
		announced: map[string]bool{},
		obs:       recorder{got: &got},
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
	raw, _ := json.Marshal(map[string]any{keyThreadID: thread, "item": item})
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

func toolResults(got []capturedEvent) []ToolCallResult {
	var out []ToolCallResult
	for _, e := range got {
		if d, ok := e.Data.(ToolCallResult); ok {
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
	if (*got)[0].Type != kindToolCall {
		t.Fatalf("event 0 = %+v, want ToolCallStart", (*got)[0])
	}
	start := (*got)[0].Data.(ToolCall)
	if start.Name != toolExec || start.Arguments[argCommand] != "go build ./..." {
		t.Errorf("ToolCallStart = %+v", start)
	}
	if (*got)[1].Type != kindToolResult {
		t.Fatalf("event 1 = %+v, want ToolResult", (*got)[1])
	}
	res := (*got)[1].Data.(ToolCallResult)
	if res.IsError || res.Content == "" {
		t.Errorf("exit 0 should be a non-error result carrying output: %+v", res)
	}
}

func TestProgress_CommandExecution_CompletedOnly_StillAnnounces(t *testing.T) {
	t.Parallel()
	progress, got := newProgress()

	// Some backends may only emit item/completed. The command must still be shown.
	feed(progress, completed(cmd("item_9", "ls", stCompleted, 0, "")))

	if len(*got) != 2 || (*got)[0].Type != kindToolCall {
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

	if len(*got) != 1 || (*got)[0].Type != kindReasoning {
		t.Fatalf("reasoning should emit one thinking chunk: %+v", *got)
	}
	if text := (*got)[0].Data.(string); text == "" {
		t.Errorf("reasoning text should join the summary lines")
	}
}

func TestProgress_FileChange_EmitsPatch(t *testing.T) {
	t.Parallel()
	progress, got := newProgress()

	feed(progress, completed(itm("item_4", "fileChange", map[string]any{
		"status":  stCompleted,
		"changes": []any{map[string]any{"path": "main.go", "kind": "update", "diff": "..."}},
	})))

	if len(*got) != 2 || (*got)[0].Type != kindToolCall {
		t.Fatalf("fileChange should announce + report: %+v", *got)
	}
	if start := (*got)[0].Data.(ToolCall); start.Name != toolApplyPatch {
		t.Errorf("fileChange tool = %q, want %q", start.Name, toolApplyPatch)
	}
}

func firstStart(t *testing.T, got []capturedEvent) ToolCall {
	t.Helper()
	for _, e := range got {
		if d, ok := e.Data.(ToolCall); ok {
			return d
		}
	}
	t.Fatal("no tool call was announced")
	return ToolCall{}
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
	if start.Name != "Recall" || start.Arguments[argQuery] != "sqlite" {
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

	if name := firstStart(t, *got).Name; name != "godoc/search" {
		t.Errorf("tool name = %q, want godoc/search", name)
	}
	res := toolResults(*got)
	if len(res) != 1 || !strings.Contains(res[0].Content, "Reader is the interface") {
		t.Fatalf("MCP content items not extracted: %+v", res)
	}
}

// A dynamicToolCall carries its output in contentItems, which is the only place
// codex defines output for that variant — it has no `result` field at all. That
// is what rs-gallium emits, so without this the entire progress display for
// every gallium tool call degrades to the bare status string.
func TestProgress_ToolCall_DynamicContentItems(t *testing.T) {
	t.Parallel()
	progress, got := newProgress()

	feed(progress, completed(itm("t2b", "dynamicToolCall", map[string]any{
		kTool:    "Write",
		"status": stCompleted,
		kArgs:    map[string]any{"file_path": "a.txt"},
		"contentItems": []any{
			map[string]any{keyType: "inputText", keyText: "Wrote 3 lines to a.txt"},
		},
	})))

	res := toolResults(*got)
	if len(res) != 1 || !strings.Contains(res[0].Content, "Wrote 3 lines to a.txt") {
		t.Fatalf("contentItems not extracted: %+v", res)
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

// The client hands arguments over as the backend stated them. Truncating here
// was display policy inside a protocol client, and it reached only some kinds of
// call — see eventObserver, which now applies one rule to all of them.
func TestProgress_ToolCall_ArgumentsArePassedThroughWhole(t *testing.T) {
	t.Parallel()
	progress, got := newProgress()

	long := strings.Repeat("x", 500)
	feed(progress, started(itm("t4", "dynamicToolCall", map[string]any{
		kTool: "Remember",
		kArgs: map[string]any{"content": long},
	})))

	v, _ := firstStart(t, *got).Arguments["content"].(string)
	if n := len([]rune(v)); n != 500 {
		t.Errorf("argument should reach the observer intact: got %d runes, want 500", n)
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
		obs:       discardObserver{},
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
// Driven off the map itself so an entry added there is covered without also
// being remembered here.
func TestProgress_KnownUnrenderedTypes_AreNotReported(t *testing.T) {
	t.Parallel()
	for typ := range itemTypesKnownUnrendered {
		progress, buf := newProgressWithLogger()
		feed(progress, completed(itm("i1", typ, nil)))
		if buf.Len() != 0 {
			t.Errorf("%s should be silent, logged: %q", typ, buf.String())
		}
	}
}

// codex opens every turn by echoing the prompt back as a userMessage item, on
// both item/started and item/completed. Missing from the known-skipped set, that
// made the drift warning fire on literally every prompt. Uses the payload codex
// 0.144.1 actually sends (content items, no top-level text) rather than the
// minimal stub, so it also pins that the echo is not mistaken for turn output.
func TestProgress_UserMessageEcho_IsSilentAndNotTurnText(t *testing.T) {
	t.Parallel()
	progress, buf := newProgressWithLogger()

	echo := itm("019fa749", "userMessage", map[string]any{
		"clientId": nil,
		"content":  []any{map[string]any{keyType: keyText, keyText: "Reply with exactly: ok"}},
	})
	final := feed(progress, started(echo), completed(echo))

	if buf.Len() != 0 {
		t.Errorf("codex's prompt echo must not be reported as drift, logged: %q", buf.String())
	}
	if final != "" {
		t.Errorf("the prompt echo must not become the turn's text, got %q", final)
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

// turnEnd builds a turn/completed notification carrying codex's Turn object.
func turnEnd(status string) rpc.Notification {
	raw, _ := json.Marshal(map[string]any{
		keyThreadID: testThread,
		"turn":      map[string]any{"id": testTurn, "status": status},
	})
	return rpc.Notification{Method: "turn/completed", Raw: raw}
}

// codex ends every turn with turn/completed and puts the outcome in the status,
// so the method alone cannot say whether the turn worked. Reading only the
// method reported a failed turn as a successful one carrying whatever text
// happened to precede it — silently, which is the worst way to be wrong about
// this. See fpt/rs-gallium#77.
func TestClassify_TurnCompletedStatusDecidesTheOutcome(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		status string
		want   noteStatus
	}{
		{"completed", noteDone},
		{"failed", noteFailed},
		// The turn ended the way it was asked to, and the caller keeps the text
		// it had produced — which is what someone who pressed Ctrl+C wants back.
		{"interrupted", noteDone},
		// A backend that says nothing is taken at its word that the turn ended.
		{"", noteDone},
	} {
		progress, _ := newProgress()
		if _, got := classifyNote(turnEnd(tc.status), testThread, progress); got != tc.want {
			t.Errorf("status %q: got %v, want %v", tc.status, got, tc.want)
		}
	}
}

// errNote builds codex's `error` server notification (v2 ErrorNotification).
func errNote(message string, willRetry bool) rpc.Notification {
	raw, _ := json.Marshal(map[string]any{
		keyThreadID: testThread,
		keyTurnID:   testTurn,
		"willRetry": willRetry,
		methodError: map[string]any{"message": message},
	})
	return rpc.Notification{Method: methodError, Raw: raw}
}

// codex sends `error` with willRetry: true for a stream error it retries itself,
// and says so in the protocol: "If true, this will not interrupt a turn."
// Treating it as a failure reported a failure that never happened and abandoned
// a turn the backend was still running — whose slot then refused the next
// turn/start.
func TestClassify_TransientErrorDoesNotEndTheTurn(t *testing.T) {
	t.Parallel()
	progress, buf := newProgressWithLogger()

	if _, got := classifyNote(errNote("stream disconnected", true), testThread, progress); got != noteContinue {
		t.Errorf("got %v, want noteContinue", got)
	}
	// A retry stalls the turn, so it must not stall silently.
	if !strings.Contains(buf.String(), "stream disconnected") {
		t.Errorf("transient error was not reported: %q", buf.String())
	}
}

// The same notification with willRetry: false is codex's real turn error.
func TestClassify_NonRetriedErrorFailsTheTurn(t *testing.T) {
	t.Parallel()
	progress, _ := newProgress()

	if _, got := classifyNote(errNote("model refused", false), testThread, progress); got != noteFailed {
		t.Errorf("got %v, want noteFailed", got)
	}
}

// unknownNote builds a notification the way the SDK hands one over for a method
// its generated table has no entry for: raw params, no decoded payload.
func unknownNote(method string) rpc.Notification {
	raw, _ := json.Marshal(map[string]any{keyThreadID: testThread})
	return rpc.Notification{Method: method, Raw: raw}
}

// A method no arm handles and codex does not define is a client and a backend
// that have drifted apart — the message-level twin of an unrendered item type,
// and silently dropping it is what klein used to do. rs-gallium warns on the
// same shape coming the other way.
func TestClassify_UnhandledMethod_IsReported(t *testing.T) {
	t.Parallel()
	progress, buf := newProgressWithLogger()

	if _, got := classifyNote(unknownNote("turn/failed"), testThread, progress); got != noteContinue {
		t.Errorf("an unhandled method must not decide the turn: got %v", got)
	}
	if !strings.Contains(buf.String(), "turn/failed") {
		t.Errorf("unhandled method was not reported: %q", buf.String())
	}
}

// A backend that repeats an unknown method costs one line per turn, not one per
// notification — the same budget reportUnrendered keeps for item types.
func TestClassify_UnhandledMethod_IsReportedOncePerTurn(t *testing.T) {
	t.Parallel()
	progress, buf := newProgressWithLogger()

	for range 5 {
		classifyNote(unknownNote("gallium/whatever"), testThread, progress)
	}

	if n := strings.Count(buf.String(), "gallium/whatever"); n != 1 {
		t.Errorf("want 1 report, got %d: %q", n, buf.String())
	}
}

// Notifications codex defines and klein simply has nothing to show for — the
// per-token deltas above all — must stay silent, or the one line that means
// something drowns in them. The SDK marks those by handing over a decoded
// payload, which is the discriminator reportUnhandledMethod reads.
func TestClassify_ProtocolKnownMethod_IsNotReported(t *testing.T) {
	t.Parallel()
	progress, buf := newProgressWithLogger()

	raw, _ := json.Marshal(map[string]any{keyThreadID: testThread, "delta": "hel"})
	n := rpc.Notification{
		Method: "item/agentMessage/delta",
		Params: protocol.AgentMessageDeltaNotification{},
		Raw:    raw,
	}
	if _, got := classifyNote(n, testThread, progress); got != noteContinue {
		t.Errorf("a known-but-unhandled method must not decide the turn: got %v", got)
	}
	if buf.Len() != 0 {
		t.Errorf("codex's own streaming notifications must stay silent, logged: %q", buf.String())
	}
}

// A handled method is obviously not drift.
func TestClassify_HandledMethod_IsNotReported(t *testing.T) {
	t.Parallel()
	progress, buf := newProgressWithLogger()

	classifyNote(turnEnd("completed"), testThread, progress)

	if buf.Len() != 0 {
		t.Errorf("a handled method should be silent, logged: %q", buf.String())
	}
}

// The logger is optional here too, and the runner leaves it nil in tests.
func TestClassify_UnhandledMethod_NilLoggerDoesNotPanic(t *testing.T) {
	t.Parallel()
	progress, _ := newProgress()

	classifyNote(unknownNote("gallium/whatever"), testThread, progress)
}
