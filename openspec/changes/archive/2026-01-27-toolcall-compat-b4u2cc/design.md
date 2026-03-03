## Context

`toolcall_compat` currently adapts OpenAI tool calling into a prompt-driven `<function_call>...</function_call>` protocol for upstreams without native tool calling. We want to switch to the b4u2cc-style protocol: a per-request trigger signal `<<CALL_xxxxxx>>` followed by `<invoke>` XML, and parse only the first `<invoke>` as a tool call.

Hard constraints (from research):
- Preserve compat gating and client contracts (`internal/proxy/compat_predicates.go`, `internal/proxy/server.go`).
- Trigger signal MUST NOT be emitted to clients.
- On successful tool-call parsing, client-visible output MUST NOT include trigger or `<invoke>` tags.
- On parse failure after triggering, behavior MUST match b4u2cc: fall back to plain text output (may include `<invoke>` tags).
- Transformer-safe upstream parsing must force `Accept-Encoding: identity`.
- Streaming rewrites must remain UTF-8 safe and bounded in memory.

## Goals / Non-Goals

**Goals:**
- Replace the injected protocol + response restoring logic to match b4u2cc semantics (trigger + `<invoke>`).
- Ignore `tool_choice` semantics (remove from upstream request).
- Enforce single tool call per response (first `<invoke>` only).
- Keep Anthropic composition (`anthropic_compat` + `toolcall_compat`) working in both non-stream and stream flows.

**Non-Goals:**
- Backward compatibility with legacy `<function_call>` protocol.
- Adding support for multiple tool calls per response.
- Changing proxy routing, retry logic, or other unrelated request/response behavior.

## Decisions

1) Protocol format (b4u2cc semantics)
- **Decision**: Use a per-request trigger `<<CALL_{6hex}>>` (lowercase hex) and `<invoke name="..."><parameter name="...">...</parameter></invoke>` tool call format.
- **Rationale**: Matches the reference implementation semantics and reduces accidental tool-call parsing.
- **Alternative**: Keep `<function_call>` JSON tags. Rejected due to explicit user request and protocol mismatch.

2) Trigger propagation
- **Decision**: Generate the trigger signal during request preprocessing and plumb it through the response transformer meta so streaming/non-stream parsers know what to detect (no global/static trigger).
- **Rationale**: Trigger is per-request; response parsing must not rely on a global/static trigger.

3) Prompt template + tool list encoding (b4u2cc exact)
- **Decision**: The injected system prompt MUST use the b4u2cc `DEFAULT_TEMPLATE` verbatim (including the literal backspace control character in `<antml\b:tools>` and `<antml\b:format>`), with:
  - `{trigger_signal}` replaced by the per-request trigger string
  - `{tools_list}` replaced by a b4u2cc `buildToolsXml()`-compatible `<function_list>...</function_list>` block
- **Rationale**: Eliminates prompt wording ambiguity; aligns behavior with the known-working reference.

4) `tool_choice` handling
- **Decision**: Remove `tool_choice` before sending upstream and do not translate it into prompt constraints.
- **Rationale**: b4u2cc ignores `tool_choice` semantics; extra prompt conditioning is out-of-scope.

5) Single tool call parsing
- **Decision**: Parse only the first `<invoke>...</invoke>` after trigger; ignore subsequent invokes.
- **Rationale**: Aligns with b4u2cc parser behavior and simplifies stream rewrite.

6) Tool result history protocol
- **Decision**: Convert `role=tool` history into `role=user` messages containing `<tool_result id="...">...</tool_result>` referencing `tool_call_id` (id is the original OpenAI `tool_call_id`).
- **Rationale**: Mirrors b4u2cc approach for preserving tool results in a text protocol.

7) Parse failure fallback (b4u2cc)
- **Decision**: If the trigger is detected but `<invoke>` parsing fails (non-stream or stream), do not synthesize any tool calls; instead, fall back to treating the captured content as plain text output (trigger itself must not be emitted).
- **Rationale**: Matches b4u2cc Toolify parser behavior; avoids hard failures on imperfect upstream outputs.

## Risks / Trade-offs

- [Behavior change for toolcall_compat users] → Document as BREAKING in proposal; ensure regression tests cover new protocol.
- [UTF-8 corruption in streaming detector] → Keep rune-aware holdback (similar to existing detector) and add/update UTF-8 regression tests.
- [Unbounded memory in protocol buffering] → Preserve 512KB cap and terminate stream safely if exceeded.
- [Protocol leakage on parse failure] → Expected (b4u2cc): trigger never leaks, but `<invoke>` may appear as text when parsing fails; ensure tests cover both success and parse-failure paths.

## Migration Plan

- Update delta specs and tests first to lock behavior.
- Implement request preprocessing:
  - generate trigger
  - inject prompt + tool list
  - remove `tools`/`tool_choice`
  - protocolize history (`assistant.tool_calls` + `role=tool`)
- Implement response restoring:
  - non-stream: parse trigger + invoke, populate `tool_calls`, strip protocol from content
  - stream: detect trigger, buffer invoke, emit `delta.tool_calls`, finish with `tool_calls`
- Validate:
  - `go test ./...` (at least `go test ./internal/proxy`)
  - `openspec validate --type change toolcall-compat-b4u2cc --strict`

Rollback:
- Disable `toolcall_compat` flag for affected groups, or revert to the previous build.

## Open Questions

<!-- None (user decisions captured in research). -->
