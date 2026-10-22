import { test } from "node:test";
import assert from "node:assert/strict";
import {
  commandAckSchema,
  conversationSnapshotSchema,
  v4ConversationFileChangesResultSchema,
  v4ConversationFileRewindPreviewResultSchema,
} from "@nex/shared/nex-protocol-v4";
import type { ConversationSnapshot } from "@nex/shared/nex-protocol-v4";
import {
  startConversationHarness,
  waitFor,
  type ConversationHarness,
} from "./helpers/conversationHarness.js";

/**
 * File changes and rewind commands over the real command handler + projection (spec B):
 * turn header summaries, `canRewindFiles`, `conversationFileChangesV4`, the rewind preview,
 * `applyFileRewind` and `editUserQuery{workspaceMode:"rewind"}`. Every ACK, snapshot and
 * query result is parsed with the real zod schemas.
 */

function headerOf(snapshot: ConversationSnapshot, turnId: string) {
  const row = snapshot.rows.window.find(
    (item) => item.kind === "turnHeader" && item.turnId === turnId,
  );
  return row && row.kind === "turnHeader" ? row : undefined;
}

function fileQuery(
  h: ConversationHarness,
  type: "conversationFileChangesV4" | "conversationFileRewindPreviewV4",
  target: { rowId: number; entityId: string },
) {
  return {
    type,
    sessionId: h.sessionId,
    target,
    baseRevision: h.snapshot().revision,
    baseLogEpoch: h.snapshot().logEpoch,
  };
}

function preview(h: ConversationHarness, userEntityId: string) {
  return fileQuery(h, "conversationFileRewindPreviewV4", {
    rowId: h.snapshot().rows.window.find((row) => row.entityId === userEntityId)?.rowId ?? 0,
    entityId: userEntityId,
  });
}

/** Scripts a change set for the newest turn (the same facts the checkpoint hook would emit). */
function scriptChanges(
  h: ConversationHarness,
  userEntryId: string,
  files: Array<{ path: string; additions: number; deletions: number }>,
) {
  h.engine.files.scriptChange(userEntryId, {
    files: files.length,
    additions: files.reduce((total, file) => total + file.additions, 0),
    deletions: files.reduce((total, file) => total + file.deletions, 0),
    state: "active",
    items: files.map((file) => ({
      path: file.path,
      additions: file.additions,
      deletions: file.deletions,
      writeCount: 1,
      toolNames: ["Edit"],
      patches: [],
    })),
  });
}

test("turn fileChanges: header summary + canRewindFiles follow the turn's tool displays", async () => {
  const h = await startConversationHarness();
  try {
    await h.runTurn("write and edit", "done", { entry: "u1" });
    const userRow = h.snapshot().rows.window.find((row) => row.entityId === "pi:u1");
    assert.ok(userRow);
    const turnId = userRow.turnId;
    assert.equal(headerOf(h.snapshot(), turnId)?.fileChanges, undefined);

    // A tool display on the live fold is enough for the card and the header summary.
    h.engine.emit(h.sessionId, {
      type: "tool_call_start",
      at: Date.now(),
      toolCallId: "t1",
      toolName: "edit",
      input: { path: "app.txt" },
    });
    h.engine.emit(h.sessionId, {
      type: "tool_call_end",
      at: Date.now(),
      toolCallId: "t1",
      toolName: "edit",
      text: "",
      isError: false,
      display: {
        kind: "file_diff",
        filePath: "app.txt",
        additions: 3,
        deletions: 1,
        structuredPatch: [
          { oldStart: 1, oldLines: 1, newStart: 1, newLines: 3, lines: ["+a", "+b", "+c"] },
        ],
      },
    });
    h.engine.emit(h.sessionId, {
      type: "turn_checkpoint",
      at: Date.now(),
      turnId,
      userEntryId: "u1",
      data: { version: 1, turnId, userEntryId: "u1", sequence: 1, files: {}, operations: [] },
    });
    await waitFor(
      () => headerOf(h.snapshot(), turnId)?.fileChanges !== undefined,
      "fileChanges summary",
    );
    conversationSnapshotSchema.parse(h.snapshot());
    const header = headerOf(h.snapshot(), turnId);
    assert.deepEqual(header?.fileChanges, {
      files: 1,
      additions: 3,
      deletions: 1,
      state: "active",
    });
    assert.equal(header?.actions?.canRewindFiles, true);
    const toolRow = h
      .snapshot()
      .rows.window.find((row) => row.kind === "toolCall" && row.toolCallId === "t1");
    assert.deepEqual(toolRow?.kind === "toolCall" ? toolRow.output?.display : undefined, {
      kind: "file_diff",
      filePath: "app.txt",
      additions: 3,
      deletions: 1,
      structuredPatch: [
        { oldStart: 1, oldLines: 1, newStart: 1, newLines: 3, lines: ["+a", "+b", "+c"] },
      ],
    });
  } finally {
    h.dispose();
  }
});

