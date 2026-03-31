package tool

import (
	"context"
	"fmt"

	"github.com/fpt/klein-cli/pkg/message"
)

// UserInputHandler is a function that prompts the user and returns their answer.
// question is the text to display; options is an optional list of choices.
// If options is non-empty the handler should present a selection UI.
// Return an error if input cannot be collected (e.g. non-interactive mode).
type UserInputHandler func(question string, options []string) (string, error)

// AskUserQuestionToolManager provides the ask_user_question tool.
// The tool pauses the ReAct loop and collects a free-form or multiple-choice
// answer from the human operator before the agent continues.
type AskUserQuestionToolManager struct {
	tools       map[message.ToolName]message.Tool
	HandleInput UserInputHandler // nil → non-interactive mode (returns error)
}

// NewAskUserQuestionToolManager creates a new manager with no interactive handler.
// Call SetHandler before use in interactive sessions.
func NewAskUserQuestionToolManager() *AskUserQuestionToolManager {
	m := &AskUserQuestionToolManager{
		tools: make(map[message.ToolName]message.Tool),
	}
	m.registerTools()
	return m
}

// SetHandler configures the interactive input handler.
func (m *AskUserQuestionToolManager) SetHandler(h UserInputHandler) {
	m.HandleInput = h
}

func (m *AskUserQuestionToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	t, ok := m.tools[name]
	return t, ok
}

func (m *AskUserQuestionToolManager) GetTools() map[message.ToolName]message.Tool {
	return m.tools
}

func (m *AskUserQuestionToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	t, ok := m.tools[name]
	if !ok {
		return message.NewToolResultError(fmt.Sprintf("tool %s not found", name)), nil
	}
	return t.Handler()(ctx, args)
}

// RegisterTool satisfies domain.ToolManager. Dynamic registration is not used
// by this manager; the tool is registered at construction time.
func (m *AskUserQuestionToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	m.tools[name] = &genericTool{
		name:        name,
		description: description,
		arguments:   arguments,
		handler:     handler,
	}
}

// genericTool is a simple Tool implementation used by RegisterTool.
type genericTool struct {
	name        message.ToolName
	description message.ToolDescription
	arguments   []message.ToolArgument
	handler     func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)
}

func (t *genericTool) RawName() message.ToolName    { return t.name }
func (t *genericTool) Name() message.ToolName        { return t.name }
func (t *genericTool) Description() message.ToolDescription { return t.description }
func (t *genericTool) Arguments() []message.ToolArgument    { return t.arguments }
func (t *genericTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return t.handler
}

func (m *AskUserQuestionToolManager) registerTools() {
	m.tools["ask_user_question"] = &askUserQuestionTool{manager: m}
}

// askUserQuestionTool implements message.Tool.
type askUserQuestionTool struct {
	manager *AskUserQuestionToolManager
}

func (t *askUserQuestionTool) RawName() message.ToolName { return "ask_user_question" }
func (t *askUserQuestionTool) Name() message.ToolName    { return "ask_user_question" }

func (t *askUserQuestionTool) Description() message.ToolDescription {
	return "Ask the human user a question and wait for their answer before continuing. " +
		"Use this when you need clarification or a decision that the user must provide. " +
		"Provide an optional list of choices to present a selection menu."
}

func (t *askUserQuestionTool) Arguments() []message.ToolArgument {
	return []message.ToolArgument{
		{
			Name:        "question",
			Description: "The question to ask the user",
			Required:    true,
			Type:        "string",
		},
		{
			Name:        "options",
			Description: "Optional list of answer choices to present as a selection menu",
			Required:    false,
			Type:        "array",
			Properties: map[string]any{
				"items": map[string]any{"type": "string"},
			},
		},
	}
}

func (t *askUserQuestionTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
		question, _ := args["question"].(string)
		if question == "" {
			return message.NewToolResultError("ask_user_question: 'question' argument is required"), nil
		}

		// Parse optional options list
		var options []string
		if raw, ok := args["options"]; ok && raw != nil {
			switch v := raw.(type) {
			case []string:
				options = v
			case []interface{}:
				for _, item := range v {
					if s, ok := item.(string); ok {
						options = append(options, s)
					}
				}
			}
		}

		if t.manager.HandleInput == nil {
			return message.NewToolResultError(
				"ask_user_question: cannot prompt user in non-interactive mode"), nil
		}

		answer, err := t.manager.HandleInput(question, options)
		if err != nil {
			return message.NewToolResultError(
				fmt.Sprintf("ask_user_question: failed to get user input: %v", err)), nil
		}

		return message.ToolResult{Text: answer}, nil
	}
}
