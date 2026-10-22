import type {
  CommandEnvelope,
  V4ConversationFileChangesParams,
  V4ConversationFileRewindPreviewParams,
} from "@nex/shared/nex-protocol-v4";
import type { AttachmentRegistry } from "../attachments/registry.js";
import type { JsonValue } from "../link/frames.js";
import type { ChannelRegistryLike } from "../link/registry.js";
import type { ConversationPublisher } from "../v4/publisher.js";
import {
  handleAttachmentAbort,
  handleAttachmentBegin,
  handleAttachmentChunk,
  handleAttachmentCommit,
  handleAttachmentPreviewSource,
  handleAttachmentRead,
  handleConversationAttachmentRead,
  handleConversationAttachmentStat,
  type AttachmentCommandContext,
  type AttachmentCommandResult,
} from "../v4/attachmentCommands.js";

/** Helpers the nex-agent call table needs from the channel registry module. */
export interface NexAgentCallDeps {
  publisher: ConversationPublisher;
  requireWorkspace: (arg: JsonValue) => string;
  requireAttachments: () => AttachmentRegistry;
  requireString: (value: unknown, name: string) => string;
  asRecord: (value: JsonValue) => Record<string, unknown>;
  numberOrZero: (value: unknown) => number;
  stringOrEmpty: (value: unknown) => string;
}

