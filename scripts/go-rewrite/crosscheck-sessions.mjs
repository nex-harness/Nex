// crosscheck-sessions.mjs — Node/Go session-store interop.
//
// Direction A: Node writes a session with the real SqliteSessionStore, Go reads
// it back (ListMessages) and the row must decode to the expected JSON.
// Direction B: Go writes the same turn, Node's real SqliteSessionStore reads it
// back (getSession/listMessages) and must see the same messages.
// Both include a reopen: the reader opens the file after the writer closed it.
//
//   node scripts/go-rewrite/crosscheck-sessions.mjs
//
// Requires: npx tsx available (Node side), and `go` on PATH (Go side).
import { spawnSync } from "node:child_process";
import { mkdtempSync, mkdirSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "../..");
const GO_DIR = join(REPO, "go");

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

const root = mkdtempSync(join(tmpdir(), "xcheck-sessions-"));
process.on("exit", () => rmSync(root, { recursive: true, force: true }));

// ---------- Direction A: Node writes, Go reads ----------
{
  const dbPath = join(root, "a", "db.sqlite");
  mkdirSync(dirname(dbPath), { recursive: true });
  const outPath = join(root, "a", "go-read.json");
  run("npx", ["--yes", "tsx", join(HERE, "crosscheck-sessions-node.mts"), "write", dbPath]);
  // Reopen on the Go side (a fresh process) and dump the transcript.
  run(
    "go",
    [
      "test",
      "-buildvcs=false",
      "-count=1",
      "-run",
      "TestCrosscheckGoReadsNode",
      "./internal/adapters/store/",
    ],
    {
      cwd: GO_DIR,
      env: {
        ...process.env,
        CROSSCHECK_MODE: "go-reads-node",
        CROSSCHECK_DB: dbPath,
        CROSSCHECK_OUT: outPath,
      },
    },
  );
  const goView = JSON.parse(readFileSync(outPath, "utf8"));
  const nodeView = JSON.parse(
    run("npx", ["--yes", "tsx", join(HERE, "crosscheck-sessions-node.mts"), "read", dbPath]).stdout,
  );
  check(
    JSON.stringify(goView) === JSON.stringify(nodeView),
    "A: Go ListMessages decodes to the same messages+parts Node wrote",
  );
  check(goView.length === 2, "A: Go sees 2 messages after Node wrote the turn");
}

// ---------- Direction B: Go writes, Node reads ----------
{
  const dbPath = join(root, "b", "db.sqlite");
  mkdirSync(dirname(dbPath), { recursive: true });
  run(
    "go",
    [
      "test",
      "-buildvcs=false",
      "-count=1",
      "-run",
      "TestCrosscheckGoWritesNode",
      "./internal/adapters/store/",
    ],
    {
      cwd: GO_DIR,
      env: { ...process.env, CROSSCHECK_MODE: "go-writes-node", CROSSCHECK_DB: dbPath },
    },
  );
  // Reopen with Node's real store and read it back.
  const nodeView = JSON.parse(
    run("npx", ["--yes", "tsx", join(HERE, "crosscheck-sessions-node.mts"), "read", dbPath]).stdout,
  );
  check(nodeView.length === 2, "B: Node's real store sees the 2 messages Go wrote");
  const session = JSON.parse(
    run("npx", ["--yes", "tsx", join(HERE, "crosscheck-sessions-node.mts"), "session", dbPath])
      .stdout,
  );
  check(
    session.id === "sess_00000000-0000-4000-8000-000000000001",
    "B: Node reads the session Go created",
  );
  check(session.title === "Say hello", "B: Node reads the title Go wrote");
  const goReadOut = join(root, "b", "go-read.json");
  run(
    "go",
    [
      "test",
      "-buildvcs=false",
      "-count=1",
      "-run",
      "TestCrosscheckGoReadsNode",
      "./internal/adapters/store/",
    ],
    {
      cwd: GO_DIR,
      env: {
        ...process.env,
        CROSSCHECK_MODE: "go-reads-node",
        CROSSCHECK_DB: dbPath,
        CROSSCHECK_OUT: goReadOut,
      },
    },
  );
  const goViewB = JSON.parse(readFileSync(goReadOut, "utf8"));
  check(
    JSON.stringify(goViewB) === JSON.stringify(nodeView),
    "B: Go re-reads its own write identically to Node's view",
  );
}

console.log(
  `\n${failures.length === 0 ? "ALL CHECKS PASSED" : `${failures.length} CHECKS FAILED`}`,
);
process.exit(failures.length === 0 ? 0 : 1);
