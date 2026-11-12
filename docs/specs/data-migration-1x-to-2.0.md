# 1.x → 2.0 数据升级迁移

## 目标

用户从 1.x（基于 ZCode 3.14.3）直接升级到 2.0（基于 3.17.2）时，**历史会话、任务、项目、
自动化与设置必须原样保留**，升级过程对用户表现为一段"正在升级历史数据"的加载态，不需要
任何手动操作，也不产生第二份数据目录。

## 关键决策

1. **沿用 `~/.nex/v2`，不升数据根。** 2.0 的 schema 与 1.x 共用同一个 SQLite 文件；迁移是
   `tasks-index.sqlite` 的**原地升级**（sqlite user-version 风格 journal），不是目录切换。
   不新建 `v3`，避免"同一份历史存在两个位置"。
2. **升级前自动备份。** 走既有的 `sqliteMigrations` 备份链路，写入
   `<dataRoot>/backup/tasks_schema-<ts>-<id>/`（含 `tables.sqlite` 与 `backup.json` 清单）。
   备份失败则不迁移，保持旧库可用。
3. **静默进行，只展示进度。** 不弹确认框。启动阶段由 `GlobalDatabaseStartupLoading` 展示
   `startup.database.{checking,waiting_for_lock,migrating,committing}`，完成后自动进入工作区。
4. **迁移是幂等的、可重入的。** 已应用的迁移按 id + checksum 跳过；中断后重启从断点继续。
5. **单一写入者。** 迁移只在 Host 的 storage 准备阶段执行一次；UI 不读写数据库文件。

## 迁移内容（1.2.2 / 3.14.3 → 2.0 / 3.17.2）

`tasks_schema_migration` 从 `0001–0003` 升到 `0001–0006`：

| id                                    | 作用                                                         |
| ------------------------------------- | ------------------------------------------------------------ |
| `0004_project_execution_scopes`       | 给 automations / off_peak_tasks 增加 project_id、host_id 等  |
| `0005_source_server_workspace_schema` | 建 workspace_alias / migration ledger（source-server scope） |
| `0006_task_group_admissions`          | 建 task_group_admissions                                     |

`schema_migration` 从空升到 `0005–0010`（3.17.2 的 Project 模型与 R3 远程改造）。

## 模型与 Provider 配置

- 1.x 的 Personal Provider 与模型配置在 `<dataRoot>/.nex/v2/config.json` 的 `provider` 映射中；
  复制数据目录时必须包含该文件，不能只迁移 SQLite、`setting.json` 和 CLI 正文。
- 2.0 沿用 `ProviderConfigRuntime` 的一次性导入：新 `provider_config.json` **不存在**时，
  读取旧 `config.json` 的自定义 Provider、模型成员、contextWindow 和可用 API 凭据，写入新格式；
  旧文件保留以便回滚。不得把旧模型目录或内置生成字段整块冻结为 Personal Overlay。
- 已存在的新文件是用户当前配置，即使规则为空也不得自动重新导入旧配置覆盖它。需要重新执行
  迁移时必须使用尚未生成新文件的隔离目录，不能覆盖已使用的新配置。
- 验收至少包含一个带凭据的自定义 Provider 和模型列表；断言导入后的数量、ID、模型成员与
  contextWindow，不在日志或测试输出中写凭据。旧 `config.json` 没有独立默认模型选择字段时
  不虚构选择；当前选择仍由现有模型选择服务决定。`verify:data-migration` 必须覆盖这条链路。

## 终端入口

桌面顶部终端按钮必须实际调用 `handleToggleTerminal()`，不能只返回函数引用。终端面板还需区分
本地/远程 workspace 的服务路由；点击后应显示真实终端或明确启动错误，不能静默无效。

## 必须同时修的问题：项目列表投影

`schema_migration` 的 `0005_projects` 引入 Project 模型后，`setting.json` 的
`recentProjects` / `lastWorkspaceSession` 不再是用户历史，而是由
`projectWorkspaceSettings()`（`packages/services/src/remote-projects/workspace-settings.ts`）
从 `projects` 表**重新投影**：

