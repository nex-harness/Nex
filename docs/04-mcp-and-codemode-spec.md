# Nex-v3 MCP 懒加载与 Code Mode 执行规范 (MCP Lazy Loading & Code Mode Spec)

- **状态**: Draft / Approved Core Direction
- **更新日期**: 2026-11-18
- **设计目标**: 完整继承 `pi-agent` 生态中极高 Token 效率的 **MCP 懒加载 + Code Mode（代码编排调用）** 机制，在纯 Go 无 CGO 的 `nex-runner` 中复用 `dop251/goja` 构建安全内存沙箱，无缝打通「Runner 本地节点态 MCP」与「Server 远程中心态 MCP」。

---

## 1. 为什么要「懒加载 + Code Mode」？

传统 Agent 接入 10 个 MCP Server（动辄上百个 Tools）时会同时引爆三个致命问题：
1. **上下文爆炸与注意力稀释**：每轮对话携带数万 Token 的冗长 Tool JSON Schema，不仅高昂烧钱，还会严重干扰大模型对核心 5 个 Coding Tools（`read` / `write` / `edit` / `bash` / `grep`）的选择准确率。
2. **Runner 本地内存膨胀**：如果 Runner 启动时把十几个基于 `npx` (Node) 或 `uvx` (Python) 的本地 `stdio` MCP 子进程全部拉起常驻，几十 MB 的轻量 Go Runner 会瞬间拖着几个 GB 的子进程树。
3. **多步串行与脏数据污染上下文**：例如“查出最近 20 个失败的 CI 构建日志并提取报错原因”，传统 Tool Call 需要来回十几次模型推理，且原始大段 JSON 全部塞进对话历史；而在 **Code Mode** 下，模型只需生成一段 15 行的异步 JS 脚本，在沙箱内 `Promise.all` 并发拉取、用代码正则过滤掉 98% 的噪音，只把最终几十行干净结论 `return` 给上下文。

---

## 2. 双层懒加载机制 (Two-Tier Lazy Loading)

Nex-v3 在 **提示词上下文层** 与 **操作系统进程层** 同时实施懒加载：

### 2.1 第一层：Schema 与上下文懒加载（Prompt Zero-Bloat）

1. **工具栏极简常驻**：发往 LLM 的 `tools` 数组中，**绝不直接注入具体的 MCP 工具定义**；除内置核心工具与激活的会话工具外，所有 MCP 能力统一收敛进单一入口工具：**`codemode`**。
2. **轻量命名空间目录提示**：在 System Prompt 中仅注入几十 Token 的已启用 MCP 命名空间摘要（不含具体工具参数 Schema），例如：
   - `github` (server-hosted, 22 tools): GitHub repositories, issues, PRs, and Actions.
   - `playwright` (runner-local, 14 tools): Headless browser navigation and DOM inspection.
   - `postgres` (runner-local, 5 tools): Read-only schema inspection and SQL query execution.
3. **沙箱内按需检索与自省**：模型在 `codemode` 脚本中通过内置全局函数 `searchTools()` 与 `describeTool()` 从 Runner 本地缓存的目录索引中毫秒级查询所需工具及其精确 JSON Schema，无需预先塞满上下文。

### 2.2 第二层：本地 MCP 子进程懒加载与空闲回收（Process Lazy Spawn & Idle Reap）

针对运行在 `nex-runner` 本机的「节点态 `stdio` MCP」：

1. **元数据快照缓存 (`mcp_catalog_cache`)**：
   - 当 Server 下发新的本地 MCP 配置（或首次添加）时，Runner 在后台短暂握手一次 `tools/list`，将工具名、描述、完整 `inputSchema` 持久化到本地 SQLite 快照表，随即关闭子进程。
   - 因此，执行 `searchTools` 和 `describeTool` **完全基于本地 SQLite 内存/磁盘索引，零子进程启动开销**。
