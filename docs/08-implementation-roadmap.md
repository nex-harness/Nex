# Nex-v3 工程实施路线图 (Implementation Roadmap)

- **状态**: Approved Execution Plan
- **更新日期**: 2026-11-18
- **推进策略**: 拒绝“连写两个月底层却跑不通一个界面”的瀑布模式；采用**垂直切片（Vertical Slice）迭代**——每个阶段（Milestone）结束都必须产出一个**端到端可编译、可运行、可交互验证**的闭环系统，且从第一天起在 CI/构建脚本中强制执行 **`CGO_ENABLED=0` 跨平台三端（macOS / Linux / Windows）静态编译门禁**。

---

## 阶段总览

| 里程碑 | 核心主题 | 关键里程碑验收标志 (Exit Criteria) |
| :--- | :--- | :--- |
| **M1** | **Go 智能体最小自治内核** | 单二进制 `nex-runner` 本地独立跑通多模型流式对话、5 大基础工具、SQLite + CAS 存储与 6 维上下文拆解计算。 |
| **M2** | **Server 贯通 + Web 对话闭环** | 浏览器打开 `nex-server` 连上远程 `nex-runner` 流畅对话、远程审批工具、断网重连不丢字、查看 `Context windows` 浮层与按需 Trace。 |
| **M3** | **`libghostty` 远程终端与多 Tab 工作台** | 左下/右侧打开 `libghostty` WASM 终端丝滑跑 `vim`/`htop`，右侧多 Tab 浏览远程文件树、新 Tab 预览源码、点开 `Review` 看 Git Diff。 |
| **M4** | **扩展生态（MCP Code Mode + 插件 + Subagent）** | 跑通 `goja` 沙箱 `codemode` 懒加载调本地/中心态 MCP；Server `esbuild` 下发 TS 插件并弹出远程 `ctx.ui`；支持 `max_depth=1` 并发子智能体。 |
| **M5** | **离线自治 Cron + 归档与成本统计** | Runner 断网准点跑完 Cron 并在重连后补传结果；对话冷热分离 Archive/Unarchive；Runner 级 Today / 7D / 30D 费用统计直出。 |
| **M6** | **`mygo` 桌面端与移动端全平台交付** | 基于 `egoist/mygo` 打包出 macOS/Win/Linux 轻量桌面客户端（含可选内置 Runner）与 iOS/Android 移动客户端（含锁屏审批推送）。 |

---

## M1：Go 智能体最小自治内核 (`pkg/ai` + `pkg/agent` + `pkg/store`)

**目标**：不依赖 Server、不写前端，先把对标 `pi-agent` 的纯 Go 心脏造出来，并第一天锁死跨平台零 CGO 约束。

1. **工程底座与零 CGO 门禁**：
   - 初始化 Go Module (`module github.com/nex-harness/nex`，当前临时 Git 仓库 `https://github.com/nex-harness/next`) 与目录结构（`pkg/ai`, `pkg/agent`, `pkg/harness`, `pkg/store`, `pkg/protocol`, `cmd/nex-runner`, `cmd/nex-server`）。
   - 编写一键交叉编译校验脚本（强制 `CGO_ENABLED=0` 同时构建 `darwin/arm64`, `linux/amd64`, `windows/amd64`），确保后续引入任何第三方库一旦破环无 CGO 立即暴露。
2. **多模型流式层 (`pkg/ai`)**：
   - 定义统一 `Message` / `ContentBlock`（Text, Thinking + Signature / EncryptedReasoning, Image, ToolCall）与流式 `StreamEvent`。
   - **一期聚焦实现两大主力协议适配器**：
     1. **Codex / OpenAI Responses API (`openai_responses`, `/v1/responses`)**
     2. **Anthropic Messages API (`anthropic`, `/v1/messages`)**
   - 内嵌 `models.dev` 离线种子快照，实现上下文上限查询与单次调用成本/缓存节省精确计算器。
