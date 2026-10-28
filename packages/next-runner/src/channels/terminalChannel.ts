/**
 * Terminal + system channels of the runner (docs/next/workspace-tools.md
 * batch 1, decisions 4/5): terminals live in the pty-host child with a
 * serialize mirror; `create` reattaches a live `terminalId`; the first
 * `onDynamicData` listen of a generation replays the serialized screen; several
 * viewers may watch one terminal; `dispose` lets the exit event flow before the
 * entry is reaped (the onSessionExit bridge owns reaping).
 *
 * The `system` channel answers host facts of the runner machine (first served
 * root as homedir, the runner's platform).
 */
import { homedir } from "node:os";
import { resolveTerminalFontProfile } from "@nex/services/node";
import type { ChannelRegistration, EventEmitter } from "../link/registry.js";
import type { JsonValue } from "../link/frames.js";
import type { Logger } from "../logger.js";
import { PtyHostClient } from "../pty-host/client.js";
import { resolveTerminalShell } from "../pty-host/shell.js";
import { isMachineScopeTarget } from "../workspaces/machineScope.js";
import {
  recordArg,
  resolveInsideWorkspaceReal,
  asString,
  errorMessage,
} from "./workspaceToolsShared.js";

export interface TerminalChannelOptions {
  logger: Logger;
  /** Absolute path check against the served workspace roots. */
  rootsForCheck: () => Promise<readonly string[]>;
  /** Live served roots (first one is reported as the machine's home). */
  servedRoots: () => readonly string[];
  terminalSettings?: { terminalFontFamily?: string; terminalInheritSystemProfile?: boolean };
}

export interface RegisteredTerminalChannel {
  terminalIds: () => ReadonlySet<string>;
  dispose: () => Promise<void>;
}

interface TerminalEntry {
  dataListeners: Set<EventEmitter>;
  exitListeners: Set<(code: number) => void>;
}

