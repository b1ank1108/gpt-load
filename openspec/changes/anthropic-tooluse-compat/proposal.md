## Why

When `anthropic_compat` + `toolcall_compat` are both enabled, Anthropic clients calling `POST /v1/messages` must receive Anthropic-native `tool_use` blocks (not OpenAI `tool_calls`). Additionally, current Anthropic streaming responses report `usage.input_tokens` / `usage.output_tokens` as `0`, making streaming usage incorrect and hard to debug.

## What Changes

- Ensure `anthropic_compat` + `toolcall_compat` on `POST /v1/messages` returns Anthropic `tool_use` (stream + non-stream) while OpenAI-format endpoints remain OpenAI-shaped (only `toolcall_compat` applies).
- Add debug-level logs for compat decisions and toolcall protocol detection/parsing (key points only; no streaming-loop spam).
- Fix Anthropic streaming usage by computing `usage.input_tokens` and `usage.output_tokens` locally using a tiktoken-like tokenizer approach (reference: `k8s-migration/b4u2cc`), and emitting them in `message_start` / `message_delta`.

## Capabilities

### New Capabilities

<!-- none -->

### Modified Capabilities

- `toolcall-compat`: Extend Anthropic `/v1/messages` compatibility behavior to include locally computed streaming `usage.input_tokens` / `usage.output_tokens`, and add debug observability for the combined `anthropic_compat` + `toolcall_compat` path.

## Impact

- Affected code: `internal/proxy/server.go`, `internal/proxy/anthropic_compat.go`, `internal/proxy/anthropic_toolcall_compat.go`, `internal/proxy/toolcall_compat_*.go`.
- Dependencies: likely add a Go tokenizer dependency (tiktoken-like) and update `go.mod` / `go.sum`.
- External behavior: Anthropic streaming responses will report non-zero `usage` fields (where applicable), without changing the event envelope/shape.