2. **首次调用唤醒 (Spawn-on-First-Call)**：
   - 仅当 `codemode` 脚本真正调用 `tools.<namespace>.<tool>(args)` 时，Runner 的 `MCPPool` 才按需拉起对应的本地 `stdio` 子进程（若已在运行则直接复用连接）。
3. **空闲自动回收 (Idle TTL Reaping)**：
   - 每个本地 MCP 子进程绑定可配置的空闲超时（默认 `idle_ttl = 5m`）。超过窗口期无新调用，Runner 自动优雅关闭该子进程并回收其进程树（Unix 进程组 / Windows Job Object），确保长期后台常驻的 `nex-runner` 始终保持十几 MB 级别的极低内存水位。

---

## 3. 基于 `goja` 的 Code Mode 沙箱设计 (`pkg/harness/codemode`)

直接复用插件系统已引入的纯 Go JS 引擎 **`dop251/goja`**，但采用与受信任插件**严格物理隔离的零特权沙箱配置**。

### 3.1 沙箱安全边界（Zero-Privilege Sandbox vs Plugin VM）

| 能力项 | 插件 VM (`pkg/extension`, 管理员受信任代码) | **Code Mode VM (`codemode`, 大模型动态生成代码)** |
| :--- | :--- | :--- |
| **生命周期** | Session 级常驻 | **单次 Tool Call 级瞬态实例**（跑完即销毁，仅保留轻量键值 `store`/`load` 供同会话脚本暂存小状态） |
| **本地文件 / 子进程 / 任意网络** | 显式注入受控 `ctx.fs` / `ctx.exec` / `ctx.http` | **绝对禁止**（无 `fs`、无 `exec`、无 `fetch`、无 `process`、无环境变量访问） |
| **唯一外部能力** | 全量 `ExtensionAPI` | **仅能调用受权限守卫管控的 `tools.*` 代理与目录查询函数** |
| **CPU 死循环防护** | Session Context 取消 | **硬超时抢占中断**：绑定定时器（默认 30s，可通过首行注释 `@options` 在上限内调整），超时直接调用 `vm.Interrupt("execution timeout")` 毫秒级强退死循环 |
| **输出防爆保护** | 插件自行控制 | **严格输出 Token/字节上限**（如默认最大返回 16KB / ~4000 tokens），超出时截断并提示模型在脚本内做 `.slice()` 或聚合过滤 |

### 3.2 沙箱内置全局 API 设计

模型提交的代码作为顶层 `async` 函数体执行（原生支持 `return`、`await`、`Promise.all`）：

```js
// @options: {"timeout_ms": 45000, "max_output_bytes": 16384}

// 1. 当不确定具体工具名或参数结构时，毫秒级检索本地缓存目录
const found = await searchTools("pull request", { namespace: "github", limit: 5 });
const schema = await describeTool("github.list_pull_requests");

// 2. 并发调用多个 MCP 工具（支持混合调用 Server 中心态 MCP 与 Runner 本地 MCP！）
const [prs, localBranches] = await Promise.all([
  tools.github.list_pull_requests({ owner: "org", repo: "nex", state: "open" }),
  tools.git_local.list_branches({})
]);

// 3. 在沙箱内过滤掉庞大无用的原始 JSON 字段，仅向大模型上下文返回高价值结论
return prs
  .filter(pr => localBranches.includes(pr.head.ref))
  .map(pr => ({ number: pr.number, title: pr.title, branch: pr.head.ref }));
```

**沙箱注入的全局变量清单**：
- `await searchTools(query, { namespace?, limit? })`：支持关键词/分词检索所有当前可用工具（自动合并本地节点态 MCP 与当前在线的 Server 中心态 MCP 目录）。
- `await describeTool(fullName)`：返回指定工具的完整 JSON Input Schema 与详细说明。
- `tools`（基于 `goja.DynamicObject` 实现的动态命名空间代理）：支持 `await tools.<namespace>.<toolName>(args)` 或 `await callTool("namespace.toolName", args)`，无需在每次新建 VM 时预先注册上百个 JS 函数对象，启动耗时 < 1ms。
- `text(str)` / `image(block)`：显式输出格式化文本块或多模态图片块。
- `store(key, val)` / `load(key)`：在同一个 Session 内的多次 `codemode` 调用之间暂存小型结构化中间变量（容量受限，如单 Session 上限 256KB），避免模型为了传递一串 ID 被迫把它们打印回对话上下文。

