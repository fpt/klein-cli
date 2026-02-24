package anthropic

import (
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
	llmmsg "github.com/fpt/klein-cli/pkg/message"
)

// Anthropic models
// https://docs.anthropic.com/en/docs/about-claude/models/overview

// getAnthropicModel maps common model names to Anthropic model constants
func getAnthropicModel(model string) anthropic.Model {
	// Map common model names to Anthropic model constants
	switch model {
	case "claude-opus-4-20250514":
		return anthropic.ModelClaudeOpus4_20250514
	case "claude-sonnet-4-20250514":
		return anthropic.ModelClaudeSonnet4_5
	case "claude-3-7-sonnet-latest":
		return anthropic.ModelClaudeSonnet4_5
	case "claude-3-5-haiku-latest":
		return anthropic.ModelClaudeHaiku4_5
	}

	// Default to Claude Sonnet 3.7
	return anthropic.ModelClaudeSonnet4_5
}

// supportsThinking checks if the model supports thinking functionality
func supportsThinking(model string) bool {
	switch model {
	case "claude-opus-4-20250514":
		return true
	case "claude-sonnet-4-20250514":
		return true
	case "claude-3-7-sonnet-latest":
		return true
	case "claude-3-5-haiku-latest":
		return false // Haiku doesn't support thinking
	}

	// Default to true for Sonnet models (conservative approach)
	return true
}

// getModelContextWindow returns a conservative approximation of the
// model's context window (input token capacity). Anthropic models
// generally provide large windows (~200k). This value is used for
// utilization reporting only.
func getModelContextWindow(model string) int {
	switch model {
	case "claude-opus-4-20250514",
		"claude-sonnet-4-20250514",
		"claude-3-7-sonnet-latest",
		"claude-3-5-haiku-latest":
		return 200000
	default:
		// Conservative default for unknown/alias names
		return 200000
	}
}

// convertToolChoiceToAnthropic converts domain ToolChoice to Anthropic format
func convertToolChoiceToAnthropic(toolChoice domain.ToolChoice) anthropic.ToolChoiceUnionParam {
	switch toolChoice.Type {
	case domain.ToolChoiceAuto:
		return anthropic.ToolChoiceUnionParam{
			OfAuto: &anthropic.ToolChoiceAutoParam{},
		}
	case domain.ToolChoiceAny:
		return anthropic.ToolChoiceUnionParam{
			OfAny: &anthropic.ToolChoiceAnyParam{},
		}
	case domain.ToolChoiceTool:
		return anthropic.ToolChoiceUnionParam{
			OfTool: &anthropic.ToolChoiceToolParam{
				Name: string(toolChoice.Name),
			},
		}
	case domain.ToolChoiceNone:
		return anthropic.ToolChoiceUnionParam{
			OfNone: &anthropic.ToolChoiceNoneParam{},
		}
	default:
		// Default to auto
		return anthropic.ToolChoiceUnionParam{
			OfAuto: &anthropic.ToolChoiceAutoParam{},
		}
	}
}

// sanitizeToolNameForAnthropic ensures tool names comply with Anthropic's pattern '^[a-zA-Z0-9_-]{1,128}$'
func sanitizeToolNameForAnthropic(name string) string {
	// Replace dots and colons with underscores
	sanitized := strings.ReplaceAll(name, ".", "_")
	sanitized = strings.ReplaceAll(sanitized, ":", "_")

	// Replace double underscores with single underscores
	for strings.Contains(sanitized, "__") {
		sanitized = strings.ReplaceAll(sanitized, "__", "_")
	}

	// Ensure length doesn't exceed 128 characters
	if len(sanitized) > 128 {
		sanitized = sanitized[:128]
	}

	return sanitized
}

// unsanitizeToolNameFromAnthropic converts sanitized tool names back to original MCP format
func unsanitizeToolNameFromAnthropic(sanitizedName string) string {
	// Reverse the sanitization - convert back to original MCP format
	if !strings.Contains(sanitizedName, "_") {
		return sanitizedName
	}

	// Handle MCP tools with double underscores first (more specific pattern)
	// For patterns like "mcp_serverB_some-tool", convert to "mcp__serverB__some-tool"
	if strings.HasPrefix(sanitizedName, "mcp_") {
		parts := strings.SplitN(sanitizedName, "_", 3)
		if len(parts) >= 3 {
			return parts[0] + "__" + parts[1] + "__" + strings.Join(parts[2:], "_")
		}
		if len(parts) == 2 {
			// Don't convert simple mcp_serverB pattern - it should remain as is
			return sanitizedName
		}
	}

	// Handle server.tool pattern (e.g., "serverA_tree_dir" -> "serverA.tree_dir")
	parts := strings.SplitN(sanitizedName, "_", 2)
	if len(parts) == 2 {
		serverName := parts[0]
		// Check if this looks like an MCP server.tool pattern
		// Only convert if it looks like a server name (contains mcp, dev, or is reasonably long)
		// Exclude common regular tool patterns like "regular_tool"
		if (strings.Contains(serverName, "mcp") || strings.Contains(serverName, "dev") ||
			strings.HasSuffix(serverName, "mcp") || len(serverName) > 6) &&
			!strings.Contains(serverName, "regular") && !strings.Contains(serverName, "simple") {
			return parts[0] + "." + parts[1]
		}
	}

	return sanitizedName
}

