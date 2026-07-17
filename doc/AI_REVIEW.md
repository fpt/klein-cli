# klein review — AI Code Review Design

How `klein review` turns a pull-request diff into a validated set of inline
review comments plus a summary, and how the GitHub Actions harness around it
fetches the PR and posts the result. Read this before touching
`internal/review/`, `internal/tool/review_tool_manager.go`, the `review`
skill, or `.github/actions/ai-review/`.

> Companion docs: [DESIGN.md](DESIGN.md) (the agent/tool/backend stack this
> builds on, esp. §10 on the reason→verify→evaluate loop) and
> [../CLAUDE.md](../CLAUDE.md) (build/run commands).

---

## 1. Design principles

1. **klein never talks to GitHub.** No `git`, no `gh`, no tokens. The harness
   (a GitHub Action, or anything else) fetches the PR and posts the review.
   klein's whole interface is two JSON documents and a working directory.
2. **Comments must land on postable lines.** The GitHub Reviews API rejects
   comments outside the diff — and a single bad comment rejects the *entire*
   review. So line validation is enforced *inside the tool call*, at the
   moment the model tries to add a comment, where it can still self-correct.
3. **Findings are accumulated through tools, not parsed from prose.** The
   model calls `AddInlineReview` / `AddSummaryReview` / `FinalizeReview`;
   the result is read from the tool manager's state after the run. There is
   no fragile "extract JSON from the response" step.
4. **The reviewer is sandboxed read-only.** The agent gets exactly
   `Read, Glob, LS` plus the three review tools — enforced as a hard
   whitelist, not just a skill preference (see §6).
5. **Verify before reporting** — the review skill follows the repo-wide
   reason→verify→evaluate loop (DESIGN.md §10): a suspicion from the diff must
   be confirmed against the actual code via `Read` before it becomes a comment.

## 2. Pipeline

```
┌────────────────────────── GitHub Actions (harness) ──────────────────────────┐
│  1. checkout PR head          (actions/checkout, full depth)                 │
│  2. gh pr view / gh pr diff → review-request.json                            │
│                    │                                                         │
│                    ▼                                                         │
│  ┌──────────────────────── klein review (no git/gh) ─────────────────────┐   │
│  │  3. parse unified diff        internal/review/diff.go                 │   │
│  │  4. derive commentable ranges (new-side hunk spans)                   │   │
│  │  5. enrich hunks ±N ctx lines internal/review/enrich.go  (file reads) │   │
│  │  6. run review agent          skill=review, sandboxed tools           │   │
│  │       AddInlineReview ──▶ validated against ranges, accumulated       │   │
│  │       AddSummaryReview ──▶ summary + verdict                          │   │
│  │       FinalizeReview  ──▶ locks the review                            │   │
│  │  7. write review-result.json                                          │   │
│  └────────────────────────────────────────────────────────────────────────┘  │
│                    │                                                         │
│                    ▼                                                         │
│  8. jq → Reviews-API payload → gh api pulls/N/reviews  (one review)          │
└──────────────────────────────────────────────────────────────────────────────┘
```

Steps 1–2 and 8 live in `.github/actions/ai-review/action.yml`; steps 3–7 are
`klein review` (`klein/review_command.go`).

## 3. I/O contract

**Input** (`--input <file>`, `-` = stdin):

```json
{
  "title": "PR title",
  "body": "PR description",
  "diff": "<unified diff to review>",
  "full_diff": "<complete PR diff>",        // optional; incremental rounds only
  "mode": "full",                            // full (default) | incremental
  "previous_comments": [                     // optional; unresolved earlier findings
    { "id": "PRRT_…", "path": "a.go", "line": 9, "body": "…" }
  ]
}
```

`diff` accepts both git format (`diff --git` + `---`/`+++` headers, rename /
new / deleted markers) and plain unified diffs. On an incremental round the
harness passes only the changes since the last reviewed commit as `diff` and
the complete PR diff as `full_diff` — commentable-line validation always uses
the full diff, because GitHub rejects comments outside it. `previous_comments`
ids are opaque to klein (the harness uses GraphQL review-thread node ids);
they only round-trip through `ResolveReviewComment` into the result.
Type: `review.Request` (`internal/review/prompt.go`).

**Output** (`--output <file>`, stdout when omitted; agent chatter goes to
stderr so stdout stays parseable):

