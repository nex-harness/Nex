# 桌面自动更新：GitHub Releases 单一来源

Status: accepted
Owner: Desktop main（`packages/desktop/src/main/autoUpdater.ts`）
Related: `docs/desktop/auto-update-baseline.md`、`docs/desktop/post-update-release-notes.md`、`packages/desktop/electron-builder.config.js`（`publish` 块）

## 背景

2.0 基线的自动更新由 ZCode 托管的 manifest 服务提供：main 通过自定义 `ManifestUpdateProvider` 请求
`https://zcode.z.ai/api/v1/releases/electron/manifest`，并在启动前请求
`https://zcode.z.ai/api/v1/client/configs` 判断是否强制升级（force-update gate）。

产品决定：**更新只走 GitHub Releases**，发布仓库为 `github.com/nex-harness/Nex`；
同时删除启动期强制升级 gate。远控 relay 与官方插件市场 / CDN 不在本规格范围内，保持不变。

## 产品规则

1. **GitHub Releases 是唯一更新来源。** main 使用 electron-updater 内置 `github` provider，
   `owner: "nex-harness"`、`repo: "Nex"`，与 `electron-builder.config.js` 的 `publish` 块保持一致。
   发布资产里的 `latest-mac.yml` / `latest.yml` / `latest-linux.yml` 及 `.blockmap` 由
   `.github/workflows/release.yaml` 在打 tag 时上传。
2. **通道（stable / preview）只由设置驱动。** stable = `allowPrerelease=false`，electron-updater
   用 `releases/latest` 解析 tag 后读取该 tag 的 `latest*.yml`；preview = `allowPrerelease=true`，
   provider 在没有显式 channel 时直接取 Atom feed 第一条 release（不校验 tag 是否为 semver），
   若该 tag 带 prerelease 段（如 `v3.18.0-preview.1`）则先读 `preview-mac.yml`，404 时回退
   `latest-mac.yml`。通道来源只有一个：`settings.receivePreviewUpdates`。
   注意：由于没有显式 channel，preview 也会被 `alpha-<sha>` 这类非 semver 的 server Release 影响，
   见「开放风险」。
3. **没有强制升级 gate。** 启动不再读取任何远端「最低版本」配置，也不会在主窗口创建前拦截启动。
   旧客户端只能靠正常更新流程升级。
4. **更新说明来自 GitHub Release body。** electron-updater 从发布资产里的 channel yml 取 `releaseNotes`；
   若 yml 未带说明，则取 GitHub Release body。main 把它转换为既有的
   `PostUpdateReleaseNotesPayload`（`version/title/markdown/releaseDate`），安装后弹窗与「更新按钮 hover」
   的渲染逻辑不变。`releaseName` 优先作为标题，否则取正文第一个一级标题，再退回 `Release v<version>`。
5. **打包应用不接受任何外部更新源。** `NEX_UPDATE_FEED_URL` / `--nex-update-feed-url` 属于服务端 manifest
   时代的开发态覆盖，随 manifest provider 一起删除；打包态始终使用上述 GitHub provider。
6. 日志、菜单文案、「检查更新」手动检查结果、跳过版本、自动下载/自动安装设置、更新状态机
   （idle / checking / update-available / download-progress / update-downloaded）全部保持现有语义。

## 状态所有者与接口

| 状态                                     | 所有者                                                   | 说明                                                                  |
| ---------------------------------------- | -------------------------------------------------------- | --------------------------------------------------------------------- |
| 当前更新状态 `menuState`                 | main `autoUpdater.ts`                                    | 经 `PlatformChannels.UpdateStateChanged` 广播，菜单与 renderer 只读   |
| 当前通道 `availableUpdateChannel`        | main `autoUpdater.ts`                                    | 由 `settings.receivePreviewUpdates` 推导，唯一写入口是检查启动与设置变更 |
| `receivePreviewUpdates`                  | `AppSettings`（设置页写入）                              | preview 开关；变更后 `refreshAutoUpdaterReleaseChannel` 重新检查       |
| `autoDownloadAndInstallUpdates`          | `AppSettings`                                            | 发现新版本后是否直接下载；main 独占判断                               |
| `skippedElectronUpdateVersions[channel]` | `AppSettings`                                            | 按通道记录跳过的版本；手动检查会清除当前通道记录                      |
| `pendingPostUpdateReleaseNotes`          | `AppSettings`                                            | 下载完成时写入，安装后首次启动展示一次，renderer ack 后清除            |
| `quitAndInstallState` / ready 更新       | main `autoUpdater.ts`                                    | 安装准入；安装中锁定目标，迟到结果不得替换                            |

数据流：

```
settings.receivePreviewUpdates ─┐
                                ├─> autoUpdater.allowPrerelease ─> GitHub Releases（atom feed）
app.startup / poll(15min,2min) ─┘        │
                                         └─> update-available ─> 跳过/自动下载/手动下载
                                                   └─> download-progress ─> update-downloaded
                                                              └─> pendingPostUpdateReleaseNotes
                                                              └─> 用户「重启以更新」/before-quit 安装
```

