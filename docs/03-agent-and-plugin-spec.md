# Nex-v3 智能体内核与插件扩展系统规范 (Agent Core & Plugin System Spec)

- **状态**: Draft / Approved Core Direction
- **更新日期**: 2026-11-18
- **设计目标**: 在纯 Go（零 CGO、无 Node 运行时依赖）约束下，完整复刻 `pi-agent` 的极简事件流内核与高可扩展插件切面（Extensions），并适配「中心 Server 下发管控 + 无头跨平台 Runner + 远端多端 UI」架构。

---

## 1. 扩展能力职责边界：Plugin vs MCP vs Skills

在 Nex-v3 中，三类扩展机制各司其职、互不替代，全部支持在 `nex-server` 集中管理并同步至目标 `nex-runner`：

| 维度 | **Plugin / Extension（插件）** | **MCP（模型上下文协议）** | **Skill（技能包）** |
| :--- | :--- | :--- | :--- |
| **核心定位** | **切入 Agent Loop 心脏的拦截器与编排器** | 标准化外部系统/环境工具提供者 | 按需注入的领域知识手册与辅助脚本 |
| **能否拦截/修改上下文与工具调用** | **能**（Pre/Post Hooks、动态改写 Context、接管压缩） | 否（无感知黑盒工具调用） | 否（仅在模型主动读取时加载文本） |
| **能否注册 `/slash` 命令与交互 UI** | **能**（注册 `/command`、通过 `ctx.ui` 唤起远端弹窗/状态徽章） | 否 | 可映射为快捷提示词模板，无运行时逻辑 |
| **运行载体** | Go 编译期内置实现，或 **TS 编写 -> Server `esbuild` 转译 -> Runner `goja` 内存沙箱执行** | Server 远程托管（SaaS 类）或 Runner 本地 `stdio` 子进程（宿主机类） | 同步缓存在 `~/.nex-runner/skills/` 下的 `SKILL.md` + 脚本目录 |

---

## 2. Go 智能体内核设计 (`pkg/ai` & `pkg/agent`)

### 2.1 统一消息与内容块模型 (`pkg/ai`)

屏蔽 **OpenAI / Codex Responses API (`/v1/responses`)** 与 **Anthropic Messages API (`/v1/messages`)** 的底层流式协议差异（其他协议留作后续扩展），在 Runner 内核中使用强类型统一结构：

- **角色枚举 (`Role`)**：`user` | `assistant` | `tool_result`（System Prompt / Instructions 作为独立请求顶层字段动态组装，便于插件每轮按需注入与标记缓存断点）。
- **多态内容块 (`ContentBlock`)**：
  - `TextBlock`: 纯文本内容。
  - `ThinkingBlock`: 模型推理思考过程 + 厂商原样回传凭证（如 Anthropic 的 `signature` 或 Codex Responses 的 `reasoning` item ID / `encrypted_content`，保障无状态多轮工具调用不断链）。
  - `ImageBlock`: MIME 类型 + Base64 / 本地资产引用。
  - `ToolCallBlock`: `ID`、`Name`、`Arguments`（结构化 JSON）。
- **自定义会话条目 (`CustomEntry`)**：借鉴 `pi-agent` 会话日志设计，允许插件向会话流写入对 LLM 不可见（或按需转为上下文）的结构化状态条目，用于跨重启恢复插件内部状态。

### 2.2 Agent Loop 双队列状态机 (`pkg/agent`)

每个活跃 Session 在 `nex-runner` 中对应一个受 `context.Context` 管控的 Loop 实例：

1. **运行期控制队列**：
   - **`Steer(msg)`（即时转向队列）**：当大模型返回一批并行/串行工具调用时，每执行完一个工具都会检查 `steer` 队列。一旦非空，**立即跳过当前批次剩余未执行的工具**（填入 skipped tool result 以保持协议合法），将用户的插话注入上下文开启新一轮推理。
   - **`FollowUp(msg)`（追加队列）**：当模型不再发起工具调用、准备结束当前回答（`agent_end` 前）时检查该队列；若非空，自动将其作为新用户消息继续下一轮循环。
2. **标准事件流枚举 (`AgentEvent`)**：
   - 会话级：`agent_start`、`agent_end`、`compaction_start`、`compaction_end`
   - 轮次级：`turn_start`、`turn_end`
   - 消息流：`message_start`、`message_update`（细分 `text_delta`、`thinking_delta`、`toolcall_delta`）、`message_end`
   - 工具级：`tool_execution_start`、`tool_execution_update`（实时回传长耗时 `bash` 输出流）、`tool_execution_end`
   - 交互级：`approval_requested`、`approval_resolved`、`ui_prompt_requested`、`ui_prompt_resolved`、`ui_status_updated`

