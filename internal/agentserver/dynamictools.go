package agentserver

import (
	"context"

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

	decisionAccept  = "accept"  // approve a command/file-change request
	decisionDecline = "decline" // deny it; codex continues the turn
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
	if h.approve(ctx, req) {
		return &protocol.CommandExecutionRequestApprovalResponse{Decision: decisionAccept}, nil
	}
	return &protocol.CommandExecutionRequestApprovalResponse{Decision: decisionDecline}, nil
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
	item := map[string]any{keyType: "inputText", keyText: text}
	return &protocol.DynamicToolCallResponse{
		Success:      success,
		ContentItems: []protocol.SanitizedDynamicToolCallResponseJSONContentItemsElem{item},
	}
}

// buildDynamicTools renders the offered tools as codex FunctionDynamicTool specs
// (type/name/description/inputSchema) for thread/start's dynamicTools field.
func buildDynamicTools(tools DynamicTools) []map[string]any {
	if tools == nil {
		return nil
	}
	var out []map[string]any
	for _, spec := range tools.Specs() {
		out = append(out, map[string]any{
			keyType:       "function",
			"name":        spec.Name,
			"description": spec.Description,
			"inputSchema": toInputSchema(spec.Parameters),
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
