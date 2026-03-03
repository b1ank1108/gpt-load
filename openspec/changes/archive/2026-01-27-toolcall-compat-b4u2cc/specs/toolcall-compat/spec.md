## MODIFIED Requirements

### Requirement: Toolcall compatibility prompt injection
When `toolcall_compat` is enabled for an OpenAI standard group, the proxy SHALL translate OpenAI tool calling inputs into a prompt-driven protocol for upstreams without native tool calling, using a per-request trigger signal and `<invoke>` XML tool calls.

Implementation constraints (b4u2cc):
- Trigger format MUST be `<<CALL_{6hex}>>` (lowercase hex) and MUST be generated per request.
- Injected prompt text MUST match b4u2cc `DEFAULT_TEMPLATE` semantics, including the literal backspace control character in `<antml\b:tools>` and `<antml\b:format>`.
- The injected prompt MUST include the concrete trigger signal string for that request (not just a pattern).

#### Scenario: tools and tool_choice are removed and prompt is injected
- **WHEN** a client sends `POST /v1/chat/completions` with `tools` and/or `tool_choice`
- **THEN** the upstream request body MUST NOT include `tools` or `tool_choice`
- **THEN** the upstream request `messages[0]` MUST be a `role: "system"` message that describes the trigger signal and `<invoke>` tool-call protocol and provides the tool list

PBT Properties:
- [INVARIANT] For any request with `messages` as an array and (`tools` OR `tool_choice`) present, preprocessing removes top-level `tools` and `tool_choice` and prepends exactly one `role="system"` message. → [FALSIFICATION STRATEGY] Generate random request JSON with varied `messages` shapes, `tools` arrays, and `tool_choice` values; assert keys removed and `messages[0].role=="system"`.
- [INVARIANT] The injected system prompt contains the per-request trigger string exactly once as a standalone line in the `<antml\b:format>` example block. → [FALSIFICATION STRATEGY] Generate many preprocess runs; extract the chosen trigger; assert prompt contains that exact substring and does not contain multiple distinct trigger values.

### Requirement: Tool list formatting for non-native upstreams
The injected prompt MUST format the available tools in a machine-readable list, so that upstream models can select a tool and output a valid `<invoke>` tool call.

Implementation constraints (b4u2cc):
- Tools MUST be serialized as a `<function_list>...</function_list>` block compatible with b4u2cc `buildToolsXml()` output, embedded inside `<antml\b:tools>...</antml\b:tools>`.
- Tool descriptions and enum JSON strings MUST escape `<` and `>` as `&lt;` and `&gt;` (b4u2cc behavior).

#### Scenario: tools are provided in the injected prompt
- **WHEN** `tools` are present in the original OpenAI request
- **THEN** the injected system prompt MUST include a tool list block that contains all tool names and their parameter schemas

PBT Properties:
- [INVARIANT] For any `tools` array, every tool `function.name` appears exactly once in the serialized `<function_list>` and no extra names appear. → [FALSIFICATION STRATEGY] Generate random tool arrays (including duplicate names, empty names, unicode) and assert set equality between input valid names and output names.
- [INVARIANT] Any `<`/`>` characters in tool descriptions or enum values are escaped in the emitted tool list. → [FALSIFICATION STRATEGY] Generate schemas with descriptions/enums containing `<`/`>`; assert `&lt;`/`&gt;` appear and raw `<`/`>` do not within the `<function_list>` block.

### Requirement: Protocolize prior tool call context
To preserve multi-turn tool workflows for upstreams without native tool calling, the proxy SHALL convert OpenAI tool-call messages in the request history into the text protocol.

Implementation constraints (b4u2cc):
- Each protocolized tool call MUST be encoded as:
  - the per-request trigger signal (not emitted to clients), followed by
  - a single `<invoke name="...">` block containing one `<parameter name="...">...</parameter>` element per argument key.
- Parameter values MUST be encoded as b4u2cc does for `tool_use` blocks: string values are emitted as-is; non-string values are emitted as JSON.

#### Scenario: assistant tool_calls are converted into invoke protocol text
- **WHEN** an OpenAI request history contains an `assistant` message with `tool_calls`
- **THEN** the proxy MUST remove `tool_calls` from that message before sending upstream
- **THEN** the proxy MUST append equivalent trigger + `<invoke>...</invoke>` protocol text to the message content

