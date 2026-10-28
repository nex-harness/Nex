import { nexMcpServerStatusSnapshotSchema, type ModelSelection } from "@nex/shared";
import type { Engine } from "../engine/types.js";
import type { ChannelRegistryLike } from "../link/registry.js";
import type { JsonValue } from "../link/frames.js";
import type { Logger } from "../logger.js";
import type { ConversationPublisher } from "../v4/publisher.js";
import { registerNexAgentCalls } from "./nexAgentCalls.js";
import type { AttachmentRegistry } from "../attachments/registry.js";
import { readWorkspacePresentation, workspacePathFromParams } from "./nexSession.js";
import { NexTaskChannel } from "./nexTask.js";
import { buildModelSelectionViews } from "./modelViews.js";
import { composeSlashCommands, loadCatalog, skillCatalogFrom } from "./piCatalog.js";
import type { McpServerStatus } from "../config/applyRuntimeConfig.js";
import type { WorkspaceRoots } from "../workspaces/workspaceRoots.js";
import { createWorkspaceGuardedRegistry, workspacePathFromArg } from "./workspaceGuard.js";
import { registerRunnerWorkspaces } from "./runnerWorkspaces.js";

export interface RunnerChannelsOptions {
  engine: Engine;
  publisher: ConversationPublisher;
  logger: Logger;
  /**
   * The live served-folder owner (docs/next/runner-folders.md decision 1). The
   * guard, the workspace-less nex-task lists and `runner-workspaces` all read it
   * at call time, so an added folder works without a restart.
   */
  roots: WorkspaceRoots;
  /** Identity prefix for `runner-workspaces.add` results (`runner:<id>:<path>`). */
  runnerId: string;
}

