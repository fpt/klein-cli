# KLEIN CLI Development Guide

This document provides detailed information for developers working on KLEIN CLI.

## Architecture Overview

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│ klein/  │    │   internal/     │    │  pkg/llmclient/ │
│   main.go       │───▶│   app/          │───▶│   react/        │
│                 │    │   agent.go      │    │   react.go      │
└─────────────────┘    └─────────────────┘    └─────────────────┘
                              │                        │
                              ▼                        ▼
                       ┌─────────────────┐    ┌─────────────────┐
                       │   internal/     │    │  pkg/llmclient/ │
                       │   tool/         │    │   client/       │
                       │   simple_tool_  │    │   ollama.go     │
                       │   manager.go    │    │   anthropic.go  │
                       └─────────────────┘    └─────────────────┘
```

### Key Components

- **Agent**: Main orchestrator managing skill-based ReAct execution with event handling
- **ReAct**: Core reasoning and acting implementation with unified tool calling and event emission
- **ClientWithTool**: Automatic wrapper that detects and handles native vs text-based tool calling
- **Event System**: Event-driven architecture separating business logic from presentation
- **Tool Approval System**: Interactive approval workflow for destructive file operations
- **LLM Clients**: Pluggable backend support (Ollama, Anthropic, OpenAI, Gemini) with capability-based design
- **Client Factory**: LLM client creation and configuration using dependency injection
- **Tool Manager**: Handles tool registration and execution with security controls
- **Message State**: Manages conversation history and context with repository pattern
- **Interactive REPL**: Continuous conversation interface with built-in commands and cursor-aware input handling

### Skill-Based ReAct Workflow

KLEIN CLI uses a simplified skill-based approach:

```
User Request → Skill Assignment → ReAct Execution
                      ↓                     ↓
              ┌─────────────────┐  ┌─────────────────────┐
              │   Default or    │  │  ReAct with Tool    │
              │ User-Specified  │  │  Calling Execution  │
              │      Skill      │  │                     │
              └─────────────────┘  └─────────────────────┘