```json
{
  "summary": "markdown review body",
  "verdict": "comment",              // approve | comment | request_changes
  "finalized": true,                 // FinalizeReview was called
  "comments": [
    { "path": "internal/foo.go", "line": 42, "end_line": 44,
      "severity": "major", "body": "..." }   // severity required:
  ],                                          // must | major | minor | nits
  "resolved": [                      // previous-round comments verified fixed
    { "id": "PRRT_…", "note": "divisor restored" }
  ]
}
```

Type: `tool.ReviewResult`. The snake_case keys are deliberate — the harness
maps them straight onto the GitHub Reviews API with jq (`line`/`end_line` →
`line`/`start_line`, `side: "RIGHT"`; verdict → review event). `line` and
`end_line` are always **new-side** (RIGHT) line numbers; `end_line == line`
for single-line comments.

**Exit codes:** 0 = review produced (zero comments is still success),
1 = error, 2 = flag error.

**Output language:** `--language <code-or-name>` (default `en`; e.g. `ja`,
`Japanese`) — injected into the prompt; comments and summary are written in
that language. The composite action exposes it as the `language` input.

**Generated files** are skipped by default: any changed file whose new-side
content carries Go's standard marker (a line matching
`^// Code generated .* DO NOT EDIT\.$` before the package clause — protoc,
sqlc, stringer, mockgen, …) is dropped before enrichment, so the model never
sees it (`review.IsGenerated` / `Enricher.DropGenerated`). If every changed
file is generated, the run short-circuits to an `approve` result naming the
skipped files — no model call. `--include-generated` reviews them anyway.

**Model / effort / limits** (all optional, all surfaced as action inputs):
`-b/--backend`, `-m/--model`, `--effort` (reasoning effort for capable
models), `--max-turns` (agent iteration cap; overrides the settings default),
and `--max-budget-tokens` (cumulative token cap for the run). When the budget
is exceeded the run stops before the next model call and emits a **partial**
result — comments gathered so far plus a "stopped early" summary and
`finalized:false` — exiting 0 so the harness still posts something. The cap is
enforced in the ReAct loop (`SetTokenBudget` → `ErrTokenBudgetExceeded`);
`klein review` recognizes that error and salvages state rather than failing.

**Comment cap** (`--max-comments`, default 15; 0 = unlimited): after the run,
`capComments` sorts the inline comments by severity (must > major > minor >
nits) and keeps the top N — the excess is **trimmed and never posted**
(`trimmed_comments` in the result; the summary gets a note). If the *must*
comments alone exceed the cap, some must-level findings couldn't be posted, so
`force_full_next` is set: the harness writes `"force_full":true` into the state
marker and the **next** review runs as a full review (not incremental) to
re-surface the trimmed findings against the whole PR.

**Prompt-size bound** (`--max-diff-bytes`, default 500 000; 0 = unbounded):
`Enricher.Render` truncates the enriched diff at a line boundary once it
exceeds the budget (reserving room for a visible marker, so the total stays
within the budget — for a budget too small even for the marker, the marker is
dropped rather than overrun). This bounds the *input* the model receives, so a huge PR
can't hard-error the first model call with `context_length_exceeded` — a
failure `--max-budget-tokens` can't prevent, since the budget is only checked
*after* a call returns. Truncation is always on a line boundary (never a
partial or half-rune line); Commentable ranges still derive from the full diff,
so line validation stays correct — the budget only limits what is *shown*.

## 4. Diff parsing and commentable ranges (`internal/review/diff.go`)

`ParseUnifiedDiff` scans the diff line by line via a small `diffParser` state
machine into `[]FileDiff{Path, OldPath, IsNew, IsDeleted, Hunks}`. Each hunk
line carries both its old- and new-side number (0 on the side it doesn't
exist), computed while scanning — this is the source of truth for all line
math downstream.

`CommentableRanges` reduces that to `Ranges: map[path][]LineRange` — the
new-side span of every hunk (`NewStart … NewStart+NewLines-1`). Two special
cases matter:

- A **deleted file** is present in the map with an *empty* slice, so
  validation can distinguish "not in the diff" (suggest the files that are)
  from "in the diff but not inline-commentable" (tell the model to use the
  summary instead).
- A hunk with `NewLines == 0` (pure deletion) contributes no range.

`Ranges.Validate(path, line, endLine)` is the single validation primitive.
Its error messages are written for the *model*, not the user: they name the
valid ranges (`commentable lines: 10-17, 31-34`) so a bounced tool call can
be corrected on the next iteration.

