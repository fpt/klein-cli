# klein — Architectural Design

How the **backend**, **agent**, and **tool** layers fit together, and how the
two interactive surfaces — the Discord **gateway** (`klein claw`) and the
terminal **REPL** (`klein claw repl`) — drive them. Read this to catch up on the
moving parts before touching them.

> Companion docs: [CONFIGS.md](CONFIGS.md) (every config field) and
> [../CLAUDE.md](../CLAUDE.md) (build/run commands, subsystem notes).

---

## 1. The layered stack

Every entry point ultimately builds the same stack. From the bottom up:

```
┌──────────────────────────────────────────────────────────────┐
│ Skill (SKILL.md)          claw · report · code · …            │  prompt + allowed-tools
├──────────────────────────────────────────────────────────────┤
│ Agent (internal/app.Agent)                                   │  session, memory, events,
│   └─ ReAct loop (pkg/agent/react.ReAct)                      │  reason→act iterations
├──────────────────────────────────────────────────────────────┤
│ Tools (domain.ToolManager)                                   │  filesystem, bash, memory,
│   CompositeToolManager → DeferredToolManager (ToolSearch)    │  schedule, web, MCP, …
├──────────────────────────────────────────────────────────────┤
│ Backend (domain.LLM)      openai · anthropic · gemini         │  chat, tool-calling, thinking
└──────────────────────────────────────────────────────────────┘
   (plus codex & appserver — whole-agent backends routed above domain.LLM; see §7)
```

The three layers the rest of this doc drills into:

- **Backend** (`pkg/client`) — an LLM client implementing `domain.LLM` plus
  optional capability interfaces. Stateless across sessions; one client per
  agent.
- **Agent** (`internal/app`, `pkg/agent`) — owns a conversation: message state,
  the ReAct loop, event emission, and (interactive only) session persistence.
- **Tools** (`internal/tool`) — a `CompositeToolManager` of every capability,
  wrapped by a `DeferredToolManager` so a skill sees a small core set and loads
  the rest on demand via `ToolSearch`. A skill's `allowed-tools` filters what is
  reachable.

---

## 2. Entry points

| Command | What it builds | Agent lives in | Session |
|---------|----------------|----------------|---------|
| `klein` (no args) | one `app.Agent`, interactive REPL | in-process | project session (`~/.klein/projects/<hash>`) |
| `klein "prompt"` | one `app.Agent`, one-shot | in-process | in-memory (not persisted) |
| `klein --serve` | `connectrpc.AgentServer` | in-process, **one agent per RPC session** | file per persistence key |
| `klein claw` | **gateway** + embedded (or remote) `AgentServer` | agent(s) behind Connect RPC | file per Discord/schedule peer |
| `klein claw repl` | one `app.Agent`, interactive REPL | in-process | project session |
| `klein mcp …` | none (edits `settings.json`) | — | — |

Two shapes matter for this doc: the **gateway** (`klein claw`) routes many peers
through a Connect server to per-peer agents, while the **REPL** (`klein claw
repl` and plain `klein`) is a single in-process agent. Both share the same tool
managers and backend configuration.

---

## 3. `klein claw` (gateway) vs `klein claw repl`

Both are "frontends to claw." The difference is topology.

### 3a. `klein claw` — the gateway

```
                          klein claw (one process)
 ┌───────────────────────────────────────────────────────────────────────┐
 │  Discord adapter ─┐                                                     │
 │  Scheduler ───────┤→ MessageBus ─→ handleInbound ─┐                     │
 │                   │   (inbound)                    │ Connect RPC        │
 │                   │                                ▼ (StartSession/     │
 │                   │                     ┌──────────────────┐  Invoke)   │
 │                   │                     │ AgentServer      │            │
 │                   │                     │  session A → app.Agent ─┐     │
 │                   │                     │  session B → app.Agent ─┤     │
 │  Discord adapter ←┴──── OutboundMessage │  scheduler:x → app.Agent┤     │
 │       (2000-char split)     ▲           └──────────────────┘      │     │
 │                             └───── streamed events ───────────────┘     │
 └───────────────────────────────────────────────────────────────────────┘
                 embedded server = default (agent_addr empty)
                 remote server   = set agent_addr → dial klein --serve
```

- **Embedded by default.** With `agent_addr` empty, `klein claw` starts the
  Connect server in-process on an ephemeral loopback port
  (`StartServerListener`) and dials it — a single command, no separate process.
  Set `agent_addr` (or `--agent-addr`) to dial a remote `klein --serve` instead.