---

## 4. 跨「节点态 MCP」与「中心态 MCP」的透明路由

结合 Nex-v3 的分布式架构，`codemode` 沙箱底层的 `ToolRouter` 对大模型彻底屏蔽了 MCP 的物理运行位置：

```text
           Code Mode Sandbox (goja VM inside nex-runner)
     Promise.all([ tools.playwright.snap(), tools.github.get_issue() ])
                          │                        │
                          ▼                        ▼
              ┌───────────────────────┐┌─────────────────────────┐
              │   Local MCP Pool      ││  Remote MCP Proxy       │
              │ (Runner 本地按需唤醒  ││ (复用 Runner<->Server   │
              │  stdio 子进程执行)    ││  WSS 长连接转发至中心)  │
              └───────────────────────┘└─────────────────────────┘
```

1. **本地节点态 MCP (`runner-local`)**：直接走 Runner 本地的懒加载 `MCPPool` 调用 `stdio` 或本地 HTTP MCP，零网络绕行，且能直接访问宿主机桌面、浏览器与内网环境。
2. **远程中心态 MCP (`server-hosted`)**：
   - `nex-server` 连接或托管外部 SaaS MCP（如 GitHub、Linear、Notion、Tavily），并将它们的工具目录摘要（不含任何 API Key / OAuth Token）同步至授权 Runner 的本地目录缓存中。
   - 当 Runner 在线执行 `tools.github.*` 时，Runner 通过现有的 `/ws/runner` WebSocket 发送 `mcp.remote_call` 请求帧，由 `nex-server` 代为执行并将结果流回 `goja` 的 Promise。
   - **离线自治优雅降级**：若 Runner 当前处于断网离线跑 Cron 状态，`searchTools` 会自动隐藏或标记 `server-hosted` 工具为 `offline_unavailable`，调用时立即抛出清晰的离线异常供脚本 `try/catch` 降级处理。

---

## 5. 关键安全铁律：权限审批与插件 Hook 绝不绕过

**模型绝不能通过把调用写进 `codemode` 脚本来绕过安全策略或人工审批！**

每次沙箱内部执行 `tools.<ns>.<tool>(args)` 时，Go 宿主层的分发管线必须逐次经过与普通顶层 Tool Call 完全相同的拦截链：

1. **Plugin `onToolCall` Hook**：安全审计插件依然能逐个检查 `codemode` 内部发起的子工具调用名称与参数，并可单独阻断某一次违规调用（向 JS 沙箱 `reject` 抛出阻断原因）。
2. **Permission Guard（权限分级与远程审批）**：
   - 只读类 MCP 工具（标注 `readOnlyHint: true` 或命中白名单）默认直接放行，保证 `Promise.all` 批量查询丝滑无阻塞。
   - 写操作 / 高危 MCP 工具（如删除资源、发送外部消息、合并代码）：
     - 在线 `interactive` 模式下，Go 宿主挂起该子调用对应的 JS Promise，向客户端发出 `approval_requested` 事件（并在审批卡片上明确展示：` via codemode -> github.merge_pull_request` 及完整参数）；用户在手机/桌面端点击允许后，Promise `resolve` 继续执行后续 JS 逻辑！
     - 离线 `autonomous-policy` 模式下，若未在任务白名单内，立即 `reject` 返回权限拒绝错误。
3. **可观测性事件上报**：`codemode` 执行期间内部触发的每一次底层 MCP 调用，都会向事件流发射嵌套的子进度事件（`tool_execution_update` 附带 `sub_call` 轨迹），前端 UI 可以在折叠的 `Codemode` 卡片内实时展示脚本里并发调用了哪几个 MCP 工具及其耗时，做到完全透明可审计。
