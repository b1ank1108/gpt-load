## Why

Current `toolcall_compat` uses a `<function_call>...</function_call>` text protocol. We want to switch to the b4u2cc-style protocol (trigger signal + `<invoke>` XML) to improve upstream compatibility and make tool calling behave consistently with the known working reference.

## What Changes

- Replace the injected tool-call protocol from `<function_call>...</function_call>` to `<<CALL_xxxxxx>>` trigger + `<invoke name="..."><parameter ...>...</parameter></invoke>` (b4u2cc semantics).
- Ignore `tool_choice` semantics (still remove unsupported fields before sending upstream).
- Streaming and non-stream responses: detect trigger + parse the first `<invoke>` as a tool call; ignore subsequent `<invoke>` blocks (b4u2cc behavior).
- Update Anthropic composition (`anthropic_compat` + `toolcall_compat`) to continue working end-to-end under the new protocol.
- **BREAKING**: upstream protocol expectations change for `toolcall_compat` (models previously following `<function_call>` prompts may stop producing tool calls until they follow the new prompt).

## Capabilities

### New Capabilities

<!-- None -->

### Modified Capabilities

- `toolcall-compat`: change the injected protocol + parsing/restoring logic to b4u2cc semantics; remove `tool_choice` hinting and enforce single-tool-call parsing.

## Impact

- `internal/proxy/toolcall_compat_request.go` (prompt injection + request protocolization)
- `internal/proxy/toolcall_compat_response.go` (non-stream restore + stream rewrite + UTF-8-safe detection)
- `internal/proxy/anthropic_toolcall_compat.go` (composition behavior when both flags are enabled)
- `openspec/specs/toolcall-compat/spec.md` (requirements must be updated to the new protocol)
- Regression tests under `internal/proxy/*toolcall*test.go`
