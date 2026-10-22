package gemini

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"

	"google.golang.org/genai"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// convertGeminiArgsToToolArgs converts Gemini function arguments JSON to tool argument values
func convertGeminiArgsToToolArgs(argsJSON string) message.ToolArgumentValues {
	result := make(message.ToolArgumentValues)

	if argsJSON == "" {
		return result
	}

	// Parse JSON arguments
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		// If parsing fails, return empty map
		return result
	}

	// Convert interface{} values to proper types
	for key, value := range args {
		result[key] = value
	}

	return result
}

// convertToolArgsToJSON converts tool argument values to JSON string
func convertToolArgsToJSON(args message.ToolArgumentValues) string {
	if len(args) == 0 {
		return "{}"
	}

	jsonBytes, err := json.Marshal(args)
	if err != nil {
		// If marshaling fails, return empty object
		return "{}"
	}

	return string(jsonBytes)
}

// convertToolsToGemini converts domain tools to Gemini function format using tool manager's schema
func convertToolsToGemini(tools map[message.ToolName]message.Tool) []*genai.Tool {
	if len(tools) == 0 {
		return nil
	}

	var geminiTools []*genai.Tool

	// Group all functions under a single Tool
	var functionDeclarations []*genai.FunctionDeclaration

	for _, tool := range tools {
		// Create properties from tool arguments using the same schema logic as OpenAI
		properties := make(map[string]*genai.Schema)
		var required []string

		for _, arg := range tool.Arguments() {
			// Convert tool argument to Gemini schema using JSON schema conversion
			schema := convertArgumentToGeminiSchema(arg)
			properties[string(arg.Name)] = schema

			if arg.Required {
				required = append(required, string(arg.Name))
			}
		}

		// Create Gemini function declaration
		functionDecl := &genai.FunctionDeclaration{
			Name:        string(tool.Name()),
			Description: tool.Description().String(),
			Parameters: &genai.Schema{
				Type:       genai.TypeObject,
				Properties: properties,
				Required:   required,
			},
		}

		functionDeclarations = append(functionDeclarations, functionDecl)
	}

	// Create a single tool with all function declarations
	if len(functionDeclarations) > 0 {
		geminiTool := &genai.Tool{
			FunctionDeclarations: functionDeclarations,
		}
		geminiTools = append(geminiTools, geminiTool)
	}

	return geminiTools
}

// convertArgumentToGeminiSchema converts a ToolArgument to Gemini schema format
func convertArgumentToGeminiSchema(arg message.ToolArgument) *genai.Schema {
	schema := &genai.Schema{
		Description: arg.Description.String(),
	}

	// Map types to Gemini schema types
	switch arg.Type {
	case "string":
		schema.Type = genai.TypeString
	case "number":
		schema.Type = genai.TypeNumber
	case "integer":
		schema.Type = genai.TypeInteger
	case "boolean":
		schema.Type = genai.TypeBoolean
	case "array":
		schema.Type = genai.TypeArray
		// Try to infer array item schema from description
		if itemSchema := inferGeminiArrayItemSchema(arg.Description.String()); itemSchema != nil {
			schema.Items = itemSchema
		}
	case "object":
		schema.Type = genai.TypeObject
	default:
		// Default to string for unknown types
		schema.Type = genai.TypeString
	}

	return schema
}

// inferGeminiArrayItemSchema attempts to infer array item schema for Gemini format
func inferGeminiArrayItemSchema(desc string) *genai.Schema {
	lowerDesc := strings.ToLower(desc)

	// String array
	if strings.Contains(lowerDesc, "string") || strings.Contains(lowerDesc, "text") {
		return &genai.Schema{Type: genai.TypeString}
	}

	// Number array
	if strings.Contains(lowerDesc, "number") || strings.Contains(lowerDesc, "numeric") {
		return &genai.Schema{Type: genai.TypeNumber}
	}

	// Integer array
	if strings.Contains(lowerDesc, "integer") {
		return &genai.Schema{Type: genai.TypeInteger}
	}

	// Boolean array
	if strings.Contains(lowerDesc, "boolean") || strings.Contains(lowerDesc, "bool") {
		return &genai.Schema{Type: genai.TypeBoolean}
	}

	// Default to object for complex arrays
	return &genai.Schema{Type: genai.TypeObject}
}

// convertToolChoiceToGemini converts domain ToolChoice to Gemini ToolConfig with native FunctionCallingConfig
func convertToolChoiceToGemini(toolChoice domain.ToolChoice, tools []*genai.Tool) *genai.ToolConfig {
	if len(tools) == 0 {
		return nil
	}

	functionCallingConfig := &genai.FunctionCallingConfig{}

	switch toolChoice.Type {
	case domain.ToolChoiceNone:
		functionCallingConfig.Mode = genai.FunctionCallingConfigModeNone
	case domain.ToolChoiceAuto:
		functionCallingConfig.Mode = genai.FunctionCallingConfigModeAuto
	case domain.ToolChoiceAny:
		functionCallingConfig.Mode = genai.FunctionCallingConfigModeAny
	case domain.ToolChoiceTool:
		// For specific tool choice, use ANY mode with allowed function names
		functionCallingConfig.Mode = genai.FunctionCallingConfigModeAny
		functionCallingConfig.AllowedFunctionNames = []string{string(toolChoice.Name)}
	default:
		// Default to auto
		functionCallingConfig.Mode = genai.FunctionCallingConfigModeAuto
	}

	return &genai.ToolConfig{
		FunctionCallingConfig: functionCallingConfig,
	}
}

