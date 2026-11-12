# 桌面端开发态自动更新验证

## 目的

开发态默认不初始化自动更新，避免日常联调误触发下载、安装和重启流程。需要验证自动更新交互时，
可以显式开启开发态自动更新，让 dev app 走与打包版相同的 GitHub Releases provider。

## 启动方式

```bash
NEX_AUTO_UPDATE_DEV=1 \
NEX_AUTO_UPDATE_DEV_VERSION=3.17.2 \
pnpm dev:desktop:test
```

- `NEX_AUTO_UPDATE_DEV=1` 只在开发态显式开启自动更新；未设置时保持开发态不检查更新。
- `NEX_AUTO_UPDATE_DEV_VERSION` 用于模拟当前客户端版本；例如设为 `3.17.2` 时，
  比它高的 GitHub Release 会被判定为可用更新。
- 通道由设置页「接收 preview 更新」控制：打开后 `autoUpdater.allowPrerelease = true`，
  可匹配 prerelease tag；关闭后只匹配稳定 Release。

## 行为边界

- 生产包与开发态使用同一个 provider 坐标（`nex-harness/Nex`）；打包应用没有、也不接受自定义更新源参数。
  服务端 manifest 时代的 `NEX_UPDATE_FEED_URL` / `--nex-update-feed-url` 已删除。
- 开发态会设置 `electron-updater` 的 `forceDevUpdateConfig`，并读取
  `packages/desktop/dev-app-update.yml` 仅用于初始化下载缓存目录；真实检查与下载地址仍来自 GitHub Releases。
- 开发态点击「重启以更新」只重启当前 dev app，用于验证 UI 状态闭环，不尝试让未打包应用接管真实安装器。
- 更新状态机、跳过版本、自动下载、release notes 弹窗的回归验证见
  `docs/specs/desktop-updates-github.md` 的验收条目；真实 GitHub Release 端到端下载/安装只能在打包环境执行。
