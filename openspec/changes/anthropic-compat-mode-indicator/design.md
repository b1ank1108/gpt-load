## Context

- 现有数据链路已包含 `anthropic_compat`（后端响应 + 前端模型 + 提交 payload）。
- 现状 UI：
  - 开关位于基础信息区（需迁移到高级配置）。
  - 分组详情“详细信息”未展示该字段。
  - 左侧分组列表未标识开启状态。

## Goals / Non-Goals

**Goals:**
- 将 `Anthropic 兼容模式` 开关移动到“高级配置”（仅 OpenAI 标准分组可配置）。
- 分组详情“详细信息”始终展示该状态（仅 OpenAI 标准分组）。
- 左侧分组列表对开启状态展示 icon + tooltip。

**Non-Goals:**
- 不修改后端字段、API、以及任何协议转换/代理逻辑。
- 不为聚合分组引入该配置入口。

## Decisions

- 保持“仅 `channel_type=openai` 且 `group_type!=aggregate` 的分组可配置”的约束。
- 当 `channel_type!=openai` 时，表单 MUST 强制将 `anthropic_compat=false`（包含提交前兜底），避免隐藏字段携带脏值。
- 创建/编辑弹窗中，该开关放置在“高级配置”折叠区内容顶部（位于其他高级配置项之前）。
- 详情页对 OpenAI 标准分组始终展示状态（`common.enable/common.disable`）。
- 左侧分组列表仅对开启状态展示 `🧠` 标识（置于分组名称右侧），tooltip 内容仅使用 `keys.anthropicCompatTooltip`。

## Risks / Trade-offs

- [高级配置默认折叠导致发现性下降] → [通过左侧列表 icon+tooltip 提升可见性]
