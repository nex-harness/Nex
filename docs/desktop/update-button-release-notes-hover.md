# Update Button Release Notes Hover

## 背景

桌面端自动更新下载完成后，左上角更新按钮只展示“可安装更新”的入口。发布链路已经把单版本 changelog 注入到 update feed 的 `releaseNotes` 中，但 renderer 当前不会在按钮 hover 时展示这份内容。

## 目标

- 更新按钮 hover 时展示当前正在下载或可安装版本的更新日志。
- 展示内容跟随界面语言：`zh-CN` 优先展示中文 changelog，`en-US` 优先展示英文 changelog。
- 不改变自动检查、下载、安装更新的主流程。
- 不在 hover 时新增网络请求，避免 update feed/CDN 状态影响 UI 响应。

## 设计

electron-updater 从 GitHub Release 的 channel YAML（`releaseName` / `releaseNotes`）或 Release body 读取说明。
main 进程在 `update-available` 时把 release notes 规范化为 `PostUpdateReleaseNotesPayload`（`title` / `markdown` / 可选 `releaseDate`），
并随 `update-available` / `download-progress` 状态广播给 renderer；下载完成后继续随 `update-downloaded` 状态广播，
并持久化给安装后弹窗使用。旧的 `onUpdateReady(version)` 事件只保留兼容用途，按钮展示详细内容以
`UpdateStatePayload.releaseNotes` 为准。GitHub provider 不提供多语言字段，`releaseNotesByLocale` 保持缺省，
UI 按当前语言回退到默认 `markdown`。

UI 层复用现有 tooltip 与 Markdown 渲染能力：没有 release notes 时保持原来的短 tooltip；有 release notes 时，hover 下载中按钮或更新按钮展示一个紧凑的浮层，标题固定显示当前语言下的 `v{version} 更新日志` / `v{version} Release Notes`，标题下方仅显示按当前语言格式化后的 feed `releaseDate` 日期，不额外加“更新日期”或 “Release date” 前缀，正文渲染对应 Markdown。若当前语言没有对应内容，则回退到 payload 的默认 Markdown。按钮可见短文案、aria label、确认弹窗与下载中提示都走 i18n。

## 验证

- 自动更新 main 进程测试覆盖 `update-available`、`download-progress`、`update-downloaded` 状态携带 release notes。
- UI 单测覆盖 hover tooltip 在缺少 `releaseNotesByLocale` 时回退到默认 Markdown。
- 真实 GitHub Release 的 release notes 内容解析只在打包环境手工验证（CI 目前只发布 macOS）。
