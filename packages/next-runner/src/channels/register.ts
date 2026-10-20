import type { CommandEnvelope } from "@nex/shared/nex-protocol-v4";
import type { ModelSelection } from "@nex/shared";
import type { Engine } from "../engine/types.js";
import type { ChannelRegistry } from "../link/registry.js";
import type { JsonValue } from "../link/frames.js";
import type { Logger } from "../logger.js";
import type { ConversationPublisher } from "../v4/publisher.js";
import { readWorkspacePresentation, workspacePathFromParams } from "./nexSession.js";
import { NexTaskChannel } from "./nexTask.js";
import { buildModelSelectionViews } from "./modelViews.js";

export interface RunnerChannelsOptions {
  engine: Engine;
  publisher: ConversationPublisher;
  logger: Logger;
  workspacePaths: string[];
}

interface WorkspaceArg {
  workspacePath?: unknown;
  workspaceIdentity?: unknown;
  remoteSessionId?: unknown;
}

export function registerChannels(
  registry: ChannelRegistry,
  options: RunnerChannelsOptions,
): { nexTask: NexTaskChannel; disposed: () => void } {
  const { engine, publisher, logger } = options;
  const nexTask = new NexTaskChannel(engine, logger);
  let modelRevision = 0;

  const requireWorkspace = (arg: JsonValue): string => {
    const path = workspacePathFromArg(arg);
    if (!path) throw new Error("workspacePath is required");
    return path;
  };

  // ── nex-agent: v4 conversation ──

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

  registry.call("nex-agent", "sendConversationCommandV4", async (arg) => {
    const record = asRecord(arg);
    const workspacePath = requireWorkspace(arg);
    const envelope = record["envelope"];
    if (!isRecord(envelope)) throw new Error("envelope is required");
    return publisher.sendCommand({
      workspacePath,
      envelope: envelope as unknown as CommandEnvelope,
    });
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

  // ── nex-agent: sessions index ──

  registry.call("nex-agent", "subscribeSessionsIndexV4", async (arg) => {
    const workspacePath = requireWorkspace(arg);
    return publisher.subscribeSessionsIndex({ workspacePath });
  });

  registry.call("nex-agent", "resyncSessionsIndexV4", async (arg) => {
    const record = asRecord(arg);
    const workspacePath = requireWorkspace(arg);
    const subscriptionId = requireString(record["subscriptionId"], "subscriptionId");
    return publisher.resyncSessionsIndex({ workspacePath, subscriptionId });
  });

  registry.call("nex-agent", "unsubscribeSessionsIndexV4", async (arg) => {
    const record = asRecord(arg);
    requireWorkspace(arg);
    const subscriptionId = requireString(record["subscriptionId"], "subscriptionId");
    publisher.unsubscribeSessionsIndex(subscriptionId);
    return undefined;
  });

  registry.listen("nex-agent", "onDynamicSessionsIndexFrame", (arg, emit) => {
    const workspacePath = requireWorkspace(arg);
    return publisher.addSessionsIndexSink(workspacePath, (frame) => emit(frame as JsonValue));
  });

  // ── nex-task ──

  registry.call("nex-task", "listTasks", async (arg) => {
    const workspacePath = requireWorkspace(arg);
    return nexTask.listTasks({ workspacePath });
  });
  registry.call("nex-task", "listPinnedTasks", async (arg) => {
    requireWorkspace(arg);
    return nexTask.listPinnedTasks();
  });
  registry.call("nex-task", "listArchivedTasks", async (arg) => {
    requireWorkspace(arg);
    return nexTask.listArchivedTasks();
  });
  registry.call("nex-task", "listDeletedTaskIds", async (arg) => {
    const workspacePath = requireWorkspace(arg);
    return nexTask.listDeletedTaskIds({ workspacePath });
  });
  // Task context-menu paths: pi session files have no stable UI-facing path.
  registry.call("nex-task", "getTaskSessionFilePath", async (arg) => {
    requireWorkspace(arg);
    return nexTask.getTaskSessionFilePath();
  });
  registry.call("nex-task", "getTaskNativeSessionLogFile", async (arg) => {
    requireWorkspace(arg);
    return nexTask.getTaskNativeSessionLogFile();
  });
  // Routed to the first connected runner, no workspace in the arg.
  registry.call("nex-task", "listPinnedTaskIds", async () => nexTask.listPinnedTaskIds());
  registry.listen("nex-task", "onDynamicWorkspaceEvent", (arg, emit) => {
    const workspacePath = requireWorkspace(arg);
    return nexTask.onDynamicWorkspaceEvent(workspacePath, (event) =>
      emit(event as unknown as JsonValue),
    );
  });

  // ── nex-session ──

  registry.call("nex-session", "readWorkspacePresentation", async (arg) => {
    const workspacePath = requireWorkspace(arg);
    return readWorkspacePresentation(workspacePath);
  });

  // ── model-selection / provider-settings (first connected runner) ──

  const views = async (arg?: JsonValue) => {
    const catalog = await engine.catalog();
    // 修复依据：getView({selection}) 必须回 effectiveSelection，否则 UI 会把有效选择
    // 当成“未选模型”（Choose model + 提交禁用）。输入只用于解析，不写配置。
    return buildModelSelectionViews(catalog, modelRevision, readModelSelectionViewInput(arg));
  };

  registry.call("model-selection", "getView", async (arg) => (await views(arg)).modelSelection);
  registry.listen("model-selection", "onDidChange", () => {
    // The catalog is static per runner process; no change events are produced.
    return { dispose: () => undefined };
  });
  registry.call("provider-settings", "getView", async () => (await views()).providerSettings);
  registry.listen("provider-settings", "onDidChange", () => ({ dispose: () => undefined }));

  // Bumping keeps the view revision monotonic if a future catalog refresh is added.
  const refreshViews = (): void => {
    modelRevision += 1;
  };

  return {
    nexTask,
    disposed: () => {
      void refreshViews;
      nexTask.dispose();
    },
  };
}

function workspacePathFromArg(arg: JsonValue): string | null {
  if (!isRecord(arg)) return null;
  const record = arg as WorkspaceArg;
  if (typeof record.workspacePath === "string" && record.workspacePath.length > 0) {
    return record.workspacePath;
  }
  const workspace = arg["workspace"];
  if (isRecord(workspace)) {
    const nested = workspace["workspacePath"];
    if (typeof nested === "string" && nested.length > 0) return nested;
  }
  return null;
}

function asRecord(value: JsonValue): Record<string, unknown> {
  if (!isRecord(value)) throw new Error("argument must be an object");
  return value;
}

/**
 * `model-selection.getView(input?)`: the browser sends `{selection}` or `undefined`
 * (`{"$undef":true}` → undefined). Anything malformed is treated as “no input” so the
 * catalog view still renders; the composer performs its own schema validation.
 */
function readModelSelectionViewInput(
  arg: JsonValue,
): { selection: ModelSelection | null } | undefined {
  if (!isRecord(arg)) return undefined;
  const selection = arg["selection"];
  if (!isRecord(selection)) return { selection: null };
  const providerId = selection["providerId"];
  const modelId = selection["modelId"];
  if (typeof providerId !== "string" || typeof modelId !== "string") {
    return { selection: null };
  }
  const options = isRecord(selection["options"]) ? selection["options"] : undefined;
  const reasoningLevel = options?.["reasoningLevel"];
  return {
    selection: {
      providerId,
      modelId,
      ...(typeof reasoningLevel === "string" && reasoningLevel.length > 0
        ? { options: { reasoningLevel } }
        : {}),
    },
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function requireString(value: unknown, name: string): string {
  if (typeof value !== "string" || value.length === 0) {
    throw new Error(`${name} is required`);
  }
  return value;
}

export { workspacePathFromParams };
