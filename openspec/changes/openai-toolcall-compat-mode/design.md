## Context

当前 `channel_type=openai` 分组在代理侧主要执行参数覆盖、模型重定向与原样转发；仅在开启 `anthropic_compat` 且命中 `POST /v1/messages` 时做 Anthropic→OpenAI 请求转换与 OpenAI→Anthropic 响应转换（`internal/proxy/server.go` + `internal/proxy/anthropic_compat.go`）。

部分 OpenAI 兼容上游不支持原生 `tools/tool_calls` 协议，直接透传会导致请求失败或工具调用失效；需要在不改变客户端 OpenAI SDK 使用方式的前提下，通过分组级开关启用“协议化兼容”。

## Goals / Non-Goals

**Goals:**
- 为 OpenAI 标准分组提供 `toolcall_compat` 开关（默认关闭），开启后让不支持 tool calling 的 OpenAI 兼容上游可用。
- 覆盖 `/v1/chat/completions` 的请求/响应（含 SSE）兼容转换：请求侧注入提示词并移除 `tools/tool_choice`；响应侧从协议化内容还原为标准 `tool_calls`。
- 与 `anthropic_compat` 同开且命中 `/v1/messages` 时，响应侧固定先做 toolcall 还原再做 Anthropic 兼容，确保产出 `tool_use`。

**Non-Goals:**
- 不保证上游一定按协议输出（提示词驱动的兼容模式无法做到强一致）。
- 不改动除 `/v1/chat/completions`（以及 `anthropic_compat` 已覆盖的 `/v1/messages`）之外的接口行为。
- 不新增列表/详情额外标识与展示（仅新增开关入口与代理侧行为）。

## Decisions

1) **开关存储与规范化**
- 在 `models.Group` 增加 `ToolcallCompat bool \`gorm:"default:false" json:"toolcall_compat"\``，由 AutoMigrate 自动加列（向后兼容，默认关闭）。
- 在创建/更新入口对非目标分组（`channel_type!=openai` 或 `group_type=aggregate`）强制规范化为 `false`，避免误开导致不可预期代理行为；代理侧也以同一谓词再兜底一次。

2) **兼容协议选择：Toolify 风格的 trigger + XML**
- 采用与 Toolify 等价的“协议化工具调用”：
  - 注入 system prompt 告知可用 tools 与输出格式；
  - 用 per-request trigger signal 作为分隔符，避免自然语言误命中；
  - 以 `<function_calls><function_call><tool>..</tool><args_json><![CDATA[...]]></args_json></function_call>...</function_calls>` 表达结构化调用。
- 解析侧仅在检测到 trigger 后进入解析模式，避免对普通内容引入额外开销。

3) **请求侧转换位置：在 proxy 入口统一处理**
- 在 `ProxyServer.HandleProxy` 读完 body 后、转发上游前统一做请求预处理：
  - 对 `/v1/messages`：先沿用现有 Anthropic→OpenAI 转换（得到 OpenAI chat completion payload）。
  - 对 OpenAI chat completion payload：若启用 `toolcall_compat` 且出现 `tools/tool_choice` 或消息中存在 `role=tool` / `assistant.tool_calls`，则执行预处理：
    - 将 `tools/tool_choice` 转为注入 system message，并从上游 payload 移除；
    - 将 `role=tool` 转为 `role=user` 文本上下文（需通过 `tool_call_id` 回溯同请求内历史 `assistant.tool_calls`）；
    - 将 `assistant.tool_calls` 转为追加到 `assistant.content` 的协议化文本，并移除原字段。

4) **响应侧顺序与实现：组合优先保证正确性**
- `/v1/chat/completions`（OpenAI 输出）：使用 `toolcallCompatTransformer` 解析非流式/流式响应，将协议化内容还原为标准 `tool_calls`。
- `/v1/messages`（Anthropic 输出）：
  - 若仅 `anthropic_compat`：沿用现有 `anthropicCompatTransformer`。
  - 若 `anthropic_compat + toolcall_compat`：使用专用组合 transformer（或在 anthropic transformer 内部增加可选 toolcall 解析步骤），在输出 Anthropic 之前先完成 OpenAI `tool_calls` 还原，满足“先 toolcall 后 Anthropic”的响应侧顺序约束。
  - 为避免引入全局可组合 transformer 框架，优先实现最小组合路径（仅覆盖该组合场景）。

5) **SSE 策略：流式检测 + 截断 + 重组**
- OpenAI SSE：
  - trigger 前：透传 `delta.content`；
  - trigger 后：不再向客户端透出协议片段，转为缓冲解析；
  - 解析成功：向客户端输出 `delta.tool_calls` 的 chunk，并以 `finish_reason=tool_calls` + `[DONE]` 结束。
- 解析失败：不生成不确定的 `tool_calls`，以 `finish_reason=stop` + `[DONE]` 结束（并记录日志）。
- Anthropic SSE（组合场景）：沿用现有 Anthropic SSE 输出结构，工具调用以 `tool_use` 语义输出；实现方式可复用现有 OpenAI→Anthropic 流式转换逻辑，新增对协议化工具调用的检测与注入点。

## Risks / Trade-offs

- [上游不遵循协议输出] → 仅在开关开启且出现 tools/工具上下文时启用；解析失败时 fail-safe，不产出错误结构。
- [SSE 解析复杂、易产生边界问题] → 使用 trigger 分段、限制 buffer 上限、仅在解析完成后输出工具调用 chunk；增加单测覆盖典型与异常流。
- [性能开销] → 非匹配场景完全透传；匹配场景只做必要的 JSON/SSE 扫描；避免全局链式 transformer 带来的额外抽象成本。
