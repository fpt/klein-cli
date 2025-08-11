package openai

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/responses"
)

// convertArgumentToProperty converts a ToolArgument to OpenAI property schema
// This provides generic JSON schema inference from ToolArgument metadata
func convertArgumentToProperty(arg message.ToolArgument) map[string]interface{} {
	// Ensure we have a valid JSON Schema type
	argType := strings.TrimSpace(arg.Type)
	// Handle various invalid type cases
	if argType == "" || argType == "None" || argType == "null" || argType == "undefined" {
		argType = "string" // Default to string for invalid types
	}

	// Validate against common JSON Schema types
	validTypes := map[string]bool{
		"string":  true,
		"number":  true,
		"integer": true,
		"boolean": true,
		"array":   true,
		"object":  true,
	}

	if !validTypes[argType] {
		// If it's not a valid JSON Schema type, default to string
		argType = "string"
	}

	property := map[string]interface{}{
		"type":        argType,
		"description": arg.Description.String(),
	}

	desc := arg.Description.String()

	// Extract enum values if present in description
	if enumValues := extractEnumFromDescription(desc); len(enumValues) > 0 {
		property["enum"] = enumValues
	}

	// Handle array types - infer item schema from description patterns
	if arg.Type == "array" {
		if itemSchema := inferArrayItemSchema(desc); itemSchema != nil {
			property["items"] = itemSchema
		}

		// Extract array constraints like maxItems
		if maxItems := extractMaxItems(desc); maxItems > 0 {
			property["maxItems"] = maxItems
		}
	}

	// Use explicit properties if available
	if len(arg.Properties) > 0 {
		// Merge explicit properties with the base property
		for k, v := range arg.Properties {
			property[k] = v
		}
	}

	return property
}

// inferArrayItemSchema attempts to infer the schema for array items from description
func inferArrayItemSchema(desc string) map[string]interface{} {
	// Look for common patterns in descriptions
	lowerDesc := strings.ToLower(desc)

	// Generic object with properties mentioned in description
	if strings.Contains(lowerDesc, "object") && strings.Contains(lowerDesc, "properties") {
		return map[string]interface{}{
			"type": "object",
		}
	}

	// String array
	if strings.Contains(lowerDesc, "string") || strings.Contains(lowerDesc, "text") {
		return map[string]interface{}{
			"type": "string",
		}
	}

	// Number array
	if strings.Contains(lowerDesc, "number") || strings.Contains(lowerDesc, "numeric") {
		return map[string]interface{}{
			"type": "number",
		}
	}

	// Default to object for complex arrays
	return map[string]interface{}{
		"type": "object",
	}
}

// extractMaxItems extracts maxItems constraint from description
func extractMaxItems(desc string) int {
	// Look for patterns like "maxItems: 5" or "5 items or fewer"
	if strings.Contains(desc, "maxItems:") {
		if idx := strings.Index(desc, "maxItems:"); idx >= 0 {
			remaining := desc[idx+9:] // Skip "maxItems:"
			if len(remaining) > 0 && remaining[0] >= '0' && remaining[0] <= '9' {
				return int(remaining[0] - '0') // Simple single digit parsing
			}
		}
	}

	// Look for "X items or fewer" pattern
	if strings.Contains(desc, "items or fewer") || strings.Contains(desc, "or fewer items") {
		// Simple pattern matching for common cases
		if strings.Contains(desc, "5 items") {
			return 5
		}
	}

	return 0
}

