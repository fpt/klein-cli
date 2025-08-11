# AGENTS.md

This file provides guidance to AI agent when working with code in this repository.

## Development Commands

**Build and Run:**
```bash
# Interactive mode (default)
go run klein/main.go                         # Start interactive REPL
go run klein/main.go -b anthropic            # Interactive with Anthropic

# One-shot mode
go run klein/main.go "your requirements"     # Run with requirements

# Build commands
go build -o klein ./klein                # Build binary
go build ./...                                       # Build all packages
go mod tidy                                          # Download/update dependencies
```

**Interactive Mode Usage:**
```bash
# Start interactive mode
go run klein/main.go

# Then use slash commands or natural language:
> Create a HTTP server with health check endpoint
> Analyze the current codebase structure  
> Write unit tests for the ReactAgent
> List files in the current directory
> Run go build and fix any errors
> /help    # Show available commands
> /clear   # Clear conversation history
> /quit    # Exit interactive mode
```

**Interactive Mode Features:**
- **Readline Support**: Full cursor movement, arrow keys, and command history
- **Autocomplete**: Tab completion for slash commands and file paths
- **Unified Command Handler**: All commands start with `/` to avoid conflicts
- **Session Persistence**: Conversation history preserved between sessions

**One-shot Mode Usage:**
```bash
go run klein/main.go "Create a HTTP server with health check endpoint"
go run klein/main.go "List files in current directory" 
go run klein/main.go "Run go build on this project"

# Multi-backend support
go run klein/main.go -b anthropic "Analyze this codebase"
go run klein/main.go -b openai -m gpt-4o "Create a REST API"
go run klein/main.go -b gemini -m gemini-2.5-flash-lite "Optimize this code"
go run klein/main.go -b ollama -m gpt-oss "Write unit tests"
```

**Advanced Usage Examples:**
```bash
# Code analysis with ReAct reasoning
go run klein/main.go "Analyze the ReAct pattern implementation and suggest improvements"

# Code generation with tool usage
go run klein/main.go "Create a REST API with user authentication using Go"

# Testing and validation
go run klein/main.go "Write comprehensive unit tests for the ReactAgent"

# Refactoring with dependency injection
go run klein/main.go "Refactor this code to use proper dependency injection"

# Multi-step development workflow
go run klein/main.go "Create a new microservice with Docker support, tests, and documentation"
```

**Testing:**
```bash
go test ./...                      # Run all tests
go test -v ./...                   # Run tests with verbose output
go test ./internal/app             # Test specific package (app layer)
go test ./pkg/agent/react          # Test ReAct implementation
```

**Code Quality:**
```bash
go fmt ./...                       # Format code
go vet ./...                       # Static analysis
go mod tidy                        # Clean up dependencies
```

## User Configuration

**klein maintains per-user configuration and project data in `$HOME/.klein/` (interactive mode only):**

```
$HOME/.klein/
├── projects/                    # Project-specific data
│   └── {project-name-hash}/    # Each project gets its own directory
│       ├── project_info.txt   # Original project path and metadata
│       ├── todos.json         # Project-specific todo list
│       └── session.json       # Conversation history and context
└── config.json                 # User preferences (future use)
```

**Key Features:**
- **Project Isolation**: Each project gets its own todo list, session data, and storage (interactive mode only)
- **Session Persistence**: Conversation history is automatically saved and restored between interactive runs (like Claude Code)
- **Safe Directory Names**: Project paths are converted to safe directory names with hash suffixes
- **Mode-Based Persistence**: Interactive mode uses persistent storage, one-shot mode uses in-memory only
- **Clean Project Structure**: No configuration files clutter your project directories
- **Automatic Creation**: User directories are created automatically when first needed (interactive mode only)
- **No Fallbacks**: Always uses `$HOME/.klein/` - no local file fallbacks

**Project Directory Naming:**
Projects are stored in directories using the pattern: `{project-basename}-{path-hash}`
- Example: `/Users/you/dev/my-app` → `my-app-a1b2c3d4/`
- Handles name collisions and special characters safely

**Mode-Specific Behavior:**

