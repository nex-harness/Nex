# Nex-v3 Subagent 子智能体并发与隔离规范 (Subagent Concurrency & Isolation Spec)

- **状态**: Draft / Approved Core Direction
- **更新日期**: 2026-11-18
- **核心决策**: 一期专注实现 **`nex-runner` 同机多协程并发子智能体（Local Concurrent Subagents，100% 离线可用）**，在会话模型与调度接口上**预留跨 Runner 分布式委派扩展能力**；默认硬锁定嵌套深度 **`max_depth = 1`**（禁止子生孙递归裂变）。

---

## 1. 设计定位与架构优势

在 Coding Agent 复杂任务中，Subagent 解决三个核心问题：**保护主会话上下文不被海量中间搜索结果污染**、**并行扇出（Fan-out）缩短长任务耗时**、以及**按任务难度分级路由大小模型以降低成本**。

依托 Go 语言运行时特性，Nex-v3 的同机 Subagent 无需拉起沉重的多 Node 进程：
- 每个 Subagent 本质上是同一个 `nex-runner` 进程内由 `SessionManager` 托管的独立 `Agent Loop` **`goroutine`**（初始栈开销仅数 KB）。
- 天然形成以父会话 `context.Context` 为根的协程取消树，主任务中止时毫秒级级联回收全部并发子智能体及其派生的操作系统子进程树。

---

## 2. 调度拓扑：一期同机并发 + 跨节点扩展预留

`SessionManager` 抽象统一的 `SubagentSpawner` 接口，使上层工具调用与底层物理执行位置解耦：

```go
type SpawnRequest struct {
    ParentSessionID  string
    ParentToolCallID string
    TargetRunnerID   string // 一期为空（默认本机 LocalSpawner）；预留未来通过 nex-server 路由至远端 Runner
    AgentType        string // 如 "explore" | "worker" | 自定义角色 slug
    Prompt           string
    Isolation        string // "shared" (默认) | "worktree" (Git 工作树物理隔离)
    RunInBackground  bool
}
```

1. **一期实现（`LocalSpawner`）**：`TargetRunnerID == ""`（或等于当前 Runner ID）时，直接在当前 `nex-runner` 内存与本地 SQLite 中创建子会话并并发运行，断网跑离线 Cron 定时任务时依然 100% 可用。
2. **未来扩展预留（`RemoteSpawner`）**：协议帧、`sessions` 表结构（保留 `runner_id` 与 `parent_session_id` 跨节点关联）以及工具定义均预留可选 `target_runner` 字段；后续无需破坏已有数据结构即可平滑接入“Mac 主智能体派活给 Windows/Linux 测试机子智能体”。

---

## 3. 防失控安全护栏（深度锁定与并发熔断）

无头守护进程运行并发智能体必须具备确定性的物理边界：

1. **嵌套深度硬锁定 (`max_depth = 1`)**：
   - 主会话 `depth = 0`，其派生的 Subagent 会话 `depth = 1`。
   - **双重阻断机制**：
     1. **工具集物理剥离**：当 `depth >= 1` 时，构造子会话可见工具列表时**直接剔除 `subagent`（及后台任务派生类）工具**，让子智能体在模型侧根本看不到派生入口，既杜绝无限套娃，也不浪费报错重试 Token。
     2. **内核兜底校验**：`SessionManager.Spawn()` 强制校验 `parent.Depth + 1 <= cfg.MaxDepth`，越界直接拒绝。
2. **并发度与轮次上限**：
   - **单会话并发槽位 (`max_concurrent_subagents`)**：默认单个父会话同时活跃的运行态 Subagent 上限为 **`6`**（超出时进入本地信号量排队或提示分批执行，防止瞬间打爆 LLM API 速率限制 Rate Limit）。
   - **子任务轮次熔断 (`max_turns`)**：每个角色预设可配置最大推理轮次（如 `explore` 默认 `20` 轮），到达水位前最后一轮自动注入收尾提示要求立即输出当前阶段结论。
3. **生命周期级联取消 (Cascade Cancellation)**：
   - 前台并发 Subagent 直接派生自当前 Turn 的 `ctx`；后台 Subagent 派生自父 Session 根 `ctx`，同时各自持有独立的 `cancelFunc`（支持用户在 UI 上单独终止某一个卡住的后台子任务而不影响主对话）。

---

## 4. 两种协作执行模式：前台扇出 vs 后台异步邮箱

### 4.1 前台并行扇出 (Foreground Parallel Fan-out)

当主 Agent 在单次回复中同时发出多个 `subagent` 工具调用（例如同时检索前端、后端、数据库三个目录）：
- Runner 工具调度器识别其为并发安全调用，并行启动多个子 `Agent Loop` 协程等待全部完成。
- 子智能体内部几十轮的 `read`/`grep` 过程完全封闭在各自的子会话中；仅将每个子智能体的**最终一条 Assistant 总结文本**作为对应 `tool_result` 返回给主会话上下文。

