# Nex-v3 客户端技术栈与通信协议规范 (Client Tech Stack & Sync Protocol Spec)

- **状态**: Draft / Approved Core Direction
- **更新日期**: 2026-11-18
- **核心决策**: 采用 **`egoist/mygo` (v0.10.0+ Web Mode) + 单套响应式 React/TypeScript Web 前端** 统一交付 Web、Desktop (macOS / Windows / Linux) 与 Mobile (iOS / Android) 三端；全项目严控在 **Go + TypeScript** 双语言栈内（**零 Rust、桌面零 CGO**）。

---

## 1. 客户端技术选型核心原则

1. **语言栈极致收敛（Go + TypeScript Only）**：不引入 Rust（放弃 Tauri）、不引入 Dart（放弃 Flutter），后端、Runner 与跨平台宿主壳全部使用纯 Go，视图层统一使用 TypeScript + React。
2. **桌面端零 CGO 交叉编译**：依托 `egoist/mygo` 纯 Go 无 CGO 的桌面宿主能力，与 `nex-runner`（`modernc.org/sqlite`）保持完全一致的跨平台构建体验。
3. **一套 UI 代码三端交付（Single Web UI Codebase）**：
   - Coding Agent 强依赖复杂流式 Markdown、代码高亮、Diff 对比、ANSI 终端输出渲染以及跨段落自然文本复制，Web DOM 生态在此领域具备压倒性优势。
   - 同一套响应式 React 前端编译产物，同时驱动：**`nex-server` 内嵌浏览器端**、**`mygo` 桌面客户端**、**`mygo` 移动客户端**。
4. **平台原生能力按需桥接**：通过运行时环境探测（Pure Browser vs `mygo` Host），在 Desktop 和 Mobile 容器内通过 `mygo` 的 **Typed IPC (`mygo.Bind`)** 无缝增强系统托盘、全局快捷键、原生推送通知（APNs / FCM 审批提醒）、Keychain 密钥存储与触觉反馈。

---

## 2. 三端交付形态与工程结构

### 2.1 三端运行形态对比

| 交付端 | 宿主容器 | 前端产物来源 | 平台专属增强能力 |
| :--- | :--- | :--- | :--- |
| **Web Client** | 现代浏览器（由 `nex-server` 通过 `//go:embed` 直接托管静态资源） | `clients/web` 构建产物 | 开箱即用零安装、URL 深链接直达指定 Session、Web Notification API。 |
| **Desktop Client** (macOS / Windows / Linux) | **`egoist/mygo` (Web Mode)**（系统原生 WKWebView / WebView2 / WebKitGTK，无内置 Chromium 膨胀） | 同一套 `clients/web` 产物 | 系统托盘常驻、全局呼出快捷键、原生系统通知、本地文件/目录选择器、**可选进程内嵌本机 `nex-runner`（开箱即单机可用）**。 |
| **Mobile Client** (iOS / Android) | **`egoist/mygo` (v0.10.0+ Mobile Web Mode)**（打包 `.ipa` / `.apk` / `.aab`） | 同一套 `clients/web` 产物（移动端响应式布局适配） | 锁屏/后台原生推送通知（工具权限审批即时提醒）、Keychain/Keystore 安全存储 Server Token、生物识别解锁、触觉反馈（Haptics）。 |

### 2.2 客户端目录规划

```text
nex-v3/
├── clients/
│   ├── web/                      # 唯一的前端工程 (React + TypeScript + Vite + Tailwind)
│   │   ├── src/
│   │   │   ├── core/             # 纯 TS 状态与通信内核（WS 重连、seq 增量合并、REST 客户端）
│   │   │   ├── host/             # 宿主适配抽象层（自动识别 Browser 环境 vs mygo Typed IPC）
│   │   │   ├── features/         # 业务模块（Chat 流式面板、Diff/Tool 卡片、Runner/MCP/Skills/Cron 管理）
│   │   │   └── layouts/          # 响应式骨架（Desktop 多栏工作台 vs Mobile 单栈抽屉/Sheet 导航）
│   │   └── package.json
│   └── app/                      # mygo 跨平台宿主壳工程 (纯 Go，同时编译 Desktop 与 iOS/Android)
│       ├── main.go               # mygo 窗口初始化、托盘/快捷键注册、移动端插件挂载、mygo.Bind IPC 导出
│       ├── bridge.go             # 向前端暴露的原生能力（Keychain、PushToken、原生通知、可选内置 Runner 启停）
│       └── embed.go              # 嵌入 clients/web/dist 静态资产
```

### 2.3 Web UI 核心技术组件栈

| 模块职责 | 选型方案 | 说明 |
| :--- | :--- | :--- |
| **UI 框架与构建** | **React 19 + TypeScript + Vite + Bun** | 与 `mygo` CLI 默认的 Bun 前端开发工作流天然契合，极速热更新。 |
| **样式与响应式组件** | **Tailwind CSS v4 + shadcn/ui (Radix Primitives)** | 桌面端采用多面板分栏布局，移动端自动切换底部输入栏、手势抽屉（Drawer/Sheet）与大触控热区。 |
| **状态管理** | **Zustand** | 轻量无样板代码，非常适合高频流式 `message_delta` 局部订阅更新，避免整页重渲染掉帧。 |
| **代码高亮与 Diff** | **Shiki + Diff 视图组件** | 精确渲染模型输出的多语言代码块、右侧边栏文件预览 Tab 与 `Review` Tab 的 Unified/Split Diff 对比。 |
| **远程交互终端渲染** | **`libghostty-vt` WASM (`coder/ghostty-web`)** | 基于 Ghostty 官方 Zig 核心编译的 `~400KB` WASM 引擎（摒弃传统 JS 模拟的 `xterm.js`），零 CGO 通吃 Web / `mygo` 三端。 |

