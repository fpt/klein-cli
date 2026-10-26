package agentserver

import (
	"context"
	"strings"

	sdk "github.com/pmenglund/codex-sdk-go"
	"github.com/pmenglund/codex-sdk-go/protocol"
)

// JSON Schema type names and content keys used in dynamic-tool specs/results.
const (
	jsonTypeString  = "string"
	jsonTypeNumber  = "number"
	jsonTypeInteger = "integer"
	jsonTypeBoolean = "boolean"
	jsonTypeArray   = "array"
	jsonTypeObject  = "object"
	keyText         = "text"
	keyType         = "type"
	keyThreadID     = "threadId"
	keyTurnID       = "turnId"
	// keyAdvertised marks whether the backend should list a registered tool
	// among those it offers the model. Written only when false; see
	// buildDynamicTools.
	keyAdvertised = "advertised"
	// keyDeferLoading is codex's spelling of the same request. codex accepts it
	// only on a tool inside a namespace spec, so it is never written beside
	// keyAdvertised; see buildDynamicTools.
	keyDeferLoading = "deferLoading"

	// deferredNamespace names the namespace codex's deferred tools are grouped
	// under, and deferredNamespaceDescription is what its tool search shows for
	// the group as a whole.
	deferredNamespace            = "deferred_tools"
	deferredNamespaceDescription = "Additional tools whose schemas are loaded on demand. " +
		"Search for one by name or purpose before calling it."

	decisionAccept  = "accept"  // approve a file-change request
	decisionDecline = "decline" // deny it; codex continues the turn
)

// A command-execution decision is a union in the protocol (some variants carry an
// amendment payload), so the two simple variants klein uses are wrapped once here.
// The kinds are protocol constants, so the checked constructor cannot reject them.
var (
	cmdDecisionAccept = protocol.MustCommandExecutionApprovalDecision(
		protocol.CommandExecutionApprovalDecisionKindAccept)
	cmdDecisionDecline = protocol.MustCommandExecutionApprovalDecision(
		protocol.CommandExecutionApprovalDecisionKindDecline)
)

// toolHandler services codex's server→client callbacks. It embeds
// AutoApproveHandler (the default accept-everything approval methods) and
// overrides ItemToolCall (dispatch klein's dynamic tools) plus the command/file
// approval methods. When approver is nil every request is accepted (headless);
// when set (interactive/on-request) it is asked.
type toolHandler struct {
	sdk.AutoApproveHandler
	tools    DynamicTools // tools offered to the backend (may be nil)
	approver Approver     // nil = auto-accept
}

