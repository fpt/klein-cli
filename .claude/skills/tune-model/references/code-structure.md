# Ollama Client Code Structure

## Call Flow (tool-capable model)

```
app.Agent.Invoke()
  → client.NewClientWithToolManager(llmClient, filteredTools)
      → OllamaClient.SetToolManager(toolManager)
  → react.ReAct.Invoke()
      → OllamaClient.ChatWithToolChoice(ctx, messages, toolChoice, enableThinking, thinkingChan)
          → toOllamaMessages(messages)               // convert domain → Ollama format
          → buildChatOptions()                        // temperature/top_p/top_k/num_predict/num_ctx
          → stripThinkingFromHistory() [UseThinkToken]// strip .Thinking from history msgs
          → injectThinkToken()         [UseThinkToken]// prepend <|think|> to system msg
          → chatRequest.Think = ...   [non-UseThinkToken thinking models]
          → chat(ctx, chatRequest, thinkingChan)      // Ollama streaming API call
          → toDomainMessageFromOllama(result, includeThinking)
```

## Thinking: Two Mechanisms

### Mechanism A — Think API parameter (qwen3, gpt-oss)
- `chatRequest.Think = &api.ThinkValue{Value: true}`
- Ollama handles the thinking internally
- Thinking content returned in `resp.Message.Thinking`
- `UseThinkToken: false`

### Mechanism B — `<|think|>` system prompt token (gemma4)
- `<|think|>` prepended to the first system message by `injectThinkToken()`
- Thinking content still returned in `resp.Message.Thinking` (Ollama parses the template)
- Past thinking stripped from history by `stripThinkingFromHistory()` (per model card spec)
- `UseThinkToken: true`
- `chatRequest.Think` is NOT set for these models

## Sampling Params

`buildChatOptions()` on `OllamaCore`:
```go
func (c *OllamaCore) buildChatOptions() map[string]any {
    params := GetModelSamplingParams(c.model)
    opts := map[string]any{
        "temperature": params.Temperature,
        "num_predict": c.maxTokens,
        "num_ctx":     c.numCtx(),
    }
    if params.TopP > 0 { opts["top_p"] = params.TopP }
    if params.TopK > 0 { opts["top_k"] = params.TopK }
    return opts
}
```

`GetModelSamplingParams()` in `model.go` walks `ollamaModels` and returns the first match.
If a model has `Temperature == 0`, it falls through to `defaultTemperature = 0.1`.

## context window: numCtx()

```go
const defaultNumCtx = 8192
const maxNumCtx    = 16384

func (c *OllamaCore) numCtx() int {
    if c.contextSize <= 0    { return defaultNumCtx }
    if c.contextSize > maxNumCtx { return maxNumCtx }
    return c.contextSize
}
```

`contextSize` is set from `GetModelContextWindow(model)` at construction time.
Very large context models (e.g. 256K qwen3.5) are capped at 16384 to avoid VRAM overuse.

## maxTokens defaults by backend

Configured in `testsuite/backends/<name>.json` → `llm.maxTokens`.
Known good values (from memory):
- gpt-oss family: 16384 (thinking models need headroom)
- qwen3.5 / glm: 8192
- gemma4: 8192

## Model matching

`IsToolCapableModel`, `IsThinkingCapableModel`, etc. all use:
```go
strings.Contains(strings.ToLower(model), strings.ToLower(entry.Name))
```
So entry `"gemma4"` matches `"gemma4:e4b"`, `"gemma4:12b"`, `"gemma4:27b"`.
More specific entries (e.g. `"gemma4:e4b"`) would also work and take precedence due to
the linear search returning the first match.

## Backend JSON format

```json
{
  "name": "display-name",
  "llm": {
    "backend": "ollama",
    "model": "gemma4:e4b",
    "thinking": true,
    "maxTokens": 8192
  }
}
```

`"thinking": true` maps to `LLMSettings.Thinking` → passed as the `thinking` arg to
`NewOllamaCoreWithOptions`. For `UseThinkToken` models, this controls whether `<|think|>`
is injected; for Think-API models, it sets `chatRequest.Think`.

## Test matrix file locations

```
testsuite/backends/ollama_gemma4_e4b.json   ← backend config
testsuite/results/test_results_YYYYMMDD_HHMMSS.txt  ← saved results
```

Result files contain full stdout with ANSI codes. Use `grep` or python `re.sub` to strip
ANSI before parsing.
