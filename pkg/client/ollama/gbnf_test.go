package ollama

import (
	"reflect"
	"strings"
	"testing"
)

// TestStructures for testing GBNF grammar generation
type SimpleStruct struct {
	Name  string `json:"name"`
	Age   int    `json:"age"`
	Email string `json:"email"`
}

type NestedStruct struct {
	User    SimpleStruct `json:"user"`
	Address Address      `json:"address"`
	Tags    []string     `json:"tags"`
}

type Address struct {
	Street  string `json:"street"`
	City    string `json:"city"`
	ZipCode int    `json:"zip_code"`
}

type ComplexStruct struct {
	ID              int                    `json:"id"`
	Name            string                 `json:"name"`
	Active          bool                   `json:"active"`
	Score           float64                `json:"score"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
	Tags            []string               `json:"tags"`
	Nested          *SimpleStruct          `json:"nested,omitempty"`
	unexportedField string                 // Should be ignored (lowercase = unexported)
	IgnoredField    string                 `json:"-"` // Should be ignored
}

func TestGBNFGenerator_GenerateGrammar_SimpleStruct(t *testing.T) {
	generator := NewGBNFGenerator()

	grammar, err := generator.GenerateGrammar(reflect.TypeOf(SimpleStruct{}))
	if err != nil {
		t.Fatalf("Failed to generate grammar: %v", err)
	}

	// Check that the grammar contains expected elements
	expectedElements := []string{
		"root ::=",
		`"name" ":" ws string`,
		`"age" ":" ws integer`,
		`"email" ":" ws string`,
		"string ::=",
		"integer ::=",
		"ws ::=",
	}

	for _, element := range expectedElements {
		if !strings.Contains(grammar, element) {
			t.Errorf("Grammar missing expected element: %s\nGenerated grammar:\n%s", element, grammar)
		}
	}
}

func TestGBNFGenerator_GenerateGrammar_NestedStruct(t *testing.T) {
	generator := NewGBNFGenerator()

	grammar, err := generator.GenerateGrammar(reflect.TypeOf(NestedStruct{}))
	if err != nil {
		t.Fatalf("Failed to generate grammar: %v", err)
	}

	// Check that nested structures are properly handled
	expectedElements := []string{
		"root ::=",
		`"user" ":" ws root_user`,
		`"address" ":" ws root_address`,
		`"tags" ":" ws root_tags_array`,
		"root_user ::=",
		"root_address ::=",
		"root_tags_array ::=",
	}

	for _, element := range expectedElements {
		if !strings.Contains(grammar, element) {
			t.Errorf("Grammar missing expected element: %s\nGenerated grammar:\n%s", element, grammar)
		}
	}
}

func TestGBNFGenerator_GenerateGrammar_ComplexTypes(t *testing.T) {
	generator := NewGBNFGenerator()

	grammar, err := generator.GenerateGrammar(reflect.TypeOf(ComplexStruct{}))
	if err != nil {
		t.Fatalf("Failed to generate grammar: %v", err)
	}

	// Check that all basic types are supported
	expectedElements := []string{
		"integer ::=",
		"string ::=",
		"boolean ::=",
		"number ::=",
		"array ::=",
	}

	for _, element := range expectedElements {
		if !strings.Contains(grammar, element) {
			t.Errorf("Grammar missing expected element: %s\nGenerated grammar:\n%s", element, grammar)
		}
	}

	// Check that unexported and ignored fields are not included
	unexpectedElements := []string{
		"unexportedField",
		"IgnoredField",
	}

	for _, element := range unexpectedElements {
		if strings.Contains(grammar, element) {
			t.Errorf("Grammar contains unexpected element: %s\nGenerated grammar:\n%s", element, grammar)
		}
	}
}

func TestGBNFGenerator_GenerateGrammar_Arrays(t *testing.T) {
	type ArrayStruct struct {
		Strings  []string       `json:"strings"`
		Numbers  []int          `json:"numbers"`
		Booleans []bool         `json:"booleans"`
		Nested   []SimpleStruct `json:"nested"`
	}

	generator := NewGBNFGenerator()

	grammar, err := generator.GenerateGrammar(reflect.TypeOf(ArrayStruct{}))
	if err != nil {
		t.Fatalf("Failed to generate grammar: %v", err)
	}

	// Check that arrays are properly handled
	expectedElements := []string{
		`"strings" ":" ws root_strings_array`,
		`"numbers" ":" ws root_numbers_array`,
		`"booleans" ":" ws root_booleans_array`,
		`"nested" ":" ws root_nested_array`,
		"root_strings_array ::=",
		"root_numbers_array ::=",
		"root_booleans_array ::=",
		"root_nested_array ::=",
	}

	for _, element := range expectedElements {
		if !strings.Contains(grammar, element) {
			t.Errorf("Grammar missing expected element: %s\nGenerated grammar:\n%s", element, grammar)
		}
	}
}

func TestGBNFGenerator_GenerateGrammar_Pointers(t *testing.T) {
	type PointerStruct struct {
		Name   *string       `json:"name"`
		Nested *SimpleStruct `json:"nested"`
	}

	generator := NewGBNFGenerator()

	// Test with pointer to struct type
	grammar, err := generator.GenerateGrammar(reflect.TypeOf(PointerStruct{}))
	if err != nil {
		t.Fatalf("Failed to generate grammar: %v", err)
	}

	// Test with pointer to struct type passed directly
	grammar2, err := generator.GenerateGrammar(reflect.TypeOf(&PointerStruct{}))
	if err != nil {
		t.Fatalf("Failed to generate grammar for pointer type: %v", err)
	}

	// Both should generate valid grammars
	if len(grammar) == 0 || len(grammar2) == 0 {
		t.Errorf("Generated grammars should not be empty")
	}
}

func TestGBNFGenerator_GenerateGrammar_InvalidInput(t *testing.T) {
	generator := NewGBNFGenerator()

	// Test with non-struct type
	_, err := generator.GenerateGrammar(reflect.TypeOf("string"))
	if err == nil {
		t.Errorf("Expected error for non-struct type, got nil")
	}

	// Test with non-struct pointer
	_, err = generator.GenerateGrammar(reflect.TypeOf((*string)(nil)))
	if err == nil {
		t.Errorf("Expected error for non-struct pointer type, got nil")
	}
}

func TestGBNFGenerator_PrimitiveRules(t *testing.T) {
	generator := NewGBNFGenerator()

	rules := generator.getPrimitiveRules()

	// Check that all basic JSON types are covered
	expectedRules := []string{
		"ws ::=",
		"string ::=",
		"integer ::=",
		"number ::=",
		"boolean ::=",
		"null ::=",
		"value ::=",
		"object ::=",
		"array ::=",
	}

	ruleContent := strings.Join(rules, "\n")

	for _, expectedRule := range expectedRules {
		if !strings.Contains(ruleContent, expectedRule) {
			t.Errorf("Primitive rules missing: %s", expectedRule)
		}
	}
}

func TestGBNFGenerator_JSONTags(t *testing.T) {
	type TaggedStruct struct {
		DefaultName string `json:"default_name"`
		CustomName  string `json:"custom_field_name"`
		OmitEmpty   string `json:"omit_empty,omitempty"`
		WithOptions string `json:"with_options,string"`
		NoTag       string
	}

	generator := NewGBNFGenerator()

	grammar, err := generator.GenerateGrammar(reflect.TypeOf(TaggedStruct{}))
	if err != nil {
		t.Fatalf("Failed to generate grammar: %v", err)
	}

	// Check that JSON tag names are used
	expectedNames := []string{
		`"default_name"`,
		`"custom_field_name"`,
		`"omit_empty"`,
		`"with_options"`,
		`"NoTag"`, // Should use field name when no JSON tag
	}

	for _, name := range expectedNames {
		if !strings.Contains(grammar, name) {
			t.Errorf("Grammar missing expected field name: %s\nGenerated grammar:\n%s", name, grammar)
		}
	}
}
