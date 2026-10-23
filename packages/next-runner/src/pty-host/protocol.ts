// pty-host 帧协议（NDJSON，一行一个 JSON 对象，走子进程 stdio）。
//
// 方向约定：
// - 父 → 子：请求 `{t:"req", id, m, ...}`（id 由父进程单调递增，用于请求/响应关联）、
//   发送输入 `{t:"data", id, d}`、流控 `{t:"pause"|"resume", id}`。
// - 子 → 父：响应 `{t:"res", id, ok:true, r?}` / `{t:"res", id, ok:false, e}`、
//   终端输出 `{t:"data", id, d}`（已按 ≈16 ms / ≤64 KiB 合并）、
//   退出 `{t:"exit", id, code, signal?}`、子进程就绪 `{t:"ready"}`。
//
// 运行时在两个进程里都校验：这些帧来自子进程 stdout，不能当作可信对象直接使用。

export type PtyHostRequestMethod = "create" | "write" | "resize" | "kill" | "list" | "serialize";

export interface PtyHostCreateOptions {
  /** 由调用方提供的终端 id（重连时复用）；缺省由 pty-host 生成。 */
  id?: string;
  cols: number;
  rows: number;
  cwd?: string;
  shell?: string;
  env?: Record<string, string>;
}

export interface PtyHostCreateResult {
  id: string;
  shell: string;
  cwd: string;
  pid: number;
}

export interface PtyHostTerminalInfo {
  id: string;
  cols: number;
  rows: number;
  shell: string;
  cwd: string;
  pid: number;
}

export type PtyHostChildMessage =
  | { t: "ready" }
  | { t: "res"; id: number; ok: true; r?: unknown }
  | { t: "res"; id: number; ok: false; e: string }
  | { t: "data"; id: string; d: string }
  | { t: "exit"; id: string; code: number; signal?: number };

export type PtyHostParentMessage =
  | ({ t: "req"; id: number } & (
      | { m: "create"; opts: PtyHostCreateOptions }
      | { m: "write"; id_: string; data: string }
      | { m: "resize"; id_: string; cols: number; rows: number }
      | { m: "kill"; id_: string; signal?: string }
      | { m: "list" }
      | { m: "serialize"; id_: string }
    ))
  | { t: "data"; id: string; d: string }
  | { t: "pause"; id: string }
  | { t: "resume"; id: string };

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isStringRecord(value: unknown): value is Record<string, string> {
  return isRecord(value) && Object.values(value).every((entry) => typeof entry === "string");
}

export function parsePtyHostChildMessage(line: string): PtyHostChildMessage | null {
  let raw: unknown;
  try {
    raw = JSON.parse(line);
  } catch {
    return null;
  }
  if (!isRecord(raw)) return null;

  switch (raw.t) {
    case "ready":
      return { t: "ready" };
    case "res": {
      if (typeof raw.id !== "number") return null;
      if (raw.ok === true) return { t: "res", id: raw.id, ok: true, r: raw.r };
      if (raw.ok === false) return { t: "res", id: raw.id, ok: false, e: String(raw.e) };
      return null;
    }
    case "data": {
      if (typeof raw.id !== "string" || typeof raw.d !== "string") return null;
      return { t: "data", id: raw.id, d: raw.d };
    }
    case "exit": {
      if (typeof raw.id !== "string" || typeof raw.code !== "number") return null;
      return {
        t: "exit",
        id: raw.id,
        code: raw.code,
        signal: typeof raw.signal === "number" ? raw.signal : undefined,
      };
    }
    default:
      return null;
  }
}

export function parsePtyHostParentMessage(line: string): PtyHostParentMessage | null {
  let raw: unknown;
  try {
    raw = JSON.parse(line);
  } catch {
    return null;
  }
  if (!isRecord(raw)) return null;

  if (raw.t === "data" || raw.t === "pause" || raw.t === "resume") {
    if (typeof raw.id !== "string") return null;
    if (raw.t === "data") {
      return typeof raw.d === "string" ? { t: "data", id: raw.id, d: raw.d } : null;
    }
    return { t: raw.t, id: raw.id };
  }
  if (raw.t !== "req" || typeof raw.id !== "number") return null;

  switch (raw.m) {
    case "create": {
      const opts = raw.opts;
      if (!isRecord(opts)) return null;
      if (typeof opts.cols !== "number" || typeof opts.rows !== "number") return null;
      if (opts.id !== undefined && typeof opts.id !== "string") return null;
      if (opts.cwd !== undefined && typeof opts.cwd !== "string") return null;
      if (opts.shell !== undefined && typeof opts.shell !== "string") return null;
      if (opts.env !== undefined && !isStringRecord(opts.env)) return null;
      return {
        t: "req",
        id: raw.id,
        m: "create",
        opts: {
          id: opts.id as string | undefined,
          cols: opts.cols,
          rows: opts.rows,
          cwd: opts.cwd as string | undefined,
          shell: opts.shell as string | undefined,
          env: opts.env as Record<string, string> | undefined,
        },
      };
    }
    case "write":
      if (typeof raw.id_ !== "string" || typeof raw.data !== "string") return null;
      return { t: "req", id: raw.id, m: "write", id_: raw.id_, data: raw.data };
    case "resize":
      if (typeof raw.id_ !== "string") return null;
      if (typeof raw.cols !== "number" || typeof raw.rows !== "number") return null;
      return { t: "req", id: raw.id, m: "resize", id_: raw.id_, cols: raw.cols, rows: raw.rows };
    case "kill":
      if (typeof raw.id_ !== "string") return null;
      if (raw.signal !== undefined && typeof raw.signal !== "string") return null;
      return {
        t: "req",
        id: raw.id,
        m: "kill",
        id_: raw.id_,
        signal: raw.signal as string | undefined,
      };
    case "list":
      return { t: "req", id: raw.id, m: "list" };
    case "serialize":
      if (typeof raw.id_ !== "string") return null;
      return { t: "req", id: raw.id, m: "serialize", id_: raw.id_ };
    default:
      return null;
  }
}