// convertToolsToAnthropic converts domain tools to Anthropic format.
// The last tool in the list is marked with cache_control: ephemeral so that
// Anthropic caches the entire tool list (everything up to and including the
// marker) on the first call and serves it from cache on subsequent calls.
func convertToolsToAnthropic(tools map[message.ToolName]message.Tool) []anthropic.ToolUnionParam {
	var anthropicTools []anthropic.ToolUnionParam

	for _, tool := range tools {
		// Create properties from tool arguments using enhanced schema conversion
		properties := make(map[string]any)
		var required []string

		for _, arg := range tool.Arguments() {
			// Enhanced schema conversion that handles complex types like Ollama
			property := convertArgumentToAnthropicProperty(arg)
			properties[string(arg.Name)] = property

			if arg.Required {
				required = append(required, string(arg.Name))
			}
		}

		anthropicTool := anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        sanitizeToolNameForAnthropic(string(tool.Name())),
				Description: anthropic.String(tool.Description().String()),
				InputSchema: anthropic.ToolInputSchemaParam{
					Properties: properties,
					Required:   required,
				},
			},
		}

		anthropicTools = append(anthropicTools, anthropicTool)
	}

	// Mark the last tool with cache_control: ephemeral.
	// Anthropic caches all content up to and including the last marked item,
	// so this caches the full tool list across calls within the same session.
	if len(anthropicTools) > 0 {
		last := &anthropicTools[len(anthropicTools)-1]
		if last.OfTool != nil {
			last.OfTool.CacheControl = anthropic.NewCacheControlEphemeralParam()
		}
	}

	return anthropicTools
}

// convertArgumentToAnthropicProperty converts a ToolArgument to Anthropic property schema
// This provides enhanced schema conversion similar to Ollama's dynamic approach
func convertArgumentToAnthropicProperty(arg message.ToolArgument) map[string]any {
	property := map[string]any{
		"type":        arg.Type,
		"description": arg.Description.String(),
	}

	// Handle enhanced schema parsing from description
	// This allows tools to provide rich schema information in their descriptions
	// Example: "Array of todo items with content, status, priority, and id. maxItems:5"
	desc := arg.Description.String()

	// Use explicit properties if available
	if len(arg.Properties) > 0 {
		// Merge explicit properties with the base property
		for k, v := range arg.Properties {
			property[k] = v
		}
		return property
	}

	// Parse enhanced schema information from description
	if arg.Type == "array" {
		// For array types, try to infer item schema from context
		if strings.Contains(strings.ToLower(desc), "todo") {
			// Enhanced todo schema based on common patterns
			property["items"] = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Unique identifier for the todo item",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "The task description or content",
					},
					"status": map[string]any{
						"type":        "string",
						"enum":        []string{"pending", "in_progress", "done"},
						"description": "Current status of the todo item",
					},
					"priority": map[string]any{
						"type":        "string",
						"enum":        []string{"high", "medium", "low"},
						"description": "Priority level of the todo item",
					},
				},
				"required": []string{"id", "content", "status", "priority"},
			}

			// Parse maxItems constraint if present
			if strings.Contains(desc, "5 items or fewer") || strings.Contains(desc, "maxItems:5") {
				property["maxItems"] = 5
			}
		}
	}

	// Parse enum values from description if present
	// Example: "Status must be one of: pending, in_progress, completed"
	if strings.Contains(desc, "one of:") || strings.Contains(desc, "enum:") {
		if enumStart := strings.Index(desc, ":"); enumStart != -1 {
			enumStr := strings.TrimSpace(desc[enumStart+1:])
			if enumValues := strings.Split(enumStr, ","); len(enumValues) > 1 {
				var cleanEnums []string
				for _, val := range enumValues {
					cleanEnums = append(cleanEnums, strings.TrimSpace(val))
				}
				property["enum"] = cleanEnums
			}
		}
	}

	return property
}

