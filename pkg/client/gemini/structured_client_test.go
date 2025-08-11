package gemini

import (
	"context"
	"reflect"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
	"google.golang.org/genai"
)

// Test structures
type TestResponse struct {
	Message string `json:"message" jsonschema:"required"`
	Count   int    `json:"count" jsonschema:"minimum=0"`
	Success bool   `json:"success"`
}

type TestNestedResponse struct {
	Data     TestResponse      `json:"data" jsonschema:"required"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func TestNewGeminiStructuredClient(t *testing.T) {
	// Create a mock core
	core := &GeminiCore{
		client:    nil, // Would be a real client in production
		model:     "gemini-2.5-flash-lite",
		maxTokens: 1000,
	}

	client := NewGeminiStructuredClient[TestResponse](core)

	if client == nil {
		t.Fatal("Expected non-nil structured client")
	}

	if client.core != core {
		t.Error("Client should reference the provided core")
	}
}

func TestGeminiStructuredClient_IsToolCapable(t *testing.T) {
	core := &GeminiCore{
		model: "gemini-2.5-flash-lite",
	}
	client := NewGeminiStructuredClient[TestResponse](core)

	// Gemini structured client uses native structured output, not tools
	if client.IsToolCapable() {
		t.Error("Gemini structured client should not be tool capable")
	}
}

func TestGeminiStructuredClient_IsVisionCapable(t *testing.T) {
	core := &GeminiCore{
		model: "gemini-2.5-flash-lite",
	}
	client := NewGeminiStructuredClient[TestResponse](core)

	// For now, this returns false as a safe default
	if client.IsVisionCapable() {
		t.Error("Vision capability should return false by default")
	}
}

func TestGeminiStructuredClient_generateGeminiSchema(t *testing.T) {
	core := &GeminiCore{
		model: "gemini-2.5-flash-lite",
	}
	client := NewGeminiStructuredClient[TestResponse](core)

	// Test simple struct schema generation
	schema, err := client.generateGeminiSchema(reflect.TypeOf(TestResponse{}))
	if err != nil {
		t.Fatalf("Failed to generate schema: %v", err)
	}

	if schema.Type != genai.TypeObject {
		t.Errorf("Expected object type, got %v", schema.Type)
	}

	if schema.Properties == nil {
		t.Fatal("Expected properties to be set")
	}

	// Check that expected properties exist
	expectedProps := []string{"message", "count", "success"}
	for _, prop := range expectedProps {
		if _, exists := schema.Properties[prop]; !exists {
			t.Errorf("Expected property %s to exist in schema", prop)
		}
	}

	// Test property ordering
	if len(schema.PropertyOrdering) != len(expectedProps) {
		t.Errorf("Expected %d properties in ordering, got %d", len(expectedProps), len(schema.PropertyOrdering))
	}
}

func TestGeminiStructuredClient_generateFieldSchema(t *testing.T) {
	core := &GeminiCore{
		model: "gemini-2.5-flash-lite",
	}
	client := NewGeminiStructuredClient[TestResponse](core)

	tests := []struct {
		name     string
		input    reflect.Type
		expected genai.Type
	}{
		{"string", reflect.TypeOf(""), genai.TypeString},
		{"int", reflect.TypeOf(0), genai.TypeInteger},
		{"bool", reflect.TypeOf(true), genai.TypeBoolean},
		{"float64", reflect.TypeOf(0.0), genai.TypeNumber},
		{"slice", reflect.TypeOf([]string{}), genai.TypeArray},
		{"map", reflect.TypeOf(map[string]string{}), genai.TypeObject},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema, err := client.generateFieldSchema(test.input)
			if err != nil {
				t.Fatalf("Failed to generate field schema for %s: %v", test.name, err)
			}

			if schema.Type != test.expected {
				t.Errorf("Expected type %v for %s, got %v", test.expected, test.name, schema.Type)
			}
		})
	}
}

func TestGeminiStructuredClient_generateNestedSchema(t *testing.T) {
	core := &GeminiCore{
		model: "gemini-2.5-flash-lite",
	}
	client := NewGeminiStructuredClient[TestNestedResponse](core)

	// Test nested struct schema generation
	schema, err := client.generateGeminiSchema(reflect.TypeOf(TestNestedResponse{}))
	if err != nil {
		t.Fatalf("Failed to generate nested schema: %v", err)
	}

	if schema.Type != genai.TypeObject {
		t.Errorf("Expected object type, got %v", schema.Type)
	}

	// Check that data property exists and is an object
	dataSchema, exists := schema.Properties["data"]
	if !exists {
		t.Fatal("Expected 'data' property to exist")
	}

	if dataSchema.Type != genai.TypeObject {
		t.Errorf("Expected 'data' property to be object type, got %v", dataSchema.Type)
	}

	// Check that nested properties exist
	if _, exists := dataSchema.Properties["message"]; !exists {
		t.Error("Expected nested 'message' property to exist")
	}
}

func TestGeminiStructuredClient_convertMessagesToGemini(t *testing.T) {
	core := &GeminiCore{
		model: "gemini-2.5-flash-lite",
	}
	client := NewGeminiStructuredClient[TestResponse](core)

	messages := []message.Message{
		message.NewSystemMessage("You are a helpful assistant"),
		message.NewChatMessage(message.MessageTypeUser, "Hello"),
		message.NewChatMessage(message.MessageTypeAssistant, "Hi there!"),
	}

	contents, systemInstruction := client.convertMessagesToGemini(messages)

	// Should have 2 content messages (user + assistant)
	if len(contents) != 2 {
		t.Errorf("Expected 2 content messages, got %d", len(contents))
	}

	// Should have system instruction
	if systemInstruction == nil {
		t.Error("Expected system instruction to be set")
	}

	// Check content types
	if contents[0].Role != genai.RoleUser {
		t.Errorf("Expected first content to be user role, got %v", contents[0].Role)
	}

	if contents[1].Role != genai.RoleModel {
		t.Errorf("Expected second content to be model role, got %v", contents[1].Role)
	}
}

func TestGeminiStructuredClient_isThinkingCapable(t *testing.T) {
	// Test with a reasoning model
	core := &GeminiCore{
		model: "gemini-2.5-flash-lite", // This should be a reasoning model
	}
	client := NewGeminiStructuredClient[TestResponse](core)

	if !client.isThinkingCapable() {
		t.Error("Gemini 2.5 Flash Lite should be thinking capable")
	}
}

// Test interface compliance
func TestGeminiStructuredClient_InterfaceCompliance(t *testing.T) {
	core := &GeminiCore{
		model: "gemini-2.5-flash-lite",
	}
	client := NewGeminiStructuredClient[TestResponse](core)

	// Should implement LLM interface
	var _ interface {
		Chat(context.Context, []message.Message, bool, chan<- string) (message.Message, error)
	} = client

	// Should implement StructuredLLM interface
	var _ interface {
		ChatWithStructure(context.Context, []message.Message, bool, chan<- string) (TestResponse, error)
	} = client
}
