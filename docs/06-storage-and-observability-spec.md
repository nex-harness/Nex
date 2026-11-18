# Nex-v3 数据存储、归档生命周期、模型字典与可观测性规范 (Storage, Archiving, Models & Observability Spec)

- **状态**: Draft / Approved Core Direction
- **更新日期**: 2026-11-18
- **设计目标**: 基于纯 Go 无 CGO 的 SQLite (`modernc.org/sqlite`) 与内容寻址块存储 (CAS Blobs)，统一规划配置、供应商与 `models.dev` 模型库、对话冷热分离归档（Archive / Unarchive / 永久删除）、实时上下文构成透视（Context Windows Inspector + 按需 Trace）以及 Runner 级多时间窗成本统计。

---

## 1. 存储引擎拓扑：主库、归档冷库与去重 Blob 仓

为保证主数据库在长期高频使用后依然保持极小体积与毫秒级检索响应，同时不引入外部重型数据库，`nex-server` 与 `nex-runner` 均采用本地目录化分层存储：

```text
~/.nex-server/ (服务端数据目录，权限 0700)
├── main.db            # 热库 (SQLite WAL)：系统配置、模型字典、Runner 节点、活跃对话、统计预聚合表
├── archive.db         # 冷库 (SQLite WAL)：已归档 (Archived) 对话的完整事件与 Trace 索引，物理隔离于主库之外
├── blobs/             # SHA-256 内容寻址去重仓 (ab/cd/<sha256>.zst)：大文本 Prompt、Skill 快照、完整 Trace 原文
└── assets/            # Skills 压缩包、已转译的 Plugin .js 产物、上传的多模态图片附件
```

`~/.nex-runner/` 采用同构精简布局（`state.db` + `blobs/` + `skills/` + `plugins/` + `agents/`），用于保存本地同步快照、离线 Cron 状态及待补传的 Outbox 事件。

---

## 2. 配置与凭证存储分层

### 2.1 UI 外观与交互配置（云端漫游 + 设备本地覆盖）

区分「账户级审美偏好」与「物理屏幕尺寸强相关参数」，防止手机端调字号污染桌面端布局：

| 配置归属 | 存储位置 | 包含配置项 |
| :--- | :--- | :--- |
| **跨端漫游层** | `nex-server` `main.db -> user_preferences` (JSON) | 主题模式（`dark` / `light` / `system`）、主题色板、代码高亮配色、正文字体族与等宽代码字体族、默认思考展开行为、**自动归档闲置天数策略**。 |
| **本机物理层** | 各客户端本地存储（`mygo` 本地配置 / `localStorage`） | 当前设备字号与 UI 缩放倍率、桌面端分栏宽度与窗口坐标、自定义全局呼出快捷键、输入框草稿缓存。 |

### 2.2 Server、Runner 与 Agent 业务配置

- **API Key 明文简洁存储**：供应商 `api_key` 直接明文保存在配置文件或 SQLite `providers` 表中（数据库与配置文件强制 `0600` 权限，同时支持机器环境变量如 `ANTHROPIC_API_KEY` 覆盖），省去繁琐的加解密密钥管理负担，便于本地直连与排障。
- **Runner 节点注册表 (`main.db -> runners`)**：存储远程与本机 Runner 列表（`id`, `name`, `hostname`, `os`, `arch`, `shell`, `version`, `tags`, `status`, `last_seen_at`, `synced_config_rev`, `auth_token`）。
- **主/子 Agent 画像 (`main.db -> agent_profiles`)**：统一存储主 Agent 默认配置与自定义 Subagent 预设（`slug`, `display_name`, `system_prompt`, `model_id` 或档位引用 `tier:fast`, `thinking_effort` [`low`/`medium`/`high`], `tools_allow`, `max_turns`, `isolation_mode`），按 `config_rev` 版本号增量同步至各 Runner 本地快照。

---

## 3. 供应商与模型能力/价格字典（集成 `models.dev`）

为了准确展示每个模型的上下文容量上限（如 `1M` / `200K`）、多模态能力开关，并自动计算每次调用的预计美元花费，Nex-v3 直接对接开源标准模型元数据注册表 **`models.dev` (`https://models.dev/api.json`)**：

### 3.1 三级模型元数据来源（优先级从高到低）

