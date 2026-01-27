## Why

当前部分 OpenAI 兼容上游并不支持原生 `tools/tool_calls` 协议，导致客户端按 OpenAI SDK 发送工具调用请求时要么报错、要么工具调用失效，无法在同一套代理与分组体系下平滑接入“非完整 OpenAI”能力的上游。

## What Changes

- 为 `channel_type=openai` 的标准分组新增一个“Tool Call 兼容模式”开关（默认关闭），用于在不改变客户端 OpenAI 协议的前提下，让不支持 tool calling 的上游可用。
- 开关开启后，对 `/v1/chat/completions` 的请求/响应进行兼容转换：
  - 请求侧：将 `tools/tool_choice` 转为注入提示词（移除原字段再转发），并将 `role=tool`、`assistant.tool_calls` 等消息内容转换为上游可理解的纯文本上下文（参考 Toolify 的协议化方案）。
  - 响应侧：从上游输出中解析出结构化工具调用，回填为 OpenAI 标准 `tool_calls`（包含 SSE 场景的流式检测/截断/重组）。
- 当与现有 `Anthropic 兼容模式（anthropic_compat）` 同时开启且通过 `/v1/messages` 调用时，响应处理顺序固定为：先完成 toolcall 兼容（得到 OpenAI `tool_calls`），再执行 Anthropic 兼容响应转换（产出 `tool_use`）。

## Capabilities

### New Capabilities
- `group-toolcall-compat-mode`: OpenAI 分组的 Tool Call 兼容模式开关、请求/响应转换与 SSE 支持，以及与 `anthropic_compat` 同开时的响应处理顺序约束

### Modified Capabilities
- (none)

## Impact

- Backend:
  - `internal/models/types.go`: 分组新增开关字段
  - `internal/handler/group_handler.go` / `internal/services/group_service.go` / `internal/services/group_manager.go`: 开关透传、存储与缓存加载
  - `internal/proxy/server.go` + `internal/proxy/*`: 新增/调整代理侧转换器逻辑（包含与 Anthropic 兼容的组合处理）
  - 新增单元测试覆盖转换逻辑与 SSE 行为
- Frontend:
  - `web/src/components/keys/GroupFormModal.vue`: 在高级配置中增加开关（仅 OpenAI 标准分组可见），并复用现有“隐藏字段兜底”模式
  - `web/src/types/models.ts` + `web/src/locales/*`: 增加字段与文案
