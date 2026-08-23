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

### 1a. `pkg/` vs `internal/`, and what "public" actually costs

Go's `internal/` rule is narrower than it looks. It stops *outside code* from
naming an `internal/...` path — nothing more. A package under `pkg/` may import
`internal/` freely and still be importable from another module, because the
restriction is checked against the importing file's path, not the dependency
graph. `pkg/client` does exactly this (it pulls `internal/config`) and an outside
module can import it today.

So `pkg/` does not mean "public API" here, and never did. It records the older
DDD split — domain and reusable machinery in `pkg/`, application wiring in
`internal/`. Most packages under `pkg/` are klein's own; they simply are not
offered to anyone.

**`pkg/agentserver` is the exception, and the only package held to a stricter
rule**: it imports nothing of klein's, because it is meant to be used by other
programs. That is not a stylistic preference — it is arithmetic. Every dependency
a shared package has, it imposes on everyone who imports it. Before the
extraction, the app-server client lived beside klein's tool managers and pulled
**151** non-stdlib packages, so a program wanting to spawn an agent process and
run a turn also compiled pdfcpu, goquery, mcp-go and modernc sqlite. It now pulls
**4**. `TestPackageImportsNothingOfKleins` keeps it that way, because a boundary
nothing checks is a boundary that drifts.

**Adding to `pkg/agentserver`.** If the new code needs something of klein's, that
is the signal it does not belong there. Take the klein type through a small
interface in `pkg/agentserver/types.go` and adapt it in
`internal/agentbackend/adapters.go`, which is where klein's side of this lives.
The dividing question is **mechanism or policy**:

| Belongs in the client (`pkg/agentserver`) | Belongs in klein (`internal/agentbackend`) |
|---|---|
| Parsing a `CommandAction` off the wire | Deciding which commands may run unattended |
| Rendering parameters as JSON Schema | Knowing what a klein `ToolManager` is |
| Knowing codex has an account to probe | Mapping a settings string to a `Dialect` |
| Reporting that a tool call started | Truncating its arguments for display |
| Asking whether a request is approved | Phrasing the question for a terminal |

Every interface the client asks for is optional and nil-tolerant (`DynamicTools`,
`Observer`, `Approver`, `Logger`), so a caller wanting only the final text of a
turn passes none of them. Note the trap that recurs at each injection point: a
nil *pointer* stored in an interface is a **non-nil interface**, so a `!= nil`
guard passes it through and the panic lands later, at the first method call. The
adapters convert at the boundary (`backendLogger`, `newToolHost`,
`backendApprover`, and `isNil` for the interface-typed ones).

Applying the same treatment to other `pkg/` packages is not planned. It is only
worth its cost where someone actually wants the package on its own.

---

## 2. Entry points

| Command | What it builds | Agent lives in | Session |
|---------|----------------|----------------|---------|
| `klein` (no args) | one `app.Agent`, interactive REPL | in-process | project session (`~/.klein/projects/<hash>`) |
| `klein "prompt"` | one `app.Agent`, one-shot | in-process | in-memory (not persisted) |
| `klein --serve` | `connectrpc.AgentServer` | in-process, **one agent per RPC session** | file per persistence key |
| `klein claw` | **gateway** + embedded (or remote) `AgentServer` | agent(s) behind Connect RPC | file per Discord/schedule peer |
| `klein claw repl` | one `app.Agent`, interactive REPL | in-process | project session |
| `klein mcp …` | none (splices `settings.toml`) | — | — |

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
  `effort` come from the agent's `settings.toml` — the gateway does **not** set
  them (it only passes a working directory when starting a session).

### Whole-agent backends: codex and appserver (not `domain.LLM`s)

`backend: "codex"` and `backend: "appserver"` are special. Neither is a chat model —
each runs its **own** reasoning + tool loop. So they are *not* plugged in as a
`domain.LLM`; instead `app.Agent.Invoke` branches: when a `CodexBackend` is set,
the turn is routed to a backend thread (`Runner.RunTurn`) and the ReAct loop +
`ToolManager` are bypassed entirely. The `domain.LLM` slot holds only a stub (so
construction and `ModelID()` work; `Chat` is never called).

Both are driven by **one** implementation, `pkg/agentserver`, because both
speak the same JSON-RPC **app-server protocol**. They differ only in the binary
spawned (resolved by `command()`) and in who owns the model: codex takes it from
the codex CLI's config, a generic app-server from its own.