**Interactive Mode (`klein` with no arguments):**
- Creates and uses project directories in `$HOME/.klein/projects/`
- Saves and restores conversation history between sessions
- Maintains persistent todo lists per project
- Session data preserved across invocations

**One-Shot Mode (`klein "your request"`):**
- Uses in-memory storage only - no project directories created
- No conversation history persistence
- Todo lists work but are not saved to disk

**File Mode (`klein -f prompts.txt`):**
- Similar to one-shot mode - no persistence
- Each prompt file execution starts fresh
- Designed for testing and batch processing

## Architecture

This is a Go-based skill-driven coding agent that uses SKILL.md-configured skills with the ReAct (Reason and Act) pattern. It supports multiple LLM backends, secure tool management, and interactive mode. The codebase follows a clean DDD architecture with skill-based execution:

**Core Structure:**
- `klein/main.go` - Application entry point with interactive REPL
- `internal/app/` - Application layer (DDD) with skill execution:
  - `agent.go` - Main Agent handling skill execution with thinking channel management
- `internal/config/` - Configuration management:
  - Settings, user configuration, and application preferences
- `internal/infra/` - Infrastructure layer:
  - `filesystem.go` - File system security configuration
- `internal/tool/` - Tool management with security features:
  - `composite_tool_manager.go` - Combines multiple tool managers
  - `filesystem_tool_manager.go` - Secure filesystem tools with read→write semantics
  - `todo_tool_manager.go` - Todo management tools
  - `web_tool_manager.go` - Web research and fetching tools
- `internal/skill/` - Skill definitions and loading:
  - `skills/*/SKILL.md` - Built-in skill definitions (YAML frontmatter + markdown content)
  - Skill loading, parsing, and filtering logic
- `internal/mcp/` - MCP (Model Context Protocol) integration:
  - External tool server integration and management
- `pkg/agent/` - Agent domain layer:
  - `domain/` - Domain interfaces and types
  - `react/` - ReAct pattern implementation with thinking channel support
  - `state/` - Message state management and session persistence
- `pkg/message/` - Message handling and thinking stream management
- `pkg/client/` - LLM client implementations and abstractions:
  - `withtool.go` - ClientWithTool wrapper for tool management
  - LLM client implementations (Ollama, Anthropic, OpenAI, Gemini)

**Key Types:**
- `app.Agent` - Main application service handling skill execution with thinking channel management
- `skill.Skill` - SKILL.md-based skill definition with allowed-tools, description, and prompt content
- `tool.CompositeToolManager` - Combines multiple specialized tool managers
- `tool.FileSystemToolManager` - Secure filesystem tools with read→write semantics and allowlist/blacklist
- `tool.TodoToolManager` - Todo management with persistent and in-memory variants
- `tool.WebToolManager` - Web research and content fetching tools
- `react.ReAct` - Core ReAct implementation with thinking channel support
- `client.ClientWithTool` - Auto-detecting wrapper for native vs text-based tool calling
- `domain.LLM` - Base interface for LLM clients
- `domain.StructuredLLM[T any]` - Generic interface for type-safe structured output
- `domain.ToolManager` - Interface for tool management with security controls
- `state.MessageState` - Session persistence and message management

**Skill-Based Execution:**
The system uses skills defined in SKILL.md files (embedded + filesystem). Skills are directly specified or selected based on user input.

**Universal + Specialized Tool Architecture:**
- **Universal Tools**: Always available (todos, filesystem, bash, grep) via composite manager
- **Specialized Tools**: Added based on skill requirements (web, MCP tools)
- **Security-First Design**: Read→write semantics, directory allowlists, file blacklists
- **Tool Filtering**: Tool access filtered per skill via `allowed-tools` frontmatter field

**Simplified Workflow Architecture:**
1. **Skill Selection** → User specifies skill or it is selected based on input
2. **SKILL.md Loading** → Load built-in embedded skills + project/personal custom skills
3. **Tool Filtering** → Filter available tools based on skill's `allowed-tools` field
4. **Prompt Rendering** → Render skill content with user input variables
5. **ReAct Execution** → Execute skill with filtered tools and rendered prompt
6. **Thinking Channel** → Stream thinking content via dedicated channel management

