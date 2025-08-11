package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
	"github.com/ollama/ollama/api"
)

// OllamaStructuredClient implements StructuredLLM for Ollama using JSON Schema
type OllamaStructuredClient[T any] struct {
	core      *OllamaCore
	generator *JSONSchemaGenerator
	schema    T // Zero value for type inference
}

// NewOllamaStructuredClient creates a new structured client for the given type
func NewOllamaStructuredClient[T any](core *OllamaCore) *OllamaStructuredClient[T] {
	return &OllamaStructuredClient[T]{
		core:      core,
		generator: NewJSONSchemaGenerator(),
	}
}

// Chat implements the base LLM interface
func (c *OllamaStructuredClient[T]) Chat(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
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

// ChatWithStructure implements StructuredLLM interface using JSON Schema
func (c *OllamaStructuredClient[T]) ChatWithStructure(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (T, error) {
	var zero T

	// Get the type of T for schema generation
	structType := reflect.TypeOf(zero)

	// Generate JSON Schema for the target type
	schema, err := c.generator.GenerateSchema(structType)
	if err != nil {
		return zero, fmt.Errorf("failed to generate JSON schema: %w", err)
	}

	// Convert messages to Ollama format
	ollamaMessages := toOllamaMessages(messages)

	// Prepare chat request with JSON Schema format
	req := &api.ChatRequest{
		Model:    c.core.model,
		Messages: ollamaMessages,
		Format:   schema, // Use JSON Schema for structured output
		Options: map[string]any{
			"num_predict": c.core.maxTokens,
		},
		Stream: &[]bool{false}[0], // Disable streaming for structured output
	}

	// Add thinking parameter if enabled and supported (v0.11+ ThinkValue)
	if enableThinking && c.core.thinking {
		req.Think = &api.ThinkValue{Value: true}
	}

	// Send request to Ollama
	resp := &api.ChatResponse{}
	err = c.core.client.Chat(ctx, req, func(response api.ChatResponse) error {
		*resp = response
		return nil
	})

	if err != nil {
		return zero, fmt.Errorf("ollama chat failed: %w", err)
	}

	// Parse the JSON response into the target type
	var result T
	if err := json.Unmarshal([]byte(resp.Message.Content), &result); err != nil {
		return zero, fmt.Errorf("failed to unmarshal structured response: %w", err)
	}

	return result, nil
}

// IsToolCapable returns false since this client uses JSON Schema, not tools
func (c *OllamaStructuredClient[T]) IsToolCapable() bool {
	return false
}

// IsVisionCapable checks if the model supports vision capabilities
func (c *OllamaStructuredClient[T]) IsVisionCapable() bool {
	return IsVisionCapableModel(c.core.model)
}

// GetSchema returns the zero value of T for schema inspection
func (c *OllamaStructuredClient[T]) GetSchema() T {
	return c.schema
}

// ModelID returns the underlying model identifier
func (c *OllamaStructuredClient[T]) ModelID() string { return c.core.model }

// Ensure OllamaStructuredClient implements the required interfaces
var _ domain.LLM = (*OllamaStructuredClient[any])(nil)
var _ domain.StructuredLLM[any] = (*OllamaStructuredClient[any])(nil)