/** Registers every `nex-agent` call that reads or drives a conversation. */
export function registerNexAgentCalls(registry: ChannelRegistryLike, deps: NexAgentCallDeps): void {
  const {
    publisher,
    requireWorkspace,
    requireAttachments,
    requireString,
    asRecord,
    numberOrZero,
    stringOrEmpty,
  } = deps;

  registry.call("nex-agent", "subscribeConversationV4", async (arg) => {
    const record = asRecord(arg);
    const workspacePath = requireWorkspace(arg);
    const sessionId = requireString(record["sessionId"], "sessionId");
    return publisher.subscribeConversation({ workspacePath, sessionId });
  });

  registry.call("nex-agent", "resyncConversationV4", async (arg) => {
    const record = asRecord(arg);
    const workspacePath = requireWorkspace(arg);
    const subscriptionId = requireString(record["subscriptionId"], "subscriptionId");
    return publisher.resyncConversation({ workspacePath, subscriptionId });
  });

  registry.call("nex-agent", "unsubscribeConversationV4", async (arg) => {
    const record = asRecord(arg);
    const workspacePath = requireWorkspace(arg);
    const subscriptionId = requireString(record["subscriptionId"], "subscriptionId");
    publisher.unsubscribeConversation(workspacePath, subscriptionId);
    return undefined;
  });

  registry.call("nex-agent", "conversationRowsRangeV4", async (arg) => {
    const record = asRecord(arg);
    const workspacePath = requireWorkspace(arg);
    const sessionId = requireString(record["sessionId"], "sessionId");
    return publisher.rowsRange(workspacePath, sessionId);
  });

  registry.call("nex-agent", "conversationPlansV4", async (arg) => {
    const record = asRecord(arg);
    const workspacePath = requireWorkspace(arg);
    const sessionId = requireString(record["sessionId"], "sessionId");
    return publisher.plans(workspacePath, sessionId);
  });

  // Workflow runs are a pi-absent feature. `useWorkflowRunJournalSummaries` reads this
  // unconditionally when a session opens, so answer an empty journal instead of letting
  // the server reject the known method with "Unknown method" on every open.
  registry.call("nex-agent", "conversationWorkflowRunsV4", async (arg) => {
    requireWorkspace(arg);
    return { runs: [] };
  });

  // File changes and rewind (spec B). Both are read-only lookups keyed like the rows and
  // plans queries; the mutation goes through the `applyFileRewind` command.
  registry.call("nex-agent", "conversationFileChangesV4", async (arg) => {
    const workspacePath = requireWorkspace(arg);
    const record = asRecord(arg);
    const sessionId = requireString(record["sessionId"], "sessionId");
    return publisher.fileChanges(workspacePath, {
      sessionId,
      target: record["target"] as V4ConversationFileChangesParams["target"],
      baseRevision: numberOrZero(record["baseRevision"]),
      baseLogEpoch: stringOrEmpty(record["baseLogEpoch"]),
    });
  });

  registry.call("nex-agent", "conversationFileRewindPreviewV4", async (arg) => {
    const workspacePath = requireWorkspace(arg);
    const record = asRecord(arg);
    const sessionId = requireString(record["sessionId"], "sessionId");
    return publisher.fileRewindPreview(workspacePath, {
      sessionId,
      target: record["target"] as V4ConversationFileRewindPreviewParams["target"],
      baseRevision: numberOrZero(record["baseRevision"]),
      baseLogEpoch: stringOrEmpty(record["baseLogEpoch"]),
    });
  });

  registry.call("nex-agent", "sendConversationCommandV4", async (arg) => {
    const record = asRecord(arg);
    const workspacePath = requireWorkspace(arg);
    const envelope = record["envelope"];
    if (typeof envelope !== "object" || envelope === null || Array.isArray(envelope)) {
      throw new Error("envelope is required");
    }
    return publisher.sendCommand({
      workspacePath,
      envelope: envelope as unknown as CommandEnvelope,
    });
  });

  // ── nex-agent: attachments (spec A, docs/next/attachments-rewind.md) ──
  const attachmentCtx = (): AttachmentCommandContext => ({
    registry: requireAttachments(),
    session: (sessionId) => publisher.sessions.get(sessionId),
    openSession: (sessionId) => publisher.sessions.get(sessionId),
  });
  const attachmentCall = (
    method: string,
    handler: (ctx: AttachmentCommandContext, arg: Record<string, unknown>) => Promise<unknown>,
  ) => {
    registry.call("nex-agent", method, async (arg) => {
      requireWorkspace(arg);
      const result = (await handler(attachmentCtx(), asRecord(arg))) as AttachmentCommandResult;
      if (!result.ok) {
        // The link forwards thrown errors as the call's failure; the reason code keeps the
        // UI's fault taxonomy (proto.invalidPayload / attachment.*).
        throw new Error(`${result.reasonCode}: ${result.message}`);
      }
      return result.value;
    });
  };
  attachmentCall("attachmentBeginV4", handleAttachmentBegin);
  attachmentCall("attachmentChunkV4", handleAttachmentChunk);
  attachmentCall("attachmentCommitV4", handleAttachmentCommit);
  attachmentCall("attachmentAbortV4", handleAttachmentAbort);
  attachmentCall("attachmentReadV4", handleAttachmentRead);
  attachmentCall("attachmentPreviewSourceV4", handleAttachmentPreviewSource);

  // Share reads carry the session's workspace target like every other forwarded method.
  registry.call("nex-agent", "conversationAttachmentReadV4", async (arg) => {
    const workspacePath = requireWorkspace(arg);
    void workspacePath;
    const result = await handleConversationAttachmentRead(attachmentCtx(), asRecord(arg));
    if (!result.ok) throw new Error(`${result.reasonCode}: ${result.message}`);
    return result.value;
  });
  registry.call("nex-agent", "conversationAttachmentStatV4", async (arg) => {
    requireWorkspace(arg);
    const result = await handleConversationAttachmentStat(attachmentCtx(), asRecord(arg));
    if (!result.ok) throw new Error(`${result.reasonCode}: ${result.message}`);
    return result.value;
  });

  registry.call("nex-agent", "queryConversationCommandsV4", async (arg) => {
    const record = asRecord(arg);
    const commands = Array.isArray(record["commands"]) ? record["commands"] : [];
    return publisher.queryCommands({
      commands: commands.map((item) => {
        const key = asRecord(item);
        return {
          sessionId: typeof key["sessionId"] === "string" ? key["sessionId"] : null,
          commandId: String(key["commandId"] ?? ""),
        };
      }),
    });
  });

  registry.listen("nex-agent", "onDynamicConversationFrame", (arg, emit) => {
    const workspacePath = requireWorkspace(arg);
    return publisher.addConversationSink(workspacePath, (frame) => emit(frame as JsonValue));
  });
}
