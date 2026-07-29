# KLEIN CLI

A CLI-based AI coding agent supporting multiple LLM backends, using the ReAct (Reason and Act) pattern and MessageState with compaction to interact with tools while maintaining context.

The default skill focuses on coding tasks with todo management, built-in tools, and user-configured tools via MCP client functionality.

The name KLEIN is inspired by the Klein bottle, a topological surface with no distinct inside or outside — symbolizing the seamless collaboration between human and AI.

## Features

- **Interactive Mode**: REPL-style interface for continuous interaction with conversation memory. Each run starts a fresh session; `--continue` resumes the most recent one
- **Multiple LLM Backends**: OpenAI GPT, Anthropic Claude, Google Gemini, plus the codex/appserver whole-agent backends
- **Simplified ReAct Pattern**: Streamlined reasoning and acting with single-action loops for simplicity
- **Integrated Tools**: File operations, grep search, bash tools, todo tools, and simple web tools
- **Secure File Access**: Files are accessible only in working directory. Also, applies Read-before-Write semantics for content updates.
- **Smart Tool Approval**: Interactive approval system for potentially destructive operations (Write, Edit, MultiEdit)
- **Persistent Permission Rules**: Allow/deny rules in JSON files at user (`~/.klein/permissions.json`) and project (`.klein/permissions.json`) level; survive restarts and support glob patterns
- **MCP Server Support**: MCP Servers can be configured in settings.json
- **Conversation State Management**: Automatic handling of conversation history and context
- **AGENTS.md support**: Includes content of AGENTS.md to system prompt automatically
- **Messaging Gateway (`klein claw`)**: Discord integration for using the agent as a personal AI assistant via messaging

## Quick Start

### Installation

```bash
go install github.com/fpt/klein-cli/klein@latest
```

### Prerequisites

**For OpenAI (default):**
- Set `OPENAI_API_KEY` environment variable

**For Anthropic Claude:**
- Set `ANTHROPIC_API_KEY` environment variable

**For OpenAI:**
- Set `OPENAI_API_KEY` environment variable

**For Google Gemini:**
- Set `GEMINI_API_KEY` environment variable

**For OpenAI Codex (`-b codex`, agentic backend):**
- Install the [`codex` CLI](https://github.com/openai/codex) and make sure `codex` is on your `PATH`
- Log in once: `codex login` (ChatGPT account or API key). klein uses codex's own auth/model — no klein env key. A login/config problem surfaces at klein startup.
- Model comes from codex (or set `llm.model`); reasoning effort from `llm.effort`.

> **Linux sandbox note:** codex sandboxes command/file execution with **bubblewrap** (`bwrap`), which needs the kernel to allow **unprivileged user namespaces**. Installing `bwrap` is not enough — many hardened kernels and Ubuntu 23.10+/24.04 (via AppArmor) block userns, and you'll see:
> `codex_app_server: Codex's Linux sandbox uses bubblewrap and needs access to create user namespaces.`
>
> Fix one of two ways:
>
> 1. **Allow unprivileged user namespaces** (keeps codex sandboxed — recommended):
>    ```bash
>    # Ubuntu 23.10+/24.04 (AppArmor gate)
>    sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0
>    echo 'kernel.apparmor_restrict_unprivileged_userns=0' | sudo tee /etc/sysctl.d/60-userns.conf
>    # Debian / older Ubuntu
>    sudo sysctl -w kernel.unprivileged_userns_clone=1
>    echo 'kernel.unprivileged_userns_clone=1' | sudo tee /etc/sysctl.d/60-userns.conf
>    sudo sysctl --system
>    ```
>    Verify: `bwrap --ro-bind / / --unshare-user echo ok` prints `ok`.
>
> 2. **Skip codex's OS sandbox** (quick unblock — no isolation) via settings.json:
>    ```json
>    { "llm": { "backend": "codex" }, "codex": { "sandbox_mode": "danger-full-access" } }
>    ```
>    Codex then runs commands with no process isolation, so prefer the interactive `klein claw repl` (which prompts for approval before each command/file edit) over the headless gateway when using this.

### Basic Usage

**Interactive Mode (default):**
```bash
# Start the interactive REPL — a fresh session each time
klein

# Resume this project's most recent session
klein --continue     # or: klein -c

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
klein -b openai -m gpt-5.6-luna "Create a console program which calculates fibonacci number in Golang."

# Pick a backend explicitly
klein -b anthropic "Write a simple main.go that prints 'Hello, world!'. Use write tool."
```

### AI Code Review (`klein review`)

`klein review` runs an AI code review over a unified diff. It is designed to be
driven by a harness (e.g. a GitHub Action) that handles all GitHub interaction —
klein itself never runs `git` or `gh`:

1. The harness fetches the PR title/description and diff, and checks out the PR head.
2. `klein review` enriches each hunk with ±10 context lines read from the checkout,
   then reviews with a restricted toolset: `Read`, `Glob`, `LS`, plus
   `AddInlineReview` / `AddSummaryReview` / `FinalizeReview` / `ResolveReviewComment`
   to accumulate the review.
   Inline comment lines are validated to fall within the diff's new-side hunk ranges.
3. The harness posts the result as one PR review (inline comments + summary).

Reviews are **stateful across rounds**: the summary lives in one sticky PR
comment that is updated each round (with turn / total / active-comment stats),
state is embedded there as an HTML marker, later pushes get an incremental
review of just the new changes, a push that doesn't change the PR diff (e.g. a
web-UI "Rebase branch") is skipped, a real force-push falls back to a full
review, previous unresolved comments are fed back to the model, and threads it
verifies as fixed are resolved on GitHub. Every inline comment carries a
required severity: `must` / `major` / `minor` / `nits`.

