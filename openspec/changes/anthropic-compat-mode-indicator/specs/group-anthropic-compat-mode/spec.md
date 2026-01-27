## ADDED Requirements

### Requirement: 配置开关位于高级配置且仅 OpenAI 标准分组可配置
管理端分组创建/编辑弹窗 SHALL 将 `anthropic_compat` 配置开关放置在“高级配置”折叠区内，并 MUST NOT 将其放置在“基础信息”区。
该开关 MUST 仅在标准分组且 `channel_type` 为 `openai` 时显示；其他渠道类型分组 MUST NOT 显示该开关。
标准分组在前端的判定规则 SHALL 为 `group_type !== 'aggregate'`（`group_type` 缺省视为标准分组）。
当 `channel_type!=openai` 时，UI MUST 强制将表单态 `anthropic_compat=false`，并 MUST 在提交前兜底强制为 false。
该开关在“高级配置”折叠区内的位置 SHALL 固定为折叠内容顶部（位于其他高级配置项之前）。

#### Scenario: OpenAI 标准分组可在高级配置中配置
- **WHEN** 管理员打开标准分组（group_type!=aggregate）且 channel_type=openai 的创建/编辑弹窗
- **THEN** “高级配置”区内展示 `Anthropic 兼容模式` 开关，且“基础信息”区内不展示该开关

#### Scenario: 非 OpenAI 标准分组不可配置
- **WHEN** 管理员打开标准分组（group_type!=aggregate）且 channel_type!=openai 的创建/编辑弹窗
- **THEN** UI 不展示 `Anthropic 兼容模式` 开关

#### PBT Properties
- [INVARIANT] `Anthropic 兼容模式` 开关在创建/编辑弹窗中仅当 `group_type!='aggregate' && channel_type=openai` 时出现，且仅出现在“高级配置”折叠内容内（不出现在“基础信息”区） → [FALSIFICATION STRATEGY] 生成 `(group_type, channel_type)` 的全组合并渲染弹窗，断言 DOM 中开关出现位置满足/不满足谓词时的可见性与区域约束
- [INVARIANT] 当 `channel_type!=openai` 时提交 payload 中 `anthropic_compat` 恒为 `false` → [FALSIFICATION STRATEGY] 生成随机表单态（含 `anthropic_compat=true`）并将 `channel_type` 置为非 openai，触发提交并断言最终 payload 归一化结果

### Requirement: 详细信息始终展示 OpenAI 分组的兼容模式状态
分组详情“详细信息”视图 SHALL 对标准 OpenAI 分组始终展示 `Anthropic 兼容模式` 状态（无论开关是否开启）。
该字段在 UI 中的值展示 SHALL 使用 `common.enable/common.disable`（`anthropic_compat` 为 `true` 显示 enable，否则显示 disable；`undefined` 视为 `false`）。
非标准 OpenAI 分组（包含聚合分组或 `channel_type!=openai`）MUST NOT 展示该字段。

#### Scenario: 详情展示开启状态
- **WHEN** 查看标准 OpenAI 分组且 anthropic_compat=true 的详细信息
- **THEN** UI 展示 `Anthropic 兼容模式` 字段且状态为启用

#### Scenario: 详情展示关闭状态
- **WHEN** 查看标准 OpenAI 分组且 anthropic_compat=false 的详细信息
- **THEN** UI 展示 `Anthropic 兼容模式` 字段且状态为禁用

#### PBT Properties
- [INVARIANT] 对任意分组：当且仅当 `group_type!='aggregate' && channel_type=openai` 时，“详细信息”视图展示 `Anthropic 兼容模式` 字段 → [FALSIFICATION STRATEGY] 生成随机分组对象并渲染详情视图，断言字段存在性与谓词等价
- [INVARIANT] 对任意标准 OpenAI 分组：字段值与 `!!anthropic_compat` 保持一致（true→`common.enable`，false/undefined→`common.disable`） → [FALSIFICATION STRATEGY] 生成 `anthropic_compat` 的三态（true/false/undefined）并渲染详情视图，断言展示值与三态映射一致

### Requirement: 左侧分组列表以图标+tooltip 标识已开启兼容模式的分组
左侧分组列表 SHALL 对 `channel_type=openai` 且 `anthropic_compat=true` 的标准分组展示图标标识，并在 hover 时显示 tooltip 说明该模式含义。
不满足条件的分组 MUST NOT 展示该图标标识。
图标标识 SHALL 使用 `🧠`，并放置在分组名称右侧。
tooltip 内容 MUST 仅使用 `keys.anthropicCompatTooltip`。

#### Scenario: 列表展示标识与 tooltip
- **WHEN** 左侧分组列表渲染到满足条件的分组
- **THEN** UI 展示图标标识，且 hover 可看到 tooltip 说明

#### Scenario: 其他分组不展示标识
- **WHEN** 左侧分组列表渲染到不满足条件的分组
- **THEN** UI 不展示兼容模式图标标识

#### PBT Properties
- [INVARIANT] 列表中 `🧠` 标识出现当且仅当 `group_type!='aggregate' && channel_type=openai && anthropic_compat===true` → [FALSIFICATION STRATEGY] 生成随机分组列表并渲染，统计每项的标识出现性与谓词等价
- [INVARIANT] 任意出现的标识其 tooltip 内容恒等于 `keys.anthropicCompatTooltip` → [FALSIFICATION STRATEGY] 生成满足条件的分组并触发 hover，断言 tooltip 内容不包含额外标题/字段且与 i18n key 对应文本一致
