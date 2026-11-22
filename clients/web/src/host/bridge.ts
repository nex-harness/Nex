import { isMyGo } from 'mygo-runtime';
import { events, type HostInfo, NexHost } from './mygo.gen';

export type HostKind = 'browser' | 'mygo-desktop' | 'mygo-mobile';
export type HostPlatform = 'web' | 'darwin' | 'linux' | 'win32' | 'ios' | 'android';

export type { HostInfo };

export interface ApprovalNotification {
  sessionId: string;
  runnerName: string;
  toolName: string;
}

export interface HostBridge {
  readonly kind: HostKind;
  readonly platform: HostPlatform;
  getHostInfo(): Promise<HostInfo | null>;
  setServerUrl(rawUrl: string): Promise<HostInfo>;
  switchConnectionMode(mode: 'local' | 'remote', serverUrl?: string): Promise<HostInfo>;
  getAuthToken(serverUrl?: string): Promise<string | null>;
  setAuthToken(token: string, serverUrl?: string): Promise<void>;
  clearAuthToken(serverUrl?: string): Promise<void>;
  notifyApprovalRequested(payload: ApprovalNotification): Promise<void>;
  haptic(style: 'selection' | 'light' | 'medium' | 'heavy' | 'success' | 'warning' | 'error'): void;
  pickDirectory(): Promise<string | null>;
  openExternal(rawUrl: string): Promise<void>;
  shareText(text: string, rawUrl?: string): Promise<void>;
  onAppResume(cb: (ts: number) => void): () => void;
  onOpenSession(cb: (sessionId: string) => void): () => void;
  onOpenSettings(cb: (section: string) => void): () => void;
  onNewTask(cb: () => void): () => void;
  onServerChanged(cb: (info: HostInfo) => void): () => void;
  getDeviceFontScale(): number;
  setDeviceFontScale(px: number): void;
}

declare global {
  interface Window {
    __MYGO_IPC__?: {
      platform?: 'desktop' | 'mobile';
      getAuthToken?: () => Promise<string>;
      setAuthToken?: (token: string) => Promise<void>;
      notify?: (title: string, body: string) => Promise<void>;
      haptic?: (style: string) => void;
    };
  }
}

const TOKEN_KEY = 'nex.auth.token';
const SERVER_KEY = 'nex.server.url';
const FONT_SCALE_KEY = 'nex.device.font_px';

function detectPlatform(): HostPlatform {
  if (typeof window === 'undefined') return 'web';
  const mygoRt = (globalThis as { mygo?: { platform?: string } }).mygo;
  if (mygoRt?.platform) {
    const p = String(mygoRt.platform).toLowerCase();
    if (p === 'darwin' || p === 'linux' || p === 'win32' || p === 'ios' || p === 'android') {
      return p;
    }
  }
  if (window.__MYGO_IPC__?.platform === 'mobile') {
    return 'ios';
  }
  if (window.__MYGO_IPC__?.platform === 'desktop') {
    return 'darwin';
  }
  return 'web';
}

