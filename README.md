# KLEIN CLI

A CLI-based AI coding agent supporting multiple LLM backends, using the ReAct (Reason and Act) pattern and MessageState with compaction to interact with tools while maintaining context.

The default skill focuses on coding tasks with todo management, built-in tools, and user-configured tools via MCP client functionality.

The name KLEIN is inspired by the Klein bottle, a topological surface with no distinct inside or outside — symbolizing the seamless collaboration between human and AI.

## Features

- **Interactive Mode**: REPL-style interface for continuous interaction with conversation memory
- **Multiple LLM Backends**: Support for Ollama (gpt-oss), Anthropic Claude, OpenAI GPT, and Google Gemini
- **Simplified ReAct Pattern**: Streamlined reasoning and acting with single-action loops for simplicity
- **Integrated Tools**: File operations, grep search, bash tools, todo tools, and simple web tools
- **Secure File Access**: Files are accessible only in working directory. Also, applies Read-before-Write semantics for content updates.
- **Smart Tool Approval**: Interactive approval system for potentially destructive operations (Write, Edit, MultiEdit)
- **MCP Server Support**: MCP Servers can be configured in settings.json
- **Conversation State Management**: Automatic handling of conversation history and context
- **AGENTS.md support**: Includes content of AGENTS.md to system prompt automatically

## Quick Start

### Installation

```bash
go install github.com/fpt/klein-cli/klein@latest
```

### Prerequisites

**For Ollama (default):**
- Install Ollama: https://ollama.ai/
- Pull a model: `ollama pull gpt-oss:latest`
- Set `OLLAMA_HOST` and `OLLAMA_PORT` environment variable if needed.

**For Anthropic Claude:**
- Set `ANTHROPIC_API_KEY` environment variable

**For OpenAI:**
- Set `OPENAI_API_KEY` environment variable

**For Google Gemini:**
- Set `GEMINI_API_KEY` environment variable

### Basic Usage

**Interactive Mode (default):**
```bash
# Start the interactive REPL
klein

# Or interactive with Anthropic Claude
klein -b anthropic

# Then use commands like:
> Create an HTTP server with a health check
> Analyze the current codebase structure
> Write unit tests for this package
> List files in the current directory
> Run go build and fix any errors
> /help    # Show available commands
> /clear   # Clear conversation history
> /quit    # Exit interactive mode
```

**One-shot Mode:**
```bash
# Run a single command with the default model
klein "Create an HTTP server with a health check endpoint"

# Use different backends
klein -b anthropic "Analyze this codebase"
klein -b openai -m gpt-5-mini "Create a console program which calculates fibonacci number in Golang."

# Offline use
klein -b ollama -m gpt-oss:latest "Write a simple main.go that prints 'Hello, world!'. Use write tool."
```

## Supported Models

- **Anthropic**: `claude-3-7-sonnet-latest`, `claude-sonnet-4-20250514`
- **OpenAI**: `gpt-5`, `gpt-5-mini`
- **Ollama**: `gpt-oss:latest`
- **Google Gemini**: `gemini-2.5`, `gemini-2.5-flash`

## Tool Approval System

KLEIN includes a smart approval system that prompts for confirmation before executing potentially destructive file operations, providing safety while maintaining workflow efficiency.

### How Tool Approval Works

**Automatic Approval (Safe Operations):**
- Read operations (viewing files, listing directories)
- Search and analysis tools (grep, code analysis)
- Non-destructive tools (todo management, web search)

**Interactive Approval (Destructive Operations):**
- `Write` - Creating new files or overwriting existing ones
- `Edit` - Modifying existing files with string replacement
- `MultiEdit` - Batch editing operations across multiple files

**Approval Options:**
- **Yes** - Approve this operation only
- **Always** - Approve this operation and auto-approve all future file operations in this session
- **No** - Cancel the operation and continue the conversation

### Approval Modes

**Interactive Mode (Default):**
```bash
📝 About to write file(s):
📋 Write to /path/to/file.go: Creating main HTTP server...

? Approve this file operation? (Yes/Always/No)
```

**Non-Interactive Mode:**
When running in non-interactive environments (pipes, scripts), operations are automatically approved with logged notifications.

## Configuration

### Unified Settings (settings.json)

KLEIN CLI uses a unified configuration system with settings stored in `~/.klein/settings.json`.

**Automatic Setup**: When you first run KLEIN, it automatically creates a default `~/.klein/settings.json` file with example configurations that you can modify.

**💡 To enable MCP servers**: Change `"enabled": false` to `"enabled": true` and update the server configuration with your actual MCP server details.

### Configuration Management

**Automatic Configuration Search:**
1. `.agents/settings.json` in current directory
2. `$HOME/.klein/settings.json` in home directory  
3. Defaults if no configuration found

**Override with Command Line:**
```bash
# Override backend and model
klein -b anthropic -m claude-3-7-sonnet-latest "Analyze this code"

# Use custom settings file
klein --settings ./my-settings.json "Create a simple web server in Golang."
```

### MCP (Model Context Protocol) Integration

**MCP Server Configuration:**
- **stdio servers**: External processes communicating via stdin/stdout
- **SSE servers**: HTTP Server-Sent Events endpoints
- **Allowed Tools (optional)**: Limit context size by specifying only needed tools. If omitted, all tools from the server are allowed.
- **Environment Variables**: Set per-server environment

**Example MCP Server (godevmcp):**

```json
{
  "mcp": {
    "servers": [
      {
        "name": "godevmcp",
        "enabled": true,
        "type": "stdio",
        "command": "godevmcp",
        "args": ["serve"]
      }
    ]
  }
}
```

## Development

**[📖 Development Guide](doc/DEVELOPMENT.md)**

This includes:
- Architecture overview and design patterns
- Structured output system with generics
- Token usage reporting and provider‑native caching hooks
- Testing and code quality guidelines
- Project structure and contribution workflow
- Model capabilities and integration testing

## AGENTS.md

This repository includes AGENTS.md — a short developer guide for automated agents and contributors describing available tools, workflows, and safety expectations.

## At‑mark file embedding ("@filename" syntax)

You can reference files in prompts using @filename. KLEIN expands @filename into the file's contents when sending prompts to the model; if a file can't be read, a note is left in place. See internal/app/prompt_builder.go for implementation details.

## ⚠️ Important Notices

### Responsible Use
- This tool is provided for research and development purposes
- Users are responsible for complying with LLM provider terms of service and applicable laws
- Users must ensure their API usage adheres to rate limits and usage policies
- Malicious use is strictly prohibited

### Security Best Practices
- **Never hardcode API keys** - always use environment variables:
  ```bash
  export ANTHROPIC_API_KEY="your_anthropic_key"
  export OPENAI_API_KEY="your_openai_key"  
  export GEMINI_API_KEY="your_gemini_key"
  ```
- Keep your API keys secure and rotate them regularly
- Be cautious when sharing configurations, logs, or screenshots that might contain sensitive information
- Review AI-generated code before using it in production systems

### Model Capability Warnings
klein automatically tests unknown Ollama models for tool-calling capability:
- ✅ **Known compatible models** (like `gpt-oss:latest`) work without testing
- ⚠️ **Unknown models** are tested automatically with clear warnings about limitations
- 🚫 **Non-tool-capable models** will have limited functionality (no file operations, web search, etc.)

### Disclaimer

This software is provided "as is" under the Apache 2.0 License without warranty of any kind. The developers are not responsible for any damage, data loss, API costs, or misuse resulting from the use of this software.

## License

Copyright 2025 Youichi Fujimoto. All rights reserved.

This project is licensed under the Apache 2.0 License.