```ts
recentProjects: localPaths.slice(0, 10),
```

对 1.x 升级用户，`projects` 表是迁移时新建并回填的，而 `setting.json` 里的
`recentProjects` 来自旧逻辑。投影一旦落盘，**旧的项目列表被整体覆盖**——用户升级后
看到侧栏只剩一个项目，误以为「对话历史没了」（任务数据实际完好）。

修复规则：

- `recentProjects` 投影结果 = **旧值中仍存在的路径（保持原顺序）∪ 项目树路径**，再去重、
  截断到上限。旧值里已从项目树消失的路径允许丢弃（它确实被移除了）。
- `lastWorkspaceSession` 同理：项目树未覆盖到的旧条目保留在尾部，而不是整块重建。
- 该合并只在投影路径生效；用户主动增删项目时仍以项目树为准。

## Source Server 安装身份收敛

2.0 Core 的安装级 `serverId` 可能不同于 1.x 首次开发/迁移时生成的 ID。Host 身份参与
`workspaceKey`、项目 owner、CLI 会话 `workspace_id` 和配置目录 hash，不能只改 `server-info`
或 UI 过滤，否则同名空项目会遮住旧项目与历史会话。

```text
Core（当前安装 ID）
  → 只读发现 hosts 表中本库旧 server Host
  → 对每个旧项目主 root 调 SourceServerWorkspaceAliasRepo
  → 受信 legacy server Host → 当前 stable server identity
  → task-index：任务/分组/调度 + hosts/projects/remote entries 同一事务收敛
  → CLI db.sqlite：正文 workspace_id 合并
  → 会话配置目录：幂等合并
  → 发布 alias + 注册 stable ↔ canonical mapper → 开放 HTTP/WS
```

- 允许合并的旧 Host 只能由 Core 从本机 `hosts` 表发现，且必须是 `kind=server`、拥有项目或
  任务的 `remote:server` 身份；RPC 不能任意提交 server Host 要求搬运。桌面其它设备入口不改。
- 复用 `SourceServerWorkspaceAliasRepo` 的双库备份、冲突检查、事务和断点 ledger，不另建转换器。
  同路径同 taskId 的冲突沿用现有 fail-closed，不用“较新覆盖较旧”吞掉不同会话。
- 已删除项目仍需保留其历史任务归属，身份收敛不能把它恢复为可见项目。空 root 项目不创建
  workspace；只更新其 Host 归属。
- 完成后 `readTree`、`server-info`、Web 与 Desktop 均使用当前 serverId；CLI 正文数量不变。
  第二次启动只读 alias，不重复搬运或创建第二份项目。
- **桌面 Local Host 与独立 Server Core 不得混用身份。** 桌面本地项目/任务归 `local`，
  不使用 remote identity；独立 Core 的项目才归当前 server Host。不得把桌面库统一改写成
  `remote:server` 或让桌面通过 localhost attachment 打开本地目录。
- **应用数据根唯一。** Core 的 `NEX_DATA_BASE_DIR`、task-index、settings、CLI `HOME` /
  `USERPROFILE` 与 `~/.nex/cli/db/db.sqlite` 必须同属一个 base；server 安装根只负责 release，
  不得再叠加 `.nex`。Agent 环境由 ProcessManager 统一设置，覆盖 CLI 默认 `os.homedir()` 路径。
- **反向请求保持 owner 校验。** CLI 发出的 workspace 身份允许是 stable 或已登记的 canonical
  alias，Host 将其解析到同一已注册 owner 后再执行；不得因 UI 直连而跳过 workspace 所有权检查。

## 同一工作区的桌面 / Web 共享拓扑

用户可在桌面和 Web 同时打开并操作同一工作区，本地与远程都必须支持。**同目录不等于
同一工作区实例**：不能把两端分别启动的 Agent 当作共享控制，也不能靠迁移 workspace
identity 伪造共享关系。

