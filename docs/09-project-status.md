# Nex-v3 项目现状总览 (Project Status)

- **更新日期**: 2026-11-20
- **分支**: `feat/nex-v3`
- **最近提交**: `e035bb8` feat(harness,ui): add ZCode prompt/todo_write system, 5-layer streaming freeze fix, and 5-section status panel（当前工作区含最新 ZCode 对齐与测试补齐改动）
- **文档定位**: 面向“接手者”的一页纸现状说明。设计细节见 `01`–`07`，阶段拆解见 `08-implementation-roadmap.md`，本文只回答“做到哪了、还差什么、下一步做什么”。

---

## 1. 项目目标

Nex-v3 是一套 **Go 原生、零 CGO** 的多 Runner Agent Harness，对标 `pi-agent` 的能力模型，交互与视觉对齐 ZCode。

核心形态是三层拓扑：

| 层 | 组件 | 职责 |
| :--- | :--- | :--- |
| 控制面 | `nex-server` | Runner 注册与鉴权、多端会话路由、配置中心（Providers / Models / Skills / Plugins / MCP / Cron）、可选 LLM 网关代理、内嵌 Web UI |
| 数据面 | `nex-runner` | 自治执行节点，内聚 Agent Loop、LLM Provider、本地工具、PTY、MCP 宿主、插件、Subagent、Cron、SQLite 存储；断网可继续工作并在重连后补传 |
| 视图层 | `clients/web` / `clients/app` | 同一套 React 19 前端资产，Web 直接由 `nex-server` 内嵌提供，桌面/移动端通过宿主壳复用 |

**不可妥协的工程约束**

1. `CGO_ENABLED=0`，必须能交叉编译 `darwin/arm64`、`linux/amd64`、`windows/amd64`。
2. 单文件静态二进制，Runner 不依赖外部运行时。
3. 本地优先：所有会话事件先落 SQLite（WAL），再按 `seq` 增量同步。
4. 前端对齐 ZCode 的布局、Block 渲染、交互与名词翻译。

---

## 2. 现状速览

### 2.1 里程碑完成度

| 里程碑 | 主题 | 状态 | 说明 |
| :--- | :--- | :--- | :--- |
| M1 | Go 智能体内核 | ✅ 完成 | 双协议流式、Agent Loop、7 个内置工具、SQLite + Zstd CAS、6 维上下文计量 |
| M2 | Server 贯通 + Web 对话闭环 | ✅ 完成 | WS 双向流、Outbox 补传、REST 管理接口、流式 Markdown、审批弹窗、上下文浮层 |
| M3 | 远程终端 + 多 Tab 工作台 | ✅ 完成 | 零 CGO PTY、`ghostty-web` WASM 终端、Files/Preview/Review/Side Chat 多 Tab |
| M4 | 扩展生态 | ✅ 完成 | `goja` Code Mode 懒加载 MCP、TS 插件 + 远程 `ctx.ui`、同机并发 Subagent |
| M5 | Cron + 归档 + 统计 | ✅ 完成 | `robfig/cron/v3`、`kardianos/service`、`main.db`/`archive.db` 冷热分离、小时桶用量看板 |
| M6 | 桌面端与移动端交付 | ⚠️ 部分完成 | Go 侧宿主与 All-in-One 模式已通，`mygo` 原生容器打包尚未接入 |

### 2.2 质量门禁

