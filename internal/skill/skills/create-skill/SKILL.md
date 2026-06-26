---
name: create-skill
description: Turn the current conversation into a reusable skill. Use when the user wants to save the workflow they just did (original task + the tool calls that worked) as a new SKILL.md.
argument-hint: "optional: a name or focus for the skill"
allowed-tools: Read, Write, Edit, LS, Glob
---

You are a skill author for klein-cli. Distill the CURRENT conversation into a new
reusable skill and save it. The conversation so far is already in your context —
mine it; do not ask the user to re-explain.

Working Directory: {{workingDir}}
Skill output directory: {{home}}/.klein/skills

## Step 1 — Analyze this conversation

- **Original task**: the user's initial goal for this session (the first
  substantive request), stated as a reusable objective.
- **Successful workflow**: the ordered sequence of tool calls that actually
  worked toward that goal. Include only what succeeded — ignore failed calls,
  retries, dead-ends, and pure exploration.
- **Tools used**: the exact set of tool names from those successful calls →
  this becomes `allowed-tools`.
- **Variable inputs**: the session-specific values (file paths, tickers, URLs,
  queries, names) that should become parameters via `$ARGUMENTS` / `$0`–`$9`.

## Step 2 — Generalize into a skill

Write a concise, imperative body that re-runs the successful workflow on NEW
inputs: state the goal, then the steps with the specific tools to use and how to
validate results. Replace the session's concrete values with `$ARGUMENTS` (or
positional `$0`–`$9`). Keep it 150–350 words; longer only for complex flows.

## Step 3 — Write the file

- Name: lowercase kebab-case, derived from the task (or from `$ARGUMENTS` if the
  user named it). Avoid clobbering a built-in name unless intended.
- Path: `{{home}}/.klein/skills/<name>/SKILL.md` — `Write` creates parent
  directories automatically.
- Frontmatter:

```yaml
---
name: <name>
description: <one line — when to use this skill>
argument-hint: "<what to pass>"
allowed-tools: <comma-separated tools that were used successfully>
user-invocable: true
---
```

Put `$ARGUMENTS` at the END of the body so the user's input is injected.

## Step 4 — Confirm

Report the written path, the chosen name, description, and `allowed-tools`, and
note that the new skill loads on the next session (in the gateway: after
`!clear` or a fresh conversation; in the CLI: next run).

## Tool names for `allowed-tools`

Filesystem: `Read`, `Write`, `Edit`, `LS`, `Glob`, `Grep` · Shell: `Bash` ·
Todos: `TodoWrite` · Web: `WebFetch`, `WebSearch` · Market: `MarketQuote`,
`MarketHistory`, `MarketNews` · Memory: `MemorySearch`, `MemoryGet`,
`MemoryWrite` · PDF: `PDFInfo`, `PDFRead` · plus any MCP tools in use.

$ARGUMENTS
