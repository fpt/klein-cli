# Repository Review — Findings & TODO

Reviewed 2026-06-10 (full read-through); reordered by severity and being worked
2026-06-14. File:line references point at the code around commit `cd530f8`.
Tasks are ordered so they can be worked top-to-bottom. Checkboxes track progress.

Severity tiers:
- **P0** — security or correctness bugs that bite in normal use. Fix first.
- **P1** — high-value correctness/cost/reliability, cheap to fix.
- **P2** — real but larger/behavioral; needs care or tooling (proto regen).
- **Nits** — small cleanups, dead code, doc drift.

---

## P0 — security / correctness (fix first)

- [x] **Bash approval is dead code (security).** `react.go:600` switched on
  lowercase `"bash"` but the tool is `"Bash"` post-rename, so the case never
  matched, `command` stayed empty, and **every** bash command auto-approved in
  interactive mode. Also: the whitelist was a hardcoded copy ignoring
  `settings.Bash.WhitelistedCommands`, and prefix matching is bypassed by
  chaining (`git diff; rm -rf ~`). Fixed: correct the tool name, reject
  command-chaining/redirection metacharacters (`;`, `&&`, `||`, `|`, newline,
  `>`/`<`, `&`) so they always require approval, and consult the user's
  configured whitelist via a new `SetBashWhitelist` setter wired from settings.

- [x] **Permission rules never match Write/Edit.** `agent.go:643`
  `extractPermissionArg` read `args["path"]` but the tools register `file_path`,
  so path-pattern rules could never match and "Always (save to project)" saved a
  blanket `*` allow-all. Fixed to read `file_path`.

- [x] **`go vet` failure: mutex copied by value.** `plain_handler.go:116,123`
  did `nh := *h` on a struct containing `sync.Mutex`. Fixed by making the mutex
  a shared `*sync.Mutex`. Wire `go vet` into `make lint` so this can't reland.

- [x] **Shared LLM client mutated across agents.** `client_factory.go:30`
  returned the *same* client instance with a swapped tool manager, so a
  `spawn_agent` sub-agent clobbered the parent's tool list mid-run. Fixed:
  `NewClientWithToolManager` now always builds a fresh wrapper from the shared
  core; `lastUsage` moved into the cores (anthropic/openai/gemini) so token
  reporting on the original client still works.

- [x] **Connect server mutates shared settings across sessions.**
  `server.go:61` did `settings := s.settings` (pointer) then mutated
  `LLM.Model`/`Agent.MaxIterations`, leaking overrides into all future sessions
  and racing concurrent `StartSession`. Fixed with a per-session value copy.

- [x] **Thinking-channel goroutine leak + nil-close panic.** `agent.go:530`
  placed `defer reactClient.Close()` after the early error return, leaking the
  drainer goroutine on every failed Invoke; `react.go:215` `close()` could panic
  on a nil channel. Fixed: `Close()` is idempotent/nil-safe and the defer is
  hoisted to right after construction in both `Invoke` and `InvokeWithOptions`.

- [x] **Concurrent `stream.Send` race (serve mode).** `server.go:147` sent on
  the Connect stream from both the Invoke goroutine and the ReAct thinking-drain
  goroutine. Fixed by serializing all sends through a per-Invoke mutex.

- [x] **Gateway runs concurrent invokes on one session.** `gateway.go:89`
  spawned a goroutine per inbound message; two messages from one peer raced on
  the same non-thread-safe session/state and overwrote the event handler. Fixed
  with a per-session invoke lock.

- [x] **Server-side agent sessions never released.** `server.go` `sessions`
  map only grew — a long-running `--serve` leaked an agent per gateway
  session-timeout cycle. Fixed without a proto change: sessions are now indexed
  by persistence key, and `StartSession` evicts the prior session for the same
  key (bounding growth to distinct peers); `ClearSession` also drops the entry.
  *A dedicated `EndSession` RPC + idle eviction for keyless sessions remains as
  a P2 improvement below.*

## P1 — high value, cheap

- [x] **Anthropic token usage lost on tool-call turns.** `anthropic/client.go`
  returned early for tool calls *before* setting `lastUsage`, so the majority of
  agentic turns under-reported tokens and weakened compaction triggers. Fixed by
  capturing usage before the tool-call branch.

- [x] **Anthropic prompt cache defeated by random tool order.**
  `anthropic/util.go` built the tool list by ranging a Go map, so the cached
  prefix differed every request and the `cache_control` marker never hit. Fixed
  by sorting tools by name before conversion.

- [x] **Literal `$HOME` directory created in the repo.** Config paths were used
  without env expansion, creating a real `./$HOME/` dir. Fixed: `os.ExpandEnv`
  applied to gateway `working_dir`/`base_dir`/`sessions_dir`; stray dir removed.

- [x] **`ListScenarios` advertises a non-existent `respond` skill.**
  `server.go` returned a hardcoded list including `respond` (deleted) while the
  gateway/help/docs still referenced it. Fixed to enumerate the loaded SkillMap
  (user-invocable only).

