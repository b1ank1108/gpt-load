## 1. Specs Coverage

- [x] 1.1 Add a non-stream regression test for `anthropic_compat` + `toolcall_compat` that asserts Anthropic `/v1/messages` returns `tool_use` blocks (and no protocol leakage) in non-stream responses.
- [x] 1.2 Extend/add a stream regression test that asserts Anthropic SSE includes locally computed `usage.input_tokens` in `message_start` and non-hardcoded `usage.output_tokens` in the final `message_delta` (counts text + tool args).

## 2. Token Estimation Utilities

- [x] 2.1 Add `tiktoken-go` + offline loader dependencies and initialize a shared tokenizer with model-based encoding fallback to `cl100k_base`.
- [x] 2.2 Implement `estimateTokens(text, model)` with fallback `ceil(len_bytes(text)/4)` on tokenization failure.
- [x] 2.3 Implement `estimateOpenAIRequestInputTokens(body, model)` that parses the final upstream OpenAI request JSON and sums estimates over `messages[*].content` only (JSON-marshal non-string content).

## 3. Anthropic Streaming Usage

- [x] 3.1 Plumb `input_tokens` into the Anthropic streaming transformer and emit it in the initial `message_start` event.
- [x] 3.2 In `streamOpenAIToAnthropic`, accumulate `output_tokens` from emitted `text_delta.text` and `input_json_delta.partial_json` chunks and emit it in the final `message_delta.usage.output_tokens`.
- [x] 3.3 In `internal/proxy/server.go`, compute `input_tokens` from the final upstream OpenAI request body (`finalBodyBytes`) when `anthropic_compat` is applied to a streaming request, and pass it into the Anthropic transformer (including the composed toolcall transformer).

## 4. Debug Observability (No Payload Leakage)

- [x] 4.1 Add request-level debug logs for compat decisions in `internal/proxy/server.go` (which compat applied; original vs effective path).
- [x] 4.2 Add debug logs for toolcall protocol detection and parse result in `transformOpenAIStreamToolcallCompat` (include tool name(s); exclude raw content/args).
- [x] 4.3 Add debug logs for final estimated `input_tokens` / `output_tokens` in Anthropic streaming (exclude raw content).

## 5. PBT (Fuzz) Properties

- [ ] 5.1 Add fuzz tests for input token estimation: deterministic, non-negative, and does not panic for arbitrary message content.
- [ ] 5.2 Add fuzz tests for output token accumulation: deterministic and equals the sum of per-chunk estimates for randomized sequences of text/tool-arg chunks.

## 6. Validation

- [ ] 6.1 Run `openspec validate --type change anthropic-tooluse-compat --strict`.
- [ ] 6.2 Run `go test ./...` (or at least `go test ./internal/proxy`).
