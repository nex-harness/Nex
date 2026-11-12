/* 只操作临时目录的 1.x→2.0 可重复运行时验收；不读取或修改真实 ~/.nex。 */
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdirSync, writeFileSync } from "node:fs";
import { mkdtemp, mkdir, readdir, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DatabaseSync } from "node:sqlite";
import {
  createProviderConfigRuntime,
  createSettingService,
  migrateApplicationStorage,
  setDataBaseDir,
  SourceServerWorkspaceAliasRepo,
} from "../packages/services/src/node.js";
import { buildServerRemoteWorkspaceIdentity } from "../packages/shared/src/remote-workspace-identity.js";
import { readLegacyNexConfigProviders } from "../packages/services/src/model-provider/legacyNexConfigProviderReader.js";

const base = await mkdtemp(join(tmpdir(), "nex-1x-to-2x-verification-"));
const originalHome = process.env.HOME;
const originalBase = process.env.NEX_DATA_BASE_DIR;
const serverId = "2x-installation";
const oldHost = "remote:server:1x-installation";
const projectPath = join(base, "project");
const defaultPath = join(base, "default");

function seed(baseDir: string, remote: boolean) {
  const configDir = join(baseDir, ".nex", "v2");
  mkdirSync(join(configDir, "sessions"), { recursive: true });
  mkdirSync(join(baseDir, ".nex", "cli", "db"), { recursive: true });
  // 1.x 的 Personal Provider 在 config.json；测试凭据为合成占位值，不读取真实用户文件。
  writeFileSync(
    join(configDir, "config.json"),
    JSON.stringify({
      provider: {
        "legacy-custom": {
          id: "legacy-custom",
          name: "Legacy custom",
          kind: "openai",
          source: "custom",
          options: { baseURL: "http://127.0.0.1:65534/v1", apiKey: "synthetic-test-key" },
          models: {
            "legacy-model": { id: "legacy-model", limit: { context: 32768 } },
          },
        },
      },
    }),
    { mode: 0o600 },
  );
  const host = remote ? oldHost : "local";
  const workspaceKey = remote ? `${oldHost}:${projectPath}` : projectPath;
  const db = new DatabaseSync(join(configDir, "tasks-index.sqlite"));
  db.exec(`CREATE TABLE tasks (
    workspace_key TEXT NOT NULL, workspace_path TEXT NOT NULL, workspace_identity TEXT,
    task_id TEXT NOT NULL, title TEXT NOT NULL DEFAULT '', task_status TEXT, provider TEXT,
    mode TEXT NOT NULL DEFAULT 'build', model TEXT, migration_source TEXT, forked_from_task_id TEXT,
    created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, unread_at INTEGER,
    last_unread_at INTEGER NOT NULL DEFAULT 0, pinned INTEGER NOT NULL DEFAULT 0,
    archived INTEGER NOT NULL DEFAULT 0, deleted INTEGER NOT NULL DEFAULT 0,
    title_overridden INTEGER NOT NULL DEFAULT 0, meta_json TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY(workspace_key, task_id));
    CREATE TABLE hosts(host_id TEXT PRIMARY KEY,kind TEXT NOT NULL,target_json TEXT,
      membership_id TEXT,position INTEGER NOT NULL DEFAULT 0,last_connection_status TEXT,
      last_connection_error TEXT,last_opened_at INTEGER);
    CREATE TABLE projects(project_id TEXT PRIMARY KEY,host_id TEXT NOT NULL REFERENCES hosts(host_id),
      name TEXT NOT NULL,name_overridden INTEGER NOT NULL DEFAULT 0,position INTEGER NOT NULL,
      pinned INTEGER NOT NULL DEFAULT 0,pinned_at INTEGER,deleted INTEGER NOT NULL DEFAULT 0,
      deleted_at INTEGER,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL);
    CREATE TABLE project_roots(project_id TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
      root_path TEXT NOT NULL,position INTEGER NOT NULL,is_primary INTEGER NOT NULL DEFAULT 0,
      PRIMARY KEY(project_id,root_path));
    CREATE TABLE remote_project_entries(entry_id TEXT PRIMARY KEY,host_id TEXT NOT NULL,
      remote_project_id TEXT,cached_name TEXT NOT NULL,cached_roots_json TEXT NOT NULL DEFAULT '[]',
      legacy_workspace_json TEXT,last_synced_at INTEGER,
      UNIQUE(host_id,remote_project_id));`);
  db.prepare("INSERT INTO hosts(host_id,kind) VALUES(?,?)").run(host, remote ? "server" : "local");
  db.prepare(
    "INSERT INTO projects(project_id,host_id,name,position,created_at,updated_at) VALUES('project',?,'project',0,1,1)",
  ).run(host);
  db.prepare(
    "INSERT INTO project_roots(project_id,root_path,position,is_primary) VALUES('project',?,0,1)",
  ).run(projectPath);
  const add =
    db.prepare(`INSERT INTO tasks(workspace_key,workspace_path,task_id,title,provider,created_at,updated_at)
    VALUES(?,?,?,'old-session','glm',10,20)`);
  add.run(workspaceKey, projectPath, "old-session");
  add.run(defaultPath, defaultPath, "default-session");
  db.close();
  const cli = new DatabaseSync(join(baseDir, ".nex", "cli", "db", "db.sqlite"));
  cli.exec(`CREATE TABLE session(id TEXT PRIMARY KEY,project_id TEXT NOT NULL,workspace_id TEXT,
    parent_id TEXT,slug TEXT NOT NULL,directory TEXT NOT NULL,path TEXT,title TEXT NOT NULL,
    version TEXT NOT NULL,share_url TEXT,summary_additions INTEGER,time_created INTEGER NOT NULL,
    time_updated INTEGER NOT NULL,time_deleted INTEGER);
    CREATE TABLE message(id TEXT PRIMARY KEY,session_id TEXT NOT NULL,role TEXT NOT NULL,time_created INTEGER NOT NULL);`);
  for (const id of ["old-session", "default-session"]) {
    const directory = id === "old-session" ? projectPath : defaultPath;
    cli
      .prepare(
        "INSERT INTO session(id,project_id,workspace_id,slug,directory,title,version,time_created,time_updated) VALUES(?, 'project', NULL, ?, ?, 'old', '1', 1, 1)",
      )
      .run(id, id, directory);
    cli
      .prepare("INSERT INTO message(id,session_id,role,time_created) VALUES(?,?,'user',1)")
      .run(`${id}-message`, id);
  }
  cli.close();
}

