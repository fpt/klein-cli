package app

import (
	"context"
	"testing"
	"time"

	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/internal/skill"
	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/agent/state"
	"github.com/fpt/klein-cli/pkg/message"
)

// mockAgentToolCallingLLM is a mock LLM that implements ToolCallingLLM interface
type mockAgentToolCallingLLM struct {
	toolManager domain.ToolManager
}

func (m *mockAgentToolCallingLLM) Chat(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	return message.NewChatMessage(message.MessageTypeAssistant, "mock response"), nil
}

func (m *mockAgentToolCallingLLM) ChatWithToolChoice(ctx context.Context, messages []message.Message, toolChoice domain.ToolChoice, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	return message.NewChatMessage(message.MessageTypeAssistant, "mock response"), nil
}

func (m *mockAgentToolCallingLLM) SetToolManager(toolManager domain.ToolManager) {
	m.toolManager = toolManager
}

func (m *mockAgentToolCallingLLM) GetToolManager() domain.ToolManager {
	return m.toolManager
}

func (m *mockAgentToolCallingLLM) ModelID() string { return "mock-llm" }

func newTestAgent(t *testing.T) *Agent {
	t.Helper()
	llmClient := &mockAgentToolCallingLLM{}
	workingDir := "/tmp/test"

	todoManager := tool.NewTodoToolManager(workingDir)
	fsConfig := infra.DefaultFileSystemConfig(workingDir)
	fsRepo := infra.NewOSFilesystemRepository()
	filesystemManager := tool.NewFileSystemToolManager(fsRepo, fsConfig, workingDir)
	bashConfig := tool.BashConfig{WorkingDir: workingDir, MaxDuration: 120 * time.Second}
	bashManager := tool.NewBashToolManager(bashConfig)
	searchManager := tool.NewSearchToolManager(tool.SearchConfig{WorkingDir: workingDir})
	webManager := tool.NewWebToolManager()

	allManagers := tool.NewCompositeToolManager(todoManager, filesystemManager, bashManager, searchManager, webManager)

	skills := skill.SkillMap{
		"code": &skill.Skill{
			Name:    "code",
			Content: "Code skill: $ARGUMENTS\nDir: {{workingDir}}",
		},
		"respond": &skill.Skill{
			Name:         "respond",
			AllowedTools: []string{"read_file", "glob", "todo_write"},
			Content:      "Respond skill",
		},
	}

	return &Agent{
		llmClient:       llmClient,
		allToolManagers: allManagers,
		todoToolManager: todoManager,
		sharedState:     state.NewMessageState(),
		workingDir:      workingDir,
		skills:          skills,
		sessionFilePath: "",
	}
}

func TestInvokeWithSkill_ValidSkill(t *testing.T) {
	agent := newTestAgent(t)
	ctx := context.Background()

	result, err := agent.Invoke(ctx, "Create a hello world function", "code")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result, got nil")
	}
	if result.Content() != "mock response" {
		t.Errorf("Expected 'mock response', got '%s'", result.Content())
	}
}

func TestInvokeWithSkill_InvalidSkill(t *testing.T) {
	agent := newTestAgent(t)
	ctx := context.Background()

	_, err := agent.Invoke(ctx, "hello", "nonexistent")
	if err == nil {
		t.Fatal("Expected error for invalid skill, got nil")
	}
	expectedError := "skill 'nonexistent' not found"
	if err.Error() != expectedError {
		t.Errorf("Expected error '%s', got '%s'", expectedError, err.Error())
	}
}

func TestInvokeWithSkill_DifferentToolSets(t *testing.T) {
	agent := newTestAgent(t)
	ctx := context.Background()

	// Test code skill (should work)
	_, err := agent.Invoke(ctx, "Test input", "code")
	if err != nil {
		t.Fatalf("Expected no error for code skill, got: %v", err)
	}

	// Test respond skill (should also work, different tools)
	_, err = agent.Invoke(ctx, "Test input", "respond")
	if err != nil {
		t.Fatalf("Expected no error for respond skill, got: %v", err)
	}
}

func TestInvokeWithSkill_CaseInsensitive(t *testing.T) {
	agent := newTestAgent(t)
	ctx := context.Background()

	// Should work with uppercase
	_, err := agent.Invoke(ctx, "hello", "CODE")
	if err != nil {
		t.Fatalf("Expected case-insensitive skill lookup, got: %v", err)
	}
}
