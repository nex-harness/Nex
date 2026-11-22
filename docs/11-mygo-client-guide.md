# Nex-v3 `egoist/mygo` 全平台客户端指南（Linux / macOS / Windows / iOS / Android）

- **框架版本**: `github.com/egoist/mygo` `v0.10.0` + `mygo-runtime` `0.10.0`
- **前端工程**: `clients/web`（唯一 React/TypeScript UI，嵌入式通过 `pkg/webui` 单份打包）
- **配置入口**: `mygo.json`（项目根目录）

---

## 1. 架构与编译隔离设计

| 平台 | 编译标签 | 渲染引擎 | 连接模式 | CGO |
| :--- | :--- | :--- | :--- | :--- |
| **macOS** | `!ios && !android` | `WKWebView` | 本机一体化（默认）/ 远程 Server | `CGO_ENABLED=0` |
| **Linux** | `!ios && !android` | `WebKitGTK 4.1` | 本机一体化（默认）/ 远程 Server | `CGO_ENABLED=0` |
| **Windows** | `!ios && !android` | `WebView2` | 本机一体化（默认）/ 远程 Server | `CGO_ENABLED=0` |
| **iOS** | `ios \|\| android` | `WKWebView` | 远程 Server（零内嵌 Runner/SQLite） | `CGO_ENABLED=1`（Xcode clang） |
| **Android** | `ios \|\| android` | `Android WebView` | 远程 Server（零内嵌 Runner/SQLite） | `CGO_ENABLED=1`（Android NDK） |

### 核心文件分工

- [mygo.json](../mygo.json)：全平台应用元信息（`com.nex.app`、Deep Link `nex://`、五端图标与启动画面配置）。
- [host.go](../clients/app/host.go)：全平台共享 `HostService`（绑定名 `NexHost`），负责服务器 URL 管理、历史记录、Keychain/Keystore token 读写（含 `0600` 本地加密回退）、原生通知、触觉反馈、目录选择与 Deep Link 解析（`nex://session/<id>`、`nex://connect?url=...`、`nex://settings/<section>`）。
- [local_desktop.go](../clients/app/local_desktop.go) / [shell_desktop.go](../clients/app/shell_desktop.go)：桌面端专属单机一体化启动器（`AllInOneInstance`）、单实例锁、原生菜单、托盘图标与全局快捷键（`CmdOrCtrl+Shift+Space`）。
- [local_mobile.go](../clients/app/local_mobile.go) / [shell_mobile.go](../clients/app/shell_mobile.go)：移动端轻量容器与状态栏/前后台恢复同步，彻底隔离 `pkg/server`、`pkg/runner` 与 `modernc.org/sqlite`。
- [main_desktop.go](../cmd/nex-app/main_desktop.go) / [main_mobile.go](../cmd/nex-app/main_mobile.go)：注入 `mygo.SetFrontend(webui.DistFS())`；桌面端保留 `-open=false` 无头守护进程兼容模式。
- [bridge.ts](../clients/web/src/host/bridge.ts) & [mygo.gen.ts](../clients/web/src/host/mygo.gen.ts)：真实 `window.mygo` 运行时桥接 + `mygo generate` 自动生成强类型客户端。
- [ServerConnectGate.tsx](../clients/web/src/features/ServerConnectGate.tsx)：桌面/移动端服务端连接与模式切换页（桌面端可一键切换「远程服务端」或「本机一体化模式」）。

---

## 2. 开发与打包命令

### 生成 TypeScript Typed IPC 客户端

```bash
make client-generate
```

### 本地开发热重载

```bash
make client-dev          # 启动桌面客户端并连接 Vite dev server
make client-dev-ios      # 编译并运行至已启动的 iOS Simulator
make client-dev-android  # 编译并部署至 adb 连接的 Android 设备/模拟器
```

### 生产打包（输出至 `dist/clients/`）

```bash
./scripts/build-clients.sh desktop   # macOS (.app + .dmg), Linux (.tar.gz/.deb/.rpm/PKGBUILD), Windows (.exe)
./scripts/build-clients.sh ios-sim   # iOS Simulator .app（仅 macOS + Xcode）
./scripts/build-clients.sh ios       # iOS 签名 .ipa（需配置 ios.team / provisioningProfile）
./scripts/build-clients.sh android   # Android .apk 与 .aab（需 Android SDK + NDK + JDK）
./scripts/build-clients.sh all       # 依次打包 Desktop + iOS Simulator (macOS) + Android
```

### Linux 运行时系统依赖

Debian / Ubuntu 桌面运行基于系统 WebKitGTK，打包或运行前需确保已安装 GTK 3 与 WebKitGTK 4.1：

```bash
sudo apt install libwebkit2gtk-4.1-0
```

### 无头兼容模式

桌面端二进制依旧保留原有服务端+Runner 一体化后台启动能力：

```bash
./bin/nex-app -open=false -addr 127.0.0.1:18899 -workspace /path/to/repo
```
