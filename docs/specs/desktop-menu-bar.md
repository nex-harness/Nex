# 桌面菜单栏图标与 Dock 图标开关

## 产品规则

- macOS 桌面端常驻一个菜单栏（状态栏）图标。**左键单击**显示主窗口（窗口全关时重建、最小化时还原、并把应用带到前台）；**右键**弹出菜单：打开 Nex、新建任务、打开工作区、检查更新（仅正式版）、关于、清除所有数据、退出。
- 设置 → 通用新增「在 Dock 中显示图标」（仅 macOS 桌面端展示），默认开启。关闭后应用以 accessory 方式运行：Dock 不显示图标，应用只通过菜单栏图标进入。
- Windows 保持现有托盘行为（`closeToTrayOnWindows`），Linux 与 Web 不受影响，本次不新增 Linux 托盘。
- 菜单栏图标使用单色 template 图（跟随浅/深色菜单栏自动反色），来源于应用内统一的 folded-ribbon N 字形，不带薄荷绿配色。

## 状态所有者与接口

| 状态                | 所有者                                                                  | 说明                                                                                    |
| ------------------- | ----------------------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| `showDockIcon` 设置 | `setting.json`（`AppSettings.showDockIcon?: boolean`，未设置视为 true） | 设置页经 `settingService.update` 写盘，再 `platform.syncAppSettings` 通知主进程即时生效 |
| Dock 当前可见性     | Desktop main 的 `desktopDockVisibility` 控制器                          | 唯一调用 `app.dock.show()/hide()` 的地方；其它模块只向它请求「前台展示」                |
| 菜单栏图标实例      | Desktop main 的 `desktopTray`                                           | 与 Windows 托盘共用同一个模块与菜单模板                                                 |

依赖方向：`index.ts` 创建 tray，把「是否有可用菜单栏入口」交给 dock 控制器；dock 控制器不引用 tray 实现。

## 不变量与失败语义

1. **不丢入口**：只有在菜单栏图标创建成功时才允许隐藏 Dock；图标创建失败（资源缺失）时，Dock 保持显示，即使设置为关闭，并记录 warn 日志。
2. 任务通知点击原先无条件调用 `app.dock?.show()`，会绕过设置；改为经 dock 控制器的 `revealForForeground()`，设置关闭时不再显示 Dock。
3. 设置变更即时生效，无需重启；启动时在窗口创建前按设置应用一次（关闭时会有极短的 Dock 闪现，Electron 限制）。
4. `dock.hide()/show()` 是幂等的：控制器记住上次应用的状态，重复应用同一值不再调用原生 API。
5. 菜单栏图标的 click 与 right-click 互不干扰：macOS 上不调用 `setContextMenu`（否则左键也会弹菜单），右键用 `popUpContextMenu`。

## 验收

1. macOS 启动后菜单栏出现 N 图标，浅色与深色菜单栏下均清晰可辨。
2. 关闭所有窗口后，单击菜单栏图标重建并显示主窗口；窗口最小化时单击还原。
3. 右键菜单项可用，「退出」走显式退出路径。
4. 关闭「在 Dock 中显示图标」后 Dock 图标立即消失、窗口仍可用；打开后立即恢复；重启后保持。
5. Dock 隐藏时点击任务通知仍能前置窗口，但 Dock 图标不会重新出现。
6. 单测覆盖 dock 控制器（隐藏需有菜单栏入口、幂等、非 macOS 空操作、通知前台展示遵守设置）。
7. `pnpm typecheck`、`pnpm lint`、`pnpm architecture:check --changed` 通过。
