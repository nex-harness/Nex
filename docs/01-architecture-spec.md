# Nex-v3 架构与核心技术选型规范 (Architecture & Tech Stack Spec)

- **状态**: Approved Core Direction
- **更新日期**: 2026-11-18
- **代码仓库与 Go Module**: Go Module 固定为 `github.com/nex-harness/nex`（当前开发期临时 Git 仓库为 `https://github.com/nex-harness/next`，后续平滑切换至 `nex` 仓库无需改动源码 import）
- **设计基准**: 参考 `pi-agent` (`pi-ai` + `pi-agent-core`) 核心理念，使用 **Golang** 重写核心引擎与跨平台守护进程。

---

## 1. 设计目标与核心原则

Nex-v3 旨在构建一套**「中心控制面 (Server) + 跨平台自治执行节点 (Runner) + 多端视图层 (Web / Desktop / Mobile)」**的智能体系统（Agent Harness）：

1. **无头跨平台部署 (Headless & Cross-Platform)**：Runner 运行在 macOS、Linux、Windows 之上，不向用户直接提供 UI 界面，编译为零外部依赖的静态单二进制文件，以系统服务或后台守护进程运行。
2. **Runner 完全自治 (Offline-Capable Autonomous Node)**：Runner 内聚完整的 `Agent Loop`、多模型 `Provider` 适配层、本地工具集、本地 MCP 宿主与 `Cron` 定时调度器。即使与中心 Server 断网或脱离 Server 独立运行，Runner 依然能按计划执行定时任务与自治工作流。
3. **中心化管控与多端同步 (Centralized Control & Multi-Client Sync)**：用户统一通过 Web、Desktop、Mobile 客户端连接 Server 与任意 Runner 对话；MCP 注册表、Skills 资产库、定时任务规则与全局策略统一在 Server 管理并下发至目标 Runner。
4. **先本地落盘、后增量同步 (Local-First Event Sourcing)**：Runner 产生的所有会话事件与工具执行记录遵循本地持久化优先（SQLite WAL）与递增序号（`seq`）机制，天然支持弱网断线重连与离线任务结果补传（Outbox Sync）。

---

## 2. 总体拓扑架构

```text
  ┌─────────────┐     ┌───────────────┐     ┌──────────────┐
  │ Web Client  │     │Desktop Client │     │Mobile Client │
  └──────┬──────┘     └───────┬───────┘     └──────┬───────┘
         │                    │                    │
         └────────────────────┼────────────────────┘
                              │ HTTPS / WSS (Event Stream + RPC)
                              ▼
             ┌─────────────────────────────────┐
             │      nex-server (Control Plane) │
             │  - 多端会话路由与事件归档同步   │
             │  - Runner 注册、鉴权与在线心跳  │
             │  - Skills / MCP / Cron 配置中心 │
             │  - 云端共享 MCP 宿主 (SaaS 类)  │
             │  - 可选 LLM Gateway (代填密钥)  │
             └────────────────┬────────────────┘
                              ▲
         反向出站长连接 (WSS / ConnectRPC 双向流，穿透 NAT/防火墙)
         ┌────────────────────┼────────────────────┐
         │                    │                    │
┌────────┴────────┐  ┌────────┴────────┐  ┌────────┴────────┐
│   nex-runner    │  │   nex-runner    │  │   nex-runner    │
│    (macOS)      │  │    (Linux)      │  │   (Windows)     │
├─────────────────┤  ├─────────────────┤  ├─────────────────┤
│ - Agent Loop    │  │ - Agent Loop    │  │ - Agent Loop    │
│ - LLM Providers │  │ - LLM Providers │  │ - LLM Providers │
│ - Coding Tools  │  │ - Coding Tools  │  │ - Coding Tools  │
│ - Local MCP     │  │ - Local MCP     │  │ - Local MCP     │
│ - Cron Engine   │  │ - Cron Engine   │  │ - Cron Engine   │
│ - SQLite Store  │  │ - SQLite Store  │  │ - SQLite Store  │
└─────────────────┘  └─────────────────┘  └─────────────────┘
```

---

## 3. 核心组件职责边界

### 3.1 `nex-runner`（自治数据面 / Autonomous Data Plane）

借鉴 `pi-agent` 的极简内核思想，每个 Runner 是一个五脏俱全的无头智能体运行时：

1. **Agent Loop 状态机 (`pkg/agent`)**
   - 维护结构化会话上下文消息树/列表。
   - 支持 `pi-agent` 标志性的双队列控制：
     - **`steer` 队列**：在运行中（工具执行间隙）即时插入用户纠偏指令，打断当前剩余工具链并转向。
     - **`followUp` 队列**：在当前 Turn 完整结束后自动追加下一轮提示词。
   - 产出细粒度生命周期事件流：`agent_start` -> `turn_start` -> `message_update` (streaming delta / thinking delta / toolcall delta) -> `tool_execution_start` -> `tool_execution_update` -> `tool_execution_end` -> `turn_end` -> `agent_end`。
