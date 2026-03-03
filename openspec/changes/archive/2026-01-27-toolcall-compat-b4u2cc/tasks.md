## 1. Specs + Regression Tests

- [x] 1.1 Update OpenAI request preprocessing tests to assert trigger+invoke prompt injection, and that `tools`/`tool_choice` are removed.
- [x] 1.2 Update non-stream OpenAI restore tests to use trigger+invoke protocol and assert `tool_calls` + no trigger/`<invoke>` leakage on successful parse.
- [x] 1.3 Update OpenAI SSE rewrite tests to use trigger+invoke protocol and assert `delta.tool_calls` + `finish_reason="tool_calls"` + no trigger/`<invoke>` leakage on successful parse.
- [x] 1.4 Update Anthropic (`anthropic_compat` + `toolcall_compat`) tests (stream + non-stream) to validate tool_use emission under the new protocol.
- [x] 1.5 Keep/extend UTF-8 safety regression tests for streaming rewrites.
- [x] 1.6 Add regression tests for parse-failure fallback after triggering: no synthetic tool calls, trigger never leaks, captured `<invoke>` may be emitted as plain text (b4u2cc).
- [x] 1.7 Add PBT-style tests for protocol round-trips (emit→parse) and chunk-boundary trigger detection invariants (order-insensitive JSON comparisons).

## 2. Request Preprocessing (toolcall_compat)

- [x] 2.1 Add per-request trigger signal generation (`<<CALL_{6hex}>>`) and plumb it through toolcall compat meta for response transformers.
- [x] 2.2 Implement b4u2cc-style tool list serialization for OpenAI `tools` into the injected system prompt (`<function_list>` inside `<antml\b:tools>`).
- [x] 2.3 Replace the system prompt template with b4u2cc `DEFAULT_TEMPLATE` semantics (including `<antml\b:tools>`/`<antml\b:format>`; ignore `tool_choice` semantics).
- [x] 2.4 Protocolize `assistant.tool_calls` history into trigger + `<invoke>...</invoke>` text and remove native `tool_calls` before sending upstream.
- [x] 2.5 Protocolize `role=tool` history into `role=user` `<tool_result id="...">...</tool_result>` records and keep existing backref validation behavior.

## 3. Response Restoring (OpenAI)

- [x] 3.1 Implement non-stream parsing: detect trigger, parse first `<invoke>`, build `tool_calls`, strip protocol text from `message.content`.
- [x] 3.2 Implement streaming parsing: UTF-8 safe trigger detection + bounded buffering, parse first `<invoke>`, emit `delta.tool_calls`, finish with `tool_calls` + `[DONE]`.
- [x] 3.3 Ensure parse failures fall back to text (no synthetic tool calls) in both non-stream and stream paths.

## 4. Anthropic Composition (anthropic_compat + toolcall_compat)

- [x] 4.1 Update non-stream Anthropic conversion fallback parsing from legacy `<function_call>` to trigger+invoke protocol.
- [x] 4.2 Update streaming Anthropic conversion to detect trigger+invoke protocol using the per-request trigger signal, and emit tool_use blocks correctly.

## 5. Validation

- [x] 5.1 Run `openspec validate --type change toolcall-compat-b4u2cc --strict`.
- [x] 5.2 Run `go test ./...` (or at least `go test ./internal/proxy`).