| 检查项 | 结果 |
| :--- | :--- |
| `go test ./...` | 全部通过（15 个包有测试，0 失败，`-race -cover`） |
| 后端覆盖率 | `prompts` **89.2%**、`pty` **78.8%**、`tools` **74.5%**、`workspace` **74.2%**、`scheduler` 73.2%、`ai` **71.8%**、`agent` **71.5%**、`extension` **69.8%**、`runner` **64.4%**、`protocol` 63.6%、`subagent` 62.3%、`codemode` 59.1%、`store` 54.5%、`server` **51.5%** |
| 前端测试 | Vitest **12 个用例全部通过**（0 unhandled errors），覆盖 PTY 协议、流式 reducer、O(tail) 冻结、todo/goal 状态、Provider 切换子菜单、Effort 升序滑块与 `^T`、目录浏览器弹窗、Subagent 检视、Composer `@` 文件引用 / `/` 命令补全 / 图片附件、会话分支编辑重发与 Git Review Stage/Commit |
| 零 CGO 交叉编译 | `verify-nocgo.sh` 通过，3 平台 × 3 二进制共 9 次构建全部成功 |
| 前端构建 | `tsc -b && vite build` 通过，产物自动嵌入 `pkg/webui/dist` |

### 2.3 代码规模

| 区域 | 行数（含测试） |
| :--- | ---: |
| `pkg/ai` | ~2,350 |
| `pkg/store` | ~2,490 |
| `pkg/harness/tools` | ~2,100 |
| `pkg/server` | ~1,890 |
| `pkg/runner` | ~1,810 |
| `pkg/agent` | ~1,080 |
| `pkg/harness/prompts` | ~960 |
| `pkg/harness/codemode` | ~955 |
| `pkg/harness/workspace` | ~670 |
| `pkg/harness/pty` | ~635 |
| `pkg/extension` | ~600 |
| `pkg/harness/subagent` | ~460 |
| `pkg/harness/scheduler` | ~250 |
| `pkg/protocol` | ~260 |
| `clients/app` | ~240 |
| `clients/web/src` | ~10,360 |

---

## 3. 已实现功能清单

### 3.1 模型与协议（`pkg/ai`）

- 三套原生流式适配器：**OpenAI / Codex Responses（`/v1/responses`）**、**OpenAI Chat Completions（`/v1/chat/completions`，兼容 DeepSeek / OpenRouter / Groq / Together / vLLM / Ollama，支持 `reasoning_content`、`reasoning_effort`、增量 `tool_calls`、多模态图像与缓存 usage）**、**Anthropic Messages（`/v1/messages`）**。
- 多轮工具调用中保留加密推理项（encrypted reasoning）与 thinking signature。
- `models.dev` 目录：内嵌离线种子 + 在线拉取 + 解析，含上下文窗口、输出上限、Vision/PDF/Audio/Video、工具、推理、缓存能力与输入/输出/缓存读写单价。
- **模型名退避识别**：`LookupModelMeta` 支持 `xxx/xx/xx/xx/glm-5.3 → glm-5.3` 的逐级 `/` 前缀剥离匹配（大小写不敏感）。

### 3.2 Agent Loop（`pkg/agent`）

- 状态机支持运行中 `Steer` 插话、结束后 `FollowUp` 追加队列、`context.Context` 全链路取消。
- **自动重试与容错（`retry.go`）**：对 429/5xx/连接超时等瞬时错误执行指数退避重试（401/403 等不可重试错误立即失败）；检测上下文溢出错误时自动触发一次压缩并立即恢复重试。
- **三层上下文压缩与输出护栏（`compact.go`）**：
  - 单工具输出硬截断 `GuardToolOutput`（64KB 头+尾保留，UTF-8 安全）。
  - Tier 1 Microcompact（达到窗口 65% 时无 LLM 裁剪历史大工具输出）。
  - Tier 2 Auto-Compact（达到窗口 85% 或溢出时安全切分并生成结构化摘要，失败回退确定性摘要；发出 `compaction` 事件 checkpoint，会话重放从最新 checkpoint 恢复）。
  - 手动 `/compact [focus]` 指令直达。
