## 1. Protocol Alignment (toolcall_example3.md)

- [x] 1.1 Update toolcall compatibility prompt to example3 format (no trigger/wrapper, fixed Chinese, includes `<tool_list>`)
- [x] 1.2 Convert OpenAI `tools` JSON into `<tool_list>` XML with names and parameter schemas
- [x] 1.3 Reflect `tool_choice` in injected prompt (when present) without using native tool fields upstream

## 2. Request Preprocessing (OpenAI chat/completions)

- [x] 2.1 Remove `tools`/`tool_choice` from upstream request and inject the system prompt when compatibility signals are present
- [x] 2.2 Protocolize `assistant.tool_calls` into appended `<function_call>` blocks and remove native `tool_calls` before sending upstream
- [x] 2.3 Protocolize `role=tool` messages into `role=user` `function_call_record` blocks and validate `tool_call_id` backrefs

## 3. Response Restoring (OpenAI)

- [x] 3.1 Parse `<function_call>` blocks (strict JSON, strip CDATA, ignore `function_call_record`) into OpenAI `tool_calls`
- [x] 3.2 Restore non-stream chat completions: set `message.tool_calls`, `finish_reason="tool_calls"`, and remove protocol blocks from `message.content`
- [x] 3.3 Rewrite streaming SSE: detect tool-call protocol without leaking partial tags, buffer protocol, then emit `delta.tool_calls` + `finish_reason="tool_calls"` + `[DONE]`
- [x] 3.4 Ensure UTF-8 safe streaming output when holding back for protocol detection

## 4. Transport / Compression Safety

- [x] 4.1 Force upstream `Accept-Encoding: identity` for requests handled by response transformers (prevents compressed SSE garbling)
- [x] 4.2 Keep transformer activation gated so normal responses are not reinterpreted as tool calls

## 5. Anthropic Compatibility Composition

- [x] 5.1 Update `anthropic_compat` + `toolcall_compat` composition to use the new protocol and restored OpenAI `tool_calls`

## 6. Tests & Validation

- [x] 6.1 Update/extend unit tests for request preprocessing under the new protocol
- [x] 6.2 Update/extend unit tests for non-stream response restoration under the new protocol
- [x] 6.3 Add regression test for OpenAI streaming UTF-8 safety and protocol non-leakage
- [x] 6.4 Add regression test covering transformer requests with client `Accept-Encoding` (ensure upstream parsing is not broken)
- [x] 6.5 Run `go test ./...`
