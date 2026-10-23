import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdir, mkdtemp, readFile, realpath, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  fauxAssistantMessage,
  fauxToolCall,
  registerFauxProvider,
  type FauxProviderRegistration,
} from "@earendil-works/pi-ai/compat";
import { ModelRuntime, SettingsManager } from "@earendil-works/pi-coding-agent";
import {
  commandAckSchema,
  conversationSnapshotSchema,
  v4ConversationFileChangesResultSchema,
  v4ConversationFileRewindPreviewResultSchema,
  type CommandAck,
  type CommandEnvelope,
  type ConversationSnapshot,
} from "@nex/shared/nex-protocol-v4";
import { PiEngine } from "../src/engine/pi/piEngine.js";
import { createLogger } from "../src/logger.js";
import { projectHistory } from "../src/projection/restore.js";
import { buildConversationSnapshot } from "../src/projection/snapshot.js";
import type { CompleteConversationWireFrame } from "../src/v4/envelopes.js";
import { ConversationPublisher } from "../src/v4/publisher.js";

/**
 * File changes and rewind with the REAL pi engine and pi-ai's faux provider (spec B):
 *
 * - a turn that writes, edits and bash-touches files records a `nex.checkpoints` entry;
 * - the tool cards carry `output.display` diffs and the header the turn summary;
 * - `applyFileRewind` restores the edited file, deletes the created one, and flips the turn
 *   to `reverted`; an external modification blocks it (`external_modified`);
 * - bash-touched files show as ignored; a restart still shows the same fileChanges;
 * - a workspace that is not a git repository works the same way (git is only used by the
 *   runner's shadow repository).
 */

