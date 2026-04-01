---
name: integ-test
description: This skill should be used when the user wants to "run integration tests", "run the matrix test suite", "analyze test results", "investigate test failures", "check why a test failed", or "improve test coverage" for the klein-cli project.
---

Run and analyze klein-cli matrix integration tests. Build the binary, execute the harness, then interpret results: for passes summarise what criteria were verified; for failures diagnose root cause and propose a fix unless the failure is a model quality issue.

## Test Harness Overview

```
testsuite/
├── matrix_runner.sh          # main: runs all testcase × backend combos
├── runner.sh                 # single test runner (used by matrix_runner internally)
├── testcases/<name>/
│   ├── prompt.txt            # multi-turn prompts (--- separator between turns)
│   ├── check.sh              # validation script; exit 0 = pass
│   └── config.json           # optional: skill/allowed_tools overrides
└── backends/<name>.json      # LLM backend config (backend, model, thinking, maxTokens)
```

**Run command:**
```bash
make build && \
OLLAMA_HOST=192.168.101.151:11434 \
CLI=./output/klein \
[TESTS=fibonacci,coding] \
[BACKENDS=ollama_gpt_oss_20b,anthropic] \
./testsuite/matrix_runner.sh
```

Both `TESTS` and `BACKENDS` are optional comma-separated filters; omit to run all.

## Workflow

### Step 1 — Build and run

```bash
make build
```
Then run the matrix runner with the filters the user specified (or all if none given).
Stream stdout so the user sees progress. Save the result file path from the output line `Results will be saved to: …`.

### Step 2 — Parse the result matrix

Read the saved result file. Extract:
- The matrix table (✅/❌ per testcase × backend)
- Summary counts (Passed / Failed / Total)

Report the matrix to the user concisely.

### Step 3 — Analyse passes (if any)

For each ✅ cell, read `testsuite/testcases/<name>/check.sh` and summarise what assertions were verified (file existence, compilation, runtime output values, response content checks, etc.). Keep it brief — one line per check group.

### Step 4 — Analyse failures (if any)

For each ❌ cell:

1. **Find the preserved temp directory.**
   The runner prints: `💾 Temporary directory preserved for debugging: /tmp/tmp.XXXXX`
   Read that path from the result file or stdout.

2. **Read the test output and error log.**
   The result file contains the full runner output including check.sh stdout. Look for:
   - `✗ FAILED: …` lines — the specific assertion that failed
   - `klein execution failed, exit code: N` — the agent crashed or timed out
   - `[usage] input=… output=…` — token counts (very low output = thinking ate the budget)

3. **Inspect the temp directory.**
   ```bash
   ls -la /tmp/tmp.XXXXX/
   cat /tmp/tmp.XXXXX/main.go   # or whatever file was expected
   ```
   Compare the actual generated file against what `check.sh` expected.

4. **Classify the failure:**

   | Category | Signals | Action |
   |----------|---------|--------|
   | **Model quality** | Correct tool calls, correct approach, but output values wrong (wrong algorithm, hallucinated content) | Note as model limitation; no code change proposed |
   | **Token budget** | Very low output tokens (≤200), thinking-capable model, task incomplete | Increase `maxTokens` in backend JSON or disable thinking |
   | **Tool abandonment** | Turn 2+ makes no tool calls; response is text-only explanation | Strengthen task instructions in prompt.txt; check IterationAdvisor hints |
   | **Edit loop** | Repeated failed `Edit` calls with stale `old_string` | Already mitigated by IterationAdvisor; check if threshold is too high |
   | **Prompt/check mismatch** | Agent did something reasonable but check.sh expected a different format | Relax the check or clarify the prompt |
   | **Framework/tool bug** | Tool returns an error, wrong file path, binary crash | File a bug; propose code fix |

5. **Propose improvement** (non-model-quality failures only):
   - For token budget: show the exact JSON change to `testsuite/backends/<name>.json`
   - For tool abandonment / prompt issues: show the specific line to add/change in `prompt.txt`
   - For framework bugs: identify the file and function, outline the fix

### Step 5 — Summary

Present:
- Pass/fail matrix (already done in Step 2)
- One-line diagnosis per failure with proposed action or "model quality — no fix"
- Any quick wins (e.g. a single `maxTokens` bump that would likely fix multiple failures)

## Available Backends

List with `ls testsuite/backends/` or read `doc/OLLAMA_MODELS.md` for capability notes.
Ollama backends require `OLLAMA_HOST=192.168.101.151:11434`.
Cloud backends (anthropic, openai, gemini) require the corresponding `*_API_KEY` env var.

$ARGUMENTS