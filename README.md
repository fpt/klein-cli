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
- **Messaging Gateway (klein-claw)**: Discord integration for using the agent as a personal AI assistant via messaging

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

## Gateway (klein-claw)

klein-claw is an OpenClaw-inspired messaging gateway that makes the agent accessible via Discord. It runs as a separate binary communicating with the klein agent over Connect-gRPC.

```
Discord ──► klein-claw (gateway) ──► Connect-gRPC ──► klein agent (--serve)
                                  ◄── streaming events ◄──
```

### Quick Start

**1. Start the agent in server mode:**
```bash
# With Anthropic Claude
klein --serve -b anthropic

# Or build and run
make build
./output/klein --serve -b anthropic --serve-addr :50051
```

**2. Create a gateway config:**

Create `~/.klein/claw/config.json`:
```json
{
  "agent_addr": "http://localhost:50051",
  "working_dir": "/path/to/your/project",
  "default_skill": "claw",
  "discord": {
    "token": "YOUR_DISCORD_BOT_TOKEN",
    "allowed_user_ids": ["YOUR_DISCORD_USER_ID"],
    "mention_only": true
  }
}
```

**3. Start the gateway:**
```bash
# From source
go run ./cmd/gateway --config ~/.klein/claw/config.json

# Or build and run
make build-gateway
./output/klein-claw --config ~/.klein/claw/config.json
```

### Discord Bot Setup

1. Go to the [Discord Developer Portal](https://discord.com/developers/applications)
2. Create a new application and add a Bot
3. Enable the **MESSAGE CONTENT** privileged intent under Bot settings
4. Generate an invite URL with `bot` scope and `Send Messages` + `Read Message History` permissions
5. Invite the bot to your server
6. Copy the bot token into your gateway config

### Gateway Commands

In Discord, use `!` prefix for gateway commands:

| Command | Description |
|---------|-------------|
| `!clear` | Clear conversation and start fresh |
| `!skill <name>` | Switch skill (code, respond, claw) |
| `!memory` | Show stored memory content |
| `!help` | Show available commands |

### Memory System

The gateway includes a persistent memory system at `~/.klein/claw/memory/`:
- **MEMORY.md** — Long-term facts about the user (preferences, projects, etc.)
- **daily/YYYY-MM-DD.md** — Daily journal notes for significant events

Memory context is automatically injected into each conversation. The agent can read and update these files using its standard filesystem tools.

### Heartbeat

Optional periodic prompt execution. Configure in `config.json`:
```json
{
  "heartbeat": {
    "enabled": true,
    "interval": "24h",
    "prompt": "Review MEMORY.md and create today's daily note.",
    "skill": "claw",
    "channel_type": "discord",
    "channel_id": "YOUR_CHANNEL_ID"
  }
}
```

### Configuration Reference

| Field | Default | Description |
|-------|---------|-------------|
| `agent_addr` | `http://localhost:50051` | Connect-gRPC server address |
| `working_dir` | `.` | Agent working directory |
| `default_skill` | `claw` | Skill used for message handling |
| `default_model` | `claude-sonnet-4-5-20250929` | LLM model |
| `max_iterations` | `15` | ReAct loop cap per invocation |
| `discord.token` | | Discord bot token (required) |
| `discord.allowed_guild_ids` | `[]` | Guild allowlist (empty = all) |
| `discord.allowed_channel_ids` | `[]` | Channel allowlist (empty = all) |
| `discord.allowed_user_ids` | `[]` | User allowlist (empty = all) |
| `discord.mention_only` | `false` | Only respond when @mentioned in guilds |
| `memory.base_dir` | `~/.klein/claw/memory/` | Memory storage directory |
| `memory.max_notes` | `30` | Maximum daily notes to retain |
| `heartbeat.enabled` | `false` | Enable periodic prompts |
| `heartbeat.interval` | `24h` | Go duration string |
| `heartbeat.prompt` | | Prompt text to execute |

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
