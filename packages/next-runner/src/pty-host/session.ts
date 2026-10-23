// pty-host 内的单个终端：node-pty 会话 + @xterm/headless 镜像 + 输出合并。
//
// 设计要点（spec 决策 4/5/6，research §5.1）：
// - 每个终端一个 headless xterm + SerializeAddon；`serialize()` 返回当前屏幕，
//   供重连时先回放再接收实时输出。
// - 输出按 ≈16 ms 窗口 / ≤64 KiB 合并，避免逐字节消息把 runner 链接打满。
// - 流控：父进程暂停消费时 `pty.pause()`；xterm 解析是异步的（write 回调表示
//   已解析），在解析积压超过水位时同样暂停 pty。恢复时先 resume pty，再允许
//   下一轮 flush，保证数据不丢。
// - `kill()` 默认先 SIGHUP（与桌面终端关闭行为一致，shell 会退出），等待
//   onExit；forceKill() 才直接杀进程。这样 `exit` 事件总是由 pty 自身发出。

import type { IPty } from "node-pty";
import headlessModule from "@xterm/headless";
import serializeModule from "@xterm/addon-serialize";

// 两个包都是 UMD/CJS 产物（无 ESM 命名导出），Node 的 ESM 互操作只会给出
// default，所以从 default 上取类；类型仍来自各自的 .d.ts。
type HeadlessTerminal = import("@xterm/headless").Terminal;
const HeadlessTerminal = (
  headlessModule as unknown as {
    Terminal: new (options: {
      cols: number;
      rows: number;
      scrollback: number;
      allowProposedApi: boolean;
    }) => HeadlessTerminal;
  }
).Terminal;
const SerializeAddon = (
  serializeModule as unknown as {
    SerializeAddon: new () => import("@xterm/addon-serialize").SerializeAddon;
  }
).SerializeAddon;

export const COALESCE_INTERVAL_MS = 16;
export const COALESCE_MAX_BYTES = 64 * 1024;
/** 已写入 xterm 但尚未解析完成的字节超过该值即暂停 pty（8 个合并窗口）。 */
export const PARSER_HIGH_WATERMARK_BYTES = 512 * 1024;
/** 屏幕回滚行数：serialize 会带上完整滚动缓冲区，重连后能看到之前的输出。 */
export const MIRROR_SCROLLBACK = 3000;

export interface PtySessionHooks {
  /** 合并后的输出（UTF-8 字符串）。 */
  emitData(data: string): void;
  emitExit(code: number, signal?: number): void;
}

export interface PtySessionSpawn {
  shell: string;
  args: string[];
  cols: number;
  rows: number;
  cwd: string;
  env: NodeJS.ProcessEnv;
  spawn(options: { cols: number; rows: number; cwd: string; env: NodeJS.ProcessEnv }): IPty;
}

function utf8Length(value: string): number {
  return Buffer.byteLength(value, "utf8");
}

export class PtySession {
  readonly id: string;
  readonly shell: string;
  readonly cwd: string;
  private readonly pty: IPty;
  private readonly mirror: HeadlessTerminal;
  private readonly serializer: import("@xterm/addon-serialize").SerializeAddon;
  private readonly hooks: PtySessionHooks;

  private pending: string[] = [];
  private pendingBytes = 0;
  private flushTimer: NodeJS.Timeout | null = null;
  private parserPendingBytes = 0;
  /** 已发出但 xterm 尚未解析的尾部（按写入顺序），用于 serialize 前后的续接。 */
  private unparsed: string[] = [];
  private pausedByConsumer = false;
  private pausedByParser = false;
  private exited = false;

  constructor(id: string, spawn: PtySessionSpawn, hooks: PtySessionHooks) {
    this.id = id;
    this.shell = spawn.shell;
    this.cwd = spawn.cwd;
    this.hooks = hooks;
    // SerializeAddon.serialize 走的 buffer API 被 xterm 标记为 proposed：
    // headless 场景（无渲染、无用户输入）必须显式开启。
    this.mirror = new HeadlessTerminal({
      cols: spawn.cols,
      rows: spawn.rows,
      scrollback: MIRROR_SCROLLBACK,
      allowProposedApi: true,
    });
    this.serializer = new SerializeAddon();
    // @xterm/addon-serialize 的 typings 绑定浏览器 Terminal；运行时 API 与
    // headless 版本一致（loadAddon/write/resize/serialize），按 ITerminalAddon 收窄。
    this.mirror.loadAddon(
      this.serializer as unknown as Parameters<HeadlessTerminal["loadAddon"]>[0],
    );

    this.pty = spawn.spawn({ cols: spawn.cols, rows: spawn.rows, cwd: spawn.cwd, env: spawn.env });
    this.pty.onData((data) => this.handleData(data));
    this.pty.onExit(({ exitCode, signal }) => this.handleExit(exitCode, signal));
  }

  get pid(): number {
    return this.pty.pid;
  }

  get cols(): number {
    return this.pty.cols;
  }

  get rows(): number {
    return this.pty.rows;
  }

  write(data: string): void {
    this.pty.write(data);
  }

  resize(cols: number, rows: number): void {
    this.pty.resize(cols, rows);
    this.mirror.resize(cols, rows);
  }

