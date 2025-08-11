package anthropic

import (
	"testing"
)

func TestSanitizeToolNameForAnthropic(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "mcp tool with dot",
			input:    "serverA.tree_dir",
			expected: "serverA_tree_dir",
		},
		{
			name:     "mcp tool with multiple dots",
			input:    "serverA.extract.declarations",
			expected: "serverA_extract_declarations",
		},
		{
			name:     "mcp tool with double underscores",
			input:    "mcp__serverB__analyze-openapi-spec",
			expected: "mcp_serverB_analyze-openapi-spec",
		},
		{
			name:     "tool with colons",
			input:    "server:tool:action",
			expected: "server_tool_action",
		},
		{
			name:     "tool with mixed special characters",
			input:    "mcp__server.tool:action",
			expected: "mcp_server_tool_action",
		},
		{
			name:     "simple tool name without special chars",
			input:    "simple_tool",
			expected: "simple_tool",
		},
		{
			name:     "tool name with hyphens (should be preserved)",
			input:    "tool-with-hyphens",
			expected: "tool-with-hyphens",
		},
		{
			name:     "long tool name exceeding 128 chars",
			input:    "very_long_tool_name_that_exceeds_the_maximum_length_of_128_characters_and_should_be_truncated_to_fit_anthropic_api_requirements_extra_text",
			expected: "very_long_tool_name_that_exceeds_the_maximum_length_of_128_characters_and_should_be_truncated_to_fit_anthropic_api_requirements_",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "tool with consecutive double underscores",
			input:    "tool__with__double__underscores",
			expected: "tool_with_double_underscores",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeToolNameForAnthropic(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeToolNameForAnthropic(%q) = %q, want %q", tt.input, result, tt.expected)
			}

			// Verify the result matches Anthropic's pattern requirements
			if len(result) > 128 {
				t.Errorf("sanitized name %q exceeds 128 character limit (length: %d)", result, len(result))
			}

			// Check that result only contains allowed characters: alphanumeric, underscore, hyphen
			for _, r := range result {
				if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
					t.Errorf("sanitized name %q contains invalid character: %c", result, r)
				}
			}
		})
	}
}

func TestUnsanitizeToolNameFromAnthropic(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "mcp tool",
			input:    "serverA_tree_dir",
			expected: "serverA.tree_dir",
		},
		{
			name:     "mcp tool with multiple parts",
			input:    "serverA_extract_declarations",
			expected: "serverA.extract_declarations",
		},
		{
			name:     "mcp serverB tool",
			input:    "mcp_serverB_analyze-openapi-spec",
			expected: "mcp__serverB__analyze-openapi-spec",
		},
		{
			name:     "mcp tool with three parts",
			input:    "mcp_server_tool_action",
			expected: "mcp__server__tool_action",
		},
		{
			name:     "non-mcp tool should remain unchanged",
			input:    "regular_tool_name",
			expected: "regular_tool_name",
		},
		{
			name:     "tool without underscores",
			input:    "simpletool",
			expected: "simpletool",
		},
		{
			name:     "tool with hyphens (should be preserved)",
			input:    "tool-with-hyphens",
			expected: "tool-with-hyphens",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "mcp tool with only two parts",
			input:    "mcp_serverB",
			expected: "mcp_serverB", // Should remain unchanged as it doesn't match the 3-part pattern
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := unsanitizeToolNameFromAnthropic(tt.input)
			if result != tt.expected {
				t.Errorf("unsanitizeToolNameFromAnthropic(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSanitizeUnsanitizeRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		original string
	}{
		{
			name:     "mcp tool",
			original: "serverA.tree_dir",
		},
		{
			name:     "mcp complex tool",
			original: "serverA.extract_declarations",
		},
		{
			name:     "mcp serverB tool",
			original: "mcp__serverB__analyze-openapi-spec",
		},
		{
			name:     "mcp tool with multiple parts",
			original: "mcp__server__tool_action",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test the round trip: original -> sanitize -> unsanitize -> should equal original
			sanitized := sanitizeToolNameForAnthropic(tt.original)
			unsanitized := unsanitizeToolNameFromAnthropic(sanitized)

			if unsanitized != tt.original {
				t.Errorf("Round trip failed for %q:\n  Original: %q\n  Sanitized: %q\n  Unsanitized: %q",
					tt.name, tt.original, sanitized, unsanitized)
			}
		})
	}
}

func TestSanitizeToolNameEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		validate func(t *testing.T, result string)
	}{
		{
			name:  "exactly 128 characters",
			input: "a1234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567",
			validate: func(t *testing.T, result string) {
				if len(result) != 128 {
					t.Errorf("Expected exactly 128 characters, got %d", len(result))
				}
			},
		},
		{
			name:  "129 characters should be truncated",
			input: "a12345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678",
			validate: func(t *testing.T, result string) {
				if len(result) != 128 {
					t.Errorf("Expected truncation to 128 characters, got %d", len(result))
				}
			},
		},
		{
			name:  "multiple consecutive special characters",
			input: "tool...with:::many__special____chars",
			validate: func(t *testing.T, result string) {
				// Should not contain consecutive underscores
				if result != "tool_with_many_special_chars" {
					t.Errorf("Expected 'tool_with_many_special_chars', got %q", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeToolNameForAnthropic(tt.input)
			tt.validate(t, result)
		})
	}
}

// Benchmark tests to ensure the functions are performant
func BenchmarkSanitizeToolNameForAnthropic(b *testing.B) {
	testName := "serverA.extract_package_dependencies"
	for i := 0; i < b.N; i++ {
		sanitizeToolNameForAnthropic(testName)
	}
}

func BenchmarkUnsanitizeToolNameFromAnthropic(b *testing.B) {
	testName := "serverA_extract_package_dependencies"
	for i := 0; i < b.N; i++ {
		unsanitizeToolNameFromAnthropic(testName)
	}
}

func BenchmarkRoundTrip(b *testing.B) {
	originalName := "mcp__serverB__analyze-openapi-spec"
	for i := 0; i < b.N; i++ {
		sanitized := sanitizeToolNameForAnthropic(originalName)
		unsanitizeToolNameFromAnthropic(sanitized)
	}
}