- [x] **Skill priority comment contradicts code.** `loader.go` comment said
  "first-comer-wins" but the code is highest-priority-wins (project over
  personal). Fixed the comment to match the implementation.

## P2 — real but larger / needs care (deferred, documented)

- [ ] **`postCompactRestore` is a no-op.** `agent.go:1053` re-injects files as
  *situation* messages, which `react.go:259` strips before the first LLM call.
  Needs a non-situation carrier (or fold into the summary) — behavioral, test
  carefully.
- [~] **Compaction correctness.** Partly fixed: `performCompaction` now clears
  only in-memory (`clearInMemory`) instead of deleting the persisted file before
  re-save, closing the data-loss window (esp. mid-run compaction, which has no
  save of its own). Still open: `CompactIfNeeded` ignores its `thresholdPercent`
  arg (always uses the constant); system messages (skill/memory/catalog) in the
  compacted range vanish mid-run; `react.estimateContextWindow` string-matches
  the client type name instead of using `domain.ContextWindowProvider`.
- [ ] **Anthropic system prompt sent as user message.** `util.go:340` prefixes
  system content with `"System: "` and sends it as a user turn instead of the
  native top-level `system` param — weakens instruction priority and the
  cacheable prefix. Restructure message conversion.
- [ ] **Anthropic drops thinking blocks on parallel tool calls.**
  `client.go:341` discards per-call thinking/signature for batches; replaying a
  turn with `tool_use` blocks but no signed thinking risks 400s when extended
  thinking is on.
- [ ] **Session lifecycle RPC (`EndSession` + idle eviction)** — fixes the P0
  leak above; needs `agent.proto` change, `make proto`, gateway wiring.

## Nits / cleanups / dead code

- [ ] `Skill.Model` parsed (`skill.go:93`) but never used — implement per-skill
  model override or drop it.
- [ ] `extractPermissionArg` for MultiEdit only checks the first edit's path.
- [ ] Dead code: `BashToolManager.handleRunGrep`, `IsCommandWhitelisted`/
  `RequiresApproval` (now partly used? verify), `MessageState.GetValidConversationHistory`,
  `AnthropicClient.cacheOpts`.
- [ ] Duplicate constants: `app.DefaultAgentMaxIterations=10` vs
  `config.DefaultAgentMaxIterations=30`.
- [ ] `splitArguments` comment claims quote-awareness; it's `strings.Fields`.
- [ ] Bash timeout error reports `m.maxDuration` even with a per-call timeout;
  numeric `timeout` arg rejects string inputs some models send.
- [ ] `react.go:305` writes `"\r...\r"` straight to stdout from the domain layer
  (corrupts `--serve` where writer is `io.Discard`).
- [ ] `emitEventWithIteration` reaches into `SimpleEventEmitter` internals.
- [ ] Anthropic model map silently maps unknown names to Sonnet 4.6 — warn.
- [ ] `convertArgumentToAnthropicProperty` infers a hardcoded todo schema from a
  description substring — declare explicit `Properties` on the todo tool.
- [ ] `unsanitizeToolNameFromAnthropic` guesses original names via heuristics —
  keep a per-request sanitized→original map.
- [ ] Vision media-type sniffing only does PNG vs JPEG (GIF/WebP mislabeled).
- [ ] `tool_results/` offload dir grows unbounded; stubs reference absolute paths.
- [ ] `@file` includes (`agent.go:465`, `skill.go:189`) are duplicated and read
  via `os.ReadFile`, bypassing the filesystem allowlist/blacklist (`@/etc/passwd`).
  Route through the secure filesystem manager; extract one helper.
- [ ] Gateway `!skill` accepts unvalidated names.
- [ ] REPL status counts `"👤 You:"` substrings instead of asking state.
- [ ] OpenAI default model `gpt-5.4-mini` vs docs' `gpt-4o`; stale CLAUDE.md.
- [ ] `pkg/` imports `internal/` — the public/private split is cosmetic.
- [ ] Three overlapping memory systems (agent prompt, gateway injection, memory
  tools) — consolidate on the system-prompt + tools approach.
- [ ] Events: `ToolResultData.ToolName`/`CallID` always empty — thread IDs through.
- [ ] Web tools have no SSRF guard (relevant once exposed via Discord).

## Testing & tooling

- [x] Add `go vet ./...` to `make lint` (would have caught the mutex bug). New
  `vet` target; `lint` now depends on it.
- [x] Add a unit test on `bashCommandRequiresApproval`
  (`pkg/agent/react/approval_test.go`) covering the PascalCase regression,
  whitelist word-boundary matching, and shell-chaining bypass.
- [ ] Remove or substantiate the "99% coverage" claim in CLAUDE.md; add
  serve-mode and gateway smoke tests.
- [ ] Sweep for other lowercase tool-name remnants after the PascalCase rename
  (`spawn_agent` is still snake_case — confirm intentional).