- **One agent per peer.** `SessionManager` maps each `(channel, peer)` to a
  Connect RPC session; `AgentServer.StartSession` builds a fresh `app.Agent`
  (its own LLM client, its own message state) per persistence key.
- **Adapters + scheduler feed one bus.** Discord messages and cron firings both
  become `InboundMessage`s on the `MessageBus`; `handleInbound` routes them.
- Discord replies flow back as `OutboundMessage`s, split at Discord's 2000-char
  limit.

### 3b. `klein claw repl` — the terminal frontend

```
                 klein claw repl (one process)
 ┌───────────────────────────────────────────────────────┐
 │  readline ─→ app.Agent ─→ ReAct ─→ tools ─→ backend    │
 │                 │                                       │
 │                 └─ project session (own message state)  │
 └───────────────────────────────────────────────────────┘
   no Connect server · no Discord · no scheduler
   same settings, same base_dir, same tool managers as the gateway
```

- **No Connect server, no bus.** It builds **one** `app.Agent` directly
  (`app.NewAgentWithOptions` + `app.StartInteractiveMode`) — the same path as
  plain `klein`, but wired with claw's tool managers and backend.
- **Shares tools + data, not the session.** It constructs the same memory /
  schedule / MCP tool managers pointed at the same `base_dir`, so anything it
  writes (a new schedule, a memory note) lands in the same files a running
  gateway sees. Its **message history is its own** (a project session), separate
  from every Discord peer and scheduled run.
- **Runs alongside the gateway.** Because coordination is through `base_dir`
  files (not shared memory), you can run the daemon and open a REPL against the
  same instance; the gateway live-reloads `schedules.json`.

### 3c. Why the split

| | Gateway | REPL |
|---|---------|------|
| Concurrency | many peers, concurrent | single user, serial |
| Agent lifetime | per-peer, created on demand | one, for the process |
| Transport | Connect RPC (embed or remote) | direct in-process calls |
| Session | file per peer under `base_dir/sessions` | project session |
| Runs Discord/scheduler | yes | no |
| Shares memory/schedules/MCP | yes (via `base_dir`) | yes (via `base_dir`) |

---

## 4. Request lifecycle

### 4a. Discord message → reply

```
Discord ─→ adapter ─→ bus.Inbound ─→ handleInbound
   │                                     │ 1. parse "/skill" one-shot override (or !command)
   │                                     │ 2. LockInvoke() — serialize this peer
   │                                     │ 3. prepend [SESSION LOG] + [MEMORY CONTEXT]
   │                                     │ 4. Connect Invoke() (streaming)
   │                                     ▼
   │                          AgentServer → app.Agent.Invoke → ReAct loop
   │                                     │  (thinking/tool/result events streamed back)
   └───────────── reply ◀── bus.Outbound ◀── final text
```

Key steps in `handleInbound`:
1. **Slash / bang commands** — `/list` and `/<skill>` (one-shot skill override)
   and `!clear/!skill/!memory/!help` are handled by the gateway; `/list` never
   hits the model.