```

**Current Implementation:**
- **Default Skill**: Uses 'code' skill by default for comprehensive development tasks
- **Manual Override**: Users can specify skill with `-s` flag (e.g., `-s respond`)
- **Direct Execution**: No complex skill selection logic - simple and reliable

**Available Skills:**
- `code` - Comprehensive coding assistant for all development tasks (generation + analysis + debug + test + refactor)
- `respond` - Direct knowledge-based responses without tool usage

**Architecture Benefits:**
- **Simplicity**: Straightforward skill assignment without complex selection logic
- **Predictability**: Users know exactly which skill will be used
- **Performance**: No overhead from skill selection algorithms
- **Reliability**: Fewer points of failure in the execution pipeline

### Capability-Based Design

The system uses a **capability-based architecture** with type assertion for clean capability detection:

**Interface Hierarchy:**
- `domain.LLM` - Base interface for basic chat functionality
- `domain.ToolCallingLLM` - Extends LLM with tool calling capabilities  
- `domain.StructuredLLM[T any]` - Extends LLM with type-safe structured output
 - Optional telemetry & caching interfaces:
   - `domain.TokenUsageProvider` — exposes last call token usage (if available)
   - `domain.ModelIdentifier` — exposes stable model ID for telemetry/grouping
   - `domain.SessionAware` — set/get provider-visible session ID (for provider caches)
   - `domain.ModelSideCacheConfigurator` — pass provider-native caching hints (no local cache)

**Type Assertion Pattern:**
```go
// Check for tool calling capability  
if toolClient, ok := client.(domain.ToolCallingLLM); ok {
    response, err := toolClient.ChatWithToolChoice(ctx, messages, toolChoice)
}
```

**Benefits:**
- **Type Safety**: Compile-time guarantees for capability existence
- **Clean Interfaces**: No redundant boolean methods
- **Go Idioms**: Follows standard Go patterns for capability detection
- **Self-Documenting**: Capabilities are clear through interface compliance

### Token Usage & Provider-Native Caching

KLEIN does not implement a local response cache. Instead, it provides hooks and optional interfaces so clients can:

- Report token usage (for logs/telemetry) via `TokenUsageProvider`.
- Identify the model via `ModelIdentifier` (useful for grouping/metrics).
- Accept provider-native caching hints via `ModelSideCacheConfigurator` and `SessionAware`.

Current status:
- OpenAI (Responses API): token usage wired (input/output/total tokens) where the SDK exposes `responses.ResponseUsage`. Session/caching hints are stored for later use when the SDK surfaces prompt caching controls.
- Anthropic/Gemini/Ollama: token usage and session/caching support will be added when their SDKs provide the required fields and toggles.

Reference for provider-side caching:
- OpenAI Prompt Caching: https://platform.openai.com/docs/guides/prompt-caching

## Architecture Patterns

### Event-Driven Architecture

KLEIN uses a clean event-driven architecture that separates business logic from presentation:

**Agent Layer (Event Emission):**
```go
// ReAct agent emits events without formatting concerns
r.eventEmitter.EmitEvent(events.EventTypeToolCallStart, events.ToolCallStartData{
    ToolName:  string(toolCall.ToolName()),
    Arguments: r.summarizeToolArgs(toolCall.ToolArguments()),
})
```

**App Layer (Event Handling):**
```go
// App layer handles formatting and output
emitter.AddHandler(func(event events.AgentEvent) {
    writer := s.OutWriter()
    switch event.Type {
    case events.EventTypeToolCallStart:
        if data, ok := event.Data.(events.ToolCallStartData); ok {
            fmt.Fprintf(writer, "🔧 Running tool %s %v\n", data.ToolName, data.Arguments)
        }
    case events.EventTypeToolResult:
        // Handle tool results with proper formatting
    }
})
```

**Event Types:**
- `EventTypeThinkingChunk` - Streaming thinking content during reasoning
- `EventTypeToolCallStart` - Tool execution begins (with tool name and args)
- `EventTypeToolResult` - Tool execution complete (with results or errors)
- `EventTypeResponse` - Final agent response when conversation completes
- `EventTypeError` - Error conditions during execution

### Dependency Injection Pattern

The system uses constructor injection for clean testability and modularity:

**Agent Construction:**
```go
// Clean dependency injection in main.go
func createAgent(cfg *config.Config, writer io.Writer) (*app.Agent, error) {
    // Create tool managers
    universalManager := tool.NewCompositeToolManager(
        tool.NewTodoToolManager(todoRepo),
        tool.NewFileSystemToolManager(fsRepo, cfg.FileSystem),
        tool.NewBashToolManager(),
    )

    // Create agent with injected dependencies
    return app.NewAgentWithOptions(
        writer,           // Output destination
        cfg,              // Configuration
        universalManager, // Universal tools
        webToolManager,   // Web tools
        mcpManagers,      // MCP tool managers
    ), nil
}
```

**ReAct Agent Construction:**
```go
// ReAct agent with injected dependencies
func NewReAct(
    llmClient domain.LLM,
    toolManager domain.ToolManager, 
    state domain.State,
    situation domain.Situation,
    maxIterations int,
    eventEmitter events.EventEmitter, // Event system injection
) *ReAct {
    return &ReAct{
        llmClient:     llmClient,
        toolManager:   toolManager,
        state:        state,
        situation:    situation,
        maxIterations: maxIterations,
        eventEmitter:  eventEmitter,
    }
}
```

### Tool Approval System

Interactive approval workflow for potentially destructive operations:

**Approval Logic in ReAct:**
```go
// Check if tool requires approval
if toolCall, ok := resp.(*message.ToolCallMessage); ok {
    toolName := string(toolCall.ToolName())
    
    // Only require approval for potentially destructive file operations
    requiresApproval := toolName == "Write" || toolName == "Edit" || toolName == "MultiEdit"
    
    if requiresApproval && !autoApprove {
        r.pendingToolCall = toolCall
        r.status = domain.AgentStatusWaitingApproval
        return nil, react.ErrWaitingForApproval
    }
}
```

**Approval Workflow in App Layer:**
```go
func (s *Agent) handleApprovalWorkflow(ctx context.Context, reactClient domain.ReAct) (message.Message, error) {
    // Check for auto-approve flag
    if s.alwaysApprove {
        return reactClient.Resume(ctx)
    }
    
    // Interactive approval with Yes/Always/No options
    result, err := s.promptForApproval(pendingAction)
    switch result {
    case "Always":
        s.alwaysApprove = true // Set session flag
        fallthrough
    case "Yes":
        return reactClient.Resume(ctx)
    case "No":
        reactClient.CancelPendingToolCall()
        return nil, nil
    }
}
```

**Approval Modes:**
- **Interactive**: Prompts user with Yes/Always/No options using promptui
- **Non-Interactive**: Auto-approves with logged notifications when running in pipes/scripts

### Domain-Driven Design (DDD) and Dependency Injection (DI) Pattern

KLEIN CLI follows Domain-Driven Design principles with clean dependency injection for testability and maintainability:

**DDD Layer Separation:**
- **Domain Layer** (`pkg/agent/domain/`): Core interfaces and business logic (no external dependencies)
- **Infrastructure Layer** (`internal/infra/`): Concrete implementations of repositories and external services
- **Application Layer** (`internal/app/`): Business workflows and use case orchestration
- **Repository Layer** (`internal/repository/`): Data access interface contracts

**Repository Pattern with DDD:**
The system uses the repository pattern to abstract data persistence and filesystem operations:

**Domain Repository Interfaces:**
```go
// Filesystem operations contract
type FilesystemRepository interface {
    // File operations
    ReadFile(ctx context.Context, path string) ([]byte, error)
    WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error
    Stat(ctx context.Context, path string) (fs.FileInfo, error)
    
    // Directory operations
    ReadDir(ctx context.Context, path string) ([]fs.DirEntry, error)
    
    // File existence and metadata
    Exists(ctx context.Context, path string) (bool, error)
    IsDir(ctx context.Context, path string) (bool, error)
    IsRegular(ctx context.Context, path string) (bool, error)
}

