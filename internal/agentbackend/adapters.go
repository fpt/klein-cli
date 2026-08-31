package agentbackend

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/agent/events"
	"github.com/fpt/klein-cli/pkg/agentserver"
	"github.com/fpt/klein-cli/pkg/message"
)

// This file is klein's side of the client's injected interfaces: the adapters
// that carry klein's types across a boundary the client itself knows nothing
// about. Everything klein-specific about driving an app-server should end up
// here rather than inside the client. See types.go.

// toolHost offers klein's tools to the backend. The backend calls back for them
// over the app-server connection, so they run against the same live manager the
// rest of klein uses — same files, same locks — rather than a copy.
type toolHost struct{ tools domain.ToolManager }

// newToolHost wraps a tool manager, or returns nil when there is none to offer.
//
// The guard is the same idea as backendLogger's and not the same code, because
// the parameter is an interface rather than a concrete pointer. `tm == nil`
// therefore catches only a caller that passed a literal nil; a nil
// *tool.CompositeToolManager arrives as a non-nil domain.ToolManager, and would
// wrap into a toolHost that registers a tool set and panics enumerating it.
// Nothing calls it that way today, but "no caller does this yet" is not what the
// doc above says, so isNil makes the claim true instead of narrowing it.
func newToolHost(tm domain.ToolManager) agentserver.DynamicTools {
	if isNil(tm) {
		return nil
	}
	return toolHost{tools: tm}
}

// newSplitToolHost offers two groups: tools the backend advertises to the model,
// and tools it registers but keeps back until something makes them visible.
//
// The split is by manager rather than by a set of names, because that is how
// klein already knows the difference — the deferred group is the proxied MCP
// managers, whole. Nothing has to enumerate tool names to classify them, so a
// server that grows a tool does not need klein to be told about it.
//
// A nil or empty deferred group collapses to a plain toolHost: identical specs,
// identical wire payload, none of this reachable. That is the shape every caller
// had before deferral existed, and the one they keep by not asking for it.
func newSplitToolHost(advertised, deferred domain.ToolManager) agentserver.DynamicTools {
	if isNil(deferred) || len(deferred.GetTools()) == 0 {
		return newToolHost(advertised)
	}
	if isNil(advertised) {
		return deferredOnlyHost(deferred)
	}
	return splitToolHost{
		advertised: toolHost{tools: advertised},
		deferred:   toolHost{tools: deferred},
	}
}

// deferredOnlyHost is the degenerate split: everything deferred, nothing
// advertised. Legitimate — a backend with its own tools may want klein's only on
// request — so it is built rather than refused.
func deferredOnlyHost(deferred domain.ToolManager) agentserver.DynamicTools {
	return splitToolHost{
		advertised: toolHost{tools: tool.NewCompositeToolManager()},
		deferred:   toolHost{tools: deferred},
	}
}

// splitToolHost renders one group advertised and the other deferred, and routes
// a call to whichever group holds the name.
type splitToolHost struct {
	advertised toolHost
	deferred   toolHost
}

// Specs lists both groups, marking the deferred half.
func (h splitToolHost) Specs() []agentserver.ToolSpec {
	specs := h.advertised.Specs()
	for _, spec := range h.deferred.Specs() {
		spec.Deferred = true
		specs = append(specs, spec)
	}
	return specs
}

// Call routes to whichever group holds the name, advertised first.
//
// A deferred tool is callable exactly like an advertised one: deferral governs
// what the model is told about, never what it is allowed to reach. A backend
// that lets a model call a tool it was never shown — by guessing the name, or
// because its own matching is lenient — gets the tool, and that is the intended
// contract.
func (h splitToolHost) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	if _, ok := h.advertised.tools.GetTools()[message.ToolName(name)]; ok {
		return h.advertised.Call(ctx, name, args)
	}
	return h.deferred.Call(ctx, name, args)
}

// isNil reports whether an interface value is nil or holds a nil pointer.
//
// The second case is the one worth the reflection: a nil pointer stored in an
// interface makes the interface itself non-nil, so an `== nil` guard passes it
// through and the panic lands later, at the first method call, somewhere that
// has no idea where the value came from.
func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		return rv.IsNil()
	default:
		// A non-nilable kind (a struct value, say) is never nil.
		return false
	}
}