- **并发只读批处理与执行前校验（`loop.go`, `validate.go`）**：连续只读工具调用通过 goroutine 并发执行并按原序稳定回填结果；所有工具执行前通过 JSON Schema 校验 `required` / `type` / `enum`，避免畸形参数触发无效审批。
- 6 维 `Context windows` 计量：`messages` / `system_tools` / `system_prompt` / `mcp_tools` / `skills` / `meta_context`。
- 系统提示词采用 **字节稳定** 的 `system_prompt` + 动态 `meta_context` 分层，避免前缀缓存失效（`prompts/builder.go`）；支持 Plan Mode（`read_only`）动态引导、从工作目录到 Git 根目录的多级 `AGENTS.md` / `CLAUDE.md` 自动发现（外到内），以及插件 `OnSystemPrompt` / `before_agent_start` 钩子。

### 3.3 内置工具（`pkg/harness/tools`）

`read`、`write`、`edit`（LF/CRLF 与缩进容错 + Unified Diff + 每文件并发互斥锁 + 先读后改与外部修改陈旧检测 `FileStateTracker`）、`bash`（Unix 进程组清理 / Windows Job Object + 细粒度只读命令链解析 `bash_policy.go` 支持 Plan Mode 安全只读命令与并发执行 + `run_in_background` 后台任务）、`task_output`、`task_stop`、`webfetch`（纯 Go SSRF 内网/环回防护 + HTML 文本提取 + 2 分钟 TTL 缓存）、`ask_user_question`（桥接远程 `UIPromptModal` 支持 select/confirm/input）、`grep`、`glob`（内置 `.gitignore` 过滤）、`todo_write`、`todo_read`。

子智能体相关工具：`subagent`、`send_message`。Code Mode 沙箱工具：`codemode`。Runner 侧同时贯通 `/compact` 与插件注册的 `/slash` 命令分发。

### 3.4 存储（`pkg/store`）

- `modernc.org/sqlite` + WAL，`entities.go` 强制会话事件 `seq` 单调。
- SHA-256 + Zstd 内容寻址 Blob 仓库（`blobs/`），带引用计数回收。
- 冷热分离：`archive.db` 通过 `ATTACH DATABASE` 原子迁移，支持归档浏览、Unarchive、永久删除。
- `usage_rollups_hourly` 小时桶预聚合，支撑 Today / 7D / 30D 看板。

### 3.5 中心控制面（`pkg/server`）

- `/ws/runner`：反向注册、可选 `PairingSecret` 鉴权、心跳、`seq` 增量上报与 `ack_seq` 补推。
- `/ws/client`：多端订阅、`last_seen_seq` 续传。
- `/llm-proxy/*`：可选托管密钥的流式转发。
- REST：`preferences`、`providers`、`models`、`runners`、`sessions`、`archives`、`blobs`、`stats`、`agents`、`skills`、`plugins`、`mcp`、`cron`。
- 本次新增模型相关接口：
  - `POST /api/v1/models/fetch-remote` —— 用供应商 Base URL + API Key 代理拉取上游 `/v1/models`。
  - `POST /api/v1/models/lookup` —— 先查在线 `models.dev`，失败回退内置种子，按退避规则匹配元数据。
  - `POST /api/v1/models/test` —— 用真实驱动发一次轻量探针，返回 `ok` / `latency_ms` / `reply`。
  - `DELETE /api/v1/models/{id}` —— 删除模型并触发配置同步。

### 3.6 运行时子系统（`pkg/runner` / `pkg/harness/*`）

- **PTY**：Unix `creack/pty`，Windows 纯 Go ConPTY，二进制帧直传，128KB 环形缓冲断线回放。
- **Workspace RPC**：`fs_list`、`fs_search`、`fs_read_preview`、`git_status/diff`。
- **Code Mode**：`searchTools` / `describeTool` / `tools.<ns>.<tool>()` 并发 Promise，`vm.Interrupt` 超时熔断，本地 stdio MCP 首次调用才拉起、空闲回收，中心态 MCP 远程路由。
- **插件**：Server 侧 `esbuild` 编译 TS，Runner 侧 `goja` 执行，支持 `tool_call` / `tool_result` 钩子、`/slash` 命令、跨网络 `ctx.ui`。
- **Subagent**：同机 goroutine 并发，硬锁 `max_depth=1`，`explore` / `worker` 两类角色，`shared` / `worktree` 隔离，后台执行 + `send_message` 邮箱。
- **Cron**：本地快照离线准点执行，`kardianos/service` 注册 launchd / systemd / Windows Service。

