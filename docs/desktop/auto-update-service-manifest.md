# 桌面端自动更新：GitHub Releases

> 本文件原名「桌面端服务端 Manifest 自动更新」。2.0 起更新只走 GitHub Releases，
> 服务端 manifest 接口 `/api/v1/releases/electron/manifest` 与强制升级 gate 已删除。
> 产品规则、状态所有者与验收见 `docs/specs/desktop-updates-github.md`。

## 发布来源

- 发布仓库：`github.com/nex-harness/Nex`（Public）。`packages/desktop/electron-builder.config.js` 的
  `publish` 块与客户端 `autoUpdater.setFeedURL({ provider: "github", owner: "nex-harness", repo: "Nex" })`
  使用同一坐标。
- 客户端读取 `https://github.com/nex-harness/Nex/releases/download/<tag>/latest-mac.yml`（Windows 为
  `latest.yml`，Linux 为 `latest-linux.yml`）及同目录 `.blockmap`。GitHub provider 固定
  `isUseMultipleRangeRequest = false`（GitHub 资产由 S3 提供），与 electron-builder 的 publish 配置一致。
- 发布流水线 `.github/workflows/release.yaml` 打 `v*` tag 时由 electron-builder `--publish always`
  上传安装包、blockmap 与 channel yml。

## 通道

| 设置 `receivePreviewUpdates` | `autoUpdater.allowPrerelease` | 行为                                                                 |
| ---------------------------- | ----------------------------- | -------------------------------------------------------------------- |
| false                        | false                         | 只匹配非 prerelease Release；读取 `latest*.yml`                      |
| true                         | true                          | 匹配 prerelease tag（如 `v3.18.0-preview.1` → `preview-mac.yml`），缺失时回退 `latest*.yml` |

通道值仍由 main 推导为 `stable | preview` 并进入 `UpdateStatePayload.channel`，用于按通道记录跳过版本。

## Windows 更新信任边界

更新授权依赖 GitHub Release 上的 channel yml 哈希与安装包的 Authenticode 签名。
`packages/desktop/electron-builder.config.js` 保留包内 `publisherName`，只用于激活
`autoUpdater.verifyUpdateCodeSignature` 自定义校验，并通过 `verifyWindowsAuthenticodeSignature`
检查实际待安装文件；不按历史发布者 DN 限制新签名者，也不需要用户预装换证过渡版。
