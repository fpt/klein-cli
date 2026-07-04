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
| `--sessions-dir` | string | `""` | Directory for session persistence (default: `<base_dir>/sessions/`) |
| `--memory-dir` | string | `""` | Directory for `MemorySearch`/`MemoryGet` tools (default: `<base_dir>/memory/`) |
| `--schedules-file` | string | `""` | Schedule store for the `Schedule*` tools (default: `<base_dir>/schedules.json`) |

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
  "bash": { ... },
  "base_dir": "~/.klein",
  "claw": { ... }
}
```

### `base_dir` — shared state root

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `base_dir` | string | `~/.klein` | Root for shared per-user state — `sessions/`, `memory/`, `schedules.json`. Env-expanded. Used by both the CLI (serve mode) and `klein claw`. Point `--settings` at a file with a different `base_dir` to run a fully isolated gateway instance (see [§5](#5-gateway-configuration-klein-claw)). |

### `claw` — gateway configuration

The `claw` object configures the `klein claw` gateway; see
[§5](#5-gateway-configuration-klein-claw).

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

`mcp` is a **map of server name → config**, matching the Claude Code / Cursor
format (paste a `mcpServers` block's contents under `mcp`):

```json
"mcp": {
  "browser-sandbox": { "command": "docker", "args": ["run", "-i", "--rm", "chromedp-container-mcp:latest"] },
  "docs":            { "url": "https://example.com/mcp" }
}
```

**Per-server fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `command` | string | stdio | Executable to launch (its presence infers `type: stdio`) |
| `args` | array | — | Command-line arguments |
| `env` | object | — | Environment variables `{ "KEY": "VAL" }` |
| `url` | string | sse | HTTP/SSE endpoint URL (its presence infers `type: sse`) |
| `type` | string | — | `stdio` or `sse`; inferred from `command`/`url` when omitted |
| `enabled` | bool | — | Defaults to **true**; set `false` to keep but disable |
| `allowed_tools` | array | — | Whitelist of tool names from this server |

The server name is the map key (also the tool-name prefix). Add/list/remove
servers by hand or with the **`klein mcp`** subcommand (Claude-Code-style):

```bash
klein mcp add browser-sandbox -- docker run -i --rm --init --shm-size 1g chromedp-container-mcp:latest
klein mcp add docs --url https://example.com/mcp
klein mcp add x -e API_KEY=secret -- my-mcp-server
klein mcp list
klein mcp remove browser-sandbox
```

`add` edits `~/.klein/settings.json`; everything after `--` is the stdio command
and its args; `--url` makes an sse server; `-e KEY=VAL` adds env vars.

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
    "godevmcp": {
      "command": "godevmcp",
      "allowed_tools": ["outline_go_package", "read_godoc"]
    }
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
| `allowed-tools` | string | `""` | Tools loaded **up front**. Every skill also gets **`ToolSearch`**: any tool not listed (incl. MCP tools, which can't be enumerated) stays deferred and the model loads it on demand. Omit `allowed-tools` to start from a small default core (Read/LS/Glob/Grep/Write/Edit/Bash/TodoWrite) and search for the rest. (The CLI `--allowed-tools` flag is a hard restriction — no ToolSearch.) |
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

## 5. Gateway Configuration (`klein claw`)

The gateway is the `klein claw` subcommand (the former standalone `klein-claw`
binary is gone). Its configuration is the **`claw` section of `settings.json`**;
run it with `klein claw` (add `--settings <path>` to select a different file).

By default `klein claw` starts an **embedded, in-process agent server** on an
ephemeral loopback port — no separate `klein --serve` needed. Set `agent_addr`
(or pass `--agent-addr`) to dial a remote agent instead.

### Shared paths — `base_dir`

Sessions, memory, and the schedule store are **not** configured in the `claw`
block. They derive from the top-level **`base_dir`** (default `~/.klein`, shared
with the CLI), so the agent's `Schedule*` tools and the scheduler always agree on
files, and the `[SESSION LOG]` path the gateway injects matches where the agent
persists:

| Path | Derived from |
|------|--------------|
| Sessions | `<base_dir>/sessions/` |
| Memory (`MEMORY.md`, `daily/`, `runs/`) | `<base_dir>/memory/` |
| Schedule store | `<base_dir>/schedules.json` |

**Multiple instances:** give each a settings file with its own `base_dir` and
Discord token — everything else isolates automatically (the embedded server's
ephemeral port avoids collisions):

```bash
klein claw                                    # default instance (~/.klein)
klein claw --settings ~/work/settings.json    # isolated instance (its own base_dir)
```

**Migration:** `klein claw migrate` folds a legacy `~/.klein/claw/config.json`
into the `claw` section of `settings.json` and moves its
`memory`/`sessions`/`schedules.json` into `base_dir`.

### `claw` block fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `agent_addr` | string | `""` (embedded) | Empty = start an embedded in-process agent server; set to dial a remote `klein --serve` |
| `working_dir` | string | — | Working directory passed to the agent |
| `default_skill` | string | `"claw"` | Skill used for incoming messages |
| `session_timeout` | string | `"30m"` | Inactivity timeout (Go duration, e.g. `"1h"`) |

> The LLM **model** and **max_iterations** are owned by the agent via the same
> `settings.json` (`llm.model`, `agent.max_iterations`) — the `claw` block does
> not set them.

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
| `max_notes` | int | `30` | Maximum recent daily notes to retain |

> The memory directory is **not** set here — it is `<base_dir>/memory/`.
> Only `max_notes` is read from this block.

### `schedules` block (and the dynamic store)

`schedules` is an array of recurring jobs. Timing is a standard 5-field
**cron expression** evaluated in a required **timezone** (the legacy
`at`/`interval` fields are retired — `"08:00 daily"` = `"0 8 * * *"`,
`"every 6h"` = `"0 */6 * * *"`). Agent-created schedules (via `ScheduleCreate`)
are stored in `<base_dir>/schedules.json` and merged with these at runtime; the
scheduler live-reloads that file, so no restart is needed.

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Unique id (used for logs + reconciliation; reused name = update) |
| `enabled` | bool | Whether the job runs |
| `cron` | string | **Required.** Standard 5-field cron in `timezone`, e.g. `"0 8 * * 1-5"` = weekdays 08:00, `"0 */6 * * *"` = every 6h |
| `timezone` | string | **Required.** IANA tz the cron is evaluated in (e.g. `"Asia/Tokyo"`) — required so schedules never silently depend on server-local time |
| `prompt` | string | The task **executed** at fire time by a headless agent. Write it as the work itself (`今朝の主要イベントをまとめて`), never as a scheduling request (`毎朝8時に…送って` — `ScheduleCreate` rejects such prompts). |
| `skill` | string | Skill for the run. Use `"report"` (headless deliverable generator) for briefings; conversational skills tend to ask questions no one will answer. |
| `silent` | bool | Run but never post the response (data-collection jobs) |
| `channel_type` / `channel_id` | string | Output channel (required unless silent) |
| `run_at_start` | bool | Fire once immediately when (re)started |

At fire time the gateway prepends a `[SCHEDULED RUN]` block (schedule name +
channel) telling the agent it is an automated run with no user present, so it
executes the task instead of conversing.

Every scheduled run's output (including silent runs) is also appended to a
daily **run log** at `<base_dir>/memory/runs/YYYY-MM-DD.md`, timestamped with
the schedule name. Later jobs can read it via `MemoryGet path=runs/<date>.md`
or `MemorySearch` — e.g. a nightly memory job that distills the day's cron
outputs (market reports, etc.) into daily notes and MEMORY.md:

```json
{
  "name": "nightly-memory",
  "enabled": true,
  "cron": "0 22 * * *",
  "timezone": "Asia/Tokyo",
  "skill": "claw",
  "prompt": "今日の runs/ ログ（MemoryGet path=runs/今日の日付.md）とMEMORY.md・daily ノートをレビューし、残す価値のある発見（ウォッチ銘柄に関わる市場の動きなど）を今日の daily ノートに要約して保存して。レポートの丸写しはしないこと。",
  "silent": true
}
```

> The legacy single-job `heartbeat` block is **retired**. A leftover
> `heartbeat` key in an old config.json is ignored (unknown JSON fields don't
> error) — move the job into `schedules` with a cron expression instead
> (`"interval": "24h"` anchored to gateway start becomes e.g.
> `"cron": "45 23 * * *"` at a real wall-clock time).

### Example `settings.json` with a `claw` section

```json
{
  "llm": { "backend": "anthropic", "model": "claude-sonnet-4-6" },
  "agent": { "max_iterations": 30 },
  "base_dir": "~/.klein",
  "claw": {
    "default_skill": "claw",
    "session_timeout": "30m",
    "discord": {
      "token": "BOT_TOKEN_HERE",
      "allowed_guild_ids": ["123456789"],
      "allowed_channel_ids": ["987654321"],
      "mention_only": true
    },
    "memory": { "max_notes": 30 },
    "schedules": [
      {
        "name": "nightly-memory",
        "enabled": true,
        "cron": "45 23 * * *",
        "timezone": "Asia/Tokyo",
        "skill": "claw",
        "silent": true,
        "channel_type": "discord",
        "channel_id": "987654321",
        "prompt": "今日の runs/ ログと MEMORY.md・daily ノートをレビューし、残す価値のある発見を今日の daily ノートに要約して保存して。"
      }
    ]
  }
}
```

Run it with `klein claw` (embedded agent) — no separate server process needed.

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