const logger = createLogger("error");
const fauxs: FauxProviderRegistration[] = [];
const hasGit = (() => {
  try {
    execFileSync("git", ["--version"], { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
})();

function currentFaux(): FauxProviderRegistration {
  const faux = fauxs.at(-1);
  if (!faux) throw new Error("no faux provider registered");
  return faux;
}

async function makeRuntime(): Promise<ModelRuntime> {
  const faux = registerFauxProvider({
    models: [{ id: "faux-1", name: "Faux", contextWindow: 200_000 }],
  });
  const model = faux.getModel();
  const runtime = await ModelRuntime.create({ refreshOnCreate: false });
  runtime.registerProvider(model.provider, {
    baseUrl: model.baseUrl,
    api: faux.api,
    apiKey: "faux-key",
    models: faux.models.map((registered) => ({
      id: registered.id,
      name: registered.name,
      api: registered.api,
      reasoning: registered.reasoning,
      input: registered.input,
      cost: registered.cost,
      contextWindow: registered.contextWindow,
      maxTokens: registered.maxTokens,
      baseUrl: registered.baseUrl,
    })),
  });
  fauxs.push(faux);
  return runtime;
}

function snapshotOf(frames: CompleteConversationWireFrame[]): ConversationSnapshot {
  const last = frames.at(-1);
  if (!last || last.frame.payload.kind !== "snapshot") throw new Error("no snapshot");
  return conversationSnapshotSchema.parse(last.frame.payload.snapshot);
}

async function waitFor<T>(
  read: () => T | undefined,
  label: string,
  timeoutMs = 20_000,
): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = read();
    if (value !== undefined) return value;
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  throw new Error(`timed out waiting for ${label}`);
}

interface Harness {
  root: string;
  workspace: string;
  engine: PiEngine;
  publisher: ConversationPublisher;
  sessionId: string;
  frames: CompleteConversationWireFrame[];
  snapshot: () => ConversationSnapshot;
  command: (
    type: CommandEnvelope["type"],
    payload?: Record<string, unknown>,
  ) => Promise<CommandAck>;
}

function envelope(
  overrides: Partial<CommandEnvelope> & { type: CommandEnvelope["type"] },
): CommandEnvelope {
  return {
    commandId: `cmd-${Math.random().toString(36).slice(2)}`,
    clientId: "client-1",
    sessionId: null,
    payload: {},
    issuedAt: Date.now(),
    ...overrides,
  };
}

async function start(options: { gitRepository?: boolean } = {}): Promise<Harness> {
  const root = await mkdtemp(join(tmpdir(), "nex-files-pi-"));
  const workspace = join(root, "workspace");
  await mkdir(workspace, { recursive: true });
  if (options.gitRepository) execFileSync("git", ["init", "--quiet"], { cwd: workspace });
  await writeFile(join(workspace, "app.txt"), "alpha\nbeta\ngamma\n");
  const resolved = await realpath(workspace);
  const engine = new PiEngine({
    dataDir: join(root, "data"),
    logger,
    createModelRuntime: makeRuntime,
    createSettingsManager: () => SettingsManager.inMemory(),
    titleSettings: { enabled: false },
  });
  await engine.init();
  const publisher = new ConversationPublisher({ engine, logger });
  const frames: CompleteConversationWireFrame[] = [];
  publisher.addConversationSink(resolved, (frame) => frames.push(frame));
  const created = commandAckSchema.parse(
    await publisher.sendCommand({
      workspacePath: resolved,
      envelope: envelope({
        type: "createSession",
        payload: {
          workspaceId: resolved,
          firstInput: {
            modelSelection: {
              providerId: "faux",
              modelId: "faux-1",
              options: { reasoningLevel: "off" },
            },
          },
        },
      }),
    }),
  );
  const sessionId = created.result?.type === "createSession" ? created.result.sessionId : "";
  assert.ok(sessionId);
  publisher.subscribeConversation({ workspacePath: resolved, sessionId });
  await waitFor(() => (frames.length > 0 ? true : undefined), "initial frame");
  const snapshot = () => snapshotOf(frames);
  const command = async (type: CommandEnvelope["type"], payload: Record<string, unknown> = {}) => {
    const current = snapshot();
    const ack = commandAckSchema.parse(
      await publisher.sendCommand({
        workspacePath: resolved,
        envelope: envelope({
          type,
          sessionId,
          payload,
          baseRevision: current.revision,
          baseLogEpoch: current.logEpoch,
        }),
      }),
    );
    if (ack.status === "accepted" || ack.status === "noop") {
      await waitFor(
        () => (snapshot().revision >= ack.revisionAtDecision ? true : undefined),
        `${type} snapshot`,
      );
    }
    return ack;
  };
  return { root, workspace: resolved, engine, publisher, sessionId, frames, snapshot, command };
}

async function cleanup(h: Harness): Promise<void> {
  await h.engine.abort(h.sessionId).catch(() => undefined);
  await h.publisher.dispose();
  await h.engine.dispose();
  currentFaux().unregister();
  await new Promise((resolve) => setTimeout(resolve, 50));
  await rm(h.root, { recursive: true, force: true, maxRetries: 5, retryDelay: 50 });
}

/**
 * Sends one prompt and answers whatever approval cards appear (bash in edit mode) until the
 * run settles. Resolves with the newest user row after its `nex.checkpoints` entry landed.
 */
async function runPrompt(h: Harness, text: string): Promise<void> {
  const before = h.snapshot().rows.window.filter((row) => row.kind === "userInput").length;
  const ack = await h.command("sendText", { text, mode: "edit" });
  assert.equal(ack.status, "accepted");
  const deadline = Date.now() + 60_000;
  const answered = new Set<string>();
  while (Date.now() < deadline) {
    const pending = h.snapshot().pendingInteractions[0];
    if (pending && !answered.has(pending.interactionId)) {
      answered.add(pending.interactionId);
      await h.command("resolveInteraction", {
        interactionId: pending.interactionId,
        answer: { optionId: "allowAlways" },
      });
    }
    const rows = h.snapshot().rows.window.filter((row) => row.kind === "userInput");
    if (
      rows.length > before &&
      h.snapshot().control.phase === "completedSuccess" &&
      !h.snapshot().pendingInteractions.length
    ) {
      await waitFor(() => {
        const branch = openSessionOf(h).sessionManager.getBranch() as Array<{
          customType?: string;
        }>;
        return branch.some((entry) => entry.customType === "nex.checkpoints") || branch.length > 0
          ? branch.length
          : undefined;
      }, "history");
      // The record is written one macrotask after the settle.
      await new Promise((resolve) => setTimeout(resolve, 120));
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 30));
  }
  throw new Error("timed out waiting for the turn to settle");
}

/** One turn: write a new file, edit app.txt, then a bash `touch` (3 tool batches). */
async function runFileTurn(h: Harness, text = "change files"): Promise<void> {
  currentFaux().setResponses([
    fauxAssistantMessage([fauxToolCall("write", { path: "created.txt", content: "brand new\n" })], {
      stopReason: "toolUse",
    }),
    fauxAssistantMessage(
      [fauxToolCall("edit", { path: "app.txt", edits: [{ oldText: "beta", newText: "BETA" }] })],
      { stopReason: "toolUse" },
    ),
    fauxAssistantMessage([fauxToolCall("bash", { command: "touch notes.txt" })], {
      stopReason: "toolUse",
    }),
    fauxAssistantMessage("done"),
  ]);
  await runPrompt(h, text);
}

function openSessionOf(h: Harness) {
  const open = (
    h.engine as unknown as {
      sessions: Map<
        string,
        {
          session: {
            isStreaming?: boolean;
            subscribe(listener: (event: unknown) => void): () => void;
          };
          sessionManager: { getBranch(): unknown[] };
        }
      >;
    }
  ).sessions.get(h.sessionId);
  if (!open) throw new Error("session is not open");
  return open;
}

function headerFor(snapshot: ConversationSnapshot, userEntityId: string) {
  const user = snapshot.rows.window.find((row) => row.entityId === userEntityId);
  if (!user) return undefined;
  const header = snapshot.rows.window.find(
    (row) => row.kind === "turnHeader" && row.turnId === user.turnId,
  );
  return header && header.kind === "turnHeader" ? header : undefined;
}

test(
  "pi + faux: write/edit/bash turn shows diffs, fileChanges and a reversible turn",
  { skip: !hasGit },
  async () => {
    const h = await start();
    try {
      await runFileTurn(h);
      const snapshot = h.snapshot();
      const userRow = snapshot.rows.window.filter((row) => row.kind === "userInput").at(-1);
      assert.ok(userRow?.entityId);
      const header = await waitFor(
        () => headerFor(h.snapshot(), userRow.entityId as string),
        "turn header",
      );
      // The write and edit cards carry file diffs; bash has none.
      const toolRows = snapshot.rows.window.filter((row) => row.kind === "toolCall");
      const writeRow = toolRows.find((row) => row.kind === "toolCall" && row.toolName === "Write");
      const editRow = toolRows.find((row) => row.kind === "toolCall" && row.toolName === "Edit");
      assert.equal(
        writeRow?.kind === "toolCall" ? writeRow.output?.display?.kind : undefined,
        "file_diff",
      );
      assert.equal(
        editRow?.kind === "toolCall" ? editRow.output?.display?.kind : undefined,
        "file_diff",
      );
      const editDisplay =
        editRow?.kind === "toolCall" && editRow.output?.display?.kind === "file_diff"
          ? editRow.output.display
          : undefined;
      assert.equal(editDisplay?.additions, 1);
      assert.equal(editDisplay?.deletions, 1);
      assert.match(editDisplay?.filePath ?? "", /app\.txt$/u);
      const summary = await waitFor(
        () => headerFor(h.snapshot(), userRow.entityId as string)?.fileChanges,
        "fileChanges summary",
      );
      assert.equal(summary.files, 2);
      assert.ok(summary.additions >= 2);
      assert.equal(summary.deletions, 1);
      assert.equal(header.actions?.canRewindFiles, true);

      // Full details through the read-only query.
      const changes = await h.publisher.fileChanges(h.workspace, {
        sessionId: h.sessionId,
        target: { rowId: header.rowId, entityId: userRow.entityId as string },
        baseRevision: snapshot.revision,
        baseLogEpoch: snapshot.logEpoch,
      });
      v4ConversationFileChangesResultSchema.parse(changes);
      // notes.txt was touched by bash only: no diff facts, reported as ignored by previews.
      assert.deepEqual(changes.items.map((item) => item.path).sort(), [
        "app.txt",
        "created.txt",
        "notes.txt",
      ]);
      assert.equal(changes.items.find((item) => item.path === "notes.txt")?.additions, 0);

      // bash-touched notes.txt is reported as ignored; the safe set still applies.
      const preview = v4ConversationFileRewindPreviewResultSchema.parse(
        await h.publisher.fileRewindPreview(h.workspace, {
          sessionId: h.sessionId,
          target: { rowId: header.rowId, entityId: userRow.entityId as string },
          baseRevision: snapshot.revision,
          baseLogEpoch: snapshot.logEpoch,
        }),
      );
      assert.ok(preview.ignoredFiles.some((file) => file.path.endsWith("notes.txt")));
      assert.equal(preview.canApply, true);
      assert.equal(preview.safeFiles.length, 2);
    } finally {
      await cleanup(h);
    }
  },
);

test(
  "pi + faux: applyFileRewind restores contents, deletes created files and survives a restart",
  { skip: !hasGit },
  async () => {
    const h = await start({ gitRepository: true });
    try {
      await runFileTurn(h, "change files");
      const userRow = await waitFor(
        () =>
          h
            .snapshot()
            .rows.window.filter((row) => row.kind === "userInput")
            .at(-1),
        "user row",
      );
      assert.ok(userRow?.entityId);
      await waitFor(
        () =>
          headerFor(h.snapshot(), userRow.entityId as string)?.actions?.canRewindFiles
            ? true
            : undefined,
        "canRewindFiles",
      );
      // The turn's checkpointed files are the undo set; the bash-created notes.txt is ignored.
      const ack = await h.command("applyFileRewind", {
        target: { rowId: userRow.rowId, entityId: userRow.entityId },
      });
      assert.equal(ack.status, "accepted");
      assert.equal(await readFile(join(h.workspace, "app.txt"), "utf8"), "alpha\nbeta\ngamma\n");
      await assert.rejects(stat(join(h.workspace, "created.txt")));
      const revertedHeader = await waitFor(
        () =>
          headerFor(h.snapshot(), userRow.entityId as string)?.fileChanges?.state === "reverted"
            ? headerFor(h.snapshot(), userRow.entityId as string)
            : undefined,
        "reverted header",
      );
      assert.equal(revertedHeader?.actions?.canRewindFiles, undefined);

      // A restart rebuilds the same cards and the reverted state from the pi session file.
      await h.engine.dispose();
      const restarted = new PiEngine({
        dataDir: join(h.root, "data"),
        logger,
        createModelRuntime: makeRuntime,
        createSettingsManager: () => SettingsManager.inMemory(),
        titleSettings: { enabled: false },
      });
      await restarted.init();
      await restarted.openSession(h.workspace, h.sessionId);
      const history = await restarted.history(h.sessionId);
      const meta = (await restarted.listSessions(h.workspace)).find(
        (item) => item.sessionId === h.sessionId,
      );
      assert.ok(meta);
      const restored = buildConversationSnapshot(projectHistory(meta, history, "restored"));
      conversationSnapshotSchema.parse(restored);
      const restoredHeader = headerFor(restored, userRow.entityId as string);
      assert.equal(restoredHeader?.fileChanges?.state, "reverted");
      assert.equal(restoredHeader?.actions?.canRewindFiles, undefined);
      const restoredEdit = restored.rows.window.find(
        (row) => row.kind === "toolCall" && row.toolName === "Edit",
      );
      assert.equal(
        restoredEdit?.kind === "toolCall" ? restoredEdit.output?.display?.kind : undefined,
        "file_diff",
      );
      await restarted.dispose();
      // The restarted engine opened the same file; there is nothing left to close.
      h.engine = restarted;
    } finally {
      await cleanup(h);
    }
  },
);

test(
  "pi + faux: an external modification blocks the rewind (external_modified)",
  { skip: !hasGit },
  async () => {
    const h = await start();
    try {
      await runFileTurn(h);
      const userRow = await waitFor(
        () =>
          h
            .snapshot()
            .rows.window.filter((row) => row.kind === "userInput")
            .at(-1),
        "user row",
      );
      assert.ok(userRow?.entityId);
      await waitFor(
        () =>
          headerFor(h.snapshot(), userRow.entityId as string)?.actions?.canRewindFiles
            ? true
            : undefined,
        "canRewindFiles",
      );
      await writeFile(join(h.workspace, "app.txt"), "alpha\nBETA\ngamma\ndelta\n");
      const preview = v4ConversationFileRewindPreviewResultSchema.parse(
        await h.publisher.fileRewindPreview(h.workspace, {
          sessionId: h.sessionId,
          target: { rowId: userRow.rowId, entityId: userRow.entityId },
          baseRevision: h.snapshot().revision,
          baseLogEpoch: h.snapshot().logEpoch,
        }),
      );
      const unsafe = preview.unsafeFiles.find((file) => file.path.endsWith("app.txt"));
      assert.equal(unsafe?.reason, "external_modified");
      assert.ok(unsafe?.currentHash);
      assert.equal(preview.canApply, false);
      const ack = await h.command("applyFileRewind", {
        target: { rowId: userRow.rowId, entityId: userRow.entityId },
      });
      assert.equal(ack.status, "rejected");
      assert.equal(ack.reasonCode, "guard.workspaceRewindUnsafeFiles");
      // Nothing was restored.
      assert.equal(
        await readFile(join(h.workspace, "app.txt"), "utf8"),
        "alpha\nBETA\ngamma\ndelta\n",
      );
    } finally {
      await cleanup(h);
    }
  },
);

test(
  "pi + faux: editUserQuery{workspaceMode:rewind} restores files and re-runs the turn",
  { skip: !hasGit },
  async () => {
    const h = await start();
    try {
      await runFileTurn(h, "change files");
      const userRow = await waitFor(
        () =>
          h
            .snapshot()
            .rows.window.filter((row) => row.kind === "userInput")
            .at(-1),
        "user row",
      );
      assert.ok(userRow?.entityId);
      await waitFor(
        () =>
          headerFor(h.snapshot(), userRow.entityId as string)?.actions?.canRewindFiles
            ? true
            : undefined,
        "canRewindFiles",
      );
      currentFaux().setResponses([fauxAssistantMessage("rewritten")]);
      const ack = await h.command("editUserQuery", {
        target: { rowId: userRow.rowId, entityId: userRow.entityId },
        newText: "no files this time",
        workspaceMode: "rewind",
      });
      assert.equal(ack.status, "accepted");
      assert.equal(ack.result?.type === "editUserQuery" ? ack.result.disposition : "", "rewind");
      assert.equal(await readFile(join(h.workspace, "app.txt"), "utf8"), "alpha\nbeta\ngamma\n");
      await assert.rejects(stat(join(h.workspace, "created.txt")));
      await waitFor(
        () =>
          h
            .snapshot()
            .rows.window.some((row) => row.kind === "assistantText" && row.text === "rewritten")
            ? true
            : undefined,
        "re-run reply",
      );
      const userTexts = h
        .snapshot()
        .rows.window.flatMap((row) => (row.kind === "userInput" ? [row.text] : []))
        .filter((text) => text.length > 0);
      assert.deepEqual(userTexts, ["no files this time"]);
    } finally {
      await cleanup(h);
    }
  },
);

/**
 * 端到端复现 `--e2e=files` 的失败序列（spec B，fixture 带真实的 write→edit 工具链）：
 * 1. 建文件的一轮 → Undo（文件被删）；
 * 2. 再建同一文件的一轮（turn-start = 文件不存在）；
 * 3. 编辑该用户消息并选「Reset chat + files」→ 文件回到该轮起始状态（不存在）。
 *
 * 回归依据：E2E 的 `rewindRestoredFiles` 曾在重跑回复一出现就同步 stat，读到删除前的
 * 内容。runner 侧的实际顺序是「先还原文件（journaled），再切分支、再重跑」，这里直接
 * 验证该顺序：命令 ACK 返回时文件已不存在，且重跑轮（无工具）不会再创建它。
 */
test(
  "pi + faux: create → Undo → create again → edit with rewind deletes the created file",
  { skip: !hasGit },
  async () => {
    const h = await start({ gitRepository: true });
    const FILE = "e2e-file-rewind.txt";
    try {
      // 1. 第一轮：写文件 + 编辑（同一次助手消息的两个 toolCall）。
      currentFaux().setResponses([
        fauxAssistantMessage(
          [
            fauxToolCall("write", { path: FILE, content: "first line 1" }),
            fauxToolCall("edit", {
              path: FILE,
              edits: [{ oldText: "first line 1", newText: "second line 1" }],
            }),
          ],
          { stopReason: "toolUse" },
        ),
        fauxAssistantMessage("file done"),
      ]);
      await runPrompt(h, "create the file with a write and an edit");
      assert.equal(await readFile(join(h.workspace, FILE), "utf8"), "second line 1");

      // Undo：该轮创建的文件被删除。
      const first = await waitFor(
        () =>
          h
            .snapshot()
            .rows.window.filter((row) => row.kind === "userInput")
            .at(-1),
        "first user row",
      );
      assert.ok(first?.entityId);
      const undo = await h.command("applyFileRewind", {
        target: { rowId: first.rowId, entityId: first.entityId },
      });
      assert.equal(undo.status, "accepted");
      await assert.rejects(stat(join(h.workspace, FILE)));

      // 2. 第二轮：同一文件只写首行（turn-start = 文件不存在）。
      currentFaux().setResponses([
        fauxAssistantMessage([fauxToolCall("write", { path: FILE, content: "first line 2" })], {
          stopReason: "toolUse",
        }),
        fauxAssistantMessage("RERUN-OLD"),
      ]);
      await runPrompt(h, "create the file again with only the first line");
      assert.equal(await readFile(join(h.workspace, FILE), "utf8"), "first line 2");
      const second = await waitFor(
        () =>
          h
            .snapshot()
            .rows.window.filter((row) => row.kind === "userInput")
            .at(-1),
        "second user row",
      );
      assert.ok(second?.entityId);
      assert.notEqual(second.entityId, first.entityId);
      await waitFor(
        () =>
          headerFor(h.snapshot(), second.entityId as string)?.actions?.canRewindFiles
            ? true
            : undefined,
        "second turn canRewindFiles",
      );

      // 3. 编辑该消息并还原文件：无工具的重跑回复。
      currentFaux().setResponses([fauxAssistantMessage("rerun line 2")]);
      const ack = await h.command("editUserQuery", {
        target: { rowId: second.rowId, entityId: second.entityId },
        newText: "Reply with exactly rerun line 2. Use no tools.",
        workspaceMode: "rewind",
        attachments: [],
      });
      assert.equal(ack.status, "accepted");
      assert.equal(ack.result?.type === "editUserQuery" ? ack.result.disposition : "", "rewind");
      // 文件在命令返回时已删除（还原先于分支切割与重跑），重跑轮不会再创建它。
      await assert.rejects(stat(join(h.workspace, FILE)));
      await waitFor(
        () =>
          h
            .snapshot()
            .rows.window.some((row) => row.kind === "assistantText" && row.text === "rerun line 2")
            ? true
            : undefined,
        "re-run reply",
      );
      await assert.rejects(stat(join(h.workspace, FILE)));
      const rerunUser = h
        .snapshot()
        .rows.window.filter((row) => row.kind === "userInput")
        .at(-1);
      assert.equal(
        rerunUser?.kind === "userInput" ? rerunUser.text : "",
        "Reply with exactly rerun line 2. Use no tools.",
        "the rewritten prompt is the newest user row",
      );
    } finally {
      await cleanup(h);
    }
  },
);
