# klein vs. hermes-agent

A structured comparison of **klein** (this project, `fpt/klein-cli`) and
**hermes-agent** ([`NousResearch/hermes-agent`](https://github.com/NousResearch/hermes-agent)),
across tools, skills, memory, permissions, and surrounding architecture.

> Snapshot as of 2026-07-13, against `hermes-agent` `main` (commit `f96b2e6ef`).
> hermes is a large, fast-moving codebase; specifics may drift.

## At a glance

| | **klein** (`fpt/klein-cli`) | **hermes-agent** (`NousResearch/hermes-agent`) |
|---|---|---|
| Language | Go 1.24, single compiled binary | Python 3.11 (`cli.py` alone is 748 KB) |
| Origin | Single-author personal project | Nous Research, team + open ecosystem, MIT |
| Scale | ~25 tool files, 9 skills | ~40+ core tools, 18 skills + 20 optional + 20 plugins + a skills **hub** |
| Agent loop | ReAct, DDD layering, event-driven | Custom agent loop, "narrow waist" core |
| Model backends | Ollama, Anthropic, OpenAI, Gemini (+ drives Codex/Kessel via an ACP app-server) | ~any (OpenRouter 200+, Nous Portal, NIM, OpenAI, custom) via `hermes model` |
| Surfaces | CLI, REPL, Connect-gRPC server, `claw` gateway (Discord) | CLI, TUI, Electron desktop, web dashboard, gateway (~20 platforms), MCP server |
| Isolation | Runs locally in cwd | 6 terminal backends (local/docker/ssh/singularity/modal/daytona) |

**The headline:** these are the same *idea* at wildly different scales. klein is a
tight, readable Go implementation of a personal coding + messaging agent; hermes is a
sprawling, batteries-included Python platform. Strikingly, they have
**independently converged** on nearly the same architecture — and both cite
**OpenClaw** as inspiration (klein's gateway is "OpenClaw-inspired"; hermes
references "OpenClaw's 9-tool bridge" and an "OpenClaw cron regression"). klein
additionally mirrors **Claude Code** conventions (`.claude/agents`,
`.claude/skills`, plan mode, deferred `ToolSearch`).

## Tools

**Shared:** file tools (read/write/edit/search), a bash/terminal tool, todos, web
fetch/search, and — notably — **both defer tool schemas to keep the prompt cheap**.
klein has `ToolSearch` (core tools always exposed; the rest discovered on demand);
hermes has `tool_search`/`tool_describe`/`tool_call` that kick in when deferrable
MCP/plugin tools exceed ~10% of the context window. Both keep a fixed "core" set
undeferrable.

**klein specifics**

- Universal: `Read Write Edit MultiEdit LS Glob Grep Bash TodoWrite`
- Memory: `MemoryWrite/Get/Search`; Schedules: `ScheduleCreate/List/Delete`;
  Web: `WebFetch WebSearch WikipediaSearch`; Plan mode: `EnterPlanMode/ExitPlanMode`;
  `AskUserQuestion`; `ReadSkill`
- Subagents: `Task` (named agent from `.claude/agents/*.md`, own context, can't
  recurse) + `spawn_agent`
- Domain tool packs: a `Researcher*` suite (market narratives, stock research),
  PDF, market tools
- MCP client support
- **Per-skill `allowed-tools` is *enforced*** — a composite manager filtered at
  invoke time is klein's actual security model.

**hermes specifics**

- Core "waist" (`toolsets.py`): `terminal process read_file write_file patch
  search_files web_search web_extract vision_analyze image_generate skills_*
  browser_*(12) text_to_speech todo memory session_search clarify execute_code
  delegate_task cronjob` + service-gated (Home Assistant, kanban, computer_use).
- **"Footprint Ladder"** governs additions: extend code → CLI+skill →
  **service-gated tool (`check_fn`)** → plugin → MCP → new core tool (last resort).
  `check_fn` hides a tool from the schema until its prerequisite is configured.
- **`execute_code` (Programmatic Tool Calling):** the model writes a Python script
  that calls tools back over a **Unix-socket/file RPC** — intermediate results never
  enter the context window. klein has no equivalent.
- **MCP both ways:** consumes stdio/HTTP/SSE servers (+ OAuth, a curated catalog)
  *and* exposes itself as an MCP server. klein is MCP-client-only.
- **Plugins** (4 discovery sources, pip-installable) can add tools, platforms,
  memory backends, and model providers without touching core.

## Skills

Both use **`SKILL.md` = YAML frontmatter + markdown**, layered with local overrides
winning. That's essentially the `agentskills.io` shape (hermes is explicitly
compatible and can install from Anthropic/OpenAI skill repos via its hub; klein's is
homegrown but the same format).

| | klein | hermes |
|---|---|---|
| Sources | embedded built-ins → `.claude/skills/` → `~/.claude/skills/` | `skills/` (bundled) → `optional-skills/` → `~/.hermes/skills/` (user/agent) + hub + external dirs |
| `allowed-tools` | **enforced** as a tool filter | **informational only** (audit signal, not enforced) |
| Discovery | `ReadSkill` tool + slash invocation | prompt-injected index (Tier 0) → `skills_list` → `skill_view` on demand; every skill is also a `/slash` command; bundles; `/learn` |
| Structure | single `SKILL.md` | `SKILL.md` + `references/ templates/ scripts/ assets/` |
| **Self-creation** | `create-skill` skill — **user-invoked**: distills the *current* conversation into a new `SKILL.md` | **autonomous**: a background-review fork after complex tasks (5+ calls, errors overcome) calls `skill_manage(create/patch)` |
| **Self-improvement** | none automatic | skills patch themselves during use; a **curator** ages agent-created skills (`active→stale→archived`) on a ~7-day cadence |

This is the biggest philosophical gap: hermes has a genuine **closed learning loop**
(create + improve + curate skills autonomously); klein keeps a human in the loop for
skill authoring.

## Memory

Both keep a `MEMORY.md` and both landed on **SQLite FTS5 (bm25)** for search — again,
convergent design.

**klein** runs two memory systems:

- **File-based** (gateway/`claw`): `MEMORY.md` (long-term) + `daily/*.md` (notes) +
  `runs/*.md` (scheduled-run output logs), keyword-searched via `MemorySearch`.
  Injected into prompts as a `[MEMORY CONTEXT]` block.
- **`memorydb`** (REPL `/memory`): a **SQLite + FTS5 bm25** store with **entity
  tracking, supersede/forget lifecycle, and candidate re-ranking** — arguably *more
  structured* than hermes's flat file.

**hermes** runs three layers, all engineered around one obsession —
**"per-conversation prompt caching is sacred"**:

- **Curated `MEMORY.md` + `USER.md`**, injected as a **frozen snapshot** captured at
  session start (mid-session writes hit disk but don't re-enter the prompt until next
  session — to preserve the cache prefix).
- **Cross-session recall:** FTS5 over *all past conversations* (`state.db`), with a
  trigram table for CJK; `session_search` tool. (The "LLM summarization" of recall is
  now vestigial — removed from the default path.)
- **External providers (plugins):** **Honcho dialectic user modeling**, mem0,
  supermemory, etc., injected into the *current user message at API-call time* (again,
  to not break the cache).
- **Nudges + compression:** a turn-counted background fork (~every 10 turns) reminds
  the agent to persist knowledge; context compression is the *only* sanctioned cache
  rebuild.

klein's memory is simpler and more direct; hermes's is deeper, self-curating, and
paranoid about token/cache cost.

## Permissions & security

**klein** — pragmatic, in-process:

- Bash: **regex injection blocking** (`$()`, backticks, `${}`, `<()`, heredocs),
  **hardcoded deny rules** that config can't override (`rm -rf /`, fork bomb), a
  no-approval **whitelist**, everything else → **interactive approval**
  (Yes / Always / No).
- `Write/Edit/MultiEdit` require approval unless auto-approve.
- Filesystem: **directory allowlist + read→write semantics** (must read before write;
  timestamp checks catch concurrent edits).
- **Plan mode** with its own approval gate; per-skill `allowed-tools` acts as a real
  capability boundary.
- ACP app-server backend has `never`/`on-request` approval modes.

**hermes** — layered defense-in-depth, but candid about its limits:

- **Three approval modes:** `manual` / `smart` / `off`. **`smart` = an auxiliary LLM
  judge** (approve/deny/escalate, with prompt-injection hardening) inspired by Codex's
  smart approvals.
- Concentric command lists: **hardline** (unconditional, un-bypassable) → user deny →
  YOLO/off bypass → permanent allowlist → **Tirith** (external Go content scanner) +
  ~60 dangerous-pattern regexes, all with heavy **anti-obfuscation normalization**.
- **Real isolation via the 6 terminal backends** (docker cap-drop/no-new-privileges,
  singularity `--containall`, modal/daytona cloud sandboxes). Containerized/cloud
  backends *skip* the in-process gate because the OS is the boundary.
- **Gateway: default-deny per-platform allowlists** for every channel
  (Telegram/Discord/Slack/…), pairing stores, role auth.
- Subagents auto-deny approvals, strip dangerous tools, cap depth; **credentials
  stripped by default** on every spawn.
- Its `SECURITY.md` states outright: *"the only security boundary against an
  adversarial LLM is the operating system"* — the in-process gates are heuristics for
  cooperative mistakes.

Net: klein's model is lighter and enforces `allowed-tools` as a boundary; hermes has
far more machinery (LLM judge, scanners, sandboxes, per-platform authz) but is explicit
that only OS-level isolation truly contains the agent.

## Everything else

| Dimension | klein | hermes |
|---|---|---|
| Messaging | Discord only (`claw`, discordgo), embeds a Connect-gRPC agent server | ~20 platforms from one gateway, voice transcription, cross-platform continuity |
| Scheduling | cron scheduler, `schedules.json`, synthetic `[SCHEDULED RUN]` messages, run logs | cron scheduler, natural-language jobs, delivery to any platform |
| Subagents | `Task` (named `.claude/agents`), depth-1 | `delegate_task` (concurrency/depth caps, async, interrupt), `execute_code` |
| Remote/serverless | local only | Modal/Daytona **serverless hibernation** ($5 VPS to GPU cluster) |
| Extra surfaces | — | Electron desktop, TUI, web dashboard, MCP server, **batch trajectory generation for model training** |

## Bottom line

- **Same architecture, opposite ends of the spectrum.** Agent core + messaging
  gateway + SKILL.md skills + `MEMORY.md`/FTS5 memory + cron + subagents +
  deferred-tool disclosure — both have all of it. The convergence is real (shared
  OpenClaw lineage; klein also tracks Claude Code).
- **klein** is what you'd reach for if you want a **legible, hackable, single-binary
  Go agent** you fully understand — with a genuinely enforced per-skill tool sandbox
  and a nicely structured `memorydb`.
- **hermes** is a **maximalist platform**: autonomous learning loop, 20 messaging
  platforms, 6 sandbox backends, plugin/skill hub ecosystem, LLM-judged approvals, and
  serverless deployment — at the cost of a huge, complex Python codebase.
- **Two philosophical deltas worth remembering:** (1) hermes *autonomously* creates and
  curates skills/memory; klein keeps a human in the loop. (2) klein *enforces*
  `allowed-tools` as a security boundary; hermes explicitly does not — it leans on
  OS-level sandboxing instead.
