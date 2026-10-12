# Nex 架构梳理与 Go/Rust 重写可行性分析

> 日期：2026-10-08
> 依据：当前检出源码（分支 `fix/model-provider-toasts`，基线新鲜）+ 本机实测基准 + 当日抓取的生态数据
> 范围：整体架构 → 自底向上逐层可行性判定 → Go vs Rust 性能与生态对比
> 前提：暂不考虑工作量，只考虑**可行性**

---

## 第一部分：整体架构

### 1.1 规模总览

仓库是 pnpm monorepo，约 **75 万行 TypeScript**，26 个包。**不可重写的 UI 占近 40%，这决定了重写问题的真正形状。**

| 层             | 包                                                          | 源码行数 | 运行环境                          |
| -------------- | ----------------------------------------------------------- | -------: | --------------------------------- |
| **UI**         | `packages/ui`                                               |  291,711 | 浏览器（Electron renderer / Web） |
|                | `packages/web`                                              |    1,891 | 浏览器                            |
| **Agent 内核** | `apps/nex-cli/packages/core`                                |   98,740 | Node                              |
|                | `apps/nex-cli/packages/adapters`                            |   50,955 | Node                              |
|                | `apps/nex-cli/packages/bootstrap`                           |   64,286 | Node                              |
|                | `apps/nex-cli/packages/contracts`                           |   21,656 | 纯类型                            |
|                | `apps/nex-cli/packages/dynamic-workflow`                    |   20,595 | 纯逻辑                            |
|                | `apps/nex-cli/packages/cli` + `tui`                         |   23,437 | Node / 终端                       |
| **服务与协议** | `packages/services`                                         |   87,836 | Node                              |
|                | `packages/shared`（协议 v3 + v4）                           |   14,598 | **两端都跑**                      |
|                | `packages/shared`（其余）                                   |   26,111 | **浏览器 + Node**                 |
|                | `packages/rpc`                                              |    3,137 | 传输无关                          |
| **宿主**       | `packages/desktop`（main 38.8k / host 7.3k / preload 1.2k） |   49,033 | Electron main 进程                |
| **服务端**     | `packages/server`                                           |   11,168 | Node                              |
|                | `packages/nex-server-cli`                                   |    5,718 | Node                              |

### 1.2 进程与部署拓扑

```
┌─────────────────────────────────────────────────────────────────┐
│ 桌面 (Electron)                                                 │
│                                                                 │
│  renderer (1.2k LOC React 壳)                                   │
│      ↕ MessagePort / contextBridge                              │
│  main (38.8k LOC)  ── 窗口/托盘/更新/内嵌浏览器 WebContentsView   │
│      ↕ electronUtilityProcess.fork (每窗口一个)                 │
│  host (7.3k LOC)  ── window-scoped Local Host                   │
│      │           每窗口一份 ServiceCollection                   │
│      │           + windowRemoteConnectionRegistry               │
│      ↕ stdio JSON-RPC ("app-server --stdio --surface desktop") │
└──────┼──────────────────────────────────────────────────────────┘
       │
  ┌────▼─────────────────────────────────────────────────────────┐
  │ Agent 进程 (nex.cjs 或 SEA 二进制)                            │
  │   bootstrap  → Nex Protocol v3/v4 server                      │
  │   core       → 工具注册表 / 执行器 / 权限 / turn loop          │
  │   adapters   → 子进程 / fs / http / SQLite / MCP / 模型       │
  │   contracts  → 27 个 port 接口（唯一 OS 边界）                │
  └──────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│ 服务端 (nex-server，SEA 230MB 单二进制)                          │
│   Hono HTTP + WebSocket，两条独立通道：                          │
│     /ws      → terminal-client / web-remote-replayable          │
│     /ws/host → trusted-host-relay / desktop-continuous          │
│   （用一次性 30s capability 换取，与普通 token 权限分离）        │
└─────────────────────────────────────────────────────────────────┘
```

### 1.3 三条关键设计线索

**(1) 端口-适配器已就位。** `apps/nex-cli/packages/contracts/src/interfaces/` 下有 **27 个 `.port.ts`**，定义了 Execution / FileSystem / HttpClient / SessionStore / Permission / Mcp / Subagent / Workflow / BrowserControl / PdfDocument / ImageProcessor 等全部 OS 边界。工具从不直接调用 OS，只拿 `ToolExecutionContext` 里的 port。**这是整个仓库为重写准备得最好的部分**——agent 内核已经是 runtime 无关的。

**(2) 协议即契约，且两端都做运行时校验。** zod 在全仓 **155 个文件**里被引用，仅 `nex-protocol/index.ts` 一个文件就有 **1,047 处 `z.` 调用**；加上 `nex-protocol-v4/` 的 10,868 行，协议 schema 面共约 **4,259 个 zod 构造器**（估算口径）。legacy 协议 **75 个方法**（`nexProtocolMethods`，`nex-protocol/index.ts:3572`）+ v4 的 **80 个命令**（`commandPayloadSchemas`，`nex-protocol-v4/command.ts`）。关键在于：**zod 不是只做类型推导，它是在线路上跑的真实验证**——`parseCommandEnvelope()` 在服务端 admission 和浏览器 `ui/src/v4/transport.ts` 两端都被调用。

