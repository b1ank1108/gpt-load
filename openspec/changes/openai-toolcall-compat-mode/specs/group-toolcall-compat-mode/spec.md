## ADDED Requirements

### Requirement: Tool Call 兼容模式开关仅对 OpenAI 标准分组可配置
管理端分组创建/编辑弹窗 SHALL 提供 `toolcall_compat` 开关，并 MUST 将其放置在“高级配置”折叠区内。
该开关 MUST 仅在标准分组且 `channel_type=openai` 时显示；其他渠道类型分组或聚合分组 MUST NOT 显示该开关。
当 `channel_type!=openai` 或 `group_type=aggregate` 时，UI MUST 强制将表单态 `toolcall_compat=false`，并 MUST 在提交前兜底强制为 false。

#### Scenario: OpenAI 标准分组可在高级配置中配置
- **WHEN** 管理员打开标准分组（`group_type!=aggregate`）且 `channel_type=openai` 的创建/编辑弹窗
- **THEN** “高级配置”区内展示 `Tool Call 兼容模式` 开关，且“基础信息”区内不展示该开关

#### Scenario: 非 OpenAI 标准分组不可配置
- **WHEN** 管理员打开 `channel_type!=openai` 或 `group_type=aggregate` 的创建/编辑弹窗
- **THEN** UI 不展示 `Tool Call 兼容模式` 开关，且提交 payload 中 `toolcall_compat` 恒为 `false`

### Requirement: Tool Call 兼容模式状态在详情与列表可见
管理端分组详情视图（“详细信息/基础信息”区）SHALL 展示 `Tool Call 兼容模式` 状态（仅 `channel_type=openai && group_type!=aggregate`），展示为 `common.enable/common.disable`（始终展示）。
左侧分组列表中，对已开启 `toolcall_compat` 的 OpenAI 标准分组 SHALL 展示图标标识，并提供 tooltip 说明（复用 `keys.toolcallCompatTooltip`）。

#### Scenario: 详情页展示 Tool Call 兼容模式状态
- **WHEN** 管理员查看 `channel_type=openai && group_type!=aggregate` 的分组详情
- **THEN** “详细信息/基础信息”区展示 `Tool Call 兼容模式` 字段，值为 `enable/disable`

#### Scenario: 分组列表展示 Tool Call 兼容模式标识
- **WHEN** 左侧分组列表中存在 `channel_type=openai && group_type!=aggregate && toolcall_compat=true` 的分组
- **THEN** 分组名称右侧展示图标标识，hover 时展示 tooltip

### Requirement: 分组 CRUD 透传并规范化 toolcall_compat 字段
后端分组创建/更新接口 MUST 支持 `toolcall_compat` 字段（bool），分组查询响应 MUST 返回 `toolcall_compat` 字段（不存在时视为 `false`）。
当分组不满足 `group_type!=aggregate && channel_type=openai` 时，后端 MUST 将 `toolcall_compat` 规范化为 `false`（无论请求传入值为何），以避免非目标分组误开启产生不可预期行为。

#### Scenario: OpenAI 标准分组可持久化开关值
- **WHEN** 创建或更新 `group_type!=aggregate && channel_type=openai` 的分组并提交 `toolcall_compat=true`
- **THEN** 后端响应中 `toolcall_compat=true`，且后续查询该分组仍为 `true`

#### Scenario: 非目标分组强制规范化为 false
- **WHEN** 创建或更新 `channel_type!=openai` 或 `group_type=aggregate` 的分组并提交 `toolcall_compat=true`
- **THEN** 后端响应中 `toolcall_compat=false`，且后续查询该分组仍为 `false`

### Requirement: Tool Call 兼容模式对 OpenAI Chat Completions 的请求进行预处理
当 `toolcall_compat=true` 且请求为 `POST /v1/chat/completions` 时，代理在转发上游前 SHALL 执行如下兼容转换：
- 若请求 body 包含 `tools` 或 `tool_choice`，代理 MUST 将工具定义与选择策略转换为注入提示词（system message），并 MUST 在转发前移除 `tools` 与 `tool_choice` 字段。
- 若 `messages` 中存在 `role=tool` 的消息，代理 MUST 将其转换为 `role=user` 的纯文本上下文，并 MUST 通过 `tool_call_id` 在同一请求的历史 `assistant.tool_calls` 中回溯对应工具名与入参；当无法回溯时代理 MUST 返回 4xx（请求非法）。
- 若 `messages` 中存在 `assistant.tool_calls`，代理 MUST 将其转换为追加到 `assistant.content` 的协议化文本（包含 trigger signal + XML 结构化 tool call），并 MUST 在转发前移除原 `tool_calls` 字段。

