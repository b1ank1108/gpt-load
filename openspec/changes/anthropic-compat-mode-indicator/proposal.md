## Why

当前管理端对 OpenAI 分组的 `Anthropic 兼容模式（anthropic_compat）` 配置入口位于基础信息区，且在分组详情与左侧分组列表缺少明确的状态展示，导致配置分散、难以快速识别哪些分组已开启兼容模式。

## What Changes

- 将 OpenAI 标准分组的 `Anthropic 兼容模式` 开关移动到“高级配置”折叠区内（不改变字段与提交结构）。
- 在分组“详细信息”视图中始终展示 `Anthropic 兼容模式` 状态（仅 OpenAI 标准分组）。
- 在左侧分组列表中，对已开启 `Anthropic 兼容模式` 的 OpenAI 标准分组展示图标标识，并提供 tooltip 说明。

## Capabilities

### New Capabilities
- `group-anthropic-compat-mode`: 管理端对 OpenAI 分组的 Anthropic 兼容模式配置入口位置、详情展示与列表标识

### Modified Capabilities
- (none)

## Impact

- Frontend:
  - `web/src/components/keys/GroupFormModal.vue`
  - `web/src/components/keys/GroupInfoCard.vue`
  - `web/src/components/keys/GroupList.vue`
- No backend/API changes