3. **Agent Loop 与跨平台基础工具 (`pkg/agent` & `pkg/harness/tools`)**：
   - 实现 `Agent Loop` 状态机（支持运行中 `Steer` 插话打断剩余工具、结束后 `FollowUp` 追加队列、以及 `context.Context` 全链路取消）。
   - 实现 5 个核心工具：`read`、`write`、`edit`（兼容 LF/CRLF 与缩进容错并输出 Unified Diff）、`bash`（Unix 进程组清理 + Windows `Job Object` 子进程树销毁）、纯 Go `grep` & `glob`（内置 `.gitignore` 过滤）。
4. **本地存储与 6 维上下文计量 (`pkg/store`)**：
   - 集成 `modernc.org/sqlite`（WAL 模式）与 SHA-256 Zstd 压缩 `blobs/` 仓库。
   - 在请求拼装层实现 6 维 `Context windows` 统计（`messages`, `system_tools`, `system_prompt`, `mcp_tools`, `skills`, `meta_context`）及 `blob_hash` 落盘。

---

## M2：中心控制面 `nex-server` + 最小可用 Web 对话闭环

**目标**：打通 `Browser <-> nex-server <-> nex-runner` 全链路事件流，做出第一版能日常使用的远程 Coding Agent 界面。

1. **双向长连接与同步协议 (`pkg/protocol` & `cmd/nex-server`)**：
   - 基于 `coder/websocket` 实现 `/ws/runner`（Runner 反向注册鉴权、心跳、Outbox `seq` 增量上报与断网重连 `ack_seq` 补推）与 `/ws/client`（多端会话订阅、`last_seen_seq` 秒级续传）。
   - 实现可选的 Server `/llm-proxy/*` 流式网关转发与 REST `/api/v1/*` 管理接口（供应商/模型配置、Runner 列表、按 `blob_hash` 按需拉取 Trace 原文）。
2. **前端工程核心骨架 (`clients/web`)**：
   - 搭建 React 19 + TypeScript + Vite + Bun + Tailwind v4 + Zustand 工程，并通过 `//go:embed` 将构建产物内嵌进 `nex-server` 单二进制。
   - 实现核心对话界面：选择目标 Runner 与工作区、切换模型与 Thinking 档位、流式 Markdown + Shiki 代码高亮、Thinking 折叠块、工具执行实时状态卡片、`Steer` 运行中插话与一键中止。
   - 实现 `interactive` 模式下的**远程工具权限审批弹窗**。
   - 实现输入框底栏的 **`Context windows` 圆环与 6 维占比浮层**，以及点击 `Show system prompt` / Trace 抽屉按需懒加载原文。

---

## M3：`libghostty` 远程终端与右侧多标签工作台

**目标**：补齐完整远程开发体验——在界面里直接敲远端命令、浏览远端目录树、新 Tab 预览代码、审查 Git 改动。

1. **Runner 跨平台零 CGO PTY 与二进制通道**：
   - Unix 接入 `creack/pty`，Windows 接入纯 Go Win32 `ConPTY` (`CreatePseudoConsole`)，支持动态 `resize(cols, rows)` 与 `128KB` 内存环形缓冲断线回放。
   - 在 WebSocket 上启用 Binary Frame 二进制快车道直传终端字节流。
2. **前端集成 `libghostty-vt` WASM**：
   - 引入 `coder/ghostty-web` 封装多实例终端组件，联动全局字体与主题配置，支持停靠在左下角或右侧边栏 Tab。
3. **右侧多态多标签侧边栏 (`WorkbenchDock`)**：
   - 实现 Runner 工作区 RPC：`workspace.fs_list`（按层懒加载目录树）、`workspace.fs_search`（文件名快搜）、`workspace.fs_read_preview`（二进制嗅探保护 + 文本/图片读取）、`workspace.git_*`（实时增删行数与文件 Diff）。
   - 前端实现多 Tab 管理：常驻/新建 `Files` 树、点击文件自动在新 Tab 打开 Shiki 语法高亮预览（支持选行插入对话 `@file:L10-20`）、主对话右上角 `[± 更改 +X -Y]` 胶囊一键联动 `Review` Tab（Unified / Split Git Diff）以及 `Side conversation` 支线问答标签页。

---

## M4：完整扩展生态（MCP Code Mode + TS 插件 + Skills + Subagent）

**目标**：对齐并超越 `pi-agent` 的可编程扩展与并发生产力。