**Key Architecture Features:**
- **DDD Layering**: Clean separation with app layer (Agent) managing workflow
- **Embedded + Custom Skills**: Built-in skills embedded in binary + project/personal custom skills
- **Universal Tool Foundation**: Core tools always available, specialized tools filtered per skill
- **Thinking Channel Management**: Application layer handles thinking stream creation and management
- **Session Persistence**: Project-specific session storage with interactive/one-shot mode separation
- **Security Isolation**: Skill-specific tool access prevents unauthorized operations
- **Template-Based Prompts**: Dynamic prompt generation with variable substitution

**Tool Support:**
The system supports unified tool calling with automatic detection of model capabilities and sophisticated tool schema handling:

**Native Tool Calling:**
- Used by Anthropic Claude and tool-capable Ollama models (gpt-oss, etc.)
- API-level tool definitions with structured tool_use/tool_result blocks
- More efficient and reliable tool execution
- Automatically detected via `IsToolCapable()` interface method


**Tool Schema Enhancement:**
The system includes comprehensive tool schema handling:
```go
// Enhanced tool descriptions with parameter schemas
toolDesc := fmt.Sprintf("- %s: %s", name, tool.Description())
if len(args) > 0 {
    toolDesc += "\n  Parameters:"
    for _, arg := range args {
        required := ""
        if arg.Required {
            required = " (required)"
        }
        toolDesc += fmt.Sprintf("\n    - %s (%s)%s: %s", 
            arg.Name, arg.Type, required, arg.Description)
    }
} else {
    toolDesc += "\n  Parameters: none"
}
```

**Schema-as-Tool Pattern (Native Tool Calling for Structured Output):**
- Used by tool-capable Ollama models for structured output (gpt-oss, etc.)
- Creates a "respond" tool where target schema becomes tool parameters
- Prompt enhancement instructs models to use the "respond" tool
- JSON Schema validation ensures type-safe structured responses

**Capability-Based Design:**

The system uses a **capability-based design** with clean interface segregation:

**Interface Hierarchy:**
- `domain.LLM` - Base interface for basic chat functionality
- `domain.ToolCallingLLM` - Extends LLM with tool calling capabilities  
- `domain.StructuredLLM[T any]` - Extends LLM with type-safe structured output

**Capability Detection:**
Capabilities are determined using Go's type assertion pattern rather than boolean methods:

```go
// Check for tool calling capability  
if toolClient, ok := client.(domain.ToolCallingLLM); ok {
    // Use tool calling methods
    response, err := toolClient.ChatWithToolChoice(ctx, messages, toolChoice)
}

// Check for structured output capability
if structuredClient, ok := client.(domain.StructuredLLM[MyType]); ok {
    // Use structured output methods
    result, err := structuredClient.InvokeStructuredOutput(ctx, messages)
}
```

NOTE: Thinking is not a capability. It's a behavior of model.

**Benefits of Type Assertion Approach:**
- **Type Safety**: Compile-time guarantees that capabilities exist
- **Clean Interfaces**: No redundant boolean methods cluttering interfaces
- **Go Idioms**: Follows standard Go patterns for capability detection
- **Maintainability**: Capabilities are self-documenting through interface compliance

**Available Tools:**

**Universal Tools (always available):**
- **Todo Management**: Create, update, delete, and manage project todos
- **Secure Filesystem**: Read, write, edit files with read→write semantics and security controls
  - `read_file` - Read file contents (with timestamp tracking)
  - `write_file` - Write content to files (requires prior read, validates timestamps)
  - `list_directory` - List directory contents (allowlist restricted)
  - `edit_file` - Edit files with exact string replacement
- **Bash Execution**: Run shell commands with working directory and timeout controls

**Specialized Tools (skill-specific):**
- **Web Tools**: Web research and content fetching (code, respond skills)
  - `fetch_web` - HTML to markdown conversion for text analysis
  - `wikipedia_search` - Wikipedia content search
  - `duckduckgo_search` - Web search capabilities
