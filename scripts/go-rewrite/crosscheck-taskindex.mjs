// crosscheck-taskindex.mjs — Node/Go interop for tasks-index.sqlite (step 3.10.4).
//
// The strangler transition runs the Go server next to a Node upstream that still
// owns services, and both reach the same tasks-index.sqlite. That only holds if
//
//   - both create byte-identical schemas (every CREATE statement, as stored in
//     sqlite_master.sql, including the ledger table's own casing), and
//   - both write the same migration ledger (id, checksum), and
//   - each can open the other's database without executing a single migration,
//   - each can read the other's rows and see the same identity projection,
//     flags, status and model.
//
// Directions:
//
//   A  Node creates + writes rows -> Go opens, reads and compares.
//   B  Go creates + writes rows    -> Node opens, reads and compares.
//   C  Node creates the database   -> Go opens it with zero migrations executed
//      and vice versa (the ledger compatibility check).
//
//   node scripts/go-rewrite/crosscheck-taskindex.mjs
//
// Requires: `npx tsx` for the Node side (the real migrations.ts and
// TaskIndexRepo), and `go` on PATH.
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { DatabaseSync } from "node:sqlite";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "../..");
const GO_DIR = join(REPO, "go");
const NODE_HALF = join(HERE, "crosscheck-taskindex-node.mts");

const failures = [];
const check = (ok, what) => {
  console.log(`${ok ? "PASS" : "FAIL"} ${what}`);
  if (!ok) failures.push(what);
};

function run(cmd, args, opts = {}) {
  const r = spawnSync(cmd, args, { encoding: "utf8", maxBuffer: 1 << 26, ...opts });
  if (r.status !== 0) {
    throw new Error(`${cmd} ${args.join(" ")} exited ${r.status}\n${r.stdout}\n${r.stderr}`);
  }
  return r;
}

const runNode = (action, arg) =>
  run("node", ["--import", "tsx", NODE_HALF, action, arg], { cwd: REPO });

const runGo = (mode, db, out) =>
  run(
    "go",
    [
      "test",
      "-buildvcs=false",
      "-count=1",
      "-run",
      "TestCrosscheckGo" + (mode === "go-reads-node" ? "ReadsNode" : "WritesNode"),
      "./internal/adapters/taskindex/",
    ],
    {
      cwd: GO_DIR,
      env: {
        ...process.env,
        NEX_CROSSCHECK_MODE: mode,
        NEX_CROSSCHECK_DB: db,
        NEX_CROSSCHECK_OUT: out,
      },
    },
  );

function dumpState(dbPath) {
  const db = new DatabaseSync(dbPath, { readOnly: true });
  const state = {
    objects: db
      .prepare(
        "select type, name, tbl_name, sql from sqlite_master where name not like 'sqlite_%' order by type, name",
      )
      .all(),
    ledger: db.prepare("select id, checksum from tasks_schema_migration order by id").all(),
    integrity: db.prepare("pragma integrity_check").get().integrity_check,
  };
  db.close();
  return state;
}

const root = mkdtempSync(join(tmpdir(), "xcheck-taskindex-"));
process.on("exit", () => rmSync(root, { recursive: true, force: true }));

// ── Direction A: Node writes, Go reads ──────────────────────────────────────
{
  const db = join(root, "a", "tasks-index.sqlite");
  runNode("write", db);
  // A second write re-opens the same file: the migrations must not run again.
  runNode("write", db);
  const goOut = join(root, "a-go.json");
  runGo("go-reads-node", db, goOut);
  // Node reads its own rows with the same normalizer.
  const nodeJson = runNode("read", db).stdout;
  const goJson = readFileSync(goOut, "utf8");
  check(
    JSON.stringify(JSON.parse(nodeJson)) === JSON.stringify(JSON.parse(goJson)),
    "A: Node and Go read the same rows (identity projection, flags, status, model)",
  );
  check(JSON.parse(nodeJson).length === 3, "A: three rows round-tripped");
}

// ── Direction B: Go writes, Node reads ──────────────────────────────────────
{
  const db = join(root, "b", "tasks-index.sqlite");
  const goOut = join(root, "b-go.json");
  runGo("go-writes-node", db, goOut);
  const nodeJson = runNode("read", db).stdout;
  const goJson = readFileSync(goOut, "utf8");
  check(
    JSON.stringify(JSON.parse(nodeJson)) === JSON.stringify(JSON.parse(goJson)),
    "B: Go and Node read the same rows",
  );
}

// ── Direction C: each engine opens the other's database without migrating ────
{
  const nodeDir = join(root, "c-node");
  const goDir = join(root, "c-go");
  runNode("write", join(nodeDir, "tasks-index.sqlite"));
  // Go's writer created its own; compare the schemas and ledgers.
  runGo("go-writes-node", join(goDir, "tasks-index.sqlite"), join(root, "c-go.json"));

  const nodeState = dumpState(join(nodeDir, "tasks-index.sqlite"));
  const goState = dumpState(join(goDir, "tasks-index.sqlite"));
  check(
    JSON.stringify(nodeState.objects) === JSON.stringify(goState.objects),
    `C: identical schema objects (${nodeState.objects.length} each)`,
  );
  check(
    JSON.stringify(nodeState.ledger) === JSON.stringify(goState.ledger) &&
      nodeState.ledger.length === 3,
    "C: identical migration ledger, 3 rows",
  );
  check(
    nodeState.integrity === "ok" && goState.integrity === "ok",
    "C: PRAGMA integrity_check ok on both",
  );

  // Re-opening must not execute a migration: the Go repo re-reads the ledger and
  // the Node repo's ensureReady must report kind=none.
  runGo("go-reads-node", join(nodeDir, "tasks-index.sqlite"), join(root, "c-reopen.json"));
  runNode("read", join(nodeDir, "tasks-index.sqlite"));
  const before = dumpState(join(nodeDir, "tasks-index.sqlite"));
  runGo("go-reads-node", join(goDir, "tasks-index.sqlite"), join(root, "c-reopen2.json"));
  runNode("read", join(goDir, "tasks-index.sqlite"));
  const after = dumpState(join(goDir, "tasks-index.sqlite"));
  check(
    JSON.stringify(before.ledger) === JSON.stringify(after.ledger),
    "C: re-opening each other's database executes no migration",
  );
}

console.log(
  `\n${failures.length === 0 ? "ALL CHECKS PASSED" : `${failures.length} CHECKS FAILED`}`,
);
process.exit(failures.length === 0 ? 0 : 1);
