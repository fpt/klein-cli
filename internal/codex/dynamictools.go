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
)

// toolHandler services codex's experimental dynamic-tool callbacks. It embeds
// AutoApproveHandler (all the approval methods) and overrides ItemToolCall to
// dispatch into klein's tool managers — so a codex turn reaches klein's native
// tools over the same stdio JSON-RPC connection (no MCP server).
type toolHandler struct {
	sdk.AutoApproveHandler
	tools domain.ToolManager // klein tools exposed as dynamic tools (may be nil)
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
