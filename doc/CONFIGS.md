# klein-cli Configuration Reference

All configuration mechanisms in one place — CLI flags, settings files, permission rules, SKILL.md frontmatter, and environment variables.

---

## Table of Contents

1. [CLI Flags](#1-cli-flags)
2. [Settings JSON](#2-settings-json)
3. [Permission Rules](#3-permission-rules)
4. [SKILL.md Frontmatter](#4-skillmd-frontmatter)
5. [Gateway Configuration](#5-gateway-configuration)
6. [Environment Variables](#6-environment-variables)
7. [User Data Directories](#7-user-data-directories)

---

## 1. CLI Flags

```
go run klein/main.go [flags] [prompt]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-b`, `--backend` | string | `""` | LLM backend: `ollama`, `anthropic`, `openai`, `gemini` |
| `-m`, `--model` | string | `""` | Model name (overrides settings file) |
| `-s`, `--skill` | string | `"code"` | Skill to invoke |
| `--workdir` | string | `"."` | Working directory for all file operations |
| `--settings` | string | `""` | Path to settings JSON file (see [§2](#2-settings-json)) |
| `--allowed-tools` | string | `""` | Comma-separated tool names, overrides skill's `allowed-tools` |
| `-f` | string | `""` | File of multi-turn prompts separated by `---` |
| `-v`, `--verbose` | bool | `false` | Enable debug-level logging |
| `-l`, `--log` | bool | `false` | Print conversation history and exit |
| `--serve` | bool | `false` | Start Connect-gRPC server (for gateway) |
| `--serve-addr` | string | `":50051"` | Listen address for Connect server |
| `--sessions-dir` | string | `""` | Directory for session persistence (default: `~/.klein/claw/sessions/`) |
| `--memory-dir` | string | `""` | Directory for `MemorySearch`/`MemoryGet` tools |

---

## 2. Settings JSON

Settings are loaded from the first file found in order:

1. Path given by `--settings` flag
2. `{workingDir}/.agents/settings.json`
3. `$HOME/.klein/settings.json`

### Full structure

```json
{
  "llm": { ... },
  "mcp": { ... },
  "agent": { ... },
  "bash": { ... }
}
```

### `llm` — LLM backend settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `backend` | string | `"ollama"` | Backend: `ollama`, `anthropic`, `openai`, `gemini` |
| `model` | string | *(backend-specific)* | Model name |
| `base_url` | string | *(backend-specific)* | API base URL (Ollama and OpenAI/Azure only) |
| `thinking` | bool | `true` | Enable thinking mode when model supports it |
| `max_tokens` | int | `0` | Max response tokens; `0` = model default |

**Default model per backend:**

| Backend | Default model | Default base_url |
|---------|--------------|-----------------|
| `ollama` | `gpt-oss:latest` | `http://localhost:11434` |
| `anthropic` | `claude-sonnet-4-6` | *(Anthropic API)* |
| `openai` | `gpt-5.4-mini` | *(OpenAI API)* |
| `gemini` | `gemini-2.5-flash-lite` | *(Google API)* |

### `agent` — Agent behaviour

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_iterations` | int | `30` | Max ReAct loop iterations before giving up |
| `log_level` | string | `"info"` | Log level: `debug`, `info`, `warn`, `error` |

### `bash` — Bash tool settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `whitelisted_commands` | array | *(see below)* | Commands that run without approval prompt |

Default whitelisted commands:
```
go build, go test, go run, go mod tidy, go fmt, go vet
git status, git log, git diff
ls, pwd, cat, head, tail, grep, find, echo, which
make
npm install, npm run, npm test
```

### `mcp` — MCP server integration

| Field | Type | Description |
|-------|------|-------------|
| `servers` | array | List of MCP server configs |

**MCP server config object:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Identifier; also used as tool name prefix |
| `enabled` | bool | — | Enable/disable this server |
| `type` | string | yes | `stdio` or `sse` |
| `command` | string | stdio | Executable to launch |
| `args` | array | — | Command-line arguments |
| `env` | array | — | Extra environment variables |
| `url` | string | sse | HTTP/SSE endpoint URL |
| `allowed_tools` | array | — | Whitelist of tool names from this server |

### Example settings file

```json
{
  "llm": {
    "backend": "anthropic",
    "model": "claude-sonnet-4-6",
    "thinking": true,
    "max_tokens": 4096
  },
  "agent": {
    "max_iterations": 20
  },
  "bash": {
    "whitelisted_commands": ["go build", "go test", "git status", "make"]
  },
  "mcp": {
    "servers": [
      {
        "name": "godevmcp",
        "enabled": true,
        "type": "stdio",
        "command": "godevmcp",
        "allowed_tools": ["outline_go_package", "read_godoc"]
      }
    ]
  }
}
```

---

## 3. Permission Rules

Permission rules control which tool calls are automatically allowed or blocked, without prompting.

### Interactive approval dialog

When a destructive tool call (`Write`, `Edit`, `MultiEdit`, `Bash`) requires approval, klein shows:

```
> Proceed with this action?
  Yes
  Always (save to project)
  No
```

| Choice | Effect |
|--------|--------|
| **Yes** | Allow this one call |
| **Always (save to project)** | Append an allow rule to `.klein/permissions.json` (persists across sessions); also takes effect immediately for the rest of the current session |
| **No** | Cancel the tool call |

The pattern saved by "Always (save to project)" is inferred from the argument:
- File tools: first path segment + `/**` (e.g. `src/foo/bar.go` → `src/**`)
- Bash: first two words + ` *` (e.g. `go build ./...` → `go build *`)

### File locations (merged in priority order, lowest first)

| File | Scope | Notes |
|------|-------|-------|
| `~/.klein/permissions.json` | User-wide | Applied to all projects |
| `{workingDir}/.klein/permissions.json` | Project | Committable; shared with team |
| `{workingDir}/.klein/permissions.local.json` | Project-local | Add to `.gitignore` |

Rules from higher-priority files override lower-priority ones when patterns conflict.

### File format

```json
{
  "rules": [
    { "tool": "Write",  "pattern": "src/**",     "behavior": "allow" },
    { "tool": "Bash",   "pattern": "go build *", "behavior": "allow" },
    { "tool": "Bash",   "pattern": "rm *",       "behavior": "deny"  }
  ]
}
```

### Rule fields

| Field | Type | Description |
|-------|------|-------------|
| `tool` | string | Tool name in PascalCase (e.g. `Write`, `Bash`, `Edit`) |
| `pattern` | string | Pattern matched against the tool's primary argument (see below) |
| `behavior` | string | `"allow"` or `"deny"` |

### Pattern syntax

| Pattern | Matches |
|---------|---------|
| `""` (empty) | Every call to this tool (blanket allow/deny) |
| `"src/**"` | Path is `src` or starts with `src/` |
| `"*.go"` | Any `.go` file at root level (`filepath.Match` semantics) |
| `"go build *"` | Bash command starting with `go build ` (trailing `*` wildcard) |
| `"go test *"` | Bash command starting with `go test ` |

For `Write`/`Edit`/`MultiEdit`, the pattern is matched against the file path.
For `Bash`, the pattern is matched against the full command string.

### Hardcoded deny rules (cannot be overridden)

These are blocked unconditionally regardless of any allow rules:

| Pattern | Reason |
|---------|--------|
| `rm -rf /` | Filesystem wipe |
| `rm -rf /*` | Filesystem wipe |
| `:(){:\|:&};:` | Fork bomb |

### Injection detection (§6.4)

These shell constructs are always rejected before any rule check:

| Construct | Example |
|-----------|---------|
| `$()` command substitution | `git log --format=$(cat /etc/passwd)` |
| Backtick substitution | `` echo `whoami` `` |
| `${}` parameter expansion | `echo ${HOME}` |
| `<()` process substitution | `diff <(ls /a) <(ls /b)` |
| Heredoc (`N<<`) | `bash 0<<EOF` |

Error format: `SECURITY: Command blocked — contains <reason>.`

---

## 4. SKILL.md Frontmatter

Skills are YAML-frontmatter + markdown files. Klein searches in priority order (last wins):

1. Built-in skills embedded in the binary (`internal/skill/skills/*/SKILL.md`)
2. Project skills: `.claude/skills/{name}/SKILL.md`
3. Personal skills: `~/.claude/skills/{name}/SKILL.md`

### Frontmatter fields

```yaml
---
name: my-skill
description: What this skill does
allowed-tools: Read, Write, Bash, WebFetch
argument-hint: "Describe what you want"
user-invocable: true
model: claude-sonnet-4-6
disable-model-invocation: false
---
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | directory name | Skill identifier used with `--skill` flag |
| `description` | string | `""` | Shown in `/help` and skill listings |
| `allowed-tools` | string | `""` (all) | Comma-separated list of tool names this skill may use; empty = all tools |
| `argument-hint` | string | `""` | Usage hint displayed to the user |
| `user-invocable` | bool | `true` | Set `false` to hide from user listings (e.g. gateway-only skills) |
| `model` | string | `""` | Override model for this skill; empty = use settings default |
| `disable-model-invocation` | bool | `false` | Skip LLM call entirely (internal/testing use) |

### Template variables in skill content

| Variable | Replaced with |
|----------|--------------|
| `$ARGUMENTS` | Full user input string |
| `$0` … `$9` | Positional arguments parsed from input |
| `{{workingDir}}` | Absolute path of `--workdir` |

---

## 5. Gateway Configuration

The gateway (`klein-claw`) reads its config from `$HOME/.klein/claw/config.json` by default; override with `--config`.

### Top-level fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `agent_addr` | string | `"http://localhost:50051"` | klein Connect-gRPC server address |
| `working_dir` | string | — | Working directory passed to the agent |
| `default_skill` | string | `"claw"` | Skill used for incoming messages |
| `default_model` | string | `"claude-sonnet-4-6"` | LLM model |
| `max_iterations` | int | `15` | ReAct loop cap per message |
| `session_timeout` | string | `"30m"` | Inactivity timeout (Go duration, e.g. `"1h"`) |
| `sessions_dir` | string | `~/.klein/claw/sessions/` | Per-session persistence directory |

### `discord` block

| Field | Type | Description |
|-------|------|-------------|
| `token` | string | Discord bot token (or set `DISCORD_TOKEN` env var) |
| `allowed_guild_ids` | array | Guild IDs to respond in; empty = all |
| `allowed_channel_ids` | array | Channel IDs to respond in; empty = all |
| `allowed_user_ids` | array | User IDs allowed to interact; empty = all |
| `mention_only` | bool | Only respond when @mentioned in guild channels |

### `memory` block

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `base_dir` | string | `~/.klein/claw/memory/` | Memory storage directory |
| `max_notes` | int | `30` | Maximum recent daily notes to retain |

### `heartbeat` block

| Field | Type | Description |
|-------|------|-------------|
| `enabled` | bool | Enable periodic execution |
| `interval` | string | Go duration (e.g. `"24h"`, `"1h"`) |
| `prompt` | string | Prompt text to execute on each tick |
| `skill` | string | Skill to invoke |
| `channel_type` | string | Adapter type (e.g. `"discord"`) |
| `channel_id` | string | Target channel for the response |

### Example gateway config

```json
{
  "agent_addr": "http://localhost:50051",
  "working_dir": "/Users/you/projects/myapp",
  "default_skill": "claw",
  "default_model": "claude-sonnet-4-6",
  "max_iterations": 15,
  "session_timeout": "30m",
  "discord": {
    "token": "BOT_TOKEN_HERE",
    "allowed_guild_ids": ["123456789"],
    "allowed_channel_ids": ["987654321"],
    "mention_only": true
  },
  "memory": {
    "base_dir": "/Users/you/.klein/claw/memory",
    "max_notes": 30
  },
  "heartbeat": {
    "enabled": true,
    "interval": "24h",
    "prompt": "Review MEMORY.md and write today's daily note.",
    "skill": "claw",
    "channel_type": "discord",
    "channel_id": "987654321"
  }
}
```

---

## 6. Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `ANTHROPIC_API_KEY` | If `backend=anthropic` | Anthropic API key |
| `OPENAI_API_KEY` | If `backend=openai` | OpenAI API key |
| `GEMINI_API_KEY` | If `backend=gemini` | Google Gemini API key |
| `OLLAMA_HOST` | No | Ollama server (default: `http://localhost:11434`) |
| `DISCORD_TOKEN` | If Discord enabled | Discord bot token (alternative to `discord.token` in config) |

---

## 7. User Data Directories

All persistent data lives under `$HOME/.klein/` (interactive mode only; one-shot mode is memory-only).

```
$HOME/.klein/
├── settings.json                        # Default settings (see §2)
├── permissions.json                     # User-wide permission rules (see §3)
├── projects/
│   └── {project-basename}-{hash}/      # One directory per project
│       ├── project_info.txt            # Project path and metadata
│       ├── todos.json                  # Todo list
│       ├── tasks.json                  # Task list
│       ├── session.json                # Conversation history
│       └── history.txt                 # Readline command history
└── claw/
    ├── config.json                      # Gateway config (see §5)
    ├── sessions/                        # Per-session Connect-gRPC state
    └── memory/
        ├── MEMORY.md                    # Long-term memory (manually curated)
        └── daily/
            └── YYYY-MM-DD.md           # Daily journal notes
```

**Project directory naming:** `{basename}-{8-char hash of absolute path}`
Example: `/Users/you/dev/my-app` → `my-app-a1b2c3d4/`

### Per-project permission files

```
{workingDir}/
└── .klein/
    ├── permissions.json          # Committable project rules
    └── permissions.local.json    # Local-only rules (add to .gitignore)
```

---

## Quick Reference

| Need | Where |
|------|-------|
| Change LLM model | `--model` flag or `llm.model` in settings JSON |
| Pre-approve a file path | `.klein/permissions.json` → `allow` rule for `Write`/`Edit` |
| Block a command | `.klein/permissions.json` → `deny` rule for `Bash` |
| Limit tools for a skill | `allowed-tools` in `SKILL.md` frontmatter |
| Add a safe bash command | `bash.whitelisted_commands` in settings JSON |
| Increase iteration limit | `agent.max_iterations` in settings JSON |
| Use a custom Ollama server | `OLLAMA_HOST` env var or `llm.base_url` in settings JSON |
