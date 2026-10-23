// PtyHostClient：runner 侧的 pty-host 管理器（父进程一侧）。
//
// - 用 process.execPath 启动 pty-host 子进程（bundle 形态见 ./launch.ts）。
// - 请求/响应按自增 id 关联；子进程崩溃时所有在途请求失败，并按 VS Code 的
//   ptyHost 策略自动重启（最多 5 次，稳定运行 30 s 后清零计数）。
// - 输出按终端分发；调用方可注册 onData/onExit，或用 nextData() 拉取（测试用）。
// - pause/resume 直接透传给宿主，宿主里对应 pty.pause()/resume()。

import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { EventEmitter } from "node:events";
import { createRequire, register } from "node:module";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { resolvePtyHostLaunchSpec, type PtyHostLaunchSpec } from "./launch.js";
import {
  parsePtyHostChildMessage,
  type PtyHostCreateOptions,
  type PtyHostCreateResult,
  type PtyHostTerminalInfo,
} from "./protocol.js";

export const PTY_HOST_MAX_RESTARTS = 5;
export const PTY_HOST_STABLE_RESET_MS = 30_000;
const REQUEST_TIMEOUT_MS = 15_000;
const READY_TIMEOUT_MS = 30_000;

interface PendingRequest {
  resolve(value: unknown): void;
  reject(error: Error): void;
  timer: NodeJS.Timeout;
}

export interface PtyHostClientOptions {
  /** 覆盖自动发现的入口（测试/调试）。 */
  launch?: PtyHostLaunchSpec | null;
  /** 额外环境变量，透传给 pty-host。 */
  env?: NodeJS.ProcessEnv;
  /** 默认 cwd（仅测试用；真正创建终端时按 create 参数）。 */
  cwd?: string;
}

export interface PtyHostDataEvent {
  id: string;
  data: string;
}

export interface PtyHostExitEvent {
  id: string;
  code: number;
  signal?: number;
}

export class PtyHostClient extends EventEmitter {
  private readonly options: PtyHostClientOptions;
  private child: ChildProcessWithoutNullStreams | null = null;
  private readyPromise: Promise<void> | null = null;
  private readyResolve: (() => void) | null = null;
  private readyReject: ((error: Error) => void) | null = null;
  private nextRequestId = 1;
  private readonly pending = new Map<number, PendingRequest>();
  private readonly dataWaiters = new Map<string, Array<(data: string) => void>>();
  private restartCount = 0;
  private disposing = false;
  private stableTimer: NodeJS.Timeout | null = null;
  private exitHandler: ((event: PtyHostExitEvent) => void) | null = null;

  constructor(options: PtyHostClientOptions = {}) {
    super();
    this.options = options;
  }

  /** 启动子进程（幂等）。失败时抛出可读错误。 */
  async start(): Promise<void> {
    if (this.child && this.readyPromise) return this.readyPromise;
    const launch =
      this.options.launch === undefined ? resolvePtyHostLaunchSpec() : this.options.launch;
    if (!launch) {
      throw new Error(
        "pty-host entry not found (expected dist/pty-host.mjs or src/pty-host/ptyHost.ts)",
      );
    }

    const child = spawn(process.execPath, [...launch.args, launch.entry], {
      cwd: launch.cwd,
      env: { ...process.env, ...this.options.env },
      stdio: ["pipe", "pipe", "pipe"],
    });
    this.child = child;
    // 用可挂接的 Promise：start() 中途失败（ready 超时）后仍留下同一实例，
    // 后续 start() 能直接复用，而不会被未处理的 rejection 击穿进程。
    const ready = Promise.withResolvers<void>();
    ready.promise.catch(() => undefined);
    this.readyResolve = ready.resolve;
    this.readyReject = ready.reject;
    this.readyPromise = ready.promise;

    const stderr = createInterface({ input: child.stderr });
    stderr.on("line", (line) => {
      // 宿主的诊断日志只进 runner 的 stderr，不参与协议。
      process.stderr.write(`[pty-host] ${line}\n`);
    });

    const stdout = createInterface({ input: child.stdout, crlfDelay: Infinity });
    stdout.on("line", (line) => this.handleLine(line));

    child.on("error", (error) =>
      this.failAll(new Error(`pty-host process error: ${error.message}`)),
    );
    child.on("exit", (code, signal) => this.handleHostExit(child, code, signal));

    const readyTimer = setTimeout(() => {
      this.failReady(new Error(`pty-host did not become ready within ${READY_TIMEOUT_MS} ms`));
    }, READY_TIMEOUT_MS);
    readyTimer.unref?.();
    try {
      await this.readyPromise;
    } catch (error) {
      // 超时：杀掉半启动的子进程，交给下一次 start()（或 restart 策略）重来。
      child.kill("SIGKILL");
      throw error;
    } finally {
      clearTimeout(readyTimer);
    }

    // 稳定运行 30 s 后清零重启计数（VS Code 的 localPtyHost 同样做法）。
    this.stableTimer = setTimeout(() => {
      this.restartCount = 0;
    }, PTY_HOST_STABLE_RESET_MS);
    this.stableTimer.unref?.();
  }

