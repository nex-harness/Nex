import {
  BROWSER_BINARY_SCREENCAST,
  decodePTYBinaryFrame,
  encodePTYBinaryFrame,
  Envelope,
  PTY_BINARY_STDIN,
  PTY_BINARY_STDOUT,
  resolveWsUrl,
} from './protocol';

type FrameListener = (env: Envelope) => void;
type BinaryDataListener = (data: Uint8Array) => void;

class NexWSClient {
  private ws: WebSocket | null = null;
  private rpcPending = new Map<string, { resolve: (env: Envelope) => void; reject: (err: Error) => void }>();
  private frameListeners = new Set<FrameListener>();
  private ptyListeners = new Map<string, Set<BinaryDataListener>>();
  private browserListeners = new Map<string, Set<BinaryDataListener>>();
  private statusListeners = new Set<(connected: boolean) => void>();
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private shouldReconnect = false;
  public connected = false;

  connect(): void {
    if (typeof WebSocket === 'undefined') return;
    this.shouldReconnect = true;
    if (this.ws && (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING)) {
      return;
    }

    const ws = new WebSocket(resolveWsUrl());
    ws.binaryType = 'arraybuffer';
    this.ws = ws;

    ws.onopen = () => {
      this.connected = true;
      this.statusListeners.forEach((fn) => fn(true));
    };

    ws.onclose = () => {
      this.connected = false;
      this.statusListeners.forEach((fn) => fn(false));
      for (const [, p] of this.rpcPending) {
        p.reject(new Error('WebSocket disconnected'));
      }
      this.rpcPending.clear();
      if (this.shouldReconnect) {
        this.reconnectTimer = setTimeout(() => this.connect(), 1000);
      }
    };

    ws.onmessage = (ev) => {
      if (ev.data instanceof ArrayBuffer || ev.data instanceof Uint8Array) {
        const parsed = decodePTYBinaryFrame(ev.data);
        if (!parsed) return;
        if (parsed.op === PTY_BINARY_STDOUT) {
          const subs = this.ptyListeners.get(parsed.ptyId);
          subs?.forEach((cb) => cb(parsed.data));
        } else if (parsed.op === BROWSER_BINARY_SCREENCAST) {
          const subs = this.browserListeners.get(parsed.ptyId);
          subs?.forEach((cb) => cb(parsed.data));
        }
        return;
      }

      if (typeof ev.data === 'string') {
        try {
          const env = JSON.parse(ev.data) as Envelope;
          if (env.reply_to && this.rpcPending.has(env.reply_to)) {
            const waiter = this.rpcPending.get(env.reply_to)!;
            this.rpcPending.delete(env.reply_to);
            if (env.type === 'error') {
              const errMsg = (env.payload as { error?: string })?.error || 'RPC failed';
              waiter.reject(new Error(errMsg));
            } else {
              waiter.resolve(env);
            }
            return;
          }
          this.frameListeners.forEach((fn) => fn(env));
        } catch {
          // ignore malformed frames
        }
      }
    };
  }

  disconnect(): void {
    this.shouldReconnect = false;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.ws?.close();
    this.ws = null;
  }

  reconnectNow(): void {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    const prev = this.ws;
    this.ws = null;
    this.connected = false;
    this.shouldReconnect = false;
    try {
      prev?.close();
    } catch {
      // ignore close errors
    }
    this.connect();
  }

  onStatus(fn: (connected: boolean) => void): () => void {
    this.statusListeners.add(fn);
    return () => this.statusListeners.delete(fn);
  }

  onFrame(fn: FrameListener): () => void {
    this.frameListeners.add(fn);
    return () => this.frameListeners.delete(fn);
  }

  onPTYOutput(ptyId: string, fn: BinaryDataListener): () => void {
    let set = this.ptyListeners.get(ptyId);
    if (!set) {
      set = new Set();
      this.ptyListeners.set(ptyId, set);
    }
    set.add(fn);
    return () => {
      set?.delete(fn);
    };
  }

  onBrowserFrame(tabId: string, fn: BinaryDataListener): () => void {
    let set = this.browserListeners.get(tabId);
    if (!set) {
      set = new Set();
      this.browserListeners.set(tabId, set);
    }
    set.add(fn);
    return () => {
      set?.delete(fn);
    };
  }

  sendEnvelope<T>(type: string, payload: T, extra?: Omit<Partial<Envelope<T>>, 'payload'>): string {
    const id = `cli_${crypto.randomUUID()}`;
    const env: Envelope<T> = {
      v: 1,
      id,
      type,
      ts: Date.now(),
      ...extra,
      payload,
    };
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(env));
    }
    return id;
  }

  sendPTYStdin(ptyId: string, input: string | Uint8Array): void {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(encodePTYBinaryFrame(PTY_BINARY_STDIN, ptyId, input));
    }
  }

  requestRPC<Req, Resp = unknown>(
    type: string,
    payload: Req,
    extra?: Omit<Partial<Envelope<Req>>, 'payload'>,
    timeoutMs = 15000,
  ): Promise<Envelope<Resp>> {
    return new Promise((resolve, reject) => {
      if (this.ws?.readyState !== WebSocket.OPEN) {
        reject(new Error('WebSocket is not open'));
        return;
      }
      const id = this.sendEnvelope(type, payload, extra);
      const timer = setTimeout(() => {
        if (this.rpcPending.has(id)) {
          this.rpcPending.delete(id);
          reject(new Error(`RPC ${type} timed out`));
        }
      }, timeoutMs);

      this.rpcPending.set(id, {
        resolve: (env) => {
          clearTimeout(timer);
          resolve(env as Envelope<Resp>);
        },
        reject: (err) => {
          clearTimeout(timer);
          reject(err);
        },
      });
    });
  }
}

export const wsClient = new NexWSClient();
