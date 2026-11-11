# Web 任务通知

## 现状与问题

任务完成、失败、需要确认时，UI 层（`useTaskNotifications`）统一调用 `platform.showTaskNotification`。Desktop 由 main 展示系统通知；Web 已有实现，但实际上一直不会弹出：

1. 只在 `Notification.permission === "granted"` 时展示，而全仓没有任何地方请求权限，默认值 `default` 下所有通知被静默丢弃。
2. `onTaskNotificationClick` 是空实现，点击通知不会回到页面，更不会切到对应任务。
3. 设置页文案写的是「桌面通知」，没有任何入口说明浏览器还需要授权。

## 产品规则

- Web 端沿用现有总开关「任务通知」和子开关「通知声音」（localStorage），不新增开关。
- 浏览器通知权限是独立于总开关的第二道门：总开关开着但权限不是 `granted` 时不会弹出。设置页在总开关下方展示权限行：
  - `default`：说明文案 + 「允许浏览器通知」按钮（用户点击即手势内调用 `Notification.requestPermission()`）。
  - `denied`：提示已被浏览器拦截，需要在浏览器站点设置中放开；不展示按钮。
  - `unsupported`（无 `Notification` API，例如非 HTTPS 页面或部分移动浏览器）：提示当前环境不支持。
  - `granted`：整行不展示。
- 展示条件不变：页面当前有焦点（`document.hasFocus()`）时不弹，避免打扰正在看的用户；通知声音只在通知真正弹出后播放。
- 同一任务的同类通知用 `tag` 合并（`taskId` + `status` + 可选 `requestId`），重复事件不会堆叠。
- 点击通知：关闭该通知、`window.focus()` 把标签页带到前台，并向已订阅的 `onTaskNotificationClick` 处理器投递 `taskId`；Root 已有的处理器负责激活对应 workspace tab 与任务。
- Desktop 与手机远控行为不变。

## 状态所有者与接口

| 状态 / 行为            | 所有者                                                                                                            |
| ---------------------- | ----------------------------------------------------------------------------------------------------------------- |
| 是否发通知意图         | `useTaskNotifications`（沿用，不改）                                                                              |
| 总开关、声音开关       | `useNexStore`（沿用）                                                                                             |
| 浏览器权限、展示、点击 | `packages/web/src/webTaskNotifier.ts`（新增，唯一所有者）                                                         |
| 权限读取 / 请求入口    | `IPlatformService` 新增可选 `getTaskNotificationPermission` / `requestTaskNotificationPermission`；Desktop 不实现 |

```text
useTaskNotifications ──showTaskNotification──▶ webTaskNotifier ──▶ Notification
                                                  ▲        │ click
settings 权限行 ──request/getPermission───────────┘        ▼
                                          onTaskNotificationClick 处理器 ──▶ Root 切换任务
```

依赖方向：`web` → `shared`/`ui` 公共入口；`ui` 只依赖 `IPlatformService`，不直接访问 `window.Notification`。

## 失败语义

- `new Notification` 抛错（如 Android Chrome 要求 Service Worker）：吞掉并记录 `warn`，不影响主流程，也不播放声音。
- `requestPermission` 抛错或不可用：返回 `unsupported`/当前值，不抛到 UI。
- 处理器抛错互不影响，逐个隔离。
- 本期不做 Service Worker 通知（后台标签页被冻结、移动端浏览器的完整支持），在 PR 中说明。

## 验收

1. 单测覆盖：无焦点 + 已授权时展示且播放声音；有焦点、未授权、无 API 时不展示；`tag` 合并；点击聚焦并投递 `taskId`、处理器可注销、处理器抛错隔离；`requestPermission` 结果映射；构造通知抛错时不播放声音。
2. 设置页权限行在 `default`/`denied`/`unsupported` 下分别展示对应内容，`granted` 与 Desktop 下不展示。
3. 浏览器实测：授权后切到其它标签页，任务完成弹出通知，点击回到对应任务。
4. `pnpm typecheck`、`pnpm lint`、`pnpm architecture:check --changed` 通过。
