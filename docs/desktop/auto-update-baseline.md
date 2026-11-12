# 自动更新临时 Baseline（历史方案）

> 现状（2.0）：自动更新只走 GitHub Releases（`nex-harness/Nex`），见
> `docs/specs/desktop-updates-github.md`。以下内容仅记录历史方案，服务端 manifest 与
> `/update` 系列 generic feed 均不再被当前客户端读取。

## 历史：服务端 manifest

桌面端曾通过服务端 manifest 接口检查更新（`GET /api/v1/releases/electron/manifest`），
由服务端根据 `platform`、`device_mid` 和 `channel` 返回 stable 或 preview 版本；
CI 把版本化静态产物上传到 OSS：

```text
<path-prefix>/<version>/macos-arm64/
<path-prefix>/<version>/macos-x64/
<path-prefix>/<version>/windows-x64/
<path-prefix>/<version>/windows-arm64/
<path-prefix>/<version>/linux-x64/
<path-prefix>/<version>/linux-arm64/
```

版本化平台/架构目录内统一上传包含多语言日志的 `latest.yml`、安装包和 `.blockmap`。
electron-builder 固定配置 `detectUpdateChannel: false`，包括 prerelease 在内都生成统一的 `latest.yml`，
发布通道由服务端 manifest 决定。灰度与全量发布只通过 release API 的 `status=3` / `status=1` 控制。

## 历史：generic feed

更早的桌面端依赖 electron-updater generic feed。已安装的旧包会固定读取包内 `app-update.yml`
写入的 generic stable feed，例如 macOS arm64 的主 feed 是：

```text
https://${CDN_DOMAIN}/${OSS_PATH_PREFIX}/update/mac/arm64/latest-mac.yml
```

当时为了避免 `2.0.0` 旧包被直接推进到 `3.0.x`，曾使用 `/update-insider/<platform>/<arch>/`
作为临时 baseline feed，并通过发布阶段覆盖一个或多个 feed 来切流。该机制已从 CI 主链路移除。

## 当前

- 新客户端：electron-updater 内置 `github` provider，读取 `nex-harness/Nex` Release 资产中的
  `latest-mac.yml` / `latest.yml` / `latest-linux.yml`。
- stable / preview：由设置 `receivePreviewUpdates` 控制 `allowPrerelease`。
- 旧 generic feed 与 manifest 接口：客户端不再请求，仅保留历史说明。
