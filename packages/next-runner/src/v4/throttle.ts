import type { Logger } from "../logger.js";

export interface ThrottleOptions {
  /** Max flushes per second; the trailing edge always flushes the latest state. */
  maxPerSecond: number;
  flush: () => void;
}

/**
 * Leading + trailing throttle for snapshot publication. Streaming deltas arrive far
 * faster than the UI needs; the final state must still be sent, so a pending flush is
 * always scheduled after the window closes.
 */
export class SnapshotThrottle {
  private timer: NodeJS.Timeout | null = null;
  private pending = false;
  private lastFlushAt = 0;
  private disposed = false;
  private readonly intervalMs: number;

  constructor(private readonly options: ThrottleOptions) {
    this.intervalMs = Math.max(1, Math.floor(1000 / options.maxPerSecond));
  }

  request(): void {
    if (this.disposed) return;
    const now = Date.now();
    const elapsed = now - this.lastFlushAt;
    if (elapsed >= this.intervalMs && this.timer === null) {
      this.flushNow();
      return;
    }
    this.pending = true;
    if (this.timer !== null) return;
    const delay = Math.max(0, this.intervalMs - elapsed);
    this.timer = setTimeout(() => {
      this.timer = null;
      if (this.disposed) return;
      if (this.pending) this.flushNow();
    }, delay);
    this.timer.unref?.();
  }

  /** Flushes any pending trailing state immediately (used on unsubscribe). */
  flushPending(): void {
    if (this.disposed) return;
    if (this.timer !== null) {
      clearTimeout(this.timer);
      this.timer = null;
    }
    if (this.pending) this.flushNow();
  }

  dispose(): void {
    this.flushPending();
    this.disposed = true;
    if (this.timer !== null) {
      clearTimeout(this.timer);
      this.timer = null;
    }
  }

  private flushNow(): void {
    this.pending = false;
    this.lastFlushAt = Date.now();
    try {
      this.options.flush();
    } catch (error) {
      // Never let a subscriber-side failure break the publisher loop.
      void error;
    }
  }
}

export interface ListenerRegistry<T> {
  add(listener: T): () => void;
  emit(value: T): void;
  size(): number;
}

export function createListenerRegistry<T>(): ListenerRegistry<T> {
  const listeners = new Set<T>();
  return {
    add(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    emit(value) {
      for (const listener of listeners) {
        try {
          (listener as unknown as (value: T) => void)(value);
        } catch (error) {
          void error;
        }
      }
    },
    size() {
      return listeners.size;
    },
  };
}

/** Deferred callback ordering: initial frame must follow the subscribe ACK. */
export function afterAck(task: () => void, logger?: Logger): void {
  queueMicrotask(() => {
    try {
      task();
    } catch (error) {
      logger?.warn("post-ack task failed", {
        message: error instanceof Error ? error.message : String(error),
      });
    }
  });
}