// Message persistence
type MessageHistoryRepository interface {
    Load() ([]message.Message, error)
    Save(messages []message.Message) error
}

// Settings persistence  
type SettingsRepository interface {
    Load() (*Settings, error)
    Save(settings *Settings) error
}
```

**Infrastructure Implementations:**
```go
// Concrete filesystem repository implementation
type OSFilesystemRepository struct{}

func NewOSFilesystemRepository() repository.FilesystemRepository {
    return &OSFilesystemRepository{}
}

func (r *OSFilesystemRepository) ReadFile(ctx context.Context, path string) ([]byte, error) {
    return os.ReadFile(path)
}

func (r *OSFilesystemRepository) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
    return os.WriteFile(path, data, perm)
}
// ... other methods
```

**Dependency Injection in Application Layer:**

**FileSystemToolManager with DI:**
```go
type FileSystemToolManager struct {
    fsRepo repository.FilesystemRepository // Injected dependency
    allowedDirectories []string
    blacklistedFiles   []string
    workingDir string
    // ... other fields
}

// Constructor injection
func NewFileSystemToolManager(
    fsRepo repository.FilesystemRepository, 
    config repository.FileSystemConfig, 
    workingDir string,
) *FileSystemToolManager {
    return &FileSystemToolManager{
        fsRepo:             fsRepo, // Injected repository
        allowedDirectories: config.AllowedDirectories,
        blacklistedFiles:   config.BlacklistedFiles,
        workingDir:         workingDir,
        // ... other initialization
    }
}

// Use injected repository instead of direct os.* calls
func (f *FileSystemToolManager) readFile(ctx context.Context, filePath string) ([]byte, error) {
    return f.fsRepo.ReadFile(ctx, filePath) // Uses injected repository
}
```

**PromptBuilder with DI:**
```go
type PromptBuilder struct {
    buf        []rune
    times      []time.Time
    workingDir string
    fsRepo     repository.FilesystemRepository // Injected dependency
}

// Constructor injection
func NewPromptBuilder(fsRepo repository.FilesystemRepository, workingDir string) *PromptBuilder {
    if workingDir == "" {
        workingDir, _ = os.Getwd()
    }
    return &PromptBuilder{
        buf:        make([]rune, 0, 256),
        times:      make([]time.Time, 0, 256),
        workingDir: workingDir,
        fsRepo:     fsRepo, // Injected repository
    }
}

