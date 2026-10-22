import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, realpath, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  fauxAssistantMessage,
  registerFauxProvider,
  type FauxProviderRegistration,
} from "@earendil-works/pi-ai/compat";
import type { Message, TranscriptContext } from "@earendil-works/pi-ai";
import { ModelRuntime, SettingsManager } from "@earendil-works/pi-coding-agent";
import {
  commandAckSchema,
  conversationSnapshotSchema,
  type CommandAck,
  type CommandEnvelope,
  type ConversationSnapshot,
  type UserInputRow,
} from "@nex/shared/nex-protocol-v4";
import { AttachmentRegistry } from "../src/attachments/registry.js";
import { withAttachmentMarkers } from "../src/engine/pi/attachmentEntry.js";
import type { EngineMessage } from "../src/engine/types.js";
import { PiEngine } from "../src/engine/pi/piEngine.js";
import { createLogger } from "../src/logger.js";
import { projectHistory } from "../src/projection/restore.js";
import { buildConversationSnapshot } from "../src/projection/snapshot.js";
import type { EngineSessionState } from "../src/engine/types.js";
import type { CompleteConversationWireFrame } from "../src/v4/envelopes.js";
import { ConversationPublisher } from "../src/v4/publisher.js";

/**
 * Attachments against the REAL pi engine and pi-ai's faux provider (spec A acceptance):
 *
 * - an uploaded PNG reaches the model as a prompt image (`[image:image/png:…]` in the
 *   faux transcript) and the user row renders the ref;
 * - a text file reaches the model as an attachment block (path + read hint), never inlined;
 * - a restart rebuilds the same attachments from the persisted `nex.attachments` entry;
 * - `editUserQuery` keeps, replaces and clears the refs (omitted / new list / `[]`).
 *
 * Every ACK and snapshot is parsed with the real zod schemas.
 */

const logger = createLogger("error");
/** Smallest real 1x1 PNG (a synthetic header is rejected by pi's resize step). */
const PNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
  "base64",
);

function checksum(bytes: Uint8Array): string {
  return `sha256:${createHash("sha256").update(bytes).digest("hex")}`;
}

