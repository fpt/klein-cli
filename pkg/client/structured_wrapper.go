package client

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
	"github.com/invopop/jsonschema"
)

// ToolCallingStructuredClient implements StructuredLLM using tool calling with a "respond" tool
// This works with any ToolCallingLLM (Anthropic, OpenAI, Ollama gpt-oss, etc.)
type ToolCallingStructuredClient[T any] struct {
	client          domain.ToolCallingLLM
	generator       *jsonschema.Reflector
	originalManager domain.ToolManager // Store original tool manager
	schema          T                  // Zero value for type inference
}

// NewToolCallingStructuredClient creates a structured client using tool calling
func NewToolCallingStructuredClient[T any](client domain.ToolCallingLLM) *ToolCallingStructuredClient[T] {
	reflector := &jsonschema.Reflector{
		AllowAdditionalProperties:  false,
		RequiredFromJSONSchemaTags: true,
		DoNotReference:             true,
	}

	return &ToolCallingStructuredClient[T]{
		client:    client,
		generator: reflector,
	}
}

// Chat implements the base LLM interface
func (c *ToolCallingStructuredClient[T]) Chat(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	// Convert structured response to regular message
	result, err := c.ChatWithStructure(ctx, messages, enableThinking, thinkingChan)
	if err != nil {
		return nil, err
	}

	// Marshal the structured result to JSON
	jsonBytes, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal structured response: %w", err)
	}

	return message.NewChatMessage(message.MessageTypeAssistant, string(jsonBytes)), nil
}

// ChatWithStructure implements StructuredLLM interface using tool calling
func (c *ToolCallingStructuredClient[T]) ChatWithStructure(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (T, error) {
	var zero T

	// Get the type of T for schema generation
	structType := reflect.TypeOf(zero)

	// Generate JSON Schema for the target type
	schema := c.generator.ReflectFromType(structType)

	// Create a "respond" tool that uses the target schema as parameters
	respondTool := c.createRespondTool(schema)

	// Create a temporary tool manager with just the respond tool
	tempToolManager := &respondToolManager{
		respondTool: respondTool,
	}

	// Set the tool manager on the client (no need to get original since we don't have access)
	c.client.SetToolManager(tempToolManager)

	// Add instruction to use the respond tool
	enhancedMessages := append([]message.Message{
		message.NewSystemMessage(
			"You must respond using the 'respond' tool with the exact structure specified. " +
				"Do not provide any other response format. The tool parameters define the required response structure."),
	}, messages...)

	// Force tool choice to use the respond tool
	toolChoice := domain.ToolChoice{
		Type: domain.ToolChoiceTool,
		Name: "respond",
	}

	// Call the LLM with forced tool choice
	response, err := c.client.ChatWithToolChoice(ctx, enhancedMessages, toolChoice, enableThinking, thinkingChan)
	if err != nil {
		return zero, fmt.Errorf("tool calling failed: %w", err)
	}

	// Extract tool call result
	if toolCallMsg, ok := response.(*message.ToolCallMessage); ok {
		// Parse the tool arguments as our target type
		var result T
		argsJSON, err := json.Marshal(toolCallMsg.ToolArguments())
		if err != nil {
			return zero, fmt.Errorf("failed to marshal tool arguments: %w", err)
		}

		if err := json.Unmarshal(argsJSON, &result); err != nil {
			return zero, fmt.Errorf("failed to unmarshal tool call arguments: %w", err)
		}
		return result, nil
	}

	return zero, fmt.Errorf("expected tool call response, got %T", response)
}

// IsToolCapable returns true since this client uses tool calling
func (c *ToolCallingStructuredClient[T]) IsToolCapable() bool {
	return true
}

// IsVisionCapable checks if the underlying client supports vision
func (c *ToolCallingStructuredClient[T]) IsVisionCapable() bool {
	// Check if the underlying client implements vision capabilities
	// This is implementation-specific, so we'll need to check the concrete type
	// For now, return false as a safe default
	return false
}

// createRespondTool creates a tool that uses the target schema as parameters
func (c *ToolCallingStructuredClient[T]) createRespondTool(schema *jsonschema.Schema) *respondTool {
	// Convert jsonschema.Schema to tool arguments
	var toolArgs []message.ToolArgument

	if schema.Properties != nil {
		for pair := schema.Properties.Oldest(); pair != nil; pair = pair.Next() {
			propName := pair.Key
			propSchema := pair.Value

			arg := message.ToolArgument{
				Name:        message.ToolName(propName),
				Type:        c.schemaTypeToString(propSchema),
				Description: message.ToolDescription(propSchema.Description),
				Required:    c.isRequired(propName, schema.Required),
			}
			toolArgs = append(toolArgs, arg)
		}
	}

	return &respondTool{
		name:        "respond",
		description: "Provide a structured response with the exact format specified",
		arguments:   toolArgs,
		handler: func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
			// This should never be called since we're only using the tool for schema definition
			return message.NewToolResultError("respondTool does not execute"), nil
		},
	}
}

// schemaTypeToString converts jsonschema type to string
func (c *ToolCallingStructuredClient[T]) schemaTypeToString(schema *jsonschema.Schema) string {
	if schema.Type != "" {
		return schema.Type
	}
	// Fallback to string for unknown types
	return "string"
}

// isRequired checks if a property is in the required list
func (c *ToolCallingStructuredClient[T]) isRequired(propName string, required []string) bool {
	for _, req := range required {
		if req == propName {
			return true
		}
	}
	return false
}

// respondTool is a concrete implementation of the Tool interface
type respondTool struct {
	name        message.ToolName
	description message.ToolDescription
	arguments   []message.ToolArgument
	handler     func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)
}

func (t *respondTool) RawName() message.ToolName {
	return t.name
}

func (t *respondTool) Name() message.ToolName {
	return t.name
}

func (t *respondTool) Description() message.ToolDescription {
	return t.description
}

func (t *respondTool) Arguments() []message.ToolArgument {
	return t.arguments
}

func (t *respondTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return t.handler
}

// respondToolManager is a simple tool manager that only provides the respond tool
type respondToolManager struct {
	respondTool *respondTool
}

func (r *respondToolManager) GetTools() map[message.ToolName]message.Tool {
	return map[message.ToolName]message.Tool{
		r.respondTool.Name(): r.respondTool,
	}
}

func (r *respondToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	if name == r.respondTool.Name() {
		return r.respondTool.Handler()(ctx, args)
	}
	return message.NewToolResultError("tool not found"), nil
}

func (r *respondToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	// Not needed for this use case
}

// ModelID returns the underlying model identifier of the wrapped client
func (c *ToolCallingStructuredClient[T]) ModelID() string { return c.client.ModelID() }

// Ensure ToolCallingStructuredClient implements the required interfaces
var _ domain.LLM = (*ToolCallingStructuredClient[any])(nil)
var _ domain.StructuredLLM[any] = (*ToolCallingStructuredClient[any])(nil)