- **MCP Tools**: External tool server integration (when available)
  - `tree_dir`, `get_github_content`, `search_local_files`, etc.

**Tool Binding and Security:**
Tools are bound to the LLM client with skill-specific security controls:
```go
// Filter tools based on skill's allowed-tools field
filteredTools := activeSkill.FilterTools(a.allToolManagers)

// Bind filtered tools to LLM client
llmWithTools, err := client.NewClientWithToolManager(a.llmClient, filteredTools)
reactClient := react.NewReAct(llmWithTools, filteredTools, a.sharedState, situation, maxIterations)
```

**Tool Filtering Logic:**
```go
// Agent creates ONE composite of ALL tool managers at construction
// At invoke time, skill's allowed-tools filters the available tools
func (a *Agent) Invoke(ctx context.Context, userInput string, skillName string) (message.Message, error) {
    activeSkill := a.skills[skillName]

    // Filter tools based on skill's allowed-tools field
    filteredTools := activeSkill.FilterTools(a.allToolManagers)

    // Create ReAct with filtered tools
    llmWithTools, _ := client.NewClientWithToolManager(a.llmClient, filteredTools)
    reactClient := react.NewReAct(llmWithTools, filteredTools, a.sharedState, situation, maxIterations)
    // ...
}
```

The `ClientWithTool` wrapper automatically detects whether the underlying LLM supports native tool calling via the `IsToolCapable()` method and routes to the appropriate implementation.

**Structured Output with Generics:**
The system provides type-safe structured output using Go generics:

```go
// Create a structured client with type safety
type MyResponse struct {
    Summary string `json:"summary" description:"Brief summary"`
    Steps   []Step `json:"steps" description:"List of steps"`
}

structuredClient, err := NewStructuredClient[MyResponse](baseClient)
// structuredClient is StructuredLLM[MyResponse]

// Get typed results without casting
result, err := structuredClient.InvokeStructuredOutput(ctx, messages)
// result is already typed as MyResponse, no casting needed!

// Access schema with compile-time type safety
mySchema := structuredClient.GetSchema() // Returns MyResponse, not any
```

**Provider-Specific Implementations:**
- **Ollama Tool**: Uses schema-as-tool pattern with native tool calling for structured output
- **Anthropic**: Uses schema-as-tool pattern with native tool calling
- **OpenAI/Gemini**: Uses native structured output with JSON Schema validation
- **Automatic Detection**: Factory function chooses optimal approach per provider and model capabilities

**Thinking Support:**
Both Ollama and Anthropic support thinking capabilities:
- **Ollama**: Reasoning models (gpt-oss) support the thinking parameter
- **Anthropic**: Claude models support ThinkingBlock responses for reasoning visibility
- **Channel Management**: Application layer creates and manages thinking channels
- **Stream Processing via Writer**: Thinking is streamed to an injected `io.Writer` for redirection (REPL, tests, or gRPC).
- **Automatic Detection**: Thinking enabled for capable models via type assertion
- **Debug Visibility**: Provides visible reasoning process for debugging and transparency

**Output System (Writer + Intentions):**
- **Agent Writer**: `Agent` accepts an `io.Writer` and routes thinking output to it.
- **Unified Console Writer**: The global logger console handler is configured to write to the same `io.Writer` (see `NewLoggerWithConsoleWriter` / `SetGlobalLoggerWithConsoleWriter`).
- **Intentions**: Semantic tags (`Intention`) describe Info/Debug logs (e.g., `tool`, `thinking`, `status`). Warn/Error use level only.
- **Console vs File Logs**: Console shows icons inferred from intention; file logs store `intention` as a structured key with no icons.
- **Model-Facing Outputs**: Tool responses sent back to models avoid emojis and use plain PASS/FAIL/ERROR language.

**Dependency Injection:**
The architecture uses clean dependency injection with DDD layering:
- SKILL.md files loaded at startup (embedded + filesystem)
- Universal tool manager always created with core capabilities
- Tool access filtered per skill via allowed-tools
- LLM clients wrapped with filtered tool managers per skill
- Agent (app layer) manages thinking channels and session persistence
- ReAct (domain layer) handles execution with injected tool managers
- No tight coupling between layers

