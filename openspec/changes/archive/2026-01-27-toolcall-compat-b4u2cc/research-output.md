# Research Output: toolcall-compat-b4u2cc

## Context Boundary Reports

```json
{
  "module_name": "gpt-load: compat gating + transformer composition",
  "existing_structures": [
    "Gating predicates live in internal/proxy/compat_predicates.go",
    "Transformers are composed in internal/proxy/server.go based on original vs effective path",
    "Any transformer that parses upstream bodies forces upstream Accept-Encoding=identity (internal/proxy/server.go)"
  ],
  "existing_conventions": [
    "toolcall_compat is only considered for OpenAI groups and POST /v1/chat/completions",
    "anthropic_compat is only considered for OpenAI groups and POST /v1/messages",
    "Transformer composition is decided once per request before the upstream call"
  ],
  "constraints_discovered": [
    "Do not change gating semantics (compat_predicates.go)",
    "Transformer-safe Content-Encoding handling must be preserved (server.go applyTransformerRequestHeaders)"
  ],
  "open_questions": [],
  "dependencies": [
    "internal/proxy/server.go composes anthropic_compat + toolcall_compat transformers"
  ],
  "risks": [
    "Changing gating predicates risks unexpected behavior changes for existing users"
  ],
  "success_criteria_hints": [
    "With anthropic_compat=true and toolcall_compat=true, POST /v1/messages still returns Anthropic-shaped JSON/SSE",
    "With toolcall_compat=true, POST /v1/chat/completions still returns OpenAI-shaped JSON/SSE"
  ]
}
```

```json
{
  "module_name": "gpt-load: toolcall_compat request preprocessing",
  "existing_structures": [
    "Request rewrite entrypoint: preprocessToolcallCompatChatCompletionsRequest (internal/proxy/toolcall_compat_request.go)",
    "Existing behavior: inject system prompt, remove tools/tool_choice, protocolize assistant.tool_calls and role=tool history"
  ],
  "existing_conventions": [
    "Only activates when compat signals exist (tools/tool_choice/tool_calls/role=tool)",
    "role=tool requires a resolvable tool_call_id backref or the request is rejected"
  ],
  "constraints_discovered": [
    "New protocol must follow b4u2cc semantics: per-request random trigger signal + <invoke>/<parameter> XML tool calls",
    "tool_choice semantics must match b4u2cc: ignore it (still remove unsupported fields upstream)",
    "Single-tool-call behavior must match b4u2cc parser: only the first tool call per response is actionable"
  ],
  "open_questions": [],
  "dependencies": [
    "Upstream response parsing must know the per-request trigger signal; request preprocessing must persist it into transformer meta"
  ],
  "risks": [
    "Switching protocols is a behavior change for toolcall_compat-enabled users (upstream prompt expectations change)"
  ],
  "success_criteria_hints": [
    "When tools are present, upstream request contains no tools/tool_choice and includes an injected system prompt describing trigger+invoke format",
    "assistant.tool_calls and role=tool history are converted into protocol text compatible with the new parser"
  ]
}
```

```json
{
  "module_name": "gpt-load: toolcall_compat response restoring (non-stream + stream)",
  "existing_structures": [
    "Non-stream restoration lives in restoreToolCallsInChatCompletion (internal/proxy/toolcall_compat_response.go)",
    "Stream rewrite lives in transformOpenAIStreamToolcallCompat (internal/proxy/toolcall_compat_response.go)",
    "UTF-8 safe streaming detection uses rune-aware holdback to avoid corrupting multi-byte characters"
  ],
  "existing_conventions": [
    "Do not leak protocol markers to clients (tests assert this)",
    "Bound protocol buffering to avoid unbounded memory growth"
  ],
  "constraints_discovered": [
    "Protocol detection must move from <function_call> tags to b4u2cc trigger + <invoke> XML",
    "Parser must only surface the first tool call (ignore subsequent <invoke> blocks)",
    "Parse failure must fall back to emitting text (no synthetic tool calls)"
  ],
  "open_questions": [],
  "dependencies": [
    "Existing OpenAI and Anthropic transformer composition must continue to work under the new protocol"
  ],
  "risks": [
    "Streaming rewrite must preserve UTF-8 correctness and avoid excessive buffering/CPU overhead"
  ],
  "success_criteria_hints": [
    "OpenAI stream emits tool_calls deltas and finishes with finish_reason=tool_calls without leaking trigger/invoke tags",
    "Non-stream response restores message.tool_calls and strips protocol text from message.content"
  ]
}
```