```bash
# Input:  {"title": "...", "body": "...", "diff": "<unified diff>"}
# Output: {"summary", "verdict", "finalized", "comments": [{"path", "line", "end_line", "severity", "body"}]}
klein review --input review-request.json --output review-result.json --workdir /path/to/pr-head

# Backend/model/context/language overrides (--language defaults to en)
klein review -b openai -m gpt-5.6-luna --context 10 --language ja --input - < review-request.json
```

A ready-made harness lives in this repo: the composite action
`.github/actions/ai-review` (fetches the PR, runs `klein review`, posts the
review via `gh api`) and the reference workflow `.github/workflows/ai-review.yml`
that reviews this repo's own PRs. To use it, set the `OPENAI_API_KEY` repository
secret. Design details: [doc/AI_REVIEW.md](doc/AI_REVIEW.md).

**Using it from another repository** — call the reusable workflow; klein is
installed from the release binaries automatically:

```yaml
# .github/workflows/ai-review.yml (in your repo)
name: AI Review
on:
  pull_request:
    types: [opened, synchronize, reopened]
permissions:
  contents: read
  pull-requests: write
jobs:
  review:
    if: github.event.pull_request.head.repo.full_name == github.repository
    uses: fpt/klein-cli/.github/workflows/ai-review-reusable.yml@main
    with:
      backend: openai             # optional: openai (default), anthropic, or gemini
      language: ja                # optional (default: en)
      # model:                    # optional; if set it MUST match `backend` (default: the backend's own default)
      effort: low                 # optional reasoning effort
      max-turns: "20"             # optional agent iteration cap
      max-budget-tokens: "200000" # optional token budget (partial review if exceeded)
      max-comments: "15"          # optional inline-comment cap (excess trimmed by severity)
      klein-version: v0.1.1       # optional (default: latest release)
    secrets:
      # Pass the key matching `backend` (any/all may be set):
      openai-api-key: ${{ secrets.OPENAI_API_KEY }}
      # anthropic-api-key: ${{ secrets.ANTHROPIC_API_KEY }}
      # gemini-api-key: ${{ secrets.GEMINI_API_KEY }}
```

The review works with any of the three direct LLM backends — `openai`
(default), `anthropic`, or `gemini` — via native tool calling; set `backend`
and pass the matching key secret. Leave `model` unset to use that backend's
default; if you do set `model`, it must be one that backend serves (an explicit
model overrides the backend default). (The whole-agent `codex`/`appserver`
backends are not supported for review.)

Pin `@main` or a tag (GitHub Actions has no `@latest` ref). Release binaries
(`klein_<os>_<arch>.tar.gz`) are attached by `.github/workflows/release.yml`
when a release is published; if an asset is missing the action falls back to
`go install`.

## Supported Models

- **Anthropic**: `claude-opus-4-7`, `claude-sonnet-4-6`, `claude-haiku-4-5`
- **OpenAI**: `gpt-5.6-luna` (default), `gpt-5.6-sol`, `gpt-5.6-terra`
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

### Persistent Permission Rules

Rules that survive across sessions can be stored in JSON files at three levels (higher priority listed first):

| File | Scope |
|------|-------|
| `{project}/.klein/permissions.local.json` | Project-local (add to `.gitignore`) |
| `{project}/.klein/permissions.json` | Project-wide (committable) |
| `~/.klein/permissions.json` | User-wide defaults |

**Rule format:**

```json
{
  "rules": [
    {"tool": "Write", "pattern": "src/**",    "behavior": "allow"},
    {"tool": "Bash",  "pattern": "go *",      "behavior": "allow"},
    {"tool": "Bash",  "pattern": "rm -rf *",  "behavior": "deny"}
  ]
}
```

**Pattern syntax:**