> 📌 **初版报告此处数字有误**：原文写"96 个方法 + 34 个命令 / 33 个方法，约 130 个"。实测 v3 为 75、v4 为 80。总量仍是一百多个、有限且可枚举，但两组分布不同——**v4 命令面比原估大 2.3 倍，阶段 1 的工作量应按 80 个命令重新估算**。

**(3) 内置工具是在进程内的 JS 闭包。** `core/src/tool/handlers/` 有 **122 个文件、22,634 行**，全部是 `(input, ctx) => Promise<Output>` 形态的进程内函数（另有少量 workflow / skill 辅助文件同处该目录）。真正的跨进程边界只有三处：MCP server（JSON-RPC over stdio）、`node_repl` 沙箱、QuickJS-WASM codemode。

> ⚠️ **bash 策略族规模此前被高估约 3 倍。** 初版报告称"约 21k 行的 bash 命令分类策略族"，并据此把它列为最大单一风险块。实测 `core/src` 下所有文件名含 `bash` 的源文件合计仅 **6,604 行 / 43 个文件**（含 `bash.ts` 主入口 569 行；最大的是 `bash-command-permission-policy.ts` 383 行）。
>
> **这个修正削弱但不推翻该风险判断**：bash 策略仍是零测试、且直接决定什么命令需要用户批准（安全边界），阶段 0 补表驱动测试的必要性不变。只是它的绝对体量从"最大单一风险块"降为"与其他工具代码同量级"。

---

## 第二部分：自底向上逐层可行性

### 第 0 层：叶子原语与 Node API

| 能力            | 当前 Node 实现                     | Go                                | Rust                    | 判定                 |
| --------------- | ---------------------------------- | --------------------------------- | ----------------------- | -------------------- |
| 子进程 / 进程树 | `node:child_process`（24 文件）    | `os/exec`                         | `tokio::process`        | ✅ 更好              |
| 文件系统        | `node:fs`（**86 文件**）           | `os` / `io/fs`                    | `std::fs` / `tokio::fs` | ✅ 更好              |
| HTTP 服务器     | Hono                               | axum                              | axum/hyper              | ✅ 更好              |
| WebSocket       | `ws`                               | coder/websocket                   | tokio-tungstenite       | ✅ 平手              |
| SQLite          | `node:sqlite`（22 迁移 / 25 表）   | modernc.org/sqlite                | rusqlite                | ✅ 见下              |
| PTY             | `node-pty`（**必需**，原生 addon） | creack/pty                        | portable-pty            | ✅ Go 更干净         |
| 文本/正则       | V8 Irregexp                        | **RE2（无反向引用、无惰性量词）** | regex（线性保证）       | ⚠️ **Go 有真实缺口** |
| SSH             | `ssh2`                             | x/crypto/ssh                      | russh                   | ✅ Go 更好           |

**SQLite 的选型是硬约束，不是偏好。** `mattn/go-sqlite3` 强制 cgo，会直接摧毁"从 macOS 一条命令交叉编译三平台"。必须选纯 Go 的 `modernc.org/sqlite`（官方自陈 CPU 密集查询慢 1.3–2.0x，对中等写入量的会话库无所谓）或 `ncruces/go-sqlite3`（并发读更强）。Rust 侧 `rusqlite` 同样强制 C 编译。

**本层曾被担心的真缺口是正则——部分排除，仍有一个真实适配点。** Go 的 RE2 出于安全**故意不支持反向引用和惰性量词**，这是 Go 相对 Rust 的真实短板。实测 grep 全量 `core/src/tool/handlers/` 与 `packages/shared/src/`（工具策略、bash 分类、脱敏逻辑所在处）：

- **反向引用（`(?<=`、`(?<!`）：0 命中** —— 对 Go 不构成障碍。
- **惰性量词（`*?` / `+?`）：15 处命中**，落在 3 个文件：
  - `core/src/tool/handlers/webfetch-content.ts`（HTML 清洗的 `[\s\S]*?`）—— ⚠️ **在 Go 需重写的工具层上**；
  - `shared/src/subagent-markdown-selection.ts`、`shared/src/background-task-notifications.ts`——这两个文件浏览器与 Node 都要跑，不随 agent 迁 Go。

**结论**：不能再说"完全不构成障碍"。Go 侧需把这 15 处逐个改写为等价的非贪婪形式（或该文件退回 cgo）。**量级很小、完全可控，但它是一个此前未被记录的适配点**——初版报告在此处只验证了反向引用就下了"已排除"的判断。

> ⚠️ 这条结论只覆盖**当前源码**。后续新增的工具策略若引入反向引用，Go 侧需改写表达式或退回 cgo——建议把这两类正则的检查都加进 lint。

### 第 1 层：持久化与会话存储

**完全可行，且是最干净的一层。** `adapters/src/storage/` 9,014 行，纯 `node:sqlite` + 手写 SQL，无 ORM 无框架。23 张表、22 个迁移必须保持二进制兼容，因为桌面（Node）与远端（将来 Go/Rust）要读同一个 `~/.nex/cli/db/db.sqlite`。

`modernc.org/sqlite` 官方警告两点必须遵守：给 `ORDER BY`/`GROUP BY`/`WHERE` 列建索引；必须 `SetMaxOpenConns`（默认无限开会各自带 page cache）。

### 第 2 层：传输与 RPC

**完全可行。** `packages/rpc` 只有 **3,137 行**，是 VS Code IPC 栈的移植，7 层清晰，传输无关。Go/Rust 都是 100–200 行泛型 + 一个 transport 接口的事。

