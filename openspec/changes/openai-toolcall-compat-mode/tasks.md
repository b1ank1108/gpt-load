## 1. 数据模型与 API

- [x] 1.1 在 `internal/models/types.go` 为 `models.Group` 增加 `toolcall_compat` 字段（默认 false），并确认 AutoMigrate 可自动加列
- [x] 1.2 在 `internal/handler/group_handler.go` 的创建/更新/响应结构中透传 `toolcall_compat`，并对非目标分组做 server-side 规范化为 false
- [x] 1.3 在 `internal/services/group_service.go` 创建/更新流程中落库 `toolcall_compat` 并触发 `GroupManager.Invalidate()`
- [x] 1.4 在 `internal/services/group_manager.go` 缓存加载阶段做兜底规范化（仅 `channel_type=openai && group_type!=aggregate` 允许为 true）

## 2. 管理端开关入口

- [x] 2.1 在 `web/src/types/models.ts` 为 `Group` 增加 `toolcall_compat?: boolean`
- [x] 2.2 在 `web/src/components/keys/GroupFormModal.vue` 的“高级配置”折叠区增加开关，仅在 `channel_type=openai && group_type!=aggregate` 显示
- [x] 2.3 在 `web/src/components/keys/GroupFormModal.vue` 提交前兜底：当不满足条件时强制 `toolcall_compat=false`，避免隐藏字段脏值
- [x] 2.4 在 `web/src/locales/zh-CN.ts`、`web/src/locales/en-US.ts`、`web/src/locales/ja-JP.ts` 增加 label/tooltip 文案
- [x] 2.5 在 `web/src/components/keys/GroupInfoCard.vue` 的“详细信息/基础信息”区增加 `Tool Call 兼容模式` 字段展示（仅 `channel_type=openai && group_type!=aggregate`）
- [x] 2.6 在 `web/src/components/keys/GroupList.vue` 中，为 `channel_type=openai && group_type!=aggregate && toolcall_compat===true` 的分组在分组名称右侧增加图标标识，并使用 `keys.toolcallCompatTooltip` 作为 tooltip

## 3. 代理请求预处理（OpenAI Chat Completions）

- [x] 3.1 在 `internal/proxy` 新增工具调用兼容请求预处理：检测 `tools/tool_choice` 并生成 per-request trigger signal
- [x] 3.2 将 `tools/tool_choice` 转为注入 system message，并在转发前移除 `tools/tool_choice`
- [x] 3.3 预处理 messages：`assistant.tool_calls` → 追加协议化文本到 `assistant.content` 且移除 `tool_calls` 字段
- [x] 3.4 预处理 messages：`role=tool` → 转为 `role=user` 文本上下文（含 tool_call_id 回溯校验）；不可回溯时返回 4xx 且不转发
- [x] 3.5 在 `internal/proxy/server.go` 接入：对 `channel_type=openai && toolcall_compat=true` 且命中 `POST /v1/chat/completions` 的请求执行预处理（`/v1/messages` 先做 anthropic→openai 再进入该流程）

## 4. 代理响应转换（非流式 + SSE）

- [x] 4.1 非流式：解析上游 OpenAI chat completion JSON，从协议化内容提取 tool calls 并回填 `choices[0].message.tool_calls` + `finish_reason=tool_calls`
- [x] 4.2 SSE：实现流式 detector（trigger 前透传、trigger 后缓冲解析），生成 `delta.tool_calls` chunk 并以 `finish_reason=tool_calls` + `[DONE]` 结束
- [x] 4.3 解析失败策略：不生成不确定 `tool_calls`；以 `finish_reason=stop` + `[DONE]` 结束并记录日志

## 5. 与 Anthropic 兼容模式组合

- [x] 5.1 非流式：当 `anthropic_compat && toolcall_compat` 同开且命中 `/v1/messages`，在 OpenAI→Anthropic 转换前先完成 tool_calls 还原，确保输出 `tool_use`
- [x] 5.2 SSE：当 `anthropic_compat && toolcall_compat` 同开且 `stream=true`，在 Anthropic SSE 输出路径中支持协议化工具调用（保持“先 toolcall 后 anthropic”的响应顺序）

## 6. 测试

- [x] 6.1 为请求预处理与 `tool_call_id` 回溯校验增加单元测试（含错误分支）
- [x] 6.2 为非流式响应解析（content → tool_calls）增加单元测试
- [ ] 6.3 为 SSE 场景增加单元测试：OpenAI SSE 产出 tool_calls；Anthropic SSE 产出 tool_use（组合场景）
- [ ] 6.4 冒烟：开关关闭时行为完全透传；开启时仅在目标分组与目标路径生效
