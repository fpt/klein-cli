package client

import (
	"context"
	"encoding/json"
	"fmt"

	jschema "github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

const schemaMaxRetries = 3

// InvokeWithSchema sends a prompt to the LLM and constrains the response to match
// the provided JSON Schema. The schema must be a JSON Schema object with a top-level
// "properties" key (i.e. {"type":"object","properties":{...}}).
//
// Requires a tool-capable backend. Retries up to schemaMaxRetries times when the
// model's response does not pass JSON Schema validation, feeding the validation
// error back so the model can self-correct. Returns the validated map on success.
func InvokeWithSchema(ctx context.Context, llm domain.LLM, prompt string, schema map[string]any) (map[string]any, error) {
	toolCallingLLM, ok := llm.(domain.ToolCallingLLM)
	if !ok {
		return nil, fmt.Errorf("backend %T does not support tool calling (required for --json-schema)", llm)
	}

	toolArgs, err := schemaPropsToToolArgs(schema)
	if err != nil {
		return nil, err
	}

	// Compile schema once for validation on every attempt.
	validator, err := compileSchema(schema)
	if err != nil {
		return nil, fmt.Errorf("--json-schema: failed to compile schema for validation: %w", err)
	}

	respond := &respondTool{
		name:        "respond",
		description: "Provide your answer in the exact JSON structure specified by the schema.",
		arguments:   toolArgs,
		handler: func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
			return message.NewToolResultError("not executable"), nil
		},
	}
	toolCallingLLM.SetToolManager(&respondToolManager{respondTool: respond})

	toolChoice := domain.ToolChoice{Type: domain.ToolChoiceTool, Name: "respond"}

	// Seed conversation: system instruction + user prompt.
	msgs := []message.Message{
		message.NewSystemMessage(
			"You must call the 'respond' tool with your answer. Do not respond in any other format."),
		message.NewChatMessage(message.MessageTypeUser, prompt),
	}

	var lastValidationErr error
	for attempt := 1; attempt <= schemaMaxRetries; attempt++ {
		resp, err := toolCallingLLM.ChatWithToolChoice(ctx, msgs, toolChoice, false, nil)
		if err != nil {
			return nil, fmt.Errorf("LLM call failed (attempt %d): %w", attempt, err)
		}

		toolCallMsg, ok := resp.(*message.ToolCallMessage)
		if !ok {
			return nil, fmt.Errorf("expected tool call response, got %T (attempt %d)", resp, attempt)
		}

		// Marshal arguments → map for validation and return.
		raw, err := json.Marshal(toolCallMsg.ToolArguments())
		if err != nil {
			return nil, fmt.Errorf("failed to marshal response: %w", err)
		}
		var result map[string]any
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("failed to unmarshal response: %w", err)
		}

		// Validate against the compiled schema.
		if valErr := validator.Validate(result); valErr == nil {
			return result, nil
		} else {
			lastValidationErr = valErr
		}

		if attempt == schemaMaxRetries {
			break
		}

		// Append the failed tool call + a tool-result error to the conversation
		// so the model sees exactly what was wrong and can correct itself.
		msgs = append(msgs, toolCallMsg)
		msgs = append(msgs, message.NewToolResultMessage(
			toolCallMsg.ID(),
			"",
			fmt.Sprintf("VALIDATION ERROR: Your response does not match the required JSON Schema.\n\n%s\n\nPlease call 'respond' again with a corrected response.", lastValidationErr),
		))
	}

	return nil, fmt.Errorf("response did not match schema after %d attempts: %w", schemaMaxRetries, lastValidationErr)
}

// compileSchema compiles a map[string]any JSON Schema into a validator using
// santhosh-tekuri/jsonschema/v6. The schema is registered under an in-memory URL.
func compileSchema(schema map[string]any) (*jschema.Schema, error) {
	c := jschema.NewCompiler()
	const schemaURL = "schema://inline"
	if err := c.AddResource(schemaURL, schema); err != nil {
		return nil, err
	}
	return c.Compile(schemaURL)
}

// schemaPropsToToolArgs converts a JSON Schema object's properties into
// []message.ToolArgument. Each property's full sub-schema is stored in
// ToolArgument.Properties so that backends (Anthropic, OpenAI) emit it
// verbatim and nested types (array items, object sub-properties) are preserved.
func schemaPropsToToolArgs(schema map[string]any) ([]message.ToolArgument, error) {
	propsRaw, ok := schema["properties"]
	if !ok {
		return nil, fmt.Errorf("--json-schema: schema must have a top-level \"properties\" key")
	}
	props, ok := propsRaw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("--json-schema: schema \"properties\" must be a JSON object")
	}

	requiredSet := make(map[string]bool)
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				requiredSet[s] = true
			}
		}
	}

	args := make([]message.ToolArgument, 0, len(props))
	for name, propRaw := range props {
		prop, ok := propRaw.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := prop["type"].(string)
		if typ == "" {
			typ = "string"
		}
		desc, _ := prop["description"].(string)

		args = append(args, message.ToolArgument{
			Name:        message.ToolName(name),
			Type:        typ,
			Description: message.ToolDescription(desc),
			Required:    requiredSet[name],
			Properties:  prop, // full sub-schema; backends merge this verbatim
		})
	}
	return args, nil
}

