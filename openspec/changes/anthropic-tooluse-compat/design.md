## Context

- `anthropic_compat` on OpenAI groups rewrites `POST /v1/messages` to `/v1/chat/completions` and converts responses back to Anthropic JSON/SSE (`internal/proxy/server.go`, `internal/proxy/anthropic_compat.go`).
- `toolcall_compat` injects a prompt protocol and restores `tool_calls` for upstreams without native tool calling (`internal/proxy/toolcall_compat_*`).
- When both are enabled, streaming composes toolcall stream rewriting with Anthropic SSE conversion via `io.Pipe` (`internal/proxy/anthropic_toolcall_compat.go`).
- Current Anthropic streaming emits `usage.input_tokens=0` and `usage.output_tokens=0`, which is misleading for debugging (`internal/proxy/anthropic_compat.go`).

## Goals / Non-Goals

**Goals:**
- For Anthropic `/v1/messages` streaming responses, emit locally estimated `usage.input_tokens` in `message_start` and cumulative `usage.output_tokens` in the final `message_delta`.
- Keep response shapes stable:
  - Anthropic clients always see Anthropic `tool_use` blocks (not OpenAI `tool_calls`).
  - OpenAI endpoints remain OpenAI-shaped (no `anthropic_compat` outside `/v1/messages`).
- Add debug logs for compat decisions and tool-call protocol detection/parsing without streaming-loop spam.

**Non-Goals:**
- Perfect (billing-grade) parity with upstream token accounting.
- Counting any non-`messages[*].content` fields (e.g., roles, tool schemas, request metadata).
- Expanding route predicates beyond exact `POST /v1/messages`.

## Decisions

- **Tokenizer**: use `github.com/pkoukk/tiktoken-go` with `github.com/pkoukk/tiktoken-go-loader` offline BPE loader.
- **Encoding selection**: `tiktoken.EncodingForModel(<openai_request.model>)`; fallback to `cl100k_base` on error.
- **Input token basis**: compute from the final upstream OpenAI request body after all proxy transformations (anthropic conversion, param overrides, toolcall compat preprocessing).
  - `input_tokens = Σ estimate(messages[i].content)`; only `content` is counted.
  - If `content` is not a string, JSON-marshal it and count tokens for the marshaled string.
- **Output token basis**: compute while emitting Anthropic SSE.
  - Include tokens for every emitted `text_delta.text` and every emitted `input_json_delta.partial_json`.
  - `output_tokens = Σ estimate(emitted_chunk_text)`
- **Fallback**: if tokenization fails for a chunk, use `ceil(len_bytes(chunk)/4)` where `len_bytes` is UTF-8 byte length.
- **Logging** (debug level only):
  - Allowed: compat enabled/disabled, protocol trigger, parse success/failure, tool name.
  - Forbidden: user prompt content, tool schemas, tool arguments JSON, API keys, or full SSE payloads.
  - Logs occur only at key transitions (no per-chunk spam).

## Risks / Trade-offs

- [Risk] Chunk-wise token counting differs from whole-string counting due to token boundary effects → Mitigation: document as approximate; keep deterministic behavior.
- [Risk] Added tokenizer dependency increases binary size and init cost → Mitigation: offline loader avoids runtime downloads; cache encoding instances.
- [Risk] Debug logs can leak data if payloads are logged → Mitigation: enforce strict allow/deny fields and avoid logging raw content.