2. **多模型 Provider 适配层 (`pkg/ai`)**
   - 内置统一流式接口 `Provider.Stream(ctx, req)`，屏蔽厂商协议差异（文本流、Thinking 思考块与签名/加密推理项回传、增量 Tool Call JSON、多模态输入、Prompt Caching 标记、Token 用量统计）。
   - **一期核心内置适配器**（其余协议如旧版 Chat Completions、Gemini 原生接口留作后续按需扩展）：
     1. **OpenAI / Codex Responses Adapter (`openai_responses`)**：对接 `/v1/responses` 流式协议（完整支持 Reasoning Items、`encrypted_content` 回传、Function Calling 与 Prompt Cache 统计）。
     2. **Anthropic Messages Adapter (`anthropic`)**：对接 `/v1/messages` 流式协议（完整支持 Extended Thinking + `signature` 回传、Tool Use 与缓存断点）。
   - **双模密钥路由**：
     - **直连模式 (Direct Mode)**：使用 Runner 本地配置的凭证直接请求厂商 API（保障脱离 Server 独立运行能力）。
     - **网关代理模式 (Server Proxy Mode)**：将 Provider `BaseURL` 指向 `nex-server` 的 `/llm-proxy/*` 接口，携带 Runner 节点 Token 由 Server 代填真实 API Key，实现受管机器零密钥落盘。
3. **本地工具与跨平台宿主 (`pkg/harness`)**
   - 内置核心 Coding Tools：`read`、`write`、`edit`（兼容 LF/CRLF 与缩进容错匹配）、`bash`/`shell`、`grep`（纯 Go 遍历 + `.gitignore` 过滤，免外部二进制依赖）。
   - **进程树隔离与清理**：Unix 采用进程组（`Setpgid`）清理子进程；Windows 绑定 **Windows Job Object**（`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`），确保取消任务或超时退出时不留僵尸进程。
   - **Windows Shell 适配**：自动探测 Git Bash / WSL 或回退至 PowerShell，并在 System Prompt 中注入当前 Runner 的 OS、Shell 语法提示与路径规范。
4. **本地快照、Cron 调度与离线补传 (`~/.nex-runner/`)**
   - 本地内嵌无 CGO SQLite（WAL 模式），存储：
     - 从 Server 同步下来的配置版本快照（MCP 清单、定时任务 Cron 定义、权限策略）。
     - 同步缓存在本地磁盘的 Skills 包（`~/.nex-runner/skills/<skill-name>/`，支持带可执行脚本的 Skill 本地调用）。
     - 本地会话历史与带递增 `seq` 的事件 Outbox 表。
   - **内置 Cron 调度器**：无论是否连接 Server，按本地持久化的 Cron 规则准时唤醒 `Agent Loop` 独立执行任务；产生的事件写入 Outbox，在线时实时流式上报，离线时缓存并在网络恢复后按 `seq` 自动增量回传至 Server。

### 3.2 `nex-server`（中心控制面 / Central Control Plane）

1. **连接与节点管理**：接收各平台 Runner 主动发起的反向长连接（规避 NAT 与防火墙入站限制），维护节点注册、鉴权、标签、在线状态与健康监控。
2. **多端会话总线与事件归档**：汇聚所有 Runner 上报的 `seq` 事件流并持久化；向 Web / Desktop / Mobile 客户端提供基于 `last_seen_seq` 的断点续传与多端实时同屏同步（包括打字机流式输出与工具审批状态）。
3. **统一资源配置中心 (MCP / Skills / Cron)**：
   - **Skills 分发**：管理全局或节点级 Skills 仓库（含 `SKILL.md` 及附属脚本资产），按内容 Hash 增量同步至目标 Runner 本地缓存目录。
   - **MCP 分层治理**：
     - **节点态 MCP (Runner-Hosted)**：操作宿主机环境的 MCP（如本地浏览器自动化、桌面控制、Docker、内网资源），由 Server 下发配置，Runner 在本地通过 `stdio` / 本地端口拉起并管理生命周期。
     - **中心态 MCP (Server-Hosted)**：SaaS 与外部 API 类 MCP（如 GitHub、Notion、Linear、Web 搜索），凭证集中保存在 Server 并由 Server 托管运行；Runner 在连线状态下可通过 Server 双向流远程路由调用此类工具（离线状态下自动降级标记为不可用）。
   - **Cron 编排**：提供统一的定时任务管理界面，变更实时下发到目标 Runner 的本地调度快照中。
