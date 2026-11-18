export type HostKind = 'browser' | 'mygo-desktop' | 'mygo-mobile';

export interface ApprovalNotification {
  sessionId: string;
  runnerName: string;
  toolName: string;
}

export interface HostBridge {
  readonly kind: HostKind;
  getAuthToken(): Promise<string | null>;
  setAuthToken(token: string): Promise<void>;
  notifyApprovalRequested(payload: ApprovalNotification): Promise<void>;
  haptic(style: 'light' | 'medium' | 'error'): void;
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
const FONT_SCALE_KEY = 'nex.device.font_px';

export function createHostBridge(): HostBridge {
  const mygo = typeof window !== 'undefined' ? window.__MYGO_IPC__ : undefined;
  const kind: HostKind = mygo
    ? mygo.platform === 'mobile'
      ? 'mygo-mobile'
      : 'mygo-desktop'
    : 'browser';

  return {
    kind,

    async getAuthToken(): Promise<string | null> {
      if (mygo?.getAuthToken) {
        return (await mygo.getAuthToken()) || null;
      }
      try {
        return localStorage.getItem(TOKEN_KEY);
      } catch {
        return null;
      }
    },

    async setAuthToken(token: string): Promise<void> {
      if (mygo?.setAuthToken) {
        await mygo.setAuthToken(token);
        return;
      }
      try {
        localStorage.setItem(TOKEN_KEY, token);
      } catch {
        // ignore storage quota errors
      }
    },

    async notifyApprovalRequested(payload: ApprovalNotification): Promise<void> {
      const title = `Approval required on ${payload.runnerName}`;
      const body = `Tool "${payload.toolName}" is waiting for authorization.`;
      if (mygo?.notify) {
        await mygo.notify(title, body);
        return;
      }
      if (typeof Notification !== 'undefined' && Notification.permission === 'granted') {
        new Notification(title, { body });
      }
    },

    haptic(style: 'light' | 'medium' | 'error'): void {
      if (mygo?.haptic) {
        mygo.haptic(style);
      } else if (typeof navigator !== 'undefined' && 'vibrate' in navigator) {
        navigator.vibrate(style === 'error' ? [30, 40, 30] : 10);
      }
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
        document.documentElement.style.setProperty('--nex-font-size', `${clamped}px`);
      }
    },
  };
}

export const hostBridge = createHostBridge();