export function registerTerminalChannel(
  registry: ChannelRegistration,
  options: TerminalChannelOptions,
): RegisteredTerminalChannel {
  const { logger } = options;
  const ptyHost = new PtyHostClient();
  const terminals = new Map<string, TerminalEntry>();
  const terminalIds = new Set<string>();

  const requireTerminal = (id: string): TerminalEntry => {
    const entry = terminals.get(id);
    if (!entry) throw new Error(`Terminal not found: ${id}`);
    return entry;
  };

  registry.call("terminal", "create", async (arg) => {
    const record = recordArg(arg);
    const cols = Number(record["cols"] ?? 80);
    const rows = Number(record["rows"] ?? 24);
    const requestedId = asString(record["terminalId"]);
    // Reattach (decision 5): a live terminal is resized and its id returned.
    if (requestedId && terminals.has(requestedId)) {
      await ptyHost.resize(requestedId, cols, rows).catch((error: unknown) => {
        logger.warn("[terminal] reattach resize failed", { message: errorMessage(error) });
      });
      return {
        id: requestedId,
        shell: "",
        fontFamily: "",
        fontFamilySource: "system",
      };
    }
    // cwd: optional; must be inside a served root when provided.
    let cwd: string | undefined = undefined;
    if (asString(record["cwd"])) {
      const resolved = await resolveInsideWorkspaceReal(
        asString(record["cwd"]) as string,
        await options.rootsForCheck(),
      );
      if (resolved === null) {
        throw new Error(`terminal cwd is outside this runner's workspaces: ${record["cwd"]}`);
      }
      cwd = resolved;
    }
    await ptyHost.start();
    const created = await ptyHost.create({
      ...(requestedId ? { id: requestedId } : {}),
      cols,
      rows,
      ...(cwd ? { cwd } : {}),
      shell: resolveTerminalShell(),
    });
    const id = created.id;
    terminals.set(id, { dataListeners: new Set(), exitListeners: new Set() });
    terminalIds.add(id);
    const fontProfile = resolveTerminalFontProfile({
      settings: {
        terminalFontFamily: options.terminalSettings?.terminalFontFamily,
        terminalInheritSystemProfile:
          options.terminalSettings?.terminalInheritSystemProfile ?? true,
      },
      env: process.env,
    });
    return {
      id,
      shell: created.shell,
      fontFamily: fontProfile.fontFamily,
      ...(fontProfile.fontSize !== undefined ? { fontSize: fontProfile.fontSize } : {}),
      ...(fontProfile.theme !== undefined ? { theme: fontProfile.theme } : {}),
      fontFamilySource: fontProfile.source,
    } as JsonValue;
  });

  // pty-host → link 桥：每个终端一份分发（data 到所有 viewer，exit 到 exit
  // listeners 然后回收 entry）。注册一次，存活期与模块相同。
  ptyHost.onSessionData((event) => {
    const entry = terminals.get(event.id);
    if (!entry) return;
    for (const emit of entry.dataListeners) emit(event.data);
  });
  ptyHost.onSessionExit((event) => {
    const entry = terminals.get(event.id);
    if (!entry) return;
    for (const emit of entry.exitListeners) emit(event.code);
    terminals.delete(event.id);
    terminalIds.delete(event.id);
  });

  registry.call("terminal", "write", async (arg) => {
    const record = recordArg(arg);
    const id = asString(record["id"]);
    const data = typeof record["data"] === "string" ? record["data"] : null;
    if (!id || data === null) throw new Error("id and data are required");
    requireTerminal(id);
    await ptyHost.write(id, data);
    return null;
  });

  registry.call("terminal", "resize", async (arg) => {
    const record = recordArg(arg);
    const id = asString(record["id"]);
    if (!id) throw new Error("id is required");
    requireTerminal(id);
    await ptyHost.resize(id, Number(record["cols"] ?? 80), Number(record["rows"] ?? 24));
    return null;
  });

  registry.call("terminal", "dispose", async (arg) => {
    const record = recordArg(arg);
    const id = asString(record["id"]);
    if (!id) throw new Error("id is required");
    requireTerminal(id);
    // kill 触发 pty-host 的 exit 帧；entry 的回收与 exit listeners 的分发都
    // 在 onSessionExit 桥里完成（这里提前 delete 会让 exit 事件静默丢失，
    // 浏览器侧 onDynamicExit 永不触发、side pane 卡在等待关闭）。
    await ptyHost.kill(id);
    return null;
  });

  registry.listen("terminal", "onDynamicData", (arg, emit) => {
    const id = typeof arg === "string" ? arg : asString(recordArg(arg)["id"]);
    if (!id) return;
    const entry = terminals.get(id);
    if (!entry) return;
    const isFirst = entry.dataListeners.size === 0;
    entry.dataListeners.add(emit);
    // Replay (decision 5): the first listener of a generation receives the
    // serialized screen, then live chunks.
    if (isFirst) {
      void ptyHost
        .serialize(id)
        .then((screen) => {
          if (entry.dataListeners.has(emit) && screen) emit(screen);
        })
        .catch(() => undefined);
    }
    return {
      dispose: () => {
        entry.dataListeners.delete(emit);
      },
    };
  });

  registry.listen("terminal", "onDynamicExit", (arg, emit) => {
    const id = typeof arg === "string" ? arg : asString(recordArg(arg)["id"]);
    if (!id) return;
    const entry = terminals.get(id);
    if (!entry) return;
    entry.exitListeners.add(emit);
    return {
      dispose: () => {
        entry.exitListeners.delete(emit);
      },
    };
  });

  // ── system（host facts of the runner machine）──
  // 机器级作用域（docs/next/runner-folders.md 决策 2）下目录浏览器从这里取起点：
  // 必须回报 runner 机器的真实 homedir，而不是第一个 served 根（那只是安装器写的
  // 默认目录，用户要在其之外选择新文件夹）。
  registry.call("system", "info", async (arg) => {
    if (isMachineScopeTarget(arg)) {
      return { homedir: homedir(), platform: process.platform };
    }
    return { homedir: options.servedRoots()[0] ?? homedir(), platform: process.platform };
  });
  registry.call("system", "listIntegratedTerminalShells", async () => []);
  registry.call("system", "probeIntranet", async (arg) => {
    const record = recordArg(arg);
    return { reachable: false, reason: "not supported on the runner" } as JsonValue;
  });

  return {
    terminalIds: () => terminalIds,
    dispose: async () => {
      try {
        await ptyHost.dispose();
      } catch {
        // Shutdown path: the child is SIGKILLed by dispose; failures are not
        // actionable here.
      }
      terminals.clear();
      terminalIds.clear();
    },
  };
}