唯一的真实约束：**wire format 是自定义 VQL 二进制编码，不是 JSON。** 如果迁移期新旧实现要互通，就必须逐字节复刻 `serialization.ts`（含 type tag + varint 长度）。如果允许"切换即全量升级"，可以直接换成 JSON/gob/protobuf 简化掉。

### 第 3 层：协议 schema —— **最大的隐藏成本**

这是整个分析里最容易被低估的一项，也是我建议最先处理的一项。

协议面是 **约 4,259 个 zod 构造器**（估算口径；全仓 `z.*` 实测 7,336 个），分散在 `nex-protocol/index.ts`（3,730 行 / 1,047 处 z.）和 `nex-protocol-v4/`（55 文件 / 10,868 行）。它有三层重写压力：

1. **类型重写**：TS 类型 → Go struct / Rust struct。
2. **运行时校验重写**：zod 的 `.strict()`、`.superRefine()`、`z.discriminatedUnion` 语义要精确复刻。`hostCapabilitiesSchema` 用了 `.strict()`，意味着**新增一个未知字段就会让老 host 拒收**——这类语义偏差会造成难以诊断的线上故障。
3. **⚠️ 代码生成**：zod 之所以便宜，是因为 `z.infer` 自动派生 TS 类型。Go/Rust **没有这个免费午餐**。可行路径是从 JSON Schema 生成，但 `zod-to-json-schema` 已经在依赖列表里，说明**生成链路是现成的**，这条路可行。

**更麻烦的是这个协议不能整体搬走。** `packages/ui/src/v4/transport.ts` 有 **154 处** v4 import，浏览器要**继续用同一份 schema 验证它收到的每一帧**。所以迁移期必然是：UI 侧保留 TS schema，服务端用新语言的副本，两边都要对。**协议 schema 是本次重写里唯一"必须双份维护"的东西。**

### 第 4 层：服务与业务逻辑

`packages/services` 87,836 行 + `core` 98,740 行。子目录分布：

- `nex-agent` 18,794（28 文件）—— agent 运行时桥接，协议耦合重
- `bots` 13,830（27 文件）—— Telegram/飞书/Discord 长驻适配器
- `session` 8,456、`conversation-share` 5,482、`git` 4,778、`model-provider` 3,295

**大部分可行，但有一个结构性陷阱：** `services` 里约一半是**薄门面**（把请求转给 agent 进程）。这类代码在重写后有两种命运：随 agent 一起搬走，或者因为 RPC 框架重构而自动消失。**在动手前应该先统计"纯转发门面"的比例**——如果占比高，真实工作量远小于行数给人的印象。

`bots` 里的飞书 SDK（`@larksuiteoapi/node-sdk`）需要重写成 REST/WS，但这是机械工作。

### 第 5 层：Agent 内核 —— **真正的硬骨头**

这里是"能否重写"的答案所在。四个决定性障碍：

**(1) 内置工具是 122 个进程内 JS 闭包，22,634 行。** 包括 6,604 行 / 43 个文件的 bash 策略族（`bash-readonly-policy-*`、`bash-command-permission-policy.ts`）。这些不是"接口"，是**用 TypeScript 写的业务规则**——大量字符串匹配、正则、模式组合。重写它们等于**重新实现业务逻辑，不是重新接线**。

⚠️ **但规模比初版报告估计的小。** 初版称"约 21k 行 bash 策略"，实测仅 6.6k 行（高估约 3 倍，见 §1.3）。这削弱了"bash 策略是最大单一风险块"的判断，但不改变其性质：**零测试 + 安全边界**，仍需逐个对照测试，不能靠类型系统保证。

**(2) 三处模型 JS 执行沙箱：**

| 沙箱             | 实现                                              | Go 对应                           | Rust 对应         |
| ---------------- | ------------------------------------------------- | --------------------------------- | ----------------- |
| `node_repl`      | `node:vm` + meriyah AST 插桩                      | goja / QuickJS                    | rquickjs / Boa    |
| codemode         | QuickJS-**WASM**（`quickjs-wasi`）+ worker_thread | **wazero**（可直接跑同一份 WASM） | wasmtime          |
| dynamic workflow | 裸 `vm` realm，只注入 `__host`                    | wazero 或子进程                   | wasmtime 或子进程 |

⚠️ **必须纠正一个常见误解：`node:vm` 不是安全边界，goja 和 rquickjs 也不是。** 三者都是进程内引擎、共享宿主地址空间。这里**不构成 Go vs Rust 的差异**——两边打成平手。真隔离两边都得走 WASM 或子进程。

对当前项目而言，**大多数场景只需要"能跑 + 能超时 + 能限制能力"（目标 A），不需要抗恶意攻击（目标 B）**。goja 的 `vm.Interrupt()` 是它的一等公民特性，正好对应 `node_repl` 的 5s 同步超时。codemode 那套 **QuickJS-WASM 已经在仓库里了**——Go 用 wazero 加载同一份 `.wasm` 是最省力的迁移路径。

**(3) 运行时动态 `import()` 任意磁盘路径（4 处）：**