// File existence checks using injected repository
func (p *PromptBuilder) highlightAtmarkFiles(input string) string {
    re := regexp.MustCompile(`@([\w\-\./]+)`)
    return re.ReplaceAllStringFunc(input, func(match string) string {
        filename := match[1:]
        fullPath := filepath.Join(p.workingDir, filename)
        if _, err := p.fsRepo.Stat(context.Background(), fullPath); err == nil {
            // File exists - color it cyan
            return fmt.Sprintf("\033[36m@%s\033[0m", filename)
        }
        return match
    })
}
```

**DI Benefits:**
- **Testability**: Easy to mock repositories for unit testing
- **Modularity**: Clear separation between business logic and infrastructure
- **Flexibility**: Can swap implementations (e.g., memory vs filesystem storage)
- **Context Awareness**: All operations support cancellation via context

**Testing with DI:**
```go
// Easy mocking for tests
type MockFilesystemRepository struct {
    files map[string][]byte
}

func (m *MockFilesystemRepository) ReadFile(ctx context.Context, path string) ([]byte, error) {
    if content, exists := m.files[path]; exists {
        return content, nil
    }
    return nil, os.ErrNotExist
}

// Test with injected mock
func TestPromptBuilder_FileHighlighting(t *testing.T) {
    mockRepo := &MockFilesystemRepository{
        files: map[string][]byte{
            "/test/existing.go": []byte("package main"),
        },
    }
    
    pb := NewPromptBuilder(mockRepo, "/test")
    // Test file highlighting logic...
}
```

**MessageState with Repository:**
```go
// MessageState uses repository for persistence
func NewMessageStateWithRepository(repo repository.MessageHistoryRepository) *MessageState {
    return &MessageState{
        Messages:    make([]message.Message, 0),
        Metadata:    make(map[string]interface{}),
        historyRepo: repo, // Injected repository
    }
}

// Clean interface - no file paths needed
func (s *MessageState) SaveToFile() error {
    if s.historyRepo == nil {
        return nil // In-memory only
    }
    return s.historyRepo.Save(s.Messages)
}
```

## Tool Calling Support

KLEIN CLI uses native tool calling for all supported models:

- **API-level tool definitions** with structured tool_use/tool_result blocks
- **Efficient and reliable** tool execution
- **Automatic capability detection** via dynamic testing for unknown models
- **Unified interface** across all LLM backends (Ollama, Anthropic, OpenAI, Gemini)

The system automatically tests unknown Ollama models for tool calling capability and warns users if a model lacks this support, ensuring you always know what functionality is available.

## Development

### Output and Logging (Writer + Intentions)

- Agent Writer: The application layer accepts an `io.Writer` and streams thinking output to it. This allows redirecting output to REPL, tests, or a future gRPC stream.
- Unified Console Writer: Configure the logger with `SetGlobalLoggerWithConsoleWriter` to write console logs to the same `io.Writer` used by Agent.
- Intentions: Only Info/Debug logs attach an `Intention` (e.g., `tool`, `thinking`, `status`, `statistics`, `success`, `debug`). Console icons are derived from intention; file logs store `intention=...` as plain structured metadata (no icons).
- Warn/Error: Do not use intention; rely on level only. No icon mapping.
- Model-Facing Output: Tool result text sent back to LLMs avoids emojis and uses PASS/FAIL/ERROR phrasing for clarity.

Relevant code:
- `internal/app/agent.go`: `Agent` accepts an `io.Writer`; thinking channels created against that writer.
- `pkg/message/thinking.go`: `CreateThinkingChannel(w io.Writer)` streams thinking to the provided writer.
- `pkg/logger/logger.go`: `NewLoggerWithConsoleWriter`, `SetGlobalLoggerWithConsoleWriter`, and `Info/DebugWithIntention` APIs.
- `pkg/logger/plain_handler.go`: Injects console icon from intention; filters it out for file logs.

### Running Tests

```bash
# Run all tests
go test ./...

# Run with coverage
go test -cover ./...

# Run specific package tests
go test -v ./pkg/llmclient/react/

# Run tests with make
make test
```

### Code Quality

```bash
# Format code
go fmt ./...

# Run linter
go vet ./...

