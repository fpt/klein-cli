package agentserver

import "context"

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

// Parameter is one input a tool accepts. Type is a JSON Schema type name
// ("string", "number", "integer", "boolean", "array", "object"); anything else
// is treated as a string, since a tool the backend cannot describe is worse than
// one described loosely.
type Parameter struct {
	// Schema carries JSON Schema keys this struct has no field for, merged into
	// the rendered parameter — `items` for an array, `enum`, `properties` for an
	// object. Without it a compound parameter reaches the backend as a bare
	// {"type":"array"}, and a model asked to fill it has nothing to go on but the
	// description. Keys here win over the ones rendered from the fields below,
	// which is what makes it an escape hatch rather than a second opinion.
	Schema      map[string]any
	Name        string
	Description string
	Type        string
	Required    bool
}

// ToolSpec describes one tool to offer the backend. It is deliberately not a
// JSON Schema: the caller says what its tool takes, and the client renders that
// into whatever shape the protocol wants (see buildDynamicTools).
type ToolSpec struct {
	Name        string
	Description string
	Parameters  []Parameter
	// Deferred registers the tool without advertising it: the backend can route
	// a call to it, but does not list it among the tools it offers the model
	// until something makes it visible — a discovery tool the backend provides,
	// typically. It is how a caller offers a large catalog without paying for
	// every schema on every thread.
	//
	// The zero value advertises, which is both the older behavior and the safer
	// one: a backend that has not implemented deferral ignores the field and
	// lists the tool as it always did, so a caller that sets this never loses
	// access to a tool — at worst it does not save anything.
	//
	// Deferral is about the model's attention, not authority. A deferred tool
	// stays callable by name, so this is a context-budget mechanism and never a
	// permission boundary; a caller that must refuse a tool has to refuse it in
	// Call.
	Deferred bool
}

// DynamicTools is a set of tools the caller wants the backend to be able to
// call. They are registered when a thread starts, and the backend then calls
// back for them over the same connection — so the caller services them in its
// own process, against its own live state, rather than handing the backend a
// copy of anything.
//
// Call returns the tool's output. An error is the failure, whatever its origin:
// no such tool, bad arguments, or a tool that ran and reported it could not do
// the job. The protocol carries one failure bit and a message, so the client has
// no use for a finer distinction, and callers with one should flatten it here.
//
// A nil DynamicTools registers nothing, which is a legitimate way to run: the
// backend still has whatever tools it brought with it.
type DynamicTools interface {
	Specs() []ToolSpec
	Call(ctx context.Context, name string, args map[string]any) (string, error)
}

// Dialect selects behavior specific to one app-server implementation, for the
// few places where the protocol is not enough on its own.
//
// It exists for exactly one thing today: codex answers `initialize` even when it
// is logged out, so a login failure would otherwise surface on the user's first
// prompt rather than at startup, and only a codex server has an account to ask
// about. Everything else the client does is the same for every backend, which is
// the point — the protocol subset is a contract, and a conforming server must
// not have to be recognized to be driven.
//
// The zero value is DialectGeneric, so a caller that says nothing gets the
// behavior that assumes nothing.
type Dialect int

const (
	// DialectGeneric — any server implementing the protocol subset.
	DialectGeneric Dialect = iota
	// DialectCodex — the codex app-server, which additionally has an account.
	DialectCodex
)

// Approval policy values for Config.ApprovalPolicy. ApprovalNever leaves the
// backend to act unattended; ApprovalOnRequest makes it ask, which is only
// useful with an Approver that can answer.
const (
	ApprovalNever     = "never"
	ApprovalOnRequest = "on-request"
)

// ApprovalKind says what a backend is asking permission to do. It is a kind, not
// a phrase: how the question is put to whoever answers it is the caller's, and
// only the caller knows whether that is a terminal, a chat message, or a policy
// with nobody watching.
type ApprovalKind int

const (
	// ApprovalCommand — the backend wants to run a shell command.
	ApprovalCommand ApprovalKind = iota
	// ApprovalFileChange — the backend wants to write to files.
	ApprovalFileChange
)

// ApprovalRequest is one such question.
//
// Commands carries the shell commands the request would run, with the backend's
// shell wrapper already stripped — the app-server parses `/bin/zsh -lc 'gh
// --version'` down to `gh --version`, so nothing downstream has to. It is empty
// for a file change, and empty for a command request the backend did not parse.
// An approver that matches on command text must treat that emptiness as "unknown",
// never as "nothing to check": the two look identical and mean opposite things.
type ApprovalRequest struct {
	Summary  string
	Commands []string
	Kind     ApprovalKind
}

// Approver decides an approval request, returning true to let it proceed. It is
// consulted only when the thread runs under an approval policy that asks —
// otherwise the backend is left to its own devices.
//
// ctx is the turn's, so an approver that waits on a human can stop waiting when
// the turn is abandoned rather than holding it open.
//
// A nil Approver accepts everything, which is what a headless caller wants: there
// is nobody to ask, and the alternative is a turn that blocks forever.
type Approver interface {
	Approve(ctx context.Context, req ApprovalRequest) bool
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

// TokenUsage is what one model call cost and what the context can hold.
//
// LastInputTokens rather than a running total is the number that belongs against
// ContextWindow. The backend reports both, and they answer different questions:
// the total is a thread's cumulative spend, which grows without bound and sails
// past the window mid-conversation, while the prompt of the most recent call is
// what the context actually had to hold. A gauge drawn on the total reads over
// 100% and means nothing.
type TokenUsage struct {
	// LastInputTokens is the prompt size of the most recent model call — the
	// occupancy figure, and the one to draw against ContextWindow.
	LastInputTokens int
	// LastOutputTokens is what that call generated.
	LastOutputTokens int
	// TotalTokens is the thread's running spend across every call. A cost
	// figure, not an occupancy one.
	TotalTokens int
	// ContextWindow is the window the backend says it is working against, or 0
	// when it will not vouch for a number.
	//
	// Zero means *do not draw a gauge*. The protocol field is nullable and a
	// backend sends null deliberately, so substituting a plausible constant
	// would turn "unknown" into a percentage — a guess wearing the clothes of a
	// measurement. Report the raw counts, or nothing.
	ContextWindow int
}

// TokenUsageObserver is an optional extension an Observer may implement to
// receive token accounting as a turn runs.
//
// Optional, and detected by type assertion, because Observer is implemented
// outside this module: adding a method to it would break every existing
// implementer to deliver something most of them do not want. An Observer that
// does not implement this is simply not told.
type TokenUsageObserver interface {
	TokenUsageUpdated(TokenUsage)
}

// discardObserver is what a nil Observer becomes, so the render path can call
// through unconditionally.
type discardObserver struct{}

func (discardObserver) ToolCallStarted(ToolCall)         {}
func (discardObserver) ToolCallCompleted(ToolCallResult) {}
func (discardObserver) ReasoningSummary(string)          {}
