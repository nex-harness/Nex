// crosscheck-taskindex-node.mts — the Node half of crosscheck-taskindex.mjs.
//
// It drives the *real* migration code from
// packages/services/src/session/tasksDatabase/{migrations,schema-v1,provider-selection-v2,
// official-glm-selection-v3}.ts through node:sqlite, so a drift in the frozen SQL,
// the ledger checksum or the code-level migrations fails here rather than being
// papered over by a Go reimplementation.
//
//   tsx crosscheck-taskindex-node.mts record <fixtureDir>
//   tsx crosscheck-taskindex-node.mts write <dbPath>    # write rows through TaskIndexRepo
//   tsx crosscheck-taskindex-node.mts read  <dbPath>    # read rows back as JSON on stdout
import { DatabaseSync } from "node:sqlite";
import { mkdirSync, writeFileSync, cpSync, rmSync } from "node:fs";
import { join } from "node:path";
import {
  runTasksDatabaseMigrations,
  inspectTasksMigrationKind,
  areTasksDatabaseMigrationsApplied,
} from "../../packages/services/src/session/tasksDatabase/migrations.ts";
import * as schemaV1 from "../../packages/services/src/session/tasksDatabase/schema-v1.ts";
import * as providerSelectionV2 from "../../packages/services/src/session/tasksDatabase/provider-selection-v2.ts";
import * as officialGlm from "../../packages/services/src/session/tasksDatabase/official-glm-selection-v3.ts";
import { TaskIndexRepo } from "../../packages/services/src/session/taskIndexRepo.ts";

const [, , action, arg] = process.argv;
const FIXTURE_VERSIONS = ["0001_adopt_task_schema", "0002_provider_selection"];

// ── record ────────────────────────────────────────────────────────────────────
// One database per version, produced by running the real runner with a ledger
// truncated to that version, plus the empty (fresh) database.
async function record(dir: string) {
  mkdirSync(dir, { recursive: true });
  const dumpSchema = (db: DatabaseSync) =>
    db
      .prepare(
        "select type, name, tbl_name, sql from sqlite_master where name not like 'sqlite_%' order by type, name",
      )
      .all()
      .map((row: any) => ({
        type: row.type,
        name: row.name,
        tbl_name: row.tbl_name,
        sql: row.sql ?? null,
      }));
  const dumpLedger = (db: DatabaseSync) =>
    db.prepare("select id, checksum, time_applied from tasks_schema_migration order by id").all();

  const save = (label: string, db: DatabaseSync, sourcePath: string) => {
    writeFileSync(join(dir, `node-${label}.schema.json`), JSON.stringify(dumpSchema(db), null, 1));
    writeFileSync(join(dir, `node-${label}.ledger.json`), JSON.stringify(dumpLedger(db), null, 1));
    const target = join(dir, `node-${label}.sqlite`);
    rmSync(target, { force: true });
    cpSync(sourcePath, target);
  };

  // fresh: run all three migrations from nothing.
  {
    const freshPath = join(dir, "partial-fresh.sqlite");
    rmSync(freshPath, { force: true });
    const db = new DatabaseSync(freshPath);
    runTasksDatabaseMigrations(db);
    if (inspectTasksMigrationKind(db) !== "none")
      throw new Error("fresh database is not at kind=none after migrating");
    if (!areTasksDatabaseMigrationsApplied(db))
      throw new Error("areTasksDatabaseMigrationsApplied is false on a fresh database");
    save("fresh", db, freshPath);
    db.close();
  }

  // v0001 / v0002: stop the runner at that version so Go has to apply the rest.
  for (const stopAfter of FIXTURE_VERSIONS) {
    const dirPath = join(dir, `partial-${stopAfter}.sqlite`);
    rmSync(dirPath, { force: true });
    const db = new DatabaseSync(dirPath);
    runTasksDatabaseMigrations(db, {
      onProgress(phase, migration) {
        // No early exit: the ledger is rewritten afterwards so the fixture is a
        // genuine "database that stopped at this version" without a half-applied
        // transaction, which SQLite would not allow anyway.
        void phase;
        void migration;
      },
    });
    db.prepare("delete from tasks_schema_migration where id > ?").run(stopAfter);
    db.close();

    const reread = new DatabaseSync(dirPath);
    save("v" + stopAfter, reread, dirPath);
    reread.close();
  }
  // The two code migrations have behaviour, not just DDL: record the row-level
  // result of 0002 (frozen legacy selection decoding) and 0003 (the GLM rename)
  // so the Go functions are checked against the real output, not a paraphrase.
  recordCodeMigrationVectors(dir);
  console.log(`recorded ${2 + FIXTURE_VERSIONS.length} task-index fixtures into ${dir}`);
}