// Specs describes klein's tools in the client's vocabulary. Ordering follows map
// iteration, as it always has — the backend matches on name, not position.
func (h toolHost) Specs() []agentserver.ToolSpec {
	out := make([]agentserver.ToolSpec, 0, len(h.tools.GetTools()))
	for name, t := range h.tools.GetTools() {
		params := make([]agentserver.Parameter, 0, len(t.Arguments()))
		for _, a := range t.Arguments() {
			params = append(params, agentserver.Parameter{
				Name:        string(a.Name),
				Description: string(a.Description),
				Type:        a.Type,
				Required:    a.Required,
				// klein's compound arguments carry their shape here (MultiEdit's
				// `edits` is an array of objects, and its element schema is the
				// only thing saying what an element looks like). Dropping it left
				// the backend a bare {"type":"array"} to guess at.
				Schema: a.Properties,
			})
		}
		out = append(out, agentserver.ToolSpec{
			Name:        string(name),
			Description: t.Description().String(),
			Parameters:  params,
		})
	}
	return out
}

// Call runs one of klein's tools for the backend.
//
// klein reports a failed tool two ways — a returned error, or a ToolResult
// carrying an Error string — and the protocol has room for neither distinction:
// one bit and a message. So both flatten into an error here, which is what the
// client asks for and what the old code did anyway once it reached the wire.
//
// Images on the result are dropped, as they always have been; the dynamic-tool
// response carries text.
func (h toolHost) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	res, err := h.tools.CallTool(ctx, message.ToolName(name), message.ToolArgumentValues(args))
	if err != nil {
		return "", fmt.Errorf("calling %s: %w", name, err)
	}
	if res.Error != "" {
		return "", errors.New(res.Error)
	}
	return res.Text, nil
}

// eventObserver reports a turn's activity onto klein's event stream, so a
// backend turn renders through the same path as the native ReAct loop.
//
// Summarization happens here, and applies to every call. That is the point of
// doing it on this side: message.SummarizeToolArgs exists so "tool-call input is
// reported identically regardless of which backend produced it" (its own words),
// and the native loop already runs it over every tool call. The app-server path
// used to run it over some of them — dynamic and MCP calls were summarized, exec
// and apply_patch were not — which is only visible as a long command printing in
// full where a long tool argument would have been cut. Now the rule is one rule.
type eventObserver struct {
	emit func(events.EventType, any)
}

func (o eventObserver) ToolCallStarted(call agentserver.ToolCall) {
	o.emit(events.EventTypeToolCallStart, events.ToolCallStartData{
		ToolName:  call.Name,
		Arguments: message.SummarizeToolArgs(call.Arguments),
	})
}

func (o eventObserver) ToolCallCompleted(res agentserver.ToolCallResult) {
	o.emit(events.EventTypeToolResult, events.ToolResultData{
		ToolName: res.Name,
		Content:  res.Content,
		IsError:  res.IsError,
	})
}

// ReasoningSummary lands on the thinking stream. The trailing newline is klein's
// display choice — the client hands over the text and says nothing about how it
// is separated from what surrounds it.
func (o eventObserver) ReasoningSummary(text string) {
	o.emit(events.EventTypeThinkingChunk, events.ThinkingChunkData{Content: text + "\n"})
}

// TokenUsageUpdated puts the backend's context accounting on klein's event
// stream. Implementing agentserver.TokenUsageObserver is what asks for it — the
// client only reports to an observer that says it wants to know.
func (o eventObserver) TokenUsageUpdated(u agentserver.TokenUsage) {
	o.emit(events.EventTypeTokenUsage, events.TokenUsageData{
		InputTokens:   u.LastInputTokens,
		OutputTokens:  u.LastOutputTokens,
		TotalTokens:   u.TotalTokens,
		ContextWindow: u.ContextWindow,
	})
}

// turnRunner adapts a Runner to domain.BackendRunner, the interface the app layer
// routes a whole-agent turn through.
//
// The two no longer share a RunTurn signature, and deliberately so: the app layer
// speaks klein's event stream, the client speaks Observer, and this is the one
// place that has to know both.
type turnRunner struct{ runner *agentserver.Runner }

func (t turnRunner) RunTurn(
	ctx context.Context, threadID, prompt, developerInstructions string, emit func(events.EventType, any),
) (string, string, error) {
	// A nil emit means the caller wants no progress, and must stay nil rather
	// than become an observer that calls it — the client already discards for us.
	var obs agentserver.Observer
	if emit != nil {
		obs = eventObserver{emit: emit}
	}
	newThreadID, text, err := t.runner.RunTurn(ctx, threadID, prompt, developerInstructions, obs)
	if err != nil {
		return newThreadID, text, fmt.Errorf("app-server turn: %w", err)
	}
	return newThreadID, text, nil
}
