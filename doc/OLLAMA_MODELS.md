# Ollama Model Compatibility Analysis

This document records empirical findings from running the klein-cli matrix test suite
(`testsuite/matrix_runner.sh`) against various Ollama models.

## Test Suite

Seven test cases cover the main capability areas:

| Test | What it measures |
|------|-----------------|
| `coding` | Single-turn file creation via `Write` tool |
| `fibonacci` | Multi-turn edit: create then modify a file via `Edit` tool |
| `long_text` | Long document processing and summarisation |
| `memory_state` | Multi-turn conversation with state retention |
| `refactoring` | Two-turn coordinated multi-step code refactoring |
| `research_scenario` | Text-only reasoning — no tools required |
| `web_search` | Fetch and analyse a web page via `WebFetch` tool |

## Results

### ✅ gpt-oss:20b — Best overall performer

| coding | fibonacci | long_text | memory_state | refactoring | research_scenario | web_search |
|--------|-----------|-----------|--------------|-------------|-------------------|------------|
| ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ |

**Score: 6/7**

- **Tool calling**: Native Ollama JSON tool calling — works reliably
- **Thinking**: Supported (configurable via `"thinking": true/false` in backend JSON)
- **Context**: 128k tokens
- **Notes**: Best balance of capability and speed. Fails only `refactoring` — a test that
  no current Ollama model passes (see known issue below).

---

### ✅ gpt-oss:120b — Most capable, very slow

| coding | fibonacci | long_text | memory_state | refactoring | research_scenario | web_search |
|--------|-----------|-----------|--------------|-------------|-------------------|------------|
| ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ❌ |

**Score: 5/7**

- **Tool calling**: Native Ollama JSON tool calling — reliable
- **Thinking**: Supported
- **Context**: 128k tokens
- **VRAM**: 60 GB (Q4_K_M) — runs mostly on CPU with 12 GB GPU. Approximately 10 min/test.
- **Known issues**:
  - `refactoring`: Used `MultiEdit` with a malformed edits array; produced partial changes
  - `web_search`: Wikipedia stub page yields only category links; model cannot extract answer
    (same sparse-content issue as qwen3 larger models)
- **Notes**: Higher quality reasoning than 20b, but CPU-bound speed makes it impractical for
  interactive use on a 12 GB GPU machine.

---

### 🟡 qwen3 family — Mostly capable

| Model | coding | fibonacci | long_text | memory_state | refactoring | research_scenario | web_search | Score |
|-------|--------|-----------|-----------|--------------|-------------|-------------------|------------|-------|
| qwen3:4b | ✅ | ❌ | ✅ | ✅ | ❌ | ✅ | ❌ | 4/7 |
| qwen3:8b | ✅ | ❌ | ✅ | ✅ | ❌ | ✅ | ✅ | 4/7 |
| qwen3:14b | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ❌ | 5/7 |
| qwen3:30b | ❌ | ✅ | ✅ | ✅ | ❌ | ✅ | ❌ | 4/7 |

- **Tool calling**: Native Ollama JSON tool calling — works after the streaming fix (see below)
- **Thinking**: Supported; disabled by default via `"thinking": false` in backend JSON to avoid
  spending all output budget on `<think>` tokens
- **Context**: 40k tokens
- **Known issues**:
  - `fibonacci` (4b/8b): Edit loop corrupts file state — model retries with stale `old_string`.
    The 8b produced a file with `Computational error: missing import for strconv` injected as
    a bare text line mid-code after repeated failed Edit attempts.
  - `coding` (30b): `think: false` API parameter is **not honoured** — model outputs its
    reasoning as `<think>…</think>` text in the Content field, consuming all 2048 output
    tokens before reaching the Write tool call. Smaller models (4b/8b/14b) suppress thinking
    correctly. Workaround: prepend `/no_think` to the system prompt or increase `maxTokens`.
  - `refactoring`: All qwen3 models fail — see universal refactoring issue below.
  - `web_search`: Fails on 4b, 14b, 30b (Wikipedia stub page too sparse). Passes on 8b
    where the fetched page happened to contain enough biography text.

#### Key fix: streaming tool calls

qwen3 sends `tool_calls` in the **first** streaming chunk (`done=false`), not in the final
`done=true` chunk. The original klein code only copied tool calls from the final chunk,
silently dropping all qwen3 tool calls and producing empty responses. Fixed in
`pkg/client/ollama/client.go` by accumulating tool calls across all streaming chunks.

---

### 🟡 MichelRosselli/GLM-4.5-Air — Q3_K_M vs Q2_K comparison

#### GLM-4.5-Air:Q3_K_M — **Recommended** quantization

| coding | fibonacci | long_text | memory_state | refactoring | research_scenario | web_search |
|--------|-----------|-----------|--------------|-------------|-------------------|------------|
| ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ |

