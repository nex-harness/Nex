# pets-desktop

桌面宠物悬浮窗的 main 侧：单例 `BrowserWindow` 的创建/销毁、拖拽状态机、动量与吸附、
按显示器持久化落点、on-demand 全局快捷键，以及 pet-window → main 的动作通道（zod 校验）。

- **唯一公开入口**：`petWindow.ts`（`createPetWindowController`、`registerPetWindowActionHandler`）。
- **依赖注入**：`BrowserWindow` / `screen` / `app` / `globalShortcut` / `logger` 由调用方传入；模块不直接读取 `electron` 的 app 实例。
- **边界**：不读写 `setting.json`（调用方经 `onPlacementPersist` 持久化），不管理 Dock 可见性。

不变量：

- 窗口层级固定 `floating` + `setVisibleOnAllWorkspaces(true, { skipTransformProcessType: true })`，**不得**使用 `visibleOnFullScreen`。
- 不调用 `app.dock.show/hide` 或 `setActivationPolicy`：Dock 可见性唯一所有者是 `desktopDockVisibility`。
- 宠物窗不计入应用窗口列表（主窗口全关后不阻塞 `window-all-closed`），`prepareAppQuit` 时必须 `destroy()`。
- 落点只在拖拽落定 / 显示器变化时持久化；没有 `pet` 设置时调用方不得写入。