1. **MCP 双层懒加载与 `goja` Code Mode (`pkg/harness/codemode`)**：
   - 接入 `modelcontextprotocol/go-sdk`，实现本地 `stdio` MCP 元数据快照缓存、首次调用按需唤醒、空闲 5 分钟进程树自动回收，以及 Server 中心态 MCP 远程路由代理。
   - 基于 `dop251/goja` 构建零系统权限瞬态沙箱，注入 `searchTools`、`describeTool` 与动态 `tools.<ns>.<tool>` 并发代理（绑定 `vm.Interrupt` 超时熔断，且严格穿透权限守卫不绕过审批）。
2. **双层插件系统与远程 `ctx.ui` (`pkg/extension`)**：
   - `nex-server` 内嵌纯 Go `evanw/esbuild` 库将上传的 `.ts` 插件实时转译为 `.js` Bundle 并按 Hash 下发 Runner 本地缓存。
   - `nex-runner` 实现 Session 级 `goja` 插件宿主（支持 `tool_call` / `tool_result` / `context` 拦截、注册 `/slash` 命令同步到前端补全列表），并打通跨网络的 `ctx.ui.select / confirm / input / setStatus / setWidget` 远程交互控件。
3. **Skills 资产同步与同机并发 Subagent**：
   - Server 管理 Skills 目录（含可执行脚本）并按 Hash 同步落盘至 `~/.nex-runner/skills/`。
   - 实现 `SessionManager` 同机多协程并发 Subagent（硬锁定 `max_depth = 1` 物理剥离套娃派生工具、预留 `target_runner_id`、支持前台并行扇出与后台 `run_in_background` + `send_message` 邮箱、只读角色与 `git worktree` 写隔离、UI 父流摘要防刷屏 + 子流按需点开订阅）。

---

## M5：离线自治 Cron、对话冷热归档与多维统计面板

**目标**：完善长期无人值守稳定性、数据生命周期治理与成本可观测性。

1. **内置 Cron 调度与系统服务化**：
   - `nex-runner` 集成 `robfig/cron/v3`，Server 配置定时任务同步至本地 SQLite 快照；验证完全断网状态下准点唤醒执行、绑定 `autonomous-policy` 非交互权限防挂死、恢复网络后 Outbox 自动补传完整回放。
   - 集成 `kardianos/service`，支持 `nex-runner service install/start/stop` 一键注册为 macOS `launchd`、Linux `systemd`、Windows Service。
2. **对话冷热物理分离归档 (`main.db` <-> `archive.db`)**：
   - 实现后台可配置闲置天数自动归档任务与手动 Archive 接口（SQLite `ATTACH` 跨库原子迁移并清理主库），以及归档会话列表浏览、一键 **Unarchive** 恢复与**永久删除**（含孤立 CAS Blob 引用计数回收）。
3. **Runner 级多时间窗统计面板**：
   - 实现 `models.dev` 后台定时同步与手动刷新。
   - 基于 `usage_rollups_hourly` 小时桶预聚合表，在前端落地全局及每个 Runner 的 **今天 / 近 7 天 / 近 30 天** Token 消耗、缓存命中率、调用模型排行与预计美元开销看板。

---

## M6：`egoist/mygo` 桌面端与移动端全平台打包交付

**目标**：复用同一套 `clients/web` 前端资产，交付轻量原生级 Desktop 与 Mobile 客户端。

1. **`clients/app` (`mygo` v0.10.0+ 跨平台宿主壳)**：
   - 实现前端 `HostBridge` 适配层与 Go 侧 `mygo.Bind` Typed IPC 对接（Keychain/Keystore 安全存取凭证、系统通知、触觉反馈）。
2. **Desktop 三端交付（macOS / Windows / Linux，零 CGO）**：
   - 接入系统托盘常驻、全局呼出快捷键，并提供「单机开箱即用开关」（桌面端进程内直接托管启动本机内置 `nex-runner`）。
3. **Mobile 双端交付（iOS / Android）**：
   - 打磨小屏手势抽屉布局（底部输入栏、侧滑切换工作台 Tab）、接入 APNs / FCM 原生后台推送（当远端 Runner 触发高危工具审批时锁屏弹窗唤醒一键放行），产出签名安装包。