# Clean up dependencies
go mod tidy
```

### Project Structure

```
klein-cli/
├── klein/             # Main application
│   └── main.go                # CLI entry point
├── doc/                    # Documentation
│   └── DEVELOPMENT.md         # This file
├── internal/               # Internal application code
│   ├── app/                   # Agent implementation
│   ├── config/                # Configuration management
│   ├── infra/                 # Infrastructure components
│   ├── mcp/                   # MCP integration
│   ├── skill/                 # Built-in skills
│   └── tool/                  # Tool management
├── pkg/agent/              # Reusable agent components
│   ├── domain/                # Domain interfaces
│   ├── mcp/                   # MCP client implementation
│   ├── react/                 # ReAct pattern implementation
│   └── state/                 # State management
├── pkg/client/             # Client factory and implementations
│   ├── anthropic/             # Anthropic client implementation
│   ├── gemini/                # Google Gemini client implementation  
│   ├── ollama/                # Ollama client implementation
│   └── openai/                # OpenAI client implementation
└── pkg/message/            # Message handling and types
```

### Available Models

**Ollama Models:**
- `gpt-oss:latest` - **Recommended** - Best balanced model with native tool calling

**Anthropic Models:**
- `claude-3-7-sonnet-latest` - Default Claude model with native tool calling
- `claude-3-5-haiku-latest` - Faster Claude model with native tool calling
- `claude-sonnet-4-20250514` - Latest Claude Sonnet 4 with native tool calling

**OpenAI Models:**
- `gpt-4o` - Latest GPT-4 Omni (vision, tool calling, structured output)
- `gpt-4o-mini` - Smaller, faster GPT-4 Omni
- `gpt-3.5-turbo` - Fast and cost-effective for most tasks

**Google Gemini Models:**
- `gemini-2.5-flash-lite` - **Recommended** - Latest, fastest, most efficient
- `gemini-1.5-pro` - High capability model for complex reasoning
- `gemini-2.0-flash` - Latest experimental features

### Development Tasks

**One-shot Mode Testing:**
```bash
# Code analysis
go run klein/main.go "Analyze the architecture of this Go project"

# Code generation
go run klein/main.go "Create a REST API with user authentication"

# Testing
go run klein/main.go "Write unit tests for the react package"

# Refactoring
go run klein/main.go "Refactor this code to use dependency injection"
```

**Interactive Mode Testing:**
```bash
# Start interactive mode
go run klein/main.go

# Then use commands like:
> Create a HTTP server with health check
> Analyze the current codebase structure
> Write unit tests for the Agent
> List files in the current directory
> Run go build and fix any errors
> /help    # Show available commands
> /clear   # Clear conversation history
> /quit    # Exit interactive mode
```

### Cursor Positioning and Input Handling

The interactive REPL provides full cursor positioning support with natural text editing behavior:

**Cursor Movement:**
- **Arrow Keys**: Navigate cursor left/right within the input line
- **Home/End**: Jump to beginning/end of input
- **Natural Insertion**: Type at any cursor position to insert text at that location
- **Backspace/Delete**: Remove characters at cursor position

**Implementation Architecture:**
The cursor positioning system uses a **readline-authoritative approach** where the readline library maintains the canonical cursor state, and the PromptBuilder synchronizes from it:

```go
// PromptBuilder syncs from readline's authoritative state
func (p *PromptBuilder) SyncFromReadline(line []rune, pos int) {
    // Update buffer from readline's line
    p.buf = make([]rune, len(line))
    copy(p.buf, line)
    
    // Update cursor position from readline
    if pos < 0 { pos = 0 }
    if pos > len(p.buf) { pos = len(p.buf) }
    p.cursorPos = pos
}
```

**Key Design Decisions:**
- **Single Source of Truth**: Readline handles all cursor movement and input events natively
- **State Synchronization**: PromptBuilder follows readline's state rather than maintaining parallel state
- **Minimal Event Handling**: Listener only handles special cases (Ctrl+C, Ctrl+K at start)
- **Clean API**: Removed unused cursor manipulation methods to keep interface minimal

**Benefits:**
- **No Double-Handling**: Eliminates issues where keys were processed by both readline and custom handlers
- **Native Feel**: Cursor behavior matches standard terminal input expectations
- **Reliability**: Leverages readline's mature input handling rather than reimplementing it
- **Maintainability**: Simpler codebase with clear separation of concerns

This approach resolved previous issues where cursor movement and text insertion weren't working correctly, providing a seamless editing experience in interactive mode.

### Integration Testing

Test different skills to evaluate the skill-based system:

1. **CODE Skill (Default)**: `go run klein/main.go "Create a new Go HTTP server with health check endpoint"`
2. **CODE Skill (Tools)**: `go run klein/main.go "List all Go files and analyze their purposes in this project"`
3. **RESPOND Skill**: `go run klein/main.go -s respond "Explain the difference between channels and mutexes in Go"`
4. **RESPOND Skill (Knowledge)**: `go run klein/main.go -s respond "What are Go best practices for error handling?"`

### Security Testing

1. **Configuration Validation**: Test settings.json validation and error handling
2. **MCP Server Integration**: Verify MCP server connection failures are handled gracefully
3. **Tool Capability Detection**: Test automatic detection of model tool calling capabilities
4. **API Key Management**: Ensure API keys are never logged or exposed in debug output

### Unit Testing Coverage

The project includes comprehensive unit tests for:

**Skill System Testing:**
- YAML skill loading and parsing
- Template variable substitution
- Tool scope configuration parsing
- Skill assignment and execution

**Configuration Testing:**
- Settings validation and loading
- MCP server configuration validation
- Default value application

**ReAct Pattern Testing:**
- Message state management with skill context
- Tool call handling across different skills
- Error handling and recovery
- JSON parsing and structured output

**Message State Management Testing:**
- Message source filtering and removal by source type
- Summary replacement (ensuring only 0 or 1 summary exists)
- Conversation compaction with safe split points
- Tool call chain preservation during compaction

**Tool Schema Testing:**
Comprehensive test suites for tool schema handling:
- Schema-as-tool pattern validation
- JSON Schema to API format conversion
- Required fields extraction and handling
- Tool serialization/deserialization for API communication
- Type safety validation (Go types → JSON Schema → API format)

### Integration Testing

Tests use mocked dependencies to ensure:
- Clean skill-based execution flow
- Skill-specific tool isolation
- YAML-driven prompt generation
- Configuration validation and error handling
- Tool schema consistency across different model capabilities

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests for new functionality
5. Run unit tests and ensure they pass: `make test`
6. Run integration tests: `make integ`
7. Run code quality checks: `make lint`
8. Submit a pull request

### Integration Test Suite

The project includes comprehensive integration tests in the `testsuite/` directory:

```bash
# Run all integration tests
make integ

