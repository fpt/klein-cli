package codex

import (
	"context"

	sdk "github.com/pmenglund/codex-sdk-go"
	"github.com/pmenglund/codex-sdk-go/protocol"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
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

	decisionAccept  = "accept"  // approve a command/file-change request
	decisionDecline = "decline" // deny it; codex continues the turn
)

// ApprovalRequest describes an action codex is asking permission to perform
// (used when approval_policy is "on-request"). Kind is a short verb phrase
// ("run a command", "apply file changes"); Summary is the specifics.
type ApprovalRequest struct {
	Kind    string
	Summary string
}

// Approver decides an approval request. Return true to accept, false to decline.
type Approver func(ApprovalRequest) bool

// toolHandler services codex's server→client callbacks. It embeds
// AutoApproveHandler (the default accept-everything approval methods) and
// overrides ItemToolCall (dispatch klein's dynamic tools) plus the command/file
// approval methods. When approver is nil every request is accepted (headless);
// when set (interactive/on-request) it is asked.
type toolHandler struct {
	sdk.AutoApproveHandler
	tools    domain.ToolManager // klein tools exposed as dynamic tools (may be nil)
	approver Approver           // nil = auto-accept
}

// approve returns true when the request should proceed. Nil approver = accept.
func (h *toolHandler) approve(req ApprovalRequest) bool {
	if h.approver == nil {
		return true
	}
	return h.approver(req)
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
	_ context.Context, p protocol.CommandExecutionRequestApprovalParams,
) (*protocol.CommandExecutionRequestApprovalResponse, error) {
	summary := ptrStr(p.Command)
	if cwd := ptrStr(p.Cwd); cwd != "" {
		summary += "  (in " + cwd + ")"
	}
	if h.approve(ApprovalRequest{Kind: "run a command", Summary: summary}) {
		return &protocol.CommandExecutionRequestApprovalResponse{Decision: decisionAccept}, nil
	}
	return &protocol.CommandExecutionRequestApprovalResponse{Decision: decisionDecline}, nil
}

// ItemFileChangeRequestApproval is called when codex wants to apply file edits.
func (h *toolHandler) ItemFileChangeRequestApproval(
	_ context.Context, p protocol.FileChangeRequestApprovalParams,
) (*protocol.FileChangeRequestApprovalResponse, error) {
	summary := "apply proposed file changes"
	if p.Reason != nil && *p.Reason != "" {
		summary = *p.Reason
	}
	if h.approve(ApprovalRequest{Kind: "edit files", Summary: summary}) {
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
	res, err := h.tools.CallTool(ctx, message.ToolName(p.Tool), message.ToolArgumentValues(args))
	if err != nil {
		return toolCallResult(false, err.Error()), nil
	}
	if res.Error != "" {
		return toolCallResult(false, res.Error), nil
	}
	return toolCallResult(true, res.Text), nil
}

func toolCallResult(success bool, text string) *protocol.DynamicToolCallResponse {
	item := map[string]any{keyType: "inputText", keyText: text}
	return &protocol.DynamicToolCallResponse{
		Success:      success,
		ContentItems: []protocol.SanitizedDynamicToolCallResponseJSONContentItemsElem{item},
	}
}

// buildDynamicTools converts a tool manager's tools into codex FunctionDynamicTool
// specs (type/name/description/inputSchema) for thread/start's dynamicTools field.
func buildDynamicTools(tm domain.ToolManager) []map[string]any {
	if tm == nil {
		return nil
	}
	var out []map[string]any
	for name, t := range tm.GetTools() {
		out = append(out, map[string]any{
			keyType:       "function",
			"name":        string(name),
			"description": t.Description().String(),
			"inputSchema": toInputSchema(t.Arguments()),
		})
	}
	return out
}

// toInputSchema renders klein tool arguments as a JSON Schema object.
func toInputSchema(args []message.ToolArgument) map[string]any {
	props := map[string]any{}
	var required []string
	for _, a := range args {
		p := map[string]any{keyType: jsonType(a.Type)}
		if a.Description != "" {
			p["description"] = string(a.Description)
		}
		props[string(a.Name)] = p
		if a.Required {
			required = append(required, string(a.Name))
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