test("conversationFileChangesV4 / preview: schema-valid facts for the target turn", async () => {
  const h = await startConversationHarness();
  try {
    await h.runTurn("change files", "ok", { entry: "u1" });
    scriptChanges(h, "u1", [{ path: "app.txt", additions: 3, deletions: 1 }]);
    const changes = v4ConversationFileChangesResultSchema.parse(
      await h.publisher.fileChanges(
        "/workspace/demo",
        fileQuery(h, "conversationFileChangesV4", { rowId: 2, entityId: "pi:u1" }),
      ),
    );
    assert.equal(changes.files, 1);
    assert.equal(changes.additions, 3);
    assert.equal(changes.items[0]?.path, "app.txt");

    h.engine.files.scriptPreview("u1", {
      canApply: true,
      ignoredFiles: [],
      safeFiles: [{ action: "restore", operationCount: 1, path: "app.txt", toolNames: ["Edit"] }],
      unsafeFiles: [],
    });
    const rewindPreview = v4ConversationFileRewindPreviewResultSchema.parse(
      await h.publisher.fileRewindPreview("/workspace/demo", preview(h, "pi:u1")),
    );
    assert.equal(rewindPreview.canApply, true);
    assert.equal(rewindPreview.safeFiles[0]?.action, "restore");
    // A second turn without facts answers an empty, valid page (never an error).
    await h.runTurn("nothing changed", "ok", { entry: "u2" });
    const empty = v4ConversationFileChangesResultSchema.parse(
      await h.publisher.fileChanges(
        "/workspace/demo",
        fileQuery(h, "conversationFileChangesV4", { rowId: 1, entityId: "pi:u2" }),
      ),
    );
    assert.deepEqual(empty, { files: 0, additions: 0, deletions: 0, state: "active", items: [] });
    const emptyPreview = v4ConversationFileRewindPreviewResultSchema.parse(
      await h.publisher.fileRewindPreview("/workspace/demo", preview(h, "pi:u2")),
    );
    assert.deepEqual(emptyPreview, {
      canApply: false,
      ignoredFiles: [],
      safeFiles: [],
      unsafeFiles: [],
    });
  } finally {
    h.dispose();
  }
});

test("applyFileRewind: restores files, flips the header to reverted, hides Undo", async () => {
  const h = await startConversationHarness();
  try {
    await h.runTurn("write it", "done", { entry: "u1" });
    const userRow = h.snapshot().rows.window.find((row) => row.entityId === "pi:u1");
    assert.ok(userRow);
    h.engine.emit(h.sessionId, {
      type: "tool_call_start",
      at: Date.now(),
      toolCallId: "t1",
      toolName: "write",
      input: { path: "new.txt" },
    });
    h.engine.emit(h.sessionId, {
      type: "tool_call_end",
      at: Date.now(),
      toolCallId: "t1",
      toolName: "write",
      text: "",
      isError: false,
      display: {
        kind: "file_diff",
        filePath: "new.txt",
        additions: 1,
        deletions: 0,
        structuredPatch: [],
      },
    });
    h.engine.emit(h.sessionId, {
      type: "turn_checkpoint",
      at: Date.now(),
      turnId: userRow.turnId,
      userEntryId: "u1",
      data: {
        version: 1,
        turnId: userRow.turnId,
        userEntryId: "u1",
        sequence: 1,
        files: { "new.txt": { blob: null } },
        operations: [],
      },
    });
    await waitFor(
      () => headerOf(h.snapshot(), userRow.turnId)?.actions?.canRewindFiles === true,
      "canRewindFiles",
    );
    h.engine.files.scriptPreview("u1", {
      canApply: true,
      ignoredFiles: [],
      safeFiles: [{ action: "delete", operationCount: 1, path: "new.txt", toolNames: ["Write"] }],
      unsafeFiles: [],
    });
    const ack = commandAckSchema.parse(
      await h.command("applyFileRewind", { target: { rowId: userRow.rowId, entityId: "pi:u1" } }),
    );
    assert.equal(ack.status, "accepted");
    assert.equal(ack.result?.type, "applyFileRewind");
    // applyFileRewind = files only; the conversation is never cut.
    assert.equal(h.engine.files.applyCalls[0]?.userEntryId, "u1");
    assert.equal(h.engine.conversation.rewinds.length, 0);
    await waitFor(
      () => headerOf(h.snapshot(), userRow.turnId)?.fileChanges?.state === "reverted",
      "reverted header",
    );
    const header = headerOf(h.snapshot(), userRow.turnId);
    assert.equal(header?.actions?.canRewindFiles, undefined);
    // The history is untouched: only the files were restored.
    assert.equal(h.snapshot().rows.window.filter((row) => row.kind === "userInput").length, 1);
  } finally {
    h.dispose();
  }
});