**Score: 6/7**

- **Tool calling**: Native Ollama JSON tool calling — reliable
- **Thinking**: Supported (enabled via `"thinking": true` in backend JSON)
- **Context**: 128k tokens
- **Quantization**: Q3_K_M; community-uploaded GGUF of THUDM's GLM-4.5-Air
- **Known issues**:
  - `refactoring`: Universal Ollama failure — see section below. Partially executes
    Turn 1 (updates `main()` to string IDs) but leaves the struct field as `int`.
    Turn 2 produces only 302 tokens and makes no tool calls — thinking overhead
    likely consumes most of the 2048 token budget before reaching the Edit call.
- **Notes**: Best GLM-4.5-Air result. Matches gpt-oss:20b at 6/7. The extra parameters
  over Q2_K fix the fibonacci Turn-2 abandonment issue. Only fails `refactoring`, which
  no current Ollama model passes.

#### GLM-4.5-Air:Q2_K — Lower VRAM, lower reliability

| coding | fibonacci | long_text | memory_state | refactoring | research_scenario | web_search |
|--------|-----------|-----------|--------------|-------------|-------------------|------------|
| ✅ | ❌ | ✅ | ✅ | ❌ | ✅ | ✅ |

**Score: 5/7**

- **Tool calling**: Native Ollama JSON tool calling — works on single-turn tasks
- **Thinking**: Supported
- **Context**: 128k tokens
- **Quantization**: Q2_K (~4 GB VRAM); community-uploaded GGUF
- **Known issues**:
  - `fibonacci` (Turn 2): Model abandons the `Edit` tool and produces a text response
    instead. Unlike qwen3's stale-`old_string` loop, the model simply stops calling
    tools mid-task.
  - `refactoring`: Step 1 partially done (only `main()` updated); Step 2 skipped.
    Left a `%!d(string=1)` format verb artefact. Hallucinated a wrong absolute path
    before falling back to a relative path.
- **Notes**: Use Q3_K_M if VRAM allows. Q2_K degrades on multi-turn tool reliability.

---

### ❌ glm-4.7-flash — Incompatible tool format

| coding | fibonacci | research_scenario | web_search |
|--------|-----------|-------------------|------------|
| ❌ | ❌ | ✅ | ❌ |

- **Tool calling**: Uses XML tool call format (`<tool_call>…</tool_call>`) — incompatible
  with Ollama's JSON tool calling API. Marked `Tool: false` in the model registry.
- **Thinking**: No
- **Context**: 128k tokens
- **Notes**: Can only do text-only tasks. Would need a custom XML parser to support tool
  use; not pursued.

---

### ❌ lfm2.5-thinking — Too small

| coding | fibonacci | research_scenario | web_search |
|--------|-----------|-------------------|------------|
| ❌ | ❌ | ✅ | ❌ |

- **Tool calling**: No — 1.2B parameter model lacks capacity for structured tool call output
- **Thinking**: Label suggests thinking support, but effective output quality is very low
- **Context**: Unknown
- **Notes**: Model is too small to follow complex instructions or produce valid JSON tool
  calls. Only passes the simple text-only research task.

---

### ❌ rnj-1:8b — No tool calling template

| coding | fibonacci | research_scenario | web_search |
|--------|-----------|-------------------|------------|
| ❌ | ❌ | ✅ | ❌ |

- **Tool calling**: No — based on Gemma3, whose GGUF template does not include a tool
  calling format
- **Thinking**: No
- **Context**: ~8k tokens
- **Notes**: DynamicCapabilityCheck correctly identifies it as non-tool-capable. No path
  to add tool support without a custom template.

---

### ✅ qwen3.5:9b — Strong performer (6/7)

| coding | fibonacci | long_text | memory_state | refactoring | research_scenario | web_search |
|--------|-----------|-----------|--------------|-------------|-------------------|------------|
| ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ |

**Score: 6/7** (tested 2026-03-03)

- **Architecture**: `qwen35moe` — MoE + Gated Delta Networks; ~9B active parameters
- **Tool calling**: Native Ollama JSON tool calling — works reliably after Ollama fixed the
  `qwen35moe` renderer (was broken with 500 crashes in early v0.17.x releases, now fixed)
- **Thinking**: ✅ — `think: false` in backend JSON disables it, keeping token budget for tools
- **Vision**: ✅ (256K context)
- **Refactoring**: ❌ — universal Ollama failure (same as all other models)
- **Notes**: Excellent quality for its size. Matches gpt-oss:20b score at a fraction of the
  memory footprint. The smaller qwen3.5 variants (0.8b–4b) have not yet been benchmarked.

---

### ⛔ nemotron-3-nano — Untestable (VRAM constraints)