1. **用户自定义 / 手动覆盖 (`user_override`)**：支持用户在 UI 添加私有部署模型（vLLM / Ollama / 内部网关如 `MP/group/gemini-4-carbon`）或手动修正单价与上下文窗口。
2. **`models.dev` 在线同步字典 (`models_dev_sync`)**：`nex-server` 后台定期（如每 24 小时）及支持前端一键触发拉取 `https://models.dev/api.json`，自动更新全球主流供应商的模型列表、上下文上限、能力标签与最新官方 Token 单价。
3. **编译期内嵌种子快照 (`embedded_seed`)**：构建二进制时通过 `//go:embed` 内置一份最新 `models.dev` 快照，确保完全离线内网环境开箱即拥有完整的模型窗口与计费基准。

### 3.2 `providers` 与 `models` 核心表结构

```sql
CREATE TABLE providers (
    id          TEXT PRIMARY KEY,              -- 如 'anthropic', 'openai-codex', 'custom-mp'
    name        TEXT NOT NULL,
    driver      TEXT NOT NULL,                 -- 一期支持: 'openai_responses' (Codex Responses API) | 'anthropic' (Messages API)
    base_url    TEXT NOT NULL,
    api_key     TEXT NOT NULL DEFAULT '',      -- 明文存储 (文件级 0600 保护)，支持环境变量回退
    proxy_mode  TEXT NOT NULL DEFAULT 'direct',-- 'direct' (下发至受信任 Runner) | 'server_proxy' (经 Server 网关转发)
    enabled     INTEGER NOT NULL DEFAULT 1,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE models (
    id                  TEXT NOT NULL,         -- 如 'claude-sonnet-4-5', 'gemini-4-carbon'
    provider_id         TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    display_name        TEXT NOT NULL,
    source              TEXT NOT NULL,         -- 'models.dev' | 'custom'
    -- 容量限制
    context_window      INTEGER NOT NULL,      -- 如 200000, 1000000
    max_output_tokens   INTEGER NOT NULL,      -- 如 16384, 64000
    -- 模型能力标记 (对齐 models.dev 字段)
    supports_vision     INTEGER NOT NULL DEFAULT 0, -- 图片多模态输入
    supports_pdf        INTEGER NOT NULL DEFAULT 0, -- 文档直传输入
    supports_audio      INTEGER NOT NULL DEFAULT 0,
    supports_video      INTEGER NOT NULL DEFAULT 0,
    supports_tools      INTEGER NOT NULL DEFAULT 1,
    supports_reasoning  INTEGER NOT NULL DEFAULT 0, -- 是否支持 Thinking 强度调节 (Low/Medium/High)
    supports_caching    INTEGER NOT NULL DEFAULT 0, -- 是否支持 Prompt Caching
    -- 计费单价 (USD per 1,000,000 tokens，对齐 models.dev cost 结构)
    cost_input_per_1m       REAL NOT NULL DEFAULT 0,
    cost_output_per_1m      REAL NOT NULL DEFAULT 0,
    cost_cache_read_per_1m  REAL NOT NULL DEFAULT 0,
    cost_cache_write_per_1m REAL NOT NULL DEFAULT 0,
    enabled             INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (provider_id, id)
);
```

---

## 4. 对话数据、Context Windows 实时透视与按需 Trace

### 4.1 6 维上下文窗口透视模型（Context Windows Inspector）

参考标准 Coding Agent 上下文透视交互，输入框底栏常驻**上下文占用圆环指示器**，悬停或点击弹出 `Context windows` 明细卡片（实时显示如 `67.6K / 1M (6.4%)`、进度条、**缓存命中率**及 **6 大标准构成维度**）：

