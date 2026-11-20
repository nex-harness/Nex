export const PTY_BINARY_STDIN = 0x01;
export const PTY_BINARY_STDOUT = 0x02;
export const PTY_ID_HEADER_LEN = 36;

export interface Envelope<T = unknown> {
  v: number;
  id: string;
  reply_to?: string;
  type: string;
  ts: number;
  session_id?: string;
  runner_id?: string;
  seq?: number;
  payload?: T;
}

const textEncoder = new TextEncoder();
const textDecoder = new TextDecoder();

/**
 * Packs a zero-base64 binary WebSocket frame matching Go `protocol.EncodePTYBinaryFrame`:
 * [1 byte OpCode][36 bytes ASCII/UUID padded PTY ID][Raw UTF-8/ANSI Bytes]
 */
export function encodePTYBinaryFrame(op: number, ptyId: string, data: Uint8Array | string): Uint8Array {
  const payload = typeof data === 'string' ? textEncoder.encode(data) : data;
  const idBytes = textEncoder.encode(ptyId);
  const frame = new Uint8Array(1 + PTY_ID_HEADER_LEN + payload.length);
  frame[0] = op;
  frame.set(idBytes.subarray(0, PTY_ID_HEADER_LEN), 1);
  frame.set(payload, 1 + PTY_ID_HEADER_LEN);
  return frame;
}

/**
 * Unpacks a binary PTY WebSocket frame into { op, ptyId, data }.
 */
export function decodePTYBinaryFrame(raw: ArrayBuffer | Uint8Array): {
  op: number;
  ptyId: string;
  data: Uint8Array;
} | null {
  const bytes = raw instanceof Uint8Array ? raw : new Uint8Array(raw);
  if (bytes.length < 1 + PTY_ID_HEADER_LEN) {
    return null;
  }
  const op = bytes[0];
  let end = 1 + PTY_ID_HEADER_LEN;
  while (end > 1 && bytes[end - 1] === 0) {
    end--;
  }
  const ptyId = textDecoder.decode(bytes.subarray(1, end));
  const data = bytes.subarray(1 + PTY_ID_HEADER_LEN);
  return { op, ptyId, data };
}

export function formatCompactTokens(n: number): string {
  if (n >= 1_000_000) {
    const m = n / 1_000_000;
    return `${m % 1 === 0 ? m.toFixed(0) : m.toFixed(1)}M`;
  }
  if (n >= 1_000) {
    return `${(n / 1_000).toFixed(1)}K`;
  }
  return String(n);
}

export function safeStorageGet(key: string): string | null {
  try {
    if (typeof localStorage !== 'undefined' && typeof localStorage.getItem === 'function') {
      return localStorage.getItem(key);
    }
  } catch {
    // ignore storage access errors
  }
  return null;
}

export function safeStorageSet(key: string, value: string): void {
  try {
    if (typeof localStorage !== 'undefined' && typeof localStorage.setItem === 'function') {
      localStorage.setItem(key, value);
    }
  } catch {
    // ignore storage quota/access errors
  }
}

export function resolveApiBase(): string {
  if (typeof window === 'undefined') return 'http://127.0.0.1:8080';
  const override = safeStorageGet('nex.server.url');
  if (override) return override.replace(/\/+$/, '');
  return window.location.origin;
}

export function resolveWsUrl(): string {
  const base = resolveApiBase();
  if (base.startsWith('https://')) {
    return 'wss://' + base.slice('https://'.length) + '/ws/client';
  }
  if (base.startsWith('http://')) {
    return 'ws://' + base.slice('http://'.length) + '/ws/client';
  }
  return `ws://127.0.0.1:8080/ws/client`;
}

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${resolveApiBase()}${path}`, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(init?.headers || {}),
    },
  });
  if (!res.ok) {
    const errText = await res.text();
    throw new Error(`API ${res.status}: ${errText}`);
  }
  return (await res.json()) as T;
}

export async function apiFetchText(path: string): Promise<string> {
  const res = await fetch(`${resolveApiBase()}${path}`);
  if (!res.ok) {
    throw new Error(`Blob fetch ${res.status}`);
  }
  return await res.text();
}