```text
桌面本地 workspace
  Renderer ─ desktop-continuous ─┐
                                 ├─ 同一窗口 Local Host → 同一 CLI / CommandInbox
  Web /remote ─ replayable ─ Relay ─ Main ─ shared-host attachment ─┘

桌面远程 workspace
  Renderer ─ desktop-continuous ─ Window Host / remoteSessionId ─┐
                                                                ├─ 同一 Source Core / CLI
  Web /remote ─ replayable ─ Relay ─ shared remote attachment ────┘
```

- `packages/ui/src/root/rootPlatformWorkspaceSync.ts` 向 Main 同步窗口中的 local/remote
  workspace。Web 经 bootstrap 选择后调用 `workspace-bridge-open`，Main 在
  `webRemoteControlManager` 中为已有 Host 建立 attachment，不创建 mobile Host/Agent。
- `webRemoteControlSharedHostAttachments` 的 local 分支复用窗口 Host；remote 分支必须校验
  `remoteSessionId`、workspace identity 与窗口归属，再沿原 remote attachment 接入。
- Host/CLI 拥有正文、命令队列、会话事实与运行状态；Main/Relay 仅鉴权、配对、透传和调度。
  桌面 continuous 与 Web replayable 使用各自订阅、snapshot/gap 恢复，不能互相替换。
- `directServerConnection` 只表示普通 Web 已直连独立 Core，不证明与桌面共享运行状态。
  桌面若要共享该 Core 的工作区，应通过现有 Server remote attachment 接入同一 Host，
  而非同时以 `local` 另起 Agent。Relay 的账号/JWT 鉴权与 shared-host bridge 是两层问题，
  不得为让 Web 可连而关闭鉴权或暴露 Host。
- **无账号不等于免鉴权。** 2.0 的 `INERT_WEB_AUTH_PORT` 只移除 OAuth 登录 UI，不能把它当作
  Relay 配对成功的证明。Web 的 shared-host 入口需要独立 Relay 凭据来源或恢复现有账号
  token 的迁移读取；不得把模型供应商 API key 当 Relay 凭据，也不得把长期 JWT/token 放入
  二维码或 URL。受控 Relay 的测试凭据仅用于隔离验收，生产部署必须明确配对与鉴权方案。

验收必须证明：

1. 桌面本地 workspace 被 Web 打开时，命令进入同一 CLI，历史/正文/终端在两端可读写；
2. 桌面远程 workspace 被 Web 打开时，沿用同一 remoteSessionId 与 workspaceIdentity，
   不另起 Host 或远程会话；远程重连后仍恢复同一工作区；
3. bridge 换代只替换 Web 的 replayable 订阅，不杀桌面 runtime、不破坏原终端或命令队列；
4. 未配对、错误 remoteSessionId 或断开的工作区不得绕过 owner/lease 直接访问。

## 所有者与边界

| 项                      | 所有者                                                        |
| ----------------------- | ------------------------------------------------------------- |
| 数据库原地升级 + 备份   | `packages/services/src/session/tasksDatabase/migrations.ts`   |
| 启动进度状态            | `packages/desktop/src/host/database-startup/`（Host）+ shared |
| 加载态展示              | `packages/ui/src/root/GlobalDatabaseStartupLoading.tsx`       |
| `setting.json` 投影合并 | `packages/services/src/remote-projects/workspace-settings.ts` |
| 数据根解析              | `packages/services/src/paths.ts`（仍为 `v2`）                 |

## 事件顺序

```
启动 → Host storage 准备
      → inspectTasksMigrationKind(db)  ─ 判定 none | upgrade | partial
      → 若需迁移：备份到 <dataRoot>/backup/  → 逐条 apply（事务）
      → 写 tasks_schema_migration / schema_migration journal
      → report(phase) 推送 UI：checking → migrating → committing → ready
      → 打开 settings 时投影合并 recentProjects（不再整块覆盖）
      → 进入工作区
```

