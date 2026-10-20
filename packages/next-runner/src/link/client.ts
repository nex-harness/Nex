import { WebSocket } from "ws";
import type { Logger } from "../logger.js";
import {
  decodeCallArg,
  isPlainObject,
  parseServerFrame,
  type ClientFrame,
  type HelloFrame,
  type JsonValue,
} from "./frames.js";
import { unknownMethodMessage, type ChannelRegistry } from "./registry.js";

export interface RunnerLinkOptions {
  url: string;
  token: string | undefined;
  hello: Omit<HelloFrame, "t">;
  registry: ChannelRegistry;
  logger: Logger;
  /** 15 s per spec. */
  pingIntervalMs?: number;
  /** 45 s per spec; measured from the last inbound frame. */
  pongTimeoutMs?: number;
  /** Max reconnect delay (spec: 10 s). */
  maxBackoffMs?: number;
}

type LinkState = "idle" | "connecting" | "open" | "closed";

interface ListenEntry {
  channel: string;
  event: string;
  dispose: (() => void) | undefined;
}

/**
 * One WebSocket to `<server>/runner`. Reconnects with exponential backoff and drops all
 * listens on disconnect; the browser resubscribes with snapshot mode, so no server-side
 * state survives here.
 */
export class RunnerLink {
  private ws: WebSocket | null = null;
  private state: LinkState = "idle";
  private attempt = 0;
  private reconnectTimer: NodeJS.Timeout | null = null;
  private pingTimer: NodeJS.Timeout | null = null;
  private lastInboundAt = 0;
  private readonly listens = new Map<string, ListenEntry>();
  private stopped = false;

  private readonly pingIntervalMs: number;
  private readonly pongTimeoutMs: number;
  private readonly maxBackoffMs: number;

  constructor(private readonly options: RunnerLinkOptions) {
    this.pingIntervalMs = options.pingIntervalMs ?? 15_000;
    this.pongTimeoutMs = options.pongTimeoutMs ?? 45_000;
    this.maxBackoffMs = options.maxBackoffMs ?? 10_000;
  }

  start(): void {
    if (this.state !== "idle") return;
    this.stopped = false;
    this.connect();
  }