```json
{
  "module_name": "reference: b4u2cc toolify protocol semantics",
  "existing_structures": [
    "Trigger: random <<CALL_{6hex}>> (k8s-migration/b4u2cc/deno-proxy/src/signals.ts)",
    "Prompt injection: injected system prompt describes trigger + <invoke>/<parameter> tool calls (k8s-migration/b4u2cc/deno-proxy/src/prompt_inject.ts)",
    "Parser: detect trigger then parse first <invoke> into a tool_call event; ignore subsequent invokes (k8s-migration/b4u2cc/deno-proxy/src/parser.ts)"
  ],
  "existing_conventions": [
    "Tools protocol is only enabled when tools exist; otherwise passthrough",
    "Arguments parsing prefers JSON.parse; fallback to raw string"
  ],
  "constraints_discovered": [
    "Semantic match only (wording/details may differ), but trigger+invoke behavior must match",
    "tool_choice is not used as a constraint signal for prompt semantics",
    "Only first invoke is actionable"
  ],
  "open_questions": [],
  "dependencies": [],
  "risks": [
    "Embedding control characters from the reference prompt (e.g. \\b) is likely unnecessary and may cause operational issues"
  ],
  "success_criteria_hints": [
    "Given trigger+invoke output, the parser reliably extracts tool name and parameters"
  ]
}
```

## Aggregated Constraint Sets

### Hard Constraints

- Preserve gating semantics:
  - `toolcall_compat` only for OpenAI groups + `POST /v1/chat/completions` (`internal/proxy/compat_predicates.go`).
  - `anthropic_compat` only for OpenAI groups + `POST /v1/messages` (`internal/proxy/compat_predicates.go`).
- Preserve client contracts:
  - OpenAI callers must receive OpenAI-shaped JSON/SSE.
  - Anthropic callers must receive Anthropic-shaped JSON/SSE.
- Preserve transformer-safe upstream body handling: any parsing transformer must force `Accept-Encoding: identity` (`internal/proxy/server.go`).
- No protocol leakage: the trigger signal and `<invoke>/<parameter>/<tool_result>` tags must never appear in client-visible content.
- Adopt b4u2cc semantics:
  - Per-request random trigger signal in the form `<<CALL_{6hex}>>`.
  - Tool call is expressed via `<invoke name="..."> ... <parameter name="...">...</parameter> ... </invoke>`.
  - Only the first tool call per response is actionable; ignore subsequent invokes.
  - `tool_choice` semantics are ignored (still remove unsupported fields upstream).
- Backward compatibility is not required: the legacy `<function_call>` protocol is removed in favor of the new protocol.

### Soft Constraints

- Keep changes localized to `internal/proxy` and existing transformer plumbing.
- Avoid logging raw prompts, tool schemas, or user content; log only counts/lengths/seeds.

## Verifiable Success Criteria (Draft)

- Request rewrite:
  - When tools are present, upstream request body has `tools`/`tool_choice` removed and an injected system prompt that defines trigger+invoke format.
  - A per-request trigger signal is generated and is available to the response transformer.
- OpenAI non-stream:
  - Upstream content containing trigger+invoke is converted into `choices[0].message.tool_calls` and `finish_reason="tool_calls"`.
  - `choices[0].message.content` contains no trigger/invoke tags.
- OpenAI stream:
  - Upstream SSE that emits trigger+invoke results in `delta.tool_calls` frames and finishes with `finish_reason="tool_calls"` and `[DONE]`.
  - No trigger/invoke tags are emitted as `delta.content`.
- Parse failure:
  - Malformed invoke blocks do not produce tool_calls/tool_use; content is treated as text.
- UTF-8:
  - Multi-byte text before the trigger remains intact and never emits replacement characters.