function hashRows(db: DatabaseSync, table: string) {
  const exists = db.prepare("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?").get(table);
  if (!exists) return "absent";
  return createHash("sha256")
    .update(JSON.stringify(db.prepare(`SELECT * FROM ${table} ORDER BY rowid`).all()))
    .digest("hex");
}

async function migrate(baseDir: string, remote: boolean) {
  process.env.HOME = baseDir;
  process.env.NEX_DATA_BASE_DIR = baseDir;
  setDataBaseDir(baseDir);
  const providerPath = join(baseDir, ".nex", "v2", "provider_config.json");
  const providerRuntime = createProviderConfigRuntime({
    nexBuiltinFilePath: join(baseDir, "unused-builtin-provider-config.json"),
    personalFilePath: providerPath,
    watch: false,
    personalPollingIntervalMs: false,
    readLegacyProviders: readLegacyNexConfigProviders,
  });
  const providerConfig = await providerRuntime.personalRepository.read();
  const providerRule = providerConfig.providers.getRule("legacy-custom");
  assert.equal(providerRule?.providerName, "Legacy custom");
  assert.deepEqual(providerRule?.config.personalModelIds, ["legacy-model"]);
  assert.equal(providerRule?.config.access?.type, "api-key");
  assert.ok(providerRule?.config.access && "apiKey" in providerRule.config.access);
  assert.equal(providerRule?.config.access.apiKey, "synthetic-test-key");
  assert.deepEqual(
    providerConfig.models.getExactRule("legacy-custom", "legacy-model")?.config.properties
      ?.contextWindow,
    32768,
  );
  const providerHash = createHash("sha256")
    .update(await readFile(providerPath))
    .digest("hex");
  const reread = await providerRuntime.personalRepository.read();
  assert.deepEqual(reread.providers.getRule("legacy-custom")?.config.personalModelIds, [
    "legacy-model",
  ]);
  assert.equal(reread.defaultModelSelection, undefined);
  assert.equal(
    createHash("sha256")
      .update(await readFile(providerPath))
      .digest("hex"),
    providerHash,
    "re-reading imported provider config must not rewrite it",
  );
  providerRuntime.dispose();
  const settings = await createSettingService().get();
  const hostId = remote ? oldHost : "local";
  await migrateApplicationStorage(settings, hostId);
  const repo = new SourceServerWorkspaceAliasRepo();
  if (remote) await repo.migrateLegacyServerHosts(serverId);
  const taskDb = new DatabaseSync(join(baseDir, ".nex", "v2", "tasks-index.sqlite"));
  const cliDb = new DatabaseSync(join(baseDir, ".nex", "cli", "db", "db.sqlite"));
  const before = Object.fromEntries(
    ["tasks", "projects", "hosts", "workspace_alias", "workspace_migration_ledger"].map((table) => [
      table,
      hashRows(taskDb, table),
    ]),
  );
  if (remote) await repo.migrateLegacyServerHosts(serverId);
  else await migrateApplicationStorage(settings, hostId);
  const after = Object.fromEntries(
    ["tasks", "projects", "hosts", "workspace_alias", "workspace_migration_ledger"].map((table) => [
      table,
      hashRows(taskDb, table),
    ]),
  );
  assert.deepEqual(after, before, "second migration must not rewrite persisted state");
  assert.equal(taskDb.prepare("PRAGMA integrity_check").get().integrity_check, "ok");
  assert.deepEqual(taskDb.prepare("PRAGMA foreign_key_check").all(), []);
  const backups = await readdir(join(baseDir, ".nex", "backup"));
  assert.ok(
    backups.some((name) => name.startsWith("tasks_index-")),
    "task backup must exist",
  );
  assert.ok(
    backups.some((name) => name.startsWith("tasks_schema-")),
    "schema backup must exist",
  );
  if (remote) {
    const identityBackups = await readdir(join(baseDir, ".nex", "server", "migration-backups"));
    assert.ok(identityBackups.length > 0, "identity migration must back up both databases");
  }
  assert.equal(cliDb.prepare("SELECT count(*) AS count FROM session").get().count, 2);
  assert.equal(cliDb.prepare("SELECT count(*) AS count FROM message").get().count, 2);
  if (remote) {
    const canonical = buildServerRemoteWorkspaceIdentity({ serverId, workspacePath: projectPath });
    assert.equal(
      taskDb.prepare("SELECT workspace_key FROM tasks WHERE task_id='old-session'").get()
        .workspace_key,
      canonical,
    );
    assert.equal(
      taskDb.prepare("SELECT host_id FROM projects").get().host_id,
      `remote:server:${serverId}`,
    );
    assert.equal(
      cliDb.prepare("SELECT workspace_id FROM session WHERE id='old-session'").get().workspace_id,
      canonical,
    );
    assert.equal(
      taskDb.prepare("SELECT count(*) AS count FROM hosts WHERE host_id=?").get(oldHost).count,
      0,
    );
  } else {
    assert.equal(
      taskDb.prepare("SELECT host_id FROM tasks WHERE task_id='old-session'").get().host_id,
      "local",
    );
    assert.equal(
      cliDb.prepare("SELECT workspace_id FROM session WHERE id='old-session'").get().workspace_id,
      null,
    );
  }
  taskDb.close();
  cliDb.close();
}

try {
  for (const remote of [false, true]) {
    const home = join(base, remote ? "server" : "local");
    await mkdir(home, { recursive: true });
    seed(home, remote);
    await migrate(home, remote);
  }
  console.log(
    "1.x→2.0 isolated migration verified: local/server, CLI messages, provider/model config, backups, idempotency",
  );
} finally {
  setDataBaseDir(null);
  if (originalHome === undefined) delete process.env.HOME;
  else process.env.HOME = originalHome;
  if (originalBase === undefined) delete process.env.NEX_DATA_BASE_DIR;
  else process.env.NEX_DATA_BASE_DIR = originalBase;
  await rm(base, { recursive: true, force: true });
}
