# pets-shared

Codex 兼容宠物格式的共享契约：帧网格与行约定常量、`pet.json` zod schema 与校验、
悬浮窗拖拽物理（速度/动量/吸附/落点还原）、`PetSettings` / `PetWindowState` / `PetWindowAction` 类型与动作 schema。

- **唯一公开入口**：`index.ts`（全量导出）与 `contract.ts`（跨层调用方使用的稳定面）。
- **依赖**：无（只依赖 `zod`）；必须保持纯数据与纯函数，不引入 Electron、Node IO 或平台 API。
- **消费方**：`pets`（services）、`pets-ui`（ui）、`pets-desktop`（desktop）、server 的宠物静态路由。

不变量：

- 精灵图网格必须铺满（宽 = 帧宽 × 列数，高为帧高的整数倍且不少于声明行数）。
- `spritesheetPath` 只能是目录内相对路径；解析后的绝对路径必须仍在 pets 根目录内（由 `pets` 服务复核）。
- 设置写入必须经 `mergePetSettings` / `mergePetPlacement`，`mergePetPlacement` 在无 `pet` 设置时返回 `null`。
- `restorePetPlacement` 是建窗、改大小、显示器变化共用的唯一落点算法。
