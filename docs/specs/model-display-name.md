# 模型显示名（Model name）

## 产品规则

- 模型编辑器（设置 → 模型供应商 → 添加/编辑模型）在「模型 ID」下方新增可选字段「模型名称」，是该模型的**别名/短名**，只影响展示。
- 默认留空，留空时显示名等于模型 ID。用户可改成任意文本（如 `sonnet-5-5`），清空即恢复为模型 ID。
- 对话输入区的模型选择器与触发器按 `供应商/显示名` 展示，例如模型 ID 为 `cst/claude/claude-sonnet-5-5` 的模型显示为 `magpie/sonnet-5-5`。
- **向供应商发请求始终使用模型 ID**。显示名不进入 `ModelSelection`、协议、会话记录和请求体；它只存在于展示边界。
- 显示名不要求唯一，也不参与任何查找：选择、切换、重试、子代理选型、`/model` 等都按 `providerId + modelId` 识别。
- 设置页的模型列表行继续展示模型 ID（它是编辑和排序的技术标识），不改为显示名。
- 历史任务里已经写入的「模型切换」记录保留当时记录的文本，不回溯改写。

## 状态所有者与接口

| 状态             | 所有者                                                                     | 说明                                                                                           |
| ---------------- | -------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------- |
| `name` 字段      | Personal Provider Config 中该模型的精确规则（`config.name`，可选）         | 与 `enabled` 同级的模型级叶子；智能规则与手动规则都允许携带                                    |
| 注册表中的显示名 | `ProviderModel.config.name`（`serializeRegistryModelConfig` 原样输出）     | 来自规则叠加，是 UI 与 CLI 读取显示名的唯一来源                                                |
| 选择器条目名     | `buildRegistryModelSelectGroups` 的 `item.name` = `config.name ?? modelId` | 触发器与菜单都读它，保持 `供应商/显示名` 的既有拼接规则不变                                    |
| CLI 模型选项标签 | `toModelOption.label` = `config.name ?? modelId`                           | 会话 `settings.model.available[].label`，TUI 与 UI 投影共用；`ref` 仍是 `providerId + modelId` |

依赖方向不变：`@nex/shared` 定义数据合同 → `@nex/provider` 叠加与校验 → UI/CLI 只读结果。UI 不直接读写规则文件，保存仍走既有 provider-settings 服务。

## 不变量与失败语义

1. `name` 是可选叶子：完整配置校验（`validateComplete`）不要求它存在；旧配置文件无需迁移。
2. 取值经 `trim` 后长度 1–64，不允许控制字符；空串在编辑器提交时转为「不设置」，不写入空字符串。
3. `name` 与「跟随推荐配置/固定配置」无关：切回推荐配置、恢复默认值都**保留**它（与 `enabled` 同一条规则），只有用户清空输入才会删除。
4. 模型 ID 重命名沿用规则重命名，`name` 随规则一起迁移。
5. 内置规则（recommended）不声明 `name`；显示名只来自个人规则，不会被推荐配置覆盖。
6. 旧版本读取带 `name` 的配置会因 strict schema 拒绝未知字段，这是可接受的降级方向（与已有字段新增方式一致）。

## 验收

1. 编辑模型，填写「模型名称」为 `sonnet-5-5` 并保存，对话输入区触发器显示 `magpie/sonnet-5-5`，菜单条目显示 `sonnet-5-5`。
2. 选择该条目后发送消息，请求体里的 `model` 仍是完整模型 ID；会话里记录的 `ModelSelection.modelId` 不变。
3. 清空「模型名称」保存后，显示回到模型 ID。
4. 切到「跟随推荐配置」或点击「恢复默认」，模型名称保留。
5. 修改模型 ID 后，模型名称保留。
6. 单测覆盖：schema 接受/拒绝 `name`；`ModelConfig` 叠加与序列化；草稿提交写入/清除 `name` 且不受推荐模式切换影响；`toModelOption` 与选择器条目回退到模型 ID。
7. `pnpm typecheck`、`pnpm lint`、`pnpm architecture:check --changed` 通过。