# Run specific integration tests
cd testsuite
./runner.sh <skill> <backend>

# Examples:
./runner.sh planning anthropic    # Test planning with Anthropic
./runner.sh code ollama           # Test code skill with Ollama
./runner.sh respond openai        # Test respond skill with OpenAI
```

**Integration Test Categories:**
- **Planning Capability**: Tests the ability to break down complex tasks
- **Skill Execution**: Tests CODE and RESPOND skill workflows
- **Backend Compatibility**: Tests all LLM backends (Ollama, Anthropic, OpenAI, Gemini)
- **Tool Integration**: Tests tool calling across different skills
- **Configuration Management**: Tests settings.json and MCP configuration

**Before Submitting PRs:**
- Ensure all unit tests pass (`make test`)
- Run integration tests with `make integ` or test specific skills/backends with `./runner.sh`
- Update integration tests if you modify skill behavior or add new backends

## Build System

### Makefile Targets

```bash
make test          # Run all unit tests
make integ         # Run all integration tests
make build         # Build the binary
make install       # Install to $GOPATH/bin
make clean         # Clean build artifacts
make deps          # Download dependencies
```

### Module Information

- **Module**: `github.com/fpt/klein-cli`
- **Go Version**: 1.24.4
- **Key Dependencies**: 
  - `github.com/ollama/ollama v0.11.10`
  - `github.com/anthropics/anthropic-sdk-go v1.5.0`
  - `github.com/openai/openai-go/v2 v2.0.2`
  - `google.golang.org/genai v1.19.0`
  - `github.com/chzyer/readline v1.5.1`
  - `github.com/mark3labs/mcp-go`

## Reference

- **Anthropic SDK**: https://pkg.go.dev/github.com/anthropics/anthropic-sdk-go
- **Ollama API**: https://pkg.go.dev/github.com/ollama/ollama/api
- **OpenAI SDK**: https://pkg.go.dev/github.com/openai/openai-go/v2
- **Google Generative AI**: https://pkg.go.dev/google.golang.org/genai
- **MCP Protocol**: https://github.com/mark3labs/mcp-go
- **ReAct Pattern**: https://arxiv.org/abs/2210.03629
