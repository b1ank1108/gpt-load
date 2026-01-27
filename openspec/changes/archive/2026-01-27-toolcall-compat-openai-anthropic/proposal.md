## Why

Some OpenAI-compatible upstreams do not support native tool calling (`tools` / `tool_choice` / `tool_calls`). GPT-Load already provides `toolcall_compat`, but the current protocol and streaming handling can leave protocol artifacts in client responses and can produce garbled output in some streaming cases. We need a more robust compatibility layer aligned with `toolcall_example3.md`, while remaining compatible with Anthropic `/v1/messages` via `anthropic_compat`.

## What Changes

- Update `toolcall_compat` to inject a tool-call prompt compatible with `toolcall_example3.md` for upstreams without native tool calling.
- Update request preprocessing to convert OpenAI `tools` / `tool_choice` / `tool_calls` / `role=tool` messages into the text protocol expected by non-native upstreams.
- Update response post-processing (stream + non-stream) to restore standard OpenAI `tool_calls` from the upstream text protocol, without leaking protocol markers.
- Ensure `anthropic_compat` works correctly when combined with `toolcall_compat` (Anthropic tools/tool_use ↔ OpenAI tools/tool_calls), including streaming.
- Fix streaming garbling by ensuring tool-call detection does not corrupt UTF-8 output and by correctly handling upstream `Content-Encoding` when parsing SSE.
- Add focused tests for streaming UTF-8 safety and compressed SSE passthrough/handling under transformers.

## Capabilities

### New Capabilities
- `toolcall-compat`: Provide a tool-call compatibility shim for OpenAI-format requests, for upstreams that do not support native tool calling, including optional Anthropic `/v1/messages` compatibility.

### Modified Capabilities
- <!-- none -->

## Impact

- `internal/proxy/*`: request/response transformers for OpenAI and Anthropic compatibility.
- `internal/channel/*` and/or proxy request header handling: avoid compressed SSE parsing issues when transformers are active.
- Existing group flags (`toolcall_compat`, `anthropic_compat`) and web UI remain unchanged.