Why new-side-only: the harness posts every comment with `side: "RIGHT"`.
Comments on removed lines (LEFT side) are deliberately out of scope — the
model is instructed to attach a deletion-related finding to a nearby
remaining line or the summary. This halves the validation surface and avoids
the Reviews API's most error-prone corner.

## 5. Enrichment (`internal/review/enrich.go`)

The raw diff often lacks the context to judge correctness, and letting the
model wander the repo for every hunk is slow. The `Enricher` pre-reads the
changed files (via `repository.FilesystemRepository`, relative to
`--workdir`, the PR-head checkout) and renders each hunk with up to
`--context` (default 10) extra lines before and after:

```
## File: internal/foo.go
Commentable lines (inline comments MUST target these): 10-17, 31-34
@@ diff lines 10-17 @@
      7  ctx| surrounding line          ← enrichment: NOT commentable
[   10]    | unchanged line in diff     ← context line inside the hunk
        -  | removed line               ← no new-side number
[   12] +  | added line
     18  ctx| surrounding line
```

The rendering encodes the validation rule visually: **only bracketed numbers
are valid comment targets**. Enrichment lines are prefixed `ctx` and carry no
brackets, so the model cannot copy an invalid number out of them. This
redundancy (prompt says it, rendering shows it, the tool enforces it) is what
makes comment placement reliable in practice.

Fallbacks: unreadable files (deleted, binary, missing) render hunk-only with
no `ctx` lines; deleted files render their raw hunks under a "no commentable
lines" notice.

Enrichment lives *inside* `klein review` (not the harness) because klein
already has secure file access to the checkout and the logic is unit-testable
Go; it is still pure file reading — principle #1 is untouched.

## 6. The review agent run (`klein/review_command.go`)

The subcommand builds the standard agent stack (DESIGN.md §1) with three
deviations:

- **Tool injection.** `ReviewToolManager` is passed through
  `AgentOptions.MCPToolManagers["review"]` — the map key is only a label; the
  composite flattens tools by name (same pattern as the serve-mode
  memory/schedule managers).
- **Hard sandbox.** A skill's `allowed-tools` only sets the *deferred core*;
  every other registered tool would remain reachable via `ToolSearch`.
  `a.SetAllowedToolsOverride(reviewAllowedTools)` switches to the hard
  whitelist path — nothing outside
  `Read, Glob, LS, AddInlineReview, AddSummaryReview, FinalizeReview`
  exists for this run. (`Grep` is intentionally absent; `Glob`+`Read` have
  been sufficient, and the enriched prompt removes most search needs.)
- **Backend restriction.** Whole-agent backends (`codex`, `kessel`) run their
  own toolset out-of-process and can't see the review tools, so they are
  rejected at startup. Any direct `domain.LLM` backend (openai default,
  anthropic, gemini) works.

The run is one-shot (`IsInteractiveMode: false`): no session persistence,
in-memory todos, and no approval prompts (the sandbox contains no
approval-gated tool anyway). The PR metadata + enriched diff form the *user
prompt* (`review.BuildPrompt`); the review policy lives in the skill.

### The review skill (`internal/skill/skills/review/SKILL.md`)

`user-invocable: false` (hidden from the Connect server's skill list; the
subcommand invokes it by name). The prompt enforces, in order:

1. reason → verify (`Read` the surrounding code) → evaluate honestly; drop
   unconfirmed findings — never comment from the diff alone
2. scope: correctness > security > API misuse > regressions; no style nits,
   no praise comments, nothing outside the diff
3. one `AddInlineReview` per verified finding, then exactly one
   `AddSummaryReview` (choosing the verdict), then `FinalizeReview` — always,
   even with zero comments

When editing this prompt, preserve the reason→verify→evaluate structure —
see DESIGN.md §10 for why this is a deliberate design property.

## 7. Review tools (`internal/tool/review_tool_manager.go`)

`ReviewToolManager` is a self-contained `domain.ToolManager` holding the
review state in memory; the subcommand reads `Result()` after the run. It
takes a `LineValidator func(path string, line, endLine int) error` at
construction — the subcommand passes `ranges.Validate`, keeping
`internal/tool` free of any dependency on `internal/review`.

| Tool | Behavior |
|---|---|
| `AddInlineReview` | Validates path+line via the injected validator; rejects duplicates (same path+range), missing/bad severities (required: `must/major/minor/nits`), and calls after finalize. Swapped `line`/`end_line` bounds are silently normalized rather than bounced. |
| `AddSummaryReview` | Sets summary + verdict (`approve/comment/request_changes`, default `comment`); calling again *replaces* (never accumulates). Locked after finalize. |
| `FinalizeReview` | Requires a summary first; idempotence errors on a second call; locks all further mutation. |
| `ResolveReviewComment` | Marks a previous-round comment as verified-fixed (id must match the `previous_comments` input; duplicates rejected). Only registered when previous comments exist. The harness resolves the thread. |

