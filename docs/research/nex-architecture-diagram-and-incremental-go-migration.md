# Nex 架构图与渐进式 Go 改写方案

> 日期：2026-10-08
> 目的：给出整体架构图，并识别可以**渐进式**替换为 Go 的边界
> 前置结论均已在本机核实（见各图下方「已核实」标注）

---

## 一、整体架构图

### 1.1 部署形态（三条产品线共用一套后端）

```
┌──────────────────────────────┐   ┌──────────────────────────┐   ┌────────────────────────┐
│  桌面 (Electron 41)           │   │  Web (浏览器)             │   │  TUI (终端)             │
│  ~173MB DMG                  │   │  纯静态 SPA              │   │  nex 命令无参启动        │
├──────────────────────────────┤   ├──────────────────────────┤   ├────────────────────────┤
│ renderer  1.2k LOC            │   │  packages/web  1.9k LOC  │   │  packages/tui          │
│  └─ @nex/ui 291k LOC         │   │  └─ @nex/ui (同一份)      │   │  13.6k LOC             │
│     (54% 纯 DOM，不重写)      │   │                          │   │  (OpenTUI + React)    │
├──────────────────────────────┤   ├──────────────────────────┤   ├────────────────────────┤
│ preload  1.2k  contextBridge  │   │  WebSocket /ws           │   │  进程内直连 core       │
├──────────────────────────────┤   └────────────┬─────────────┘   └───────────┬────────────┘
│ main  38.8k                   │                │                             │
│  窗口/托盘/更新/内嵌浏览器       │                │                             │
│  WebContentsView+CDP 4.6k     │                │                             │
├──────────────────────────────┤                │                             │
│ host  7.3k  每窗口一个        │                │                             │
│  window-scoped Local Host    │                │                             │
└───────────┬──────────────────┘                │                             │
            │ MessagePort                       │                             │
            ▼                                   ▼                             │
   ┌──────────────────────────────────────────────────────────────────────┐    │
   │  ServiceCollection (@nex/services 87.8k)                              │    │
   │  nex-agent 18.8k │ bots 13.8k │ session 8.5k │ git 4.8k │ ...       │    │
   │  39 个 service，通过 IServiceAccessor 暴露                             │    │
   └───────────────────────────────┬──────────────────────────────────────┘    │
                                   │                                          │
                    ┌──────────────┴───────────────┐                          │
                    │  ★ 唯一的跨语言边界 ★          │                          │
                    │  stdio: NDJSON + JSON         │                          │
                    │  NexProtocolMessage (zod)     │◄─────────────────────────┘
                    │  由 NEX_AGENT_SERVER_COMMAND  │
                    │  决定启动哪个可执行文件          │
                    └──────────────┬───────────────┘
                                   ▼
   ┌──────────────────────────────────────────────────────────────────────┐
   │  Agent 进程                                                          │
   │                                                                       │
   │  bootstrap 64.3k  ── Nex Protocol v3/v4 server 实现                   │
   │  core      98.7k  ── 工具注册表 / 执行器 / 权限 / turn loop / 沙箱    │
   │  adapters  51.0k  ── 子进程 / fs / http / SQLite / MCP / 模型流式      │
   │  contracts 21.7k  ── 27 个 port 接口（唯一 OS 边界）                  │
   │  dynamic-workflow 20.6k ── 工作流 DSL 编译器 + 引擎                   │
   │  ┌─────────────────────────────────────────────────────────────────┐ │
   │  │ SQLite ~/.nex/cli/db/db.sqlite  (25 表 / 22 迁移)               │ │
   │  └─────────────────────────────────────────────────────────────────┘ │
   └──────────────────────────────────────────────────────────────────────┘

┌───────────────────────────────────────────────────────────────────────────┐
│  服务端 nex-server（SEA 单二进制，实测 241MB）                             │
│  Hono HTTP + WS                                                          │
│    /ws       → terminal-client    / web-remote-replayable                 │
│    /ws/host  → trusted-host-relay  / desktop-continuous（一次性 30s 凭据） │
│  11.2k LOC，无业务状态，只做鉴权/配对/心跳/转发/attachment 调度            │
└───────────────────────────────────────────────────────────────────────────┘
```