### 3.7 前端（`clients/web`）

布局与交互对齐 ZCode：

- 可收起左侧项目/会话侧栏，主对话区，可调高度的底部终端，右侧多 Tab 工作台。
- 流式渲染 5 层抗卡顿：30ms 合并、瞬时 delta 不落库、`rAF` 批帧、`O(tail)` 冻结历史引用、`React.memo` 轮次隔离 + `ResizeObserver` 滚动锚定。
- 工具卡片、Thinking 折叠、Unified Diff、Shiki 代码高亮、`@file:L10-20` 引用。
- 5 段浮动状态面板：`Git tools` / `Goal` / `Progress`（严格取自 `todo_write`）/ `Terminals`（带 ■ Stop）/ `Agents`（Ended N >）。
- 设置页 11 个分区：General、Appearance、Providers、Runners、Agents、Plugins、MCP、Skills、Automations、Usage、Archives。
- 本次新增：
  - **新建对话目录选择弹窗（`DirectoryBrowserModal`）**：草稿页项目菜单改为向上弹出 + 搜索已知/最近工作区 +「选择目录…」按钮，直接浏览所选 Runner 的目录树（面包屑导航、主目录/上级目录、隐藏目录开关、快捷目录、点选子目录），不再要求手动输入路径；后端新增 `workspace` RPC `fs_browse_dirs` 及 `GET /api/v1/runners/{id}/dirs`。
  - **模型选择浮窗对齐 ZCode**：点击顶部 Provider 行打开 Provider 选择子菜单（宽屏侧拉、窄屏/三栏内联下钻），点击供应商直接切换模型列表而不跳转设置，仅底部「管理供应商」入口跳转设置页；模型列表按当前供应商过滤，带上下文与 `Vision` 标签。
  - **推理强度（Effort）修复与对齐 ZCode**：修正档位顺序为严格升序 `关闭(off) → 低(low) → 中(medium) → 高(high) → 极高(xhigh)/最高(max)`（默认 `medium` 并持久化，不再误落到「关闭」）；`ModelMeta` 携带 `reasoning_levels`，前端对齐 ZCode 绿色填充 / 最高档 oklch 绿蓝渐变 + 流动气泡 + 档位圆点 hover 放大，并绑定全局 `^T`（Ctrl+T）快捷键与工具栏垂直滚动标签。
  - **ZCode 动效体系对齐**：补齐左右侧栏与底部终端的 `200ms` 锁宽折叠/展开动效、浮层与弹窗进场缩放/淡入动效、草稿页建议芯片瀑布流（`zcode-draft-prompt-waterfall`）、折叠区 Grid 平滑展开、运行中工具 indeterminate 扫光进度条、状态面板悬浮胶囊 `Maximize2` 图标切换、复制回弹微动效及 `prefers-reduced-motion` 支持。
  - **Plan 模式与 Subagent 修复**：权限选择新增 `计划 / 只读`（`read_only`）；修复 `openSubagentTab` 状态字段拼写（`sidePaneVisible`）并补齐中英双语 i18n。
  - **ZCode Composer 完整交互对齐**：新增 `@` 工作区文件实时搜索补全浮层（键盘上下/Tab/Enter/Esc）、`/` 斜杠命令补全（内置 `/goal`、`/compact` + 已启用插件命令 + 技能）、多模态图片粘贴（`onPaste`）、拖拽上传、文件选择与缩略图预览条，并在对话流 `UserBubble` 中渲染图片附件。
  - **消息历史分支与编辑重发**：后端新增 `POST /api/v1/sessions/{id}/fork` 与 `session.fork_sync` Runner 同步协议；用户消息悬停支持「编辑分支」，助手回复支持「从此分支」，无损分叉历史并立即切换继续对话。
  - **Git 快捷工作流（Review Tab）**：后端 `workspace` 新增 `git_stage`、`git_unstage`、`git_discard`、`git_commit`、`git_branches`、`git_checkout` 及精确 `staged`/`unstaged` 状态解析；前端 Review 面板支持单文件/全量暂存与撤销、放弃未暂存改动、分支切换/新建、一键填写 Commit 信息提交。
  - **模型管理能力**：模型可见性开关、拖拽排序（`localStorage` 持久化并同步至输入框下拉）、行内重命名显示名、从 `/v1/models` 批量添加、手动添加模型 + `models.dev` 退避自动填充、每行连通性测试探针。