```
cli/src/plugin-host-command.ts:49   → import(pathToFileURL(serverPath))   // 加载插件 .js
cli/src/dwf-child-command.ts:54     → import(pathToFileURL(entryPath))    // 加载生成的 workflow .mjs
cli/src/tui-runtime-loader.ts:43    → import(pathToFileURL(runtimeDir/...))
core/src/repl/node-repl-session.ts  → importModule() 由模型控制
```

**Go/Rust 二进制无法 `import()` 一个 `.js` 文件。** 插件系统要么改成 WASM 插件、要么只保留 MCP 协议形态——**这是一个产品形态变更，不是移植工作**。如果用户生态里有大量自定义 JS 插件，这是最需要先确认的业务约束。

**(4) AI SDK 流式层 12,327 行**（`adapters/src/model/`，`runner-stream.ts` 单文件 1,655 行）深度绑定 `ai@6.0.193` 的类型与 async iterator 语义，且同时支持 Anthropic / OpenAI / OpenAI-compatible / GLM 等多家 provider。Go/Rust 都没有对等的多 provider 官方 SDK 组合——**这一层必须从零写 provider 归一化层**。

### 第 6 层：桌面宿主与 UI

**桌面 main / preload / renderer 不可重写，也不该重写。** 它们是 Electron API（窗口、托盘、`electron-updater`、`node-pty` 原生 addon、内嵌 `WebContentsView` + CDP 浏览器 4,639 行）。这层继续留在 Electron/Node。

**UI 29 万行必须永久留在 TypeScript。** 实测：622 个 `.tsx` 中 **333 个（54%）完全不 import 任何 `@nex/*`**，是纯 DOM/布局/动画/富文本（Lexical、xterm、canvas、virtual list）。这部分在任何语言里都不可能变成后端。

好消息是 **UI 与后端之间已有干净的接缝**：`packages/ui/src/hooks/useServices.tsx` 暴露 `IServiceAccessor`（39 个 service getter），传输层由 `packages/client/src/remoteServiceAccess.ts` 在 `Root.tsx:93` 注入。**重写后端只需重新实现这些 channel，UI 代码不动。**

⚠️ **接缝有 8 个漏洞**：这些文件绕过 `useServices` 直接 import `@nex/rpc`（`useTerminalService.ts`、`TerminalSession.tsx`、`useGitAutoRefresh.ts`、`attachmentUploadTransaction.ts`、`useFileWatcherService.ts`、`usePptxFileWatch.ts`、`useWorkspaceServices.tsx`、`workspace-file-tree/types.ts`）。**它们是迁移时最先会崩的地方。**

`packages/shared` 同样分裂：**约 455 个运行时符号被 UI 直接执行**（其中 82% 是常量表，真正的函数约 77 个），加上全部 schema。这些**必须继续留在 TS**。

### 可行性汇总

| 层              |    行数 | 判定        | 主要障碍                                                  |
| --------------- | ------: | ----------- | --------------------------------------------------------- |
| 0. 原语         |       — | ✅ 可行     | Go 的 RE2 缺口已验证不影响当前代码                        |
| 1. 持久化       |   9,014 | ✅ 可行     | 必须纯 Go/Rust 驱动以保住零 cgo                           |
| 2. RPC          |   3,137 | ✅ 可行     | 自定义 VQL 二进制 wire format                             |
| 3. 协议 schema  |  14,598 | ⚠️ 高成本   | 4,259 个 zod + `.strict()` 语义 + **UI 侧必须留 TS 副本** |
| 4. 服务         |  87,836 | ✅ 可行     | 薄门面占比需先统计                                        |
| 5. Agent 内核   |  98,740 | ⚠️ 最高成本 | 122 个 JS 工具闭包 / 脚本动态加载 / AI SDK 流式        |
| 6. Desktop + UI | 340,744 | ❌ 不重写   | Electron API + 浏览器运行时                               |

**结论：可行性成立，但不是"移植"，是"重写"。** 第 0–2 层（约 16k 行）是机械工作，可以高置信度完成。**第 3 层和第 5 层合计 113k 行是全部风险所在**，且第 5 层有三项无法靠编码消解：工具逻辑要重写、JS 插件生态要么改形态要么嵌入 JS 引擎、AI 流式层要从零建。

---

## 第三部分：性能实测

### 3.1 方法

在 **macOS arm64 本机**（Go 1.27.1 / Rust 1.99.0 / Node v26.10.0）跑等价负载：200 条 agent 消息结构（每条约 1KB，含 role/content/toolCalls/ts），全部归一化到"200 次迭代"为比较单位，取 3 次运行。覆盖四个 agent 真实热点：消息序列化、反序列化、bash 输出行扫描、内容过滤正则。

### 3.2 JSON 序列化 / 反序列化（毫秒，越小越好）

|                   |       Node V8 | Go `encoding/json` | Go `segmentio/encoding` | Rust `serde_json` |
| ----------------- | ------------: | -----------------: | ----------------------: | ----------------: |
| 序列化 200 条消息 |       **6.5** |              30–93 |           **11.5–31.8** |     **11.9–31.6** |
| 反序列化          | **20.0–20.4** |             52–111 |           **14.4–27.4** |     **11.2–24.6** |

**⚠️ 这是本次实测最反直觉、也最重要的发现：V8 的原生 JSON 在这个负载上最快，Rust 接近持平，而 Go 的标准库明显落后。**

三点必须说明，避免误读：

