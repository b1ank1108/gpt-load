# Research Output: anthropic-tooluse-compat

## Context Boundaries (Module Reports)

```json
{
  "module_name": "internal/proxy routing + compat predicates",
  "existing_structures": [
    "Compatibility gating lives in internal/proxy/compat_predicates.go",
    "Proxy handler composes transformers in internal/proxy/server.go based on original path (/v1/messages) and effective path (/v1/chat/completions)",
    "When a transformer parses upstream bodies, upstream Accept-Encoding is forced to identity (internal/proxy/server.go applyTransformerRequestHeaders)"
  ],
  "existing_conventions": [
    "anthropic_compat is only considered for OpenAI channel groups (group.ChannelType == \"openai\")",
    "toolcall_compat is only considered for OpenAI channel groups and POST /v1/chat/completions",
    "Transformer composition is decided once per request (before executeRequestWithRetry)"
  ],
  "constraints_discovered": [
    "anthropic_compat applies only when method=POST and path==\"/v1/messages\" (compat_predicates.go)",
    "toolcall_compat applies only when method=POST and effectivePath==\"/v1/chat/completions\" (compat_predicates.go)",
    "If anthropic_compat is applied, the upstream path is rewritten to /v1/chat/completions and toolcall_compat can then apply (server.go)",
    "OpenAI-format callers (e.g. /v1/chat/completions) are not affected by anthropic_compat and should only go through toolcall_compat when enabled (compat_predicates.go + server.go)"
  ],
  "open_questions": [
    "Should anthropic_compat match only /v1/messages or also tolerate common misconfigured prefixes (e.g. /v1/v1/messages)? (not requested yet)"
  ],
  "dependencies": [
    "LOG_LEVEL controls visibility of debug logs (internal/utils/logger_utils.go)"
  ],
  "risks": [
    "Any new transformer must keep Accept-Encoding identity to avoid compressed SSE bytes reaching parsers/clients",
    "Changing gating predicates risks unexpected behavior changes for existing users"
  ],
  "success_criteria_hints": [
    "With anthropic_compat=true and toolcall_compat=true, POST /v1/messages streams back Anthropic tool_use blocks (validated by internal/proxy/toolcall_compat_sse_test.go)",
    "With anthropic_compat=true and toolcall_compat=true, POST /v1/chat/completions remains OpenAI-shaped and only toolcall_compat applies"
  ]
}
```

```json
{
  "module_name": "internal/proxy/toolcall_compat_request",
  "existing_structures": [
    "preprocessToolcallCompatChatCompletionsRequest injects a system prompt, removes tools/tool_choice, protocolizes assistant tool_calls and tool results",
    "Uses a per-request ID seed (toolcallCompatRequestMeta.IDSeed) for deterministic tool_call IDs in restored responses"
  ],
  "existing_conventions": [
    "Compatibility shims are gated by group flags and route predicates (server.go + compat_predicates.go)",
    "Protocol text is injected as messages[0] system message when tools/tool_choice are present"
  ],
  "constraints_discovered": [
    "The toolcall_compat protocol relies on <function_call>...</function_call> tags and an injected <tool_list> prompt (toolcall_compat_request.go)",
    "Requests are rejected if role=tool messages cannot be back-referenced to a prior tool_call_id in the same history"
  ],
  "open_questions": [
    "Should debug logs ever include a preview of the injected prompt, or only its length/tool count to avoid leaking schemas?"
  ],
  "dependencies": [
    "OpenAI tool schema parsing/serialization in toolcall_compat_request.go"
  ],
  "risks": [
    "Over-logging request content/tools can leak sensitive schema or user data"
  ],
  "success_criteria_hints": [
    "When tools are present, upstream request has tools/tool_choice removed and a system prompt injected (toolcall_compat_request_test.go)"
  ]
}
```

```json
{
  "module_name": "internal/proxy/toolcall_compat_response (non-stream + stream)",
  "existing_structures": [
    "restoreToolCallsInChatCompletion converts protocol text into OpenAI tool_calls for non-stream responses",
    "transformOpenAIStreamToolcallCompat rewrites upstream SSE into OpenAI tool_calls streaming events",
    "toolcallCompatStreamDetector buffers a small UTF-8 safe suffix to detect protocol triggers without corrupting runes"
  ],
  "existing_conventions": [
    "Do not leak protocol markers to clients (tests assert <function_call> tags are removed)",
    "Keep a bounded protocol buffer (512KB cap) to avoid unbounded memory growth"
  ],
  "constraints_discovered": [
    "Once triggered, upstream content is buffered until tool-call protocol can be parsed; then the stream is terminated with finish_reason=tool_calls and [DONE]",
    "UTF-8 safety depends on rune-aware decoding (utf8.DecodeRuneInString) when holding back detection suffixes"
  ],
  "open_questions": [
    "For Anthropic callers, do we want to bypass emitting OpenAI tool_calls SSE and instead directly emit Anthropic tool_use SSE? (current implementation uses a composition via io.Pipe)"
  ],
  "dependencies": [
    "internal/proxy/anthropic_toolcall_compat.go composes this stream transformer with Anthropic SSE conversion"
  ],
  "risks": [
    "More debug logs inside the streaming loop can be extremely noisy; needs careful sampling or debug-only gating"
  ],
  "success_criteria_hints": [
    "OpenAI stream: tool_calls deltas and finish_reason=tool_calls are emitted without protocol leakage (toolcall_compat_sse_test.go)"
  ]
}
```

