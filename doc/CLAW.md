# klein-claw: Messaging Gateway

klein-claw is an OpenClaw-inspired messaging gateway that turns the klein agent into a personal AI assistant accessible via Discord (and other platforms in the future).

## Current State (MVP)

The MVP is functional with the following components:

- **Connect-gRPC server** (`--serve` mode) — Exposes the agent via HTTP/2 with session management
- **Gateway binary** (`cmd/gateway`) — Routes messages between Discord and the agent
- **Discord adapter** — Bot with allowlists, mention-only mode, typing indicators, 2000-char splitting
- **Memory system** — MEMORY.md (long-term) + daily notes, injected into prompts
- **Session routing** — Per-channel/peer sessions mapped to Connect RPC sessions
- **Heartbeat** — Configurable periodic prompt execution
- **Claw skill** — Messaging-optimized assistant with memory awareness

### What Works

- Send a message to the Discord bot, get a response from the agent
- Agent has full tool access (read/write files, bash, web search) in the configured working directory
- Memory context injected into every prompt; agent can update MEMORY.md and daily notes
- `!clear`, `!skill`, `!memory`, `!help` commands
- Typing indicator while the agent is thinking/running tools
- Message splitting for responses over 2000 characters

### Known Limitations

- Responses are sent only after the agent finishes (no streaming/progressive updates)
- Images in Discord messages are ignored (the `Images` field exists in `InboundMessage` but is not wired)
- No tool use visibility — the user can't see what tools the agent is using
- All tool calls are auto-approved (no interactive approval via messaging)
- No session timeout or cleanup — sessions accumulate in memory
- No rate limiting or cost controls
- Single-process gateway — no horizontal scaling

---

## Roadmap

### Phase 1: Tool Visibility and Streaming

**Goal:** Make the agent's work visible to the user in real-time.

**Streaming responses:**
- Instead of waiting for the final message, send incremental updates as the agent works
- Show a brief status update when tool calls start (e.g., "Reading main.go...")
- Send partial text as `AssistantDelta` events arrive, editing the Discord message in place
- discordgo supports editing messages — use this for progressive updates

**Tool call summaries:**
- The gateway already receives `ToolCall` and `ToolResult` stream events but ignores them
- Surface tool activity as a compact summary, either inline or in a separate "status" message
- Example: after the final response, append a collapsed tool log:
  ```
  [tools: Read(main.go) -> bash(go build) -> Write(server.go)]
  ```

**Thinking visibility (optional):**
- `ThinkingDelta` events are already streamed but discarded
- Optionally show abbreviated thinking in a thread or spoiler block

### Phase 2: Memory Improvements

**Goal:** Make memory more intelligent and less dependent on the agent remembering to update it.

**Automatic memory extraction:**
- After each conversation, run a lightweight summarization pass
- Extract key facts, preferences, and action items automatically
- Append to MEMORY.md without requiring the agent to explicitly use the Write tool
- Could use a small/fast model (e.g., Haiku) to keep costs low

**Structured memory:**
- Current MEMORY.md is free-form markdown — useful but hard to query
- Add structured sections: `## User Preferences`, `## Ongoing Projects`, `## Key Facts`
- Memory manager could parse and merge updates into the right sections
- Consider a separate `facts.json` for machine-readable facts alongside the human-readable markdown

**Memory search:**
- As MEMORY.md grows, injecting all of it into every prompt becomes expensive
- Add semantic search over memory entries — only inject relevant context per message
- Could use embeddings (local via Ollama or API) to find relevant memory fragments
- The `!memory search <query>` command could expose this to the user

**Daily note automation:**
- Heartbeat already supports periodic prompts
- Add a "daily review" prompt that summarizes the day's interactions into a daily note
- Auto-prune old daily notes beyond `max_notes` (currently configured but not enforced)

**Memory across peers:**
- Currently memory is global — all users share one MEMORY.md
- Add per-peer memory directories: `memory/{peer_id}/MEMORY.md`
- Shared memory stays in the global MEMORY.md, personal facts go to per-peer files

### Phase 3: Richer Discord Integration

**Goal:** Use more Discord features for a better UX.

**Image support:**
- `InboundMessage.Images` field exists but is not populated
- Extract image attachments from Discord messages, download them, pass as base64 to the agent
- Requires vision-capable models (Claude, GPT-4o, Gemini)
- Could also send generated images/charts back via Discord file uploads

**Discord threads:**
- Long conversations clutter channels — use Discord threads instead
- On first response in a guild channel, create a thread and continue there
- Thread title could be auto-generated from the first message

**Reactions and embeds:**
- Use Discord reactions for lightweight feedback (thumbs up/down on responses)
- Use embeds for structured output (code blocks, file previews, tool results)
- Reactions could drive memory: thumbs up = "remember this", thumbs down = "forget this"

**Slash commands:**
- Migrate `!` commands to Discord's native slash commands (`/clear`, `/skill`, `/memory`)
- Provides autocomplete, descriptions, and a better UX than prefix commands
- Register commands on bot startup via discordgo's application command API

**Voice channel integration:**
- Join a Discord voice channel, transcribe speech, send to agent, TTS the response
- Requires an STT/TTS pipeline — could use Whisper (local via Ollama) + a TTS service

### Phase 4: Tool Approval via Messaging

**Goal:** Bring the interactive approval system to messaging platforms.