PBT Properties:
- [INVARIANT] For any `assistant` message with `tool_calls`, preprocessing removes `tool_calls` and appends at least one `<invoke name="...">` block to `content`. → [FALSIFICATION STRATEGY] Generate messages with random `tool_calls` lists and confirm removal + appended invoke markers.
- [INVARIANT] For any protocolized tool call, the round-trip holds: emitting `<invoke>` then parsing it yields the same tool name and argument key/value structure (modulo JSON object key ordering). → [FALSIFICATION STRATEGY] Generate random JSON-compatible argument objects; serialize to `<parameter>` tags; parse back and deep-compare.

### Requirement: Protocolize tool results using backrefs
To preserve tool results for upstream models, the proxy SHALL convert `role: "tool"` messages into protocol records, using `tool_call_id` to reference the original tool call.

Implementation constraints (b4u2cc):
- A tool result record MUST be encoded as `<tool_result id="{tool_call_id}">{content}</tool_result>`.
- `{content}` MUST be the tool message content as a string; if the content is structured JSON, it MUST be JSON-serialized to a string.

#### Scenario: role=tool becomes a protocol record message
- **WHEN** an OpenAI request history contains a `role: "tool"` message with `tool_call_id`
- **THEN** the proxy MUST replace that message with a `role: "user"` message containing a `<tool_result id="...">...</tool_result>` record referencing `tool_call_id`
- **THEN** the proxy MUST reject the request if `tool_call_id` cannot be resolved to a prior tool call in the same request history

PBT Properties:
- [INVARIANT] For any message sequence where every `role="tool"` has a valid backref to an earlier `tool_calls[].id`, preprocessing succeeds and each tool message becomes a `role="user"` `<tool_result id="...">...` record. → [FALSIFICATION STRATEGY] Generate random DAG-consistent backrefs; assert success and structural conversion.
- [INVARIANT] For any message sequence containing a `role="tool"` with a missing/unknown `tool_call_id`, preprocessing fails with `BAD_REQUEST`. → [FALSIFICATION STRATEGY] Randomly delete or corrupt `tool_call_id` values and assert rejection.

### Requirement: Restore OpenAI tool_calls from non-stream responses
When tool-call protocol output is produced by the upstream, the proxy SHALL restore a standard OpenAI `tool_calls` response to the client.

Implementation constraints (b4u2cc):
- Only the first `<invoke>...</invoke>` after the trigger is actionable; subsequent `<invoke>` blocks are ignored.
- On parse failure after triggering, the proxy MUST fall back to plain text output (no synthetic tool calls), and MUST NOT emit the trigger signal.

#### Scenario: non-stream response is converted to tool_calls
- **WHEN** the upstream non-stream response content contains a trigger signal followed by a valid `<invoke>...</invoke>` tool call
- **THEN** the proxy response MUST set `choices[0].message.tool_calls` to the parsed tool call
- **THEN** the proxy response MUST set `choices[0].finish_reason` to `"tool_calls"`
- **THEN** the proxy response MUST NOT include the trigger signal or `<invoke>` tags in `choices[0].message.content`
- **THEN** the proxy MUST ignore any subsequent `<invoke>` tool calls in the same response

PBT Properties:
- [INVARIANT] If (and only if) the content contains the trigger and a well-formed `<invoke>...</invoke>`, the restored response has `finish_reason="tool_calls"` and a single `tool_calls[0].function.name` equal to the `<invoke name="...">`. → [FALSIFICATION STRATEGY] Generate random content with/without trigger and with valid/invalid invoke blocks; assert the conversion predicate matches.
- [INVARIANT] On successful conversion, the client-visible `message.content` contains no trigger substring and no `<invoke`/`</invoke>` tags. → [FALSIFICATION STRATEGY] Generate mixed content with surrounding text; assert all protocol markers are stripped on success.

### Requirement: Restore OpenAI tool_calls from streaming responses
When tool-call protocol output is produced by the upstream stream, the proxy SHALL emit OpenAI streaming tool call deltas to the client without leaking the trigger signal or `<invoke>` tags.

