package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
	"google.golang.org/genai"
)

// GeminiStructuredClient implements StructuredLLM for Gemini using native structured output
type GeminiStructuredClient[T any] struct {
	core   *GeminiCore
	schema T // Zero value for type inference
}

// NewGeminiStructuredClient creates a new structured client for the given type
func NewGeminiStructuredClient[T any](core *GeminiCore) *GeminiStructuredClient[T] {
	return &GeminiStructuredClient[T]{
		core: core,
	}
}

// Chat implements the base LLM interface
func (c *GeminiStructuredClient[T]) Chat(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
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

// ChatWithStructure implements StructuredLLM interface using Gemini's native structured output
func (c *GeminiStructuredClient[T]) ChatWithStructure(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (T, error) {
	var zero T

	// Generate Gemini schema from Go struct type
	geminiSchema, err := c.generateGeminiSchema(reflect.TypeOf(zero))
	if err != nil {
		return zero, fmt.Errorf("failed to generate Gemini schema: %w", err)
	}

	// Convert messages to Gemini format
	geminiContents, systemInstruction := c.convertMessagesToGemini(messages)

	// Create structured output configuration
	config := &genai.GenerateContentConfig{
		ResponseMIMEType: "application/json",
		ResponseSchema:   geminiSchema,
	}

	// Add system instruction if present
	if systemInstruction != nil {
		config.SystemInstruction = systemInstruction
	}

	// Add thinking parameter if supported and enabled
	if enableThinking && c.isThinkingCapable() {
		config.ThinkingConfig = &genai.ThinkingConfig{
			IncludeThoughts: true,
		}
	}

	// Generate content using Gemini's structured output
	result, err := c.core.client.Models.GenerateContent(
		ctx,
		c.core.model,
		geminiContents,
		config,
	)
	if err != nil {
		return zero, fmt.Errorf("gemini generate content failed: %w", err)
	}

	// Parse the JSON response into the target type
	var parsedResult T
	if err := json.Unmarshal([]byte(result.Text()), &parsedResult); err != nil {
		return zero, fmt.Errorf("failed to unmarshal structured response: %w", err)
	}

	return parsedResult, nil
}

// IsToolCapable returns false since this client uses native structured output, not tools
func (c *GeminiStructuredClient[T]) IsToolCapable() bool {
	return false
}

// IsVisionCapable checks if the model supports vision capabilities
func (c *GeminiStructuredClient[T]) IsVisionCapable() bool {
	// This would depend on the specific Gemini model
	// For now, return false as a safe default
	return false
}

// generateGeminiSchema converts a Go struct type to Gemini's schema format
func (c *GeminiStructuredClient[T]) generateGeminiSchema(structType reflect.Type) (*genai.Schema, error) {
	// Handle pointer types
	if structType.Kind() == reflect.Ptr {
		structType = structType.Elem()
	}

	if structType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("expected struct type, got %v", structType.Kind())
	}

	// Create the root schema as an object
	schema := &genai.Schema{
		Type:       genai.TypeObject,
		Properties: make(map[string]*genai.Schema),
	}

	var propertyOrdering []string

	// Process struct fields
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Get JSON tag name
		jsonTag := field.Tag.Get("json")
		if jsonTag == "-" {
			continue
		}

		fieldName := field.Name
		if jsonTag != "" {
			parts := strings.Split(jsonTag, ",")
			if parts[0] != "" {
				fieldName = parts[0]
			}
		}

		// Generate schema for this field
		fieldSchema, err := c.generateFieldSchema(field.Type)
		if err != nil {
			return nil, fmt.Errorf("failed to generate schema for field %s: %w", fieldName, err)
		}

		schema.Properties[fieldName] = fieldSchema
		propertyOrdering = append(propertyOrdering, fieldName)
	}

	// Set property ordering for consistent JSON output
	schema.PropertyOrdering = propertyOrdering

	return schema, nil
}

// generateFieldSchema generates a Gemini schema for a struct field
func (c *GeminiStructuredClient[T]) generateFieldSchema(fieldType reflect.Type) (*genai.Schema, error) {
	// Handle pointers
	if fieldType.Kind() == reflect.Ptr {
		fieldType = fieldType.Elem()
	}

	switch fieldType.Kind() {
	case reflect.String:
		return &genai.Schema{Type: genai.TypeString}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return &genai.Schema{Type: genai.TypeInteger}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &genai.Schema{Type: genai.TypeInteger}, nil
	case reflect.Float32, reflect.Float64:
		return &genai.Schema{Type: genai.TypeNumber}, nil
	case reflect.Bool:
		return &genai.Schema{Type: genai.TypeBoolean}, nil
	case reflect.Slice, reflect.Array:
		elemSchema, err := c.generateFieldSchema(fieldType.Elem())
		if err != nil {
			return nil, err
		}
		return &genai.Schema{
			Type:  genai.TypeArray,
			Items: elemSchema,
		}, nil
	case reflect.Struct:
		// Recursively generate schema for nested struct
		return c.generateGeminiSchema(fieldType)
	case reflect.Interface:
		// For interface{} or any, we'll use a generic object type
		return &genai.Schema{Type: genai.TypeObject}, nil
	case reflect.Map:
		// For maps, we'll use a generic object type
		return &genai.Schema{Type: genai.TypeObject}, nil
	default:
		// Fallback to string for unknown types
		return &genai.Schema{Type: genai.TypeString}, nil
	}
}

// convertMessagesToGemini converts internal messages to Gemini format
func (c *GeminiStructuredClient[T]) convertMessagesToGemini(messages []message.Message) ([]*genai.Content, *genai.Content) {
	geminiContents := make([]*genai.Content, 0)
	var systemInstruction *genai.Content

	for _, msg := range messages {
		switch msg.Type() {
		case message.MessageTypeUser:
			// Handle images if present
			if images := msg.Images(); len(images) > 0 {
				// Create parts with both text and images
				parts := []*genai.Part{}
				if content := msg.Content(); content != "" {
					parts = append(parts, &genai.Part{Text: content})
				}
				// TODO: Implement image handling with proper base64 decoding
				// For now, just add text content
				geminiContents = append(geminiContents, genai.NewContentFromParts(parts, genai.RoleUser))
			} else {
				geminiContents = append(geminiContents, genai.NewContentFromText(msg.Content(), genai.RoleUser))
			}

		case message.MessageTypeAssistant:
			// Add assistant messages as context
			geminiContents = append(geminiContents, genai.NewContentFromText(msg.Content(), genai.RoleModel))

		case message.MessageTypeSystem:
			// Use the last system message as system instruction
			systemInstruction = genai.NewContentFromText(msg.Content(), genai.RoleUser)

			// Skip tool call and result messages for structured output
		}
	}

	return geminiContents, systemInstruction
}

// isThinkingCapable checks if the current model supports thinking
func (c *GeminiStructuredClient[T]) isThinkingCapable() bool {
	capabilities := getModelCapabilities(c.core.model)
	return capabilities.IsReasoningModel
}

// ModelID returns the underlying model identifier
func (c *GeminiStructuredClient[T]) ModelID() string { return c.core.model }

// Ensure GeminiStructuredClient implements the required interfaces
var _ domain.LLM = (*GeminiStructuredClient[any])(nil)
var _ domain.StructuredLLM[any] = (*GeminiStructuredClient[any])(nil)
