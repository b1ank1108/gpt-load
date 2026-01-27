## Scope

Goal: improve `toolcall_compat` so OpenAI groups can support tool calling for upstreams without native tool calling, and keep it compatible with `anthropic_compat` (Anthropic `/v1/messages` ↔ OpenAI `/v1/chat/completions`), using `toolcall_example3.md` as the primary protocol reference.

References:
- `toolcall_example3.md` (primary protocol)
- `/Users/b1ank/workspace/k8s-migration/b4u2cc` (Toolify-style prompt injection + streaming parser patterns)

## Module Reports (uniform JSON)

```json
{
  "module_name": "proxy/toolcall_compat_request",
  "existing_structures": [
    "preprocessToolcallCompatChatCompletionsRequest injects a system prompt, removes tools/tool_choice, protocolizes assistant tool_calls and tool results",
    "Converts role=tool messages into role=user <function_call> records using tool_call_id backrefs"
  ],
  "existing_conventions": [
    "Compatibility shims are gated by group flags (models.Group.ToolcallCompat) and route predicates (POST /v1/chat/completions)",
    "Protocol text is injected as an extra system message at messages[0]"
  ],
  "constraints_discovered": [
    "Only OpenAI standard groups can enable toolcall_compat (services/normalizeToolcallCompat disables for non-openai or aggregate)",
    "Must accept OpenAI client semantics: tools/tool_choice, assistant tool_calls, tool role messages with tool_call_id"
  ],
  "open_questions": [
    "Should the injected prompt be strictly aligned to toolcall_example3.md (no trigger / no <function_calls> wrapper)?",
    "Should locale be driven by request headers (Accept-Language) or a new system/group setting?"
  ],
  "dependencies": [
    "proxy/server.go request header forwarding affects upstream compression and streaming parsers"
  ],
  "risks": [
    "Changing the protocol requires updating both request injection and response parsing while avoiding regressions"
  ],
  "success_criteria_hints": [
    "Upstream requests must not contain tools/tool_choice when toolcall_compat is active",
    "Tool results must be embedded back into the upstream conversation deterministically via tool_call_id backrefs"
  ]
}
```

```json
{
  "module_name": "proxy/toolcall_compat_response (non-stream + stream)",
  "existing_structures": [
    "restoreToolCallsInChatCompletion converts protocol text into OpenAI tool_calls for non-stream responses",
    "transformOpenAIStreamToolcallCompat rewrites upstream SSE into OpenAI tool_calls streaming events",
    "A detector buffers content until a trigger is found, then parses <function_calls>...</function_calls>"
  ],
  "existing_conventions": [
    "Do not leak protocol markers to client (tests assert trigger and tags are removed)",
    "Keep a bounded protocol buffer (512KB cap) to avoid unbounded memory growth"
  ],
  "constraints_discovered": [
    "Streaming transformers must emit valid OpenAI SSE frames and terminate with data: [DONE]",
    "Multiple tool calls in a single response must map to OpenAI tool_calls with stable ids"
  ],
  "open_questions": [
    "Should we keep trigger-based detection as a legacy fallback while adding tag-based detection for toolcall_example3.md?"
  ],
  "dependencies": [
    "Upstream Content-Encoding impacts whether SSE is parseable",
    "UTF-8 correctness matters when rewriting streamed delta content"
  ],
  "risks": [
    "Byte-based buffering can split UTF-8 runes and produce replacement characters in JSON output (garbling)",
    "If upstream response is compressed (gzip/br/zstd) and we parse without decompression, the stream parser will fail and leak garbage"
  ],
  "success_criteria_hints": [
    "No protocol strings/tags appear in client-visible content",
    "Tool calls are emitted as tool_calls deltas + finish_reason=tool_calls"
  ]
}
```

```json
{
  "module_name": "proxy/anthropic_compat + proxy/anthropic_toolcall_compat",
  "existing_structures": [
    "anthropic_compat converts /v1/messages requests into OpenAI chat/completions, and converts responses back",
    "anthropic_toolcall_compat composes toolcall_compat stream rewrite with anthropic streaming conversion via io.Pipe"
  ],
  "existing_conventions": [
    "anthropic_compat is enabled only for OpenAI groups via group.AnthropicCompat and POST /v1/messages predicate"
  ],
  "constraints_discovered": [
    "When anthropic_compat is enabled, effective upstream path becomes /v1/chat/completions and response must be Anthropic-shaped",
    "Tool calling must map Anthropic tool_use/tool_result blocks to OpenAI tool_calls/tool role messages and back"
  ],
  "open_questions": [
    "Do we need to support both <think> and <thinking> blocks for upstream models that emit reasoning tags?"
  ],
  "dependencies": [
    "OpenAI SSE parsing correctness (including decompression) is required for Anthropic streaming conversion to work"
  ],
  "risks": [
    "If the OpenAI SSE parser sees compressed bytes, the Anthropic stream will break as well"
  ],
  "success_criteria_hints": [
    "Anthropic streaming returns tool_use blocks when upstream tool-call protocol is detected"
  ]
}
```