| 维度标识 (`category`) | UI 展示名称 | 统计内容范围 | 按需 Trace 可检视内容 |
| :--- | :--- | :--- | :--- |
| `messages` | **Messages** | 历史对话消息总和。内部附带子拆解：`user_tokens`（用户输入）、`assistant_tokens`（模型回复与 Thinking）、`tool_io_tokens`（工具调用入参与返回结果）。 | 各轮消息与被折叠的大段 Tool 执行原始输出。 |
| `system_tools` | **System tools** | 内置核心工具（`read`, `write`, `edit`, `bash`, `grep`, `subagent` 等）与插件注册工具的 JSON Schema 体积。 | 当轮实际发送给 LLM 的内置工具完整 Schema JSON。 |
| `system_prompt` | **System prompt** | 当前 Agent 角色的核心基础系统提示词正文。 | 点击底部 **`Show system prompt`** 按钮秒开完整提示词原文。 |
| `mcp_tools` | **MCP tools** | `codemode` 沙箱工具定义 + 已启用 MCP 命名空间目录摘要（体现懒加载省下的极低占比）。 | 当前注入的 MCP 目录文本及本轮激活查询过的 MCP Schema。 |
| `skills` | **Skills** | 可用 Skills 列表摘要 + 当前对话中已触发加载进上下文的 `SKILL.md` 正文。 | 逐个列出已激活 Skill 的名称、各自 Token 大小与正文快照。 |
| `meta_context` | **Meta context** | 动态注入的环境元信息（目标 Runner OS/Shell、工作区 `AGENTS.md`/`CLAUDE.md` 项目规范、Git 分支状态、当前日期等）。 | 实际拼接的环境与项目级规则块原文。 |

### 4.2 「轻量指标随流直推 + 重型原文 CAS 按需拉取」机制

1. **平时对话零冗余传输**：
   - 每轮推理完成时，Runner 将不到 1KB 的 `turn_telemetry` 结构化数值指标（总窗口、当前占用、6 大分类各自的 `bytes` / `tokens` / `pct` / `blob_hash`、以及 `cache_read_tokens` / `cache_write_tokens` / `cache_hit_rate` / `estimated_cost_usd`）随 `turn_end` 事件推送到客户端。
   - 对话界面**无需拉取任何 Prompt 或 Trace 原文**即可实时渲染底部圆环、6 维百分比列表与缓存利用率。
2. **点开看 Trace 时按 Hash 懒加载 (`blobs` 去重仓)**：
   - 拼装好的完整 System Prompt、Skills 文本、Tools Schema 以及整轮原始 LLM Request/Response Trace，按内容计算 `sha256` 后经 Zstd 压缩存入 `blobs` 目录（同一个会话跑 50 轮若提示词未变，磁盘永远只占 1 份空间）。
   - 仅当用户在浮层点击 **`Show system prompt`** 或打开 **`Trace Inspector` 抽屉**点选某一项时，前端才携带对应的 `blob_hash` 请求 `GET /api/v1/blobs/:hash` 拉取原文展示（若该 Blob 仅存在于远端 Runner 本地，Server 自动通过 WebSocket 透传拉取一次并落盘缓存）。

---

## 5. 用户可配置的对话归档机制（冷热物理分离：Archive / Unarchive / 永久删除）

为确保主库 `main.db` 长期轻快，同时绝不丢失用户历史资产，实施**冷热双库物理分离归档**：

### 5.1 归档触发策略（用户可配置）
- 在「设置 -> 数据与存储」中提供用户可配置的自动归档周期 `auto_archive_after_days`（如 `7 天` / `30 天` / `90 天` / `从不自动归档`，基于会话 `last_active_at` 判断，仅针对非运行态会话），同时支持在会话列表右键手动一键 **Archive**。

### 5.2 冷热迁移闭环流程
1. **Archive（移出主库，迁入 `archive.db`）**：
   - `nex-server` 通过 SQLite `ATTACH DATABASE 'archive.db' AS archive` 开启原子事务：
     1. 将目标会话（及其派生的所有 Subagent 子会话）的 `sessions` 元数据、`session_events` 对话流、`turn_telemetry` 明细行完整写入 `archive.db`；
     2. **从主库 `main.db` 中彻底 `DELETE` 上述行**（主库体积与索引树保持精简，定期自动执行增量 `PRAGMA incremental_vacuum` 回收空闲页）；
     3. 注意：**预聚合统计表 `usage_rollups_hourly` 永久保留在 `main.db` 中不受影响**，归档或删除旧对话绝不会导致历史 Token 账单与费用统计失真！
2. **Archive 列表浏览与只读预览**：
   - 用户可在客户端切换至「已归档对话 (Archived Sessions)」视图，直接分页查询 `archive.db` 浏览历史归档，甚至支持只读点开查看内容。
3. **Unarchive（无损恢复回主库）**：
   - 用户点击 **Unarchive** 时，原子事务将该会话数据从 `archive.db` 完整迁回 `main.db` 并从冷库移除，会话立即回到活跃列表并可继续追问（Resume）。