// toAnthropicMessages converts neutral messages to Anthropic format
func toAnthropicMessages(messages []message.Message) []anthropic.MessageParam {
	var anthropicMessages []anthropic.MessageParam

	// Convert messages to Anthropic format

	for _, msg := range messages {
		switch msg.Type() {
		case message.MessageTypeUser:
			// Check if message has images
			if images := msg.Images(); len(images) > 0 {
				// Create content blocks with images and text
				var contentBlocks []anthropic.ContentBlockParamUnion

				// Add image blocks first (Anthropic recommendation)
				for _, imageData := range images {
					// Detect image format from Base64 data
					mediaType := "image/jpeg" // Default to JPEG format
					if strings.HasPrefix(imageData, "iVBORw0KGgo") {
						mediaType = "image/png"
					}
					imageBlock := anthropic.NewImageBlockBase64(mediaType, imageData)
					contentBlocks = append(contentBlocks, imageBlock)
				}

				// Add text block if there's content
				if msg.Content() != "" {
					textBlock := anthropic.NewTextBlock(msg.Content())
					contentBlocks = append(contentBlocks, textBlock)
				}

				anthropicMessages = append(anthropicMessages, anthropic.NewUserMessage(contentBlocks...))
			} else {
				// Text only message
				anthropicMessages = append(anthropicMessages, anthropic.NewUserMessage(anthropic.NewTextBlock(msg.Content())))
			}
		case message.MessageTypeAssistant:
			// Handle assistant messages with thinking content
			var contentBlocks []anthropic.ContentBlockParamUnion

			// Add thinking block first if present (required by Anthropic when thinking is enabled)
			if thinking := msg.Thinking(); thinking != "" {
				thinkingBlockParam := &anthropic.ThinkingBlockParam{
					Thinking: thinking,
				}
				if signature, ok := msg.Metadata()["anthropic_thinking_signature"].(string); ok && signature != "" {
					thinkingBlockParam.Signature = signature
				}
				thinkingBlock := anthropic.ContentBlockParamUnion{
					OfThinking: thinkingBlockParam,
				}
				contentBlocks = append(contentBlocks, thinkingBlock)
			}

			// Add text content if present
			if msg.Content() != "" {
				textBlock := anthropic.NewTextBlock(msg.Content())
				contentBlocks = append(contentBlocks, textBlock)
			}

			// If no content blocks, create a simple text block to avoid empty message
			if len(contentBlocks) == 0 {
				textBlock := anthropic.NewTextBlock(msg.Content())
				contentBlocks = append(contentBlocks, textBlock)
			}

			anthropicMessages = append(anthropicMessages, anthropic.NewAssistantMessage(contentBlocks...))
		case message.MessageTypeSystem:
			// System messages in Anthropic are handled differently - convert to user message
			anthropicMessages = append(anthropicMessages, anthropic.NewUserMessage(anthropic.NewTextBlock(fmt.Sprintf("System: %s", msg.Content()))))
		case message.MessageTypeToolCall:
			if toolCallMsg, ok := msg.(*llmmsg.ToolCallMessage); ok {
				// When thinking is enabled globally and we have thinking content,
				// ALL assistant messages must start with thinking blocks
				var contentBlocks []anthropic.ContentBlockParamUnion

				// Add thinking block only if we have actual thinking content
				if thinkingContent := msg.Thinking(); thinkingContent != "" {
					thinkingBlockParam := &anthropic.ThinkingBlockParam{
						Thinking: thinkingContent,
					}

					// Check if we have a preserved signature from streaming
					if signature, hasSignature := msg.Metadata()["anthropic_thinking_signature"].(string); hasSignature && signature != "" {
						thinkingBlockParam.Signature = signature
					}

					thinkingBlock := anthropic.ContentBlockParamUnion{
						OfThinking: thinkingBlockParam,
					}
					contentBlocks = append(contentBlocks, thinkingBlock)
				}

				// Add tool use block
				toolUse := anthropic.NewToolUseBlock(
					msg.ID(),
					toolCallMsg.ToolArguments(),
					string(toolCallMsg.ToolName()),
				)
				contentBlocks = append(contentBlocks, toolUse)

				anthropicMessages = append(anthropicMessages, anthropic.NewAssistantMessage(contentBlocks...))
			}
		case message.MessageTypeToolResult:
			if toolResultMsg, ok := msg.(*llmmsg.ToolResultMessage); ok {
				// Build content blocks for tool result
				var contentBlocks []anthropic.ToolResultBlockParamContentUnion

				// Add image blocks if present (Anthropic supports images in tool results)
				if images := toolResultMsg.Images(); len(images) > 0 {
					for _, imageData := range images {
						mediaType := "image/jpeg"
						if strings.HasPrefix(imageData, "iVBORw0KGgo") {
							mediaType = "image/png"
						}
						contentBlocks = append(contentBlocks, anthropic.ToolResultBlockParamContentUnion{
							OfImage: &anthropic.ImageBlockParam{
								Source: anthropic.ImageBlockParamSourceUnion{
									OfBase64: &anthropic.Base64ImageSourceParam{
										Data:      imageData,
										MediaType: anthropic.Base64ImageSourceMediaType(mediaType),
									},
								},
							},
						})
					}
				}

				// Add text block
				contentBlocks = append(contentBlocks, anthropic.ToolResultBlockParamContentUnion{
					OfText: &anthropic.TextBlockParam{Text: toolResultMsg.Content()},
				})

				toolResultParam := anthropic.ToolResultBlockParam{
					ToolUseID: toolResultMsg.ID(),
					Content:   contentBlocks,
				}
				toolResult := anthropic.ContentBlockParamUnion{
					OfToolResult: &toolResultParam,
				}
				anthropicMessages = append(anthropicMessages, anthropic.NewUserMessage(toolResult))
			}
		}
	}

	return anthropicMessages
}
