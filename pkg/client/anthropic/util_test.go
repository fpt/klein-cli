package anthropic

import (
	"context"
	"reflect"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

func TestGetAnthropicModel(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected anthropic.Model
	}{
		{
			name:     "claude-3-7-sonnet-latest",
			input:    "claude-3-7-sonnet-latest",
			expected: anthropic.ModelClaudeSonnet4_5,
		},
		{
			name:     "claude-3-5-haiku-latest",
			input:    "claude-3-5-haiku-latest",
			expected: anthropic.ModelClaudeHaiku4_5,
		},
		{
			name:     "claude-sonnet-4-20250514",
			input:    "claude-sonnet-4-20250514",
			expected: anthropic.ModelClaudeSonnet4_5,
		},
		{
			name:     "unknown model defaults to claude-3-7-sonnet-latest",
			input:    "unknown-model",
			expected: anthropic.ModelClaudeSonnet4_5,
		},
		{
			name:     "empty string defaults to claude-3-7-sonnet-latest",
			input:    "",
			expected: anthropic.ModelClaudeSonnet4_5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getAnthropicModel(tt.input)
			if result != tt.expected {
				t.Errorf("getAnthropicModel(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSupportsThinking(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		expected bool
	}{
		{
			name:     "claude-opus-4 supports thinking",
			model:    "claude-opus-4-20250514",
			expected: true,
		},
		{
			name:     "claude-sonnet-4 supports thinking",
			model:    "claude-sonnet-4-20250514",
			expected: true,
		},
		{
			name:     "claude-3-7-sonnet supports thinking",
			model:    "claude-3-7-sonnet-latest",
			expected: true,
		},
		{
			name:     "claude-3-5-haiku does NOT support thinking",
			model:    "claude-3-5-haiku-latest",
			expected: false,
		},
		{
			name:     "unknown model defaults to thinking support",
			model:    "unknown-model",
			expected: true,
		},
		{
			name:     "empty string defaults to thinking support",
			model:    "",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := supportsThinking(tt.model)
			if result != tt.expected {
				t.Errorf("supportsThinking(%q) = %v, want %v", tt.model, result, tt.expected)
			}
		})
	}
}

func TestConvertToolChoiceToAnthropic(t *testing.T) {
	tests := []struct {
		name     string
		input    domain.ToolChoice
		validate func(t *testing.T, result anthropic.ToolChoiceUnionParam)
	}{
		{
			name: "tool choice auto",
			input: domain.ToolChoice{
				Type: domain.ToolChoiceAuto,
			},
			validate: func(t *testing.T, result anthropic.ToolChoiceUnionParam) {
				if result.OfAuto == nil {
					t.Errorf("Expected OfAuto to be non-nil")
				}
				if result.OfAny != nil || result.OfTool != nil || result.OfNone != nil {
					t.Errorf("Expected only OfAuto to be non-nil")
				}
			},
		},
		{
			name: "tool choice any",
			input: domain.ToolChoice{
				Type: domain.ToolChoiceAny,
			},
			validate: func(t *testing.T, result anthropic.ToolChoiceUnionParam) {
				if result.OfAny == nil {
					t.Errorf("Expected OfAny to be non-nil")
				}
				if result.OfAuto != nil || result.OfTool != nil || result.OfNone != nil {
					t.Errorf("Expected only OfAny to be non-nil")
				}
			},
		},
		{
			name: "tool choice specific tool",
			input: domain.ToolChoice{
				Type: domain.ToolChoiceTool,
				Name: "test_tool",
			},
			validate: func(t *testing.T, result anthropic.ToolChoiceUnionParam) {
				if result.OfTool == nil {
					t.Errorf("Expected OfTool to be non-nil")
				}
				if result.OfAuto != nil || result.OfAny != nil || result.OfNone != nil {
					t.Errorf("Expected only OfTool to be non-nil")
				}
				if result.OfTool.Name != "test_tool" {
					t.Errorf("Expected tool name to be 'test_tool', got %q", result.OfTool.Name)
				}
			},
		},
		{
			name: "tool choice none",
			input: domain.ToolChoice{
				Type: domain.ToolChoiceNone,
			},
			validate: func(t *testing.T, result anthropic.ToolChoiceUnionParam) {
				if result.OfNone == nil {
					t.Errorf("Expected OfNone to be non-nil")
				}
				if result.OfAuto != nil || result.OfAny != nil || result.OfTool != nil {
					t.Errorf("Expected only OfNone to be non-nil")
				}
			},
		},
		{
			name: "invalid tool choice defaults to auto",
			input: domain.ToolChoice{
				Type: "invalid",
			},
			validate: func(t *testing.T, result anthropic.ToolChoiceUnionParam) {
				if result.OfAuto == nil {
					t.Errorf("Expected OfAuto to be non-nil for invalid type")
				}
				if result.OfAny != nil || result.OfTool != nil || result.OfNone != nil {
					t.Errorf("Expected only OfAuto to be non-nil for invalid type")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertToolChoiceToAnthropic(tt.input)
			tt.validate(t, result)
		})
	}
}

// Note: TestSanitizeToolNameForAnthropic and TestUnsanitizeToolNameFromAnthropic
// are already defined in client_test.go, so we skip them here to avoid redeclaration

func TestConvertArgumentToAnthropicProperty(t *testing.T) {
	tests := []struct {
		name     string
		input    message.ToolArgument
		expected map[string]any
	}{
		{
			name: "basic string argument",
			input: message.ToolArgument{
				Name:        "path",
				Type:        "string",
				Description: message.ToolDescription("File path to read"),
				Required:    true,
			},
			expected: map[string]any{
				"type":        "string",
				"description": "File path to read",
			},
		},
		{
			name: "number argument",
			input: message.ToolArgument{
				Name:        "timeout",
				Type:        "number",
				Description: message.ToolDescription("Timeout in seconds"),
				Required:    false,
			},
			expected: map[string]any{
				"type":        "number",
				"description": "Timeout in seconds",
			},
		},
		{
			name: "boolean argument",
			input: message.ToolArgument{
				Name:        "recursive",
				Type:        "boolean",
				Description: message.ToolDescription("Search recursively"),
				Required:    false,
			},
			expected: map[string]any{
				"type":        "boolean",
				"description": "Search recursively",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertArgumentToAnthropicProperty(tt.input)

			if result["type"] != tt.expected["type"] {
				t.Errorf("Type mismatch: got %v, want %v", result["type"], tt.expected["type"])
			}
			if result["description"] != tt.expected["description"] {
				t.Errorf("Description mismatch: got %v, want %v", result["description"], tt.expected["description"])
			}
		})
	}
}

func TestConvertArgumentToAnthropicProperty_TodoArray(t *testing.T) {
	// Special test for todo array conversion
	input := message.ToolArgument{
		Name:        "todos",
		Type:        "array",
		Description: message.ToolDescription("Array of todo items with content, status, priority, and id. maxItems:5"),
		Required:    true,
	}

	result := convertArgumentToAnthropicProperty(input)

	// Check type and description
	if result["type"] != "array" {
		t.Errorf("Expected type 'array', got %v", result["type"])
	}
	if result["description"] != "Array of todo items with content, status, priority, and id. maxItems:5" {
		t.Errorf("Description mismatch: %v", result["description"])
	}
	if result["maxItems"] != 5 {
		t.Errorf("Expected maxItems 5, got %v", result["maxItems"])
	}

	// Check items structure
	items, ok := result["items"].(map[string]any)
	if !ok {
		t.Fatalf("Expected 'items' to be map[string]any, got %T", result["items"])
	}

	// Verify items has the expected properties
	props, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Expected 'items.properties' to be map[string]any, got %T", items["properties"])
	}

	// Check that required fields exist
	required, ok := items["required"].([]string)
	if !ok {
		t.Fatalf("Expected 'items.required' to be []string, got %T", items["required"])
	}

	expectedRequired := []string{"id", "content", "status", "priority"}
	if !reflect.DeepEqual(required, expectedRequired) {
		t.Errorf("Required fields mismatch: got %v, want %v", required, expectedRequired)
	}

	// Check that all expected properties exist
	expectedProps := []string{"id", "content", "status", "priority"}
	for _, propName := range expectedProps {
		if _, exists := props[propName]; !exists {
			t.Errorf("Expected property '%s' is missing", propName)
		}
	}

	// Check status enum
	statusProp, ok := props["status"].(map[string]any)
	if !ok {
		t.Fatalf("Expected status property to be map[string]any, got %T", props["status"])
	}

	statusEnum, ok := statusProp["enum"].([]string)
	if !ok {
		t.Fatalf("Expected status enum to be []string, got %T", statusProp["enum"])
	}

	expectedEnum := []string{"pending", "in_progress", "done"}
	if !reflect.DeepEqual(statusEnum, expectedEnum) {
		t.Errorf("Status enum mismatch: got %v, want %v", statusEnum, expectedEnum)
	}
}

// Mock tool implementation for testing
type mockTool struct {
	name        string
	description string
	args        []message.ToolArgument
}

func (m *mockTool) RawName() message.ToolName { return message.ToolName(m.name) }
func (m *mockTool) Name() message.ToolName    { return message.ToolName(m.name) }
func (m *mockTool) Description() message.ToolDescription {
	return message.ToolDescription(m.description)
}
func (m *mockTool) Arguments() []message.ToolArgument { return m.args }
func (m *mockTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
		return message.ToolResult{Text: "mock result"}, nil
	}
}

func TestConvertToolsToAnthropic(t *testing.T) {
	// Create sample tools
	tools := map[message.ToolName]message.Tool{
		"read_file": &mockTool{
			name:        "read_file",
			description: "Read contents of a file",
			args: []message.ToolArgument{
				{
					Name:        "path",
					Type:        "string",
					Description: message.ToolDescription("File path to read"),
					Required:    true,
				},
			},
		},
		"write_file": &mockTool{
			name:        "write_file",
			description: "Write content to a file",
			args: []message.ToolArgument{
				{
					Name:        "path",
					Type:        "string",
					Description: message.ToolDescription("File path to write to"),
					Required:    true,
				},
				{
					Name:        "content",
					Type:        "string",
					Description: message.ToolDescription("Content to write"),
					Required:    true,
				},
			},
		},
		"serverA.tool": &mockTool{
			name:        "serverA.tool",
			description: "A tool with dots that should be sanitized",
			args: []message.ToolArgument{
				{
					Name:        "arg1",
					Type:        "string",
					Description: message.ToolDescription("An argument"),
					Required:    true,
				},
			},
		},
	}

	result := convertToolsToAnthropic(tools)

	// Verify the correct number of tools were converted
	if len(result) != 3 {
		t.Errorf("Expected 3 tools, got %d", len(result))
	}

	// Create a map by tool name for easier testing
	toolMap := make(map[string]*anthropic.ToolParam)
	for _, tool := range result {
		if tool.OfTool != nil {
			toolMap[tool.OfTool.Name] = tool.OfTool
		}
	}

	// Test read_file tool
	if readFileTool, exists := toolMap["read_file"]; exists {
		if readFileTool.Description.Value != "Read contents of a file" {
			t.Errorf("Expected description 'Read contents of a file', got %s", readFileTool.Description.Value)
		}
		// Check properties
		properties := readFileTool.InputSchema.Properties
		if properties == nil {
			t.Errorf("Expected properties to be non-nil")
			return
		}
		propsMap, ok := properties.(map[string]interface{})
		if !ok {
			t.Errorf("Expected properties to be map[string]interface{}, got %T", properties)
			return
		}
		if len(propsMap) != 1 {
			t.Errorf("Expected 1 property, got %d", len(propsMap))
		}
		if prop, ok := propsMap["path"].(map[string]interface{}); ok {
			if prop["type"] != "string" {
				t.Errorf("Expected type 'string', got %v", prop["type"])
			}
			if prop["description"] != "File path to read" {
				t.Errorf("Expected description 'File path to read', got %v", prop["description"])
			}
		} else {
			t.Errorf("Property 'path' not found or wrong type")
		}
		// Check required fields
		required := readFileTool.InputSchema.Required
		if len(required) != 1 || required[0] != "path" {
			t.Errorf("Expected required fields ['path'], got %v", required)
		}
	} else {
		t.Errorf("Tool 'read_file' not found in converted tools")
	}

	// Test write_file tool
	if writeFileTool, exists := toolMap["write_file"]; exists {
		if writeFileTool.Description.Value != "Write content to a file" {
			t.Errorf("Expected description 'Write content to a file', got %s", writeFileTool.Description.Value)
		}
		// Check properties
		properties := writeFileTool.InputSchema.Properties
		if properties == nil {
			t.Errorf("Expected properties to be non-nil")
			return
		}
		propsMap, ok := properties.(map[string]interface{})
		if !ok {
			t.Errorf("Expected properties to be map[string]interface{}, got %T", properties)
			return
		}
		if len(propsMap) != 2 {
			t.Errorf("Expected 2 properties, got %d", len(propsMap))
		}
		// Check required fields
		required := writeFileTool.InputSchema.Required
		if len(required) != 2 {
			t.Errorf("Expected 2 required fields, got %d", len(required))
		}
		// Check required field order doesn't matter
		requiredMap := make(map[string]bool)
		for _, r := range required {
			requiredMap[r] = true
		}
		if !requiredMap["path"] || !requiredMap["content"] {
			t.Errorf("Expected required fields to include 'path' and 'content', got %v", required)
		}
	} else {
		t.Errorf("Tool 'write_file' not found in converted tools")
	}

	// Test serverA.tool (should be sanitized)
	if serverTool, exists := toolMap["serverA_tool"]; exists {
		if serverTool.Description.Value != "A tool with dots that should be sanitized" {
			t.Errorf("Expected description 'A tool with dots that should be sanitized', got %s", serverTool.Description.Value)
		}
	} else {
		t.Errorf("Tool 'serverA_tool' not found in converted tools")
	}
}

func TestToAnthropicMessages(t *testing.T) {
	tests := []struct {
		name          string
		inputMessages []message.Message
		validate      func(t *testing.T, result []anthropic.MessageParam)
	}{
		{
			name: "basic user message",
			inputMessages: []message.Message{
				message.NewChatMessage(message.MessageTypeUser, "Hello, world!"),
			},
			validate: func(t *testing.T, result []anthropic.MessageParam) {
				if len(result) != 1 {
					t.Fatalf("Expected 1 message, got %d", len(result))
				}
				if result[0].Role != anthropic.MessageParamRoleUser {
					t.Errorf("Expected role 'user', got %s", result[0].Role)
				}
				// Check content blocks
				if len(result[0].Content) != 1 {
					t.Fatalf("Expected 1 content block, got %d", len(result[0].Content))
				}
			},
		},
		{
			name: "assistant message",
			inputMessages: []message.Message{
				message.NewChatMessage(message.MessageTypeAssistant, "I can help with that."),
			},
			validate: func(t *testing.T, result []anthropic.MessageParam) {
				if len(result) != 1 {
					t.Fatalf("Expected 1 message, got %d", len(result))
				}
				if result[0].Role != anthropic.MessageParamRoleAssistant {
					t.Errorf("Expected role 'assistant', got %s", result[0].Role)
				}
			},
		},
		{
			name: "system message (converts to user message with 'System:' prefix)",
			inputMessages: []message.Message{
				message.NewChatMessage(message.MessageTypeSystem, "You are a helpful assistant."),
			},
			validate: func(t *testing.T, result []anthropic.MessageParam) {
				if len(result) != 1 {
					t.Fatalf("Expected 1 message, got %d", len(result))
				}
				if result[0].Role != anthropic.MessageParamRoleUser {
					t.Errorf("Expected role 'user', got %s", result[0].Role)
				}
			},
		},
		{
			name: "conversation sequence",
			inputMessages: []message.Message{
				message.NewChatMessage(message.MessageTypeUser, "Hello"),
				message.NewChatMessage(message.MessageTypeAssistant, "Hi there!"),
				message.NewChatMessage(message.MessageTypeUser, "How are you?"),
			},
			validate: func(t *testing.T, result []anthropic.MessageParam) {
				if len(result) != 3 {
					t.Fatalf("Expected 3 messages, got %d", len(result))
				}

				// Check roles
				expectedRoles := []anthropic.MessageParamRole{anthropic.MessageParamRoleUser, anthropic.MessageParamRoleAssistant, anthropic.MessageParamRoleUser}
				for i, role := range expectedRoles {
					if result[i].Role != role {
						t.Errorf("Message %d: Expected role '%s', got '%s'", i, role, result[i].Role)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toAnthropicMessages(tt.inputMessages)
			tt.validate(t, result)
		})
	}
}
