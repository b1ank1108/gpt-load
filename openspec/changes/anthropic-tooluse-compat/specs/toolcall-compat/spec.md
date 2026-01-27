## MODIFIED Requirements

### Requirement: Anthropic compatibility composition
When `anthropic_compat` is enabled for an OpenAI group, and `toolcall_compat` is also enabled, tool calling MUST work end-to-end for Anthropic `/v1/messages` clients even if the upstream lacks native tool calling.

#### Scenario: Anthropic tool_use is returned when upstream emits tool-call protocol (stream)
- **WHEN** a client calls `POST /v1/messages` with tools and streaming enabled
- **THEN** the proxy MUST convert the request to OpenAI `/v1/chat/completions`, apply toolcall compatibility, and convert the response back to Anthropic SSE
- **THEN** the Anthropic SSE MUST include a `content_block` of type `"tool_use"` with the parsed tool name and input

#### Scenario: Anthropic tool_use is returned when upstream emits tool-call protocol (non-stream)
- **WHEN** a client calls `POST /v1/messages` with tools and streaming disabled
- **THEN** the proxy MUST convert the request to OpenAI `/v1/chat/completions`, apply toolcall compatibility, and convert the response back to Anthropic message JSON
- **THEN** the Anthropic JSON MUST include a content block of type `"tool_use"` with the parsed tool name and input

**PBT**
- [INVARIANT] Tool-call protocol markers (`<function_call>`, `<function_calls>`, `</function_calls>`) never appear in Anthropic client-visible outputs → [FALSIFICATION STRATEGY] fuzz upstream stream chunks with embedded markers and assert outputs exclude them.
- [INVARIANT] For any tool call set produced by protocol restoration, Anthropic tool_use blocks preserve tool name and arguments JSON (semantically) → [FALSIFICATION STRATEGY] generate random tool names + JSON args, serialize into protocol blocks, run conversion, and compare parsed values.

## ADDED Requirements

### Requirement: Anthropic streaming usage is locally estimated
When `anthropic_compat` is enabled for an OpenAI group, the proxy SHALL emit locally estimated token usage in Anthropic SSE responses for `POST /v1/messages`.

#### Scenario: message_start includes locally estimated input_tokens
- **WHEN** a client calls `POST /v1/messages` with streaming enabled
- **THEN** the first `message_start` event MUST include `message.usage.input_tokens` computed from the final upstream OpenAI request body after all proxy transformations
- **THEN** `input_tokens` MUST be computed by summing per-message estimates over the OpenAI request `messages[*].content` values only
- **THEN** the estimator MUST use model-based encoding with fallback to `cl100k_base`, and on tokenization failure MUST fall back to `ceil(len_bytes(text)/4)` for that `content` value

#### Scenario: message_delta includes cumulative output_tokens for streamed text and tool_use args
- **WHEN** a client calls `POST /v1/messages` with streaming enabled
- **THEN** the final `message_delta` event MUST include `usage.output_tokens` computed by summing per-chunk estimates over all emitted `text_delta.text` and `input_json_delta.partial_json` values
- **THEN** the estimator MUST use model-based encoding with fallback to `cl100k_base`, and on tokenization failure MUST fall back to `ceil(len_bytes(text)/4)` for that chunk

**PBT**
- [INVARIANT] Given the same final upstream OpenAI request body, `input_tokens` is deterministic → [FALSIFICATION STRATEGY] fuzz-generate OpenAI request bodies and assert repeated estimates are equal.
- [INVARIANT] `output_tokens` equals the sum of per-chunk estimates over emitted `text_delta.text` and `input_json_delta.partial_json` segments → [FALSIFICATION STRATEGY] generate random sequences of segments, run estimator, and compare with independently computed sum.

### Requirement: Debug logs for compat decisions and tool-call protocol detection
When `LOG_LEVEL=debug`, the proxy SHALL emit debug-level logs for compat decisions and tool-call protocol detection/parsing without leaking sensitive payloads.

#### Scenario: debug logs are emitted without payload leakage
- **WHEN** `LOG_LEVEL=debug` and a request triggers `anthropic_compat` and/or `toolcall_compat`
- **THEN** logs MUST include whether each compat feature was applied for the request
- **THEN** logs MUST include tool-call protocol trigger and parse success/failure, and MUST include the tool name when available
- **THEN** logs MUST NOT include raw user prompt content, tool schemas, tool arguments JSON, API keys, or full SSE frame payloads

**PBT**
- [INVARIANT] Enabling debug logging never changes HTTP/SSE response bodies compared to non-debug logging → [FALSIFICATION STRATEGY] run the same request through the transformer under debug and non-debug, compare outputs byte-for-byte.