4. **永久删除 (Delete Permanently)**：
   - 用户在归档列表中点击「永久删除」（或批量清空归档）时，从 `archive.db` 彻底移除该会话记录，并对关联的 CAS `blobs` 执行引用计数减扣（当某 `blob_hash` 不再被任何活跃或归档会话引用时，物理删除对应 `.zst` 文件释放磁盘空间）。

---

## 6. Runner 级与全局多时间窗统计体系（Today / 近 7 天 / 近 30 天）

为支撑在**全局仪表盘**以及**每个 Runner 节点卡片/详情页**直观查看：**「今天、近 7 天、近 30 天用了多少 Token、调用了哪些模型、预计花了多少钱」**，采用**小时级预聚合账本表 (`usage_rollups_hourly`)**。

> **为什么用「小时桶」而不是「UTC 自然日桶」？**  
> 如果按 UTC 日期聚合，东八区（UTC+8）用户查看「今天」的数据会差 8 个小时！按整点小时时间戳（`hour_bucket = ts - ts % 3600`）预聚合，一年单个模型在单个 Runner 上最多仅几千行，却能按任意客户端本地时区在 **< 1ms** 内精确切出「今天 00:00 至今」、「过去 7×24h / 自然 7 天」、「近 30 天」的账单！

### 6.1 统计预聚合表结构 (`main.db -> usage_rollups_hourly`)

```sql
CREATE TABLE usage_rollups_hourly (
    hour_bucket         INTEGER NOT NULL, -- Unix 秒级整点时间戳 (如 1763452800)
    runner_id           TEXT NOT NULL,    -- 归属的具体 Runner 节点 ID
    provider_id         TEXT NOT NULL,
    model_id            TEXT NOT NULL,
    trigger_source      TEXT NOT NULL,    -- 'chat' | 'subagent' | 'cron'
    -- 累计调用次数与 Token 明细
    turn_count          INTEGER NOT NULL DEFAULT 0,
    input_tokens        INTEGER NOT NULL DEFAULT 0, -- 非缓存普通输入
    output_tokens       INTEGER NOT NULL DEFAULT 0, -- 输出 (含 reasoning tokens)
    cache_read_tokens   INTEGER NOT NULL DEFAULT 0, -- 命中缓存读取
    cache_write_tokens  INTEGER NOT NULL DEFAULT 0, -- 缓存写入
    -- 累计预计费用与缓存节省 (按调用发生时刻 models 表的单价快照实时计算累加)
    estimated_cost_usd  REAL NOT NULL DEFAULT 0,
    cache_saved_usd     REAL NOT NULL DEFAULT 0,
    PRIMARY KEY (hour_bucket, runner_id, provider_id, model_id, trigger_source)
);

CREATE INDEX idx_rollups_runner_hour ON usage_rollups_hourly(runner_id, hour_bucket);
```

### 6.2 每个 Runner 统计面板直出指标

无论是打开全局统计页，还是点进某一台具体的 macOS / Linux / Windows **Runner 详情状态卡**，通过一条轻量 `GROUP BY model_id` SQL 即可实时展示 **Today / 7D / 30D** 三个切换标签页：

1. **总览核心卡**：
   - **总消耗 Token**：`Total Tokens`（拆分为 Input / Output / Cache Read）与**平均缓存命中率 (`Cache Hit %`)**。
   - **预计总花费 (`Estimated Cost`)**：基于 `models.dev`（或自定义）单价算出的美元总开销（如 `$4.82`），并附带提示 *“Prompt Cache 已为你节省 `$9.15`”*。
   - **来源构成**：交互对话 (`chat`) vs 子智能体并发 (`subagent`) vs 定时自治任务 (`cron`) 各自的消耗占比。
2. **模型使用排行明细 (`Models Breakdown`)**：
   - 列出该 Runner 在当前所选时间窗（今天 / 7 天 / 30 天）内实际使用过的所有模型列表、每个模型的调用轮次、Token 消耗占比条及各自产生的预计费用。
3. **离线自治任务不漏账**：
   - 当某台 Runner 断网独立运行了多天 Cron 定时任务后重新连上 `nex-server` 时，Outbox 补传回来的每条 `turn_telemetry` 会严格按照任务**实际发生时的 `hour_bucket`** 累加进 `usage_rollups_hourly`，确保每个 Runner 的 7 天 / 30 天账单真实完整。