Every rejection returns a tool *error result* (not a Go error), phrased as an
instruction the model can act on — this is the self-correction loop that
keeps invalid comments out of the output without aborting the run.

**Fallback:** if the model never calls `AddSummaryReview`, the subcommand
uses the agent's final text response as the summary and marks
`finalized: false`, so the harness always has something to post.

## 8. The harness (`.github/actions/ai-review/`)

A composite action, deliberately thin — `git` (read-only), `gh`, and `jq`:

1. **Determine review scope.** State is recovered from the sticky summary
   comment's marker (§8.1; falls back to scanning old-style review bodies).
   Modes:
   - no marker → **full** review of `gh pr diff`
   - current full-diff hash equals the marker's `diff_sha` → **skip**: only
     history was rewritten, the content is identical. This is what a web-UI
     "Rebase branch" / "Update branch" push (committer `GitHub web flow`)
     produces — hashing the diff catches it robustly without committer
     sniffing, and also covers any other no-op force-push. The marker's
     `head_sha` is refreshed in place so the *next* content push still gets
     an incremental review.
   - marker's `head_sha` still exists and is an ancestor of head →
     **incremental**: `git diff LAST_SHA..HEAD` is reviewed, the full PR diff
     rides along as `full_diff` for validation
   - commit rewritten with real content changes (**force-push**) → **full**
   - head already reviewed, or the incremental diff is empty → **skip**
   (Requires `fetch-depth: 0` on checkout.)
2. **Collect previous comments.** GraphQL `reviewThreads` → unresolved threads
   whose first comment is by `github-actions` → `previous_comments`
   (`id` = thread node id, `line` falls back to `originalLine` for outdated
   threads).
3. `klein review --input … --output … --workdir "$GITHUB_WORKSPACE"`
   (the klein binary is provided by the calling workflow, input `klein-path`;
   backend selected via `backend` + the matching `*-api-key` input;
   output language via `language`, default `en`)
4. **Resolve fixed threads.** Each `resolved[].id` → GraphQL
   `resolveReviewThread` mutation. Best-effort: a failed mutation logs a
   warning and never blocks posting.
5. **Post.** Two artifacts:
   - **Sticky summary comment** — one PR issue comment created on round 1 and
     PATCHed in place on every later round (no summary spam). Carries the
     summary, a stats footer (`turn N (mode review of sha) · comments: total
     T, active A (+new, −resolved) · verdict`), and the state marker.
     `total` = comments ever posted; `active` = unresolved previous comments
     − this round's resolutions + new comments.
   - **Minimal review** — inline comments (severity prefixed as
     `**[must]** …`, `end_line > line` → `start_line`+`line`) plus the
     verdict event (`APPROVE`/`REQUEST_CHANGES`/`COMMENT`), with a one-line
     body pointing at the summary comment. Skipped entirely when there are
     no inline comments and the verdict is neutral (`comment`).
