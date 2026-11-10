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

| id                                  | 作用                                                        |
| ----------------------------------- | ----------------------------------------------------------- |
| `0004_project_execution_scopes`     | 给 automations / off_peak_tasks 增加 project_id、host_id 等 |
| `0005_source_server_workspace_schema` | 建 workspace_alias / migration ledger（source-server scope）|
| `0006_task_group_admissions`        | 建 task_group_admissions                                    |

`schema_migration` 从空升到 `0005–0010`（3.17.2 的 Project 模型与 R3 远程改造）。

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

## 所有者与边界

| 项                        | 所有者                                                        |
| ------------------------- | ------------------------------------------------------------- |
| 数据库原地升级 + 备份     | `packages/services/src/session/tasksDatabase/migrations.ts`   |
| 启动进度状态              | `packages/desktop/src/host/database-startup/`（Host）+ shared |
| 加载态展示                | `packages/ui/src/root/GlobalDatabaseStartupLoading.tsx`       |
| `setting.json` 投影合并   | `packages/services/src/remote-projects/workspace-settings.ts` |
| 数据根解析                | `packages/services/src/paths.ts`（仍为 `v2`）                 |

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

1. 用 1.x 真实数据目录（`tasks_schema_migration` 只有 0001–0003）启动 2.0：任务条数、
   归档/置顶状态、项目根均不变；`backup/` 下生成一份迁移前快照。
2. 升级后侧栏项目数 ≥ 升级前；`setting.json` 的 `recentProjects` 不缩小。
3. 重复启动不重复迁移（幂等），不会覆盖已升级库。
4. 迁移中途强杀进程后重启：从断点继续，不产生半升级状态。
5. 迁移失败（如磁盘满）时旧库保持可读，UI 显示 `startup.database.failed` 与重试入口。
6. 迁移前后 `tasks-index.sqlite` 的 `tasks` 行数与 `deleted=0` 行数一致。