1. **Node 6.5ms 是在为该负载高度特化的 V8 上测的**，Go/Rust 的数字受各自版本 JIT 预热影响，波动区间很大。
2. **Go 用 `segmentio/encoding` 后从 30–93ms 降到 11.5–31.8ms，接近 Rust**——说明差距主要来自 `encoding/json` 的实现（反射 + 排序 map key），不是 Go 语言本身。**如果真要写 Go，必须用第三方 JSON 库，不能用标准库。**
3. **我测 simd-json 时它反而更慢**（34–60ms），因为我的基准里多了一次 `to_value` 转换，把它的收益吃掉了。**这个数据不足以判断 simd-json 的真实性能，我不据此下任何结论**——需要用"预编译 Schema + 零拷贝"的正确用法重测。

### 3.3 字符串扫描与匹配（毫秒）

|                                  |       Node V8 |            Go |          Rust |
| -------------------------------- | ------------: | ------------: | ------------: |
| 10 万行 `includes("error")` 扫描 |       2.9–7.1 | **1.08–1.29** | **0.46–0.81** |
| 10 万次子串匹配                  | **0.98–1.66** |       2.6–5.0 | **0.39–0.80** |

Rust 在字符串/正则上是**最快且稳定**的（1.5–4 倍于 Node）。Go 的 `strings.Count` 在这个场景反而不如 V8——因为 V8 的字符串内建优化很激进。

### 3.4 冷启动与产物体积

|          |  空程序启动 | 基准二进制 | 说明                                          |
| -------- | ----------: | ---------: | --------------------------------------------- |
| **Node** | **80.4 ms** |          — | `node -e ''`，含运行时初始化                  |
| **Go**   |  **6.0 ms** |    2.51 MB | `CGO_ENABLED=0 -ldflags="-s -w"`              |
| **Rust** |  **5.7 ms** | **385 KB** | `lto + codegen-units=1 + strip + panic=abort` |

**冷启动 13 倍差距（80ms → 6ms），Rust 二进制比 Go 小 6.5 倍。**

这一项对 Nex 有**具体、可感知的产品价值**，而不只是跑分：

- 当前桌面打包里 agent 是 `nex.cjs`（16MB JS），靠 Electron 自带的 Node 跑；远程部署是 230MB 的 SEA 二进制。
- **`nex-server` 的 SEA 产物实测 241MB**（blob 122MB + 基础 Node 二进制）。**换成 Go/Rust 单二进制，这个数字会掉到十几 MB 量级**——远程部署从"下载 230MB"变成"下载 20MB"。
- 冷启动 80ms → 6ms 对 CLI 工具是肉眼可见的差别。

### 3.5 这次实测里"用户能感觉到"的与"感觉不到"的

**能感觉到：**

- 冷启动 13 倍（桌面 agent 拉起、CLI 启动、远程 server ready）
- 安装包/下载体积（230MB SEA → 十几 MB）
- 常驻内存（无 GC 抖动，Rust 尤甚）

**感觉不到：**

- JSON 序列化的 6.5ms vs 11.5ms——在一个 200 消息的批次上是 5ms 差异，而用户实际瓶颈是**等 LLM 返回 token**（百毫秒到秒级）。**这个差异被模型延迟完全淹没。**
- 上千并发流的 goroutine vs tokio 差异——生态调研明确指出"两者都能轻松扛住几百到几千并发流，这不是选型瓶颈"。

**结论：性能不是选 Go 还是 Rust 的理由，甚至不是选"重写"的理由。** 真正能兑现的收益是**冷启动、体积、分发**这三项——而这三项 **Go 和 Rust 都远好于 Node，差别只在 Go 简单一些、Rust 小一些。**

---

## 第四部分：Go vs Rust 生态对比

数据来自 `docs/research/agent-backend-go-rust-ecosystem-2026-10.md`（当日抓取 GitHub API / crates.io / 官方文档核对）。

### 4.1 决定性差异

| 能力                 | Go                                          | Rust                                           | 对 Nex 的影响                  |
| -------------------- | ------------------------------------------- | ---------------------------------------------- | ------------------------------ |
| **MCP SDK**          | ✅ **官方** `go-sdk` v1.8.0，5.2k★          | ✅ **官方** `rmcp` v3.5.1，4.0k★               | 平手，都达标                   |
| **Anthropic SDK**    | ✅ **官方** v1.79.1                         | ❌ **无官方**                                  | **Go 明确胜**                  |
| **OpenAI SDK**       | ✅ **官方** `openai-go` v3.73.0             | ❌ 无官方                                      | **Go 明确胜**                  |
| **多 provider 抽象** | ⚠️ 无现成，需自建 adapter                   | ✅ `rig` v0.44.0，8.8k★，20+ provider          | **Rust 胜**                    |
| **浏览器 CDP**       | ✅ `chromedp` 13.3k★ + `rod` 7.1k★          | ⚠️ `chromiumoxide` 1.4k★，**半年未推送**       | **Go 明显胜（5–10 倍成熟度）** |
| **终端模拟（VT）**   | ❌ 无成熟库                                 | ✅ `vte` + `ratatui`（alacritty/wezterm 背书） | **Rust 胜**                    |
| **PTY 转发**         | ✅ `creack/pty` 2.1k★                       | ✅ `portable-pty`（19M 下载，但 1.5 年未发版） | 平手                           |
| **JS 引擎**          | ✅ `goja` 7.1k★，`Interrupt` 超时是一等特性 | ✅ `rquickjs`（有 C 依赖）/ `Boa`（纯 Rust）   | 平手                           |
| **交叉编译零 cgo**   | ✅ 一条命令出三平台静态二进制               | ❌ `rusqlite`/`rquickjs` 均需 C 交叉工具链     | **Go 明显胜**                  |
| **正则**             | ❌ RE2 无反向引用/惰性量词                  | ✅ 有                                          | **Rust 胜**                    |