That client is a **standalone package**: it imports nothing of klein's, so
another Go program can drive an app-server without taking klein with it (4
non-stdlib dependencies, against 151 when it lived under `internal/`). It asks
its caller for what it needs through small interfaces in
`pkg/agentserver/types.go` — `DynamicTools`, `Observer`, `Approver`, `Logger`,
each optional and each nil-tolerant — and says nothing about how they are
implemented.

klein's half lives in `internal/agentbackend`: the adapters that convert klein's
types into those interfaces (`adapters.go`), the settings→`Config` plumbing, the
command allowlist, and the `domain.AgentBackend` implementations the app layer
injects. The split follows one rule — **protocol knowledge in the client, policy
in klein**. Truncating a tool argument for display, deciding which commands may
run unattended, and phrasing an approval prompt are all klein's; parsing a
`CommandAction`, rendering a JSON Schema, and knowing that codex has an account
to probe are all the client's. `TestPackageImportsNothingOfKleins` enforces the
boundary, because a boundary nothing checks is a boundary that drifts.

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

**Spawned or dialed.** The transport is the one place these two deployments
differ. `appserver.command` spawns a child and speaks JSONL over its stdio;
`appserver.address` dials a server already listening on `host:port` (rs-gallium's
`GALLIUM_LISTEN`) and speaks the same JSONL over a socket. Same methods, same
`item/tool/call` in the reverse direction — which is the point of dialing: the
model can run on a GPU box while klein's dynamic tools keep running on the user's
machine, in klein's process, against klein's files.

The two are mutually exclusive, and so is everything that configures a child:
`args`, `env` and the deprecated `config` describe a process klein starts, and a
dialed server was started and configured wherever it runs. Naming them alongside
an address is an error rather than a silent no-op — a user who sets
`appserver.env` believes they chose a model.

Three consequences follow from the socket, all in `pkg/agentserver`:

- **Thread ids belong to the connection.** A server hands them out per
  connection, sequentially, so after a redial `thread_1` names a thread klein
  never started. `connect()` clears the started-thread map, which is what makes
  the next turn open a fresh thread instead of naming a dead one.
- **EOF is not a crash.** A server serving one client at a time hands the session
  to whoever connects last and closes the older socket — deliberately, so a
  laptop that slept and left a zombie connection cannot lock its owner out. That
  arrives as a clean EOF, reported as `ErrServerHungUp` so the message names the
  right cause.
- **Reconnects happen at a turn, never on a timer.** `ensureConnected` redials
  only when the user has asked for a turn. A background reconnect would let two
  idle klein instances take the session from each other forever; tied to a turn,
  reclaiming it takes someone actually typing.

There is no read deadline on the socket — a turn can run for minutes with nothing
on the wire while the remote model works — so liveness is TCP keepalive's job,
set once at dial. And the connection carries no authentication and no TLS:
anything that reaches the port runs turns on that agent, with whatever tools the
server lends it — nothing at all for one that serves none of its own, everything
they can do as the user it runs as for one that does. So the address belongs on
loopback, an SSH tunnel, or an overlay network that does the authenticating.
klein gives it no default.

**Whose tools run: `workspace_tools`.** klein can offer its own workspace tools —
`Read`, `Write`, `Edit`, `MultiEdit`, `LS`, `Glob`, `Grep`, `Bash` — as
`dynamicTools` alongside the native memory and schedule ones, serviced in klein's
process against klein's working directory. The default follows the transport,
because the server's behavior does:

- **Dialed** (`appserver.address`): on. A listening rs-gallium lends no
  filesystem or shell tools at all — its built-ins would run as the user *it* was
  started as, and a socket carrying no identity cannot say who is asking, so no
  approval policy could reason about whose privileges a call really uses.
  Loopback earns no exception: same machine is not the same user. klein's tools
  are therefore the model's only hands, and `validateWorkspaceTools` refuses an
  explicit `false` rather than obeying it into a session that cannot read a file.
- **Spawned** (`appserver.command`): off. That server is klein's own child with
  klein's privileges and working tools of its own; klein's would only shadow
  them, which is worth doing when klein's boundaries should apply and not
  otherwise.

Paired with a listening server, this splits the agent in two: the model reasons
wherever it runs, and every read, write and command happens where the user is.

The tool *names* are the contract. A server resolves a call to the first exact
name match, so klein's `Read` replaces a built-in `Read` rather than sitting
behind it — which is what lets the spawned case work as a substitution at all,
and why the names are the conventional PascalCase ones and not klein-prefixed.
`internal/agentbackend`'s `conventionalToolNames` test is what keeps a rename
from silently unhooking the model's hands.