2. **Per-peer lock** — `Session.LockInvoke()` serializes two quick messages from
   the same peer (the agent's message state is not concurrency-safe).
3. **Context injection** — the gateway prepends the session-log path and the
   `[MEMORY CONTEXT] … [END MEMORY CONTEXT]` block (from `MEMORY.md` + recent
   daily notes) to the user text.
4. **Invoke over Connect** — the server translates ReAct `events.AgentEvent`s
   into streamed proto `InvokeEvent`s (thinking deltas, tool calls, final text).

### 4b. Scheduled run → run log

```
Scheduler (cron fires) ─→ bus.Inbound (PeerID "scheduler:<name>", Skill from job)
   handleInbound prepends [SCHEDULED RUN] (name + channel; no user present)
   → agent runs the job's prompt under its skill (usually `report`, headless)
   → response posted to the job's channel (unless silent)
   → ALWAYS appended to base_dir/memory/runs/YYYY-MM-DD.md (run log)
```

The run log lets a later job (e.g. a nightly memory cron) read what earlier jobs
produced via `MemoryGet`/`MemorySearch`. See CONFIGS.md §5 for the schedule
schema.

### 4c. REPL line → reply

```
readline ─→ app.Agent.Invoke(text, skill) ─→ ReAct loop ─→ tools ─→ backend
         ◀────────────────────── final text + streamed thinking ◀──
```

No bus, no RPC — direct method calls. Same ReAct loop and tools as the gateway.

### 4d. The ReAct loop (shared by all)

`app.Agent.Invoke` (internal/app/agent.go) per turn:
1. Resolve the skill; filter tools by its `allowed-tools` (or expose the full
   deferred/ToolSearch view when unset).
2. Wrap the backend with the filtered tools (`client.NewClientWithToolManager`).
3. Run `react.ReAct` up to `max_iterations`: the model reasons, optionally calls
   tools, sees results, and repeats until it produces a final answer.
4. Emit `events.AgentEvent`s (thinking, tool-call-start, tool-result, response,
   error) — the REPL prints them; the Connect server streams them.

---

## 5. Tool layer

### 5a. Composition and discovery

Every agent is built with **all** tool managers composed into one
`CompositeToolManager`, then wrapped in a `DeferredToolManager`:

- **Universal tools** (always present): filesystem (`Read`/`Write`/`Edit`/`LS`),
  `Bash`, `Grep`/`Glob`, `TodoWrite`, task tools, web, PDF, market, skill.
- **claw specialized tools** (registered by the gateway/REPL/serve paths):
  `MemorySearch`/`MemoryGet`/`MemoryWrite`, `ScheduleCreate`/`List`/`Delete`,
  and any configured **MCP** servers.
- **Deferred / ToolSearch** — a skill sees a small **core** set up front; the
  rest (including MCP servers) are loadable on demand with `ToolSearch`. This is
  why a skill needn't enumerate every tool.
- **Filtering** — a skill's `allowed-tools` acts as a hard whitelist
  (`skill.NewFilteredToolManager`); the CLI `--allowed-tools` flag overrides it.

### 5b. Shared vs per-session managers — the important distinction

Not all tool managers have the same lifetime, which drives the concurrency model
(§6):

| Manager | Lifetime | Shared across sessions? |
|---------|----------|-------------------------|
| Filesystem, todo, task | built **per agent** in `NewAgentWithOptions` | no |
| Memory, schedule, MCP | built **once**, passed to every session (`AgentServer.mcpToolManagers`) | **yes** |

So in the gateway, one `MemoryToolManager` / `ScheduleToolManager` instance is
called by every concurrent session (a Discord peer *and* a firing cron), and the
REPL is a **second process** holding its own instances over the same files.

---

## 6. Concurrency & mutual exclusion

The design isolates conversations and guards only the genuinely shared state.

**Isolated (no locking needed):**
- **Per-session agents.** Each Connect session (and the REPL) has its own
  `app.Agent`, LLM client, and `MessageState`. Message history is never shared
  or raced between peers or between REPL and Discord.
- **Per-peer invoke lock.** `Session.invokeMu` serializes multiple messages from
  the *same* peer.
- **Backend.** One LLM client per agent, no shared mutable state; the remote API
  handles concurrent requests.

**Shared, and how it's protected:**

| Resource | In-process | Cross-process (REPL ↔ gateway, same `base_dir`) |
|----------|------------|--------------------------------------------------|
| `schedules.json` | `ScheduleToolManager.mu` around read-modify-write | **atomic** temp-file + rename, so the scheduler (polling every ~20s) never reads a torn file |
| memory files | `MemoryToolManager.writeMu`; append is locked RMW | **atomic** replace on overwrite/append |
| workspace files (`Write`/`Edit`) | per-session read-timestamp map | **optimistic concurrency**: reject a write if the file's mtime advanced since it was read → "re-read and retry" |

Two deliberately different strategies for shared files:
- **Filesystem** = a shared workspace of arbitrary files → **optimistic
  concurrency** (mandatory read-before-write + mtime stale-check). Detects
  concurrent edits from any session or process.
- **Memory** = an agent-owned, append-oriented store → **append-safe by
  default** (order-independent) + **atomic** whole-file replace. The manager is
  a single shared instance, so it can't reuse the per-session read map.

**Known limitation.** Atomic writes prevent corruption/partial reads but not a
cross-process *lost update* on the memory **overwrite/curate** path (two
processes doing read-modify-write at once → last writer wins). That's tracked in
issue #23 (optimistic-concurrency / ETag options); it's low-frequency because
append is the dominant memory op.

---

## 7. Backend layer

- **One interface, capability add-ons.** `domain.LLM` is the base (chat).
  Optional capabilities are discovered by type assertion, not booleans:
  `domain.ToolCallingLLM` (tool calls), `domain.StructuredLLM[T]` (typed
  structured output). Thinking is a model behavior, not a capability.
- **One client per agent.** `client.NewLLMClient(settings.LLM)` builds the client
  from settings; `AgentServer.StartSession` calls it per session, the REPL once.
- **Tool-calling detection.** `ClientWithTool` wraps a client and routes to
  native tool calling when the model is tool-capable (`IsToolCapable()`), else to
  a text-based tool protocol. This is transparent to the agent.
- **Model/effort ownership.** The model, `max_iterations`, and reasoning
  `effort` come from the agent's `settings.json` — the gateway does **not** set
  them (it only passes a working directory when starting a session).

### Whole-agent backends: codex and appserver (not `domain.LLM`s)

`backend: "codex"` and `backend: "appserver"` are special. Neither is a chat model —
each runs its **own** reasoning + tool loop. So they are *not* plugged in as a
`domain.LLM`; instead `app.Agent.Invoke` branches: when a `CodexBackend` is set,
the turn is routed to a backend thread (`Runner.RunTurn`) and the ReAct loop +
`ToolManager` are bypassed entirely. The `domain.LLM` slot holds only a stub (so
construction and `ModelID()` work; `Chat` is never called).

Both are driven by **one** implementation, `internal/agentserver`, because both
speak the same JSON-RPC **app-server protocol**. They differ only in the binary
spawned (resolved by `command()`) and in who owns the model: codex takes it from
the codex CLI's config, a generic app-server from its own.

`appserver` is deliberately **generic** — it names the protocol, not an
implementation. Any local agent that implements the subset used here
(`initialize` with `experimentalApi`, `thread/start`, `turn/start`,
`dynamicTools`) can be plugged in by naming its binary in `appserver.command`;
there is no default, so klein never guesses one. `codex` stays a distinct id only
because it carries codex-specific behavior (sandbox modes, the login probe in
`probeReady`). The reference server is
[rs-gallium](https://github.com/fpt/rs-gallium) (`gallium app-server`, see its
`crates/gallium-agent/src/appserver/`).

**Why not "ACP".** This backend was named `acp` between 2026-07-23 and
2026-07-25, and that name conflated two different protocols:

| "ACP" as used by | Means | klein |
|---|---|---|
| gallium / rs-kessel / klein | the codex-app-server subset — `initialize` / `thread/start` / `turn/start` plus `dynamicTools` | **what this backend speaks** |
| [agentclientprotocol.com](https://agentclientprotocol.com), the `codex-acp` npm bridge | the editor-facing standard — `session/new`, `session/prompt`, `session/update` | **not implemented** |

rs-gallium settled the question on its side — the standard is a no-go, with no
adapter planned ([#15](https://github.com/fpt/rs-gallium/issues/15), reaffirmed
in [#13](https://github.com/fpt/rs-gallium/issues/13)) — so klein names the
backend after the protocol it actually speaks. `"backend": "acp"` is rejected at
startup with a message naming the new id; there is no silent alias, because the
two senses must not be allowed to blur back together.

That subset is a contract, so nothing outside it may be required at startup. In
particular `probeReady`'s `account/read` call runs **only for `codex`** — it
validates a login that a generic app-server does not have, and demanding the
method would reject conforming implementations with no account concept. There is
no liveness cost: `initialize` is itself a round trip, so an unreachable or
non-conforming server has already failed by then.

klein keeps every frontend duty around the backend turn: the repl/claw surfaces,
memory-context injection, run-log append, and **session↔thread mapping**. The
active skill's prompt is passed to the backend as *developer instructions*. One
app-server process is shared across all sessions (turns are serialized).

**How klein's tools reach the backend.** The Runner drives the app-server over
the **low-level JSON-RPC protocol** (not the SDK's high-level `Thread` helpers),
because klein registers its native tools via the experimental **`dynamicTools`**
mechanism — which needs the `experimentalApi` capability negotiated at
`initialize`, something the SDK's `New()` does not send. So the Runner spawns the
app-server, `initialize`s with `experimentalApi`, registers klein's memory +
schedule tools as `dynamicTools` on `thread/start`, and services the backend's
**`ItemToolCall`** callbacks in-process by dispatching to the live tool managers
(same files, same locks — no HTTP, no MCP server). klein's configured **external
MCP servers** are also passed through backend config; filesystem/shell come from
the backend's own native tools.

Thread lifecycle: `dynamicTools` cannot be re-registered on `thread/resume`, so a
tool-enabled thread is always one this process started — a session's persisted
`thread_id` from a prior run is replaced by a fresh (tool-enabled) thread on its
next turn (memory-context injection still carries facts across restarts).

**Approvals.** `approval_policy` decides who authorizes a mutation. Under
`never` the backend proceeds unasked (headless surfaces). Otherwise it raises
`item/commandExecution/requestApproval` / `item/fileChange/requestApproval`, and
klein's `toolHandler` either auto-accepts (headless, `Approver == nil`) or
prompts the user (interactive repl). Note a generic app-server typically has **no
sandbox** of its own — `codex.sandbox_mode` does not apply to it, so approvals
are the only gate.

**Cancellation.** `turn/start` is an acknowledgement, not the outcome: the
backend answers at once and reports the turn by notification. So a cancelled
context (Ctrl+C in the repl) unblocks *klein* and nothing else — the turn keeps
running, holding the thread's one turn slot, and the next `turn/start` is refused
("one turn at a time") until it ends on its own. `runTurn` therefore keeps the
turn id from the start response and, on any exit from the notification loop that
isn't the turn's own ending, sends `turn/interrupt{threadId, turnId}` and
**waits**: the protocol parks that request and answers once the turn has aborted,
so the reply means *stopped*, not *heard*. The wait is bounded
(`interruptTimeout`) because a backend with no interruption point would otherwise
hold klein indefinitely, and it is best-effort — an app-server predating
`turn/interrupt` answers method-not-found, which is logged, since it means Ctrl+C
left work running.

Requires the `codex` binary on `PATH` (with `dynamicTools` support —
experimental) or the binary named by `appserver.command`; auth/model are the
backend's own.

**Protocol drift.** A subset is a contract between two repos that release
separately, so the failure mode is not a crash but a *silent* divergence:
`turnProgress.render` switches over the item variants it knows, and falling off
the end is correct forward-compatibility — a newer backend must not break an
older klein. It is also indistinguishable from a backend that has gone wrong.
That is not hypothetical: rs-gallium sent tool results as `toolResult`, a variant
with no case here, so every one was dropped, while the *start* announcement
arrived as `commandExecution` and rendered as a shell result — `exit 0`, printed
before the tool had run, with the real output never shown and a failing call
byte-identical to a passing one ([fpt/rs-gallium#49](https://github.com/fpt/rs-gallium/issues/49),
fixed producer-side in [#50](https://github.com/fpt/rs-gallium/pull/50)). Both
projects' test suites passed throughout.

Two seams make that loud instead:

- **Report what nothing renders.** `render`'s `default` warns once per turn per
  unknown type. `itemTypesKnownUnrendered` holds the variants klein recognises
  and deliberately skips (`agentMessage` — the turn text arrives via
  `extractText` — plus `plan`, `sleep`, the review modes, `autoApprovalReview`
  and `permissions`), so ordinary codex bookkeeping does not drown the signal.
  The list is best-effort: a legitimate variant showing up in a log is fixed by
  adding it there, and the per-type dedupe bounds the noise meanwhile. The
  logger is threaded from `StartAgentBackend` → `RunnerOptions` → `Config` and
  is optional the whole way; `nil` is silent.
- **Test the pair, not the belief.** Unit tests that hand-build notifications
  can only confirm what klein already thinks; they passed throughout the bug
  above. `gallium_integration_test.go` spawns a real gallium and asserts on the
  rendered events — and, because it hands the Runner a logger, fails if gallium
  ever sends a type nothing renders. It is affordable because gallium's
  `scripted` engine replays a JSON script: no model, no API key, no network.
  See [DEVELOPMENT.md](DEVELOPMENT.md) for running it.

---

## 8. State & paths

Everything derives from the shared **`base_dir`** (default `~/.klein`), so the
CLI, the gateway, and the REPL agree on locations:

```
<base_dir>/
├── sessions/            per-peer persistence (gateway); keyed by X-Persistence-Key
├── memory/
│   ├── MEMORY.md        long-term memory (injected into gateway prompts)
│   ├── daily/           dated notes
│   └── runs/            scheduled-run output log (read-only for agents)
├── schedules.json       dynamic schedule store (agent-written, scheduler-watched)
└── projects/<hash>/     interactive project sessions (plain klein, claw repl)
```

- **Multi-instance** = a settings file with a different `base_dir` (and Discord
  token). Fully isolated; the embedded server's ephemeral port avoids
  collisions.
- **Session persistence** on the server side is keyed by the gateway's
  `X-Persistence-Key` header; the file path the gateway injects as `[SESSION
  LOG]` matches where the server writes because both derive from `base_dir`.

---

## 9. Skills

- **SKILL.md** = YAML frontmatter (`name`, `description`, `allowed-tools`,
  `argument-hint`, `user-invocable`, optional `model`) + a prompt body with
  `$ARGUMENTS` / `$0-$9` / `{{workingDir}}` / `{{home}}` substitution.
- **Sources & precedence** — embedded (in the binary) < project `.claude/skills`,
  `.agents/skills` < personal `~/.klein/skills` … highest priority wins by name.
- **Roles relevant here:** `claw` (conversational, memory-aware; the gateway and
  REPL default) and `report` (headless deliverable generator; the default for
  scheduled runs — it produces output instead of asking questions).

---

## 10. The reason → verify → evaluate loop (prompt design principle)

klein's quality on non-trivial and research tasks comes from the model running a
**hypothesis → verification → evaluation** cycle rather than answering in one
shot: propose an action, treat its result as *evidence*, judge whether that
evidence actually proves the claim, and continue if it doesn't. This mirrors
OpenAI Codex, whose edge on research/debugging comes from the same discipline.
It is a property we deliberately maintain, so keep it intact when editing prompts.

Three seams enforce it — two in code (model-agnostic, apply to every skill) and
one in the skill prompts:

- **The ReAct loop feeds every tool result back for re-evaluation** (§4d). The
  loop itself is the observe→act→observe cycle. `IterationAdvisor`
  (`internal/app/iteration_advisor.go`) sharpens the *evaluate* step: after a tool
  result it injects an ephemeral nudge to treat the result as evidence — does it
  confirm or contradict the expectation? revise rather than repeat on a
  contradiction; conclude only when the evidence is sufficient. (Ephemeral so the
  static tool list stays cacheable — see §5.)
- **`/goal` runs an evidence-driven completion audit** (`internal/app/goal.go`).
  After each turn `evaluateGoal` treats completion as *unproven*: it requires
  authoritative evidence (command output, test results, file contents — not
  intentions), matches the evidence to the goal's scope, and counts
  uncertain/indirect evidence as *not met*. The continuation directive then tells
  the agent to work from current-state evidence rather than assuming earlier steps
  succeeded. (Its output stays the two-line `MET:`/`REASON:` contract that
  `parseGoalEvaluation` and its tests depend on.)
- **Skill prompts carry the cycle explicitly.** The research skills each end with
  a verification gate — `research-stock` "Verify before you report" (price and
  news are separate evidence; a headline is a hypothesis to confirm against price
  action), `report` "Verify before you finalize" (every figure traces to data
  gathered this run; corroborate key numbers), `web` "Verify & synthesize" (each
  claim backed by fetched content; follow links to fill gaps rather than guess).
  The `code` skill's "Verifying & reporting" and "When stuck" sections do the same
  for coding.

**When you modify prompts, preserve:**
1. **State a hypothesis before acting** — don't guess or assert; plan the check.
2. **Verify against authoritative evidence** — run the test/command, read the
   file, fetch the second source; prefer specific checks before broad claims.
3. **Evaluate honestly** — treat weak, indirect, or single-source evidence as not
   proven; revise the approach on a contradiction instead of repeating the call.
4. **Report faithfully** — never claim success when checks fail or a figure is
   unverified; when checks pass, say so plainly without hedging.

If you change the `/goal` evaluator, keep the `MET:`/`REASON:` two-line output
(`internal/app/goal.go`, `loop_goal_test.go`). If you change the `IterationAdvisor`
nudges, keep them ephemeral so Anthropic prompt caching still hits (§5).

---

## 11. Where things live

| Concern | Package / file |
|---------|----------------|
| CLI entry, subcommand dispatch | `klein/main.go` |
| `klein claw` (gateway + repl) | `klein/claw_command.go` |
| `klein mcp` | `klein/mcp_command.go` |
| Agent orchestration, ReAct wiring | `internal/app/agent.go`, `internal/app/repl.go` |
| ReAct loop | `pkg/agent/react/` |
| Message state / persistence | `pkg/agent/state/` |
| Backend clients + capabilities | `pkg/client/`, `pkg/agent/domain/` |
| Tools (composite, deferred, filesystem, memory, schedule, …) | `internal/tool/` |
| Skills (load, filter, embedded SKILL.md) | `internal/skill/` |
| Connect server (embed + blocking) | `internal/connectrpc/` |
| Gateway (bus, sessions, memory, scheduler, discord) | `internal/gateway/` |
| Settings + base_dir + claw block | `internal/config/settings.go` |
| Config reference | `doc/CONFIGS.md` |