### 4.2 Nex 特有的两个判断

**(1) 终端那条"Rust 胜"对 Nex 大概率不适用。** Nex 的终端是 **PTY 字节流转发 + 前端 xterm.js 渲染**（`services/src/terminal` + `@xterm/*`），不是后端复刻 xterm 屏幕缓冲。如果是这种形态，Go 的 `creack/pty` 完全够用，**Go 侧无缺口**。**这一项要先确认需求形态再决策。**

**(2) Rust 的 `rig` 解决了 Go 最痛的一层，但要谨慎。** Nex 是多 provider（Anthropic / OpenAI / OpenAI-compatible / GLM / models.dev 聚合）。Rust 有现成的 20+ provider 统一抽象；Go 得自己写 adapter。**但注意两点：**

- `rig` 仍是 **0.x**，且生态调研**未能验证**它是否原生支持 GLM/Zhipu。
- Go 侧底下垫的是 **Anthropic 和 OpenAI 两家官方 SDK**，自己写薄 adapter 的风险，比"依赖一个 0.x 的社区统一库"更低。

### 4.3 一条对两边都成立的关键事实

**没有任何进程内 JS 引擎是安全边界。** `node:vm`、goja、rquickjs、Boa 都是进程内、共享宿主地址空间。这一项**不构成选 Go 还是 Rust 的理由**。真隔离两边都得走 WASM（Go: wazero；Rust: wasmtime）或 OS 级子进程。

好消息：**Nex 的 codemode 已经在用 QuickJS-WASM**，Go 侧用 wazero 加载同一份 `.wasm` 是最短路径。

---

## 第五部分：结论与建议

### 5.1 一句话结论

**把 agent 核心和 UI 无关的部分换成 Go 或 Rust，在技术上可行；但它是一次 11 万行级的重写而非移植，且性能收益集中在冷启动/体积/分发三项，而非吞吐。生态上 Go 更适配 Nex 的实际形态（官方 SDK 全覆盖、零 cgo 交叉编译、浏览器自动化成熟 5–10 倍），Rust 在正则和终端模拟上更强但 Nex 大概率用不到。**

### 5.2 动手前必须先确认的五个问题

这五个问题的答案会实质改变结论，建议在投入任何编码之前先查清：

1. ~~**工具策略里有没有依赖反向引用的正则？**~~ → **已验证：没有**（0 命中）。**但惰性量词有 15 处**，其中 `webfetch-content.ts` 在 Go 重写路径上——见第 0 层。Go 可行，需逐处改写。
2. ~~**用户生态里有多少自定义 JS 插件？**~~ → **已核实：不存在"大量用户 JS 插件"这个障碍。** 详见下方「插件生态的真实形态」。
3. **JS 沙箱要防的是 bug 还是攻击？** → 前者 goja/rquickjs 够用；后者两边都得自建 WASM 隔离层。**且需先决定 `node_repl` 的 `importModule` 是否保留**（模型脚本要不要能 `await import()` 任意模块）——这是"要不要保留完整 JS 运行时"的真正开关，比"防 bug 还是防攻击"更前置。
4. **协议要不要新旧实现并存过渡？** → 要的话，`packages/rpc` 的自定义 VQL 二进制 wire format 必须逐字节复刻；不要的话可以直接简化成 JSON。
5. **终端是"后端 PTY 转发 + 前端 xterm 渲染"，还是"后端复刻终端模拟器"？** → 前者 Go 无缺口；后者 Rust 明显更强。这是决定 Go/Rust 的最后一个未决问题。

#### 插件生态的真实形态（问题 2 的答案）

初版报告假设"用户生态里有大量自定义 JS 插件"，并据此担心必须保留 JS 运行时。**核实源码后这个前提不成立**——插件分四类，只有最后一类需要决策：

| 类别 | 实际形态 | 对 Go 迁移的影响 |
| ---- | -------- | ---------------- |
| **第三方插件的 MCP server** | `McpServerConfig` 是 `{ command, args, env }` 的**进程 spawn** 形态（`packages/shared/src/mcp.ts:24`），由 `ProcessTreeStdioClientTransport`（`adapters/src/mcp/stdio-transport.ts:45`）在**独立子进程**跑 JSON-RPC over stdio | ✅ **无影响**。MCP 本就是跨进程协议，Go 侧用官方 go-sdk 即可，第三方生态照常工作 |
| **官方插件 `__nex-plugin-host`** | `plugin-host-command.ts:49` 的 `import()` 只服务 `nex-plugins-official` 市场——`officialPluginHostPrefixArgs()`（`official-plugin-runtime.ts:95`）仅在该文件内被调用，用来把 official 插件的 `command` 重写成 `process.execPath __nex-plugin-host <server.js>` | ✅ 随 Nex 官方代码一起重写。⚠️ 但该处对 CUA broker bearer token 做了 plugin id + 凭据组 + canonical socket 三重校验才允许 import，这段安全逻辑需在 Go 侧重新实现 |
| **dynamic workflow 的 `.mjs`** | `dwf-child-command.ts:54` 的入口文件是 **DSL 编译器自己生成的产物**（注释明示"入口文件自己判定不是进程入口…不会自启"），跑在沙箱子进程里，只注入 `vm: { createContext, runInContext }` | ✅ 可换 wazero + QuickJS-WASI（仓库 codemode 已有 QuickJS-WASM 可复用），不需要 `import()` 磁盘 `.js` |
| **TUI 自举 / `node_repl`** | `tui-runtime-loader.ts:43` 加载的是 Nex 自身的 `@nex/tui`（SEA 态解包后 import）；`node-repl-session.ts:459` 的 `importModule` 是**模型生成的脚本**在 repl 里 `await import()` | ⚠️ TUI 与 agent 迁移无关。**`importModule` 是唯一需要拍板的**——见问题 3 |

