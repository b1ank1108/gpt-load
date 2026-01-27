## Context

GPT-Load supports OpenAI groups and provides two compatibility flags:

- `anthropic_compat`: accept Anthropic `POST /v1/messages`, convert to OpenAI `POST /v1/chat/completions`, and convert responses back to Anthropic.
- `toolcall_compat`: adapt OpenAI tool calling (`tools` / `tool_choice` / `tool_calls` / `role=tool`) for upstreams that do not support native tool calling.

Current `toolcall_compat` relies on a per-request trigger line plus a `<function_calls>...</function_calls>` wrapper, and uses a byte-based streaming detector. Users observed garbled output on `stream=true` in OpenAI mode.

Confirmed decisions from user:

- Tool-call protocol MUST follow `toolcall_example3.md`: no trigger line, no wrapper; tool calls are one or more `<function_call>...</function_call>` tags with JSON content.
- Prompt language is fixed (no dynamic locale).
- Strict JSON parsing is sufficient (no `jsonrepair`).
- The garbling issue is on OpenAI `stream=true`.

## Goals / Non-Goals

**Goals:**
- For OpenAI standard groups with `toolcall_compat=true`, allow upstreams without native tool calling to perform tool calls via prompt + `<function_call>` protocol.
- Restore standard OpenAI `tool_calls` for both non-stream and streaming responses without leaking protocol artifacts.
- Eliminate streaming garbling by ensuring UTF-8 safe rewrites and avoiding compressed SSE parsing failures.
- Keep `anthropic_compat` working when combined with `toolcall_compat`, including streaming.

**Non-Goals:**
- Add new settings (locale/flags) or change existing group/UI semantics.
- Implement JSON repair heuristics beyond strict JSON parsing.
- Expand compatibility to non-OpenAI groups or to additional endpoints beyond existing predicates.

## Decisions

1) **Protocol alignment**
- Adopt `toolcall_example3.md` protocol for upstream tool calls:
  - Inject `<tool_list>...</tool_list>` in a system prompt.
  - Upstream tool call output is one or more `<function_call>...</function_call>` blocks containing JSON.
- Keep legacy parsing support for `<function_calls>...</function_calls>` as a fallback, but do not inject trigger/wrapper going forward.

2) **Request preprocessing**
- When `toolcall_compat` is active and the request contains compatibility signals:
  - Remove `tools` / `tool_choice` from upstream request body after converting them into the injected prompt.
  - Convert `assistant.tool_calls` into protocol text appended to `message.content`.
  - Convert `role=tool` messages into `role=user` messages containing a `function_call_record` protocol block keyed by `tool_call_id`.

3) **Response restoring**
- Non-stream: scan the assistant message content for `<function_call>` blocks, parse JSON strictly, ignore `function_call_record`, and populate `message.tool_calls` + `finish_reason="tool_calls"`. Remove protocol blocks from `message.content`.
- Stream: rewrite upstream OpenAI SSE:
  - Pass through normal assistant text until tool-call protocol is detected.
  - Once protocol starts, stop emitting protocol text to client, buffer protocol blocks, and emit OpenAI `delta.tool_calls` followed by `finish_reason="tool_calls"` and `[DONE]`.
  - Support multiple tool calls.

4) **UTF-8 safe streaming detector**
- Replace byte-step holdback logic with rune-safe (or UTF-8 boundary-safe) buffering so emitted `delta.content` is always valid UTF-8 and does not produce replacement characters.

5) **Compression safety**
- When any response transformer is active (toolcall and/or anthropic compatibility), force upstream `Accept-Encoding: identity` (or remove `Accept-Encoding`) to prevent compressed SSE bodies that cannot be parsed and would otherwise appear as garbled output to clients.

6) **Tool call IDs**
- Generate a per-request stable seed for tool call IDs and use `call_<seed>_<n>` for `tool_call_id` fields so follow-up `role=tool` messages can reference them.

## Risks / Trade-offs

- [Upstream ignores `Accept-Encoding: identity`] → Mitigation: add defensive fallback handling (non-fatal) and avoid leaking binary to clients; keep a bounded protocol buffer and fail closed to normal stop behavior.
- [Protocol false positives from user content] → Mitigation: enable response restoration only when the request was preprocessed for toolcall compatibility (not globally).
- [Multiple tool calls + streaming ordering differences] → Mitigation: generate stable ids/indexes per request and emit tool calls deterministically.

## Migration Plan

- No schema/config migration required: the change is guarded by existing `toolcall_compat` and `anthropic_compat` flags (default off).
- Deploy: ship code changes; enable `toolcall_compat` per affected OpenAI group.
- Rollback: disable `toolcall_compat` or revert the proxy transformer changes.

## Open Questions

- None (protocol, locale, JSON tolerance, and reproduction mode confirmed).
