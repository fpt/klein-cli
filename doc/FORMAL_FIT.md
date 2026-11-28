# Formal testing fit for klein (PBT / Lean)

Survey date: 2026-11-23, at `a5b6318`. #1, #2 and #3 are done (see their Status
lines). The workflow is in the `property-test` project skill
(`.claude/skills/property-test/`).

Notation: **[verified]** means checked by running a throwaway copy of the code
(the scripts live in the session scratchpad, not in the repo). **[read]** means
read from the code only.

Fix counts below come from `fix-history.sh`, which counts subjects that start
with `fix`. This repo also has fix commits that don't (e.g. `617ac31 Rejoin
bracketed-paste markers split across reads`), so the counts are a lower bound.

## Ranking

| # | Candidate | Method | Size | Why this rank |
|---|---|---|---|---|
| 1 | `isSafeSplitPoint` / compaction split (`pkg/agent/state/compaction.go:448`) | PBT against a reference model | S | **Confirmed bug**: rejects every split once any tool call appears earlier in the history. Pure, ~50 lines, no tests at all |
| 2 | `BracketedPasteReader.processBytes` (`internal/app/bracketed_paste.go:123`) | PBT (chunking invariance) | S | Has a past fix (`617ac31`) for a boundary bug. The invariant holds today, so the property test locks it in |
| 3 | Review diff parser and commentable ranges (`internal/review/diff.go`, `enrich.go:88-112`) | PBT | M | **Done.** Highest fix rate (66% / 57%). Pure. D1 found an edge case (a final bare empty line is dropped) |
| 4 | Grep/Glob fallback parity (`internal/tool/search_tool_manager.go`) | Differential PBT (rg vs grep/find) | M–L | 7 of 12 commits are fixes. Two implementations of one contract. Depends on external binaries and a temp FS |
| 5 | Path allowlist containment (`filesystem_tool_manager.go`, `enrich.go:139`) | PBT on the lexical part | M | Security-relevant (`0550ea4`, `d0f5398`, `75e35fa`). The symlink half needs a real FS |
| 6 | Command allowlist (`internal/agentbackend/autoapprove.go:99`, `pkg/agent/react/react.go:640`) | Differential PBT with a shell parser as oracle | M | Two implementations of one security rule. **Default-whitelist issue found** (see §6), which is a policy decision, not a test target |
| — | `pkg/agentserver` runner, gateway scheduler reconcile, background agent runs | **Out of scope: TLA+ model-checking candidates** | — | Concurrency and reconnect bugs (see below). Function-level PBT/Lean won't find design errors there |
| — | `nextCronFire` (`internal/gateway/schedules.go:221`) | Not a target | — | Delegates to `robfig/cron`. klein adds only the timezone check |

**Start with #1.** The effect can be stated in numbers: on the history
`[user, call, result, assistant, user, call, result, assistant]`, the
implementation rejects all 7 split points while a correct check accepts 5. It
also takes the least time end to end: one pure function, a ~15-line reference
model, and no generator beyond a slice of message kinds.

At survey time the repo had no PBT. `pgregory.net/rapid` was added with #1.
**Lean is not recommended** for any candidate (see "Lean" below).

---

## 1. Compaction split point — `pkg/agent/state/compaction.go`

- **Location**: `findSafeSplitPoint` :432, `isSafeSplitPoint` :448, `findConversationBlocksToPreserve` :623, `findTurnBoundary` :90.
- **Purity**: pure on `[]message.Message`. Only `Type()` is used, so a generator over message kinds is enough.
- **Rule**: compaction must never separate a tool call from its result (Anthropic's API rejects orphans).
- **Method**: PBT with a reference model. Lean is not worth it because the logic is tiny once correct.

**Finding [verified]**: both scans in `isSafeSplitPoint` count in the wrong
direction.
- The backward scan over `[0, split)` increments on a *call*. A result is seen before its own call when scanning backward, so it is ignored, and every earlier call that was already answered still counts as "unpaired".
- The forward scan has the mirror-image error.

Copied with simplified types and run:

```
history: U C R A U C R A
split=1 impl=false ref=true
split=3 impl=false ref=true
split=4 impl=false ref=true
split=5 impl=false ref=true
split=7 impl=false ref=true
```

Consequence [read]: `performCompaction` reaches `findSafeSplitPoint` only on the
fallback path (:305). That happens when the block-based path does not qualify:
the preserved blocks are under 10 messages, or not at least 5 messages short of
the eligible slice. On that path, any session that has used a tool gets "No safe
split point found, skipping compaction" (a debug log), so the context keeps
growing. Not confirmed against a live session.

No test references `isSafeSplitPoint`/`findSafeSplitPoint`. The
`CompactIfNeeded` tests use histories that take the block path.

**Theorem candidates**
1. If `isSafeSplitPoint(m, s)`, every tool call in `m[:s]` has its result in `m[:s]`, and every result in `m[s:]` has its call in `m[s:]`.
2. Conversely, a split that breaks no pair is reported safe (completeness against the reference).
3. `0 ≤ findSafeSplitPoint(m, k) ≤ len(m) - k`, and if it is non-zero it is safe.
4. `findConversationBlocksToPreserve(m, n)` is a suffix of `m`. Its first element is a user message unless it contains the whole history.
5. In `findTurnBoundary(m, n)`, the slice `m[boundary:]` contains at least `min(n, #user)` user messages, and `m[boundary]` is a user message whenever the boundary is non-zero.

**Status: fixed, with ID-based pairing.** A result's `ID()` is its call's ID
(`react.go` `handleToolCall`). A batch is written as all calls, then all
results. A cancelled batch leaves calls with no result, and those belong to no
pair. S1–S3 are implemented in `pkg/agent/state/split_property_test.go`, using
the generator in `pkg/testhelper/messagegen`. Theorems 4–5
(`findConversationBlocksToPreserve`, `findTurnBoundary`) are not done yet.

## 2. Bracketed-paste reader — `internal/app/bracketed_paste.go`

- **Location**: `processBytes` :123, `finalizePaste` :189, `partialMatchSuffix` :223. State machine over a byte stream; pure apart from its own buffers.
- **Evidence**: `617ac31` (start/end markers split across reads were never rescanned). 16 example tests exist, but none cover arbitrary chunkings.
- **Method**: PBT.

**Check [verified]**: I copied the file and fed 200k random inputs, built from
`a \n ESC [ 2 0 1 ~ é` and the full markers. Each input was fed both whole and
cut into 1–4-byte chunks. Output, segments, held-back bytes and `inPaste` were
identical every time, so the property holds today.

**Theorem candidates**
1. **Chunking invariance**: any split of the input into reads yields the same output, segments and final state as a single read.
2. Input with no `ESC[200~` passes through unchanged, except for a trailing proper prefix of the marker, which is held back.
3. Each segment is exactly the bytes between one start/end pair. The output contains exactly one placeholder `#i` per segment, in order.
4. A paste is inlined iff it has no `\n` and at most 120 runes (`maxInlinePasteRunes`).

**Status: done, no bug found.** `internal/app/bracketed_paste_property_test.go`,
with the generator in `pkg/testhelper/pastegen`, covers the candidates above as:
- **P1**: chunking invariance (candidate 1).
- **P2**: the reader matches a byte-at-a-time reference model, which covers candidates 2–4 together.
- **P3**: the same through `Read`, for any terminal read size and any readline buffer size.

The mutation check is in `.claude/skills/property-test/mutations/bracketed_paste.tsv`.
Every real mutation is caught by P2 or P3. Three are caught *only* by the
properties, not by the 16 example tests:
- bytes counted as runes;
- an off-by-one placeholder index;
- `Read` skipping buffered output.

**Dead code [verified by mutation]**: the `seqBuf` flush in `finalizePaste`
never runs, because `processBytes` empties `seqBuf` on entry. Removing it
changes nothing.

**Team decisions (pinned, not changed)**:
- `TestBracketedPasteReader_LoneEscWaitsForNextRead`: a read that ends in
  `ESC` (or `ESC[`, `ESC[2`…) is held until more bytes arrive. A lone Esc
  keypress therefore reaches readline only with the next keystroke. Is that
  intended? Not observed interactively.
- `TestBracketedPasteReader_DropsHeldBytesAtEOF`: at EOF, a held partial marker
  and the content of an unterminated paste are discarded. Reachable only if
  stdin closes mid-sequence; unverified whether that happens in practice.

## 3. Review diff parser and ranges — `internal/review/`

- **Location**: `ParseUnifiedDiff` diff.go:145 and the `diffParser` methods, `CommentableRanges` :123, `Ranges.Validate` :71, `truncateAtLine` / `applyByteBudget` enrich.go:88–112. All pure.
- **Evidence**:
  - `diff.go`: 3 commits, 2 fixes (`2bc7f11` a deleted Lua `--` comment parsed as the next file's header; `6db3cde`).
  - `enrich.go`: 7 commits, 4 fixes (`7d84df7` partial line on truncation; `5bcebad` over budget for tiny `--max-diff-bytes`; `75e35fa` symlink traversal).
  - Tests: 8 + 9, all example-based.
- **Method**: PBT with a generator of well-formed diffs. The generator should deliberately emit body lines that start with `--- `, `+++ `, `\` and empty context lines. `truncateAtLine` and `Validate` are small enough for a Lean model plus a differential test, but the payoff over PBT is low.

**Theorem candidates**
1. Round trip: rendering a generated `[]FileDiff` and parsing it gives back the same paths, hunk headers and line kinds/contents. This holds even when content lines look like headers.
2. Counting: in each parsed hunk, `#added + #context == NewLines` and `#removed + #context == OldLines`, provided the header is truthful.
3. Every added or context line's `NewNumber` passes `Validate(path, n, 0)`. Every line outside the hunks is rejected.
4. `truncateAtLine(s, n)` is a prefix of `s`, has length at most `n`, and is either `s` itself or ends in `\n`. `applyByteBudget(out)` has length at most `maxBytes` whenever `maxBytes > 0`.
5. `CommentableRanges` gives a deleted file an empty, non-nil entry.

**Status: done.** `internal/review/diff_property_test.go`, with the generator
in `pkg/testhelper/diffgen` (git and plain `diff -u` output; header-forging
contents, bare empty context lines, `\ No newline`, no trailing newline,
omitted `,1` counts, zero-count sides, new/deleted/renamed/binary/empty files).
The properties:
- **D1**: round trip. Parsing gives back every path, flag, hunk header, line
  kind, content and old/new number the model encodes (covers candidates 1–2).
- **D3**: `Validate(path, line, end)` accepts exactly the ranges whose lines
  are all added/context lines of one hunk, and rejects paths not in the diff,
  a rename's old path included (candidate 3).
- **D4 / D4b**: `truncateAtLine` is the longest line-boundary prefix within
  `n` bytes, checked against a brute-force reference. `applyByteBudget` returns
  the input unchanged when it fits, otherwise stays within budget and keeps the
  marker whenever it fits (candidate 4).
- **D5**: a deleted file has a present, empty entry (candidate 5).
- **D6**: the guarantee stated on `diffParser.feed`. In git format, a
  miscounted hunk header never swallows the next `diff --git` file.

Mutation check: `.claude/skills/property-test/mutations/review_diff.tsv`. All
19 mutations are caught by a property, including the reverses of `2bc7f11`,
`7d84df7` and `5bcebad`. Six are caught *only* by a property, not by the
example tests:
- `diff --git` made conditional on the hunk (D6);
- `\ No newline` read as context;
- old-side numbering started from the new start;
- every bare empty line dropped;
- a timestamp left on a plain-format path;
- `truncateAtLine` cutting at `n` inclusive.

**Team decisions (pinned in `diff_property_test.go`, not changed)**:
- `TestParseUnifiedDiff_DropsFinalBareEmptyContextLine` **[verified, found by
  D1]**: when the text has no trailing newline and its last line is an empty
  context line written bare (`""`), that line is taken for the end of the text
  and dropped. The hunk ends up one line short of its header. A one-line fix
  would be to drop the final `""` only when `!p.inHunkBody()`. Not reachable
  from the GHA harness: git writes `" "`, and `jq --rawfile` keeps the trailing
  newline. Unverified for other callers of `klein review --input`.
- `TestParseUnifiedDiff_UndercountLetsBodyForgeHeader`: once a header's counts
  run out, a later `+++ x` body line renames the *same* file to `x`, and a
  `--- x` line starts a spurious file. The next git file is unaffected (D6).
  Practical only for hand-edited or truncated diffs.
- `TestParseUnifiedDiff_PlainOvercountSwallowsNextFile`: in plain format,
  `--- ` is the only file separator, so an overcounting header swallows the next
  file into the previous one. Git format is protected.
- `TestParseUnifiedDiff_PlainDiffCommandLineIsContext`: GNU `diff -ruN` writes a
  `diff -ruN a/y b/y` line before each file. After a hunk, the parser reads it as
  one extra context line of that hunk. The diff harness produces git format, so
  this only matters if plain diffs are ever fed in.

## 4. Grep/Glob fallback parity — `internal/tool/search_tool_manager.go`

- **Evidence**: 12 commits, 7 fixes (`d3aaf99` fallback answered in the wrong shape; `4f1cc48` negated glob; `77fb1ae` unreadable file / single-file path; `c69a49a` `-A/-B/-C/-n` dropped; `48c745a`).
- **Multiple implementations**: ripgrep (`runRipgrep`/`ripgrepFlags`) and BSD/GNU grep (`runGrep`/`grepFlags`/`grepGlobFlags`). Both answer the same tool contract; `grepCannotExpress` marks where they diverge on purpose.
- **Method**: differential PBT. Generate a small temp tree and grep arguments, then require both backends to give equal results after normalization, unless `grepCannotExpress` rejects the arguments. Skip the test when `rg` is absent. The pure pieces (`resolveOutputMode`, `grepGlobFlags`, `headLimit`, `dropZeroCounts`) can get plain PBT first.

**Theorem candidates**: same output shape for the same `output_mode`; same
file set for the same glob, negations included; `headLimit` returns a prefix
with at most N lines.

## 5. Path allowlist containment

- **Evidence**: `0550ea4` (allowlist must follow where a path leads, not how it is spelled), `d0f5398` (Glob/Grep weren't bounded), `75e35fa` (symlink traversal in enrichment).
- **Method**: PBT on the lexical layer: random `..`, `.`, `//` and absolute segments, with the property "accepted ⇒ the cleaned path is under an allowed root". The symlink layer needs a temp FS fixture, which is still feasible in a test.

## 6. Command allowlist — `autoapprove.go` and the native Bash approval

- **Location**:
  - App-server backends: `commandAllowed` in `internal/agentbackend/autoapprove.go:99`, with `shellMetacharacters` at :27. The allowlist comes from settings and is empty by default.
  - Native ReAct Bash tool: `bashCommandRequiresApproval` in `pkg/agent/react/react.go:640`, with `containsShellChaining` at :671 and `isCommandNotWhitelisted` at :693. The default list is `defaultBashWhitelist` at :682, which mirrors `config.GetDefaultSettings().Bash.WhitelistedCommands`.
- **Evidence**: `35060cb` (P0: Bash approval was dead code; shell-chaining detection added). The same rule is implemented twice, with independently maintained metacharacter lists.
- **Divergence [read]**: only `autoapprove.go` lists `\r`. This is harmless: `\r` is not a command separator in sh. A `\r` directly after the program name already fails the word-boundary check in both versions. It differs only after a space (e.g. `ls -la\rfoo`).
- **Method**: differential PBT. Generate command strings; whenever either function approves, parse the string with a real shell parser (`mvdan.cc/sh/v3/syntax`). Assert:
  - the parse is exactly one simple command;
  - it has no redirections and no command or process substitution;
  - its leading words equal an allowlist entry.

  Also assert that the two implementations agree.

**Theorem candidates**
1. Approved ⇒ the shell parse is one simple command with no redirection or substitution.
2. Approved ⇒ the command equals an entry, or is the entry followed by a space or tab (the word boundary: `ls` does not approve `lsof`).
3. Adding an allowlist entry never revokes an approval (monotonicity).
4. Both implementations approve the same commands for the same list.

**Security finding [verified] — team decision**: the default Bash whitelist
trusts program *arguments*. The metacharacter check stops command chaining. It
cannot stop a whitelisted program from deleting files, writing files or running
code by itself. I copied both implementations and ran them with
`defaultBashWhitelist`. Both auto-approve all of these:

```
find . -exec rm -rf {} +    approved (deletes)
find . -delete              approved (deletes)
git diff --output=/tmp/x    approved (writes a file)
go run ./evil               approved (runs arbitrary code)
```

[read] `make`, `npm install`, `npm run` and `go test` are on the same list.
Each runs code the repository controls: Makefile recipes, npm lifecycle
scripts, and test code. `go build -toolexec=<prog>` also runs an arbitrary
program. Neither PBT nor Lean addresses this; it is a policy question. Options:
- Drop the arbitrary-code and destructive entries (`find`, `make`, `go run`, `npm install`, `npm run`) from the defaults.
- Add per-program argument denylists (`-exec`, `-execdir`, `-delete`, `-ok`, `--output`, `-toolexec`, …).
- Accept the risk and document what the defaults allow.

Not changed. The app-server allowlist is not affected by default, because it
starts empty.

## Lean

**Not recommended for any candidate.** Lean earns its cost when a proof over
*every* input matters more than a large random sample. It also pays off when one
spec is the oracle for several implementations. Neither holds strongly here:

| Candidate | What Lean could prove | Why it does not pay |
|---|---|---|
| Command allowlist (§6) | One spec, used as the oracle for both implementations | Over strings, the theorem is trivial ("no metacharacter, then prefix match"). The meaningful one is about sh parsing, which needs a shell model in Lean. A real parser as a PBT oracle gives that directly |
| Path containment (§5) | An accepted path, once cleaned, stays under an allowed root | Needs a Lean model of Go's `filepath.Clean`. The symlink half cannot be proved at all. PBT plus a temp-FS fixture covers most of it |
| Bracketed paste (§2) | Chunking invariance for every input and every split | A tidy proof, but P1–P3 already catch every real mutation, and the code has one bug in its history |
| `truncateAtLine`, `Ranges.Validate` (§3) | Prefix, size and range bounds | Easy to prove, but adds nothing over PBT |

Revisit this if klein gains logic where an error costs real money or data, and
that logic is pure and has several implementations — e.g. a usage or billing
calculator, or a permission engine with a written spec.

## Out of scope — model-checking (TLA+) candidates

These are protocol and concurrency bugs. Per-function tests wouldn't have
caught them.

- `pkg/agentserver/runner.go` (17 commits, 4 fixes):
  - `e8ec39b` Close raced a redial for the client handle
  - `edee6a4` / `6d75647` thread identity across reconnects
  - `e70302d` notifications not filtered by thread before dispatch

  A spec of connect / reconnect / thread-start / close with message reordering would be the right tool.
- Gateway: `48f69e9` mutual exclusion for the shared memory/schedule stores; the `Scheduler.reconcile` live reload; the per-session invoke lock from `35060cb`.
- Background agent runs (`internal/app/agent_runs.go`): "each run reports exactly once" across `/clear` and process exit.
