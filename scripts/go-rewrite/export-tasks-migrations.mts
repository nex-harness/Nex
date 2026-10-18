// 把 tasksDatabase 里冻结的迁移输入原样导出成 Go 里 //go:embed 的文件，并写出 manifest。
//
// tasks-index.sqlite 的账本校验和不是「SQL 的 sha256」——migrations.ts 刻意用
// `JSON.stringify(migration.checksumInput)` 的 sha256（冻结的语义输入，禁用
// function.toString 哈希，Electron/SEA 打包会改变函数文本）。Go 侧要复现同一校验和，
// 必须对与 Node 完全相同的 JSON 字节流求 sha256。
//
// 因此本脚本把 Node 的 `JSON.stringify(checksumInput)` 结果逐字节写成
// `checksum-input.json`，Go 直接对该文件求 sha256；两引擎因此一致，
// 一个字节都不能漂移。DDL（0001）里的 ALTER/backfill 用 Go 函数实现（adopt.go），
// 与 Phase 1 处理 code-level migrations 一样。
//
//   node_modules/.bin/tsx scripts/go-rewrite/export-tasks-migrations.mts
import { createHash } from "node:crypto";
import { mkdirSync, writeFileSync, rmSync } from "node:fs";

const schema = await import("../../packages/services/src/session/tasksDatabase/schema-v1.ts");
const glm =
  await import("../../packages/services/src/session/tasksDatabase/official-glm-selection-v3.ts");

const columns: ReadonlyArray<readonly [string, string, string]> = [
  ["tasks", "title_overridden", "INTEGER NOT NULL DEFAULT 0"],
  ["tasks", "last_unread_at", "INTEGER NOT NULL DEFAULT 0"],
  ["tasks", "searchable_text", "TEXT NOT NULL DEFAULT ''"],
  ["tasks", "cron_automation_id", "TEXT"],
  ["tasks", "off_peak_task_id", "TEXT"],
  ["automations", "target_task_id", "TEXT"],
  ["automations", "bot_delivery_target", "TEXT"],
  ["automations", "mode", "TEXT"],
  ["automations", "end_at", "INTEGER"],
  ["automations", "schedule_rule", "TEXT"],
  ["automations", "schedule_edited_by_user", "INTEGER NOT NULL DEFAULT 0"],
  ["automations", "thought_level", "TEXT"],
  ["automations", "model_selection", "TEXT"],
  ["automations", "scheduled_run_count", "INTEGER NOT NULL DEFAULT 0"],
  ["automation_runs", "model_selection", "TEXT"],
  ["off_peak_tasks", "thought_level", "TEXT"],
  ["off_peak_tasks", "model_selection", "TEXT"],
  ["off_peak_tasks", "history_deleted_at", "INTEGER"],
];
const indexes = `
  CREATE INDEX IF NOT EXISTS idx_tasks_cron_automation ON tasks(cron_automation_id, updated_at DESC)
    WHERE cron_automation_id IS NOT NULL AND deleted=0;
  CREATE INDEX IF NOT EXISTS idx_tasks_off_peak_task ON tasks(off_peak_task_id, updated_at DESC)
    WHERE off_peak_task_id IS NOT NULL AND deleted=0;
  CREATE INDEX IF NOT EXISTS idx_automations_target_task ON automations(target_task_id) WHERE target_task_id IS NOT NULL;
`;
const terminalStatuses = "'completed','failed','cancelled'";
const activePredicate = `session_id IS NOT NULL AND status NOT IN (${terminalStatuses})`;
const boundIndex = `CREATE UNIQUE INDEX IF NOT EXISTS idx_off_peak_bound_active ON off_peak_tasks(workspace_key,session_id) WHERE ${activePredicate}`;

const definitions = [
  {
    id: "0001_adopt_task_schema",
    // 0001 没有单条 SQL：adoptSchema 依次 exec 三段 schema、ALTER 补列、建索引、
    // 再按去重裁决建 boundIndex（Go 侧是 adopt.go 里的函数，不是这里的 SQL 文件）。
    checksumInput: [
      schema.TASK_INDEX_SCHEMA,
      schema.AUTOMATION_SCHEMA,
      schema.OFF_PEAK_SCHEMA,
      columns,
      indexes,
      boundIndex,
      "scheduled-count-backfill-v1",
    ],
  },
  {
    id: "0002_provider_selection",
    checksumInput: ["legacy-automation-selection-v1", "no-provider-for-legacy-off-peak-v1"],
  },
  {
    id: "0003_official_glm_selection",
    checksumInput: [glm.OFFICIAL_GLM_SELECTION_MIGRATION_SQL],
  },
] as const;

const out = new URL("../../go/internal/adapters/taskindex/migrations/", import.meta.url);
rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });

const manifest = definitions.map((m) => {
  const inputJSON = JSON.stringify(m.checksumInput);
  writeFileSync(new URL(`${m.id}.checksum-input.json`, out), inputJSON);
  const checksum = createHash("sha256").update(inputJSON).digest("hex");
  return { id: m.id, checksum, inputBytes: Buffer.byteLength(inputJSON) };
});
// 0003 的实际执行 SQL 就是它 checksumInput[0]，单列出来方便 Go 直接 exec；
// 0002 是 Go 函数（importLegacyAutomationSelections），0001 是 Go 函数（adoptSchema）。
writeFileSync(
  new URL("0003_official_glm_selection.sql", out),
  glm.OFFICIAL_GLM_SELECTION_MIGRATION_SQL,
);
// 0001 的三段 schema 与索引块也按原文导出：Go 的 adoptSchema 直接 exec 这些文件，
// 保证与 Node 的 db.exec(TASK_INDEX_SCHEMA + …) 字节一致（sqlite_master 里的 sql 文本
// 必须逐字节相同，跨引擎 schema 比对依赖这一点）。
writeFileSync(new URL("task_index_schema.sql", out), schema.TASK_INDEX_SCHEMA);
writeFileSync(new URL("automation_schema.sql", out), schema.AUTOMATION_SCHEMA);
writeFileSync(new URL("off_peak_schema.sql", out), schema.OFF_PEAK_SCHEMA);
writeFileSync(new URL("task_index_indexes.sql", out), indexes);
writeFileSync(new URL("bound_index.sql", out), boundIndex);
writeFileSync(new URL("manifest.json", out), JSON.stringify(manifest, null, 1) + "\n");
console.log(`exported ${manifest.length} task-index migrations`);