**Interactive Mode:**
The application supports two modes:
1. **Interactive Mode (default)**: REPL-style interface with conversation memory
2. **One-shot Mode**: Single command execution when arguments provided

Interactive mode supports built-in slash commands:
- `/help` - Show available commands and usage information
- `/clear` - Clear conversation history and start fresh
- `/quit` or `/exit` - Exit interactive mode gracefully

## Message State Management

The system includes sophisticated message state management with intelligent compaction and summary replacement:

**Message Sources:**
Messages are categorized by source for intelligent filtering and management:
- `MessageSourceDefault` - Standard user/assistant messages
- `MessageSourceSituation` - Situation messages (removed after each iteration)
- `MessageSourceSummary` - Conversation summary messages (replaced, not accumulated)

**Summary Replacement Strategy:**
The system maintains at most one summary message in conversation history:
- **Previous Summary Removal**: Old summary messages are automatically removed before creating new ones
- **Intelligent Compaction**: When conversation exceeds 50 messages, older messages are summarized by the LLM
- **Context Preservation**: Recent messages (last 10) are always preserved for immediate context
- **Tool Chain Safety**: Compaction respects tool call/result pairs to maintain API compatibility

**Message Compaction Features:**
- **LLM-Generated Summaries**: Uses the active LLM to create intelligent conversation summaries
- **Vision Content Truncation**: Older images are removed to save tokens while preserving recent visual context
- **Tool Call Preservation**: Never splits tool call/result chains during compaction
- **Safe Split Points**: Finds boundaries that don't break conversation flow or tool interactions
- **Fallback Summaries**: Basic statistical summaries when LLM summarization fails

**Implementation Details:**
```go
// Remove previous summaries before creating new ones
previousSummariesRemoved := state.RemoveMessagesBySource(message.MessageSourceSummary)

// Create new summary with correct source
summaryMsg := message.NewSummarySystemMessage(
    fmt.Sprintf("# Previous Conversation Summary\n%s\n\n# Current Conversation Continues", summary))

// Ensure only one summary exists in history
state.AddMessage(summaryMsg)
```

**Benefits:**
- **Token Efficiency**: Keeps conversation within LLM context limits
- **Context Preservation**: Maintains conversation continuity through intelligent summarization
- **Memory Management**: Prevents memory bloat in long-running sessions
- **API Compatibility**: Maintains tool call/result pairing required by LLM APIs

## SKILL.md System

The system uses SKILL.md files (YAML frontmatter + markdown content) to define skills. Skills are loaded from multiple sources with a clear priority order.

**Skill Format:**
Each skill is defined in a `SKILL.md` file using YAML frontmatter for metadata and markdown body for the prompt content:

```markdown
---
name: code
description: Comprehensive coding assistant for all development tasks
allowed-tools:
  - read_file
  - write_file
  - edit_file
  - list_directory
  - bash
  - fetch_web
  - duckduckgo_search
argument-hint: "Describe what you want to build or change"
user-invocable: true
---

You are a coding assistant. Help the user with their development task.

User Request: $ARGUMENTS
Working Directory: {{workingDir}}

Instructions:
- Analyze the codebase before making changes
- Write clean, tested code
- Follow existing patterns and conventions
```

**Built-in Embedded Skills:**
Built-in skills are embedded in the binary from `internal/skill/skills/*/SKILL.md`:
- `code` - Comprehensive coding assistant for all development tasks
- `respond` - Direct knowledge-based responses and todo management

**Custom Skills:**
Custom skills can be placed in well-known directories to extend or override built-in skills:
- **Project skills**: `.claude/skills/*/SKILL.md` (project-specific, checked into version control)
- **Personal skills**: `~/.claude/skills/*/SKILL.md` (user-specific, not checked in)

**Priority Order:**
1. Built-in embedded skills (lowest priority)
2. Project skills in `.claude/skills/` (higher priority)
3. Personal skills in `~/.claude/skills/` (highest priority - override all others)