export function createHostBridge(): HostBridge {
  const platform = detectPlatform();
  const hasNativeMyGo = isMyGo();
  const legacyIpc = typeof window !== 'undefined' ? window.__MYGO_IPC__ : undefined;

  const kind: HostKind = hasNativeMyGo
    ? platform === 'ios' || platform === 'android'
      ? 'mygo-mobile'
      : 'mygo-desktop'
    : legacyIpc
      ? legacyIpc.platform === 'mobile'
        ? 'mygo-mobile'
        : 'mygo-desktop'
      : 'browser';

  const tokenStorageKey = (serverUrl?: string) =>
    serverUrl ? `${TOKEN_KEY}.${serverUrl}` : TOKEN_KEY;

  return {
    kind,
    platform,

    async getHostInfo(): Promise<HostInfo | null> {
      if (hasNativeMyGo) {
        try {
          return await NexHost.info();
        } catch {
          return null;
        }
      }
      return null;
    },

    async setServerUrl(rawUrl: string): Promise<HostInfo> {
      if (hasNativeMyGo) {
        return await NexHost.setServerURL(rawUrl);
      }
      const normalized = rawUrl.trim().replace(/\/+$/, '');
      try {
        localStorage.setItem(SERVER_KEY, normalized);
      } catch {
        // ignore storage errors
      }
      return {
        platform,
        form_factor: kind === 'mygo-mobile' ? 'mobile' : 'desktop',
        version: '0.3.0',
        connection_mode: normalized ? 'remote' : 'unconfigured',
        server_url: normalized,
        supports_local_mode: false,
        recent_servers: normalized ? [normalized] : [],
      };
    },

    async switchConnectionMode(mode: 'local' | 'remote', serverUrl = ''): Promise<HostInfo> {
      if (hasNativeMyGo) {
        return await NexHost.switchMode(mode, serverUrl);
      }
      if (mode === 'remote' && serverUrl) {
        return this.setServerUrl(serverUrl);
      }
      return {
        platform,
        form_factor: kind === 'mygo-mobile' ? 'mobile' : 'desktop',
        version: '0.3.0',
        connection_mode: 'unconfigured',
        server_url: '',
        supports_local_mode: false,
        recent_servers: [],
      };
    },

    async getAuthToken(serverUrl?: string): Promise<string | null> {
      if (hasNativeMyGo) {
        try {
          return (await NexHost.getAuthToken(serverUrl || '')) || null;
        } catch {
          // fall through to storage fallback
        }
      }
      if (legacyIpc?.getAuthToken) {
        return (await legacyIpc.getAuthToken()) || null;
      }
      try {
        return localStorage.getItem(tokenStorageKey(serverUrl)) || localStorage.getItem(TOKEN_KEY);
      } catch {
        return null;
      }
    },

    async setAuthToken(token: string, serverUrl?: string): Promise<void> {
      if (hasNativeMyGo) {
        await NexHost.setAuthToken(serverUrl || '', token);
      }
      if (legacyIpc?.setAuthToken) {
        await legacyIpc.setAuthToken(token);
      }
      try {
        localStorage.setItem(tokenStorageKey(serverUrl), token);
        localStorage.setItem(TOKEN_KEY, token);
      } catch {
        // ignore storage quota errors
      }
    },

    async clearAuthToken(serverUrl?: string): Promise<void> {
      if (hasNativeMyGo) {
        await NexHost.clearAuthToken(serverUrl || '');
      }
      try {
        localStorage.removeItem(tokenStorageKey(serverUrl));
        localStorage.removeItem(TOKEN_KEY);
      } catch {
        // ignore storage errors
      }
    },

    async notifyApprovalRequested(payload: ApprovalNotification): Promise<void> {
      const title = `Approval required on ${payload.runnerName}`;
      const body = `Tool "${payload.toolName}" is waiting for authorization.`;
      if (hasNativeMyGo) {
        try {
          await NexHost.notify(title, body, payload.sessionId);
          return;
        } catch {
          // fall through to browser notification
        }
      }
      if (legacyIpc?.notify) {
        await legacyIpc.notify(title, body);
        return;
      }
      if (typeof Notification !== 'undefined' && Notification.permission === 'granted') {
        new Notification(title, { body });
      }
    },

    haptic(style): void {
      if (hasNativeMyGo) {
        void NexHost.haptic(style).catch(() => {});
        return;
      }
      if (legacyIpc?.haptic) {
        legacyIpc.haptic(style);
      } else if (typeof navigator !== 'undefined' && 'vibrate' in navigator) {
        navigator.vibrate(style === 'error' ? [30, 40, 30] : 10);
      }
    },

    async pickDirectory(): Promise<string | null> {
      if (!hasNativeMyGo) return null;
      try {
        const picked = await NexHost.pickDirectory();
        return picked || null;
      } catch {
        return null;
      }
    },

    async openExternal(rawUrl: string): Promise<void> {
      if (hasNativeMyGo) {
        await NexHost.openExternal(rawUrl);
        return;
      }
      if (typeof window !== 'undefined') {
        window.open(rawUrl, '_blank', 'noopener,noreferrer');
      }
    },

    async shareText(text: string, rawUrl = ''): Promise<void> {
      if (hasNativeMyGo) {
        await NexHost.shareText(text, rawUrl);
        return;
      }
      if (typeof navigator !== 'undefined' && typeof navigator.share === 'function') {
        await navigator.share({ text, url: rawUrl || undefined });
      }
    },

    onAppResume(cb) {
      if (!hasNativeMyGo) return () => {};
      return events.nexAppResume.on(cb);
    },

    onOpenSession(cb) {
      if (!hasNativeMyGo) return () => {};
      return events.nexOpenSession.on(cb);
    },

    onOpenSettings(cb) {
      if (!hasNativeMyGo) return () => {};
      return events.nexOpenSettings.on(cb);
    },

    onNewTask(cb) {
      if (!hasNativeMyGo) return () => {};
      return events.nexNewTask.on(() => cb());
    },

    onServerChanged(cb) {
      if (!hasNativeMyGo) return () => {};
      return events.nexServerChanged.on(cb);
    },

    getDeviceFontScale(): number {
      try {
        const raw = localStorage.getItem(FONT_SCALE_KEY);
        const parsed = raw ? Number(raw) : 14;
        return Number.isFinite(parsed) && parsed >= 11 && parsed <= 22 ? parsed : 14;
      } catch {
        return 14;
      }
    },

    setDeviceFontScale(px: number): void {
      const clamped = Math.max(11, Math.min(22, Math.round(px)));
      try {
        localStorage.setItem(FONT_SCALE_KEY, String(clamped));
      } catch {
        // ignore
      }
      if (typeof document !== 'undefined') {
        document.documentElement.style.setProperty('--ui-font-size', `${clamped}px`);
      }
    },
  };
}

export const hostBridge = createHostBridge();
