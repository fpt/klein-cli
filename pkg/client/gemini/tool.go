package gemini

import (
	"encoding/json"
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