export function registerChannels(
  linkRegistry: ChannelRegistryLike,
  options: RunnerChannelsOptions,
): { nexTask: NexTaskChannel; disposed: () => void } {
  const { engine, publisher, logger, roots, runnerId } = options;
  // 唯一 workspace 边界：所有 call/listen 在注册时统一包一层校验，handler 内部不再
  // 逐个重复。校验读取的是活的 WorkspaceRoots，而不是启动时的数组。
  const registry = createWorkspaceGuardedRegistry(linkRegistry, roots);
  const nexTask = new NexTaskChannel(engine, logger, {
    servedPaths: () => roots.list().map((folder) => folder.path),
    isSessionWatched: (sessionId) => publisher.sessions.isWatched(sessionId),
  });
  // 机器级目标（`workspacePath: "~"`）下唯一的写入入口：添加/移除本机 served folder。
  registerRunnerWorkspaces(registry, { roots, runnerId, logger: logger.child("workspaces") });
  let modelRevision = 0;

  const requireAttachments = (): AttachmentRegistry => {
    const attachmentRegistry = publisher.attachmentRegistry();
    if (!attachmentRegistry) throw new Error("attachments are not configured on this runner");
    return attachmentRegistry;
  };

  const requireWorkspace = (arg: JsonValue): string => {
    const path = workspacePathFromArg(arg);
    if (!path) throw new Error("workspacePath is required");
    return path;
  };

  const optionalWorkspaceTarget = (
    arg: JsonValue,
  ): { workspacePath: string; workspaceIdentity?: string } | undefined => {
    const target = requireWorkspaceTarget(arg, false);
    return target.workspacePath ? target : undefined;
  };

  const requireWorkspaceTarget = (
    arg: JsonValue,
    required = true,
  ): { workspacePath: string; workspaceIdentity?: string } => {
    const path = workspacePathFromArg(arg);
    if (!path) {
      if (required) throw new Error("workspacePath is required");
      return { workspacePath: "" };
    }
    const identity = workspaceIdentityFromArg(arg);
    return { workspacePath: path, ...(identity ? { workspaceIdentity: identity } : {}) };
  };

  /** nex-task mutations: workspace target plus the row id. */
  const requireTaskTarget = (
    arg: JsonValue,
  ): { workspacePath: string; workspaceIdentity?: string; taskId: string } => {
    const target = requireWorkspaceTarget(arg);
    return { ...target, taskId: requireStringField(arg, "taskId") };
  };

  // ── nex-agent: v4 conversation (call table in `nexAgentCalls.ts`) ──
  registerNexAgentCalls(registry, {
    publisher,
    requireWorkspace,
    requireAttachments,
    requireString,
    asRecord,
    numberOrZero,
    stringOrEmpty,
  });

  // ── nex-agent: sessions index ──

  registry.call("nex-agent", "subscribeSessionsIndexV4", async (arg) => {
    const target = requireWorkspaceTarget(arg);
    return publisher.subscribeSessionsIndex(target);
  });

  registry.call("nex-agent", "resyncSessionsIndexV4", async (arg) => {
    const record = asRecord(arg);
    const target = requireWorkspaceTarget(arg);
    const subscriptionId = requireString(record["subscriptionId"], "subscriptionId");
    return publisher.resyncSessionsIndex({ ...target, subscriptionId });
  });

  registry.call("nex-agent", "unsubscribeSessionsIndexV4", async (arg) => {
    const record = asRecord(arg);
    requireWorkspaceTarget(arg);
    const subscriptionId = requireString(record["subscriptionId"], "subscriptionId");
    publisher.unsubscribeSessionsIndex(subscriptionId);
    return undefined;
  });

  registry.listen("nex-agent", "onDynamicSessionsIndexFrame", (arg, emit) => {
    const target = requireWorkspaceTarget(arg);
    return publisher.addSessionsIndexSink(target.workspacePath, (frame) =>
      emit(frame as JsonValue),
    );
  });

  // ── nex-task ──

  // The server routes by `workspaceIdentity` and the UI groups sidebar rows by it, so
  // every nex-task call must echo the identity it received.
  registry.call("nex-task", "listTasks", async (arg) => {
    const target = requireWorkspaceTarget(arg);
    return nexTask.listTasks(target);
  });
  registry.call("nex-task", "listPinnedTasks", async (arg) => {
    return nexTask.listPinnedTasks(optionalWorkspaceTarget(arg));
  });
  registry.call("nex-task", "listArchivedTasks", async (arg) => {
    return nexTask.listArchivedTasks(optionalWorkspaceTarget(arg));
  });
  registry.call("nex-task", "listDeletedTaskIds", async (arg) => {
    const workspacePath = requireWorkspace(arg);
    return nexTask.listDeletedTaskIds({ workspacePath });
  });
  // 全文搜索（docs/next/automations-views-search.md §C）：只返回会话 id 与摘要片段，
  // 行数据仍由 server 的行缓存提供。query 必须是字符串（空串按“无匹配”处理）。
  registry.call("nex-task", "searchTasks", async (arg) => {
    const target = requireWorkspaceTarget(arg);
    const record = asRecord(arg);
    const query = record["query"];
    if (typeof query !== "string") throw new Error("query is required");
    const limit = record["limit"];
    return nexTask.searchTasks({
      ...target,
      query,
      ...(typeof limit === "number" && Number.isFinite(limit) ? { limit } : {}),
    });
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

  // Sidebar task state (docs/next/task-state.md). Every method carries the workspace
  // target so the server routes it to the owning runner.
  registry.call("nex-task", "archiveTask", async (arg) => {
    const target = requireTaskTarget(arg);
    return nexTask.archiveTask(target);
  });
  registry.call("nex-task", "unarchiveTask", async (arg) => {
    const target = requireTaskTarget(arg);
    return nexTask.unarchiveTask(target);
  });
  registry.call("nex-task", "setTaskPinned", async (arg) => {
    const target = requireTaskTarget(arg);
    return nexTask.setTaskPinned({ ...target, pinned: requireBoolean(arg, "pinned") });
  });
  registry.call("nex-task", "setTaskUnread", async (arg) => {
    const target = requireTaskTarget(arg);
    const record = asRecord(arg);
    const expected = record["expectedUnreadAt"];
    return nexTask.setTaskUnread({
      ...target,
      unread: requireBoolean(arg, "unread"),
      // CAS 只在调用方给出数字标记时启用；其余保持无条件写入语义。
      ...(typeof expected === "number" ? { expectedUnreadAt: expected } : {}),
    });
  });
  registry.call("nex-task", "renameTask", async (arg) => {
    const target = requireTaskTarget(arg);
    return nexTask.renameTask({ ...target, title: requireStringField(arg, "title") });
  });
  registry.call("nex-task", "deleteTask", async (arg) => {
    const target = requireTaskTarget(arg);
    await nexTask.deleteTask(target);
    return undefined;
  });
  registry.call("nex-task", "deleteArchivedTask", async (arg) => {
    const target = requireTaskTarget(arg);
    return nexTask.deleteArchivedTask(target);
  });
  registry.call("nex-task", "deleteArchivedTasks", async (arg) => {
    const target = requireTaskTarget(arg);
    const record = asRecord(arg);
    const taskIds = Array.isArray(record["taskIds"])
      ? record["taskIds"].filter((item): item is string => typeof item === "string")
      : [];
    return nexTask.deleteArchivedTasks({ ...target, taskIds });
  });

  registry.listen("nex-task", "onDynamicWorkspaceEvent", (arg, emit) => {
    const target = requireWorkspaceTarget(arg);
    return nexTask.onDynamicWorkspaceEvent(
      target.workspacePath,
      (event) => emit(event as unknown as JsonValue),
      target.workspaceIdentity,
    );
  });

  // ── nex-session ──

  // P2: the server doc's skills, forwarded to this runner, so the `/` menu of
  // the workspace includes them before a session materializes them.
  let serverSlashCommands: Array<{
    name: string;
    description?: string;
    source?: "builtin" | "custom";
  }> = [];
  registry.call("nex-agent", "getSkillReferenceCatalog", async (arg) => {
    const target = requireWorkspaceTarget(arg);
    const resources = await engine.piResources(target.workspacePath);
    const skills = skillCatalogFrom(resources);
    serverSlashCommands = skills.map((skill) => ({
      name: `skill:${skill.name}`,
      description: skill.description,
      source: "custom" as const,
    }));
    return { authority: "workspace", skills };
  });

  registry.call("nex-session", "readWorkspacePresentation", async (arg) => {
    const target = requireWorkspaceTarget(arg);
    // P2: pi's own prompt templates and `/skill:` commands (pi expands them).
    const { resources, skillCommands } = await loadCatalog(engine, target.workspacePath);
    return readWorkspacePresentation(
      target,
      composeSlashCommands({ slashCommands: serverSlashCommands, resources, skillCommands }),
    );
  });

  // P1: live MCP statuses of the newest session (the server forwards this).
  //
  // 修复依据：此前直接返回引擎内部的 `{state, toolCount}`，而浏览器按
  // `nexMcpServerStatusSnapshotSchema`（`status`、`updatedAt` 必填）解析，整条结果
  // 校验失败，Settings 里所有服务器都显示 “Unable to retrieve MCP status”。
  // 这里按共享协议的线上形状映射，并且只在该 schema 校验通过后才返回。
  // P1: 状态查询走隐藏的状态会话（引擎的 `requestMcpStatus`），在第一个聊天会话之前
  // 也能给出真实的 connected/工具数；设置页的 "connect" 模式因此不必先开一次聊天。
  registry.call("mcp-sync", "listWorkspaceMcpServerStatuses", async () => {
    const snapshot = await engine.requestMcpStatus();
    // Logged so a settings status that never leaves "connecting" is traceable
    // to the runner's view (counts only, never values).
    options.logger.info("mcp statuses reported", {
      owned: snapshot.owned,
      servers: Object.keys(snapshot.statuses).length,
      states: Object.values(snapshot.statuses)
        .map((status) => status.state)
        .join(","),
    });
    const statuses: Record<string, unknown> = {};
    for (const status of Object.values(snapshot.statuses)) {
      const mapped = toWireStatus(status);
      if (mapped) statuses[status.name] = mapped;
    }
    return { statuses };
  });

  // ── model-selection / provider-settings ──
  //
  // These remain runner-served for peers that still forward them (an older
  // nex-next server, or the desktop host); the nex-next server now answers them
  // itself from the config doc plus this runner's `configApplied` facts
  // (docs/next/config-mcp-skills.md decision 6) and never forwards them.

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

/**
 * Engine MCP status → `nexMcpServerStatusSnapshotSchema` (the browser validates
 * every entry strictly; one bad row fails the whole list).
 */
function toWireStatus(status: McpServerStatus): Record<string, unknown> | null {
  const parsed = nexMcpServerStatusSnapshotSchema.safeParse({
    status: status.state === "error" ? "failed" : status.state,
    transport: status.transport,
    toolCount: status.toolCount,
    ...(status.toolNames?.length ? { toolNames: status.toolNames } : {}),
    updatedAt: new Date().toISOString(),
    ...(status.error ? { error: status.error.slice(0, 500) } : {}),
    ...(status.failureKind ? { failureKind: status.failureKind } : {}),
  });
  return parsed.success ? (parsed.data as Record<string, unknown>) : null;
}

function workspaceIdentityFromArg(arg: JsonValue): string | undefined {
  if (!isRecord(arg)) return undefined;
  const identity = arg["workspaceIdentity"];
  if (typeof identity === "string" && identity.trim().length > 0) return identity.trim();
  const workspace = arg["workspace"];
  if (isRecord(workspace)) {
    const nested = workspace["workspaceIdentity"];
    if (typeof nested === "string" && nested.trim().length > 0) return nested.trim();
  }
  return undefined;
}

/** Read-only queries ignore the CAS watermark; the runner builds it from its own state. */
function numberOrZero(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function stringOrEmpty(value: unknown): string {
  return typeof value === "string" ? value : "";
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

function requireStringField(arg: JsonValue, name: string): string {
  const value = asRecord(arg)[name];
  if (typeof value !== "string" || value.trim().length === 0) {
    throw new Error(`${name} is required`);
  }
  return value;
}

function requireBoolean(arg: JsonValue, name: string): boolean {
  const value = asRecord(arg)[name];
  if (typeof value !== "boolean") throw new Error(`${name} must be a boolean`);
  return value;
}

export { workspacePathFromParams };
