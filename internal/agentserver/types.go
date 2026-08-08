package agentserver

// This file holds the types the app-server client asks its caller to supply.
//
// They exist so the client depends on nothing of klein's. It drives an external
// process over a JSON-RPC protocol — a job with no opinion about klein's tool
// managers, event bus, or logger — and every klein type reachable from its
// public surface is a type an outside importer would be forced to adopt. So the
// client declares the narrowest interface it can use and takes an instance;
// klein supplies an adapter. See doc/DESIGN.md.
//
// Every one of these is optional: a nil value means the client does without.

// Logger is where the client reports protocol drift — an item type it cannot
// render, a notification method it does not handle, an interrupt the backend
// refused. Diagnostics only: nothing here is on a path the caller acts on, and
// the client never logs a turn's ordinary progress (that is Observer's job).
//
// One method, because that is all the client has to say. klein's *logger.Logger
// satisfies it as-is.
//
// A nil Logger is silent, which is what the tests use. Beware the typed nil: a
// nil *logger.Logger assigned here is a non-nil interface holding a nil
// pointer, and calling it panics. Convert at the injection site, where the
// concrete type is still visible (see backendLogger).
type Logger interface {
	Warn(msg string, keysAndValues ...any)
}

// ToolCall is an action the backend has begun — a command it is running, a patch
// it is applying, an MCP or dynamic tool it has called.
//
// Arguments are the call's input as the backend stated it, parsed but otherwise
// untouched: no truncation, no summarization. Those are display decisions, and
// the client does not know what the caller displays to. A payload that is not a
// JSON object at all is carried whole under a single "input" key, since there is
// nothing else useful to say about it.
type ToolCall struct {
	// Arguments leads for field alignment, not emphasis.
	Arguments map[string]any
	Name      string
}

// ToolCallResult is how one of those finished. Content is the output to show —
// an error message, the tool's own output, or the bare status when the backend
// offered nothing else.
type ToolCallResult struct {
	Name    string
	Content string
	IsError bool
}

// Observer receives a turn's intermediate activity as it streams in, so a caller
// can show what the backend is doing instead of waiting out a silence and
// getting only the final text.
//
// Calls arrive from the goroutine draining the backend's notifications, one at a
// time, and the client makes no other promise about timing. An implementation
// that blocks stalls the turn.
//
// A tool call is announced exactly once however the backend reports it (started
// then completed, or completed alone), so ToolCallStarted without a matching
// ToolCallCompleted means the turn ended first, not that the call was lost.
//
// A nil Observer discards everything, which is what a caller wanting only the
// turn's text should pass.
type Observer interface {
	ToolCallStarted(ToolCall)
	ToolCallCompleted(ToolCallResult)
	// ReasoningSummary is a completed block of the backend's own reasoning,
	// already joined into one string and free of trailing punctuation or
	// newlines — how it is separated from surrounding output is the caller's call.
	ReasoningSummary(text string)
}

// discardObserver is what a nil Observer becomes, so the render path can call
// through unconditionally.
type discardObserver struct{}

func (discardObserver) ToolCallStarted(ToolCall)         {}
func (discardObserver) ToolCallCompleted(ToolCallResult) {}
func (discardObserver) ReasoningSummary(string)          {}