4. **LLM 代理网关（可选开启）**：为不希望在终端机器保存 API Key 的 Runner 提供统一的流式转发、配额统计与审计。

### 3.3 客户端 (`Web` / `Desktop` / `Mobile`)

- 纯视图与交互控制层，不包含本地 Agent 执行逻辑。
- 核心能力：选择目标 Runner 创建/切换会话、实时渲染流式消息与思考过程、响应工具权限审批（Approval Request）、运行中发送 `steer` 插话或中止信号、管理 Server 上的 MCP / Skills / Cron / Runner 节点。

---

## 4. 关键运行机制与边界策略

### 4.1 在线交互 vs 离线自治的权限策略 (Permission Policy)

由于 Runner 无本地 UI，工具调用权限审批按触发源区分策略：

| 运行场景 | 默认策略模式 | 行为机制 |
| :--- | :--- | :--- |
| **用户在线对话** | `interactive` | 命中需确认的高危操作时，Runner 挂起当前工具协程，发送 `approval_requested` 事件至 Server；用户在任意客户端点击允许/拒绝后，结果推回 Runner 继续执行。 |
| **Cron 定时任务 / 离线自治** | `autonomous-policy` | 任务配置时显式绑定非交互权限白名单（如允许指定工作区读写与白名单命令，`auto-approve` 或 `workspace-write`）。若触发超出白名单的操作且无法在超时窗口内联系上 Server 人工审批，立即向大模型返回明确的 `permission denied (headless mode)` 错误，由模型自主降级处理或记录失败报告，严禁无限期阻塞协程。 |

### 4.2 断网与重连一致性协议

1. **Runner -> Server**：每条 Session 内事件拥有Runner本地单调递增的 `seq`（`uint64`）。重连握手时，Server 返回已确认接收的 `ack_seq`，Runner 从 `ack_seq + 1` 开始批量补推 Outbox 事件，随后无缝切回实时流。
2. **Client -> Server**：手机锁屏、切换网络重连时，客户端携带 `last_seen_seq` 订阅会话，Server 补发缺失事件帧，保证不丢字、不乱序。

---

## 5. Golang 代码库目录结构规划 (Monorepo)

采用单一 Go Module（或 Go Workspace）组织核心库与双端可执行入口，确保核心类型 100% 复用且保持极小编译体积：

```text
nex-v3/
├── docs/
│   ├── 01-architecture-spec.md                  # 总体架构与技术选型规范（本文档）
│   ├── 02-client-and-protocol-spec.md           # 客户端技术栈 (mygo + React) 与通信协议规范
│   ├── 03-agent-and-plugin-spec.md              # Go 智能体内核与 TS 动态插件扩展系统规范
│   ├── 04-mcp-and-codemode-spec.md              # MCP 双层懒加载与 goja Code Mode 沙箱执行规范
│   ├── 05-subagent-concurrency-spec.md          # Subagent 同机并发、深度锁定与隔离规范
│   ├── 06-storage-and-observability-spec.md     # 存储分层、对话冷热归档、models.dev 与统计透视规范
│   ├── 07-remote-terminal-and-workspace-spec.md # libghostty WASM 远程终端与多 Tab 工作台规范
│   └── 08-implementation-roadmap.md             # 分阶段工程实施路线图 (M1 ~ M6)
├── pkg/
│   ├── ai/                            # 对标 pi-ai：统一消息模型、流式事件定义、Anthropic/OpenAI/Gemini 适配器
│   ├── agent/                         # 对标 pi-agent-core：Agent Loop 状态机、steer/followUp 队列、生命周期事件
│   ├── extension/                     # 对标 pi-agent Extensions：Go 原生插件接口 + goja 纯 Go JS 运行时 + 远程 ctx.ui 桥
│   ├── harness/                       # 工具执行器 (read/write/edit/bash/grep)、Skills 加载器、MCP 宿主、Cron 调度、权限守卫
│   ├── store/                         # 纯 Go SQLite 封装（Runner 本地快照 & Outbox / Server 持久化接口）
│   └── protocol/                      # Server <-> Runner <-> Client 通信帧结构、错误码与同步协议定义
├── cmd/
│   ├── nex-runner/               # 跨平台无头守护进程入口（macOS / Linux / Windows 服务管理 + 本地自治引擎 + 反向连接）
│   └── nex-server/               # 中心控制面服务入口（多端网关、节点调度、MCP/Skills/Cron 管理中心）
└── clients/                      # 后续客户端工程目录（Web / Desktop / Mobile）
```

---

## 6. 核心技术栈选型清单