Implementation constraints (b4u2cc):
- Streaming parsing MUST be incremental over UTF-8 text and MUST detect the trigger signal across chunk boundaries.
- Only the first `<invoke>...</invoke>` after the trigger is actionable; subsequent `<invoke>` blocks are ignored.
- On parse failure after triggering, the proxy MUST fall back to emitting text (no synthetic tool calls), and MUST NOT emit the trigger signal.

#### Scenario: stream emits tool_calls and finishes with tool_calls
- **WHEN** the upstream stream contains the trigger signal followed by a valid `<invoke>...</invoke>` tool call
- **THEN** the proxy stream MUST emit a `delta.tool_calls` frame for the parsed tool call
- **THEN** the proxy stream MUST emit a frame with `finish_reason = "tool_calls"`
- **THEN** the proxy stream MUST NOT emit the trigger signal or `<invoke>` tags as `delta.content`
- **THEN** the proxy MUST ignore any subsequent `<invoke>` tool calls in the same stream

PBT Properties:
- [INVARIANT] For any upstream SSE where trigger+valid invoke appears, output SSE contains exactly one `delta.tool_calls` emission and terminates with `finish_reason="tool_calls"` and `[DONE]`. → [FALSIFICATION STRATEGY] Generate random chunk boundaries splitting trigger and invoke tags; assert normalized output event sequence properties.
- [INVARIANT] Before tool-call emission, all non-protocol text is preserved in order. → [FALSIFICATION STRATEGY] Generate random pre-trigger unicode text; ensure output stream contains it unchanged and in-order.

### Requirement: UTF-8 safe streaming rewrites
Streaming detection and rewriting MUST preserve valid UTF-8 in all emitted JSON frames.

#### Scenario: multi-byte characters remain intact
- **WHEN** the upstream stream contains multi-byte UTF-8 text (e.g. 中文) before the trigger signal starts
- **THEN** the proxy MUST emit identical text content to the client without replacement characters or corruption

PBT Properties:
- [INVARIANT] For any unicode text prefix (including multi-byte runes) emitted before the trigger, the client-visible stream contains the exact same rune sequence and contains no replacement characters. → [FALSIFICATION STRATEGY] Generate random unicode strings; split into random SSE delta chunks; assert equality and absence of `\ufffd`/`�`.

### Requirement: Transformer-safe handling of Content-Encoding
Any response transformer that parses upstream bodies (including SSE) MUST ensure the parsed stream is not compressed, or is decompressed before parsing.

#### Scenario: client Accept-Encoding does not break streaming transformers
- **WHEN** a client includes `Accept-Encoding` that could cause compressed upstream responses
- **THEN** streaming transformers MUST still parse and transform SSE correctly and MUST NOT output compressed bytes to clients without the corresponding `Content-Encoding` header

PBT Properties:
- [INVARIANT] Whenever any parsing transformer is selected, the upstream request header `Accept-Encoding` is forced to `identity` regardless of client input. → [FALSIFICATION STRATEGY] Generate random `Accept-Encoding` inputs and assert the final upstream request header is `identity`.

### Requirement: Anthropic compatibility composition
When `anthropic_compat` is enabled for an OpenAI group, and `toolcall_compat` is also enabled, tool calling MUST work end-to-end for Anthropic `/v1/messages` clients even if the upstream lacks native tool calling.

#### Scenario: Anthropic tool_use is returned when upstream emits trigger+invoke protocol
- **WHEN** a client calls `POST /v1/messages` with tools and streaming enabled
- **THEN** the proxy MUST convert the request to OpenAI `/v1/chat/completions`, apply toolcall compatibility, and convert the response back to Anthropic SSE
- **THEN** the Anthropic SSE MUST include a `content_block` of type `"tool_use"` with the parsed tool name and input

PBT Properties:
- [INVARIANT] For any upstream output containing trigger+valid invoke, Anthropic output includes exactly one `tool_use` block with `name` equal to the invoke name and `input` equal to the parsed parameter map. → [FALSIFICATION STRATEGY] Generate random invokes with JSON-parseable/non-parseable parameter values; assert mapping and JSON parsing behavior matches b4u2cc.