  async stop(): Promise<void> {
    this.stopped = true;
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.stopPing();
    this.disposeListens();
    const ws = this.ws;
    this.ws = null;
    this.state = "closed";
    if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
      await new Promise<void>((resolve) => {
        ws.once("close", () => resolve());
        ws.close();
      });
    }
  }

  get connected(): boolean {
    return this.state === "open";
  }

  private connect(): void {
    if (this.stopped) return;
    this.state = "connecting";
    const headers: Record<string, string> = {};
    if (this.options.token) headers.Authorization = `Bearer ${this.options.token}`;
    const ws = new WebSocket(this.options.url, { headers });
    this.ws = ws;

    ws.on("open", () => {
      this.state = "open";
      this.attempt = 0;
      this.lastInboundAt = Date.now();
      this.options.logger.info("runner link connected", { url: this.options.url });
      this.send({ t: "hello", ...this.options.hello } satisfies ClientFrame);
      this.startPing();
    });

    ws.on("message", (data) => {
      this.lastInboundAt = Date.now();
      const text = typeof data === "string" ? data : data.toString("utf8");
      if (text === "pong") {
        // Some proxies answer bare pongs; the timestamp reset above already covers liveness.
        return;
      }
      this.handleFrame(text);
    });

    ws.on("error", (error) => {
      this.options.logger.warn("runner link error", { message: error.message });
    });

    ws.on("close", (code, reason) => {
      this.stopPing();
      this.disposeListens();
      if (this.ws === ws) this.ws = null;
      if (this.stopped) {
        this.state = "closed";
        return;
      }
      this.state = "idle";
      this.options.logger.warn("runner link closed", {
        code,
        reason: reason.toString("utf8"),
      });
      this.scheduleReconnect();
    });
  }

  private scheduleReconnect(): void {
    if (this.stopped || this.reconnectTimer) return;
    const delay = Math.min(1000 * 2 ** this.attempt, this.maxBackoffMs);
    this.attempt += 1;
    this.options.logger.info("runner link reconnect scheduled", { delayMs: delay });
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.connect();
    }, delay);
    this.reconnectTimer.unref?.();
  }

  private startPing(): void {
    this.stopPing();
    this.pingTimer = setInterval(() => {
      if (Date.now() - this.lastInboundAt > this.pongTimeoutMs) {
        this.options.logger.warn("runner link pong timeout, reconnecting");
        this.ws?.terminate();
        return;
      }
      this.send({ t: "ping" });
    }, this.pingIntervalMs);
    this.pingTimer.unref?.();
  }

  private stopPing(): void {
    if (this.pingTimer) {
      clearInterval(this.pingTimer);
      this.pingTimer = null;
    }
  }

  private handleFrame(text: string): void {
    const frame = parseServerFrame(text);
    if (!frame) {
      this.options.logger.debug("runner link ignored unparsable frame", {
        bytes: text.length,
      });
      return;
    }
    switch (frame.t) {
      case "welcome":
        this.options.logger.info("runner link welcomed", {
          serverVersion: frame.serverVersion ?? "unknown",
        });
        return;
      case "ping":
        this.send({ t: "pong" });
        return;
      case "call":
        void this.handleCall(frame.id, frame.channel, frame.method, decodeCallArg(frame.arg));
        return;
      case "listen":
        this.handleListen(frame.id, frame.channel, frame.event, decodeCallArg(frame.arg));
        return;
      case "unlisten":
        this.disposeListen(frame.id);
        return;
    }
  }

  private async handleCall(
    id: string,
    channel: string,
    method: string,
    arg: JsonValue,
  ): Promise<void> {
    const handler = this.options.registry.getCall(channel, method);
    if (!handler) {
      this.send({
        t: "result",
        id,
        ok: false,
        error: { message: unknownMethodMessage(channel, method), name: "UnknownMethod" },
      });
      return;
    }
    try {
      const value = await handler(arg);
      const result: ClientFrame =
        value === undefined ? { t: "result", id, ok: true } : { t: "result", id, ok: true, value };
      this.send(result);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      const name = error instanceof Error && error.name ? error.name : "Error";
      this.options.logger.debug("runner link call failed", { channel, method, message });
      this.send({ t: "result", id, ok: false, error: { message, name } });
    }
  }

  private handleListen(id: string, channel: string, event: string, arg: JsonValue): void {
    this.disposeListen(id);
    const handler = this.options.registry.getListen(channel, event);
    // Unknown events are accepted but never emit (spec: "Unknown event → accept and never emit").
    if (!handler) {
      this.listens.set(id, { channel, event, dispose: undefined });
      return;
    }
    const emit = (value: JsonValue) => {
      if (!this.listens.has(id)) return;
      this.send({ t: "event", id, value });
    };
    try {
      const subscription = handler(arg, emit);
      this.listens.set(id, { channel, event, dispose: subscription?.dispose });
    } catch (error) {
      this.options.logger.warn("runner link listen handler failed", {
        channel,
        event,
        message: error instanceof Error ? error.message : String(error),
      });
      this.listens.set(id, { channel, event, dispose: undefined });
    }
  }

  private disposeListen(id: string): void {
    const entry = this.listens.get(id);
    if (!entry) return;
    this.listens.delete(id);
    try {
      entry.dispose?.();
    } catch (error) {
      this.options.logger.warn("runner link listen dispose failed", {
        channel: entry.channel,
        event: entry.event,
        message: error instanceof Error ? error.message : String(error),
      });
    }
  }

  private disposeListens(): void {
    for (const id of this.listens.keys()) this.disposeListen(id);
  }

  private send(frame: ClientFrame): void {
    const ws = this.ws;
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(JSON.stringify(frame));
  }
}

export function assertHelloShape(value: unknown): asserts value is HelloFrame {
  if (!isPlainObject(value) || value["t"] !== "hello") {
    throw new Error("invalid hello frame");
  }
}