### 1.2 代码依赖方向（架构策略 `architecture-policy.yaml` 的约束）

```
ui ──► shared(zod schema, 455 个运行时符号) ──► 无
web ──► ui
client ──► rpc, shared
services ──► shared, rpc          ← 业务逻辑，不含 UI
server/nex-server-cli ──► services, rpc, shared
desktop(main/host/preload) ──► services, rpc, shared, client
nex-cli(core/contracts/adapters/bootstrap) ──► 独立 workspace

禁止：ui → runtime 实现 / 跨域深导入 / 循环依赖
现状：managed 模块仅 storage、pets 两个；其余均 legacy
```

---

## 二、可替换性分析：三个关键发现

### 发现 1 ★★★ agent 进程是一个**真正的可替换单元**

**已核实**（`packages/services/src/nex-agent/nexStdioTransport.ts`）：

```ts
// 第 85 行 —— 写
const frame = `${JSON.stringify(message)}\n`;
// 第 232-240 行 —— 读
let newlineIndex = this.stdoutBuffer.indexOf("\n");
const frame = this.stdoutBuffer.slice(0, newlineIndex);
```

**agent ↔ 宿主的线协议是 NDJSON + JSON**，不是 `packages/rpc` 那套自定义 VQL 二进制编码。消息类型是 `NexProtocolMessage`，由 zod schema 校验。

**已核实**（`nexAgentProcessManager.ts:445-470`）启动命令解析顺序：

```
NEX_AGENT_SERVER_COMMAND 环境变量覆盖  ← 可指向任意可执行文件
  ↓ 未设置时
monorepo dev bundle → Electron Node 跑 nex.cjs → 已部署 native binary
```

全仓共 **23 处**引用这个环境变量。SEA 里 `nex-server` 也是通过设 `NEX_AGENT_SERVER_COMMAND = process.execPath` 来让子进程重新进入 agent 角色。

> **结论：可以用 `NEX_AGENT_SERVER_COMMAND=/path/to/nex-go app-server --stdio` 启动一个 Go 实现的 agent，桌面端、server 端、TUI 全部不改。** 这是整个渐进式方案的地基——它把「重写 24 万行」变成了「实现一个协议兼容体」。

**代价**：Go 版 agent 必须实现 Nex Protocol v3（75 个方法）+ v4（80 个命令），约 155 个方法的请求/响应/事件语义。这是真实工作量，但**它是有限、可枚举、可增量完成的工作**，而且每一部分都可以用协议 fixture 单独验证。

> ⚠️ **初版数字有误**：原文写"v3 96 个方法 + v4 34 命令/33 方法，约 130 个"。实测 v3 为 75、v4 为 80（依据 `nexProtocolMethods` 与 `commandPayloadSchemas`）。**v4 命令面比原估大 2.3 倍**，阶段 1 的工作量应按此重新估算。

> **已核实 v3/v4 共用同一通道**：两者都经由 `runNexProtocolAgent()`（`bootstrap/src/nex-protocol-entrypoint.ts:82`）进入，共用同一份 stdin/stdout。因此阶段 1 的 Go agent **只需要实现一个 NDJSON 端点**，v3/v4 两套语义都在其内——不需要两条通道。

### 发现 2 ★★ UI 与后端之间有一道**质量不错的缝**

`packages/ui/src/hooks/useServices.tsx` → `IServiceAccessor`（39 个 service getter），传输层在 `Root.tsx:93` 由 `@nex/client` 注入。

**漏洞**：8 个 UI 文件绕过 `useServices` 直接 `import "@nex/rpc"`（`useTerminalService.ts`、`TerminalSession.tsx`、`useGitAutoRefresh.ts`、`attachmentUploadTransaction.ts` 等）。**它们是任何 RPC 层改动的第一批受害者。**

