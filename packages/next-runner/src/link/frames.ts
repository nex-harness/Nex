/**
 * Runner link wire protocol (see docs/next/README.md "Runner link protocol").
 *
 * Values arriving in `call`/`listen` args are JSON bridge forms produced by the server:
 * `{"$undef":true}` denotes JavaScript undefined. Buffers are not used by this runner, so
 * they are passed through untouched (they never occur in channel results we produce).
 */

export type JsonValue = unknown;

export interface HelloFrame {
  t: "hello";
  runnerId: string;
  name: string;
  version: string;
  os: string;
  arch: string;
  workspaces: Array<{ path: string; label: string }>;
  /** Last config version this runner applied (0 = none); optional for old servers. */
  lastAppliedVersion?: number;
  /**
   * Content-derived build id (docs/next/runner-update.md decision 2); `"dev"` when
   * running from source. The server compares it with the platform bundle's id.
   */
  buildId: string;
  /** Optional features of this build; a legacy runner omits both fields. */
  capabilities: string[];
}

/** Capabilities shipped by this batch (docs/next/runner-update.md decision 2). */
export const RUNNER_CAPABILITIES: readonly string[] = [
  "runner-workspaces",
  "machine-scope",
  "search",
  "self-update",
];

/** One `{t:"update", buildId}` trigger from the server (decision 5). */
export interface UpdateFrame {
  t: "update";
  buildId: string;
}

/** Self-update progress reported back to the server (decision 5). */
export interface UpdateStatusFrame {
  t: "updateStatus";
  state: "downloading" | "restarting" | "failed";
  buildId: string;
  error?: string;
}

export interface WelcomeFrame {
  t: "welcome";
  serverVersion?: string;
}

export interface CallFrame {
  t: "call";
  id: string;
  channel: string;
  method: string;
  arg?: JsonValue;
}

export interface ResultFrame {
  t: "result";
  id: string;
  ok: boolean;
  value?: JsonValue;
  error?: { message: string; name?: string };
}

export interface ListenFrame {
  t: "listen";
  id: string;
  channel: string;
  event: string;
  arg?: JsonValue;
}

export interface UnlistenFrame {
  t: "unlisten";
  id: string;
}

export interface EventFrame {
  t: "event";
  id: string;
  value: JsonValue;
}

/**
 * Server-owned config push (docs/next/config-mcp-skills.md decision 4). The
 * payload is a full merged snapshot per runner, never a delta.
 */
export interface ConfigFrame {
  t: "config";
  version: number;
  doc: unknown;
  secrets?: Record<string, string>;
  bundles?: unknown[];
}

/** Runner acknowledgement of one applied config version. */
export interface ConfigAppliedFrame {
  t: "configApplied";
  version: number;
  ok: boolean;
  errors: Array<{ scope?: string; path?: string; code: string; message: string }>;
  facts: Record<string, unknown>;
}

/**
 * The runner's served folder list changed (docs/next/runner-folders.md decision 3).
 * A full list, never a delta; the server replaces its catalogue for this runner.
 * Old servers ignore unknown frames, so a new runner keeps its startup folders.
 */
export interface WorkspacesFrame {
  t: "workspaces";
  workspaces: Array<{ path: string; label: string }>;
}

export interface PingFrame {
  t: "ping";
}

export interface PongFrame {
  t: "pong";
}

export type ClientFrame =
  | HelloFrame
  | PingFrame
  | PongFrame
  | ResultFrame
  | EventFrame
  | ConfigAppliedFrame
  | WorkspacesFrame
  | UpdateStatusFrame;
export type ServerFrame =
  | WelcomeFrame
  | CallFrame
  | ListenFrame
  | UnlistenFrame
  | PingFrame
  | ConfigFrame
  | UpdateFrame;

const UNDEF_KEY = "$undef";

/** Decode the bridge's undefined sentinel at the top level of a call/listen arg. */
export function decodeArg(value: JsonValue): JsonValue {
  if (isPlainObject(value) && Object.keys(value).length === 1 && value[UNDEF_KEY] === true) {
    return undefined;
  }
  return value;
}

/**
 * Channel RPC carries positional arguments. The browser sends one object per call, so
 * the server forwards `[arg]` verbatim: unwrap a single-element array and decode the
 * undefined sentinel inside it. Zero/multi-element arrays are passed through untouched;
 * no method in this runner takes more than one argument.
 */
export function decodeCallArg(value: JsonValue): JsonValue {
  if (Array.isArray(value)) {
    const decoded = value.map((item) => decodeArg(item));
    return decoded.length === 1 ? decoded[0] : decoded;
  }
  return decodeArg(value);
}

/** Encode undefined results by omitting `value`; everything else passes through. */
export function isUndefinedResult(value: JsonValue): boolean {
  return value === undefined;
}

export function parseServerFrame(raw: string): ServerFrame | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  if (!isPlainObject(parsed)) return null;
  const t = parsed["t"];
  if (typeof t !== "string") return null;
  switch (t) {
    case "welcome":
    case "call":
    case "listen":
    case "unlisten":
    case "ping":
      return parsed as unknown as ServerFrame;
    case "config":
      if (typeof parsed["version"] !== "number") return null;
      return parsed as unknown as ServerFrame;
    case "update":
      // 决策 5：更新触发帧只携带目标 buildId；缺字段视为非法帧，避免把空 id
      // 当成一次真实更新（会把 runner 重启到未知状态）。
      if (typeof parsed["buildId"] !== "string" || parsed["buildId"].length === 0) return null;
      return parsed as unknown as ServerFrame;
    default:
      return null;
  }
}

export function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
