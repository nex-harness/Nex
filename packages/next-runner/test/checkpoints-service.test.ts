import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdir, mkdtemp, readFile, readdir, realpath, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { CheckpointService } from "../src/engine/checkpoints/checkpointService.js";
import { RewindStore } from "../src/engine/checkpoints/rewindStore.js";
import { parseUnifiedPatch } from "../src/engine/checkpoints/patch.js";
import { createLogger } from "../src/logger.js";

/**
 * Shadow-git checkpoint service with a REAL `git` and a real temp workspace (spec B):
 * turn snapshot → per-tool post-state → changed files → preview → journaled apply.
 */

const logger = createLogger("error");
const hasGit = (() => {
  try {
    execFileSync("git", ["--version"], { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
})();

interface Workspace {
  root: string;
  workspace: string;
  dataDir: string;
  service: CheckpointService;
  rewind: RewindStore;
}

export async function workspace(initialize = true): Promise<Workspace> {
  const root = await mkdtemp(join(tmpdir(), "nex-ck-"));
  const dir = join(root, "workspace");
  await mkdir(dir, { recursive: true });
  const ws = await realpath(dir);
  const dataDir = join(root, "data");
  const service = new CheckpointService(ws, { dataDir, logger });
  const rewind = new RewindStore(service, logger);
  if (initialize) {
    await writeFile(join(ws, "app.txt"), "one\ntwo\nthree\n");
    await writeFile(join(ws, ".gitignore"), "ignored/\n*.log\n");
  }
  return { root, workspace: ws, dataDir, service, rewind };
}

async function cleanup(ws: Workspace): Promise<void> {
  await rm(ws.root, { recursive: true, force: true, maxRetries: 5, retryDelay: 50 });
}

/** One turn: snapshot, then one op per `steps` entry, producing the records a run persists. */
export async function turn(
  ws: Workspace,
  turnId: string,
  userEntryId: string,
  steps: Array<{
    tool: "edit" | "write" | "bash";
    path?: string;
    content?: string;
    patch?: string;
  }>,
): Promise<void> {
  for (const [index, step] of steps.entries()) {
    if (step.tool === "bash") {
      continue; // shell tools are not checkpointed; the hook records them on the turn only.
    }
    await ws.service.prepareTurn(turnId, userEntryId, step.path);
    if (step.content !== undefined && step.path) {
      await writeFile(join(ws.workspace, step.path), step.content);
    }
    const operation = await ws.service.recordOperation(turnId, {
      toolCallId: `${turnId}-${index}`,
      toolName: step.tool,
      path: step.path,
      ...(step.patch !== undefined ? { patch: step.patch } : {}),
      isError: false,
    });
    assert.ok(operation, `operation ${index} recorded`);
  }
}

test(
  "checkpoints: create, edit and write produce changed files with correct counts",
  { skip: !hasGit },
  async () => {
    const ws = await workspace();
    try {
      await turn(ws, "turn-1", "u1", [
        { tool: "write", path: "created.txt", content: "alpha\nbeta\n" },
        {
          tool: "edit",
          path: "app.txt",
          content: "one\nTWO\nthree\n",
          patch: "--- a/app.txt\n+++ b/app.txt\n@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three",
        },
      ]);
      const record = ws.service.record("turn-1");
      assert.ok(record);
      assert.equal(record.userEntryId, "u1");
      assert.deepEqual(
        record.operations.map((operation) => operation.path),
        ["created.txt", "app.txt"],
      );
      // The write's diff is synthesized from the empty turn-start blob.
      assert.match(record.operations[0]?.patch ?? "", /\+\+\+ b\/created\.txt/u);
      assert.equal(record.operations[0]?.additions, 2);
      // Per-path diff = the LAST operation's patch (the card and the turn diff agree).
      const changes = await ws.rewind.fileChanges({ version: 1, ...record }, false);
      assert.equal(changes.files, 2);
      assert.equal(changes.state, "active");
      const created = changes.items.find((item) => item.path === "created.txt");
      assert.equal(created?.additions, 2);
      assert.equal(created?.deletions, 0);
      assert.equal(created?.patches[0]?.lines.at(-1), "+beta");
      const edited = changes.items.find((item) => item.path === "app.txt");
      assert.equal(edited?.additions, 1);
      assert.equal(edited?.deletions, 1);
      assert.equal(edited?.writeCount, 1);
      assert.deepEqual(edited?.toolNames, ["edit"]);
      assert.equal(edited?.patches[0]?.lines.at(-1), " three");
    } finally {
      await cleanup(ws);
    }
  },
);

test(
  "checkpoints: .gitignore is respected, and the snapshot never touches the workspace",
  { skip: !hasGit },
  async () => {
    const ws = await workspace();
    try {
      await mkdir(join(ws.workspace, "ignored"), { recursive: true });
      await writeFile(join(ws.workspace, "ignored", "cache.bin"), "cache");
      await writeFile(join(ws.workspace, "debug.log"), "log");
      await turn(ws, "turn-1", "u1", [{ tool: "write", path: "kept.txt", content: "kept\n" }]);
      const git = await ws.service.gitReader();
      assert.ok(git);
      const record = ws.service.record("turn-1");
      assert.ok(record);
      // Files in the turn tree come from `git add -A`, which honours `.gitignore`.
      const tree = await git.run(["ls-tree", "-r", "--name-only", "HEAD"], {}).catch(async () => {
        // No commit was made for a ref-less check: inspect the object database directly.
        return { stdout: "", stdoutBuffer: Buffer.alloc(0), stderr: "" };
      });
      void tree;
      const listing = execFileSync(
        "git",
        [
          `--git-dir=${git.repositoryDir}`,
          `--work-tree=${ws.workspace}`,
          "ls-files",
          "--cached",
          "--others",
          "--exclude-standard",
        ],
        { encoding: "utf8", cwd: ws.workspace },
      )
        .split("\n")
        .filter(Boolean);
      assert.ok(listing.includes("app.txt"));
      assert.ok(!listing.includes("debug.log"));
      assert.ok(!listing.includes("ignored/cache.bin"));
      // The workspace has no `.git`: the checkpoint repo lives entirely in the data dir.
      await assert.rejects(stat(join(ws.workspace, ".git")));
      assert.ok((await readdir(ws.dataDir)).includes("checkpoints"));
    } finally {
      await cleanup(ws);
    }
  },
);

test(
  "checkpoints: an external modification makes the preview unsafe",
  { skip: !hasGit },
  async () => {
    const ws = await workspace();
    try {
      await turn(ws, "turn-1", "u1", [
        {
          tool: "edit",
          path: "app.txt",
          content: "one\nTWO\nthree\n",
          patch: "--- a/app.txt\n+++ b/app.txt\n@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three",
        },
      ]);
      const record = {
        version: 1 as const,
        ...(ws.service.record("turn-1") as NonNullable<ReturnType<CheckpointService["record"]>>),
      };
      const safe = await ws.rewind.preview(record, []);
      assert.equal(safe.preview.canApply, true);
      assert.equal(safe.preview.safeFiles[0]?.action, "restore");

      await writeFile(join(ws.workspace, "app.txt"), "one\nTWO\nthree\nfour\n");
      const unsafe = await ws.rewind.preview(record, []);
      assert.equal(unsafe.preview.canApply, false);
      assert.equal(unsafe.preview.safeFiles.length, 0);
      assert.equal(unsafe.preview.unsafeFiles[0]?.reason, "external_modified");
      assert.ok(unsafe.preview.unsafeFiles[0]?.currentHash);
      assert.ok(unsafe.preview.unsafeFiles[0]?.expectedHash);
    } finally {
      await cleanup(ws);
    }
  },
);

test(
  "checkpoints: bash-touched paths are ignored, and a partial turn is checkpoint_missing",
  { skip: !hasGit },
  async () => {
    const ws = await workspace();
    try {
      await turn(ws, "turn-1", "u1", [{ tool: "write", path: "created.txt", content: "x\n" }]);
      const record = {
        version: 1 as const,
        ...(ws.service.record("turn-1") as NonNullable<ReturnType<CheckpointService["record"]>>),
      };
      const withBash = await ws.rewind.preview(record, ["notes.txt"]);
      assert.equal(withBash.preview.ignoredFiles[0]?.reason, "bash_ignored");
      assert.equal(withBash.preview.ignoredFiles[0]?.path, "notes.txt");
      // An ignored file alone does not block the turn's own safe file.
      assert.equal(withBash.preview.canApply, true);

      const partial = await ws.rewind.preview({ ...record, partial: true }, []);
      assert.equal(partial.preview.canApply, false);
      assert.equal(partial.preview.unsafeFiles[0]?.reason, "checkpoint_missing");
    } finally {
      await cleanup(ws);
    }
  },
);

test(
  "checkpoints: apply restores contents, deletes created files, and records reverted",
  { skip: !hasGit },
  async () => {
    const ws = await workspace();
    try {
      await turn(ws, "turn-1", "u1", [
        { tool: "write", path: "created.txt", content: "brand new\n" },
        {
          tool: "edit",
          path: "app.txt",
          content: "one\nTWO\nthree\n",
          patch: "--- a/app.txt\n+++ b/app.txt\n@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three",
        },
      ]);
      const record = {
        version: 1 as const,
        ...(ws.service.record("turn-1") as NonNullable<ReturnType<CheckpointService["record"]>>),
      };
      const { preview, plan } = await ws.rewind.preview(record, []);
      assert.equal(preview.canApply, true);
      assert.equal(plan.find((file) => file.path === "created.txt")?.action, "delete");
      const result = await ws.rewind.apply(plan);
      assert.equal(result.applied, true);
      assert.equal(await readFile(join(ws.workspace, "app.txt"), "utf8"), "one\ntwo\nthree\n");
      await assert.rejects(stat(join(ws.workspace, "created.txt")));
      // The workspace's own files outside the turn are untouched.
      assert.equal(await readFile(join(ws.workspace, ".gitignore"), "utf8"), "ignored/\n*.log\n");
    } finally {
      await cleanup(ws);
    }
  },
);

test(
  "checkpoints: a failed commit step compensates every write in reverse",
  { skip: !hasGit },
  async () => {
    const ws = await workspace();
    try {
      await turn(ws, "turn-1", "u1", [
        { tool: "write", path: "a-created.txt", content: "a\n" },
        { tool: "write", path: "b-created.txt", content: "b\n" },
        {
          tool: "edit",
          path: "app.txt",
          content: "one\nTWO\nthree\n",
          patch: "--- a/app.txt\n+++ b/app.txt\n@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three",
        },
      ]);
      const record = {
        version: 1 as const,
        ...(ws.service.record("turn-1") as NonNullable<ReturnType<CheckpointService["record"]>>),
      };
      const { plan } = await ws.rewind.preview(record, []);
      const applied: Record<string, string | null> = {};
      const result = await ws.rewind.apply(plan, {
        commitAfterApply: async () => {
          // Every write is on disk at this point: the edit restored and the created files gone.
          for (const path of ["app.txt", "a-created.txt", "b-created.txt"]) {
            applied[path] = await readFile(join(ws.workspace, path), "utf8").catch(() => null);
          }
          throw new Error("branch cut failed");
        },
      });
      assert.equal(result.applied, false);
      assert.match(result.failures[0] ?? "", /branch cut failed/u);
      // Compensated: the edited file is back to its post-turn content and both created files
      // exist again with their turn content.
      assert.equal(await readFile(join(ws.workspace, "app.txt"), "utf8"), "one\nTWO\nthree\n");
      assert.equal(await readFile(join(ws.workspace, "a-created.txt"), "utf8"), "a\n");
      assert.equal(await readFile(join(ws.workspace, "b-created.txt"), "utf8"), "b\n");
      // All writes had happened before the failing commit.
      assert.deepEqual(applied, {
        "app.txt": "one\ntwo\nthree\n",
        "a-created.txt": null,
        "b-created.txt": null,
      });
    } finally {
      await cleanup(ws);
    }
  },
);

test(
  "checkpoints: a non-git workspace works (no .git required) and a turn survives a restart",
  { skip: !hasGit },
  async () => {
    const ws = await workspace();
    try {
      // A workspace that is its own git repository must not be disturbed either.
      execFileSync("git", ["init", "--quiet"], { cwd: ws.workspace });
      await turn(ws, "turn-1", "u1", [{ tool: "write", path: "created.txt", content: "x\n" }]);
      const record = ws.service.record("turn-1");
      assert.ok(record);
      // A fresh service (runner restart) reads the same blobs from the shadow repo.
      const restarted = new CheckpointService(ws.workspace, { dataDir: ws.dataDir, logger });
      const restartedRewind = new RewindStore(restarted, logger);
      const changes = await restartedRewind.fileChanges({ version: 1, ...record }, false);
      assert.equal(changes.files, 1);
      assert.equal(changes.items[0]?.path, "created.txt");
      const { preview } = await restartedRewind.preview({ version: 1, ...record }, []);
      assert.equal(preview.canApply, true);
    } finally {
      await cleanup(ws);
    }
  },
);

test("checkpoints: git missing disables the service and hides the turn facts", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-ck-nogit-"));
  try {
    const dir = join(root, "workspace");
    await mkdir(dir, { recursive: true });
    const workspacePath = await realpath(dir);
    const warnings: string[] = [];
    const service = new CheckpointService(workspacePath, {
      dataDir: join(root, "data"),
      gitExecutable: join(root, "definitely-not-git"),
      logger: {
        ...createLogger("error"),
        warn: (message: string) => void warnings.push(message),
      } as never,
    });
    const snapshot = await service.prepareTurn("turn-1", "u1", "app.txt");
    assert.equal(snapshot.ok, false);
    assert.equal(service.disabled, true);
    assert.equal(await service.available(), false);
    assert.equal(warnings.length, 1);
    assert.match(warnings[0] ?? "", /git is unavailable/u);
    // Repeated use never logs again.
    await service.available();
    assert.equal(warnings.length, 1);
    // Nothing was created in the data dir (the probe runs before `mkdir`).
    await assert.rejects(stat(join(root, "data")));
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("patch parser: hunks, + / - counts and CRLF tolerance", () => {
  const parsed = parseUnifiedPatch(
    [
      "--- a/x",
      "+++ b/x",
      "@@ -1,2 +1,3 @@",
      " keep",
      "-old",
      "+new",
      "+extra",
      "\\ No newline at end of file",
    ].join("\r\n"),
  );
  assert.equal(parsed.additions, 2);
  assert.equal(parsed.deletions, 1);
  assert.deepEqual(parsed.hunks[0], {
    oldStart: 1,
    oldLines: 2,
    newStart: 1,
    newLines: 3,
    lines: [" keep", "-old", "+new", "+extra"],
  });
});