## 验收

可重复运行时入口：`pnpm verify:data-migration`。脚本只创建临时 HOME 与小型 1.x 数据库，
不读取真实 `~/.nex`，验证 Local/Server 迁移、CLI 正文、备份与二次执行哈希不变。
它不能替代桌面 CDP、Web 浏览器与真实副本验收；UI 还需分别检查项目列表、历史正文与输入区。

1. 用 1.x 真实数据目录（`tasks_schema_migration` 只有 0001–0003）启动 2.0：任务条数、
   归档/置顶状态、项目根均不变；`backup/` 下生成一份迁移前快照。
2. 升级后侧栏项目数 ≥ 升级前；`setting.json` 的 `recentProjects` 不缩小。
3. 重复启动不重复迁移（幂等），不会覆盖已升级库。
4. 迁移中途强杀进程后重启：从断点继续，不产生半升级状态。
5. 迁移失败（如磁盘满）时旧库保持可读，UI 显示 `startup.database.failed` 与重试入口。
6. 迁移前后 `tasks-index.sqlite` 的 `tasks` 行数与 `deleted=0` 行数一致。

### 验收记录（2026-11-12，隔离 HOME，真实 `~/.nex` 未被读写）

数据来源：真实数据目录里 2.0 首次迁移前自动留下的 `backup/tasks_index-*`（390 个任务、8 个任务组，
即 1.2.2 时期的任务库）与一份 2026-07-20 的更早快照（176 个任务）。两者都只拷贝到临时目录；
`setting.json` 也是拷贝。运行的是 `migrateApplicationStorage` 本身，不是模拟。

| 验收项                          | 结果                                                                                                              |
| ------------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| 1 任务条数不变、生成迁移前快照 | 通过：390 → 390，176 → 176；`backup/` 下生成 `tasks_index-*` 与 `tasks_schema-*`                                   |
| 2 项目数、recentProjects        | 通过（数据层）：投影出 18 个项目；`recentProjects` 10 项保留。侧栏实际渲染**未验证**                              |
| 3 幂等                          | 通过：第二次执行后任务数不变，两次都 `backupSkipped=false`                                                        |
| 4 迁移中途强杀后续跑            | **未验证**                                                                                                        |
| 5 迁移失败时旧库可读与重试入口  | **未验证**                                                                                                        |
| 6 `tasks` 与 `deleted=0` 行数   | 通过：任务数一致；任务组 8、成员 21、排序 301 条全部保留；`PRAGMA integrity_check` 为 ok                          |
| 迁移日志                        | `tasks_schema_migration` 升到 0001–0004、0006；`schema_migration` 升到 0005–0010                                  |
| `pnpm verify:data-migration`    | 通过（合成数据：Local/Server、CLI 正文、Provider 与模型配置、备份、幂等）                                         |

没有覆盖的部分：

- 真实的 1.2.2 安装包原始数据目录。本机 `~/.nex` 已被开发版 2.0 运行过，不是纯 1.2.2 状态，所以用迁移前备份代替。
- CLI 会话正文（`~/.nex/cli/db/db.sqlite`，约 1.4 GB）的真实体量迁移：只在合成数据上验证。
- 真实 Provider 凭据与模型列表的导入：只在合成凭据上验证。
- 桌面端界面（项目侧栏、历史正文、输入区）、迁移加载态与失败重试页面。

### 打包产物冒烟启动（2026-11-12，release 版 `Nex.app`，隔离 HOME）

对象：`NEX_ENV=production` 构建的 `Nex-2.0.0-mac-arm64`（未签名）。数据全部是副本：2.0 首次迁移前
留下的任务库备份（390 个任务、8 个任务组）、真实的 `setting.json`、1.x `config.json`（7 个 Provider）、
真实 CLI 对话库的一致性拷贝（`.backup`，673 个会话、118,557 条消息，约 1.4 GB）。启动参数：
`env -i HOME=… NEX_DATA_BASE_DIR=… NEX_DESKTOP_USER_DATA_DIR=… NEX_DESKTOP_HOME_DIR=…`，调试端口 9333。

