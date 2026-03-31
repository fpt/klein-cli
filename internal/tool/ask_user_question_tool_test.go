package tool

import (
	"context"
	"errors"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

func TestAskUserQuestion_NoHandler(t *testing.T) {
	m := NewAskUserQuestionToolManager()
	result, err := m.CallTool(context.Background(), "ask_user_question",
		message.ToolArgumentValues{"question": "What is your name?"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected error result when no handler set, got none")
	}
}

func TestAskUserQuestion_FreeFormAnswer(t *testing.T) {
	m := NewAskUserQuestionToolManager()
	m.SetHandler(func(question string, options []string) (string, error) {
		if question != "What is your name?" {
			t.Errorf("unexpected question: %q", question)
		}
		if len(options) != 0 {
			t.Errorf("expected no options, got %v", options)
		}
		return "Alice", nil
	})

	result, err := m.CallTool(context.Background(), "ask_user_question",
		message.ToolArgumentValues{"question": "What is your name?"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error result: %s", result.Error)
	}
	if result.Text != "Alice" {
		t.Errorf("expected 'Alice', got %q", result.Text)
	}
}

func TestAskUserQuestion_OptionsPassedThrough(t *testing.T) {
	m := NewAskUserQuestionToolManager()
	var gotOptions []string
	m.SetHandler(func(question string, options []string) (string, error) {
		gotOptions = options
		return options[0], nil
	})

	// Options as []interface{} (as JSON unmarshalling produces)
	result, err := m.CallTool(context.Background(), "ask_user_question",
		message.ToolArgumentValues{
			"question": "Pick one",
			"options":  []interface{}{"A", "B", "C"},
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error result: %s", result.Error)
	}
	if len(gotOptions) != 3 {
		t.Errorf("expected 3 options, got %d", len(gotOptions))
	}
	if result.Text != "A" {
		t.Errorf("expected 'A', got %q", result.Text)
	}
}

func TestAskUserQuestion_HandlerError(t *testing.T) {
	m := NewAskUserQuestionToolManager()
	m.SetHandler(func(question string, options []string) (string, error) {
		return "", errors.New("cancelled by user")
	})

	result, err := m.CallTool(context.Background(), "ask_user_question",
		message.ToolArgumentValues{"question": "Anything?"})
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected error result when handler returns error")
	}
}

func TestAskUserQuestion_MissingQuestion(t *testing.T) {
	m := NewAskUserQuestionToolManager()
	m.SetHandler(func(question string, options []string) (string, error) {
		return "ok", nil
	})

	result, err := m.CallTool(context.Background(), "ask_user_question",
		message.ToolArgumentValues{})
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected error result for missing 'question' argument")
	}
}

func TestAskUserQuestion_ToolMetadata(t *testing.T) {
	m := NewAskUserQuestionToolManager()
	tools := m.GetTools()
	tool, ok := tools["ask_user_question"]
	if !ok {
		t.Fatal("ask_user_question tool not registered")
	}
	if tool.Name() != "ask_user_question" {
		t.Errorf("unexpected name: %s", tool.Name())
	}
	if string(tool.Description()) == "" {
		t.Error("tool description should not be empty")
	}
	args := tool.Arguments()
	if len(args) < 2 {
		t.Errorf("expected at least 2 arguments, got %d", len(args))
	}
}