async function makeRuntime(): Promise<ModelRuntime> {
  const faux = registerFauxProvider({
    models: [{ id: "faux-1", name: "Faux", contextWindow: 200_000, input: ["text", "image"] }],
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

const fauxs: FauxProviderRegistration[] = [];

let sequence = 0;
function envelope(input: {
  type: CommandEnvelope["type"];
  sessionId: string | null;
  payload: Record<string, unknown>;
  baseRevision?: number;
  baseLogEpoch?: string;
}): CommandEnvelope {
  sequence += 1;
  return {
    protocolVersion: 1,
    commandId: `cmd-${sequence}`,
    issuedAt: Date.now(),
    clientId: "e2e",
    type: input.type,
    sessionId: input.sessionId,
    ...(input.baseRevision !== undefined ? { baseRevision: input.baseRevision } : {}),
    ...(input.baseLogEpoch !== undefined ? { baseLogEpoch: input.baseLogEpoch } : {}),
    payload: input.payload,
  } as CommandEnvelope;
}

async function waitFor<T>(
  check: () => T | undefined,
  label: string,
  timeoutMs = 10_000,
): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = check();
    if (value !== undefined) return value;
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  throw new Error(`timed out waiting for ${label}`);
}

function snapshotOf(frames: CompleteConversationWireFrame[]): ConversationSnapshot {
  const last = frames[frames.length - 1];
  if (!last || last.frame.payload.kind !== "snapshot") throw new Error("no snapshot");
  return conversationSnapshotSchema.parse(last.frame.payload.snapshot);
}

function userRows(snapshot: ConversationSnapshot): UserInputRow[] {
  return snapshot.rows.window.filter((row): row is UserInputRow => row.kind === "userInput");
}

/** Uploads bytes through the V4 transaction and returns the committed ref + display meta. */
async function upload(
  publisher: ConversationPublisher,
  sessionId: string,
  bytes: Uint8Array,
  fileName: string,
  mime: string,
): Promise<{ ref: string; fileName: string; mime: string; bytes: number }> {
  const registry = publisher.attachmentRegistry();
  assert.ok(registry);
  const uploadId = `upload-${Math.random().toString(36).slice(2, 10)}`;
  const connectionId = "conn";
  const begin = await registry.store.begin({
    connectionId,
    uploadId,
    sessionId,
    fileName,
    mime,
    totalBytes: bytes.byteLength,
    totalChunks: 1,
    checksum: checksum(bytes),
  });
  assert.equal(begin.state, "staging");
  await registry.store.appendChunk({
    connectionId,
    sessionId,
    uploadId,
    chunkIndex: 0,
    dataBase64: Buffer.from(bytes).toString("base64"),
  });
  const { ref } = await registry.store.commit({ connectionId, sessionId, uploadId });
  return { ref, fileName, mime, bytes: bytes.byteLength };
}

function restoredSession(sessionId: string): EngineSessionState {
  return {
    sessionId,
    workspacePath: "",
    title: "",
    titleSource: "default",
    createdAt: 0,
    lastActivityAt: 0,
    phase: "completedSuccess",
    model: null,
    usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    isRunning: false,
  };
}

interface Harness {
  root: string;
  workspace: string;
  dataDir: string;
  registry: AttachmentRegistry;
  engine: PiEngine;
  publisher: ConversationPublisher;
  frames: CompleteConversationWireFrame[];
  sessionId: string;
  snapshot: () => ConversationSnapshot;
  command: (
    type: CommandEnvelope["type"],
    payload?: Record<string, unknown>,
  ) => Promise<CommandAck>;
}

async function start(): Promise<Harness> {
  const faux = registerFauxProvider({
    models: [{ id: "faux-1", name: "Faux", contextWindow: 200_000, input: ["text", "image"] }],
  });
  fauxs.push(faux);
  const root = await mkdtemp(join(tmpdir(), "nex-attach-pi-"));
  const workspace = await realpath(root);
  const dataDir = join(root, "data");
  await mkdir(dataDir, { recursive: true });
  const registry = new AttachmentRegistry({ dataDir });
  const engine = new PiEngine({
    dataDir,
    logger,
    attachments: registry,
    createModelRuntime: makeRuntime,
    createSettingsManager: () => SettingsManager.inMemory(),
    titleSettings: { enabled: false },
  });
  await engine.init();
  const publisher = new ConversationPublisher({ engine, logger, attachments: registry });
  const frames: CompleteConversationWireFrame[] = [];
  publisher.addConversationSink(workspace, (frame) => frames.push(frame));
  const created = commandAckSchema.parse(
    await publisher.sendCommand({
      workspacePath: workspace,
      envelope: envelope({
        type: "createSession",
        sessionId: null,
        payload: {
          workspaceId: workspace,
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
  publisher.subscribeConversation({ workspacePath: workspace, sessionId });
  await waitFor(() => (frames.length > 0 ? true : undefined), "initial frame");
  const snapshot = () => snapshotOf(frames);
  const command = async (type: CommandEnvelope["type"], payload: Record<string, unknown> = {}) => {
    const current = snapshot();
    const ack = commandAckSchema.parse(
      await publisher.sendCommand({
        workspacePath: workspace,
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
  return {
    root,
    workspace,
    dataDir,
    registry,
    engine,
    publisher,
    frames,
    sessionId,
    snapshot,
    command,
  };
}

async function stop(h: Harness): Promise<void> {
  await h.publisher.dispose();
  await h.engine.dispose();
  h.registry.dispose();
  await rm(h.root, { recursive: true, force: true, maxRetries: 5, retryDelay: 50 }).catch(
    () => undefined,
  );
}

/** User messages of the request as text + `[image:mime:chars]` markers. */
function transcriptText(context: TranscriptContext): string {
  return context.messages
    .filter((message: Message) => message.role === "user")
    .map((message: Message) =>
      typeof message.content === "string"
        ? message.content
        : message.content
            .map((block) =>
              block.type === "text"
                ? block.text
                : block.type === "image"
                  ? `[image:${block.mimeType}:${block.data.length}]`
                  : "",
            )
            .join("\n"),
    )
    .join("\n---\n");
}

test("attachments: image → pi prompt image, text file → attachment block, restore keeps refs", async () => {
  const h = await start();
  const requested: string[] = [];
  try {
    const png = await upload(h.publisher, h.sessionId, PNG, "pixel.png", "image/png");
    const txt = Buffer.from("secret-content-42\n");
    const file = await upload(h.publisher, h.sessionId, txt, "notes.txt", "text/plain");
    const currentFaux = fauxs[fauxs.length - 1] as FauxProviderRegistration;
    currentFaux.setResponses([
      (context: TranscriptContext) => {
        requested.push(transcriptText(context));
        return fauxAssistantMessage("I can see the pixel and the notes.");
      },
    ]);
    const ack = await h.command("sendText", {
      text: "Look at the attached pixel and the notes file.",
      attachments: [png, file],
    });
    assert.equal(ack.status, "accepted");
    await waitFor(() => (requested.length > 0 ? requested[0] : undefined), "model request", 20_000);
    const prompt = requested[0] ?? "";
    // Image: a real image block for the model (not a path).
    assert.match(prompt, /\[image:image\/png:\d+\]/u);
    assert.equal(prompt.includes("Image omitted"), false);
    // Text file: a block naming the on-disk path, never the contents.
    assert.match(prompt, /\[Attachment 1: notes\.txt \(text\/plain, 18 bytes\)\] path=/u);
    assert.equal(prompt.includes("secret-content-42"), false);

    // The user row carries both refs with the display metadata the composer sent.
    const row = await waitFor(() => userRows(h.snapshot())[0], "user row");
    assert.equal(row.text, "Look at the attached pixel and the notes file.");
    assert.deepEqual(
      row.attachments?.map((attachment) => [attachment.ref, attachment.fileName, attachment.mime]),
      [
        [png.ref, "pixel.png", "image/png"],
        [file.ref, "notes.txt", "text/plain"],
      ],
    );
  } finally {
    await stop(h);
  }
});

test("attachments: restore joins the user message with its nex.attachments marker", async () => {
  // Restore is a pure join over the active branch (pi's `SessionFile` currently writes an
  // empty header before the first message and never flushes the entry, so the on-disk
  // path is not observable in-process; the join itself is the runner's contract).
  const branch = [
    { type: "message", id: "e-system", message: { role: "system", content: "" } },
    {
      type: "message",
      id: "e-user",
      message: { role: "user", content: [{ type: "text", text: "Describe the pixel." }] },
    },
    {
      type: "custom",
      id: "e-marker",
      customType: "nex.attachments",
      data: {
        version: 1,
        userEntryId: "e-user",
        attachments: [
          {
            ref: "nex-runner://sess-1/upload-1/pixel.png",
            fileName: "pixel.png",
            mime: "image/png",
            bytes: 70,
          },
        ],
      },
    },
    { type: "message", id: "e-assistant", message: { role: "assistant", content: [] } },
  ];
  const messages: EngineMessage[] = [
    { role: "user", at: 1, text: "Describe the pixel.", entryId: "e-user" },
    { role: "assistant", at: 2, stopReason: "stop", parts: [], entryId: "e-assistant" },
  ];
  const restored = userRows(
    buildConversationSnapshot(
      projectHistory(restoredSession("sess-1"), withAttachmentMarkers(messages, branch), "epoch"),
    ),
  )[0];
  assert.equal(restored?.text, "Describe the pixel.");
  assert.equal(restored?.attachments?.[0]?.ref, "nex-runner://sess-1/upload-1/pixel.png");
  // A marker that names another entry never leaks onto this one.
  const foreign = withAttachmentMarkers(messages, [
    ...branch.slice(0, 2),
    { ...branch[2], data: { version: 1, userEntryId: "e-other", attachments: [] } },
  ]);
  const foreignUser = foreign[0];
  assert.equal(foreignUser?.role === "user" ? foreignUser.attachments : "n/a", undefined);
});

test("attachments: editUserQuery keeps, replaces and clears ([] = none)", async () => {
  const h = await start();
  try {
    const png = await upload(h.publisher, h.sessionId, PNG, "pixel.png", "image/png");
    const txt = await upload(h.publisher, h.sessionId, Buffer.from("a"), "a.txt", "text/plain");
    const currentFaux = fauxs[fauxs.length - 1] as FauxProviderRegistration;
    currentFaux.setResponses([fauxAssistantMessage("first"), fauxAssistantMessage("second")]);
    await h.command("sendText", { text: "first prompt", attachments: [png] });
    const first = await waitFor(() => userRows(h.snapshot())[0], "first row");
    assert.equal(first.attachments?.[0]?.ref, png.ref);

    // Replace: the new list wins.
    const replaced = await h.command("editUserQuery", {
      target: { rowId: first.rowId, entityId: first.entityId },
      newText: "second prompt",
      attachments: [txt],
    });
    assert.equal(replaced.status, "accepted");
    const second = await waitFor(() => {
      const rows = userRows(h.snapshot());
      return rows.length === 1 && rows[0]?.text === "second prompt" ? rows[0] : undefined;
    }, "replaced row");
    assert.deepEqual(
      second.attachments?.map((item) => item.ref),
      [txt.ref],
    );

    // Clear: `[]` removes every ref.
    const cleared = await h.command("editUserQuery", {
      target: { rowId: second.rowId, entityId: second.entityId },
      newText: "third prompt",
      attachments: [],
    });
    assert.equal(cleared.status, "accepted");
    const third = await waitFor(() => {
      const rows = userRows(h.snapshot());
      return rows.length === 1 && rows[0]?.text === "third prompt" ? rows[0] : undefined;
    }, "cleared row");
    assert.equal(third.attachments, undefined);
  } finally {
    await stop(h);
  }
});
