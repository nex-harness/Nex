# pets-ui

宠物呈现层的意图：把「设置里的选中宠物 / 开关 / 大小 / 显示方式」与 sessions-index 派生的语义状态，
接到双端呈现（Web 主窗口浮动挂件、Desktop 独立悬浮窗）。产品规则见 `docs/specs/desktop-pets.md`。

- **唯一公开入口**：`index.ts`（`PetRuntime`、`PetsSettingsSection`、`PetSprite` 与手势/呈现纯函数）。
- **唯一数据源**：宠物目录来自 `IServiceAccessor.petService`（`@nex/services` 的 `pets` 模块公开契约），
  偏好来自 `ISettingService` 的 `AppSettings.pet`，语义状态来自 sessions-index（纯派生，不落盘）。
- **平台差异**：通过 `IPlatformService.syncPetState` / `onOpenSettingsSection` 注入，不直接使用 `window.nex`。

不变量：

- 无宠物、开关关闭或选中宠物不可用时，不渲染任何宠物 UI。
- 设置写入必须经 `mergePetSettings`，避免覆盖 main 进程持久化的落点字段。
- 动画状态只读，不写回 setting / sessions-index。
- 桌面悬浮窗的窗口层级与激活策略不属于本模块；main 侧只经 `desktopDockVisibility` 管理 Dock。
