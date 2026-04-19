---
name: tune-model
description: This skill should be used when the user wants to "add a new Ollama model", "update model settings", "check the model card", "adjust model configuration", "onboard a new model", "fix model capabilities", or verify that a model's Tool/Think/Vision flags and sampling parameters match its official model card.
---

Onboard or tune an Ollama model by reading its official model card, comparing against the current registry, making safe implementation changes, and validating with integration tests.

## Key Files

- `pkg/client/ollama/model.go` — `OllamaModel` struct + `ollamaModels` list (safe to edit per-model)
- `testsuite/backends/<name>.json` — backend config for the test matrix
- `pkg/client/ollama/client.go` — sampling params, thinking logic (**shared code — requires approval**)
- `pkg/client/ollama/util.go` — message helpers (**shared code — requires approval**)

## OllamaModel Fields

```go
type OllamaModel struct {
    Name          string  // matched via strings.Contains (lowercase)
    Tool          bool    // native Ollama tool-calling API
    Think         bool    // thinking/reasoning capability
    Vision        bool    // image input
    Context       int     // context window in tokens
    Temperature   float64 // 0 = use defaultTemperature (0.1)
    TopP          float64 // 0 = not set
    TopK          int     // 0 = not set
    UseThinkToken bool    // inject <|think|> in system prompt instead of Think API param
}
```

`UseThinkToken: true` is for models (e.g. gemma4) that activate thinking via a `<|think|>` token at the start of the system prompt rather than the Ollama `Think` API parameter. When true, thinking content is also stripped from conversation history.

## Workflow

### Step 1 — Read the model card

Fetch the Ollama library page:
```
https://ollama.com/library/<model-name>
```

Extract:
- **Tool calling** — does the page mention "tool use", "function calling", or "agentic"?
- **Thinking/reasoning** — configurable thinking mode, reasoning tokens, `<think>` blocks?
- **Vision** — multimodal, image input?
- **Context window** — maximum token count
- **Sampling parameters** — recommended temperature, top_p, top_k (often in a "Best Practices" section)
- **Thinking mechanism** — does it use a special system prompt token (e.g. `<|think|>`) rather than the standard API parameter?

### Step 2 — Compare against current registry

Read `pkg/client/ollama/model.go`. Find the entry for the model (use `strings.Contains` matching — a `"gemma4"` entry covers `gemma4:e4b`, `gemma4:12b`, etc.).

Note any mismatches between the model card findings and the current entry.

### Step 3 — Classify changes

| Change type | Safe? | Action |
|-------------|-------|--------|
| Add new entry to `ollamaModels` | ✅ Safe | Implement directly |
| Update flags on existing entry (Tool/Think/Vision/UseThinkToken) | ✅ Safe | Implement directly |
| Update sampling params on existing entry (Temperature/TopP/TopK) | ✅ Safe | Implement directly |
| Add new field to `OllamaModel` struct | ✅ Safe if no other models affected | Implement directly |
| Modify `buildChatOptions`, `ChatWithToolChoice`, `chatWithOptions` | ⚠️ Shared | Plan + approval before touching |
| Modify `injectThinkToken`, `stripThinkingFromHistory`, `toOllamaMessages` | ⚠️ Shared | Plan + approval before touching |
| Modify any non-Ollama client (anthropic, openai, gemini) | ⚠️ Shared | Plan + approval before touching |

For shared-code changes, present the plan concisely — what changes, which functions, which models affected — and wait for explicit approval before editing.

### Step 4 — Apply safe changes

For `model.go` changes:
- Match name broadly (e.g. `"gemma4"` not `"gemma4:e4b"`) so all variants are covered
- Set `Context: 0` only if the context is unknown; prefer a real value
- Set `Temperature: 0` to inherit the global default (0.1); set explicitly only if the model card recommends something different

For the backend test config (`testsuite/backends/<name>.json`):
- Check that `"thinking"` matches the model's `Think` capability
- Set `"maxTokens"` to at least 8192 for thinking models (they need headroom)

Build after changes:
```bash
make build
```

### Step 5 — Run integration tests

```bash
CLI=./output/klein BACKENDS=<backend_name> ./testsuite/matrix_runner.sh
```

Run in background if it will take a while. Use the same failure analysis from the `integ-test` skill:

| Category | Signals | Action |
|----------|---------|--------|
| Model quality | Correct tool calls, wrong values | Note as model limitation |
| Token budget | ≤200 output tokens, task incomplete | Increase `maxTokens` in backend JSON |
| Tool abandonment | No tool calls, text-only response | Model likely has `Tool: false` — verify |
| Framework bug | Tool error, binary crash | Fix in code |

### Step 6 — Report

Summarise:
- What the model card says vs. what was in the registry
- Changes made
- Test matrix (✅/❌ per testcase)
- One-line diagnosis per failure

## Additional Resources

- `references/code-structure.md` — full OllamaClient architecture, thinking flow, and how UseThinkToken works end-to-end