**Approval flow:**
- Currently the Connect server creates agents with `alwaysApprove=true`
- Add an approval mode where destructive tool calls (Write, Edit, bash) require user confirmation
- Gateway sends an approval request message with the tool call details
- User reacts (checkmark/X) or replies (yes/no) to approve or deny
- Gateway forwards the approval back to the agent via Connect RPC

**Trust levels:**
- Configurable per-user trust levels: `full` (auto-approve all), `safe` (auto-approve reads, prompt for writes), `strict` (prompt for everything)
- Default to `safe` for new users
- `!trust full` / `!trust safe` / `!trust strict` commands

**Timeout handling:**
- If the user doesn't respond to an approval request within N minutes, auto-deny and inform the agent
- The agent can then adjust its approach (e.g., describe the change instead of making it)

### Phase 5: Multi-Adapter Support

**Goal:** Support more messaging platforms beyond Discord.

**Telegram adapter:**
- Telegram Bot API is simpler than Discord's — HTTP webhooks or long polling
- Markdown formatting differs (Telegram uses its own MarkdownV2 syntax)
- Supports inline keyboards for approval flows
- 4096-char message limit (vs Discord's 2000)

**Slack adapter:**
- Slack Bot with Socket Mode (WebSocket) or Events API
- Rich message formatting with Block Kit
- Thread support maps well to the session model
- Slack's 40k-char limit means less message splitting

**LINE adapter:**
- LINE Messaging API with webhooks
- Important for Japanese user base
- 5000-char limit, supports Flex Messages for rich formatting

**Adapter abstraction improvements:**
- The `Adapter` interface is already clean, but `OutboundMessage` needs richer fields:
  - `Attachments []Attachment` for files/images
  - `Format string` for adapter-specific formatting hints
  - `Metadata map[string]string` for adapter-specific data (thread ID, etc.)

### Phase 6: Session and Resource Management

**Goal:** Production-ready session lifecycle and resource controls.

**Session timeout and cleanup:**
- Add a TTL to sessions (e.g., 30 minutes of inactivity)
- Background goroutine sweeps expired sessions, calls `ClearSession` on the agent
- Configurable: `session_timeout: "30m"` in config

**Cost controls:**
- Track token usage per user/session via `TokenUsage` in stream events
- Configurable limits: `max_tokens_per_day: 100000`, `max_tokens_per_message: 10000`
- Warn users approaching limits, hard-stop when exceeded
- `!usage` command to show current token consumption

**Rate limiting:**
- Per-user rate limiting: max N messages per minute
- Global rate limiting: max concurrent agent invocations
- Queue overflow handling — respond with "I'm busy, please wait"

**Persistent sessions:**
- Currently sessions are lost on gateway restart
- Serialize session state (key -> agent session ID mapping) to disk
- On startup, attempt to reconnect to existing agent sessions
- Falls back to creating new sessions if the agent was also restarted

### Phase 7: Observability and Operations

**Goal:** Make it easy to monitor and debug the gateway in production.

**Metrics:**
- Message count (inbound/outbound) per adapter, per user
- Response latency (time from inbound message to outbound response)
- Tool call frequency and duration
- Token usage per invocation
- Error rates and types
- Export via Prometheus endpoint or structured log entries

**Health check endpoint:**
- HTTP health endpoint on the gateway process
- Checks: agent connectivity (Connect RPC ping), Discord WebSocket state, memory directory writable
- Useful for process supervisors and monitoring

**Admin commands:**
- `!admin sessions` — List active sessions
- `!admin stats` — Token usage, message counts
- `!admin restart` — Restart a session without clearing memory
- Restricted to configured admin user IDs

---

## Architecture Notes

### Why Two Processes?

The agent and gateway run as separate processes for good reasons:

1. **Agent reuse** — The same agent serves CLI, gateway, and future IDE integrations via the same Connect-gRPC API
2. **Independent scaling** — The gateway is I/O-bound (WebSocket, HTTP), the agent is compute-bound (LLM calls). They have different resource profiles.
3. **Independent restarts** — Update the gateway (new Discord features) without interrupting long-running agent sessions, and vice versa
4. **Security boundary** — The gateway handles external input (Discord messages from the internet). Keeping it separate from the agent (which has filesystem and bash access) limits the blast radius.

### Memory Architecture

The current memory system is deliberately file-based:

```
~/.klein/claw/memory/
├── MEMORY.md          # Long-term facts (injected into every prompt)
└── daily/
    ├── 2025-06-01.md  # Daily journal notes
    ├── 2025-06-02.md
    └── ...
```

The agent reads and writes these files using its standard filesystem tools. The gateway's `MemoryManager` reads them for prompt injection. This means:

- Memory is human-readable and editable (just markdown files)
- The agent manages its own memory using tools it already has
- No additional database or embedding infrastructure required
- Easy to back up, version control, or reset

The tradeoff is that full-file injection doesn't scale — once MEMORY.md grows past a few thousand tokens, we'll need the search-based approach described in Phase 2.

### Adapter Pattern

Adding a new messaging platform requires implementing one interface:

```go
type Adapter interface {
    Start(ctx context.Context) error
    Stop() error
    Send(ctx context.Context, msg OutboundMessage) error
    SendTyping(ctx context.Context, channelID string) error
}
```

The gateway orchestrator handles routing, session management, and memory injection. The adapter only needs to translate between the platform's message format and `InboundMessage`/`OutboundMessage`.

## References

- OpenClaw
    - https://github.com/openclaw/openclaw
    - https://docs.openclaw.ai
- ClawHub - skills repository
    - https://clawhub.ai
- PicoClaw (Golang, just for reference)
    - https://github.com/sipeed/picoclaw