Two things follow, both in `internal/agentbackend/workspace.go`:

- **Approval has to be re-established.** When the backend owns the tools it asks
  permission (`item/*/requestApproval`) and klein answers. Hand the tools to
  klein and nobody asks at all — the backend has nothing to request, and the
  dynamic-tool path runs what it is called with. So `withApproval` puts
  `Write`/`Edit`/`MultiEdit`/`Bash` to the same `Approver` first, and a `Bash`
  request carries its command in `Commands` so `auto_approve_commands` answers
  for it exactly as it does on the other side. Read-only tools are not asked
  about, matching the native loop.
- **Searching is reading.** `Glob` and `Grep` hand their `path` to `rg`/`find`,
  which answer about anywhere on the machine, and `SearchToolManager` did not
  check it against any allowlist — a gap that was cosmetic while klein's own loop
  was the only caller and a disclosure once the tools are offered to a remote
  one. Both managers now share `pathWithinAllowedDirectories`, so "inside the
  allowlist" means the same thing for a `Grep` as for a `Read`.
- **The allowlist is about where a path leads, not how it is spelled.** A prefix
  test on the literal path passes `<workspace>/link/secret` however far outside
  `link` points, so `pathWithinAllowedDirectories` resolves symlinks on both
  sides before comparing — both, because macOS temp directories live under
  `/var`, itself a symlink, and resolving one side alone would refuse paths that
  are plainly inside. `Read` was following such a symlink out of the workspace
  before this; the tools that shell out escape a second way, through the walk
  itself, which is why `Grep` runs `grep -r` and never `-R`
  (`--dereference-recursive`). `rg` and `find` already decline to follow.
- **Compound parameters need their shape.** `MultiEdit` takes an array of edit
  objects, and `agentserver.Parameter` had nowhere to put the element schema —
  the backend received a bare `{"type":"array"}` and the model had to guess at
  field names, which presents as a model that "cannot use the tool".
  `Parameter.Schema` carries the JSON Schema keys the struct has no field for.

**Approvals.** `approval_policy` decides who authorizes a mutation. Under
`never` the backend proceeds unasked (headless surfaces). Otherwise it raises
`item/commandExecution/requestApproval` / `item/fileChange/requestApproval`, and
klein's `toolHandler` either auto-accepts (headless, `Approver == nil`) or
prompts the user (interactive repl). Note a generic app-server typically has **no
sandbox** of its own — `codex.sandbox_mode` does not apply to it, so approvals
are the only gate.

**Auto-approving trusted commands: `auto_approve_commands`.** A top-level list of
command prefixes an app-server backend may run without asking. It is deliberately
*not* under `codex` or `appserver`: whether the agent behind the protocol is codex
or gallium does not change which commands you trust it to run unattended, so both
read the same list.

```toml
auto_approve_commands = ["gh run list", "gh run view"]
```

`WithAutoApprove` decorates the `Approver`, so the terminal prompt is untouched
and the decision is testable without a terminal. An allowlisted request is
answered before the prompt is ever printed; everything else still asks. Empty (the
default) is exactly the behavior from before it existed.

It is **not** seeded from `bash.whitelisted_commands`, which is a list chosen for
klein's own sandbox-free Bash tool. Inheriting it would auto-approve `go run` and
`make` on a surface where, under `workspace-write`, an approval can mean "run this
*outside* the sandbox" — codex asks precisely when the sandbox refused, so a yes
there is an escalation, not a repeat.

Three findings shaped the matcher, all measured against codex-cli 0.144.1's
`item/commandExecution/requestApproval`:

- **The backend unwraps the shell for us.** `command` is
  `/bin/zsh -lc 'gh --version'`, which no prefix list could ever match, but
  `commandActions[].command` is the bare `gh --version`. klein matches the parsed
  actions, so there is no shell-quoting parser here to get wrong.
- **A compound line is *one* action, not one per command.** `gh --version &&
  whoami` arrives as a single action holding the whole chain. Rejecting a
  candidate containing `&&`, `||`, `;`, `|`, `$(`, backticks, `>`, `<`, `&`, or a
  newline is therefore the entire security boundary, not a backstop: an entry of
  `gh` would otherwise approve anything appendable to it.
- **`acceptForSession` is not always on offer.** `availableDecisions` came back
  `["accept", {"acceptWithExecpolicyAmendment": …}, "cancel"]`, so klein cannot
  assume a session-scoped accept exists; it keeps sending plain `accept`.

