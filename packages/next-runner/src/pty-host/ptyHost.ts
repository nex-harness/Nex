// pty-host 子进程入口：以 `process.execPath pty-host.mjs` 启动，用 NDJSON 在
// stdin/stdout 上与 runner 父进程通信（协议见 ./protocol.ts）。
//
// 单独的进程（而不是 runner 内的模块）换来三点：
// - 终端生命周期与 runner 的 JS 堆/事件循环解耦，bundle 更新或 runner 重启时
//   pty 可以按策略回收，而不会因为 runner 崩溃连带丢掉 shell；
// - node-pty 原生模块只在被 spawn 时才加载，runner 主进程不必承担崩溃面；
// - 每个终端的状态（headless xterm 镜像）隔离在本进程内。
//
// 注意：stdout 只用于协议帧，任何日志都必须写 stderr，否则会污染帧流。

import { createInterface } from "node:readline";
import type { IPty } from "node-pty";
import { ensureNodePtyAssets, ptyPlatformKey } from "./assets.js";
import {
  parsePtyHostParentMessage,
  type PtyHostChildMessage,
  type PtyHostCreateResult,
  type PtyHostTerminalInfo,
} from "./protocol.js";
import { PtySession } from "./session.js";
import { resolveTerminalCwd, resolveTerminalEnv, resolveTerminalShell } from "./shell.js";

type NodePtyModule = typeof import("node-pty");

let nodePty: NodePtyModule | null = null;
let nodePtyError: string | null = null;

async function loadNodePty(): Promise<NodePtyModule> {
  if (nodePty) return nodePty;
  if (nodePtyError) throw new Error(nodePtyError);
  try {
    // 先确保原生资产就位（Linux 预编译拷入 / darwin spawn-helper 权限），再加载 addon。
    const assets = ensureNodePtyAssets();
    if (!assets.ptyNodePath) {
      process.stderr.write(
        `[pty-host] node-pty prebuild missing for ${ptyPlatformKey()} (target: ${assets.targetDir})\n`,
      );
    }
    nodePty = (await import("node-pty")) as NodePtyModule;
    return nodePty;
  } catch (error) {
    nodePtyError = `node-pty failed to load: ${error instanceof Error ? error.message : String(error)}`;
    throw new Error(nodePtyError);
  }
}

function spawnPty(
  pty: NodePtyModule,
  shell: string,
  options: { cols: number; rows: number; cwd: string; env: NodeJS.ProcessEnv },
): IPty {
  if (process.platform !== "win32") {
    return pty.spawn(shell, [], { name: "xterm-256color", encoding: "utf8", ...options });
  }

  // Windows：优先 node-pty 自带的 ConPTY DLL（prebuilds/win32-*/conpty/*），
  // 加载它失败（缺 conpty.dll / 模块句柄错误）时回退到系统 ConPTY。
  const base = { name: "xterm-256color", encoding: "utf8", useConpty: true, ...options };
  try {
    return pty.spawn(shell, [], { ...base, useConptyDll: true });
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    if (!/conpty\.node|conpty\.dll|error code:\s*126/i.test(message)) throw error;
    process.stderr.write(`[pty-host] ConPTY DLL unavailable, using system ConPTY: ${message}\n`);
    return pty.spawn(shell, [], { ...base, useConptyDll: false });
  }
}

const sessions = new Map<string, PtySession>();
let nextId = 0;

function send(message: PtyHostChildMessage): void {
  process.stdout.write(`${JSON.stringify(message)}\n`);
}

async function handleCreate(
  requestId: number,
  opts: {
    id?: string;
    cols: number;
    rows: number;
    cwd?: string;
    shell?: string;
    env?: Record<string, string>;
  },
): Promise<void> {
  const id = opts.id?.trim() || `t${++nextId}`;
  if (sessions.has(id)) throw new Error(`terminal already exists: ${id}`);

  const pty = await loadNodePty();
  // 调用方（runner）传入 env 时以此为基础，仍走同一套 TERM/locale/PATH 规范化。
  const env = resolveTerminalEnv(opts.env ? { PATH: process.env.PATH, ...opts.env } : process.env);
  // 显式 shell（调用方校验过可执行）优先；否则按 SHELL → /bin/zsh → … 解析。
  const shell = opts.shell?.trim() || resolveTerminalShell();
  const cwd = resolveTerminalCwd(opts.cwd);

  const session = new PtySession(
    id,
    {
      shell,
      args: [],
      cols: opts.cols,
      rows: opts.rows,
      cwd,
      env,
      spawn: (spawnOptions) => spawnPty(pty, shell, spawnOptions),
    },
    {
      emitData: (data) => send({ t: "data", id, d: data }),
      emitExit: (code, signal) => {
        sessions.delete(id);
        send({ t: "exit", id, code, signal });
      },
    },
  );
  sessions.set(id, session);
  session.start();
  const result: PtyHostCreateResult = { id, shell: session.shell, cwd, pid: session.pid };
  send({ t: "res", id: requestId, ok: true, r: result });
}

function getSession(id: string): PtySession {
  const session = sessions.get(id);
  if (!session) throw new Error(`terminal not found: ${id}`);
  return session;
}

async function handleRequest(
  message: Extract<ReturnType<typeof parsePtyHostParentMessage>, { t: "req" }>,
): Promise<void> {
  try {
    switch (message.m) {
      case "create":
        await handleCreate(message.id, message.opts);
        return;
      case "write":
        getSession(message.id_).write(message.data);
        send({ t: "res", id: message.id, ok: true });
        return;
      case "resize":
        getSession(message.id_).resize(message.cols, message.rows);
        send({ t: "res", id: message.id, ok: true });
        return;
      case "kill":
        getSession(message.id_).kill(message.signal);
        send({ t: "res", id: message.id, ok: true });
        return;
      case "serialize": {
        const screen = await getSession(message.id_).serialize();
        send({ t: "res", id: message.id, ok: true, r: { screen } });
        return;
      }
      case "list": {
        const terminals: PtyHostTerminalInfo[] = [...sessions.values()].map((session) => ({
          id: session.id,
          cols: session.cols,
          rows: session.rows,
          shell: session.shell,
          cwd: session.cwd,
          pid: session.pid,
        }));
        send({ t: "res", id: message.id, ok: true, r: { terminals } });
        return;
      }
    }
  } catch (error) {
    send({
      t: "res",
      id: message.id,
      ok: false,
      e: error instanceof Error ? error.message : String(error),
    });
  }
}

function handleLine(line: string): void {
  if (!line.trim()) return;
  const message = parsePtyHostParentMessage(line);
  if (!message) {
    process.stderr.write(`[pty-host] dropped malformed frame (${line.length} bytes)\n`);
    return;
  }
  if (message.t === "req") {
    void handleRequest(message);
    return;
  }
  const session = sessions.get(message.id);
  if (!session) return;
  if (message.t === "data") session.write(message.d);
  else if (message.t === "pause") session.pause();
  else session.resume();
}

const reader = createInterface({ input: process.stdin, crlfDelay: Infinity });
reader.on("line", handleLine);
reader.on("close", () => {
  // 父进程消失：结束所有 shell，然后退出，避免孤儿进程占着伪终端。
  for (const session of sessions.values()) session.kill();
  setTimeout(() => process.exit(0), 1000).unref();
});

send({ t: "ready" });
