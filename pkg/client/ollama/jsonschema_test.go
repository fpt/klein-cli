package ollama

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Test structures for JSON Schema generation
type SimpleResponse struct {
	Message string `json:"message" jsonschema:"title=Response Message,description=The main response message"`
	Code    int    `json:"code" jsonschema:"minimum=100,maximum=599"`
	Success bool   `json:"success"`
}

type ComplexResponse struct {
	Data struct {
		ID   int    `json:"id" jsonschema:"minimum=1"`
		Name string `json:"name" jsonschema:"minLength=1,maxLength=100"`
	} `json:"data"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Tags     []string       `json:"tags" jsonschema:"uniqueItems=true"`
	Optional *string        `json:"optional,omitempty"`
}

func TestNewJSONSchemaGenerator(t *testing.T) {
	generator := NewJSONSchemaGenerator()

	if generator == nil {
		t.Fatal("Expected non-nil generator")
	}

	if generator.reflector == nil {
		t.Error("Generator should have a reflector")
	}
}

func TestJSONSchemaGenerator_GenerateSchema_SimpleStruct(t *testing.T) {
	generator := NewJSONSchemaGenerator()

	schema, err := generator.GenerateSchema(reflect.TypeOf(SimpleResponse{}))
	if err != nil {
		t.Fatalf("Failed to generate schema: %v", err)
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

func TestJSONSchemaGenerator_GenerateSchema_ComplexStruct(t *testing.T) {
	generator := NewJSONSchemaGenerator()

	schema, err := generator.GenerateSchema(reflect.TypeOf(ComplexResponse{}))
	if err != nil {
		t.Fatalf("Failed to generate schema: %v", err)
	}

	// Parse the schema to verify it's valid JSON
	var schemaObj map[string]any
	err = json.Unmarshal(schema, &schemaObj)
	if err != nil {
		t.Fatalf("Generated schema is not valid JSON: %v", err)
	}

	// Check properties exist
	properties, ok := schemaObj["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Expected properties to be an object")
	}

	expectedProperties := []string{"data", "metadata", "tags", "optional"}
	for _, prop := range expectedProperties {
		if _, exists := properties[prop]; !exists {
			t.Errorf("Missing property: %s", prop)
		}
	}

	// Check nested data structure
	dataProperty, ok := properties["data"].(map[string]any)
	if !ok {
		t.Fatalf("Expected data property to be an object")
	}

	if dataProperty["type"] != "object" {
		t.Errorf("Expected data type to be 'object', got %v", dataProperty["type"])
	}
}

func TestJSONSchemaGenerator_GenerateSchema_JSONSchemaTags(t *testing.T) {
	generator := NewJSONSchemaGenerator()

	schema, err := generator.GenerateSchema(reflect.TypeOf(SimpleResponse{}))
	if err != nil {
		t.Fatalf("Failed to generate schema: %v", err)
	}

	// Convert to string to check for jsonschema tag content
	schemaStr := string(schema)

	// Check that jsonschema tags are included
	expectedContent := []string{
		"Response Message", // title from jsonschema tag
		"minimum",          // minimum constraint
		"maximum",          // maximum constraint
	}

	for _, content := range expectedContent {
		if !strings.Contains(schemaStr, content) {
			t.Errorf("Schema missing expected content: %s\nGenerated schema:\n%s", content, schemaStr)
		}
	}
}

func TestJSONSchemaGenerator_GenerateSchemaFromValue(t *testing.T) {
	generator := NewJSONSchemaGenerator()

	value := SimpleResponse{
		Message: "test",
		Code:    200,
		Success: true,
	}

	schema, err := generator.GenerateSchemaFromValue(value)
	if err != nil {
		t.Fatalf("Failed to generate schema from value: %v", err)
	}

	// Parse the schema to verify it's valid JSON
	var schemaObj map[string]any
	err = json.Unmarshal(schema, &schemaObj)
	if err != nil {
		t.Fatalf("Generated schema is not valid JSON: %v", err)
	}

	if schemaObj["type"] != "object" {
		t.Errorf("Expected type to be 'object', got %v", schemaObj["type"])
	}
}

func TestJSONSchemaGenerator_GenerateSchema_PointerTypes(t *testing.T) {
	generator := NewJSONSchemaGenerator()

	// Test with pointer to struct
	schema, err := generator.GenerateSchema(reflect.TypeOf(&SimpleResponse{}))
	if err != nil {
		t.Fatalf("Failed to generate schema for pointer type: %v", err)
	}

	// Should generate same schema as non-pointer
	var schemaObj map[string]any
	err = json.Unmarshal(schema, &schemaObj)
	if err != nil {
		t.Fatalf("Generated schema is not valid JSON: %v", err)
	}

	if schemaObj["type"] != "object" {
		t.Errorf("Expected type to be 'object', got %v", schemaObj["type"])
	}
}

func TestJSONSchemaGenerator_GenerateSchema_InvalidInput(t *testing.T) {
	generator := NewJSONSchemaGenerator()

	// Test with non-struct type
	_, err := generator.GenerateSchema(reflect.TypeOf("string"))
	if err == nil {
		t.Error("Expected error for non-struct type, got nil")
	}

	// Test with non-struct pointer
	_, err = generator.GenerateSchema(reflect.TypeOf((*string)(nil)))
	if err == nil {
		t.Error("Expected error for non-struct pointer type, got nil")
	}
}

func TestJSONSchemaCapabilityDetection(t *testing.T) {
	tests := []struct {
		model    string
		expected bool
		desc     string
	}{
		{
			model:    "gpt-oss:latest",
			expected: false, // Has native tool calling, doesn't need JSON Schema
			desc:     "Tool-capable model should not use JSON Schema",
		},
		{
			model:    "gemma3:latest",
			expected: true, // Known model without tool calling, supports JSON Schema
			desc:     "Gemma3 model should use JSON Schema",
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			actual := IsJSONSchemaCapableModel(tt.model)
			if actual != tt.expected {
				t.Errorf("IsJSONSchemaCapableModel(%s) = %v, expected %v", tt.model, actual, tt.expected)
			}
		})
	}
}

func TestBackwardCompatibility_GBNF(t *testing.T) {
	// Test that the deprecated GBNF function still works
	models := []string{
		"gpt-oss:latest",
		"llama3.1:8b",
		"codellama:13b",
	}

	for _, model := range models {
		gbnfResult := IsGBNFCapableModel(model)
		schemaResult := IsJSONSchemaCapableModel(model)

		if gbnfResult != schemaResult {
			t.Errorf("Backward compatibility broken for model %s: GBNF=%v, Schema=%v",
				model, gbnfResult, schemaResult)
		}
	}
}