## 失败语义

1. **离线、GitHub 限流、仓库不可达、发布缺少 channel yml**：检查以 `error` 事件或 Promise reject 收口，
   状态回到 idle（或保留已下载的 ready），手动检查回传 `{ kind: "error", message }`；
   **绝不阻塞启动**，也不退出应用。轮询在下一周期重试。
2. 仓库为 `nex-harness/Nex` 公开仓库（当前为 Public），匿名请求即可读取 Releases；若未来改为私有，
   公开使用场景将全部无法检查更新（需要自带 token，当前未实现，见「开放风险」）。
3. 下载失败：回到 `update-available` 供用户重试，并立即复查；取消下载不算跳过版本。
4. 安装（quitAndInstall）失败：退出准备已开始则重启应用恢复 Host/Agent；未开始则回到 idle。
5. `update-downloaded` 后 macOS Squirrel 才报 staging 错误时，清除 ready 与其 pending 说明，
   避免 renderer 长期停留在「重启以更新」。

## 平台覆盖

- 只有随 GitHub Release 上传对应 channel yml 的平台才能收到更新。当前 `release.yaml` 只构建 macOS
  （`latest-mac.yml`）；`electron-builder.config.js` 虽声明 Windows / Linux 目标，但发布流水线不上传
  `latest.yml` / `latest-linux.yml`，因此 Windows / Linux 安装包的更新检查会得到「找不到 channel 文件」
  的错误且不更新，需先补齐 CI 才能启用。
- macOS 更新依赖 zip + `latest-mac.yml` + 代码签名 / 公证一致性；签名或公证失败的包无法被 Squirrel 接管。

## 发布约束（GitHub Latest 指针）

electron-updater 的 stable 检查在 github.com 上使用 `https://github.com/nex-harness/Nex/releases/latest`
解析 tag（`getLatestTagName`），然后读取该 tag 下的 `latest*.yml`。因此：

1. 最新桌面版 Release 必须带上当前平台 channel yml；只有 server 二进制的 alpha Release 若被 GitHub 标记为
   Latest，stable 检查会以 `ERR_UPDATER_CHANNEL_FILE_NOT_FOUND` 失败（不会回退到更早的桌面版 Release）。
2. `allowPrerelease=true` 且未显式设置 channel 时，provider 直接使用 Atom feed 第一条 entry，不校验
   semver，因此 `alpha-<sha>` 这类 server Release 同样会被选中并因缺少 channel yml 失败。
   发布流水线应让桌面 Release 成为 Latest，或把纯 server Release 标为 prerelease
   （非本仓库当前 CI 行为，属于已开放的发布侧风险）。

## 开放风险

- 私有仓库：GitHub 匿名读取要求仓库保持 Public；若改为私有，需要给客户端注入 token（未实现）。
- 桌面 Release 与 server Release 共用同一仓库的 Releases：`releases/latest` 指向纯 server 的 alpha Release 时，
  stable 检查会失败；preview 的 Atom feed 第一条同样可能是 alpha。发布流程必须保证桌面 Release 是 Latest，
  或给 server Release 标 prerelease。
- Windows / Linux channel yml 未由 CI 上传，这两个平台当前无法自动更新。
- GitHub 未认证 API 限流（每小时 60 次/IP）；provider 主要走 `releases.atom` 与 `releases/latest`，
  失败会按失败语义重试，不做退避以外的特殊处理。
- `homepage` / `maintainer` 仍为 `zcode.z.ai` 元数据，仅影响 Linux 包元信息，不影响更新源。

## 验收

1. 打包版（生产 flavor）启动后，`initAutoUpdater` 装配 `github` provider；日志出现
   `GitHub Releases update provider applied`，不再出现 manifest 服务相关请求。
2. 存在高于当前版本的非 prerelease Release（含当前平台 channel yml 与 blockmap）时：
   菜单显示「发现新版本」；自动下载开启时直接进入下载；关闭时由 renderer 弹窗提示。
   下载完成写入 `pendingPostUpdateReleaseNotes`，重启安装后首次启动弹出更新说明并可关闭。
3. 打开「接收 preview 更新」后重新检查：`allowPrerelease` 变为 true；关闭后变回 false；检查进行中切换时
   只记录待刷新通道，等当前检查收口后再重新请求，旧请求的结果不会被标成新通道。
4. 跳过某版本后不再提示；手动「检查更新」会清除该通道的跳过记录并重新显示。
5. 断网 / GitHub 不可达时：启动正常进入主界面，手动检查提示错误，轮询恢复后自动继续检查。
6. 任何 `settings.forceUpdate` / `/api/v1/client/configs` 最低版本配置都不再影响启动与更新对话。
