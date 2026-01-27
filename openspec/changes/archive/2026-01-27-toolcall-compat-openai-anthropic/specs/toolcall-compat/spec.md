## ADDED Requirements

### Requirement: Toolcall compatibility prompt injection
When `toolcall_compat` is enabled for an OpenAI standard group, the proxy SHALL translate OpenAI tool calling inputs into a prompt-driven protocol for upstreams without native tool calling.

#### Scenario: tools and tool_choice are removed and prompt is injected
- **WHEN** a client sends `POST /v1/chat/completions` with `tools` and/or `tool_choice`
- **THEN** the upstream request body MUST NOT include `tools` or `tool_choice`
- **THEN** the upstream request `messages[0]` MUST be a `role: "system"` message that describes the tool-call protocol and provides the tool list

### Requirement: Tool list formatting for non-native upstreams
The injected prompt MUST format the available tools in a way compatible with `toolcall_example3.md`, so that upstream models can select a tool and output a valid tool call.

#### Scenario: tools are provided as a <tool_list> in the injected prompt
- **WHEN** `tools` are present in the original OpenAI request
- **THEN** the injected system prompt MUST include a `<tool_list>` block containing all tools, including names and parameter schemas

### Requirement: Protocolize prior tool call context
To preserve multi-turn tool workflows for upstreams without native tool calling, the proxy SHALL convert OpenAI tool-call messages in the request history into the text protocol.

#### Scenario: assistant tool_calls are converted into protocol text
- **WHEN** an OpenAI request history contains an `assistant` message with `tool_calls`
- **THEN** the proxy MUST remove `tool_calls` from that message before sending upstream
- **THEN** the proxy MUST append equivalent `<function_call>...</function_call>` blocks to the message content

### Requirement: Protocolize tool results using backrefs
To preserve tool results for upstream models, the proxy SHALL convert `role: "tool"` messages into protocol records, using `tool_call_id` to reference the original tool call.

#### Scenario: role=tool becomes a protocol record message
- **WHEN** an OpenAI request history contains a `role: "tool"` message with `tool_call_id`
- **THEN** the proxy MUST replace that message with a `role: "user"` message containing a `<function_call>` record that includes the tool name, the tool arguments, and the tool response
- **THEN** the proxy MUST reject the request if `tool_call_id` cannot be resolved to a prior tool call in the same request history

### Requirement: Restore OpenAI tool_calls from non-stream responses
When tool-call protocol output is produced by the upstream, the proxy SHALL restore a standard OpenAI `tool_calls` response to the client.

#### Scenario: non-stream response is converted to tool_calls
- **WHEN** the upstream non-stream response content contains one or more valid `<function_call>...</function_call>` tool call blocks
- **THEN** the proxy response MUST set `choices[0].message.tool_calls` to the parsed tool calls
- **THEN** the proxy response MUST set `choices[0].finish_reason` to `"tool_calls"`
- **THEN** the proxy response MUST NOT include the protocol tags/markers in `choices[0].message.content`

### Requirement: Restore OpenAI tool_calls from streaming responses
When tool-call protocol output is produced by the upstream stream, the proxy SHALL emit OpenAI streaming tool call deltas to the client without leaking protocol tags/markers.

#### Scenario: stream emits tool_calls and finishes with tool_calls
- **WHEN** the upstream stream contains tool-call protocol output
- **THEN** the proxy stream MUST emit a `delta.tool_calls` frame for all parsed tool calls
- **THEN** the proxy stream MUST emit a frame with `finish_reason = "tool_calls"`
- **THEN** the proxy stream MUST NOT emit protocol tags/markers as `delta.content`

### Requirement: UTF-8 safe streaming rewrites
Streaming detection and rewriting MUST preserve valid UTF-8 in all emitted JSON frames.

#### Scenario: multi-byte characters remain intact
- **WHEN** the upstream stream contains multi-byte UTF-8 text (e.g. 中文) before tool-call protocol starts
- **THEN** the proxy MUST emit identical text content to the client without replacement characters or corruption

### Requirement: Transformer-safe handling of Content-Encoding
Any response transformer that parses upstream bodies (including SSE) MUST ensure the parsed stream is not compressed, or is decompressed before parsing.

#### Scenario: client Accept-Encoding does not break streaming transformers
- **WHEN** a client includes `Accept-Encoding` that could cause compressed upstream responses
- **THEN** streaming transformers MUST still parse and transform SSE correctly and MUST NOT output compressed bytes to clients without the corresponding `Content-Encoding` header

### Requirement: Anthropic compatibility composition
When `anthropic_compat` is enabled for an OpenAI group, and `toolcall_compat` is also enabled, tool calling MUST work end-to-end for Anthropic `/v1/messages` clients even if the upstream lacks native tool calling.

#### Scenario: Anthropic tool_use is returned when upstream emits tool-call protocol
- **WHEN** a client calls `POST /v1/messages` with tools and streaming enabled
- **THEN** the proxy MUST convert the request to OpenAI `/v1/chat/completions`, apply toolcall compatibility, and convert the response back to Anthropic SSE
- **THEN** the Anthropic SSE MUST include a `content_block` of type `"tool_use"` with the parsed tool name and input
