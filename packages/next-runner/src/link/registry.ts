import type { JsonValue } from "./frames.js";

export type CallHandler = (arg: JsonValue) => Promise<JsonValue> | JsonValue;
export type EventEmitter = (value: JsonValue) => void;
export type ListenHandler = (arg: JsonValue, emit: EventEmitter) => { dispose?: () => void } | void;

/**
 * Registration surface used by channel modules. Kept as an interface so a decorator
 * (e.g. the workspace-served guard in channels/register.ts) can wrap it.
 */
export interface ChannelRegistration {
  call(channel: string, method: string, handler: CallHandler): void;
  listen(channel: string, event: string, handler: ListenHandler): void;
}

/** Dispatch surface used by the runner link. */
export interface ChannelDispatch {
  getCall(channel: string, method: string): CallHandler | undefined;
  getListen(channel: string, event: string): ListenHandler | undefined;
  listRegistered(): string[];
}

/** Full registry surface: handlers register calls/listens and the link dispatches them. */
export type ChannelRegistryLike = ChannelRegistration & ChannelDispatch;

/**
 * `(channel, method)` and `(channel, event)` registries. The runner is a single process,
 * so a plain map is the whole routing table; per-workspace selection happens in handlers.
 */
export class ChannelRegistry implements ChannelRegistration, ChannelDispatch {
  private readonly calls = new Map<string, CallHandler>();
  private readonly listens = new Map<string, ListenHandler>();
  private readonly debugNames = new Set<string>();

  call(channel: string, method: string, handler: CallHandler): void {
    this.calls.set(key(channel, method), handler);
    this.debugNames.add(`${channel}.${method}`);
  }

  listen(channel: string, event: string, handler: ListenHandler): void {
    this.listens.set(key(channel, event), handler);
    this.debugNames.add(`${channel}.${event}`);
  }

  getCall(channel: string, method: string): CallHandler | undefined {
    return this.calls.get(key(channel, method));
  }

  getListen(channel: string, event: string): ListenHandler | undefined {
    return this.listens.get(key(channel, event));
  }

  listRegistered(): string[] {
    return [...this.debugNames].sort();
  }
}

export function methodKey(channel: string, method: string): string {
  return `${channel}.${method}`;
}

function key(channel: string, method: string): string {
  return methodKey(channel, method);
}

export function unknownMethodMessage(channel: string, method: string): string {
  return `Unknown method ${channel}.${method}`;
}