## Constraint Sets

### Hard Constraints

- Scope gating:
  - `toolcall_compat` only applies when `group.channel_type == openai`, `group.group_type != aggregate`, `POST /v1/chat/completions`.
  - `anthropic_compat` only applies for OpenAI groups on `POST /v1/messages` and rewrites upstream path to `/v1/chat/completions`.
- Client API contract preservation:
  - OpenAI clients MUST continue to use standard `tools` / `tool_choice` and receive standard `tool_calls`.
  - Anthropic clients MUST continue to use `/v1/messages` and receive standard Anthropic message/event formats when `anthropic_compat` is enabled.
- No protocol leakage:
  - Compatibility protocol markers/tags MUST NOT appear in client-visible responses.
- Streaming correctness:
  - Stream transformers MUST output valid OpenAI SSE (and Anthropic SSE for anthropic_compat) and terminate with `[DONE]`.
  - Rewriting streamed text MUST NOT corrupt UTF-8.
- Compression safety:
  - Any transformer that parses upstream response bodies (especially SSE) MUST handle `Content-Encoding` safely (either force identity upstream or decompress before parsing).
- Robust tool parsing:
  - The tool-call parser MUST tolerate small formatting variations observed in `toolcall_example3.md` (e.g. `{function_call:{...}}`, `{name,arguments}`, `{function:{...}}`), and MUST ignore `function_call_record`.

### Soft Constraints

- Keep changes minimal and localized under `internal/proxy` and request header handling.
- Maintain existing group flags and UI behavior; no new mandatory settings unless required.
- Prefer backward-compatible parsing (support legacy trigger/wrapper) if it does not add significant complexity.

### Dependencies

- `proxy/server.go` request header forwarding (`Accept-Encoding`) impacts all streaming parsers.
- Existing tests under `internal/proxy/*_test.go` encode current protocol expectations and will need alignment.

### Key Risks / Likely Root Causes for “乱码”

Based on current code paths:

1) Streaming transformers parse `resp.Body` as plain text, but upstream can be compressed because the proxy forwards client `Accept-Encoding`. In transformer paths we do not forward `Content-Encoding` to the client, so compressed bytes can reach the client as unreadable output (garbling), and parsers will fail.

2) `toolcallCompatStreamDetector.Process` operates on bytes and may split UTF-8 runes when withholding a suffix for trigger/tag detection. When the rewritten SSE chunk is JSON-marshaled, invalid UTF-8 can be replaced, producing visible garbling.

## Verifiable Success Criteria

- OpenAI non-stream:
  - Given a request with `tools` (and optional `tool_choice`), upstream receives a valid `chat/completions` request without those fields, plus an injected system prompt.
  - If upstream returns tool-call protocol per `toolcall_example3.md`, the proxy returns OpenAI `tool_calls` with `finish_reason = "tool_calls"` and `message.content = null` (or plain text if present), with no protocol artifacts.
- OpenAI stream:
  - Given `stream=true`, the proxy forwards normal assistant text deltas until tool-call protocol starts, then emits `tool_calls` deltas and a final `finish_reason = "tool_calls"`, and `[DONE]`.
  - No `<function_call>` / legacy wrapper strings appear in the client stream.
  - UTF-8 characters (e.g. 中文) remain intact in the output.
  - Works even when client sends `Accept-Encoding: gzip, br, zstd` (proxy ensures upstream is parseable for transformers).
- Anthropic:
  - With `anthropic_compat=true`, `/v1/messages` requests with tools stream back Anthropic `tool_use` blocks when tool-call protocol is detected; otherwise stream back text blocks.
  - Non-stream responses are converted to Anthropic JSON without protocol artifacts.

## Open Questions (need user confirmation)

1) Protocol selection: do we fully migrate to `toolcall_example3.md` (no trigger line, no `<function_calls>` wrapper) and keep trigger/wrapper as legacy parsing fallback only?

2) Locale: should the injected prompt’s “current language” be:
   - fixed (`zh-CN`), or
   - derived from request headers (`Accept-Language`), or
   - driven by a new system/group setting?

3) JSON repair: do we need `jsonrepair`-level tolerance (like `toolcall_example3.md`), or is strict JSON acceptable?