Matching is prefix-at-a-word-boundary (so `gh` does not match `ghost`) and entries
may be several words — which is how the list can permit reading workflow state
without also permitting `gh api --method DELETE` through the same binary. Every
command in a request must match, and a request klein cannot parse in full goes to
the prompt. Each auto-approval is logged: one that leaves no trace is
indistinguishable from a command that was never proposed.

codex's own `execpolicy` covers similar ground from the other side — it offers
`proposedExecpolicyAmendment` and takes `acceptWithExecpolicyAmendment` to persist
a rule — but it is decided interactively and stored in codex's config, so it does
nothing for `claw`/`--serve`, where there is nobody to prompt.

**Sandbox and environment detail: two codex config tables.** `codex.sandbox_mode`
and `codex.approval_policy` are named per-thread on `thread/start`, but the
finer-grained knobs have no thread-scoped equivalent — `thread/start`'s `sandbox`
is the bare mode name. So `codex.sandbox_workspace_write` and
`codex.shell_environment_policy` mirror the codex config tables of the same
names, and klein renders them as `-c key=value` overrides on the child's launch
line (`codexConfigArgs`). That reconfigures *this one process*, leaving
`~/.codex/config.toml` and ordinary `codex` runs untouched. The generic backend
is unaffected; codex's config is codex's.

The pairing that motivates it: `workspace-write` keeps the file restrictions but
disables network access, which is what stops a tool like `gh` from working, while
codex separately filters the environment it hands to commands — so the token the
shell exported may not arrive either.

```toml
[codex]
sandbox_mode = "workspace-write"

[codex.sandbox_workspace_write]
network_access = true

[codex.shell_environment_policy]
inherit = "all"
```

Two properties matter more than the field list:

- **Unset is not false.** The bools are pointers, so a field the user did not
  write produces no override at all. Emitting codex's own default instead would
  quietly overrule whatever their `config.toml` says — an omitted setting has to
  mean "not klein's business", not "off".
- **Values are escaped as TOML** via JSON encoding, which for strings and string
  arrays is the same encoding: Go emits exactly the escapes a TOML basic string
  accepts and never JSON's `\/`. This is not cosmetic — codex falls back to
  treating an unparseable value as a *literal string*, so a mis-escaped path or
  token would be accepted and silently mean something else.

`shell_environment_policy.inherit` is validated against `core|all|none` before
the spawn, because codex checks it at startup and exits with `unknown variant`,
which reaches the user as a spawn failure naming neither the setting nor the file
it came from. The rest of the keys were confirmed against codex-cli 0.144.1's own
`--strict-config` validator, which rejects any field it does not recognise.

**Cancellation starts with who gets the signal.** All of the below assumes the
backend is still alive to be interrupted, and by default it was not: a terminal
delivers Ctrl+C to *every* process in the foreground group, and an app-server
installs no SIGINT handler (codex-cli 0.144.1 dies with signal 2). So the
interrupt klein meant as "stop this turn" killed the backend outright, and every
prompt after it failed on a dead pipe — `codex turn/start EOF`. `spawnStdio`
therefore starts the child in its own process group
(`detachFromTerminalSignals`), leaving Ctrl+C for klein alone to interpret via
the machinery below. Nothing leaks: `Close` shuts the child down normally, and a
klein that dies without it closes the stdin pipe, which is the stdio-server
contract for "exit".

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

That leaves one window: a cancellation *between* sending `turn/start` and reading
its reply, where a turn may exist that klein cannot name. `startTurn` therefore
does not tie the request to ctx — a request already on the wire has a turn behind
it whether or not klein is still listening — and on cancellation waits
`startTurnGrace` for the id before giving up, so the interrupt has something to
target. An id that arrives *after* that grace is not dropped either — by then
`runTurn` has returned and cannot act on it, so the pending request interrupts
the turn itself; ownership of that duty passes under a lock, so exactly one of
the two does it. A context already canceled on entry starts no turn at all.

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
| App-server protocol client (standalone, no klein deps) | `pkg/agentserver/` |
| klein's side of that client (adapters, settings, allowlist) | `internal/agentbackend/` |
| Tools (composite, deferred, filesystem, memory, schedule, …) | `internal/tool/` |
| Skills (load, filter, embedded SKILL.md) | `internal/skill/` |
| Connect server (embed + blocking) | `internal/connectrpc/` |
| Gateway (bus, sessions, memory, scheduler, discord) | `internal/gateway/` |
| Settings + base_dir + claw block | `internal/config/settings.go` |
| Config reference | `doc/CONFIGS.md` |