**结论**：插件系统是**声明式**的（`plugin.json` 提供 skills / commands / mcpServers / hooks / subagents），主体走进程级 MCP，天然跨语言。**真正需要决策的不是"用户生态"，而是 `node_repl` 要不要让模型脚本保留 `import()` 任意模块的能力**——那个才决定"是否必须保留完整 JS 运行时"。

### 5.3 建议的切入顺序（若决定推进）

按"风险从高到低"倒序推进——**先做能证伪的，再做能交付的**：

1. **先做协议 schema 的代码生成**（第 3 层）。它是所有层的共同前置，且能立刻验证 zod→Go/Rust 的语义等价性是否成立（`.strict()` / `.superRefine()` / `discriminatedUnion`）。**这一步失败，后面全部白做。** 关键抓手是 `zod-to-json-schema` 已在依赖里，生成链路现成。
2. **再做持久化与 RPC**（第 1–2 层，12k 行）。机械工作，高置信度，且能独立交付——新后端至少要能读写同一个 `db.sqlite`。
3. **然后是模型流式层**（AI SDK 12,327 行的替代品）。这是第 5 层里唯一"有明确对标物"的部分，官方 SDK 能兜住 60%。
4. **最后才是 122 个工具闭包**（22,634 行）。这是纯业务逻辑重写，也是最容易在发现设计缺陷时需要回头改协议的阶段——所以要放在协议稳定之后。

**不建议**在早期就动 UI 或桌面宿主：第 6 层不可重写，且 UI 侧的 TS schema 副本在过渡期必须保留。

### 5.4 必须同时承认的三个风险

- **协议双份维护**：迁移期同一份 schema 在 TS（UI 侧验证用）和新语言（服务端）各存一份。这是本次重写**唯一无法消除**的长期负担，需要明确的 owner 和同步机制。
- **122 个工具闭包是业务逻辑，不是接口**。它们会持续演进——重写期间工具行为会与 TS 原版产生漂移，而 bash 权限策略的错误直接影响安全边界。这部分需要逐个对照测试，不能靠类型系统保证。
- **性能不是理由**。如果把重写的收益论证建立在"更快"上，实测数据不支持——JSON 那 5ms 差异被模型延迟完全淹没。真正站得住的收益是**冷启动 13 倍、安装体积 230MB→十几 MB、内存确定性**，外加"官方 SDK 齐备、交叉编译干净"的工程收益。

---

## 第六部分：测试覆盖实测 —— 重写的真实成本项

> 这一部分在初版报告之外补充。原因是：**测试覆盖直接决定重写后能否验证行为一致**，它不是附属信息，而是重写可行性的核心变量。

### 6.1 总体覆盖水平：约 0.6%

全仓 **45 个测试文件、4,874 行测试代码**（用 `node:test` + `node:assert/strict`，无 Jest/Vitest）。

| 包                                                   | 源码文件 | 测试文件 | 测试代码行数 | 覆盖率量级   |
| ---------------------------------------------------- | -------: | -------: | -----------: | ------------ |
| `packages/ui`                                        |    1,367 |       12 |       ~1,212 | 纯函数为主   |
| `packages/services`                                  |      283 |       14 |       ~1,592 | **稀疏**     |
| `apps/nex-cli/packages/core`                         |      520 |        7 |      **714** | **极稀疏**   |
| `packages/desktop`                                   |      196 |        3 |          421 | 局部         |
| `apps/nex-cli/packages/bootstrap`                    |      228 |        4 |          286 | 极稀疏       |
| `apps/nex-cli/packages/adapters`                     |      199 |        1 |           51 | **近乎为零** |
| `packages/server`                                    |       51 |        1 |          243 | 集中在鉴权   |
| `packages/web`                                       |       11 |        2 |          302 | 尚可         |
| **`packages/shared`（协议 14.6k 行）**               |  **231** |    **0** |        **0** | **零**       |
| **`packages/rpc`（3.1k 行）**                        |   **15** |    **0** |        **0** | **零**       |
| **`packages/nex-server-cli`**                        |       42 |        0 |            0 | 零           |
| **`contracts` / `dynamic-workflow` / `cli` / `tui`** |      283 |    **0** |        **0** | **零**       |

**换算成行覆盖率**：`core` 是 **98,740 行源码对 714 行测试 ≈ 0.7%**；`adapters` 是 **50,955 行对 51 行 ≈ 0.1%**。