### 2.3 内置跨平台基础工具集 (`pkg/harness/tools`)

保持 `pi-agent` 精干的高正交基础工具设计（其余高阶能力通过插件或 MCP 挂载）：
1. **`read`**：支持文本行号切片读取与常见图片格式多模态读取。
2. **`write`**：原子创建或覆写文件（自动创建父目录）。
3. **`edit`**：精确片段替换 + **跨平台容错归一匹配**（自动兼容 Windows `CRLF` 与 Unix `LF` 差异、UTF-8 BOM 头剥离还原、首尾空白微调匹配），并在事件流中附带结构化 Unified Diff 供客户端直接渲染对比视图。
4. **`bash` / `shell`**：流式输出采集、超时控制、输出尾部截断落盘保护，以及跨平台进程树彻底销毁（Unix `-pgid` / Windows `Job Object`）。
5. **`grep` & `glob`**：纯 Go 实现的高速目录遍历、`.gitignore` 规则过滤与正则检索，无需在 Windows/Linux 目标机额外预装 `ripgrep`。

---

## 3. 双层插件扩展架构 (`pkg/extension`)

为兼顾**核心性能**与**Server 中心化免编译动态下发**，插件系统采用同构的双层设计：底层定义统一的 Go `Extension` 接口，上层内置基于纯 Go JS 虚拟机的动态插件宿主。

### 3.1 第一层：Go 原生 `Extension` 接口（内置插件）

官方高频基础能力（如权限策略守卫、默认上下文自动压缩、Plan/Todo 状态追踪）直接实现 Go 接口编译进二进制：

```go
type Extension interface {
    ID() string
    Register(reg Registry) error
}

type Registry interface {
    OnBeforeAgentStart(fn BeforeAgentStartHook)
    OnContextBuild(fn ContextBuildHook)
    OnToolCall(fn ToolCallHook)       // 可修改入参，或直接拦截阻断
    OnToolResult(fn ToolResultHook)   // 可修改/截断结果，或追加诊断信息（如自动 Lint）
    OnCompact(fn CompactHook)         // 自定义上下文摘要压缩器
    RegisterTool(tool DynamicTool)
    RegisterCommand(cmd SlashCommand)
}
```

### 3.2 第二层：TypeScript 动态插件引擎（`esbuild` + `goja`，零 CGO）

延续「全项目仅使用 Go + TypeScript」的原则，自定义业务插件使用 **TypeScript** 编写，完整保留 `pi-agent` 插件的开发体感：

1. **编写期（`@nex/plugin-sdk` `.d.ts` 类型支持）**：
   插件作者面向强类型的 `ExtensionAPI` 编写 `.ts` 文件，心智模型与 `pi-agent` 完全一致：
   ```ts
   import type { ExtensionAPI } from "@nex/plugin-sdk";

   export default function (nex: ExtensionAPI) {
     // 1. 注册斜杠命令（自动同步至 Web/Desktop/Mobile 输入框补全列表）
     nex.registerCommand("deploy-check", {
       description: "检查当前分支构建状态并确认发布目标",
       async handler(args, ctx) {
         if (ctx.ui.hasUI) {
           const env = await ctx.ui.select("选择目标环境", ["staging", "production"]);
           if (!env) return;
           ctx.sendUserMessage(`请对当前工作区执行 ${env} 环境发布前检查`);
         }
       },
     });

     // 2. 拦截工具调用（安全防护示例）
     nex.on("tool_call", async (event, ctx) => {
       if (event.toolName === "bash" && String(event.input.command).includes("rm -rf /")) {
         return { block: true, reason: "禁止执行高危根目录删除指令" };
       }
     });

     // 3. 增强工具结果（改完 Go 代码自动格式化检查示例）
     nex.on("tool_result", async (event, ctx) => {
       if (event.toolName === "edit" && String(event.input.file_path).endsWith(".go")) {
         const check = await ctx.exec("gofmt", ["-e", String(event.input.file_path)]);
         if (check.exitCode !== 0) {
           return { appendText: `\n[auto-gofmt] 检测到语法错误:\n${check.stderr}` };
         }
       }
     });
   }
   ```

