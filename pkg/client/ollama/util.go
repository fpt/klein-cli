package ollama

import (
	"encoding/base64"
	"fmt"

	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
	"github.com/fpt/klein-cli/pkg/message"
	"github.com/ollama/ollama/api"
)

// Package-level logger for Ollama utility operations
var logger = pkgLogger.NewComponentLogger("ollama-util")

// toDomainMessageFromOllama converts a final Ollama API message to our domain message.
// When includeThinking is true and thinking text is present, it attaches thinking.
// Tool calls are converted to ToolCall or ToolCallBatch messages regardless of includeThinking.
func toDomainMessageFromOllama(msg api.Message, includeThinking bool) message.Message {
	// Handle tool calls from the model first
	if len(msg.ToolCalls) == 1 {
		toolCall := msg.ToolCalls[0]
		tcMsg := message.NewToolCallMessage(
			message.ToolName(toolCall.Function.Name),
			message.ToolArgumentValues(toolCall.Function.Arguments.ToMap()),
		)
		// Store the native Ollama tool call ID for round-tripping in conversation history
		if toolCall.ID != "" {
			tcMsg.SetMetadata("ollama_tool_call_id", toolCall.ID)
		}
		return tcMsg
	} else if len(msg.ToolCalls) > 1 {
		var calls []*message.ToolCallMessage
		for _, tc := range msg.ToolCalls {
			tcMsg := message.NewToolCallMessage(
				message.ToolName(tc.Function.Name),
				message.ToolArgumentValues(tc.Function.Arguments.ToMap()),
			)
			if tc.ID != "" {
				tcMsg.SetMetadata("ollama_tool_call_id", tc.ID)
			}
			calls = append(calls, tcMsg)
		}
		return message.NewToolCallBatch(calls)
	}

	// Assistant text response (thinking optional)
	if includeThinking && len(msg.Thinking) > 0 {
		return message.NewChatMessageWithThinking(
			message.MessageTypeAssistant,
			msg.Content,
			msg.Thinking,
		)
	}
	return message.NewChatMessage(message.MessageTypeAssistant, msg.Content)
}

// toOllamaMessages converts neutral messages to Ollama format
func toOllamaMessages(messages []message.Message) []api.Message {
	var ollamaMessages []api.Message

	// Track tool calls by their internal message ID so tool results can reference them
	// (ToolResultMessage.ID() equals the ToolCallMessage.ID() used as callID in handleToolCall)
	toolCallsByID := make(map[string]*message.ToolCallMessage)

	for _, msg := range messages {
		switch msg.Type() {
		case message.MessageTypeUser, message.MessageTypeAssistant, message.MessageTypeSystem:
			ollamaMsg := api.Message{
				Role:    msg.Type().String(),
				Content: msg.Content(),
			}

			// Add images if present
			if images := msg.Images(); len(images) > 0 {
				ollamaMsg.Images = make([]api.ImageData, len(images))
				for i, imageData := range images {
					// Always assume Base64 data and decode to raw binary
					if decodedData, err := base64.StdEncoding.DecodeString(imageData); err == nil {
						ollamaMsg.Images[i] = api.ImageData(decodedData) // Use raw binary data
						logger.DebugWithIntention(pkgLogger.IntentionDebug, "Using Base64 image data", "decoded_bytes", len(decodedData))
					} else {
						logger.Warn("Failed to decode Base64 image data", "error", err)
						// Fallback to treating as raw data (though this probably won't work)
						ollamaMsg.Images[i] = api.ImageData(imageData)
					}
				}
			}

			// Add thinking if present
			if thinking := msg.Thinking(); thinking != "" {
				ollamaMsg.Thinking = thinking
			}

			ollamaMessages = append(ollamaMessages, ollamaMsg)
		case message.MessageTypeToolCall:
			// Check if this is a ToolCallMessage
			if toolCallMsg, ok := msg.(*message.ToolCallMessage); ok {
				// Track by internal ID for matching tool results
				toolCallsByID[toolCallMsg.ID()] = toolCallMsg

				// Use native tool calling format
				args := api.NewToolCallFunctionArguments()
				for k, v := range toolCallMsg.ToolArguments() {
					args.Set(k, v)
				}
				ollamaToolCall := api.ToolCall{
					Function: api.ToolCallFunction{
						Name:      string(toolCallMsg.ToolName()),
						Arguments: args,
					},
				}
				// Restore the original Ollama tool call ID for conversation reconstruction
				if id, ok := toolCallMsg.Metadata()["ollama_tool_call_id"].(string); ok {
					ollamaToolCall.ID = id
				}
				ollamaMessages = append(ollamaMessages, api.Message{
					Role:      "assistant",
					Content:   "", // Content can be empty for tool calls
					ToolCalls: []api.ToolCall{ollamaToolCall},
				})
			}
		case message.MessageTypeToolResult:
			// Check if this is a ToolResultMessage
			if toolResultMsg, ok := msg.(*message.ToolResultMessage); ok {
				content := toolResultMsg.Result
				if toolResultMsg.Error != "" {
					content = fmt.Sprintf("Error: %s", toolResultMsg.Error)
				}
				// Use the proper "tool" role for native tool calling.
				// The ToolResultMessage.ID() equals the corresponding ToolCallMessage.ID(),
				// so we can look up the original call to get the tool name and native ID.
				toolMsg := api.Message{
					Role:    "tool",
					Content: content,
				}
				if toolCallMsg, found := toolCallsByID[toolResultMsg.ID()]; found {
					toolMsg.ToolName = string(toolCallMsg.ToolName())
					if id, ok := toolCallMsg.Metadata()["ollama_tool_call_id"].(string); ok {
						toolMsg.ToolCallID = id
					}
				}
				ollamaMessages = append(ollamaMessages, toolMsg)
			}
		case message.MessageTypeToolCallBatch:
			// Skip batch container in request reconstruction; individual calls/results are present
			continue
		}
	}

	return ollamaMessages
}

// convertToOllamaTools converts domain tools to Ollama API tool format
func convertToOllamaTools(tools map[message.ToolName]message.Tool) api.Tools {
	var ollamaTools api.Tools

	for _, tool := range tools {
		// Create parameter definitions for the tool
		properties := api.NewToolPropertiesMap()
		var required []string

		for _, arg := range tool.Arguments() {
			properties.Set(string(arg.Name), api.ToolProperty{
				Type:        api.PropertyType{arg.Type},
				Description: string(arg.Description),
			})
			if arg.Required {
				required = append(required, string(arg.Name))
			}
		}

		toolFunction := api.ToolFunction{
			Name:        string(tool.Name()),
			Description: tool.Description().String(),
			Parameters: api.ToolFunctionParameters{
				Type:       "object",
				Properties: properties,
				Required:   required,
			},
		}

		ollamaTool := api.Tool{
			Type:     "function",
			Function: toolFunction,
		}

		ollamaTools = append(ollamaTools, ollamaTool)
	}

	return ollamaTools
}