#### Scenario: tools/tool_choice 被移除且注入 system message
- **WHEN** 客户端对开启 `toolcall_compat` 的分组发送 `POST /v1/chat/completions` 且请求 body 含 `tools/tool_choice`
- **THEN** 上游实际接收的 payload 不包含 `tools/tool_choice` 字段，且 `messages[0]` 为包含工具协议提示词的 system message

#### Scenario: tool 消息被转换为上游可理解文本
- **WHEN** 客户端请求 `messages` 包含 `role=tool` 且其 `tool_call_id` 可在同一请求内找到对应 `assistant.tool_calls`
- **THEN** 上游实际接收的 `messages` 中不再包含 `role=tool`，并包含等价的 `role=user` 文本上下文（包含工具名、入参与执行结果）

#### Scenario: tool_call_id 不可回溯时拒绝请求
- **WHEN** 客户端请求 `messages` 包含 `role=tool` 但其 `tool_call_id` 无法在同一请求的历史 `assistant.tool_calls` 中回溯
- **THEN** 代理返回 4xx 且不向上游转发该请求

### Requirement: Tool Call 兼容模式将上游内容协议还原为 OpenAI tool_calls（非流式）
当 `toolcall_compat=true` 且客户端请求非流式（`stream!=true`）时：
若上游返回的 `choices[0].message.content` 中包含协议化工具调用（trigger signal + `<function_calls>...</function_calls>`），代理 MUST 从中解析出工具调用列表并回填为 OpenAI `choices[0].message.tool_calls`，同时 MUST 从返回给客户端的 `content` 中移除协议化工具调用文本，仅保留工具调用之前的自然语言内容。
当解析成功且存在工具调用时，代理 MUST 将 `choices[0].finish_reason` 置为 `tool_calls`。
当上游未输出协议化工具调用时，代理 MUST 保持响应透传（不强制修改 `finish_reason`）。

#### Scenario: 协议化工具调用被回填为 tool_calls
- **WHEN** 上游返回 `choices[0].message.content` 包含协议化工具调用文本
- **THEN** 客户端收到的响应包含 `choices[0].message.tool_calls`，且 `finish_reason=tool_calls`，并且 `content` 不包含 trigger/XML 片段

#### Scenario: 无协议化工具调用时透传响应
- **WHEN** 上游返回内容不包含 trigger/XML 结构
- **THEN** 代理不新增 `tool_calls` 字段，且响应主体保持与上游等价（除必要 header 透传外）

### Requirement: Tool Call 兼容模式支持 SSE 流式响应的 tool_calls 还原
当 `toolcall_compat=true` 且客户端请求 `stream=true` 时，代理 MUST 在 SSE 流中支持 tool_calls 还原：
- 在检测到 trigger signal 之前，代理 SHALL 透传自然语言内容增量（不透出 trigger/XML 片段）。
- 当解析到完整工具调用列表时，代理 MUST 生成符合 OpenAI 流式规范的 `tool_calls` 增量 chunk，并以 `finish_reason=tool_calls` 结束该轮流式响应，随后发送 `[DONE]`。
- 当工具调用协议解析失败时，代理 MUST NOT 生成不确定的 `tool_calls` 结构；代理 SHALL 以 `finish_reason=stop` 结束并发送 `[DONE]`（可记录日志用于排查）。

#### Scenario: SSE 流中产出 tool_calls 并以 tool_calls 结束
- **WHEN** 开启 `toolcall_compat` 的分组收到 `stream=true` 请求且上游输出协议化工具调用
- **THEN** 客户端在 SSE 流中收到包含 `delta.tool_calls` 的 chunk，且最终以 `finish_reason=tool_calls` 结束并收到 `[DONE]`

### Requirement: 与 Anthropic 兼容模式同开时先还原 tool_calls 再转换为 tool_use
当 `toolcall_compat=true` 与 `anthropic_compat=true` 同时开启，且客户端通过 `/v1/messages` 调用时：
代理在响应侧 MUST 先完成 Tool Call 兼容（还原为 OpenAI `tool_calls`），再执行 Anthropic 兼容响应转换，从而确保最终 Anthropic 响应包含正确的 `tool_use` content blocks。
该顺序约束 MUST 同时适用于非流式与 SSE 流式响应。

#### Scenario: /v1/messages 非流式返回包含 tool_use
- **WHEN** 两个开关同开，客户端对 `/v1/messages` 发起非流式请求且上游输出协议化工具调用
- **THEN** 客户端收到的 Anthropic 响应 `content` 中包含 `type=tool_use` block

#### Scenario: /v1/messages 流式返回包含 tool_use 事件
- **WHEN** 两个开关同开，客户端对 `/v1/messages` 发起 `stream=true` 请求且上游输出协议化工具调用
- **THEN** 客户端收到的 Anthropic SSE 流中包含等价的 tool_use 语义输出（而非原始 trigger/XML 片段）