| 检查项                         | 结果                                                                                                         |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------ |
| 应用能否启动                   | 通过：窗口创建、`dom-ready`、本地服务就绪；渲染进程 0 个未捕获异常                                           |
| 任务库迁移                     | 通过：升级到 `schema_migration` 0005–0010，任务 390 → 391、项目 18、任务组 8                                 |
| 侧栏                           | 通过：置顶会话 8 条、18 个项目及各自会话按预期渲染（截图核对），无迁移失败页                                 |
| 历史对话正文                   | 通过：点开一条置顶会话，正文（用户消息、工具结果、表格、代码块）与右侧进度面板完整渲染，输入框可用          |
| 1.x Provider 导入              | 部分通过：输入区显示了模型 `deepseek-v4-flash`；Provider 数量与凭据是否逐项一致**未核对**                    |
| 迁移加载态与失败重试页         | **未验证**（这次迁移很快，没有观察到加载态）                                                                 |
| 迁移中途强杀、磁盘满           | **未验证**                                                                                                   |

冒烟中的两个告警，核实后的结论（第一次记录写成了"未修复问题"，那是错的，已更正）：

1. **`ON CONFLICT clause does not match any PRIMARY KEY or UNIQUE constraint`（8 个里失败 2 个）**：**夹具伪像，不是产品缺陷。**
   第一次用的是 2.0 迁移前自动留下的 `tasks_index` 备份，它只是数据导出，没有 1.2.2 `tasks` 表的
   `PRIMARY KEY (workspace_key, task_id)`。用 1.2.2 的真实 DDL（`v1.2.2:…/schema-v1.ts`）重建同一份数据
   （390 个任务、8 个任务组，没有行被真实主键拒绝）后，再次冷启动打包应用，该告警 0 次；
   迁移后 `tasks` 仍保留主键。迁移里"历史 task ID 存在冲突，host/task 索引暂用非唯一索引"的提示仍会出现
   （1.2.2 数据里有 27 个 `task_id` 同时出现在两个 workspace），这是设计内的降级，不影响写入。
2. **多出的空任务行与 `meta_json 非法`**：第二次冷启动仍多出 2 行。
   - `sess_c3acf936…`：有标题和时间，在 CLI 对话库里确有对应会话——是 sessions-index 基线补齐的正常结果。
   - `sess_bc1bc371…`：标题为空、创建时间为 0（显示为 1970）、`meta_json` 为 `{}`，CLI 对话库里**没有**这个会话。
     日志显示它来自渲染进程恢复"上次打开的会话"（`lastWorkspaceSession`，取自我拷贝的现行 `setting.json`），
     而这份旧任务库里没有该会话，应用于是为它创建了空行并报 `meta_json 非法`。
     这是"现行设置 + 旧库"混搭造成的；真正的 1.2.2 设置不会指向 2.0 才产生的会话。
     但它也说明：**恢复一个不存在的会话时，应用会留下一条空任务行**。是否要在产品层拦截，尚未决定。

另：第一次启动我只传了 `--user-data-dir`，没有清掉 shell 里继承的 `NEX_AGENT_SERVER_COMMAND` 等变量，
应用因此报 `Storage preparation failed: unsupported_runtime`。这是测试环境污染，不是产品缺陷；
但该次启动误写了真实 `~/Library/Application Support/Nex/session/` 下的 Chromium 缓存文件
（GPU/Dawn 缓存、Local Storage 日志、Cookies 等，1.2.2 也使用该目录）。`~/.nex` 本身未被它修改。
此后全部使用 `env -i` 加 `NEX_DESKTOP_USER_DATA_DIR` / `NEX_DESKTOP_HOME_DIR` 完整隔离。

仍然没有覆盖：真实 Provider 凭据逐项核对、迁移加载态与失败重试页、强杀续跑、签名与公证后的安装体验、
Windows/Linux。