### 6.2 三个"零覆盖"正好落在重写最贵的三层上

这是最值得强调的发现——**测试缺口的分布与重写风险的分布高度重合**：

| 层                                   |     代码量 |     测试文件 | 重写风险                               | 覆盖是否匹配      |
| ------------------------------------ | ---------: | -----------: | -------------------------------------- | ----------------- |
| **协议 schema**（nex-protocol + v4） |  14,598 行 |        **0** | 极高（4,259 个 zod、`.strict()` 语义） | ❌ **完全不匹配** |
| **RPC 框架**                         |   3,137 行 |        **0** | 中（自定义 VQL 二进制编码）            | ❌ **完全不匹配** |
| **SQLite 存储**（22 迁移 / 25 表）   |   9,014 行 |        **0** | 中高（跨实现二进制兼容）               | ❌ **完全不匹配** |
| **bash 权限策略族**                  | ~6,600 行 |        **0** | **极高（安全边界）**                   | ❌ **完全不匹配** |
| **122 个工具 handler**               |  22,634 行 | 0 个直接测试 | 极高（业务规则）                       | ❌ **完全不匹配** |
| **AI 流式层**                        |  12,327 行 |            0 | 高（provider 归一化）                  | ❌ 不匹配         |

**结论：风险最高、行为最复杂、最需要回归保护的代码，恰好是测试覆盖为零的代码。**

尤其 **bash 策略族（6.6k 行，决定什么命令需要用户批准、什么被允许）零测试**——这既是重写时最难保证行为一致的模块，也是出错后果最严重的模块（它是安全边界）。

### 6.3 测试质量本身是正面的

需要说清楚的是：**现有测试质量不差，只是不多。** 我实际跑了 `core` 的测试：

```
ℹ tests 35   ℹ pass 35   ℹ fail 0   ℹ duration_ms 1847
```

它们是真测试而非空壳：

- **用了真实依赖**——593 行测试代码里包含真实的 `spawn` / `execFile` / `createServer`，比如 `tool-search-mcp.test.ts` 会拉起真实 stdio MCP server 注册工具；`nodePtyHalfInitCache.test.ts` 复现的是真实踩过的 SEA 半初始化 bug。
- **命名描述真实行为**——`petStateMachine.test.ts`（空会话列表 → idle）、`nested-runner.test.ts`（脚本拿到原始输出，子任务走权限执行器）、`codemode-sandbox.test.ts`（脚本调注入工具但拿不到 `process`/`require`/`fetch`）。
- 724 处 `assert` 调用，是对具体值做断言而非快照比对。

**所以问题不是"测试写得烂"，而是"测试太少，而且少在最要命的地方"。**

### 6.4 对重写可行性的实质影响 —— 需要下调此前的乐观程度

我上一轮建议"先做协议 schema 代码生成，能立刻证伪或证实语义等价"。**这条建议依然成立，而且优先级更高了**——因为协议层零测试，它同时是最高风险和最无保护。

但更重要的推论是：

> **在当前测试覆盖下，Go/Rust 重写的失败模式不是"编译不过"，而是"编译通过、测试通过、上线后行为悄悄变了"。**

具体到各模块：

1. **协议层**：零测试意味着没有 golden fixture。`.strict()` 字段白名单、`z.discriminatedUnion` 的分支判定、`superRefine` 的跨字段约束——**重写后没有任何东西能告诉你语义漂移了**。必须先补一套协议 round-trip / golden fixture 测试，否则这一层无法安全重写。
2. **bash 策略（安全边界）**：6.6k 行零测试。重写它等于**重写一个没有任何回归保护的安全策略引擎**。**行数比初版估计小 3 倍，但仍是一项独立的安全关键工作**——不能因体量下降就降级处理。
3. **存储层**：零测试，但 22 个迁移要跨语言二进制兼容。**这反而是风险最低的一项**——SQL 语义有标准，行为可比对。
4. **工具 handler**：零直接测试。现有测试测的是"工具如何被暴露和调度"（tool-search、mcp-exposure、nested-runner），**不是"工具本身做什么"**（read/write/edit/bash 的正确性）。

### 6.5 修正后的建议

把"先补测试"提到与"选语言"同等的位置：

**建议的推进顺序调整为：**

1. **给协议层 + bash 策略 + 工具 handler 补回归测试**（黄金 fixture / 表驱动测试）。这本身是有价值的工程改进，**即使不重写也该做**——它们现在是完全裸奔的。
2. 再做协议 schema 的代码生成，用第 1 步的 fixture 验证语义等价。
3. 持久化 + RPC（有标准可比对，风险最低）。
4. 最后是工具逻辑与 AI 流式层。

**如果不愿补测试**，那么可行的重写范围要大幅收窄到：**只重写持久化层 + RPC + server 端外壳**（约 25k 行，即第 1–2 层 + 部分第 4 层），把 `core` / `tools` / 协议全部留在 TypeScript。这个范围收益有限（拿不到冷启动和体积的大部分好处），但风险可控。

### 6.6 一个附带发现

`packages/ui` 有 1,367 个源码文件却有 12 个测试文件，但这些测试质量不错（`petPhysics`、`localeMessagesFormat` 等纯函数）。UI 层测试少**不是问题**——UI 本来就不重写，且纯函数逻辑易验证。**测试资源的分布本身是合理的，问题在于后端核心逻辑完全没有保护。**
