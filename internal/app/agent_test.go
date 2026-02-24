package app

import (
	"context"
	"testing"
	"time"

	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/internal/skill"
	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/pkg/agent/state"
	"github.com/fpt/klein-cli/pkg/message"
)

// mockAgentLLM is a simple mock for testing Agent
type mockAgentLLM struct{}

func (m *mockAgentLLM) Chat(ctx context.Context, messages []message.Message, enableThinking bool, thinkingChan chan<- string) (message.Message, error) {
	return message.NewChatMessage(message.MessageTypeAssistant, "mock response"), nil
}

func (m *mockAgentLLM) ModelID() string { return "mock-llm" }

// TestSkillBasedToolSelection tests that different skills get different tool managers
func TestSkillBasedToolSelection(t *testing.T) {
	workingDir := "/tmp/klein-agent-test"

	// Create tool managers
	todoManager := tool.NewTodoToolManager(workingDir)
	fsConfig := infra.DefaultFileSystemConfig(workingDir)
	fsRepo := infra.NewOSFilesystemRepository()
	filesystemManager := tool.NewFileSystemToolManager(fsRepo, fsConfig, workingDir)
	bashConfig := tool.BashConfig{WorkingDir: workingDir, MaxDuration: 120 * time.Second}
	bashManager := tool.NewBashToolManager(bashConfig)
	searchManager := tool.NewSearchToolManager(tool.SearchConfig{WorkingDir: workingDir})
	webManager := tool.NewWebToolManager()

	allManagers := tool.NewCompositeToolManager(todoManager, filesystemManager, bashManager, searchManager, webManager)

	// Create skills with different tool scopes
	skills := skill.SkillMap{
		"code": &skill.Skill{
			Name:         "code",
			AllowedTools: nil, // all tools
			Content:      "Code skill",
		},
		"readonly": &skill.Skill{
			Name:         "readonly",
			AllowedTools: []string{"read_file", "glob", "grep"},
			Content:      "Read-only skill",
		},
	}

	agent := &Agent{
		llmClient:       &mockAgentLLM{},
		allToolManagers: allManagers,
		todoToolManager: todoManager,
		sharedState:     state.NewMessageState(),
		workingDir:      workingDir,
		skills:          skills,
	}

	t.Run("code skill has all tools", func(t *testing.T) {
		codeSkill := agent.skills["code"]
		toolManager := codeSkill.FilterTools(agent.allToolManagers)
		tools := toolManager.GetTools()

		// Should have tools from all managers
		requiredTools := []string{"todo_write", "read_file", "write_file", "bash", "glob", "grep", "web_fetch", "web_fetch_block"}
		for _, toolName := range requiredTools {
			if _, exists := tools[message.ToolName(toolName)]; !exists {
				t.Errorf("code skill: expected tool '%s' but didn't find it", toolName)
			}
		}
	})

	t.Run("readonly skill has limited tools", func(t *testing.T) {
		readonlySkill := agent.skills["readonly"]
		toolManager := readonlySkill.FilterTools(agent.allToolManagers)
		tools := toolManager.GetTools()

		if len(tools) != 3 {
			t.Errorf("readonly skill: expected 3 tools, got %d", len(tools))
		}

		// Should have read_file, glob, grep
		for _, name := range []string{"read_file", "glob", "grep"} {
			if _, exists := tools[message.ToolName(name)]; !exists {
				t.Errorf("readonly skill: expected tool '%s'", name)
			}
		}

		// Should NOT have write_file, bash, etc.
		for _, name := range []string{"write_file", "bash", "todo_write"} {
			if _, exists := tools[message.ToolName(name)]; exists {
				t.Errorf("readonly skill: should not have tool '%s'", name)
			}
		}
	})
}

// TestCompositeToolManager_Agent tests the composite tool manager functionality
func TestCompositeToolManager_Agent(t *testing.T) {
	testWorkDir := "/tmp/klein-composite-test"
	todoManager := tool.NewTodoToolManager(testWorkDir)
	fsConfig := infra.DefaultFileSystemConfig(".")
	fsRepo := infra.NewOSFilesystemRepository()
	fsManager := tool.NewFileSystemToolManager(fsRepo, fsConfig, testWorkDir)
	bashConfig := tool.BashConfig{WorkingDir: testWorkDir, MaxDuration: 120 * time.Second}
	bashManager := tool.NewBashToolManager(bashConfig)

	composite := tool.NewCompositeToolManager(todoManager, fsManager, bashManager)
	tools := composite.GetTools()

	if _, exists := tools[message.ToolName("bash")]; !exists {
		t.Error("Expected to find bash tool from bash manager")
	}
	if _, exists := tools[message.ToolName("read_file")]; !exists {
		t.Error("Expected to find read_file tool from filesystem manager")
	}
	if _, exists := tools[message.ToolName("todo_write")]; !exists {
		t.Error("Expected to find todo_write tool from todo manager")
	}
	if tool, exists := composite.GetTool(message.ToolName("read_file")); !exists {
		t.Error("Expected to be able to get read_file tool")
	} else if tool == nil {
		t.Error("Got nil tool when expecting read_file")
	}
}