---

## 4. 已知差距与风险

### 4.1 功能缺口

| 项 | 现状 | 影响 |
| :--- | :--- | :--- |
| `mygo` 原生容器 | `clients/app` 只有 Go 侧 `NativeBridge` 与 All-in-One 宿主；`nex-app` 目前用系统浏览器打开 | M6 未真正交付桌面/移动原生壳，`go.mod` 里也没有 `mygo` 依赖 |
| 推送通知 | `SetMobilePushToken` 仅本地持久化，未对接 APNs / FCM | 移动端锁屏审批唤醒不可用 |
| ZCode Workflow 编排 | 按需暂缓 | 当前聚焦单会话与多子智能体开发体验 |

### 4.2 质量缺口

- `pkg/server` 覆盖率已提升至 **50.9%**（已覆盖 `models/fetch-remote`、`lookup`、`test`、`DELETE`、`runners/{id}/dirs` 等）；WebSocket 多端并发与离线补传场景仍可继续扩展。
- 前端 Vitest 已扩展至 10 个用例，SettingsPage 表单保存/批量拉取交互仍可继续补充测试。

### 4.3 工作区状态

本次四项调试改动尚未提交（工作区有 9 个源文件修改 + 前端构建产物更替）：

```
clients/web/src/core/store.ts
clients/web/src/core/types.ts
clients/web/src/features/ConversationPane.tsx
clients/web/src/features/ConversationStatusPanel.tsx
clients/web/src/features/SettingsPage.tsx
clients/web/src/features/WorkbenchSidebar.tsx
pkg/ai/models_catalog.go
pkg/server/api.go
pkg/store/store.go
pkg/webui/dist/*（构建产物）
```

---

## 5. 建议的下一步

按优先级排序：

1. **补齐后端测试**：为 `models/fetch-remote`、`models/lookup`（含退避规则）、`models/test`、`DELETE /models` 写 httptest 用例，把 `pkg/server` 覆盖率拉到 50% 以上。
2. **前端测试**：为模型选择浮窗、模型可见性开关、Subagent 面板开合写 Vitest 用例；补一个 reducer 流式回归测试。
3. **推理档位对齐**：让 `ModelMeta` 携带 `reasoning_levels []string`，前端按模型动态渲染档位，而不是全局四档。
4. **交互补齐**：模型拖拽排序与重命名；绑定 `^T` 快捷键；Plan 模式。
5. **i18n 收口**：清理硬编码英文，统一走 `messages.ts`。
6. **M6 收尾**：引入 `egoist/mygo`，把 `clients/app` 从“系统浏览器打开”换成真正的 Web Mode 容器；接入 APNs / FCM。

---

## 6. 快速开始

```bash
make run-local     # 构建 Web UI + 三端二进制并本地启动（默认 http://127.0.0.1:8080，占用则 18080）
make test          # Go 全量测试（-race -cover）
make test-web      # 前端 Vitest
make verify-nocgo  # 三平台零 CGO 交叉编译校验
```

本地启动脚本会自动从 `~/.nex/v2/provider_config.json` 引导供应商与模型配置（若存在）。
