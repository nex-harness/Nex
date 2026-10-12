// crosscheck-webauth-sessions.mjs — Node/Go web-sessions.json interop (step 2.6).
//
// The strangler transition runs both servers against the same state owner
// (docs/specs/web-token-login.md, decision D8): a user who logs into the Node
// server must be logged into the Go front, and vice versa. That only holds if both
// implementations agree on
//
//   - tokenFingerprint = sha256("nex-web-session:" + token)
//   - idHash           = sha256(sessionId)
//   - the file shape    {version:1, tokenFingerprint, sessions:[{idHash,createdAt,expiresAt}]}
//   - the write         mode 0600, temp file + rename, trailing newline
//
// This script drives the *real* createWebSessionStore from
// packages/server/src/webAuth.ts on the Node side and go/internal/server/webauth on
// the Go side, in both directions:
//
//   Direction A  Node creates a session -> Go must validate and revoke it.
//   Direction B  Go creates a session    -> Node must validate it.
//
//   node scripts/go-rewrite/crosscheck-webauth-sessions.mjs
//
// Requires: `npx tsx` for the Node side (webAuth.ts imports hono/node-server, so
// tsx is needed) and `go` on PATH.
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
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

const root = mkdtempSync(join(tmpdir(), "xcheck-webauth-"));
process.on("exit", () => rmSync(root, { recursive: true, force: true }));

const TOKEN = "crosscheck-token";

// ---------------------------------------------------------------- Direction A
// The Node half: create a session with the real TS store, print its id.
{
  const sessionsFile = join(root, "a", "web-sessions.json");
  const out = run("npx", [
    "--yes",
    "tsx",
    join(HERE, "crosscheck-webauth-sessions-node.mts"),
    "create",
    TOKEN,
    sessionsFile,
  ]);
  const { sessionId } = JSON.parse(out.stdout);

  // Go opens the file Node just wrote and must validate + revoke that session.
  run(
    "go",
    [
      "test",
      "-buildvcs=false",
      "-count=1",
      "-run",
      "TestCrosscheckGoReadsNode",
      "./internal/server/webauth/",
    ],
    {
      cwd: GO_DIR,
      env: {
        ...process.env,
        NEX_CROSSCHECK_MODE: "go-reads-node",
        NEX_CROSSCHECK_TOKEN: TOKEN,
        NEX_CROSSCHECK_FILE: sessionsFile,
        NEX_CROSSCHECK_ID: sessionId,
      },
    },
  );
  check(true, "A: a session created by Node's real store validates (and is revocable) in Go");

  // Node can still read its own (now revoked) file — the format round-trips through Go.
  const after = run("npx", [
    "--yes",
    "tsx",
    join(HERE, "crosscheck-webauth-sessions-node.mts"),
    "list",
    TOKEN,
    sessionsFile,
  ]);
  check(
    JSON.parse(after.stdout).length === 0,
    "A: after Go revokes the Node session, Node reads the file back as empty",
  );
}

// ---------------------------------------------------------------- Direction B
// Go creates a session into the shared file; Node must validate it.
{
  const sessionsFile = join(root, "b", "web-sessions.json");
  const outPath = join(root, "b", "go-session-id.txt");
  run(
    "go",
    [
      "test",
      "-buildvcs=false",
      "-count=1",
      "-run",
      "TestCrosscheckGoWritesNode",
      "./internal/server/webauth/",
    ],
    {
      cwd: GO_DIR,
      env: {
        ...process.env,
        NEX_CROSSCHECK_MODE: "go-writes-node",
        NEX_CROSSCHECK_TOKEN: TOKEN,
        NEX_CROSSCHECK_FILE: sessionsFile,
        NEX_CROSSCHECK_OUT: outPath,
      },
    },
  );
  const sessionId = readFileSync(outPath, "utf8");

  const valid = run("npx", [
    "--yes",
    "tsx",
    join(HERE, "crosscheck-webauth-sessions-node.mts"),
    "validate",
    TOKEN,
    sessionsFile,
    sessionId,
  ]);
  check(
    valid.stdout.trim() === "valid",
    "B: a session created by Go validates in Node's real store",
  );
}

console.log(
  `\n${failures.length === 0 ? "ALL CHECKS PASSED" : `${failures.length} CHECKS FAILED`}`,
);
process.exit(failures.length === 0 ? 0 : 1);