### 发现 3 ★ 服务端 nex-server 的 HTTP 层很薄，但整体不薄

`packages/server` 只有 11.2k LOC，职责严格限定为鉴权/配对/心跳/转发/attachment 调度，**不持有任务队列或快照**——它已经是「Go 化的形状」。

⚠️ **但不要因此认为 server 侧容易去 Node。** 它启动时调用 `createLocalServices()`（`packages/services/src/node.ts`，**2,238 行**），装配 **39 个 service**（含 file/git/terminal/bots/skills/plugins/credential）并全部经 `/ws` 暴露。**换掉 HTTP 层 ≠ 去掉 Node**，后者需要阶段 2.5（见 §3.1）。

---

## 三、渐进式 Go 改写方案

### 3.0 先说结论：推荐路线是「Agent 外壳先行」而非「自底向上」

常见的渐进式思路是自底向上：先换 SQLite 驱动、再换 RPC、再换服务层。**但这条路在本项目里收益低、风险高**——因为：

- 换 SQLite/RPC 省不下可执行文件体积（体积的大头是 Electron 216MB + SEA 里嵌的 Node 110MB）
- 而**真正占体积和冷启动的是 agent 和 server 二进制本身**

所以推荐**从「可独立替换的进程边界」入手**：先做 agent，再做 server。这两个正好是体积和冷启动收益的来源。

### 3.1 阶段划分

```
阶段 0 ── 准备（不写 Go，先补 TS 侧护栏）
   ├─ 协议 golden fixture：把 v3 的 75 个方法 + v4 的 80 个命令
   │  固化成可跨语言复用的 JSON 样本
   ├─ bash 策略表驱动测试（6.6k 行，当前零覆盖）
   └─ 打通一个"假 agent"（echo 实现的 NDJSON 服务端）作为契约测试基座
   产出：任何 Go 实现都能用它证明"行为一致"

阶段 1 ── Go Agent 外壳（最高收益 / 可独立验证）★★★
   范围：进程生命周期 + 协议服务端 + turn loop 骨架
   保留 TS：全部 122 个工具 handler（通过 RPC 回调 TS 侧）
   收益：可执行文件 16MB(nex.cjs) → ~20MB 静态二进制；冷启动 80ms→6ms
   验证：NEX_AGENT_SERVER_COMMAND 指向 Go 二进制，跑阶段 0 的 fixture

阶段 2 ── Go Server 外壳（服务化独立）★★★
   范围：nex-server 11.2k → Go（HTTP/WS/鉴权/attachment 调度）
   保留 TS：全部 services + agent
   ⚠️ 阶段 2 不去掉 Node：那 11.2k 只是 HTTP/WS 层，
      启动时 createLocalServices()（services/node.ts 2,238 行）
      装配 39 个 service 并经 /ws 暴露。去 Node 见阶段 2.5
   收益：远端部署包 241MB → 十几 MB（去掉内嵌的 110MB Node）
   验证：web 客户端零改动，连 /ws 与 /ws/host 两种角色

阶段 2.5 ── Go services 实现（nex-server 场景去 Node 的必经之路）★★
   见第五节；这是唯一能彻底去掉 server 侧 Node 的阶段
   先做 7 个低风险 service（file/fileWatcher/system/setting/
   modelSelection/providerSettings/onboarding），再做 proxy 类，
   最后做重实现（terminal → git）
   ⚠️ 前两阶段已能拿到大部分体积与冷启动收益，2.5 是"彻底去 Node"的必要条件

阶段 3 ── 按实际收益决定是否继续
   ├─ 3a 存储层：见第五节的 S1–S4（与阶段 1/2 有依赖关系）
   ├─ 3b 工具 handler 逐批迁移（122 个，风险从高到低）
   │      建议顺序：只读工具(read/glob/grep) → 状态写入(write/edit) → bash
   └─ 3c MCP / AI 流式层（有官方 SDK，风险最低但工作量明确）
   ⚠️ 阶段 3 每一步都应独立评估
```