### 4.2 后台异步运行与点对点通信 (`run_in_background` + `send_message`)

针对耗时较长的编译排查、全库扫描或并行模块编写：
1. **非阻塞启动**：设置 `run_in_background: true` 时，`subagent` 工具立即返回 `{ agent_id: "sub_01...", session_id: "sess_sub_01...", status: "running" }`，主 Agent 可继续响应用户其他问题。
2. **双向通信 (`send_message`)**：
   - 主 Agent 可随时调用 `send_message(to: "sub_01...", message: "...")`：
     - 若目标子智能体**正在运行**，消息推入该子会话的 `steer` 队列即时纠偏转向；
     - 若目标子智能体**已运行完毕**，基于其保留在本地 SQLite 中的完整子会话上下文原地唤醒（Resume）继续追问，避免重新扫描代码。
3. **完成自动回传**：后台子智能体结束时，自动向父会话投递一条结构化完成通知（若父会话当前空闲，自动触发一轮 `followUp` 向用户汇报汇总结果）。

---

## 5. 并发写冲突防护：角色分级与 `git worktree` 隔离

为防止多个并行 Subagent 同时修改同一份源码或争抢 `.git/index.lock`：

### 5.1 内置与 Server 下发的角色预设 (`~/.nex-runner/agents/`)

除了内置基础角色外，支持在 `nex-server` 用 Markdown + YAML Frontmatter 统一管理自定义子智能体画像，并按内容 Hash 同步缓存到各 Runner 本地：

```markdown
---
name: explore
description: 高速只读代码库探索与问题定位专家
model_tier: fast          # 自动映射至配置的轻量高速模型（如 Haiku / Flash）
tools_allow: [read, grep, glob, codemode] # 物理剥离 write / edit / 非只读 bash
max_turns: 20
---
你是一个只读代码检索子智能体。请快速定位关键文件路径与行号，直接输出结论摘要，不要贴大段无关源码。
```

- **只读角色（如 `explore`、`reviewer`）**：底层强制剥离文件写入工具，天然支持在同一工作区目录任意高并发运行。
- **读写角色（如 `worker`）**：具备 `write`、`edit`、`bash` 权限。

### 5.2 并行写任务的 `git worktree` 隔离模式

当主 Agent 需要同时派出多个读写型 `worker` 修改代码时，可指定 `isolation: "worktree"`：
1. `nex-runner` 在启动子会话前，基于当前 HEAD 自动执行 `git worktree add -b nex/sub-<id> .nex/worktrees/<id>`，并将该 Subagent 的工作目录 `cwd` 锁定到该独立工作树内。
2. 多个 `worker` 各自在独立目录改代码、跑局部测试，100% 互不踩踏。
3. 子任务结束时：
   - 若工作树无任何文件变更，Runner 自动清理移除临时 worktree；
   - 若产生了代码修改或提交，向主 Agent 返回该 worktree 路径、分支名与变更统计，由主 Agent 审查后合并回主工作区。

---

## 6. 权限收敛、存储复用与多端防刷屏流式设计

### 6.1 权限交集原则与审批冒泡

- **绝不越权**：Subagent 的有效权限取 **「父会话当前权限策略 ∩ 子角色允许工具集」** 的交集（例如父会话处于只读或离线受限模式，任何子角色均无法获得更高权限）。
- **审批冒泡**：在线交互模式下，若某个 `worker` Subagent 触发了需要用户确认的高危命令，发往客户端的 `approval_requested` 事件会同时携带 `session_id`（父会话）与 `subagent_meta: { id, title, agent_type }`，用户在手机或桌面上能清楚看到是哪个子任务正在请求授权。

### 6.2 存储同构与 UI「父流摘要 + 子流按需订阅」

1. **零额外同步表**：每个 Subagent 在本地 SQLite 中就是一条标准的 `sessions` 记录（附带 `parent_session_id`、`parent_tool_call_id`、`depth = 1`），其内部事件同样写入统一的 Outbox 表同步至 `nex-server`。
2. **主界面防刷屏机制**：
   - `nex-server` 向订阅了**父会话**的客户端推送时，**不会混入**各个并发 Subagent 海量的逐字 `message_update` 增量帧（防止 6 个子任务同时输出卡死移动端渲染）。
   - 父会话事件流仅接收低频聚合的**子任务状态快照**（`subagent.started`、`subagent.step` 当前正在调用的工具摘要、`subagent.completed` 耗时与 Token 统计）。
   - 当用户在 Web / Desktop / Mobile 界面上**主动点击展开某张 Subagent 卡片**时，前端直接复用现有的会话订阅通道订阅该 `sub_session_id`，即可丝滑查看或回放该子智能体完整的内部思考与工具调用细节。