  private armStableReset(): void {
    if (this.stableTimer) clearTimeout(this.stableTimer);
    this.stableTimer = setTimeout(() => {
      this.restartCount = 0;
    }, PTY_HOST_STABLE_RESET_MS);
    this.stableTimer.unref?.();
  }

  /**
   * 开发态（tsx 源码入口）从 node_modules 外的目录启动时，`--import tsx`
   * 无法按裸包名解析。这里在 tsx ESM loader 前串联一个 resolve hook，把
   * `tsx` 指向已安装的真实入口；bundle 形态不需要这一步。
   */
  private installTsxResolver(launchCwd: string): void {
    const packageName = "tsx";
    type ResolveHook = (
      specifier: string,
      context: unknown,
      nextResolve: (specifier: string, context: unknown) => Promise<unknown>,
    ) => Promise<unknown>;
    const hooks: { resolve: ResolveHook } = {
      resolve: async (specifier, context, nextResolve) => {
        try {
          return await nextResolve(specifier, context);
        } catch (error) {
          if (specifier !== packageName) throw error;
          return nextResolve(
            createRequire(join(launchCwd, "noop.js")).resolve(packageName),
            context,
          );
        }
      },
    };
    // 用 data: URL 注册内联 hooks（module.register 接受任意可导入的模块说明符）。
    const source = `export const resolve = ${hooks.resolve.toString()};`;
    register(`data:text/javascript,${encodeURIComponent(source)}`, import.meta.url);
  }

  /** 注册唯一的 exit 监听（runner 层再分发给会话）。 */
  onSessionExit(handler: (event: PtyHostExitEvent) => void): void {
    this.exitHandler = handler;
  }

  onSessionData(handler: (event: PtyHostDataEvent) => void): void {
    this.on("data", handler);
  }

  async create(options: PtyHostCreateOptions): Promise<PtyHostCreateResult> {
    return this.request<PtyHostCreateResult>({ m: "create", opts: options });
  }

  async write(id: string, data: string): Promise<void> {
    await this.request({ m: "write", id_: id, data });
  }

  async resize(id: string, cols: number, rows: number): Promise<void> {
    await this.request({ m: "resize", id_: id, cols, rows });
  }

  async kill(id: string, signal?: string): Promise<void> {
    await this.request({ m: "kill", id_: id, signal });
  }

  async list(): Promise<PtyHostTerminalInfo[]> {
    const result = await this.request<{ terminals: PtyHostTerminalInfo[] }>({ m: "list" });
    return result.terminals;
  }

  /** 当前屏幕（含滚动缓冲区），用于重连回放。 */
  async serialize(id: string): Promise<string> {
    const result = await this.request<{ screen: string }>({ m: "serialize", id_: id });
    return result.screen;
  }

  pause(id: string): void {
    this.sendFrame({ t: "pause", id });
  }

  resume(id: string): void {
    this.sendFrame({ t: "resume", id });
  }

  /** 等待下一次该终端输出（测试与轮询式调用方使用）。 */
  nextData(id: string, timeoutMs = 10_000): Promise<string> {
    return new Promise<string>((resolve, reject) => {
      const waiters = this.dataWaiters.get(id) ?? [];
      const timer = setTimeout(() => {
        const current = this.dataWaiters.get(id) ?? [];
        this.dataWaiters.set(
          id,
          current.filter((entry) => entry !== waiter),
        );
        reject(new Error(`timed out waiting for pty data on ${id}`));
      }, timeoutMs);
      const waiter = (data: string): void => {
        clearTimeout(timer);
        resolve(data);
      };
      waiters.push(waiter);
      this.dataWaiters.set(id, waiters);
    });
  }