### 3.2 阶段 1 的关键设计：工具委托回 TypeScript

这是让阶段 1 可行的核心设计。Go agent **不实现工具**，而是把工具调用反向代理回 TS 侧：

```
┌─────────────┐   NDJSON/NexProtocol   ┌──────────────┐
│  宿主 (TS)   │◄──────────────────────►│  Go agent     │
│  services    │                        │  bootstrap    │
└─────────────┘                         │  turn loop    │
                                        │  权限裁定      │
                                        └───┬──────────┘
                                            │ 工具调用反向 RPC
                                            ▼
                                    ┌───────────────┐
                                    │  TS 工具宿主   │
                                    │  117 handlers │
                                    │  bash 策略     │
                                    └───────────────┘
```

**收益**：阶段 1 完全**不碰**那 122 个零测试覆盖的工具 handler 和 6.6k 行 bash 策略——把最大的风险推到了阶段 3。

**代价**：多一跳 IPC，工具调用的延迟增加一次序列化往返。但实测 Node 的 JSON 序列化是 6.5ms/200 消息，**单次工具调用的往返开销在毫秒量级，对交互体验无感**。

**必须保持不变的语义**（否则宿主行为会变）：

- 进程树管理（`terminateProcessTree`、Windows Job Object）
- `storagePreparationEntry` 能力字段（SEA 用它释放内嵌 nex.cjs 做存储准备）
- `--surface` 参数语义
- stderr 收集与进程退出码

### 3.3 每个阶段的「可回滚点」

渐进式的核心是**每阶段都能单独退回**：

| 阶段  | 回滚方式                                 | 用户可见影响       |
| ----- | ---------------------------------------- | ------------------ |
| 0     | 无（纯加测试）                           | 无                 |
| 1     | 删掉 `NEX_AGENT_SERVER_COMMAND` 环境变量 | 立即退回 TS agent  |
| 2     | server 启动时改回 `nex.cjs`              | 立即退回 TS server |
| 2.5   | 按 service 名配置路由表，逐个切回 TS     | 粒度到单个 service |
| S2/S3 | 把该 DB 所有权切回 TS                    | 两个库互不干扰     |
| S4    | 同上（切换本身即回滚点）                 | 立即退回 TS 存储   |
| 3b+   | 按工具名配置路由表，逐个切               | 粒度到单个工具     |

**阶段 1、2 与 S2/S3 不需要改动任何 TS 源码即可回滚**——只靠环境变量、启动命令和配置路由表。

---

## 四、存储层渐进替换（SQLite）

> 单独成节，因为它是**唯一一个可以完全独立于其他阶段推进**的迁移线：两个数据库的所有权彼此独立，S2 线不依赖阶段 1，S3 线依赖阶段 1。

### 4.1 先厘清一个容易误解的点

「把 SQLite 改用 Golang 实现」有两种含义，成本差一个量级：

| 含义            | 做法                                    | 判断                                                                        |
| --------------- | --------------------------------------- | --------------------------------------------------------------------------- |
| **A. 只换驱动** | 保留 Node 进程，加 Go sidecar 做 SQLite | **无意义**——Node 用的是内置 `node:sqlite`，换驱动不等于换语言，还多一个进程 |
| **B. 换实现方** | Go 进程独占读写，TS 经 RPC 访问         | **✅ 这才是渐进替换，下文讨论的就是它**                                     |

### 4.2 三个已核实的有利前提

**前提 1：数据库已是 WAL 模式，且明确设计为多进程共享。**

已核实 `migration-runner.ts:248` 设置 `journal_mode = wal`，配合 `busy_timeout` 与指数退避重试（`WAL_RETRY_INITIAL_DELAY_MS = 10` → `MAX = 200`）。更关键的是 `dwf-journal.ts:391` 的注释：

