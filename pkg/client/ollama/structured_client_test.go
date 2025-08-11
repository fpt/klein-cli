package ollama

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Test structures for structured client testing
type TestResponse struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
	Success bool   `json:"success"`
}

func TestNewOllamaStructuredClient(t *testing.T) {
	// Create a mock OllamaCore (normally would require actual Ollama connection)
	core := &OllamaCore{
		client:    nil, // Would be real client in actual usage
		model:     "test-model",
		maxTokens: 1000,
		thinking:  true,
	}

	client := NewOllamaStructuredClient[TestResponse](core)

	if client == nil {
		t.Fatal("Expected non-nil client")
	}

	if client.core != core {
		t.Error("Client should reference the provided core")
	}

	if client.generator == nil {
		t.Error("Client should have a GBNF generator")
	}
}

func TestOllamaStructuredClient_IsToolCapable(t *testing.T) {
	core := &OllamaCore{
		model: "test-model",
	}

	client := NewOllamaStructuredClient[TestResponse](core)

	// Structured clients using GBNF should not report as tool capable
	if client.IsToolCapable() {
		t.Error("GBNF-based structured client should not be tool capable")
	}
}

func TestOllamaStructuredClient_IsVisionCapable(t *testing.T) {
	core := &OllamaCore{
		model: "gpt-oss:latest", // Known model without vision
	}

	client := NewOllamaStructuredClient[TestResponse](core)

	// Should delegate to model capability detection
	expected := IsVisionCapableModel(core.model)
	actual := client.IsVisionCapable()

	if actual != expected {
		t.Errorf("IsVisionCapable() = %v, expected %v", actual, expected)
	}
}

func TestOllamaStructuredClient_GetSchema(t *testing.T) {
	core := &OllamaCore{
		model: "test-model",
	}

	client := NewOllamaStructuredClient[TestResponse](core)

	schema := client.GetSchema()

	// Should return zero value of the type
	expected := TestResponse{}
	if schema != expected {
		t.Errorf("GetSchema() = %+v, expected %+v", schema, expected)
	}
}

func TestOllamaStructuredClient_ChatWithStructure_SchemaGeneration(t *testing.T) {
	core := &OllamaCore{
		client:    nil, // Mock - would fail on actual API call
		model:     "test-model",
		maxTokens: 1000,
		thinking:  true,
	}

	_ = NewOllamaStructuredClient[TestResponse](core) // Create client to verify it can be instantiated

	// Test that schema generation works without making API call
	// We can test the JSON Schema generator directly
	generator := NewJSONSchemaGenerator()
	structType := reflect.TypeOf(TestResponse{})

	schema, err := generator.GenerateSchema(structType)
	if err != nil {
		t.Fatalf("Failed to generate JSON schema: %v", err)
	}

	// Parse the schema to verify it's valid JSON
	var schemaObj map[string]any
	err = json.Unmarshal(schema, &schemaObj)
	if err != nil {
		t.Fatalf("Generated schema is not valid JSON: %v", err)
	}

	// Check basic structure
	if schemaObj["type"] != "object" {
		t.Errorf("Expected type to be 'object', got %v", schemaObj["type"])
	}

	// Check properties exist
	properties, ok := schemaObj["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Expected properties to be an object")
	}

	expectedProperties := []string{"message", "code", "success"}
	for _, prop := range expectedProperties {
		if _, exists := properties[prop]; !exists {
			t.Errorf("Missing property: %s", prop)
		}
	}
}

func TestStructuredClientTypeAssertions(t *testing.T) {
	core := &OllamaCore{
		model: "test-model",
	}

	client := NewOllamaStructuredClient[TestResponse](core)

	// Test interface compliance
	var _ any = client

	// These type assertions would be done at runtime
	// Testing that the client implements the expected interfaces
	t.Run("StructuredLLM interface", func(t *testing.T) {
		// This is tested at compile time via the var declaration at the bottom of structured_client.go
		// Just verify we can call the method
		schema := client.GetSchema()
		_ = schema // Use the schema to avoid unused variable error
	})
}

// Integration test structure for testing with different types
type ComplexTestResponse struct {
	Data    map[string]any `json:"data"`
	Results []string       `json:"results"`
	Status  struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
	Metadata *TestResponse `json:"metadata,omitempty"`
}

func TestOllamaStructuredClient_ComplexTypes(t *testing.T) {
	core := &OllamaCore{
		model: "test-model",
	}

	client := NewOllamaStructuredClient[ComplexTestResponse](core)

	// Test that complex type schema can be generated
	schema := client.GetSchema()
	expected := ComplexTestResponse{}

	if schema.Status.Code != expected.Status.Code {
		t.Error("Schema should return zero value for complex nested types")
	}
}