test("applyFileRewind: an unsafe preview is rejected and nothing is applied", async () => {
  const h = await startConversationHarness();
  try {
    await h.runTurn("write it", "done", { entry: "u1" });
    const userRow = h.snapshot().rows.window.find((row) => row.entityId === "pi:u1");
    assert.ok(userRow);
    h.engine.emit(h.sessionId, {
      type: "tool_call_start",
      at: Date.now(),
      toolCallId: "t1",
      toolName: "edit",
      input: { path: "app.txt" },
    });
    h.engine.emit(h.sessionId, {
      type: "tool_call_end",
      at: Date.now(),
      toolCallId: "t1",
      toolName: "edit",
      text: "",
      isError: false,
      display: {
        kind: "file_diff",
        filePath: "app.txt",
        additions: 1,
        deletions: 1,
        structuredPatch: [],
      },
    });
    h.engine.emit(h.sessionId, {
      type: "turn_checkpoint",
      at: Date.now(),
      turnId: userRow.turnId,
      userEntryId: "u1",
      data: {
        version: 1,
        turnId: userRow.turnId,
        userEntryId: "u1",
        sequence: 1,
        files: {},
        operations: [],
      },
    });
    await waitFor(
      () => headerOf(h.snapshot(), userRow.turnId)?.actions?.canRewindFiles === true,
      "canRewindFiles",
    );
    h.engine.files.scriptPreview("u1", {
      canApply: false,
      ignoredFiles: [],
      safeFiles: [],
      unsafeFiles: [
        { reason: "external_modified", operationCount: 1, path: "app.txt", toolNames: ["Edit"] },
      ],
    });
    const ack = await h.command("applyFileRewind", {
      target: { rowId: userRow.rowId, entityId: "pi:u1" },
    });
    assert.equal(ack.status, "rejected");
    assert.equal(ack.reasonCode, "guard.workspaceRewindUnsafeFiles");
    assert.equal(h.engine.files.applyCalls.length, 1);
    assert.equal(headerOf(h.snapshot(), userRow.turnId)?.fileChanges?.state, "active");
  } finally {
    h.dispose();
  }
});

test("editUserQuery{workspaceMode:rewind}: applies files, then rewinds and re-prompts", async () => {
  const h = await startConversationHarness();
  try {
    await h.runTurn("q1", "a1", { entry: "u1" });
    await h.runTurn("q2", "a2", { entry: "u2" });
    const userRow = h.snapshot().rows.window.find((row) => row.entityId === "pi:u2");
    assert.ok(userRow);
    h.engine.files.scriptPreview("u2", {
      canApply: true,
      ignoredFiles: [],
      safeFiles: [{ action: "restore", operationCount: 1, path: "app.txt", toolNames: ["Edit"] }],
      unsafeFiles: [],
    });
    const ack = commandAckSchema.parse(
      await h.command("editUserQuery", {
        target: { rowId: userRow.rowId, entityId: "pi:u2" },
        newText: "q2 edited",
        workspaceMode: "rewind",
      }),
    );
    assert.equal(ack.status, "accepted");
    assert.equal(ack.result?.type === "editUserQuery" ? ack.result.disposition : "", "rewind");
    // Files restored first (journaled), then one cut-only rewind, then the new prompt.
    assert.equal(h.engine.files.applyCalls[0]?.userEntryId, "u2");
    assert.equal(h.engine.conversation.rewinds.length, 2);
    assert.equal(h.engine.conversation.rewinds[0]?.request.newText, "");
    assert.equal(h.engine.conversation.rewinds[1]?.request.newText, "q2 edited");
    const userTexts = h
      .snapshot()
      .rows.window.flatMap((row) => (row.kind === "userInput" ? [row.text] : []));
    // q2 is gone; the empty-text cut leaves no row of its own, only the re-prompt's row.
    assert.deepEqual(
      userTexts.filter((text) => text.length > 0),
      ["q1", "q2 edited"],
    );
  } finally {
    h.dispose();
  }
});

test("editUserQuery{workspaceMode:rewind}: an unsafe preview answers blocked with it", async () => {
  const h = await startConversationHarness();
  try {
    await h.runTurn("q1", "a1", { entry: "u1" });
    const userRow = h.snapshot().rows.window.find((row) => row.entityId === "pi:u1");
    assert.ok(userRow);
    h.engine.files.scriptPreview("u1", {
      canApply: false,
      ignoredFiles: [],
      safeFiles: [{ action: "restore", operationCount: 1, path: "app.txt", toolNames: ["Edit"] }],
      unsafeFiles: [
        { reason: "external_modified", operationCount: 1, path: "app.txt", toolNames: ["Edit"] },
      ],
    });
    const ack = commandAckSchema.parse(
      await h.command("editUserQuery", {
        target: { rowId: userRow.rowId, entityId: "pi:u1" },
        newText: "edited",
        workspaceMode: "rewind",
      }),
    );
    assert.equal(ack.result?.type === "editUserQuery" ? ack.result.disposition : "", "blocked");
    assert.equal(
      ack.result?.type === "editUserQuery" ? ack.result.reasonCode : "",
      "guard.workspaceRewindUnsafeFiles",
    );
    const previewResult = ack.result?.type === "editUserQuery" ? ack.result.preview : undefined;
    v4ConversationFileRewindPreviewResultSchema.parse(previewResult);
    assert.equal(previewResult?.unsafeFiles[0]?.reason, "external_modified");
    assert.equal(h.engine.conversation.rewinds.length, 0);
    assert.equal(h.engine.files.applyCalls.length, 0);
  } finally {
    h.dispose();
  }
});