6. **Dismiss stale change requests.** A `CHANGES_REQUESTED` review is *sticky*
   on GitHub — resolving its threads or posting later `COMMENT` reviews does
   **not** clear it; only a dismissal or an `APPROVE` (which the bot can't do)
   does. So when a round's verdict is no longer `request_changes`, the action
   dismisses the bot's still-active `CHANGES_REQUESTED` review(s) via the
   review `dismissals` endpoint. Without this, a PR that was fixed after an
   early blocking round keeps showing "changes requested" with no open
   comments. Best-effort: a failed dismissal is a warning.

If the review POST fails (e.g. repo settings forbid the Actions token from
APPROVE/REQUEST_CHANGES, or an edge-case comment is still rejected), the
action retries once without inline comments, so the run always reports
*something* — and the summary comment was already updated regardless.

### 8.1 Review-state marker

Round state lives in the PR itself — no external storage. The sticky summary
comment ends with:

```html
<!-- klein-review-state {"head_sha":"<sha>","diff_sha":"<sha256 of full PR diff>","turn":3,"total":5,"force_full":true} -->
```

- `head_sha` — last reviewed head commit (incremental base)
- `diff_sha` — sha256 of the full PR diff at review time (no-op-rebase skip)
- `turn` — review round counter (skipped runs don't increment)
- `total` — cumulative inline comments posted across all rounds
- `force_full` — present only when the last round trimmed must-level comments;
  the scope step upgrades the next incremental round to a full review, then it
  clears (the full round re-surfaces everything)

The marker is written by the *harness* (klein has no SHA knowledge —
principle #1). Editing or deleting the comment simply causes the next run to
fall back to a full review with reset counters. Markers written by the older
review-body scheme (only `head_sha`) are still parsed as a fallback.

The reference workflow `.github/workflows/ai-review.yml` reviews this repo's
own PRs: checkout PR head → build klein from source → run the action.
Requirements: the `OPENAI_API_KEY` repository secret and
`permissions: pull-requests: write`. Fork PRs are skipped (secrets are not
exposed to them). Concurrency is keyed per PR with `cancel-in-progress` so a
rapid push supersedes the in-flight review.

## 9. Failure modes

| Failure | Behavior |
|---|---|
| Empty/unparseable diff | Exit 1 before any model call |
| All changed files are generated | Exit 0; `approve` result naming skipped files, no model call |
| Model targets an invalid line | Tool call bounced with the valid ranges; finding is re-placed or dropped by the model |
| Model never summarizes | Final response becomes the summary, `finalized: false` (logged as a warning) |
| Review with zero findings | Exit 0; summary-only review is posted |
| Reviews API rejects the payload | Harness retries summary-only as `COMMENT` |
| Fork PR | Workflow job skipped (no secret) |
| Force-push rewrote the last reviewed commit | Full review (marker SHA missing or not an ancestor) |
| Web-UI rebase / update-branch (content unchanged) | Skipped — diff hash matches; marker head refreshed in place |
| State marker edited/deleted | Full review (no marker found; turn/total counters reset) |
| Head commit already reviewed / empty incremental diff | Run skipped with a notice |
| Sticky comment deleted between rounds | Recreated on the next round (legacy review-body markers still parsed) |
| `resolveReviewThread` mutation fails | Warning; review is still posted, thread stays open for the next round |
| Model resolves an id not in `previous_comments` | Tool call bounced (unknown id) |
| More comments than the cap | Sorted by severity, lowest trimmed and not posted (`trimmed_comments`) |
| More *must* comments than the cap | As above + `force_full` set → next round is a full review |

## 10. Testing

- `internal/review/diff_test.go` — parser (git/plain formats, new/deleted/
  count-less hunks), new-side line numbering, range derivation and every
  `Validate` rejection class
- `internal/review/enrich_test.go` — context window math, bracketed vs `ctx`
  rendering, unreadable-file fallback, prompt assembly
- `internal/tool/review_tool_manager_test.go` — the full tool flow: dupes,
  out-of-range, severity/verdict enums, swapped bounds, finalize locking,
  summary replacement
- Live smoke test (needs an API key): build klein, plant a bug in a scratch
  checkout, feed a matching diff, and check the result JSON — see
  README "AI Code Review" for the request-JSON shape. The jq payload mapping
  can be exercised offline against a hand-written result JSON.

## 11. Consuming from other repositories

Other repos call the reusable workflow
`.github/workflows/ai-review-reusable.yml` (`workflow_call`): it checks out
the caller's PR head (`fetch-depth: 0`), sets up Go, and runs the composite
action with `klein-version` set — the action then downloads the
`klein_<os>_<arch>.tar.gz` asset from the fpt/klein-cli release (falling back
to `go install` when no asset exists for that tag). Binaries are attached to
releases by `.github/workflows/release.yml` (on `release: published`;
`workflow_dispatch` backfills older tags). Callers pin `@main` or a tag —
Actions has no `@latest` ref — and pass their own API-key secret; see the
README for the caller snippet. This repo's own workflow deliberately keeps
building from source so PRs exercise their unreleased review code.

## 12. Extension points

- **Other forges / posting styles** — the contract is the two JSON documents;
  write a different harness (GitLab, Gerrit, a local pre-push hook) without
  touching klein.
- **LEFT-side comments** — would need `Ranges` to carry old-side spans and a
  `side` field through `ReviewComment` and the jq mapping. Deliberately
  deferred (see §4).
- **Review policy** — override the `review` skill via `.claude/skills/review/`
  (project) or `~/.claude/skills/review/` (personal); the skill loader's
  priority order applies. Keep the finishing sequence (summary → finalize)
  intact — the subcommand depends on it.
- **Severity/verdict taxonomy** — enums live in `review_tool_manager.go`; the
  jq body-prefix mapping in the action must be kept in sync.