type LegacyInput = [
  id: string,
  model: string | null,
  provider: string | null,
  thought: string | null,
];
const LEGACY_INPUTS: LegacyInput[] = [
  ["a1", "custom:builtin:zai:GLM-5.3", "glm", null],
  ["a2", "openai/gpt-4o$high", "openai", null],
  ["a3", "glm-4.7", "glm", null],
  ["a4", null, null, null],
  ["a5", "  ", "openai", "medium"],
  ["a6", "custom:my%20prov:my%3Amodel", "other", "low"],
  ["a7", "custom:noseparator", "other", null],
  ["a8", "plain-model", "builtin:zai", null],
  ["a9", "prov/model$", "openai", null],
  ["a10", "custom:builtin:bigmodel-start-plan:GLM-5.1", "other", null],
  ["a11", "anthropic/claude-x$", "nex", null],
];
const GLM_INPUTS: Array<[string, string]> = [
  ["g1", '{"providerId":"account:zai-start-plan","modelId":"glm-5.3"}'],
  ["g2", '{"providerId":"account:bigmodel-individual-coding-plan","modelId":"GLM-4.6V"}'],
  ["g3", '{"providerId":"openai","modelId":"glm-5.3"}'],
  ["g4", '{"providerId":"account:zai-start-plan","modelId":"glm-4.7-flashx"}'],
  ["g5", "not json"],
  ["g6", '{"providerId":"account:bigmodel-team-coding-plan","modelId":"glm-4-flash-250414"}'],
  ["g7", "null"],
];

function recordCodeMigrationVectors(dir: string) {
  const { AUTOMATION_SCHEMA } = schemaV1;
  const { importLegacyAutomationSelections } = providerSelectionV2;
  const { OFFICIAL_GLM_SELECTION_MIGRATION_SQL } = officialGlm;

  // 0002
  const legacy = new DatabaseSync(":memory:");
  legacy.exec(AUTOMATION_SCHEMA);
  legacy.exec("BEGIN IMMEDIATE");
  const insertLegacy = legacy.prepare(
    `INSERT INTO automations (automation_id,title,cron_expr,prompt,workspace_key,workspace_path,created_at,updated_at,model,provider,thought_level)
     VALUES (?, 't', '* * * * *', 'p', '/w', '/w', 1, 1, ?, ?, ?)`,
  );
  for (const row of LEGACY_INPUTS) insertLegacy.run(row[0], row[1], row[2], row[3]);
  importLegacyAutomationSelections(legacy);
  legacy.exec("COMMIT");
  writeFileSync(
    join(dir, "node-0002-provider-selection.rows.json"),
    JSON.stringify(
      legacy
        .prepare("select automation_id, model_selection from automations order by automation_id")
        .all(),
      null,
      1,
    ),
  );
  legacy.close();

  // 0003
  const glm = new DatabaseSync(":memory:");
  glm.exec(AUTOMATION_SCHEMA);
  glm.exec("BEGIN IMMEDIATE");
  const insertGlm = glm.prepare(
    `INSERT INTO automations (automation_id,title,cron_expr,prompt,workspace_key,workspace_path,created_at,updated_at,model_selection)
     VALUES (?, 't', '* * * * *', 'p', '/w', '/w', 1, 1, ?)`,
  );
  for (const row of GLM_INPUTS) insertGlm.run(row[0], row[1]);
  glm.exec(OFFICIAL_GLM_SELECTION_MIGRATION_SQL);
  glm.exec("COMMIT");
  writeFileSync(
    join(dir, "node-0003-official-glm.rows.json"),
    JSON.stringify(
      glm
        .prepare("select automation_id, model_selection from automations order by automation_id")
        .all(),
      null,
      1,
    ),
  );
  glm.close();
}