- **Architecture**: Hybrid MoE (23 Mamba-2/MoE + 6 Attention layers); 31.6B total
  parameters with ~3.6B active per token
- **VRAM requirement**: Q4_K_M quantization is **24.27 GB** — requires a GPU with ≥24 GB
  VRAM. The test machine (Alienware M15) has 12 GB VRAM; loading the model fails with
  `cudaMalloc failed: out of memory` even with no other models loaded.
- **Tool calling**: Unreliable in Ollama — the model frequently wraps JSON tool calls in
  XML (`<tool_call>…</tool_call>`) roughly 50% of the time. Once the wrong format appears
  in the conversation history it tends to persist. Behaviour is similar to glm-4.7-flash.
- **Thinking**: Supported; configurable budget
- **Context**: 1M tokens
- **Recommendation**: If a GPU with ≥24 GB VRAM becomes available, test with
  `DynamicCapabilityCheck` first before adding to the backend matrix. The 1M context
  window is attractive for large-codebase tasks, but the XML/JSON tool call inconsistency
  is likely to cause failures similar to glm-4.7-flash. A smaller quantization (Q2_K or
  IQ2_M) may reduce VRAM requirements to ~12–14 GB at the cost of output quality.

---

## Summary

| Model | Tool calling | Thinking | Matrix score |
|-------|-------------|----------|--------------|
| gpt-oss:20b | ✅ Native | ✅ | 6/7 |
| qwen3.5:9b | ✅ Native | ✅ | 6/7 |
| GLM-4.5-Air:Q3_K_M | ✅ Native | ✅ | 6/7 |
| gpt-oss:120b | ✅ Native | ✅ | 5/7 (slow — CPU-bound) |
| qwen3:14b | ✅ Native* | ✅ | 5/7 |
| GLM-4.5-Air:Q2_K | ✅ Native | ✅ | 5/7 |
| qwen3:30b | ✅ Native* | ✅† | 4/7 |
| qwen3:8b | ✅ Native* | ✅ | 4/7 |
| qwen3:4b | ✅ Native* | ✅ | 4/7 |
| glm-4.7-flash | ❌ XML only | ❌ | 1/4 |
| lfm2.5-thinking | ❌ | ❌ | 1/4 |
| rnj-1:8b | ❌ | ❌ | 1/4 |
| nemotron-3-nano | ❓ Unreliable | ✅ | ⛔ OOM (24GB model, 12GB VRAM) |

\* Required streaming fix: qwen3 sends tool calls in intermediate streaming chunks, not the final chunk.
† qwen3:30b ignores `think: false` — outputs reasoning as content, exhausting token budget before tool calls.

### Known universal failure: `refactoring` test

All Ollama models fail the `refactoring` test. Root causes observed:
- Models call `todo_write` with incorrect field schema before reading the file
- Models produce text explanations of required changes instead of executing them via tools
- `MultiEdit` is called with an improperly structured edits array
- Even when some changes are applied, the check script's criteria (requiring all 6 specific
  changes) are not fully satisfied

This appears to be a combination of test difficulty (two-turn, multi-step, strict checklist)
and model limitations. The test may need relaxed pass criteria or a more guided prompt.

## Lessons Learned

1. **Streaming tool call placement matters**: Different models send `tool_calls` at different
   points in the streaming response. Always accumulate across all chunks.
2. **Model registry (`model.go`) is manual**: Capability flags (`Tool`, `Think`, `Vision`)
   must be verified empirically — model cards are not always accurate.
3. **`think: false` is not universally honoured**: For qwen3:30b, the Ollama `think`
   parameter is ignored and reasoning appears in the Content field. Larger quantizations of
   the same family may have different template behaviour.
4. **Tool result role must be `"tool"`**: Sending tool results as `"user"` messages breaks
   native tool calling for strict models.
5. **Edit loops need escape hatches**: Multi-step edit tasks degrade when a failed `Edit`
   leaves the model unable to recover. `IterationAdvisor` now injects a re-read hint after
   the first failure on a file (via `FileSystemToolManager.GetToolState()`), down from the
   original threshold of 2+ consecutive failures.
6. **Wikipedia stub pages break web_search**: The target article for the web_search test
   contains only category links on smaller/denser models. A richer test URL or a fallback
   search step would improve reliability across all model sizes.
7. **Large models on insufficient VRAM are impractical**: gpt-oss:120b (60 GB) on a 12 GB
   GPU runs at ~10 min/test. Only useful if a GPU with sufficient VRAM is available.
8. **New Ollama architectures may have deeply broken chat support at launch**: qwen3.5 uses
   a new `qwen35moe` architecture. Early v0.17.x releases crashed on both system messages
   and tool calls. Ollama fixed it shortly after. Always verify basic multi-turn chat with a
   system message before committing a new model family to `model.go`.