  /** 关闭：结束宿主进程与其所有终端。 */
  async dispose(): Promise<void> {
    this.disposing = true;
    if (this.stableTimer) clearTimeout(this.stableTimer);
    const child = this.child;
    this.child = null;
    this.readyPromise = null;
    this.failAll(new Error("pty-host client disposed"));
    if (!child || child.killed) return;
    await new Promise<void>((resolve) => {
      child.once("exit", () => resolve());
      // 关 stdin 让宿主先优雅结束终端；1.5 s 内不退出则强杀。
      child.stdin.end();
      const timer = setTimeout(() => child.kill("SIGKILL"), 1500);
      timer.unref?.();
      child.once("exit", () => clearTimeout(timer));
    });
  }

  get restarts(): number {
    return this.restartCount;
  }

  private sendFrame(frame: unknown): boolean {
    const child = this.child;
    if (!child || child.killed || !child.stdin.writable) return false;
    child.stdin.write(`${JSON.stringify(frame)}\n`);
    return true;
  }

  private request<T>(payload: Record<string, unknown>): Promise<T> {
    if (!this.child) return Promise.reject(new Error("pty-host is not running"));
    const id = this.nextRequestId++;
    return new Promise<T>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`pty-host request ${String(payload.m)} timed out`));
      }, REQUEST_TIMEOUT_MS);
      timer.unref?.();
      this.pending.set(id, { resolve: resolve as (value: unknown) => void, reject, timer });
      if (!this.sendFrame({ t: "req", id, ...payload })) {
        clearTimeout(timer);
        this.pending.delete(id);
        reject(new Error("pty-host is not accepting frames"));
      }
    });
  }

  private handleLine(line: string): void {
    if (!line.trim()) return;
    const message = parsePtyHostChildMessage(line);
    if (!message) {
      process.stderr.write(
        `[pty-host] dropped malformed frame from child (${line.length} bytes)\n`,
      );
      return;
    }
    switch (message.t) {
      case "ready":
        this.readyResolve?.();
        this.readyResolve = null;
        this.readyReject = null;
        return;
      case "res": {
        const pending = this.pending.get(message.id);
        if (!pending) return;
        this.pending.delete(message.id);
        clearTimeout(pending.timer);
        if (message.ok) pending.resolve(message.r);
        else pending.reject(new Error(message.e));
        return;
      }
      case "data": {
        const waiters = this.dataWaiters.get(message.id);
        if (waiters && waiters.length > 0) {
          // 只唤醒第一个等待者，其余留给后续帧，保证每个 data 事件只被消费一次。
          const [first, ...rest] = waiters;
          if (rest.length > 0) this.dataWaiters.set(message.id, rest);
          else this.dataWaiters.delete(message.id);
          first?.(message.d);
        }
        this.emit("data", { id: message.id, data: message.d } satisfies PtyHostDataEvent);
        return;
      }
      case "exit":
        this.emit("exit", {
          id: message.id,
          code: message.code,
          signal: message.signal,
        } satisfies PtyHostExitEvent);
        this.exitHandler?.({ id: message.id, code: message.code, signal: message.signal });
        return;
    }
  }

  private failReady(error: Error): void {
    this.readyReject?.(error);
    this.readyResolve = null;
    this.readyReject = null;
  }

  private failAll(error: Error): void {
    this.failReady(error);
    for (const pending of this.pending.values()) {
      clearTimeout(pending.timer);
      pending.reject(error);
    }
    this.pending.clear();
  }

  private handleHostExit(
    child: ChildProcessWithoutNullStreams,
    code: number | null,
    signal: NodeJS.Signals | null,
  ): void {
    if (this.child === child) this.child = null;
    this.readyPromise = null;
    this.failAll(new Error(`pty-host exited (code=${String(code)}, signal=${String(signal)})`));
    if (this.disposing) return;

    if (this.restartCount >= PTY_HOST_MAX_RESTARTS) {
      process.stderr.write(
        `[pty-host] crashed ${this.restartCount + 1} times; giving up (VS Code policy: max ${PTY_HOST_MAX_RESTARTS} restarts)\n`,
      );
      this.emit("crashed", { restarts: this.restartCount });
      return;
    }
    this.restartCount += 1;
    process.stderr.write(`[pty-host] restarting (${this.restartCount}/${PTY_HOST_MAX_RESTARTS})\n`);
    this.start().catch((error: unknown) => {
      process.stderr.write(
        `[pty-host] restart failed: ${error instanceof Error ? error.message : String(error)}\n`,
      );
    });
  }
}