// ── write / read through the real TaskIndexRepo ──────────────────────────────
const SCENARIO = [
  {
    meta: {
      taskId: "task-alpha",
      traceId: "nex-task-alpha",
      title: "Alpha session",
      workspacePath: "/w/alpha",
      createdAt: 1000,
      updatedAt: 2000,
      mode: "build",
      model: "GLM-5.3",
      provider: "glm",
      status: "running",
    },
    searchableText: "hello alpha body",
  },
  {
    meta: {
      taskId: "task-beta",
      traceId: "nex-task-beta",
      title: "Beta session",
      workspacePath: "/w/beta",
      createdAt: 1500,
      updatedAt: 2500,
      mode: "plan",
      provider: "glm",
      status: "completed",
    },
  },
  {
    meta: {
      taskId: "task-gamma",
      traceId: "nex-task-gamma",
      title: "Gamma session",
      workspacePath: "/w/alpha",
      workspaceIdentity: "remote:ssh:host:22:root:/srv/alpha",
      createdAt: 1200,
      updatedAt: 9000,
      mode: "build",
      provider: "glm",
      status: "error",
      lastError: { code: "boom", message: "agent failed" },
    },
  },
];

async function write(dbPath: string) {
  rmSync(dbPath, { force: true });
  const repo = new TaskIndexRepo(dbPath, 5000);
  await repo.ensureReady();
  for (const { meta, searchableText } of SCENARIO) {
    await repo.syncTaskMeta({ meta, searchableText });
  }
  // pin + archive so the flags are covered too
  await repo.updateTaskState({
    workspacePath: "/w/alpha",
    taskId: "task-alpha",
    patch: { pinned: true, updatedAt: 3000 },
  });
  await repo.updateTaskState({
    workspacePath: "/w/beta",
    taskId: "task-beta",
    patch: { archived: true },
  });
  // rename + unread marker
  await repo.applyAgentPatch({
    workspacePath: "/w/alpha",
    taskId: "task-alpha",
    patch: { title: "Alpha renamed" },
  });
  repo.close({ throwOnError: true });
  console.log(`wrote ${SCENARIO.length} rows into ${dbPath}`);
}

async function read(dbPath: string) {
  const repo = new TaskIndexRepo(dbPath, 5000);
  await repo.ensureReady();
  const metas = await repo.listTaskMetas({ includeDeleted: true });
  repo.close({ throwOnError: true });
  // Normalise to a stable shape: the Go half prints the same JSON.
  const out = metas.map((meta) => ({
    taskId: meta.taskId,
    title: meta.title,
    workspacePath: meta.workspacePath,
    workspaceIdentity: meta.workspaceIdentity ?? null,
    model: meta.model ?? null,
    provider: meta.provider ?? null,
    mode: meta.mode,
    status: meta.status ?? null,
    lastErrorCode: meta.lastError?.code ?? null,
    createdAt: meta.createdAt,
    updatedAt: meta.updatedAt,
    unreadAt: meta.unreadAt ?? null,
  }));
  process.stdout.write(JSON.stringify(out, null, 1));
}

if (action === "record") await record(arg);
else if (action === "write") await write(arg);
else if (action === "read") await read(arg);
else {
  console.error("usage: crosscheck-taskindex-node.mts record|write|read <path>");
  process.exit(2);
}