**Frontmatter Fields:**
- `name` - Skill identifier (used for invocation)
- `description` - Human-readable description of what the skill does
- `allowed-tools` - List of tool names the skill can access (empty = all tools available)
- `argument-hint` - Hint text shown to user for expected arguments
- `user-invocable` - Whether the skill can be directly invoked by the user (boolean)
- `model` - Optional model override for this skill

**Template Variables:**
- `$ARGUMENTS` - The user's full input text
- `$0` through `$9` - Positional arguments parsed from user input
- `{{workingDir}}` - Current working directory path

**Tool Access Control:**
The `allowed-tools` frontmatter field controls which tools are available to the skill:
- Empty list or omitted: all tools are available (no filtering)
- Specified list: only the named tools are accessible

**MCP Tool Management:**
- **Graceful Degradation**: If an MCP tool manager is not available, the system prints a warning but continues running with other available tools
- **Tool Isolation**: MCP tools are isolated per skill - only skills that explicitly list them in `allowed-tools` get access
- **Multiple MCP Support**: A single skill can list multiple MCP tools in its `allowed-tools`

**Creating Custom Skills:**
Create a directory with a `SKILL.md` file to add new skills:

```bash
# Project-level custom skill
mkdir -p .claude/skills/custom-analysis
```

```markdown
---
name: custom-analysis
description: Custom analysis workflow for specialized tasks
allowed-tools:
  - read_file
  - list_directory
  - bash
  - duckduckgo_search
argument-hint: "Describe what you want to analyze"
user-invocable: true
---

Custom analysis for specialized requirements.

User Request: $ARGUMENTS
Working Directory: {{workingDir}}

Instructions:
- Apply custom analysis techniques
- Use specialized tool combinations
- Provide detailed insights and recommendations
```

## Module Information

- Module: `github.com/fpt/klein-cli`
- Go Version: 1.24.4
- Dependencies: 
  - `github.com/ollama/ollama v0.11.10`
  - `github.com/anthropics/anthropic-sdk-go v1.5.0`
  - `github.com/openai/openai-go/v2 v2.0.2`
  - `google.golang.org/genai v1.19.0`
  - `github.com/chzyer/readline v1.5.1` - Terminal interaction with cursor movement and autocomplete

## Prerequisites

**For Ollama (default):**
- Ollama must be installed and running locally
- Set `OLLAMA_HOST` environment variable if using non-default host
- Ensure the model is available in Ollama

**For Anthropic/Claude:**
- Set `ANTHROPIC_API_KEY` environment variable with your API key
- API key can be obtained from https://console.anthropic.com/

**For OpenAI:**
- Set `OPENAI_API_KEY` environment variable with your API key
- API key can be obtained from https://platform.openai.com/api-keys
- Note: The client auto-detects streaming unsupported errors and permanently disables streaming for the session after the first failure.

**For Google Gemini:**
- Set `GEMINI_API_KEY` environment variable with your API key  
- API key can be obtained from https://makersuite.google.com/app/apikey

## Model Performance

**Ollama Models:**

**Native Tool Calling:**
- `gpt-oss:latest` - Supports native Ollama tool calling API with thinking

**Anthropic Models (Native Tool Calling):**
- `claude-3-7-sonnet-latest` - Default Claude model
- `claude-3-5-haiku-latest` - Faster Claude model
- `claude-sonnet-4-20250514` - Latest Claude Sonnet 4

**OpenAI Models (Native Tool Calling + Structured Output):**
- `gpt-4o` - Latest GPT-4 Omni (vision, tool calling, structured output)
- `gpt-4o-mini` - Smaller, faster GPT-4 Omni
- `gpt-3.5-turbo` - Fast and cost-effective for most tasks

**Google Gemini Models (Native Schema + Structured Output):**
- `gemini-2.5-flash-lite` - **Recommended** - Latest, fastest, most efficient
- `gemini-1.5-pro` - High capability model for complex reasoning
- `gemini-2.0-flash` - Latest experimental features

## Testing Practices

### Unit Tests
The project has comprehensive unit tests with 99% coverage:

```bash
# Run all tests
go test ./...

# Run with coverage
go test -cover ./pkg/agent/react/

# Run specific test suites
go test -v ./pkg/agent/react/ -run TestReAct_Invoke
go test -v ./pkg/client/ollama/ -run TestToolSchemaIntegration

# Test tool schema handling specifically
go test ./pkg/client/ollama/ -v
```

### Integration Testing
Run these tests to evaluate klein's skill-based system:
NOTE: Use `source .env` to configure API keys.
NOTE: Use `make build` to build binary and always use `./output/klein` to run.

**Skill Testing (One-shot Mode):**
1. **code skill**: `go run klein/main.go "Create a new Go HTTP server with health check endpoint"`
2. **respond skill**: `go run klein/main.go "Explain the difference between channels and mutexes in Go"`

**Security Testing:**
1. **Filesystem Access Control**: Try generating files outside working directory
2. **Read→Write Semantics**: Verify files must be read before writing
3. **Tool Isolation**: Confirm skills without filesystem in allowed-tools can't access filesystem tools
4. **Allowlist Validation**: Test directory access restrictions

**Tool Schema Testing:**
1. **Native Tool Calling**: `go run klein/main.go -b ollama -m gpt-oss:latest "List files and analyze the code"`
2. **Parameter Handling**: Test tools with complex parameter schemas (tree_dir, search_local_files)
3. **Schema Validation**: Verify proper parameter type mapping and validation
4. **Tool Selection**: Test automatic routing for native tool calling based on model capabilities

**Interactive Mode Testing:**
1. **Start Interactive**: `go run klein/main.go`
2. **Terminal Features**: Test arrow keys, cursor movement, command history
3. **Slash Commands**: Test `/help`, `/clear`, `/quit` functionality with tab completion
4. **Multi-turn Conversations**: Test conversation context across multiple turns
5. **Tool Usage**: Test secure filesystem operations vs. default tool usage
6. **State Persistence**: Verify conversation history is preserved across sessions
7. **Message Compaction**: Test automatic summary replacement in long conversations
8. **Skill Loading**: Confirm built-in embedded skills load correctly
9. **Template Rendering**: Verify `$ARGUMENTS` and `{{workingDir}}` substitution

### Unit Testing Coverage
The project includes comprehensive unit tests for:

**Skill System Testing:**
- SKILL.md loading and parsing
- Template variable substitution
- allowed-tools filtering

**Security Feature Testing:**
- FileSystemToolManager read→write semantics
- Directory allowlist enforcement
- File blacklist validation
- Timestamp-based concurrent modification detection

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
- Vision content truncation for token efficiency

**Configuration Testing:**
- Embedded skill loading from internal/skill/
- Skill content rendering and variable substitution
- FileSystem security configuration validation
- Composite tool manager composition

**Tool Schema Testing:**
Comprehensive test suites for tool schema handling:

**Native Tool Schema Testing (pkg/client/ollama/):**
- "Respond" tool creation with target schemas as parameters
- Schema-as-tool pattern validation
- JSON Schema to API format conversion
- Required fields extraction and handling
- Tool serialization/deserialization for API communication
- Prompt enhancement for tool-based structured output

**Test Coverage Features:**
- Mock tool managers with realistic tool definitions
- Complete flow testing from schema generation to tool execution
- Type safety validation (Go types → JSON Schema → API format)
- Error handling and edge case coverage
- Integration tests with serialization/deserialization
- Parameter schema consistency across different model types

**Integration Testing:**
Tests use mocked dependencies to ensure:
- Clean DDD layer separation (app → domain → infrastructure)
- Universal + specialized tool composition
- Skill-specific tool isolation
- SKILL.md-driven prompt generation
- Secure filesystem access patterns
- Tool schema consistency across different model capabilities
- Proper routing between native tool calling and schema-as-tool patterns
- Thinking channel management and streaming

## Troubleshooting (OpenAI)
- 400 Bad Request with message "Your organization must be verified to stream this model": your account/org cannot use streaming for that model.
  - Auto fallback: the client caches this condition and disables streaming for the rest of the session
  - Use a model and/or account that allows streaming