  /**
   * 返回当前屏幕（含滚动缓冲区），重连回放直接写进新客户端。
   *
   * xterm 的解析是异步的：serialize 只反映"已解析完"的内容，尾部可能还在队列里。
   * 这里等已入队的解析完成；若此时仍有未提交的原始输出（罕见竞态），顺序补抄
   * 到镜像末尾后再序列化，保证回放覆盖所有已发出的字节。期间到达的新输出落入
   * `pending`，由下一轮合并照常发送，不会与回放重叠。
   */
  async serialize(): Promise<string> {
    this.flushPending();
    await new Promise<void>((resolve) => this.mirror.write("", () => resolve()));
    while (this.unparsed.length > 0) {
      const carry = this.unparsed.join("");
      this.unparsed = [];
      this.parserPendingBytes += utf8Length(carry);
      await new Promise<void>((resolve) => {
        this.mirror.write(carry, () => {
          this.parserPendingBytes = Math.max(0, this.parserPendingBytes - utf8Length(carry));
          this.applyPtyFlow();
          resolve();
        });
      });
    }
    // scrollback 给足：重连回放要看到历史输出，不能只回放可见窗口。
    return this.serializer.serialize({ scrollback: MIRROR_SCROLLBACK });
  }

  /** 重新开始读取 pty（node-pty 的 Unix 实现初始为暂停读取）。 */
  start(): void {
    this.applyPtyFlow();
  }

  pause(): void {
    this.pausedByConsumer = true;
    this.applyPtyFlow();
  }

  resume(): void {
    this.pausedByConsumer = false;
    this.applyPtyFlow();
  }

  /** 关闭：先 SIGHUP，pty 的 onExit 会触发 emitExit。 */
  kill(signal?: string): void {
    if (this.exited) return;
    this.pty.kill(signal as NodeJS.Signals | undefined);
  }

  /** 超时兜底：直接 SIGKILL，退出事件仍由 onExit 发出。 */
  forceKill(): void {
    if (this.exited) return;
    this.pty.kill("SIGKILL");
  }

  private handleData(data: string): void {
    if (this.exited || data.length === 0) return;
    this.pending.push(data);
    this.pendingBytes += utf8Length(data);
    if (this.pendingBytes >= COALESCE_MAX_BYTES) {
      this.flushPending();
      return;
    }
    this.scheduleFlush();
  }

  private scheduleFlush(): void {
    if (this.flushTimer) return;
    this.flushTimer = setTimeout(() => {
      this.flushTimer = null;
      this.flushPending();
    }, COALESCE_INTERVAL_MS);
    this.flushTimer.unref?.();
  }

  private flushPending(): void {
    if (this.flushTimer) {
      clearTimeout(this.flushTimer);
      this.flushTimer = null;
    }
    if (this.pending.length === 0) return;

    const chunk = this.pending.join("");
    this.pending = [];
    this.pendingBytes = 0;
    if (utf8Length(chunk) > COALESCE_MAX_BYTES) {
      // 单次读块超过上限（大段 cat / 粘贴回显）：按字符边界切成 ≤64 KiB 的帧，
      // 不丢字节，也不让相邻两条消息在传输层被粘成超帧。
      for (const piece of this.splitByBytes(chunk)) this.emitChunk(piece);
      this.applyPtyFlow();
      return;
    }
    this.emitChunk(chunk);
    this.applyPtyFlow();
  }

  private splitByBytes(chunk: string): string[] {
    const pieces: string[] = [];
    let current = "";
    let currentBytes = 0;
    for (const char of chunk) {
      // for..of 按码点迭代，代理对不会被切开。
      const charBytes = utf8Length(char);
      if (currentBytes + charBytes > COALESCE_MAX_BYTES && current) {
        pieces.push(current);
        current = "";
        currentBytes = 0;
      }
      current += char;
      currentBytes += charBytes;
    }
    if (current) pieces.push(current);
    return pieces;
  }

  private emitChunk(chunk: string): void {
    // 先算字节数再交给 xterm 解析（回调异步执行，chunk 可能已被释放引用）。
    const chunkBytes = utf8Length(chunk);
    this.parserPendingBytes += chunkBytes;
    const index = this.unparsed.push(chunk) - 1;
    this.mirror.write(chunk, () => {
      this.parserPendingBytes = Math.max(0, this.parserPendingBytes - chunkBytes);
      this.unparsed[index] = "";
      this.applyPtyFlow();
    });
    this.hooks.emitData(chunk);
  }

  private applyPtyFlow(): void {
    if (this.exited) return;
    this.pausedByParser = this.parserPendingBytes > PARSER_HIGH_WATERMARK_BYTES;
    // 两个暂停原因任一成立就保持暂停；都解除后无条件 resume（对未暂停的 pty
    // resume 是空操作）。不能假设 pty 初始状态：node-pty 在 Unix 上默认就是
    // 暂停读取的，首次 data 到达后才由父进程或解析器触发 resume。
    if (this.pausedByConsumer || this.pausedByParser) {
      this.pty.pause();
      return;
    }
    this.pty.resume();
  }

  private handleExit(code: number, signal?: number): void {
    this.exited = true;
    // 退出前把剩余输出发出去，保证 `exit` 之前的最后一行不丢。
    this.flushPending();
    this.hooks.emitExit(code, signal);
  }
}