// thoughtSignatureKey is the message metadata key holding a function call's
// thought signature (base64, so it survives the JSON session file unchanged).
const thoughtSignatureKey = "gemini_thought_signature"

// skipThoughtSignature is Google's documented placeholder for a function call
// that has no real signature — history resumed from an older session or
// produced by another model. Gemini 3+ rejects function calls without one.
// https://ai.google.dev/gemini-api/docs/thought-signatures
var skipThoughtSignature = []byte("skip_thought_signature_validator")

// newToolCallFromPart builds a tool call message from a functionCall part,
// keeping its thought signature so it can be echoed back on the next request.
func newToolCallFromPart(part *genai.Part) *message.ToolCallMessage {
	fc := part.FunctionCall
	args := convertGeminiArgsToToolArgs(convertToolArgsToJSON(fc.Args))
	call := message.NewToolCallMessage(message.ToolName(fc.Name), args)
	if len(part.ThoughtSignature) > 0 {
		call.SetMetadata(thoughtSignatureKey, base64.StdEncoding.EncodeToString(part.ThoughtSignature))
	}
	return call
}

// toolCallsFromParts collects every functionCall part into a single message:
// a ToolCallMessage for one call, a ToolCallBatchMessage for several.
func toolCallsFromParts(parts []*genai.Part) message.Message {
	var calls []*message.ToolCallMessage
	for _, part := range parts {
		if part.FunctionCall != nil {
			calls = append(calls, newToolCallFromPart(part))
		}
	}
	switch len(calls) {
	case 0:
		return nil
	case 1:
		return calls[0]
	default:
		return message.NewToolCallBatch(calls)
	}
}

// convertMessagesToGemini converts the transcript into Gemini contents for a
// tool-calling request. Tool calls and results become native functionCall /
// functionResponse parts; consecutive calls (a parallel batch) share one model
// turn and their results share one user turn, as the API expects.
func convertMessagesToGemini(messages []message.Message) ([]*genai.Content, *genai.Content) {
	var contents []*genai.Content
	var systemInstruction *genai.Content
	callNames := make(map[string]string) // tool call ID → function name, for responses

	for _, msg := range messages {
		switch msg.Type() {
		case message.MessageTypeUser:
			// TODO: Implement image handling with proper base64 decoding
			contents = append(contents, genai.NewContentFromText(msg.Content(), genai.RoleUser))

		case message.MessageTypeAssistant:
			contents = append(contents, genai.NewContentFromText(msg.Content(), genai.RoleModel))

		case message.MessageTypeSystem:
			// Use the last system message as system instruction
			systemInstruction = genai.NewContentFromText(msg.Content(), genai.RoleUser)

		case message.MessageTypeToolCall:
			if call, ok := msg.(*message.ToolCallMessage); ok {
				callNames[call.ID()] = string(call.ToolName())
				contents = appendFunctionPart(contents, genai.RoleModel, functionCallPart(call))
			}

		case message.MessageTypeToolResult:
			contents = appendFunctionPart(contents, genai.RoleUser, functionResponsePart(msg, callNames[msg.ID()]))

		case message.MessageTypeToolCallBatch:
			// Skip batch containers; individual calls/results are already in the transcript
			continue
		}
	}

	markUnsignedCallTurns(contents)
	return contents, systemInstruction
}

// functionCallPart converts a tool call, restoring its thought signature.
func functionCallPart(call *message.ToolCallMessage) *genai.Part {
	part := genai.NewPartFromFunctionCall(string(call.ToolName()), call.ToolArguments())
	if sig, ok := call.Metadata()[thoughtSignatureKey].(string); ok {
		if decoded, err := base64.StdEncoding.DecodeString(sig); err == nil {
			part.ThoughtSignature = decoded
		}
	}
	return part
}

// functionResponsePart converts a tool result, forwarding a tool error as such.
func functionResponsePart(msg message.Message, name string) *genai.Part {
	response := map[string]any{"output": msg.Content()}
	if result, ok := msg.(*message.ToolResultMessage); ok && result.Error != "" {
		response = map[string]any{"error": result.Error}
	}
	return genai.NewPartFromFunctionResponse(name, response)
}

// isFunctionTurn reports whether content holds functionCall/functionResponse parts.
func isFunctionTurn(content *genai.Content) bool {
	return len(content.Parts) > 0 &&
		(content.Parts[0].FunctionCall != nil || content.Parts[0].FunctionResponse != nil)
}

// appendFunctionPart adds a function part to the previous content when it has
// the same role and holds function parts too, so a batch stays one turn.
func appendFunctionPart(contents []*genai.Content, role string, part *genai.Part) []*genai.Content {
	if n := len(contents); n > 0 && contents[n-1].Role == role && isFunctionTurn(contents[n-1]) {
		contents[n-1].Parts = append(contents[n-1].Parts, part)
		return contents
	}
	return append(contents, genai.NewContentFromParts([]*genai.Part{part}, genai.Role(role)))
}

// markUnsignedCallTurns gives the placeholder signature to every model turn of
// function calls that has none. Only the first call of a turn carries one, and
// a turn without any was not produced by this model.
func markUnsignedCallTurns(contents []*genai.Content) {
	for _, content := range contents {
		if content.Role != genai.RoleModel || !isFunctionTurn(content) || content.Parts[0].FunctionCall == nil {
			continue
		}
		if !slices.ContainsFunc(content.Parts, func(p *genai.Part) bool { return len(p.ThoughtSignature) > 0 }) {
			content.Parts[0].ThoughtSignature = skipThoughtSignature
		}
	}
}
