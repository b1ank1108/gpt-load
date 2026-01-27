## 1. 分组创建/编辑（高级配置）

- [x] 1.1 将 `Anthropic 兼容模式（anthropic_compat）` 开关从基础信息区移动到 `web/src/components/keys/GroupFormModal.vue` 的“高级配置”折叠区内容顶部（位于其他高级配置项之前）
- [x] 1.2 保持现有约束：仅 `channel_type=openai` 且 `group_type!=aggregate` 的分组显示该开关；提交 payload 字段不变
- [x] 1.3 当 `channel_type!=openai` 时自动将 `anthropic_compat=false`，并在提交前兜底强制为 false（避免隐藏字段提交脏值）

## 2. 分组详情（详细信息展示）

- [x] 2.1 在 `web/src/components/keys/GroupInfoCard.vue` 的“详细信息/基础信息”区增加 `Anthropic 兼容模式` 字段展示（仅 `channel_type=openai && group_type!=aggregate`）
- [x] 2.2 状态展示为 `common.enable/common.disable`（始终展示）

## 3. 左侧分组列表标识（icon + tooltip）

- [x] 3.1 在 `web/src/components/keys/GroupList.vue` 中，为 `channel_type=openai && group_type!=aggregate && anthropic_compat===true` 的分组在分组名称右侧增加 `🧠` 图标标识
- [x] 3.2 hover tooltip 仅复用现有文案 `keys.anthropicCompatTooltip`，不新增 i18n key

## 4. 验证

- [ ] 4.1 冒烟：创建/编辑 OpenAI 分组切换开关 → 详情页状态一致 → 左侧列表开启时出现标识