| Pattern | Matches |
|---------|---------|
| `""` (omitted) | Every invocation of the tool |
| `**` | Every invocation of the tool |
| `src/**` | `src` directory and all files beneath it |
| `go *` | Any string starting with `go ` (trailing wildcard) |
| `src/*.go` | Standard glob — `*` does not cross `/` |
| `src/main.go` | Exact match |

Rules are evaluated first-match-wins. A local rule overrides a project rule; a project rule overrides a user rule.

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
klein -b anthropic -m claude-sonnet-4-6 "Analyze this code"

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

## Gateway (`klein claw`)

`klein claw` is an OpenClaw-inspired messaging gateway that makes the agent accessible via Discord. It is a subcommand of `klein`, not a separate binary, and by default it starts an **embedded, in-process agent server** — so a single command is the whole gateway.

```
Discord ──► klein claw ──► [in-process agent]
                       ◄── streaming events ◄──
```

Set `agent_addr` in the `claw` config (or pass `--agent-addr`) to dial a separately-run `klein --serve` instead, which splits the two across processes.

### Quick Start

**1. Add a `claw` section to `settings.json`:**

```json
{
  "llm": { "backend": "anthropic", "model": "claude-sonnet-4-6" },
  "base_dir": "~/.klein",
  "claw": {
    "default_skill": "claw",
    "discord": {
      "token": "YOUR_DISCORD_BOT_TOKEN",
      "allowed_user_ids": ["YOUR_DISCORD_USER_ID"],
      "mention_only": true
    }
  }
}
```

**2. Start the gateway:**

```bash
# From source
go run ./klein claw

# Or build and run
make build
./output/klein claw

# A fully isolated second instance (its own base_dir + Discord token)
./output/klein claw --settings ~/work/settings.json
```

Sessions, memory, and the schedule store are **not** configured in the `claw` block — they derive from the top-level `base_dir` (default `~/.klein`), shared with the CLI.

### Interactive CLI — `klein claw repl`

`klein claw repl` opens a terminal chat that shares claw's tools (memory, schedules, MCP) and backend but keeps its own session. It's the local frontend for inspecting and curating memory and schedules; it does not start Discord or the scheduler.

```bash
./output/klein claw repl
```

### Discord Bot Setup

1. Go to the [Discord Developer Portal](https://discord.com/developers/applications)
2. Create a new application and add a Bot
3. Enable the **MESSAGE CONTENT** privileged intent under Bot settings
4. Generate an invite URL with `bot` scope and `Send Messages` + `Read Message History` permissions
5. Invite the bot to your server
6. Copy the bot token into the `claw.discord.token` field of your `settings.json`

### Gateway Commands

In Discord, use `!` prefix for gateway commands:

| Command | Description |
|---------|-------------|
| `!clear` | Clear conversation and start fresh |
| `!skill <name>` | Switch the session's default skill |
| `!memory` | Show stored memory content |
| `!help` | Show available commands |

Slash commands are also available: `/list` shows the loaded skills, and `/<skill> [args]` runs one skill for a single message without changing the session's persistent skill.

### Memory System

The gateway includes a persistent memory system at `<base_dir>/memory/`:
- **MEMORY.md** — Long-term facts about the user (preferences, projects, etc.)
- **daily/YYYY-MM-DD.md** — Daily journal notes for significant events
- **runs/YYYY-MM-DD.md** — Daily log of scheduled runs, so one job can read another's output

Memory context is automatically injected into each conversation. The agent can read and update these files using its standard filesystem tools.

### Schedules

Recurring jobs are configured in the `claw.schedules` array. Each job fires on a standard 5-field **cron expression** in a required **timezone**, and `"silent": true` runs the prompt without posting the result back to a channel.

```json
"schedules": [
  {
    "name": "nightly-memory",
    "enabled": true,
    "cron": "45 23 * * *",
    "timezone": "Asia/Tokyo",
    "skill": "claw",
    "silent": true,
    "prompt": "Review today's runs/ log and daily note, and summarise what's worth keeping into today's daily note."
  }
]
```

Schedules the agent creates itself (via the `Schedule*` tools) are stored in `<base_dir>/schedules.json` and live-reloaded, so no restart is needed.

### Full Configuration Reference

Every `claw` field — plus `base_dir`, the `discord`/`memory`/`schedules` blocks, and multi-instance setup — is documented in **[§5 of the Configuration Reference](doc/CONFIGS.md#5-gateway-configuration-klein-claw)**.


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

### Disclaimer

This software is provided "as is" under the Apache 2.0 License without warranty of any kind. The developers are not responsible for any damage, data loss, API costs, or misuse resulting from the use of this software.

## License

Copyright 2025 Youichi Fujimoto. All rights reserved.

This project is licensed under the Apache 2.0 License.
