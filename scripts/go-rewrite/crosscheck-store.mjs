// 跨引擎检查（phase-1 步骤 1.3 / 门禁 G1.4）：Node 与 Go 各自从零创建会话库，必须得到逐项相同的 schema 与迁移账本；
// 并且双方都能无迁移地打开对方创建的库。
//   node crosscheck-store.mjs <go-agent-exe>
import { spawnSync, spawn } from "node:child_process";
import { mkdtempSync, rmSync, cpSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { DatabaseSync } from "node:sqlite";

const HERE = dirname(fileURLToPath(import.meta.url));
const NODE_AGENT = resolve(HERE, "../../apps/nex-cli/packages/cli/dist/nex.cjs");
const goExe = process.argv[2];
if (!goExe) {
  console.error("usage: crosscheck-store.mjs <go-agent-exe>");
  process.exit(2);
}

function runAgent(kind, home) {
  const env = { PATH: process.env.PATH, HOME: home, USERPROFILE: home, NEX_DATA_BASE_DIR: home };
  const [cmd, args] =
    kind === "node"
      ? ["node", [NODE_AGENT, "app-server", "--stdio"]]
      : [resolve(goExe), ["app-server", "--stdio"]];
  const r = spawnSync(cmd, args, {
    env,
    input: "",
    timeout: 30000,
    encoding: "utf8",
    maxBuffer: 1 << 26,
  });
  if (r.status !== 0)
    throw new Error(`${kind} agent exited ${r.status}: ${r.stderr.slice(0, 300)}`);
  return r.stdout
    .trim()
    .split("\n")
    .filter(Boolean)
    .map((l) => JSON.parse(l))
    .filter((m) => m.method === "startup/storageState")
    .map((m) => m.params);
}
const dbPath = (home) => join(home, ".nex/cli/db/db.sqlite");
function dump(home) {
  const db = new DatabaseSync(dbPath(home), { readOnly: true });
  const out = {
    ledger: db.prepare("select id, checksum, app_version from schema_migration order by id").all(),
    objects: db
      .prepare(
        "select type, name, tbl_name, sql from sqlite_master where name not like 'sqlite_%' order by type, name",
      )
      .all(),
    integrity: db.prepare("pragma integrity_check").get().integrity_check,
  };
  db.close();
  return out;
}

const root = mkdtempSync(join(tmpdir(), "xcheck-"));
const failures = [];
const check = (ok, what) => {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}`);
  if (!ok) failures.push(what);
};
try {
  const nodeHome = join(root, "node"),
    goHome = join(root, "go");
  const nodeStates = runAgent("node", nodeHome),
    goStates = runAgent("go", goHome);
  const a = dump(nodeHome),
    b = dump(goHome);
  check(
    a.objects.length === b.objects.length && a.objects.length > 60,
    `schema objects: node ${a.objects.length}, go ${b.objects.length}`,
  );
  check(
    JSON.stringify(a.objects) === JSON.stringify(b.objects),
    "every table/index/trigger definition is byte-identical",
  );
  check(
    JSON.stringify(a.ledger) === JSON.stringify(b.ledger) && a.ledger.length === 22,
    "migration ledger (id, checksum, app_version) identical, 22 rows",
  );
  check(a.integrity === "ok" && b.integrity === "ok", "PRAGMA integrity_check ok on both");
  const phases = (s) =>
    s.map((p) => `${p.phase}${p.migration ? ":" + p.migration.kind : ""}`).join(",");
  check(phases(nodeStates) === phases(goStates), "startup/storageState phase sequence identical");

  // Node 打开 Go 建的库、Go 打开 Node 建的库：都必须是 kind=none、零迁移。
  for (const [opener, home, owner] of [
    ["node", goHome, "go"],
    ["go", nodeHome, "node"],
  ]) {
    const copy = join(root, `${opener}-opens-${owner}`);
    cpSync(home, copy, { recursive: true });
    const states = runAgent(opener, copy);
    const ready = states[states.length - 1];
    check(
      ready.phase === "ready" &&
        ready.migration.kind === "none" &&
        ready.migration.executedCount === 0 &&
        !states.some((s) => s.phase === "migrating"),
      `${opener} opens the ${owner}-created database without migrating`,
    );
  }
} finally {
  rmSync(root, { recursive: true, force: true });
}
process.exit(failures.length ? 1 : 0);
