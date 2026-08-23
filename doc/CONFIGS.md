# klein-cli Configuration Reference

All configuration mechanisms in one place — CLI flags, settings files, permission rules, SKILL.md frontmatter, and environment variables.

---

## Table of Contents

1. [CLI Flags](#1-cli-flags)
2. [Settings TOML](#2-settings-toml)
3. [Permission Rules](#3-permission-rules)
4. [Roles and Skills](#4-roles-and-skills)
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
| `-b`, `--backend` | string | `""` | LLM backend: `openai`, `anthropic`, `gemini`, `codex`, `appserver` |
| `-m`, `--model` | string | `""` | Model name (overrides settings file) |
| `-r`, `--role` | string | `"code"` | Role (startup prompt) to open the session with: `code`, `cad`, `claw`, `review`. Naming a *skill* is rejected — see [§4](#4-roles-and-skills) |
| `--workdir` | string | `"."` | Working directory for all file operations |
| `--settings` | string | `""` | Path to a settings file (see [§2](#2-settings-toml)). Parsed as TOML whatever the file is named — the extension carries no meaning, and a `.json` passed here fails to parse rather than falling back. |
| `--allowed-tools` | string | `""` | Comma-separated tool names, overrides skill's `allowed-tools` |
| `-f` | string | `""` | File of multi-turn prompts separated by `---` |
| `-v`, `--verbose` | bool | `false` | Enable debug-level logging |
| `-c`, `--continue` | bool | `false` | Resume this project's most recently used session. Without it, interactive mode starts a **fresh** session (see [§7](#7-user-data-directories)) |
| `-l`, `--log` | bool | `false` | Print conversation history and exit (implies `--continue` — the history it prints is the session `--continue` would resume) |
| `--serve` | bool | `false` | Start Connect-gRPC server (for gateway) |
| `--serve-addr` | string | `":50051"` | Listen address for Connect server |
| `--sessions-dir` | string | `""` | Directory for session persistence (default: `<base_dir>/sessions/`) |
| `--memory-dir` | string | `""` | Directory for `MemorySearch`/`MemoryGet` tools (default: `<base_dir>/memory/`) |
| `--schedules-file` | string | `""` | Schedule store for the `Schedule*` tools (default: `<base_dir>/schedules.json`) |

---

## 2. Settings TOML

Settings are loaded from the first file found in order:

1. Path given by `--settings` flag
2. `{workingDir}/.agents/settings.toml`
3. `$HOME/.klein/settings.toml`

A `settings.json` from an older klein is **not** read. klein warns once when it
finds one instead of silently scaffolding a default over your configuration, but
porting it is manual — the shapes below are what it should become.

Only the `[mcp.*]` tables are ever written by klein (`klein mcp add/remove`), and
that edit splices the file in place: comments, ordering and spacing everywhere
else survive it.

### Full structure

```toml
base_dir = "~/.klein"        # top-level keys first — anything after a
                             # [table] header belongs to that table

[llm]      # …
[mcp.NAME] # one table per MCP server
[agent]    # …
[bash]     # …
[claw]     # gateway; see §5
```

> **TOML ordering gotcha.** Bare keys like `base_dir` must appear *before* the
> first `[table]` header. After `[llm]`, every key belongs to `llm` until the
> next header — so a `base_dir` written at the bottom of the file silently
> becomes `llm.base_dir` and is ignored.

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
| `backend` | string | `"openai"` | Backend: `openai`, `anthropic`, `gemini`, `codex`, `appserver` |
| `model` | string | *(backend-specific)* | Model name |
| `base_url` | string | *(backend-specific)* | API base URL (OpenAI/Azure-compatible only) |
| `thinking` | bool | `true` | Enable thinking mode when model supports it |
| `max_tokens` | int | `0` | Max response tokens; `0` = model default |

**Default model per backend:**

| Backend | Default model | Default base_url |
|---------|--------------|-----------------|
| `anthropic` | `claude-sonnet-4-6` | *(Anthropic API)* |
| `openai` | `gpt-5.6-luna` | *(OpenAI API)* |
| `gemini` | `gemini-2.5-flash-lite` | *(Google API)* |
| `codex` | *(codex-owned)* | *(codex app-server)* |
| `appserver` | *(server-owned)* | *(the app-server)* |

### `codex` — codex app-server backend

Used only when `llm.backend == "codex"`. Codex is a **whole-agent** backend: it
runs its own reasoning + tool loop (shell, `apply_patch`, and MCP servers). klein
routes each conversation turn to a codex thread and takes back the final answer,
while still providing the frontend (repl/claw), memory-context injection,
session↔thread mapping, and run-logs. Requires the **`codex` binary on `PATH`**
(auth/model come from the codex CLI's own config; `llm.model`/`llm.effort` map
onto the thread when set). klein's configured external MCP servers are passed
through to codex, and klein's **native tools (memory + schedule) are registered
with codex as experimental `dynamicTools`** — codex calls back to klein
in-process over the app-server JSON-RPC connection, hitting the same live tool
managers (so a codex turn can read/curate memory and manage schedules). This
needs a codex build with `dynamicTools`/`experimentalApi` support.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `codex_path` | string | `codex` (PATH) | Path to the codex binary |
| `approval_policy` | string | *(mode-dependent)* | `never` / `on-request` / `untrusted` / `granular`. **Default depends on the surface**: the interactive repl (`klein claw repl`, `klein -b codex`) uses `on-request` and prompts you (y/N) before codex runs a command or edits files; headless surfaces (the `klein claw` gateway, `--serve`, one-shot) use `never` and auto-approve since no one is present. Set this explicitly to override the mode default. |
| `sandbox_mode` | string | `workspace-write` | `read-only` / `workspace-write` / `danger-full-access` |

```toml
[llm]
backend = "codex"
model   = "gpt-5.6-luna"
effort  = "medium"

[codex]
sandbox_mode = "workspace-write"
```

### `appserver` — generic app-server backend

Used only when `llm.backend == "appserver"`. Like codex, such an agent is a
**whole-agent** backend driven over the same app-server JSON-RPC protocol
(`pkg/agentserver` serves both): klein routes each turn to a thread on the
server and takes back the final answer, while klein keeps the frontend,
memory-context injection, and session↔thread mapping.

`appserver` names a **protocol, not a program**. Any local agent implementing the
subset klein uses — `initialize` with the `experimentalApi` capability,
`thread/start`, `turn/start`, and `dynamicTools` — can be plugged in. Because
there is no single "the" app-server binary, **`appserver.command` is required**;
klein does not guess a default and will refuse to start without it. (The one
alternative is `appserver.address`, which dials a server somebody else started —
see [below](#reaching-a-server-on-another-machine).) The reference
implementation is [rs-gallium](https://github.com/fpt/rs-gallium), whose binary is
`gallium`.

> **On the name.** This backend was called `acp` until 2026-07-25. That was
> wrong: "ACP" also names the [agentclientprotocol.com](https://agentclientprotocol.com)
> standard (`session/new`, `session/prompt`, `session/update`), which klein does
> **not** speak and which rs-gallium has ruled out
> ([fpt/rs-gallium#15](https://github.com/fpt/rs-gallium/issues/15), reaffirmed in
> [#13](https://github.com/fpt/rs-gallium/issues/13)). What klein speaks is the
> **codex-app-server** protocol, hence the name. `"backend": "acp"` is now
> rejected at startup with a message pointing here — rename it and the `acp`
> settings block to `appserver`.

The server owns its own model and credentials — it reads `MODEL_PATH` (a local
GGUF) or `OPENAI_API_KEY` from the environment it is spawned in, so `llm.model` is
optional. klein's native tools (memory + schedule) are registered as
`dynamicTools` exactly as for codex, and the server calls back in-process over the
same connection. klein's external MCP servers are passed through; the server's own
`read`/`glob`/`grep`/`write`/`edit`/`bash` tools cover filesystem and shell.

Two differences from codex worth knowing:

- **No sandbox.** `codex.sandbox_mode` has no equivalent here, so
  `approval_policy` is the only gate on filesystem and shell mutations.
- **No login.** Such a server has nothing to authenticate against beyond the API
  key or model path in its own environment.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `command` | string | *(none — **required**, unless `address` is set)* | The app-server binary, e.g. `gallium` or an absolute path |
| `address` | string | *(none)* | `host:port` of an app-server **already running** — klein dials it instead of spawning one. Mutually exclusive with `command`/`args`/`env`. See [Reaching a server on another machine](#reaching-a-server-on-another-machine) |
| `args` | string[] | `["app-server"]` | Subcommand that puts the binary into app-server mode |
| `env` | object | — | Environment variables `{ "KEY": "VAL" }` passed to the server. See below. |
| `workspace_tools` | bool | *(on when dialing, off when spawning)* | Offer klein's own `Read`/`Write`/`Edit`/`MultiEdit`/`LS`/`Glob`/`Grep`/`Bash` to the server as dynamic tools, so they run **here**. See [Whose tools run](#whose-tools-run) |
| `approval_policy` | string | *(mode-dependent)* | `never` / `on-request`. Same mode defaults as codex: the interactive repl prompts you (y/N) before the server writes a file or runs a command; headless surfaces auto-approve. Set explicitly to override. |

> `config` is **deprecated** and undocumented here — it still parses and still
> works, and setting it logs a warning naming its replacement. Use `env`, or hand
> the file to the server itself via `args`.

#### Configuring the server

An app-server is configured **entirely by environment variables**, which keeps
klein in control of what reaches the child. `appserver.env` is a sub-table of
whatever the server reads:

```toml
[llm]
backend = "appserver"

[appserver]
command         = "../rs-gallium/target/release/gallium"
approval_policy = "on-request"

# Passed to the child verbatim
[appserver.env]
MODEL_PATH         = "/models/qwen3.6.gguf"
GALLIUM_CPU_MOE    = "1"
GALLIUM_GPU_LAYERS = "20"
```

klein does **not** validate these keys, and that is deliberate: `appserver` names
a protocol, not a program, so klein is not the authority on any one server's
option set — which is what lets a server option klein has never heard of work the
day the server ships it. The cost is that a typo is silently ignored rather than
rejected. Values here override the ambient shell; anything not named is inherited.

Nor are values resolved: a relative path in `env` is left exactly as written, and
the server will read it relative to **its own working directory**, not to your
settings file.

If the server reads a config file of its own, `args` hands it over directly —
usually the better option, since the server then resolves its own keys and its
own relative paths, and klein is not in the middle:

```toml
[appserver]
command = "gallium"
args    = ["app-server", "--config", "../rs-gallium/configs/qwen3.6-cuda-12gb.toml"]
```

Without any of this, the server simply inherits klein's environment:

```toml
[llm]
backend = "appserver"

[appserver]
command         = "gallium"
approval_policy = "on-request"
```

#### Whose tools run

Whether the agent's hands are on klein's machine or on the server's follows how
the server is reached, because the server behaves differently in the two cases.

**Dialed (`address`): klein's tools, and only klein's.** A listening rs-gallium
lends no filesystem or shell tools at all. That is a privilege decision, not a
setting: its built-ins would run as the user *gallium* was started as, the socket
carries no authentication or identity, and a server that handed a connecting
client a `Bash` with someone else's rights could not be reasoned about by any
approval policy. Loopback is no exception — same machine is not the same user. So
klein registers `Read`, `Write`, `Edit`, `MultiEdit`, `LS`, `Glob`, `Grep` and
`Bash` as dynamic tools on every thread, and they run in klein's process against
klein's working directory. This is the default when `address` is set; setting
`workspace_tools = false` alongside it is refused at startup, because nothing on
the other end would cover for it.

```bash
# on the GPU box — nothing to configure but the address
GALLIUM_LISTEN=127.0.0.1:47821 gallium app-server --config configs/qwen3.8.toml
```

```toml
# on your laptop
[appserver]
address         = "127.0.0.1:47821"
approval_policy = "on-request"
```

The model then reasons on the GPU box while every read, write and command happens
where you are.

**Spawned (`command`): the server's own tools, unless you say otherwise.** That
server is klein's child, running with klein's privileges, and its tools already
act on the right machine. `workspace_tools = true` substitutes klein's for them —
a server resolving a call to the first exact name match runs klein's `Read` in
place of the one it shipped with. Worth doing when klein's boundaries are the
ones that should apply; otherwise it only moves the work.

Either way, what klein's tools bring with them:

- **klein's boundaries.** The working-directory allowlist, the blacklist of
  secrets, and read-before-write are enforced here, whatever the server would
  have allowed. `Glob` and `Grep` are bounded by the same allowlist as `Read` —
  searching is reading.
- **Approvals.** With the server holding the tools, it asks permission and klein
  answers. With klein holding them, nobody would ask — so klein prompts for
  itself before `Write`/`Edit`/`MultiEdit`/`Bash`, on the same `approval_policy`,
  and `auto_approve_commands` still answers for the commands it covers.
  Read-only tools are not prompted, matching klein's own loop.
- **Your files.** A remote agent edits the checkout on your laptop rather than
  one on the GPU box.

It applies to the `appserver` backend only. codex brings its own shell and
`apply_patch`, which klein has no business shadowing.

**MCP servers are still the backend's**, not klein's. `[mcp.*]` servers are
passed to the server as configuration, so it launches them on **its** machine —
which is what you want for a server klein spawned, and probably not what you want
for one on a GPU box. Moving them client-side is not implemented yet; until it
is, an MCP server a dialed backend needs has to exist on the backend's machine.

> **Gotcha for spawned gallium.** `gallium app-server` with no `--config` reads
> `~/.config/gallium/config.toml`, so if yours sets `[agent] listen` a gallium
> klein *spawned* will open a socket and never speak stdio — klein then waits for
> a reply that is not coming. Either hand the server a config explicitly
> (`args = ["app-server", "--config", "…"]`) or put `GALLIUM_LISTEN = ""` in
> `[appserver.env]`, which recent gallium reads as "stdio, whatever the config
> says".

#### Reaching a server on another machine

`appserver.address` dials an app-server that is already listening instead of
spawning one, so the agent can run where the GPU is while klein stays on your
laptop. The protocol is identical over the wire — including the direction that
runs backwards, where the server calls back for klein's tools, which then run
**locally**, against your files.

On the server (rs-gallium spells its listen address `GALLIUM_LISTEN`, or
`[agent] listen` in its own TOML):

```bash
GALLIUM_LISTEN=127.0.0.1:47821 gallium app-server --config configs/qwen3.8.toml
```

On the client:

```toml
[llm]
backend = "appserver"

[appserver]
address         = "127.0.0.1:47821"   # e.g. the local end of an SSH tunnel
approval_policy = "on-request"
```

> **This connection is unauthenticated and unencrypted.** Anything that can reach
> that port drives an agent with that user's privileges on that machine. Bind it
> to loopback and reach it through an SSH tunnel
> (`ssh -N -L 47821:127.0.0.1:47821 gpubox`), or use a Tailscale/WireGuard
> address where the overlay does the authenticating. klein gives `address` no
> default for this reason.

`command`, `args`, `env` and `config` configure a server klein **starts**, so
setting any of them alongside `address` is rejected rather than ignored: that
process was configured wherever it runs, and silently dropping a `MODEL_PATH`
would leave you believing you had chosen a model.

Two behaviours are worth knowing:

- **Being displaced is normal.** A server like gallium serves one client at a
  time and prefers the newest, closing the older connection — which is what keeps
  a laptop that slept from locking you out of your own box. klein reports that as
  the session being taken over, not as the backend dying.
- **Reconnects happen on your next turn.** klein redials when you ask for
  something, never in the background (two idle klein instances would otherwise
  take the session from each other forever). Thread ids do not survive the
  reconnect — they belong to the connection — so that turn starts a fresh thread
  on the server. Your conversation history is klein's and is unaffected; what is
  lost is the server-side context of the old thread.

### `agent` — Agent behaviour

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_iterations` | int | `30` | Max ReAct loop iterations before giving up |
| `log_level` | string | `"info"` | Log level: `debug`, `info`, `warn`, `error` |
| `max_tool_result_runes` | int | `16000` | Inline budget for one tool result; longer results are offloaded to a file |

**`max_tool_result_runes`** keeps a single tool result from consuming the whole
context window. A result over the budget is written to
`~/.klein/projects/<project>/tool_results/<tool-use-id>.txt` and replaced in the
conversation by a stub naming that path plus a short preview; the directory is on
the filesystem allowlist, so the agent can `Read` it back (and page it with
`offset`/`limit`). The budget counts **runes, not bytes**, so CJK text gets the
same effective room as ASCII. Interactive mode only — one-shot runs keep every
result inline and in memory. `0` selects the default.

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
shape — one table per server, keyed by name:

```toml
[mcp.browser-sandbox]
command = "docker"
args    = ["run", "-i", "--rm", "chromedp-container-mcp:latest"]

[mcp.docs]
url = "https://example.com/mcp"

# env is a sub-table of the server it belongs to
[mcp.browser-sandbox.env]
API_KEY = "…"
```

A Claude Code `mcpServers` entry translates key-for-key: the JSON object
`"docs": { "url": … }` becomes the table `[mcp.docs]`.

**Per-server fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `command` | string | stdio | Executable to launch (its presence infers `type: stdio`) |
| `args` | array | — | Command-line arguments |
| `env` | object | — | Environment variables `{ "KEY": "VAL" }` |
| `url` | string | sse | HTTP/SSE endpoint URL (its presence infers `type: sse`) |
| `type` | string | — | `stdio`, `sse`, or `http`. Inferred from `command`/`url` when omitted — note a bare `url` infers **`sse`**, so a streamable-HTTP server needs `"type": "http"` written out |
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
klein mcp --settings ./team-settings.toml add docs --url https://example.com/mcp
```

`add` edits `~/.klein/settings.toml` — or the file named by `--settings` — by
splicing just that server's table, so comments, key order and spacing everywhere
else in the file survive it. Everything after `--` is the stdio command and its
args; `--url` makes an sse server; `-e KEY=VAL` adds env vars.

**CAD servers for the `cad` role.** The `cad` role does not hard-code any MCP
tool names — it discovers whatever is connected via `ToolSearch`, so it works
before you configure anything and picks servers up once you do. Autodesk's
[Fusion MCP server](https://blog.autodesk.io/fusion-mcp-server/) is streamable
HTTP on port 27182:

```toml
[mcp.fusion]
type = "http"
url  = "http://127.0.0.1:27182/mcp"
```

Two things to know about it: `"type": "http"` must be explicit (a bare `url`
infers `sse`), and the server only answers **while Fusion is running** — it
binds loopback, so reaching an instance on another machine needs an SSH tunnel
or port-forward to that host.

### Example settings file

```toml
[llm]
backend    = "anthropic"
model      = "claude-sonnet-4-6"
thinking   = true
max_tokens = 4096

[agent]
max_iterations = 20

[bash]
whitelisted_commands = ["go build", "go test", "git status", "make"]

[mcp.godevmcp]
command       = "godevmcp"
allowed_tools = ["outline_go_package", "read_godoc"]
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

## 4. Roles and Skills

Klein has two kinds of prompt, in the same format but with different jobs.

**A role is a startup prompt.** It is chosen once, when a session opens, and
never switched — it gives the session its identity. Roles are selected with
`-r`/`--role`, or fixed by an entry point (`klein claw` always opens `claw`;
`klein review` always opens `review`).

**A skill is a task capability**, reached from *inside* a session: the model
loads one with `ReadSkill` as it works, the gateway runs one for a single
message with `/<skill>`, and `schedules[].skill` picks one for a scheduled turn.

| | Role | Skill |
|---|---|---|
| File | `roles/{name}/ROLE.md` | `skills/{name}/SKILL.md` |
| Built-in | `code`, `cad`, `claw`, `review` | `pdf`, `github`, `web`, `report`, `research-stock`, `market-narratives`, `create-skill` |
| Chosen | Once, at startup | Any time, per turn |
| Selected by | `-r`, `klein claw`, `klein review` | `ReadSkill`, `/<skill>`, `schedules[].skill` |
| Listed in `/list` | No | Yes |

Passing a skill to `-r` is an error, since a skill was never written to open a
session:

```
$ klein -r pdf
Error: "pdf" is a skill, not a role — roles start a session, skills are used
within one (roles: cad, claw, code, review)
```

Both kinds are searched in the same priority order (last wins), with `roles` or
`skills` as the directory name:

1. Built-in, embedded in the binary (`internal/skill/roles/*/ROLE.md`, `internal/skill/skills/*/SKILL.md`)
2. `~/.claude/{roles,skills}/` → `~/.agents/…` → `~/.klein/…` (personal)
3. `.claude/{roles,skills}/` → `.agents/…` (project — highest)

So a project `.claude/roles/code/ROLE.md` replaces the built-in `code` role.

### Frontmatter fields

Roles and skills share this format; a `ROLE.md` simply lives under `roles/`.

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
| `name` | string | directory name | Identifier: the name a role is selected by (`-r`), or a skill is invoked by |
| `description` | string | `""` | Shown in `/help` and skill listings |
| `allowed-tools` | string | `""` | Tools loaded **up front**. Every skill also gets **`ToolSearch`**: any tool not listed (incl. MCP tools, which can't be enumerated) stays deferred and the model loads it on demand. Omit `allowed-tools` to start from a small default core (Read/LS/Glob/Grep/Write/Edit/Bash/TodoWrite) and search for the rest. (The CLI `--allowed-tools` flag is a hard restriction — no ToolSearch.) |
| `argument-hint` | string | `""` | Usage hint displayed to the user |
| `user-invocable` | bool | `true` | Skills only: set `false` to hide from `/list`. Roles are never listed there |
| `model` | string | `""` | Override model for this role/skill; empty = use settings default |
| `disable-model-invocation` | bool | `false` | Skip LLM call entirely (internal/testing use) |

### Template variables in role/skill content

| Variable | Replaced with |
|----------|--------------|
| `$ARGUMENTS` | Full user input string |
| `$0` … `$9` | Positional arguments parsed from input |
| `{{workingDir}}` | Absolute path of `--workdir` |

---

## 5. Gateway Configuration (`klein claw`)

The gateway is the `klein claw` subcommand, not a binary of its own. Its
configuration is the **`[claw]` section of `settings.toml`**;
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
klein claw --settings ~/work/settings.toml    # isolated instance (its own base_dir)
```

### Interactive CLI — `klein claw repl`

`klein claw repl` opens an interactive terminal chat that shares claw's tools
(memory, schedules, MCP) and backend (LLM + `base_dir`) but keeps its **own
session** (message history separate from Discord peers and scheduled runs). It's
the local frontend for inspecting and curating memory and schedules — e.g. "list
my schedules", "add a weekday 8am market briefing", "what do you remember about
me". It does **not** start Discord or the scheduler; because state is file-backed
under `base_dir`, a schedule it creates lands in `<base_dir>/schedules.json`,
which a running `klein claw` gateway live-reloads.

```bash
klein claw repl                                  # default instance
klein claw repl --settings ~/work/settings.toml  # a specific instance's tools/data
klein claw repl --role code                       # override the session role (default: claw)
```

### `claw` block fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `agent_addr` | string | `""` (embedded) | Empty = start an embedded in-process agent server; set to dial a remote `klein --serve` |
| `working_dir` | string | — | Working directory passed to the agent |
| `session_timeout` | string | `"30m"` | Inactivity timeout (Go duration, e.g. `"1h"`) |

> The LLM **model** and **max_iterations** are owned by the agent via the same
> `settings.toml` (`llm.model`, `agent.max_iterations`) — the `claw` block does
> not set them.

### `discord` block

| Field | Type | Description |
|-------|------|-------------|
| `token` | string | Discord bot token |
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

```toml
[[claw.schedules]]
name     = "nightly-memory"
enabled  = true
cron     = "0 22 * * *"
timezone = "Asia/Tokyo"
skill    = "claw"
silent   = true
prompt   = "今日の runs/ ログ（MemoryGet path=runs/今日の日付.md）とMEMORY.md・daily ノートをレビューし、残す価値のある発見（ウォッチ銘柄に関わる市場の動きなど）を今日の daily ノートに要約して保存して。レポートの丸写しはしないこと。"
```

> **The dynamic store is still JSON.** `<base_dir>/schedules.json` — what
> `ScheduleCreate` writes and the scheduler live-reloads — keeps the same field
> names in JSON. It is a queue the agent maintains, not a file anyone hand-edits,
> so it gained nothing from the move to TOML.

> The legacy single-job `heartbeat` block is **retired**. A leftover
> `[claw.heartbeat]` table is ignored (unknown keys don't error) — move the job
> into `schedules` with a cron expression instead (`interval = "24h"` anchored to
> gateway start becomes e.g. `cron = "45 23 * * *"` at a real wall-clock time).

### Example `settings.toml` with a `claw` section

```toml
base_dir = "~/.klein"

[llm]
backend = "anthropic"
model   = "claude-sonnet-4-6"

[agent]
max_iterations = 30

[claw]
session_timeout = "30m"

[claw.discord]
token               = "BOT_TOKEN_HERE"
allowed_guild_ids   = ["123456789"]
allowed_channel_ids = ["987654321"]
mention_only        = true

[claw.memory]
max_notes = 30

# One [[claw.schedules]] block per job — the double brackets make it an array,
# so repeat the whole block to add a second schedule.
[[claw.schedules]]
name         = "nightly-memory"
enabled      = true
cron         = "45 23 * * *"
timezone     = "Asia/Tokyo"
skill        = "claw"
silent       = true
channel_type = "discord"
channel_id   = "987654321"
prompt       = "今日の runs/ ログと MEMORY.md・daily ノートをレビューし、残す価値のある発見を今日の daily ノートに要約して保存して。"
```

Run it with `klein claw` (embedded agent) — no separate server process needed.

---

## 6. Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `ANTHROPIC_API_KEY` | If `backend=anthropic` | Anthropic API key |
| `OPENAI_API_KEY` | If `backend=openai` | OpenAI API key |
| `GEMINI_API_KEY` | If `backend=gemini` | Google Gemini API key |

> The Discord bot token is **not** read from the environment — set
> `claw.discord.token` in `settings.toml` (see [§5](#5-gateway-configuration-klein-claw)).

---

## 7. User Data Directories

All persistent data lives under `$HOME/.klein/` (interactive mode only; one-shot mode is memory-only).

```
$HOME/.klein/
├── settings.toml                        # Default settings (see §2)
├── permissions.json                     # User-wide permission rules (see §3)
├── projects/
│   └── {project-basename}-{hash}/      # One directory per project
│       ├── project_info.txt            # Project path and metadata
│       ├── todos.json                  # Todo list
│       ├── tasks.json                  # Task list
│       ├── sessions/                   # One file per interactive run
│       │   └── YYYYMMDDTHHMMSS.ffffff.json
│       └── history.txt                 # Readline command history
├── sessions/                            # Per-session Connect-gRPC state (serve mode / gateway)
└── memory/
    ├── MEMORY.md                        # Long-term memory
    ├── daily/
    │   └── YYYY-MM-DD.md               # Daily journal notes
    └── runs/
        └── YYYY-MM-DD.md               # Scheduled-run log
```

> The gateway's own paths derive from the top-level `base_dir` (default
> `~/.klein`), not a `claw/` subdirectory — see [§5](#5-gateway-configuration-klein-claw).

**Project directory naming:** `{basename}-{8-char hash of absolute path}`
Example: `/Users/you/dev/my-app` → `my-app-a1b2c3d4/`

### Interactive sessions

Each interactive run gets **its own file** under the project's `sessions/`
directory, so a plain `klein` starts fresh without overwriting what came before.
`klein --continue` resumes the most recently *used* session — ordering is by
modification time, so a session started days ago but worked in today is the one
you get.

A session file is written only once a run has an actual exchange: starting
`klein` and quitting leaves nothing behind, and so cannot shadow the real
conversation before it. A pre-existing `session.json` from before per-run
sessions is migrated into `sessions/` on first use, keeping it resumable.

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
| Change LLM model | `--model` flag or `llm.model` in settings TOML |
| Pre-approve a file path | `.klein/permissions.json` → `allow` rule for `Write`/`Edit` |
| Block a command | `.klein/permissions.json` → `deny` rule for `Bash` |
| Limit tools for a skill | `allowed-tools` in `SKILL.md` frontmatter |
| Add a safe bash command | `bash.whitelisted_commands` in settings TOML |
| Increase iteration limit | `agent.max_iterations` in settings TOML |
| Use an OpenAI-compatible endpoint | `llm.base_url` in settings TOML |