```json
{
  "module_name": "internal/proxy/anthropic_compat + internal/proxy/anthropic_toolcall_compat",
  "existing_structures": [
    "convertAnthropicMessagesToOpenAI maps Anthropic request shape to OpenAI chat/completions request",
    "convertOpenAIChatCompletionToAnthropic maps OpenAI non-stream responses back into Anthropic message JSON, including tool_use blocks",
    "streamOpenAIToAnthropic converts OpenAI SSE into Anthropic SSE, including tool_calls -> tool_use streaming blocks",
    "anthropic_toolcall_compat composes toolcall_compat stream rewrite with anthropic streaming conversion via io.Pipe"
  ],
  "existing_conventions": [
    "Anthropic compatibility is only enabled for OpenAI groups via group.AnthropicCompat (compat_predicates.go)",
    "Non-stream token usage uses upstream usage.prompt_tokens/completion_tokens when available"
  ],
  "constraints_discovered": [
    "Streaming Anthropic SSE currently hardcodes usage.input_tokens=0 and usage.output_tokens=0 (anthropic_compat.go)",
    "Tool-use conversion is already validated for streaming composition when both flags are enabled (toolcall_compat_sse_test.go)"
  ],
  "open_questions": [],
  "dependencies": [
    "User decision: use tiktoken-like tokenizer approach (reference b4u2cc) to compute streaming usage tokens",
    "Implementation likely adds a Go tokenizer dependency and updates go.mod/go.sum"
  ],
  "risks": [
    "Tokenizer dependency increases binary size and complexity; heuristic may be inaccurate and surprise users",
    "Counting tokens from streamed JSON fragments must avoid memory bloat and must remain UTF-8 safe"
  ],
  "success_criteria_hints": [
    "Anthropic stream: emitted message_start includes non-zero usage.input_tokens (computed locally, reference b4u2cc)",
    "Anthropic stream: emitted message_delta includes non-zero usage.output_tokens reflecting the produced content, not always 0",
    "Anthropic stream: tool_use blocks still emitted correctly under anthropic_compat + toolcall_compat"
  ]
}
```

```json
{
  "module_name": "Logging & Debugging surface",
  "existing_structures": [
    "logrus is globally configured via internal/utils/logger_utils.go and LOG_LEVEL",
    "Request-level access logs are emitted by internal/middleware/middleware.go"
  ],
  "existing_conventions": [
    "Sensitive credentials are masked when logged in proxy/server.go via utils.MaskAPIKey",
    "Debug logs exist but are sparse in compat transformers today"
  ],
  "constraints_discovered": [
    "Debug logging must not leak user content, tool schemas, or API keys; prefer counts/lengths/seeds instead"
  ],
  "open_questions": [],
  "dependencies": [
    "LOG_LEVEL and log format configuration (README + internal/utils/logger_utils.go)"
  ],
  "risks": [
    "High-volume debug logs inside streaming loops can degrade performance and overwhelm log storage"
  ],
  "success_criteria_hints": [
    "User decision: only key-point debug logs (compat applied, trigger detected, parse success/failure, computed input/output tokens)",
    "When LOG_LEVEL=debug, logs show whether anthropic_compat/toolcall_compat were applied and key decision points without streaming-loop spam"
  ]
}
```

## Aggregated Constraint Sets

### Hard Constraints
- Preserve gating semantics: `anthropic_compat` only for `POST /v1/messages` on OpenAI groups; `toolcall_compat` only for `POST /v1/chat/completions` on OpenAI groups (`internal/proxy/compat_predicates.go`).
- Preserve client contract: Anthropic callers must receive Anthropic-shaped JSON/SSE; OpenAI callers must receive OpenAI-shaped JSON/SSE (`internal/proxy/server.go` transformer composition).
- No protocol leakage: toolcall protocol markers/tags must never appear in client-visible output (`openspec/specs/toolcall-compat/spec.md` + tests).
- Streaming transformers must not parse compressed upstream bodies: ensure `Accept-Encoding: identity` is enforced whenever a transformer is active (`internal/proxy/server.go`).
- Streaming Anthropic SSE usage MUST be computed locally (reference b4u2cc) and MUST NOT be hardcoded to 0 for `input_tokens`/`output_tokens`.

### Soft Constraints
- Keep changes minimal and localized under `internal/proxy` unless a new tokenizer dependency is explicitly accepted.
- Debug logs should be debug-level only and avoid sensitive payloads.

## Verifiable Success Criteria (Draft)
- Anthropic + both flags: `POST /v1/messages` streaming responses include `content_block_start` with `type:"tool_use"` when upstream emits tool-call protocol; and `message_delta.usage.output_tokens` is not always `0`.
- Anthropic + both flags: `message_start.usage.input_tokens` is set (non-zero for non-empty requests), computed via tokenizer approach (reference b4u2cc), and `message_delta.usage.output_tokens` is computed similarly.
- OpenAI + toolcall_compat: `POST /v1/chat/completions` streams emit `delta.tool_calls` and finish with `finish_reason:"tool_calls"` and `[DONE]` with no protocol leakage.
- Debug logs: with `LOG_LEVEL=debug`, logs explicitly show when each compat feature is applied, plus key runtime decisions (trigger detected, tool call parse success/failure, token estimate), without logging raw user prompts or API keys.
