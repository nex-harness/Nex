/**
 * Workspace tool channels (docs/next/workspace-tools.md batch 1, decisions 2/3/5-7):
 * `file`, `file-watcher`, `media-preview`, `git`, `git-checkpoint` on the runner.
 *
 * - The implementations are the `@nex/services` factories (decision 3): the file
 *   tree/preview/mention surface, git CLI and the watcher are the same code the
 *   desktop host runs, so behavior stays identical.
 * - The runner is the security boundary for paths (decision 2): every path
 *   argument is resolved (realpath where it exists) and must stay inside a
 *   served workspace root; symlinks escaping a root are refused.
 * - Binary results (`readFileRange`) cross the runner link as the rpcwire JSON
 *   bridge's `{"$buf":…}` form (decision 7): the Go server converts that
 *   sentinel into real buffers for the browser.
 * - terminal/system live in `terminalChannel.ts` (pty-host + replay).
 */
import {
  createFileService,
  createFileWatcherService,
  createGitService,
  createMediaPreviewService,
} from "@nex/services/node";
import type { IFileService, IFileWatcherService } from "@nex/services";
import type { IGitService, IMediaPreviewService } from "@nex/services";
import type { FileEntry } from "@nex/shared";
import type { ChannelRegistration } from "../link/registry.js";
import type { JsonValue } from "../link/frames.js";
import type { Logger } from "../logger.js";
import type { WorkspaceRoots } from "../workspaces/workspaceRoots.js";
import {
  isMachineScopeTarget,
  createMachineScopeGuardedRegistration,
} from "../workspaces/machineScope.js";
import {
  asString,
  recordArg,
  errorMessage,
  resolveInsideWorkspace,
  resolveInsideWorkspaceReal,
  resolveMachineScopePath,
} from "./workspaceToolsShared.js";
import { registerTerminalChannel, type RegisteredTerminalChannel } from "./terminalChannel.js";

export {
  resolveInsideWorkspace,
  resolveInsideWorkspaceReal,
  recordArg,
  asString,
  errorMessage,
} from "./workspaceToolsShared.js";

export interface WorkspaceToolsOptions {
  logger: Logger;
  /** The live served-folder owner; root checks read it at call time. */
  roots: WorkspaceRoots;
  /** Static settings surface for the terminal font profile (no setting service on the runner). */
  terminalSettings?: { terminalFontFamily?: string; terminalInheritSystemProfile?: boolean };
}

export interface RegisteredWorkspaceTools {
  terminalIds: () => ReadonlySet<string>;
  dispose: () => Promise<void>;
}

/**
 * `$buf` sentinel forms of the rpcwire JSON bridge (server internal/rpcwire
 * jsonbridge.go): the browser receives a real Uint8Array, the runner link only
 * carries JSON. `Uint8Array` results must be encoded here; arguments that carry
 * buffers (none today) would decode symmetrically.
 */
export function encodeBufferForWire(value: Uint8Array): { $buf: string; $vs: boolean } {
  return { $buf: Buffer.from(value).toString("base64"), $vs: false };
}

export function decodeWireBuffer(value: unknown): Uint8Array | null {
  if (typeof value !== "object" || value === null) return null;
  const record = value as Record<string, unknown>;
  if (typeof record["$buf"] !== "string") return null;
  return new Uint8Array(Buffer.from(record["$buf"], "base64"));
}

