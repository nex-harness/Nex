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

export interface PingFrame {
  t: "ping";
}

export interface PongFrame {
  t: "pong";
}

export type ClientFrame = HelloFrame | PingFrame | PongFrame | ResultFrame | EventFrame;
export type ServerFrame = WelcomeFrame | CallFrame | ListenFrame | UnlistenFrame | PingFrame;

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
    default:
      return null;
  }
}

export function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
