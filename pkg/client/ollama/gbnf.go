package ollama

import (
	"fmt"
	"reflect"
	"strings"
)

// GBNFGenerator generates GBNF (Grammar BNF) rules from Go struct types
type GBNFGenerator struct{}

// NewGBNFGenerator creates a new GBNF grammar generator
func NewGBNFGenerator() *GBNFGenerator {
	return &GBNFGenerator{}
}

// GenerateGrammar generates GBNF grammar rules from a Go struct type
func (g *GBNFGenerator) GenerateGrammar(structType reflect.Type) (string, error) {
	if structType.Kind() == reflect.Ptr {
		structType = structType.Elem()
	}

	if structType.Kind() != reflect.Struct {
		return "", fmt.Errorf("expected struct type, got %v", structType.Kind())
	}

	rules := make(map[string]string)
	rootRule := g.generateStructRule(structType, "root", rules)

	// Build the complete grammar
	var grammarParts []string
	grammarParts = append(grammarParts, fmt.Sprintf("root ::= %s", rootRule))

	// Add all generated rules
	for name, rule := range rules {
		if name != "root" {
			grammarParts = append(grammarParts, fmt.Sprintf("%s ::= %s", name, rule))
		}
	}

	// Add basic primitive rules
	grammarParts = append(grammarParts, g.getPrimitiveRules()...)

	return strings.Join(grammarParts, "\n"), nil
}

// generateStructRule generates a GBNF rule for a struct
func (g *GBNFGenerator) generateStructRule(structType reflect.Type, ruleName string, rules map[string]string) string {
	var fields []string

	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

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

		fieldRule := g.generateFieldRule(field.Type, fmt.Sprintf("%s_%s", ruleName, fieldName), rules)
		fields = append(fields, fmt.Sprintf(`"%s" ":" ws %s`, fieldName, fieldRule))
	}

	if len(fields) == 0 {
		return `"{" ws "}"`
	}

	// Generate object rule with optional trailing comma
	objectFields := strings.Join(fields, ` ws "," ws `)
	return fmt.Sprintf(`"{" ws (%s)? ws "}"`, objectFields)
}

// generateFieldRule generates a GBNF rule for a struct field
func (g *GBNFGenerator) generateFieldRule(fieldType reflect.Type, ruleName string, rules map[string]string) string {
	// Handle pointers
	if fieldType.Kind() == reflect.Ptr {
		fieldType = fieldType.Elem()
	}

	switch fieldType.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "integer"
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Bool:
		return "boolean"
	case reflect.Slice, reflect.Array:
		elemRule := g.generateFieldRule(fieldType.Elem(), fmt.Sprintf("%s_elem", ruleName), rules)
		arrayRule := fmt.Sprintf(`"[" ws (%s ws ("," ws %s ws)*)? "]"`, elemRule, elemRule)
		rules[fmt.Sprintf("%s_array", ruleName)] = arrayRule
		return fmt.Sprintf("%s_array", ruleName)
	case reflect.Struct:
		structRule := g.generateStructRule(fieldType, ruleName, rules)
		rules[ruleName] = structRule
		return ruleName
	case reflect.Interface:
		// For any or interface{}, allow any JSON value
		return "value"
	default:
		// Fallback to string for unknown types
		return "string"
	}
}

// getPrimitiveRules returns the basic GBNF rules for JSON primitives
func (g *GBNFGenerator) getPrimitiveRules() []string {
	return []string{
		`ws ::= [ \t\n\r]*`,
		`string ::= "\"" ([^"\\] | "\\" (["\\/bfnrt] | "u" [0-9a-fA-F] [0-9a-fA-F] [0-9a-fA-F] [0-9a-fA-F]))* "\""`,
		`integer ::= "-"? ("0" | [1-9] [0-9]*)`,
		`number ::= "-"? ("0" | [1-9] [0-9]*) ("." [0-9]+)? ([eE] [+-]? [0-9]+)?`,
		`boolean ::= "true" | "false"`,
		`null ::= "null"`,
		`value ::= string | number | boolean | null | object | array`,
		`object ::= "{" ws (string ws ":" ws value ws ("," ws string ws ":" ws value ws)*)? "}"`,
		`array ::= "[" ws (value ws ("," ws value ws)*)? "]"`,
	}
}
