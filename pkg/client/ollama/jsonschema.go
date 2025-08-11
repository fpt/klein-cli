package ollama

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/invopop/jsonschema"
)

// JSONSchemaGenerator generates JSON Schema from Go struct types using the invopop/jsonschema library
type JSONSchemaGenerator struct {
	reflector *jsonschema.Reflector
}

// NewJSONSchemaGenerator creates a new JSON Schema generator
func NewJSONSchemaGenerator() *JSONSchemaGenerator {
	reflector := &jsonschema.Reflector{
		// Allow additional properties by default for flexibility
		AllowAdditionalProperties: false,
		// Use json tags for property names
		RequiredFromJSONSchemaTags: true,
		// Expand all references inline for Ollama compatibility
		DoNotReference: true,
	}

	return &JSONSchemaGenerator{
		reflector: reflector,
	}
}

// GenerateSchema generates a JSON Schema from a Go struct type
func (g *JSONSchemaGenerator) GenerateSchema(structType reflect.Type) (json.RawMessage, error) {
	// Handle pointer types
	if structType.Kind() == reflect.Ptr {
		structType = structType.Elem()
	}

	if structType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("expected struct type, got %v", structType.Kind())
	}

	// Generate the schema using the reflector
	schema := g.reflector.ReflectFromType(structType)

	// Marshal to JSON
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON schema: %w", err)
	}

	return json.RawMessage(schemaBytes), nil
}

// GenerateSchemaFromValue generates a JSON Schema from a Go value (convenience method)
func (g *JSONSchemaGenerator) GenerateSchemaFromValue(value any) (json.RawMessage, error) {
	return g.GenerateSchema(reflect.TypeOf(value))
}