2. **转译与分发期（`nex-server` 内置纯 Go `esbuild`）**：
   - `evanw/esbuild` 本身即为纯 Go 库。管理员或用户在 `nex-server` 上传/更新 `.ts` 插件源码时，`nex-server` 直接在内存中调用 `esbuild/pkg/api` 毫秒级将其转译并打包为自包含的单文件 `.js` Bundle（同时计算内容 SHA-256）。
   - Server 下发配置变更通知，目标 Runner 拉取 Bundle 并缓存在本地 `~/.nex-runner/plugins/<plugin-id>/<sha256>.js`。
   - **离线可用**：由于已缓存编译好的 `.js` 产物，Runner 脱离 Server 执行离线 Cron 定时任务时，所有已启用的插件依然 100% 正常工作。

3. **运行期（`nex-runner` 内置纯 Go `dop251/goja`）**：
   - `goja` 为 100% 纯 Go 实现的 ECMAScript 虚拟机（零 CGO，跨 Windows/Linux/macOS 静态编译无阻碍，体积开销仅 ~2MB）。
   - **默认内存隔离与显式能力注入 (`PluginContext`)**：`goja` 实例默认无任何操作系统文件或网络访问权限；Runner 宿主通过受控桥接对象向插件暴露确定性 API：
     - `ctx.exec(cmd, args, opts)`：受当前 Session `context` 超时与取消管控的子进程调用。
     - `ctx.fs`：受工作区策略约束的轻量文件读写探测。
     - `ctx.http.fetch`：受控 HTTP 请求。
     - `ctx.state.get / appendEntry`：读写与当前 Session 绑定的持久化插件状态。
     - `ctx.ui`：远程声明式 UI 交互桥（见下节）。
   - **并发安全**：每个 Session 拥有独立的插件 VM 实例（或串行事件循环调度器），彻底避免 Go 多协程直接并发操作同一个 `goja.Runtime` 引发竞态。

---

## 4. 无头 Runner 的灵魂补丁：远程声明式 `ctx.ui` 交互协议

在原版单机 `pi-agent` 中，插件通过 `ctx.ui.*` 直接操控终端 TUI；而在 Nex-v3 中，Runner 是运行在远端 Linux/Windows 上的无头守护进程。

通过将 `ctx.ui` 设计为**基于 Session 事件流的远程异步交互原语（Remote UI RPC）**，我们让运行在远端服务器上的插件能够直接在用户的手机或桌面客户端上弹出交互控件：

| 插件调用的 `ctx.ui` 方法 | 在线会话行为 (`ctx.ui.hasUI === true`) | 离线 / Cron 自治模式 (`ctx.ui.hasUI === false`) |
| :--- | :--- | :--- |
| `ctx.ui.select(title, options)` | Runner 发出 `ui_prompt_requested (kind: select)` 事件并挂起当前 Hook；Web/Desktop/Mobile 渲染选项卡片，用户点选后通过 `ui.resolve` 回传选中值唤醒协程。 | 不挂起，立即返回 `null`（或插件传入的 `defaultValue`），防止无头任务死锁。 |
| `ctx.ui.confirm(title, message)` | 客户端弹出确认对话框，返回 `boolean`。 | 立即返回 `false`（或默认值）。 |
| `ctx.ui.input(title, placeholder)` | 客户端弹出文本输入框（支持密码掩码模式），返回用户输入字符串或 `null`。 | 立即返回 `null`。 |
| `ctx.ui.notify(message, level)` | 非阻塞发出通知事件，客户端展示 Toast 提示。 | 写入本地任务运行日志，不同步弹窗。 |
| `ctx.ui.setStatus(key, badgeText)` | 更新客户端当前会话顶部/底部的插件状态徽章（例如展示当前激活的 `Plan Mode`、Git 分支或代码检查状态）。 | 记录为最新状态快照，待重连后同步展示。 |
| `ctx.ui.setWidget(key, markdownOrSchema)` | 在客户端聊天侧边栏或输入框上方挂载自定义轻量状态面板（如实时更新的 Todo 进度卡片）。 | 状态随 Outbox 事件落盘，客户端回看历史时可正常渲染。 |

---

## 5. 插件治理与作用域（Server 统一管控）

在 `nex-server` 控制台中，插件支持三级挂载作用域，下发到 Runner 本地快照执行：

1. **Global（全局默认插件）**：自动下发并应用于所有接入的 Runner。
2. **Runner-Scoped（节点级插件）**：按操作系统标签或指定机器下发（例如仅在 Windows Runner 上启用 PowerShell 环境增强插件，或仅在构建机启用特定 CI 检查插件）。
3. **Workspace-Scoped（项目工作区插件）**：同时兼容工作区仓库内的 `.nex/plugins/*.ts`（受信任工作区经用户首次审批授权后，由 Runner 本地或请求 Server 转译加载），方便项目团队随代码库共享专属工程化 Hook。
