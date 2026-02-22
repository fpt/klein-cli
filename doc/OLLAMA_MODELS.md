# Ollama Model Compatibility Analysis

This document records empirical findings from running the klein-cli matrix test suite
(`testsuite/matrix_runner.sh`) against various Ollama models.

## Test Suite

Four test cases cover the main capability areas:

| Test | What it measures |
|------|-----------------|
| `coding` | Single-turn file creation via `Write` tool |
| `fibonacci` | Multi-turn edit: create then modify a file via `Edit` tool |
| `research_scenario` | Text-only reasoning — no tools required |
| `web_search` | Fetch and analyse a web page via `WebFetch` tool |

## Results

### ✅ gpt-oss:20b — Fully capable

| coding | fibonacci | research_scenario | web_search |
|--------|-----------|-------------------|------------|
| ✅ | ✅ | ✅ | ✅ |

- **Tool calling**: Native Ollama JSON tool calling — works reliably
- **Thinking**: Supported (configurable via `"thinking": true/false` in backend JSON)
- **Context**: 128k tokens
- **Notes**: The baseline reference model. Passes all four tests consistently.

---

### 🟡 qwen3 family — Mostly capable

| Model | coding | fibonacci | research_scenario | web_search |
|-------|--------|-----------|-------------------|------------|
| qwen3:4b | ✅ | ❌ | ✅ | ❌ |
| qwen3:8b | ✅ | ❌ | ✅ | ✅ |
| qwen3:14b | ✅ | ✅* | ✅ | ❌ |

\* Intermittent — passes ~50% of runs.

- **Tool calling**: Native Ollama JSON tool calling — works after the streaming fix (see below)
- **Thinking**: Supported; disabled by default via `"thinking": false` in backend JSON to avoid spending all output budget on `<think>` tokens
- **Context**: 40k tokens
- **Known issues**:
  - `fibonacci` fails when the `Edit` tool loop corrupts file state — the model retries with a stale `old_string` after a failed edit
  - `web_search` fails on smaller models (4b) because the Wikipedia stub page contains only category links, not biography text; the model cannot extract the expected answer from the sparse content
  - `web_search` with qwen3:14b similarly fails due to context/retrieval quality from the thin page content

#### Key fix: streaming tool calls

qwen3 sends `tool_calls` in the **first** streaming chunk (`done=false`), not in the final
`done=true` chunk. The original klein code only copied tool calls from the final chunk,
silently dropping all qwen3 tool calls and producing empty responses. Fixed in
`pkg/client/ollama/client.go` by accumulating tool calls across all streaming chunks.

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
| gpt-oss:20b | ✅ Native | ✅ | 4/4 |
| qwen3:14b | ✅ Native* | ✅ | ~3.5/4 |
| qwen3:8b | ✅ Native* | ✅ | ~3/4 |
| qwen3:4b | ✅ Native* | ✅ | ~2.5/4 |
| glm-4.7-flash | ❌ XML only | ❌ | 1/4 |
| lfm2.5-thinking | ❌ | ❌ | 1/4 |
| rnj-1:8b | ❌ | ❌ | 1/4 |
| nemotron-3-nano | ❓ Unreliable | ✅ | ⛔ OOM (24GB model, 12GB VRAM) |

\* Required streaming fix: qwen3 sends tool calls in intermediate streaming chunks, not the final chunk.

## Lessons Learned

1. **Streaming tool call placement matters**: Different models send `tool_calls` at different
   points in the streaming response. Always accumulate across all chunks.
2. **Model registry (`model.go`) is manual**: Capability flags (`Tool`, `Think`, `Vision`)
   must be verified empirically — model cards are not always accurate.
3. **`/no_think` is critical for qwen3**: Without `think: false`, qwen3 spends its entire
   output budget on thinking tokens and produces empty content responses.
4. **Tool result role must be `"tool"`**: Sending tool results as `"user"` messages breaks
   native tool calling for strict models.
5. **Edit loops need escape hatches**: Multi-step edit tasks degrade when a failed `Edit`
   leaves the model unable to recover. A fallback to `Write` after repeated Edit failures
   would improve fibonacci-style tests.