// approve returns true when the request should proceed. Nil approver = accept.
func (h *toolHandler) approve(ctx context.Context, req ApprovalRequest) bool {
	if h.approver == nil {
		return true
	}
	return h.approver.Approve(ctx, req)
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ItemCommandExecutionRequestApproval is called when codex wants to run a shell
// command and approval_policy requires a decision.
func (h *toolHandler) ItemCommandExecutionRequestApproval(
	ctx context.Context, p protocol.CommandExecutionRequestApprovalParams,
) (*protocol.CommandExecutionRequestApprovalResponse, error) {
	summary := ptrStr(p.Command)
	if cwd := ptrStr(p.Cwd); cwd != "" {
		summary += "  (in " + cwd + ")"
	}
	req := ApprovalRequest{
		Kind:     ApprovalCommand,
		Summary:  summary,
		Commands: commandActionCommands(p.CommandActions),
	}
	// writeStdin asks to send input to a terminal that is already running. The
	// command and its actions name that terminal's process, not the input — which
	// the request does not carry at all. Passing the actions on would let an
	// allowlist that trusts the process approve whatever is typed into it, so the
	// request goes to the prompt with nothing to match against.
	if p.Kind == protocol.CommandExecutionApprovalKindWriteStdin {
		req.Summary = "write to the stdin of: " + summary
		req.Commands = nil
	}
	if h.approve(ctx, req) {
		return &protocol.CommandExecutionRequestApprovalResponse{Decision: cmdDecisionAccept}, nil
	}
	return &protocol.CommandExecutionRequestApprovalResponse{Decision: cmdDecisionDecline}, nil
}

// ItemFileChangeRequestApproval is called when codex wants to apply file edits.
func (h *toolHandler) ItemFileChangeRequestApproval(
	ctx context.Context, p protocol.FileChangeRequestApprovalParams,
) (*protocol.FileChangeRequestApprovalResponse, error) {
	summary := "apply proposed file changes"
	if p.Reason != nil && *p.Reason != "" {
		summary = *p.Reason
	}
	if h.approve(ctx, ApprovalRequest{Kind: ApprovalFileChange, Summary: summary}) {
		return &protocol.FileChangeRequestApprovalResponse{Decision: decisionAccept}, nil
	}
	return &protocol.FileChangeRequestApprovalResponse{Decision: decisionDecline}, nil
}

func (h *toolHandler) ItemToolCall(
	ctx context.Context, p protocol.DynamicToolCallParams,
) (*protocol.DynamicToolCallResponse, error) {
	if h.tools == nil {
		return toolCallResult(false, "no tools available"), nil
	}
	args, _ := p.Arguments.(map[string]any)
	// A failed call is reported to the backend, not returned as an RPC error: the
	// turn continues, and the backend decides what to do about it.
	out, err := h.tools.Call(ctx, p.Tool, args)
	if err != nil {
		return toolCallResult(false, err.Error()), nil
	}
	return toolCallResult(true, out), nil
}

func toolCallResult(success bool, text string) *protocol.DynamicToolCallResponse {
	// A content item is a union keyed on `type`; this is the fixed inputText
	// variant with its one required field, so the constructor cannot reject it.
	item, _ := protocol.NewDynamicToolCallOutputContentItem(
		map[string]any{keyType: "inputText", keyText: text},
	)
	return &protocol.DynamicToolCallResponse{
		Success:      success,
		ContentItems: []protocol.DynamicToolCallOutputContentItem{item},
	}
}

// buildDynamicTools renders the offered tools as codex FunctionDynamicTool specs
// (type/name/description/inputSchema) for thread/start's dynamicTools field.
//
// A deferred tool is spelled per dialect, because the two known servers ask for
// deferral in incompatible ways:
//
//   - rs-gallium (and the generic default) takes "advertised": false on the tool
//     itself. The key is written only when deferring, so the payload for a caller
//     that defers nothing is byte-identical to what it always was, and a backend
//     that has never heard of the field skips an unknown key and advertises the
//     tool — the degradation that makes deferral safe to ask for before the other
//     end supports it.
//   - codex takes "deferLoading": true, and only on a tool inside a namespace
//     spec: a top-level deferred function is refused at thread/start ("deferred
//     dynamic tool must include a namespace", codex-cli 0.164.0). So the deferred
//     tools travel together in one namespace after the advertised ones. Its tool
//     calls name the namespace beside the tool, and dispatch is by tool name
//     alone, so nothing on the calling side changes.
func buildDynamicTools(tools DynamicTools, dialect Dialect) []map[string]any {
	if tools == nil {
		return nil
	}
	var out, deferred []map[string]any
	for _, spec := range tools.Specs() {
		entry := map[string]any{
			keyType:       "function",
			"name":        spec.Name,
			"description": spec.Description,
			"inputSchema": toInputSchema(spec.Parameters),
		}
		switch {
		case !spec.Deferred:
			out = append(out, entry)
		case dialect == DialectCodex:
			entry[keyDeferLoading] = true
			deferred = append(deferred, entry)
		default:
			entry[keyAdvertised] = false
			out = append(out, entry)
		}
	}
	if len(deferred) > 0 {
		out = append(out, map[string]any{
			keyType:       "namespace",
			"name":        deferredNamespace,
			"description": deferredNamespaceDescription,
			"tools":       deferred,
		})
	}
	return out
}

// toInputSchema renders a tool's parameters as a JSON Schema object.
func toInputSchema(params []Parameter) map[string]any {
	props := map[string]any{}
	var required []string
	for _, a := range params {
		p := map[string]any{keyType: jsonType(a.Type)}
		if a.Description != "" {
			p["description"] = a.Description
		}
		// An array's `items`, an enum's values: the shape a caller could not say
		// in the struct's fields. Last, so an explicit key overrides.
		for k, v := range a.Schema {
			p[k] = v
		}
		props[a.Name] = p
		if a.Required {
			required = append(required, a.Name)
		}
	}
	schema := map[string]any{keyType: jsonTypeObject, "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func jsonType(t string) string {
	switch t {
	case jsonTypeNumber, jsonTypeInteger, jsonTypeBoolean, jsonTypeArray, jsonTypeObject:
		return t
	default:
		return jsonTypeString
	}
}

// commandActionCommands pulls the parsed command out of each of the app-server's
// CommandAction entries, or returns nil if any entry cannot be read.
//
// The actions are typed (read/listFiles/search/unknown) but the type is only for
// friendlier display — every variant carries the same `command` string, and an
// ordinary program like `gh` arrives as `unknown`. So the type is ignored and the
// command is what matters.
//
// All or nothing is the point of the nil. Skipping an unreadable action and
// returning the rest would hand WithAutoApprove a list it could find entirely
// allowlisted while an action nobody could read went with it. Returning nothing
// sends the request to the prompt instead, which is the answer for a request
// klein does not fully understand.
func commandActionCommands(actions []interface{}) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		action, ok := a.(map[string]any)
		if !ok {
			return nil
		}
		cmd, ok := action["command"].(string)
		if !ok || strings.TrimSpace(cmd) == "" {
			return nil
		}
		out = append(out, strings.TrimSpace(cmd))
	}
	return out
}