> 序号分配与写入必须是同一条语句：MAX(sequence)+1 单独读一次再插入，会在多进程（**WAL 下 nex 允许多个 Agent 共享同一个库**）之间竞争出重复序号。

**「多个进程同时打开这个库」是既有的、被依赖的设计**，不是Go 引入的新风险。

**前提 2：session-store 是纯CRUD，职责边界干净。**

`sqlite-session-store.ts` 1,022 行 + 约 20 个 repository，每个文件仅import 1–3 个 `node:` API（主要是 `node:fs` / `node:path`）。**无子进程、无网络、无复杂异步**——这是整个后端最容易切的一块。

**前提 3：已有 port 抽象。**

`contracts/src/interfaces/session-store.port.ts` 定义了抽象，TS 侧经`SessionStorePort` 访问。**换实现不需要改调用方。**

### 4.3 关键约束：不能按语言拆分同一个库

仓库里是**两个独立的数据库**：

| 库             | 位置                      | 拥有者                 | 依赖                               |
| -------------- | ------------------------- | ---------------------- | ---------------------------------- |
| session 库     | `~/.nex/cli/db/db.sqlite` | **agent 侧**           | 25 表 / 22 迁移 / 9k 行            |
| tasks-index 库 | 独立文件                  | **services（宿主）侧** | `automationRepo` / `taskIndexRepo` |

⚠️ **agent 侧的DB 准备由独立子进程完成，其路径从不过协议**（`storagePreparationEntry` 机制，见 `packages/shared/src/database-startup.ts`）。

> **因此：session 库的所有权必须整体切换，不能一边 Go 一边 TS 争抢初始化权。** S3 必须在阶段 1 的 Go agent 就绪之后进行，因为存储准备能力随 agent 一起迁移。

### 4.4 推进路径

```
S1 ── 补测试（不写 Go）★ 必选项
      · 22 个迁移的幂等性
      · 各 repository 的读写往返
      · WAL 并发写：2 进程同时 append，验证无重复序号
      ⚠️ 当前 session-store 与 tasksDatabase 均为零测试

S2 ── Go 实现 tasks-index 库  ← 建议最先做
      · 归宿主所有，agent 侧不接触→ 无跨进程竞争，最安全
      · 服务于阶段 2.5 的早期 service

S3 ── Go 实现 session 库（25 表）  ← 依赖阶段 1
      · 与 TS 版对同一fixture 做差分比对
      · 读写必须原子切换，禁止双写

S4 ── 所有权切换：Go 独占，TS 不再打开
      · 这一步才是"真的换掉了"
```

**S2 先行的理由**：tasks-index 库归services 侧所有，与 agent 零竞争；而 session 库与 agent 强绑定。**先切无竞争的那个，能把存储迁移的早期风险压到最低。**

### 4.5 两个必须提前处理的风险

**风险 1：`modernc.org/sqlite` 会改变性能特征。**

生态调研中它官方自陈 CPU 密集查询比 C 慢 **1.3–2.0x**。而会话列表分页、usage 聚合**恰好是 CPU 密集的**。

> **建议在S1 阶段就用真实查询集测一遍**，再决定用 `modernc`（纯 Go，保住零 cgo 交叉编译）还是 `ncruces`（并发更强）。这个决定要在写 Go 代码**之前**做。

**风险 2：迁移期的双实现并存。**

S3 阶段会短暂存在「Go 读、TS 写」或反之的状态。**必须约定单写者**（single-writer），否则 WAL 下会出现两个实现对同一张表写不同编码的数据——这类问题不会立刻暴露，排查成本很高。

### 4.6 与其他阶段的关系

```
       阶段 0（协议 fixture）
             │
             ├──►阶段 1 Go agent ──────┐
             │                          │
阶段 2.5 Go services ◄──S2 tasks-index  │
   │                                   │
   └──► S3 session 库 ◄────────────────┘
              │
              └──► S4 所有权切换
```