// extractEnumFromDescription extracts enum values from description text
func extractEnumFromDescription(desc string) []string {
	if len(desc) < 10 {
		return nil
	}

	// Look for "one of:" or "enum:" pattern
	var enumStart int = -1
	for i := 0; i < len(desc)-6; i++ {
		if desc[i:i+7] == "one of:" || (i < len(desc)-5 && desc[i:i+5] == "enum:") {
			if desc[i:i+7] == "one of:" {
				enumStart = i + 7
			} else {
				enumStart = i + 5
			}
			break
		}
	}

	if enumStart == -1 {
		return nil
	}

	enumStr := desc[enumStart:]
	if len(enumStr) == 0 {
		return nil
	}

	// Skip whitespace
	for len(enumStr) > 0 && enumStr[0] == ' ' {
		enumStr = enumStr[1:]
	}

	if len(enumStr) == 0 {
		return nil
	}

	// Simple split by comma
	parts := make([]string, 0)
	current := ""
	for i := 0; i < len(enumStr); i++ {
		if enumStr[i] == ',' {
			trimmed := ""
			// Trim spaces from current
			for j := 0; j < len(current); j++ {
				if current[j] != ' ' {
					trimmed = current[j:]
					break
				}
			}
			for len(trimmed) > 0 && trimmed[len(trimmed)-1] == ' ' {
				trimmed = trimmed[:len(trimmed)-1]
			}
			if len(trimmed) > 0 {
				parts = append(parts, trimmed)
			}
			current = ""
		} else {
			current += string(enumStr[i])
		}
	}

	// Add final part
	trimmed := ""
	for j := 0; j < len(current); j++ {
		if current[j] != ' ' {
			trimmed = current[j:]
			break
		}
	}
	for len(trimmed) > 0 && trimmed[len(trimmed)-1] == ' ' {
		trimmed = trimmed[:len(trimmed)-1]
	}
	if len(trimmed) > 0 {
		parts = append(parts, trimmed)
	}

	if len(parts) > 1 {
		return parts
	}
	return nil
}

// convertOpenAIArgsToToolArgs converts OpenAI function arguments JSON to tool argument values
func convertOpenAIArgsToToolArgs(argsJSON string) message.ToolArgumentValues {
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

// convertTools converts domain tools to Responses API ToolUnionParam format
func convertTools(tools map[message.ToolName]message.Tool) []responses.ToolUnionParam {
	var responsesTools []responses.ToolUnionParam

	// Convert domain tools to Responses API format
	for _, tool := range tools {
		// Create properties from tool arguments
		properties := make(map[string]any)
		var required []string

		for _, arg := range tool.Arguments() {
			// Convert tool argument to property schema
			property := convertArgumentToProperty(arg)
			properties[string(arg.Name)] = property

			// Add to required if the argument is required
			if arg.Required {
				required = append(required, string(arg.Name))
			}
		}

		// Create proper JSON Schema object
		schema := map[string]any{
			"type":       "object",
			"properties": properties,
		}

		// Add required array if we have required fields
		if len(required) > 0 {
			schema["required"] = required
		}

		// Debug: Print tool schema
		if os.Getenv("DEBUG_TOOLS") == "1" {
			fmt.Printf("DEBUG: Tool %s schema: %+v\n", string(tool.Name()), schema)
		}

		// Create function tool using Responses API helper
		toolParam := responses.ToolParamOfFunction(
			string(tool.Name()),
			schema,
			false, // strict - set to false for now, could be configurable
		)

		// Set description if available
		if desc := tool.Description().String(); desc != "" {
			// Note: There doesn't seem to be a direct way to set description
			// in the helper function, so we might need to set it after creation
			// or use a more direct approach
		}

		responsesTools = append(responsesTools, toolParam)
	}

	return responsesTools
}

// convertToolChoice converts domain ToolChoice to Responses API format
func convertToolChoice(toolChoice domain.ToolChoice) *responses.ResponseNewParamsToolChoiceUnion {
	switch toolChoice.Type {
	case domain.ToolChoiceAuto:
		return &responses.ResponseNewParamsToolChoiceUnion{
			OfToolChoiceMode: openai.Opt(responses.ToolChoiceOptionsAuto),
		}
	case domain.ToolChoiceAny:
		return &responses.ResponseNewParamsToolChoiceUnion{
			OfToolChoiceMode: openai.Opt(responses.ToolChoiceOptionsRequired),
		}
	case domain.ToolChoiceTool:
		// For specific tool choice, we'll use required mode for now
		// TODO: Implement specific function tool choice when available in API
		return &responses.ResponseNewParamsToolChoiceUnion{
			OfToolChoiceMode: openai.Opt(responses.ToolChoiceOptionsRequired),
		}
	case domain.ToolChoiceNone:
		return &responses.ResponseNewParamsToolChoiceUnion{
			OfToolChoiceMode: openai.Opt(responses.ToolChoiceOptionsNone),
		}
	default:
		// Default to auto
		return &responses.ResponseNewParamsToolChoiceUnion{
			OfToolChoiceMode: openai.Opt(responses.ToolChoiceOptionsAuto),
		}
	}
}