export function registerWorkspaceTools(
  baseRegistry: ChannelRegistration,
  options: WorkspaceToolsOptions,
): RegisteredWorkspaceTools {
  const { logger } = options;
  // 机器级目标（`workspacePath: "~"`）只允许 readdir/stat/system.info 与
  // runner-workspaces.*（docs/next/runner-folders.md 决策 2）。这些频道注册在原始
  // registry 上（不经 register.ts 的 workspace guard），因此在这里补同一道闸门：
  // 否则带 `~` 的 readTextFile/terminal/git 调用会绕过限制。
  const registry = createMachineScopeGuardedRegistration(baseRegistry);
  // 根集合是活的（docs/next/runner-folders.md decision 1）：每次校验都读
  // WorkspaceRoots，新增的目录无需重启即可生效；realRoots 内部缓存并在变更时失效。
  const rootsForCheck = (): Promise<readonly string[]> => options.roots.realRoots();
  const servedPaths = (): readonly string[] => options.roots.list().map((folder) => folder.path);

  const fileService: IFileService = createFileService();
  const watcherService: IFileWatcherService = createFileWatcherService({
    logger: {
      info: () => undefined,
      warn: (_scope: string, message: string) => logger.warn("[file-watcher] " + message),
      error: (_scope: string, message: string) => logger.error("[file-watcher] " + message),
    } as never,
  });
  const mediaService: IMediaPreviewService = createMediaPreviewService({
    fileService,
    inlineMaxBytes: 4 * 1024 * 1024,
  });
  const gitService: IGitService = createGitService();

  const requirePathInside = async (
    arg: Record<string, unknown>,
    field: string,
  ): Promise<string> => {
    const raw = asString(arg[field]);
    if (!raw) throw new Error(`${field} is required`);
    const resolved = await resolveInsideWorkspaceReal(raw, await rootsForCheck());
    if (resolved === null) throw new Error(`path is outside this runner's workspaces: ${raw}`);
    return resolved;
  };

  /**
   * 修复依据（docs/next/runner-folders.md 决策 2）：目录浏览器在机器级作用域
   * （`workspacePath: "~"`）下必须能浏览 served 根之外的目录，否则用户无法选中
   * 新文件夹。这里只在目标为机器级时放开到「任意绝对路径 + realpath」，其余
   * 调用仍走 served 根 containment。
   */
  const requirePathForBrowse = async (
    arg: Record<string, unknown>,
    field: string,
  ): Promise<{ path: string; machineScope: boolean }> => {
    if (isMachineScopeTarget(arg)) {
      const raw = asString(arg[field]);
      if (!raw) throw new Error(`${field} is required`);
      return { path: await resolveMachineScopePath(raw), machineScope: true };
    }
    return { path: await requirePathInside(arg, field), machineScope: false };
  };

  const requireRootInside = async (
    arg: Record<string, unknown>,
    field: string,
  ): Promise<string> => {
    const raw = asString(arg[field]);
    if (!raw) throw new Error(`${field} is required`);
    const resolved = resolveInsideWorkspace(raw, await rootsForCheck());
    if (resolved === null) throw new Error(`path is outside this runner's workspaces: ${raw}`);
    return resolved;
  };

  // ── file ──
  const passthroughFile = (
    method: string,
    transform: (arg: Record<string, unknown>) => Promise<unknown>,
  ) => {
    registry.call("file", method, async (arg) => {
      try {
        return (await transform(recordArg(arg))) as JsonValue;
      } catch (error) {
        throw new Error(errorMessage(error));
      }
    });
  };

  passthroughFile("searchWorkspaceFiles", async (arg) => {
    const rootPath = await requireRootInside(arg, "rootPath");
    return fileService.searchWorkspaceFiles({
      rootPath,
      query: asString(arg["query"]) ?? "",
      ...(arg["limit"] !== undefined ? { limit: Number(arg["limit"]) } : {}),
      ...(arg["refresh"] === true ? { refresh: true } : {}),
    });
  });
  passthroughFile("readdir", async (arg) => {
    const { path, machineScope } = await requirePathForBrowse(arg, "path");
    const entries = await fileService.readdir({
      path,
      ...(arg["includeHidden"] === true ? { includeHidden: true } : {}),
    });
    // 机器级作用域只列目录：文件既不能成为 served 根，也不需要展示。
    return machineScope
      ? entries.filter((entry: FileEntry) => entry.type === "directory")
      : entries;
  });
  passthroughFile("stat", async (arg) => {
    const { path } = await requirePathForBrowse(arg, "path");
    return fileService.stat({ path });
  });
  passthroughFile("checkFilesExist", async (arg) => {
    const rawPaths = Array.isArray(arg["paths"]) ? arg["paths"] : [];
    const paths = rawPaths.filter((p): p is string => typeof p === "string");
    const roots = await rootsForCheck();
    const checked = await Promise.all(
      paths.map(async (raw) => {
        const resolved = await resolveInsideWorkspaceReal(raw, roots);
        if (resolved === null) return { path: raw, exists: false };
        return {
          path: raw,
          exists: await fileService.stat({ path: resolved }).then(Boolean, () => false),
        };
      }),
    );
    return checked;
  });
  passthroughFile("resolvePath", async (arg) => {
    const path = await requirePathInside(arg, "path");
    return fileService.resolvePath({ path });
  });
  passthroughFile("readTextFile", async (arg) => {
    const path = await requirePathInside(arg, "path");
    return fileService.readTextFile({
      path,
      ...(arg["offset"] !== undefined ? { offset: Number(arg["offset"]) } : {}),
      ...(arg["length"] !== undefined ? { length: Number(arg["length"]) } : {}),
    });
  });
  passthroughFile("readMediaPreview", async (arg) => {
    const path = await requirePathInside(arg, "path");
    return fileService.readMediaPreview({
      path,
      ...(arg["maxBytes"] !== undefined ? { maxBytes: Number(arg["maxBytes"]) } : {}),
    });
  });
  passthroughFile("readFileRange", async (arg) => {
    const path = await requirePathInside(arg, "path");
    const data = await fileService.readFileRange({
      path,
      offset: Number(arg["offset"] ?? 0),
      length: Number(arg["length"] ?? 0),
    });
    return encodeBufferForWire(data);
  });
  passthroughFile("readBinaryPreview", async (arg) => {
    const path = await requirePathInside(arg, "path");
    return fileService.readBinaryPreview({
      path,
      ...(arg["maxBytes"] !== undefined ? { maxBytes: Number(arg["maxBytes"]) } : {}),
    });
  });
  passthroughFile("listWorkspaceFilesLength", async (arg) => {
    const rootPath = await requireRootInside(arg, "rootPath");
    return fileService.listWorkspaceFilesLength({ rootPath });
  });
  passthroughFile("listWorkspaceFilesRange", async (arg) => {
    const rootPath = await requireRootInside(arg, "rootPath");
    return fileService.listWorkspaceFilesRange({
      rootPath,
      offset: Number(arg["offset"] ?? 0),
      length: Number(arg["length"] ?? 0),
    });
  });
  passthroughFile("readWorkspaceFileSearchIgnore", async (arg) => {
    const rootPath = await requireRootInside(arg, "rootPath");
    return fileService.readWorkspaceFileSearchIgnore({
      rootPath,
      ...(arg["source"] !== undefined ? { source: arg["source"] as "file" | "template" } : {}),
    });
  });
  passthroughFile("applyWorkspaceFileSearchIgnoreTransform", async (arg) => {
    const rootPath = await requireRootInside(arg, "rootPath");
    const transform = arg["transform"] === "reset-defaults" ? "reset-defaults" : "sync-gitignore";
    return fileService.applyWorkspaceFileSearchIgnoreTransform({ rootPath, transform });
  });
  passthroughFile("writeWorkspaceFileSearchIgnore", async (arg) => {
    const rootPath = await requireRootInside(arg, "rootPath");
    return fileService.writeWorkspaceFileSearchIgnore({
      rootPath,
      content: asString(arg["content"]) ?? "",
    });
  });
  passthroughFile("ensureConversationWorkspace", async () => {
    // Web already answers this from a server stub; on the runner the
    // conversation scratch space is per-workspace state owned by the engine.
    return { path: servedPaths()[0] ?? "", created: false, workspacePurpose: "conversation" };
  });

  // ── media-preview ──
  registry.call("media-preview", "prepare", async (arg) => {
    const record = recordArg(arg);
    const path = await requirePathInside(record, "path");
    const expectedKindRaw = asString(record["expectedKind"]);
    const expectedKind: "audio" | "video" | null =
      expectedKindRaw === "video" || expectedKindRaw === "audio" ? expectedKindRaw : null;
    return (await mediaService.prepare({
      path,
      expectedKind: expectedKind ?? "audio",
    })) as JsonValue;
  });

  // ── file-watcher ──
  registry.call("file-watcher", "watch", async (arg) => {
    const record = recordArg(arg);
    const path = await requirePathInside(record, "path");
    return watcherService.watch({
      path,
      ...(record["recursive"] === true ? { recursive: true } : {}),
    });
  });
  registry.call("file-watcher", "unwatch", async (arg) => {
    const record = recordArg(arg);
    const id = asString(record["id"]);
    if (!id) throw new Error("id is required");
    await watcherService.unwatch({ id });
    return null;
  });
  registry.listen("file-watcher", "onDynamicChange", (arg, emit) => {
    // The listen argument is the watcher id (positional string, unwrapped by
    // the link). Events are forwarded verbatim — the watcher id namespace is
    // per runner, and the server routes the listen by the watch's binding.
    const id = typeof arg === "string" ? arg : asString(recordArg(arg)["id"]);
    if (!id) return;
    const subscription = watcherService.onDynamicChange(id)((event) => {
      emit(event as unknown as JsonValue);
    });
    return { dispose: () => subscription.dispose() };
  });

  // ── git ──
  const gitPassthrough = (method: string) => {
    registry.call("git", method, async (arg) => {
      const record = recordArg(arg);
      const workspacePath = await requireRootInside(record, "workspacePath");
      const handler = (
        gitService as unknown as Record<string, (params: never) => Promise<unknown>>
      )[method];
      if (typeof handler !== "function") throw new Error(`git method not found: ${method}`);
      try {
        return (await handler({ ...record, workspacePath } as never)) as JsonValue;
      } catch (error) {
        throw new Error(errorMessage(error));
      }
    });
  };
  for (const method of [
    "getRepositorySummary",
    "getWorkspaceRepositoryInfo",
    "getLocalBranches",
    "getCommitGraph",
    "switchBranch",
    "createBranchAndSwitch",
    "getChanges",
    "getIgnoredPaths",
    "getDiff",
    "getBranchComparison",
    "stagePaths",
    "unstagePaths",
    "discardPaths",
    "commit",
    "push",
    "getIdentity",
    "refresh",
  ] as const) {
    gitPassthrough(method);
  }
  // generateCommitMessage: no LLM wiring on the runner in batch 1 — deterministic
  // fallback (branch + file list), spec decision 8.
  registry.call("git", "generateCommitMessage", async (arg) => {
    const record = recordArg(arg);
    const workspacePath = await requireRootInside(record, "workspacePath");
    const branchName = asString(record["branchName"]);
    const files = Array.isArray(record["files"]) ? record["files"] : [];
    const names = files
      .map((file) =>
        typeof file === "object" && file !== null
          ? (file as Record<string, unknown>)["path"]
          : null,
      )
      .filter((name): name is string => typeof name === "string");
    const summary = branchName
      ? `${branchName}: ${names
          .slice(0, 5)
          .map((name) => name.split("/").pop())
          .join(", ")}`
      : `changes: ${names
          .slice(0, 5)
          .map((name) => name.split("/").pop())
          .join(", ")}`;
    return { message: summary, providerId: "", model: "" } as JsonValue;
  });

  // git-checkpoint: the runner's checkpoints live in the engine (pi hooks); the
  // UI has no callers. Register the channel so routing never 404s.
  for (const method of [
    "createCheckpoint",
    "diffCheckpoints",
    "restoreBetweenCheckpoints",
    "deleteCheckpoint",
  ]) {
    registry.call("git-checkpoint", method, async () => {
      throw new Error("git checkpoints are not exposed on the runner channel (engine-owned)");
    });
  }

  // ── terminal + system（pty-host 子进程）──
  const terminalChannel: RegisteredTerminalChannel = registerTerminalChannel(registry, {
    logger,
    rootsForCheck,
    servedRoots: servedPaths,
    terminalSettings: options.terminalSettings,
  });

  return {
    terminalIds: terminalChannel.terminalIds,
    dispose: async () => {
      await terminalChannel.dispose();
      watcherService.disposeAll();
    },
  };
}