| 领域 | 选型方案 | 选型理由 |
| :--- | :--- | :--- |
| **核心语言** | **Go (1.27.3+) + TypeScript** | 后端/Runner/跨平台客户端壳全栈纯 Go（对齐 `mygo` 工具链要求），零 Rust、桌面零 CGO，交叉编译零依赖单二进制。 |
| **本地数据库** | **SQLite (`modernc.org/sqlite`)** | 纯 Go 实现（无 CGO），保障 Windows/Linux 跨平台静态编译不破功；开启 WAL 模式满足高频事件流落盘；Server 端初期亦可共用 SQLite，后期可按需扩展 Postgres。 |
| **Tool Schema 生成** | **Go Struct + `invopop/jsonschema`** | 通过结构体与 Tag 自动生成标准 JSON Schema 喂给大模型，兼顾编译期类型安全与开发效率。 |
| **MCP 协议栈** | **`modelcontextprotocol/go-sdk`** | 官方 Go SDK，统一支撑 Runner 侧 `stdio` 子进程宿主与 Server 侧 Streamable HTTP / 远程工具代理。 |
| **Cron 调度器** | **`robfig/cron/v3`** | 轻量稳定，支持标准 5/6 段 Cron 表达式与时区绑定，内嵌于 `nex-runner`。 |
| **系统服务托管** | **`kardianos/service`** | 一套代码将 `nex-runner` 注册为 macOS `launchd`、Linux `systemd` 或 Windows Service 后台常驻运行。 |
| **长连接传输层** | **HTTPS REST + WebSocket (`coder/websocket`)** | 单连接多路复用 JSON Envelope，零代理摩擦打通浏览器、`mygo` Desktop/Mobile 与 Go Runner。 |
| **插件扩展引擎** | **Go Interface + `evanw/esbuild` + `dop251/goja`** | 纯 Go 零 CGO 闭环：插件用 TS 编写，Server 内存内 `esbuild` 转译单文件 JS 下发，Runner 内置 `goja` 沙箱热加载执行并支持远程 `ctx.ui`。 |
| **远程终端技术栈** | **`coder/ghostty-web` (`libghostty-vt` WASM) + `creack/pty` & `ConPTY`** | 前端采用 Ghostty Zig 编译的 WASM 终端核心，Runner 侧采用纯 Go 零 CGO 跨平台伪终端与 WebSocket 二进制流。 |
| **三端客户端栈** | **`egoist/mygo` (v0.10.0+ Web Mode) + React/TS** | 单套响应式 React 前端同时驱动 `nex-server` 内嵌网页、`mygo` 桌面端与 `mygo` iOS/Android 移动端。 |

---

## 7. 子规范文档索引 (Specs Index)

1. **`02-client-and-protocol-spec.md`（已完成）**：`mygo` (Web Mode) + 单套 React/TS 三端客户端架构、原生 `HostBridge` 抽象以及 HTTPS REST + WebSocket 同步通信协议规范。
2. **`03-agent-and-plugin-spec.md`（已完成）**：Go 智能体内核（`pkg/ai`、`pkg/agent`、跨平台基础工具）与双层插件扩展系统（Go 原生接口 + TS/`esbuild`/`goja` 动态插件 + 远程声明式 `ctx.ui` 交互协议）。
3. **`04-mcp-and-codemode-spec.md`（已完成）**：MCP 双层懒加载（Prompt 零膨胀 + 本地 `stdio` 进程按需唤醒与空闲回收）以及基于 `goja` 隔离沙箱的 `codemode` 并发编排规范（透明打通节点态与中心态 MCP，且不绕过权限审批）。
4. **`05-subagent-concurrency-spec.md`（已完成）**：同机多协程并发 Subagent 机制（锁死默认嵌套深度 `max_depth = 1`、预留跨 Runner 委派扩展点、前台扇出与后台异步邮箱、只读分级与 `git worktree` 写隔离、以及父流摘要/子流按需订阅）。
5. **`06-storage-and-observability-spec.md`（已完成）**：`main.db` + `archive.db` 对话冷热物理分离归档、`models.dev` 模型能力与单价同步、6 维 `Context windows` 实时占比浮层 + 按需 CAS Blob Trace 检视、以及按 Runner 维度的 Today / 7D / 30D 小时桶预聚合统计体系。
6. **`07-remote-terminal-and-workspace-spec.md`（已完成）**：基于 `libghostty-vt` WASM 与零 CGO 跨平台 PTY 的远程终端，以及右侧多标签侧边栏（`Files` 树状目录、新 Tab 文件预览、`Review` Git Diff 审查、`Terminal` 与支线对话）。
7. **`08-implementation-roadmap.md`（已完成）**：按端到端可验证垂直切片划分的 6 阶段实施路线图（M1 ~ M6）。