- **S1/S2 与阶段 1、2 无依赖**，可并行推进
- **S3 依赖阶段 1**（存储准备随agent 迁移）
- **S4 是存储线终点**，独立可回滚：把该 DB 所有权切回 TS 即回退

### 4.7 这一节的核心判断

存储层是**整个迁移中唯一同时满足三个条件的部分**：

1. ✅ **职责边界干净**（纯 CRUD，已在 port 后面）
2. ✅ **技术前提成立**（WAL 多进程共享是既有设计）
3. ✅ **可独立验证与回滚**（两个库互不干扰）

**它应该作为第一条实际投入的迁移线**，比阶段 1 的Go agent 更适合起步——因为即使后续 agent 迁移因故暂停，存储层的收益（S4 完成 = 存储去 Node）已经落袋。

---

## 五、这个方案能拿到什么、拿不到什么

### 能拿到

| 收益                 | 来源                                     | 幅度                            |
| -------------------- | ---------------------------------------- | ------------------------------- |
| 远端部署包体积       | 阶段 2（去掉内嵌 Node 110MB）            | 241MB → 十几 MB                 |
| agent 冷启动         | 阶段 1（Node 80ms → Go 6ms）             | 13 倍                           |
| 存储层去 Node        | **S4**（最早可独立达成的去 Node 里程碑） | 两个DB 全部 Go 持有             |
| server 侧彻底去 Node | 阶段 2.5 + S4 全部完成                   | 用户无需预装 Node               |
| 可独立发版           | 阶段 1（Go agent 单二进制）              | 不再需要 Node 工具链            |
| 交叉编译三平台       | 阶段 1/2/S（零 cgo）                     | 一条命令出 darwin/linux/windows |

### 拿不到 / 需要说清楚

| 项目               | 原因                                                                                                                           |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------ |
| **桌面安装包体积** | 大头是 Electron 框架 216MB + UI 资源，**不在这几个阶段内**。即使 3b 全做完也只能省下 `Resources/glm/nex.cjs` 那 15.4MB（实测） |
| **UI 性能**        | UI 永久留在 TS/浏览器                                                                                                          |
| **吞吐提升**       | 实测 JSON 差异被模型延迟淹没，见 `nex-architecture-and-golang-rust-feasibility.md` §3.5                                        |
| **去掉 JS 运行时** | 7 处 `node:vm` 沙箱 + 5 处脚本动态加载路径需在阶段 3 单独处理；其中 `node_repl` 的 `importModule` 是**是否保留完整 JS 运行时**的决定性开关                                                                  |

> **必须诚实的一点**：桌面 DMG 仍是 173MB，因为 Electron 本身就要 216MB。用户感知的"小体积"主要来自**远端部署**（nex-server 241MB → 十几 MB），而不是桌面。
>
> **「去掉 Node」有两种程度**，别混淆：
>
> - **部分去 Node**（阶段 1+2）→ 仍有 Node 跑 services 和工具，体积与冷启动收益已拿到
> - **彻底去 Node**（阶段 2.5 + S4）→ 用户部署时无需预装 Node，这是自部署场景的主要卖点
> - 存储线**S4 单独完成即是一个独立里程碑**，不依赖 2.5

---

## 六、与前几份报告的关系

- **可行性与逐层判定** → `nex-architecture-and-golang-rust-feasibility.md`
- **Go/Rust 生态对比** → `agent-backend-go-rust-ecosystem-2026-10.md`
- **测试覆盖实测** → 同上 §6（0.6%，缺口与风险重合）
- **命名面审计** → `nex-naming-surface-audit.md`

本方案的核心依据是可行性报告的一个约束：**bash 策略（6.6k 行）与 122 个工具 handler 零测试**。阶段 1 的「工具委托回 TS」设计，正是为了在不补测试的前提下避开这块零覆盖区域。

**推荐先做阶段 0。** 它不产出任何 Go 代码，但决定了阶段 1 是否可行——协议 fixture 能否在两种语言间保持语义等价，是这个方案成立的前提。