---

## 3. 平台桥接抽象层 (`HostBridge`)

为了保证同一套 React 代码在普通浏览器和 `mygo` 容器中零报错运行，前端通过统一的 `HostBridge` 接口隔离宿主差异：

```ts
export interface HostBridge {
  readonly kind: 'browser' | 'mygo-desktop' | 'mygo-mobile';

  // 凭证存储：mygo 下走系统 Keychain/Keystore，浏览器下走受控 Storage / HttpOnly Cookie
  getAuthToken(): Promise<string | null>;
  setAuthToken(token: string): Promise<void>;

  // 通知与推送：工具审批请求到达时唤醒用户
  notifyApprovalRequested(payload: { sessionId: string; runnerName: string; toolName: string }): Promise<void>;
  getMobilePushToken?(): Promise<string | null>;

  // 触觉反馈（移动端审批点击、发送消息、任务完成反馈）
  haptic?(style: 'light' | 'medium' | 'error'): void;
}
```

在 `clients/app`（Go 侧），通过 `mygo.Bind` 暴露强类型方法并自动生成前端 TypeScript 调用客户端，前端无需手写脆弱的字符串事件名。

---

## 4. 全链路通信协议选型 (`Client <-> Server <-> Runner`)

考虑到服务端与 Runner 为 Go、前端为 TypeScript Web 环境，且核心流量为**双向异步事件流 + 控制指令**，全链路统一采用 **「HTTPS REST JSON + 单连接多路复用 WebSocket (`coder/websocket`)」**，不引入 Protobuf/gRPC-Web 代理复杂度。

### 4.1 职责切分：什么走 REST，什么走 WebSocket？

1. **HTTPS REST API (`/api/v1/*`) —— 无状态 CRUD 与资产传输**
   - 用户登录鉴权、设备推送 Token 注册（APNs/FCM）。
   - Runner 列表查询、配对码（Pairing Token）生成、节点重命名与移除。
   - Skills 仓库增删改查与压缩包上传下载（Runner 按内容 SHA-256 拉取 Skill 包）。
   - MCP 配置清单、Cron 定时任务规则、全局权限策略的增删改查。
   - 历史会话分页列表与归档查询。

2. **WebSocket (`/ws/client` 与 `/ws/runner`) —— 实时事件流、控制信令与双向 RPC**
   - **Client <-> Server (`/ws/client`)**：
     - 订阅一个或多个活跃 Session（携带 `last_seen_seq` 瞬间补齐断网期间漏掉的流式帧）。
     - 发送对话消息（`prompt`）、运行中纠偏插话（`steer`）、追加指令（`follow_up`）、紧急中止运行（`abort`）。
     - 实时接收流式文本增量、思考块增量、工具执行状态、Runner 在线状态变更。
     - 接收并响应工具权限审批（`approval.request` -> `approval.resolve`）。
   - **Runner <-> Server (`/ws/runner`)**：
     - Runner 启动后主动发起出站 WSS 长连接，完成节点鉴权与能力上报（OS、Arch、Shell、本地已同步配置版本号）。
     - **上行（Runner -> Server）**：实时流式上报 Session 事件帧（带 Runner 本地持久化的单调递增 `seq`），或在断网恢复后批量补推 Outbox 离线事件；发起远程「中心态 MCP」工具调用请求。
     - **下行（Server -> Runner）**：转发客户端的对话/插话/中止/审批指令；推送 Skills / MCP / Cron 配置变更通知（触发 Runner 增量同步快照并热重载调度器）。

### 4.2 统一 WebSocket 信封格式 (JSON Envelope)

两端 WebSocket 均采用轻量自描述的 JSON 帧结构，便于抓包排查与跨版本兼容：

```json
{
  "v": 1,
  "id": "msg_01jd8x...",
  "type": "session.event",
  "ts": 1763452800123,
  "session_id": "sess_01jd8w...",
  "runner_id": "run_macbook_pro",
  "seq": 108,
  "payload": {
    "event": "message_update",
    "delta_kind": "text",
    "delta": "正在检查配置文件..."
  }
}
```

### 4.3 移动端弱网与后台唤醒特别设计

手机客户端存在频繁切后台、锁屏断连的特点，通过以下机制保障体验闭环：
1. **秒级无感续传**：App 从后台切回前台时，`core` 立即重连 `/ws/client` 并发送当前打开会话的 `{ session_id, last_seen_seq }`，Server 从持久化事件表补发差量帧，UI 无缝拼接后续打字机输出。
2. **离线审批推送唤醒**：当 Runner 处于 `interactive` 模式触发高危工具审批，而 Server 检测到用户当前无活跃前台连接（或超过 3 秒未响应）时，Server 通过推送通道向 `mygo` 手机端下发系统级通知，用户点击通知直接唤起 App 定位到该审批卡片一键放行或拒绝。

---

## 5. Desktop 客户端的「单机一体化模式」（可选红利）

由于 `clients/app`（`mygo` 宿主壳）本身就是纯 Go 程序，Nex-v3 在桌面端天然拥有一个架构红利：
- **远程控制模式（默认）**：Desktop 客户端仅作为视图层连接远端部署的 `nex-server`，统一操控云端 Linux、办公室 Windows 与家里的设备。
- **本地内嵌 Runner 模式**：用户在一台新 Mac 或 Windows 上安装了 Nex Desktop 客户端后，客户端 Go 宿主可以直接在后台协程或子服务中拉起本机 `nex-runner` 并自动配对连入指定 `nex-server`——用户无需在自己主力电脑上敲命令行单独装一遍 Runner。
