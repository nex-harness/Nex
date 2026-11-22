import { applyDocumentLocale, detectBrowserLocale, formatMsg, type LocaleCode } from '../i18n/messages';
import { create } from 'zustand';
import { hostBridge, type HostInfo } from '../host/bridge';
import { resolveEffectiveEffort } from './effort';
import {
  apiFetch,
  safeStorageGet,
  safeStorageRemove,
  safeStorageSet,
  setAuthRequiredHandler,
  setRuntimeAuthToken,
  setRuntimeServerUrl,
} from './protocol';
import { applySessionEvent, emptyStreamState, RawSessionEventRow, SessionStreamState } from './reducer';
import {
  AgentProfile,
  BrowseDirsResult,
  BrowserHandoffState,
  BrowserProbeResult,
  BrowserSessionState,
  ChatImageBlock,
  ComposerAttachment,
  CronJobRecord,
  DirEntry,
  FilePreview,
  GitSummary,
  MCPServerRecord,
  MemoryRecord,
  ModelMeta,
  PluginRecord,
  ProviderRecord,
  QueuedFollowUp,
  RewindResult,
  RunnerRecord,
  SessionRecord,
  SessionSearchHit,
  SettingsSectionId,
  SkillRecord,
  SSHTargetRecord,
  ThinkingEffort,
  TurnTelemetry,
  UsageSummary,
  UserPreferences,
  WorkbenchTab,
  WorkbenchTabKind,
} from './types';
import { wsClient } from './wsClient';

const ACTIVE_SESSION_STORAGE_KEY = 'nex.activeSessionId';

export function readPersistedSessionId(): string {
  if (typeof window !== 'undefined') {
    try {
      const fromQuery = new URLSearchParams(window.location.search).get('session')?.trim();
      if (fromQuery) return fromQuery;
    } catch {
      // ignore URL parse errors
    }
  }
  return safeStorageGet(ACTIVE_SESSION_STORAGE_KEY)?.trim() || '';
}

export function persistActiveSessionId(sessionId: string): void {
  const clean = (sessionId || '').trim();
  if (clean) {
    safeStorageSet(ACTIVE_SESSION_STORAGE_KEY, clean);
  } else {
    safeStorageRemove(ACTIVE_SESSION_STORAGE_KEY);
  }
  if (typeof window !== 'undefined' && typeof window.history?.replaceState === 'function') {
    try {
      const url = new URL(window.location.href);
      if (clean) {
        url.searchParams.set('session', clean);
      } else {
        url.searchParams.delete('session');
      }
      window.history.replaceState(null, '', url.pathname + url.search + url.hash);
    } catch {
      // ignore history errors in non-http test environments
    }
  }
}

let wsHandlersWired = false;
let hostEventsWired = false;

export function applyDocumentTheme(theme: UserPreferences['theme']): void {
  if (typeof document === 'undefined') return;
  const prefersDark =
    typeof window !== 'undefined' && typeof window.matchMedia === 'function'
      ? window.matchMedia('(prefers-color-scheme: dark)').matches
      : true;
  const isDark = theme === 'dark' || (theme === 'system' && prefersDark);
  document.documentElement.classList.toggle('dark', isDark);
}

function appendOptimisticUserPrompt(
  prev: SessionStreamState | undefined,
  text: string,
  images?: ChatImageBlock[],
): SessionStreamState {
  const base = prev || emptyStreamState();
  const optId = `opt_usr_${crypto.randomUUID().slice(0, 8)}`;
  return {
    ...base,
    running: true,
    runStartedAt: Date.now(),
    items: [
      ...base.items,
      {
        id: optId,
        role: 'user',
        text,
        thinking: '',
        images: images && images.length > 0 ? images : undefined,
        toolCalls: [],
        optimistic: true,
      },
    ],
  };
}

function resolveCatalogModel(
  models: ModelMeta[],
  providerId?: string,
  modelId?: string,
): { providerId: string; modelId: string } {
  const p = (providerId || '').trim();
  const m = (modelId || '').trim();
  if (!p || !m) return { providerId: '', modelId: '' };
  if (models.some((it) => it.provider_id === p && it.id === m)) {
    return { providerId: p, modelId: m };
  }
  return { providerId: '', modelId: '' };
}

function insertConcreteRightTab(
  tabs: WorkbenchTab[],
  activeId: string,
  sidePaneWasOpen: boolean,
  newTab: WorkbenchTab,
): { rightTabs: WorkbenchTab[]; activeRightTabId: string } {
  if (!sidePaneWasOpen) {
    const clean = tabs.filter((t) => t.kind !== 'new_tab');
    return { rightTabs: [...clean, newTab], activeRightTabId: newTab.id };
  }
  const activeIdx = tabs.findIndex((t) => t.id === activeId);
  if (activeIdx >= 0 && tabs[activeIdx].kind === 'new_tab') {
    const nextTabs = [...tabs];
    nextTabs[activeIdx] = newTab;
    return { rightTabs: nextTabs, activeRightTabId: newTab.id };
  }
  return { rightTabs: [...tabs, newTab], activeRightTabId: newTab.id };
}

let gitSummaryInFlight: Promise<GitSummary | null> | null = null;
let gitSummaryInFlightDiffs = false;

export interface AppState {
  hostInfo: HostInfo | null;
  serverConnectRequired: boolean;
  activeServerUrl: string;
  wsConnected: boolean;
  authChecked: boolean;
  authEnabled: boolean;
  authenticated: boolean;
  locale: LocaleCode;
  mainView: 'workspace' | 'settings';
  settingsSection: SettingsSectionId;
  sidebarVisible: boolean;
  sidePaneVisible: boolean;
  showBottomPanel: boolean;
  quickSearchOpen: boolean;
  previewImage: { src: string; alt?: string } | null;
  pinnedSessionIds: Record<string, boolean>;

  runners: RunnerRecord[];
  providers: ProviderRecord[];
  models: ModelMeta[];
  preferences: UserPreferences;
  sessions: SessionRecord[];
  archivedSessions: SessionRecord[];
  agentProfiles: AgentProfile[];
  memories: MemoryRecord[];
  sshTargets: SSHTargetRecord[];
  skills: SkillRecord[];
  plugins: PluginRecord[];
  mcpServers: MCPServerRecord[];
  cronJobs: CronJobRecord[];
  todaySummary: UsageSummary | null;

  selectedRunnerId: string;
  selectedWorkspacePath: string;
  recentWorkspaces: Array<{ runnerId: string; path: string }>;
  selectedProviderId: string;
  selectedModelId: string;
  draftProviderId: string;
  draftModelId: string;
  sessionModels: Record<string, { providerId: string; modelId: string }>;
  hiddenModelKeys: Record<string, boolean>;
  modelOrder: Record<string, string[]>;
  thinkingEffort: ThinkingEffort;
  permissionMode: 'full_access' | 'interactive' | 'read_only' | 'plan';
  activeSessionId: string;
  composerText: string;
  composerAttachments: ComposerAttachment[];

  streams: Record<string, SessionStreamState>;
  followUpQueues: Record<string, QueuedFollowUp[]>;
  gitSummary: GitSummary | null;
  gitDiffLoading: boolean;
  browserStates: Record<string, BrowserSessionState>;
  browserProbes: Record<string, BrowserProbeResult>;
  browserInstalling: Record<string, boolean>;

  rightTabs: WorkbenchTab[];
  activeRightTabId: string;
  bottomTerminals: Array<{ id: string; title: string; ptyId: string; createdAt: number }>;
  activeBottomTermId: string;

  openSettings: (section?: SettingsSectionId) => void;
  closeSettings: () => void;
  setLocale: (loc: LocaleCode) => void;
  togglePinSession: (sessionId: string) => void;
  toggleSidebar: () => void;
  toggleSidePane: () => void;
  toggleBottomPanel: () => void;
  setQuickSearchOpen: (open: boolean) => void;
  openImagePreview: (img: { src: string; alt?: string }) => void;
  closeImagePreview: () => void;

  setComposerText: (text: string) => void;
  addComposerAttachment: (att: ComposerAttachment) => void;
  removeComposerAttachment: (id: string) => void;
  clearComposerAttachments: () => void;
  appendComposerRef: (refText: string) => void;
  setSelectedRunner: (id: string, workspacePath?: string) => void;
  setSelectedProvider: (providerId: string) => void;
  setSelectedModel: (providerId: string, modelId: string) => void;
  toggleModelVisibility: (providerId: string, modelId: string) => void;
  reorderProviderModels: (providerId: string, orderedIds: string[]) => void;
  setThinkingEffort: (effort: ThinkingEffort) => void;
  setPermissionMode: (mode: 'full_access' | 'interactive' | 'read_only' | 'plan') => void;
  updateThemeQuick: (theme: UserPreferences['theme']) => Promise<void>;

  bootstrap: () => Promise<void>;
  connectToServer: (rawUrl: string) => Promise<{ ok: boolean; error?: string }>;
  useLocalDesktopMode: () => Promise<{ ok: boolean; error?: string }>;
  openServerConnect: () => void;
  cancelServerConnect: () => void;
  checkAuthStatus: () => Promise<{ enabled: boolean; authenticated: boolean }>;
  loginWithPassword: (password: string) => Promise<{ ok: boolean; error?: string }>;
  logoutWeb: () => Promise<void>;
  updateWebPassword: (currentPassword: string, newPassword: string) => Promise<{ ok: boolean; enabled?: boolean; error?: string }>;
  refreshInitialData: () => Promise<void>;
  refreshRegistries: () => Promise<void>;
  selectSession: (sessionId: string) => Promise<void>;
  startNewSession: (runnerId?: string, workspacePath?: string) => void;
  renameSession: (sessionId: string, newTitle: string) => Promise<void>;
  deleteSession: (sessionId: string) => Promise<void>;
  forkSession: (
    sessionId: string,
    opts?: { upToSeq?: number; beforeSeq?: number; title?: string; prefillText?: string },
  ) => Promise<string | null>;
  searchSessions: (query: string) => Promise<SessionSearchHit[]>;
  exportSessionMarkdown: (sessionId: string) => Promise<{ filename: string; markdown: string } | null>;
  rewindSessionFiles: (sessionId: string, beforeSeq: number) => Promise<RewindResult | null>;
  consolidateMemories: (opts?: {
    providerId?: string;
    modelId?: string;
  }) => Promise<{ before_count: number; after_count: number; summary: string; memories: MemoryRecord[] } | null>;
  sendPromptOrSteer: (overrideText?: string, mode?: 'follow_up' | 'steer') => void;
  queueFollowUp: (text: string) => void;
  removeFollowUp: (sessionId: string, id: string) => void;
  promoteFollowUpToSteer: (sessionId: string, id: string) => void;
  abortActiveSession: () => void;
  resolveApproval: (approvalId: string, approved: boolean) => void;
  resolveUIPrompt: (requestId: string, confirmed: boolean, value: string, canceled?: boolean) => void;

  archiveSession: (sessionId: string) => Promise<void>;
  unarchiveSession: (sessionId: string) => Promise<void>;
  deleteArchivedPermanently: (sessionId: string) => Promise<void>;

  openSubagentTab: (data: import('./types').SubagentInspectorData) => void;
  openRightTab: (kind: WorkbenchTabKind, title?: string) => Promise<void>;
  closeRightTab: (tabId: string) => void;
  setActiveRightTab: (tabId: string) => void;
  openFileInPreviewTab: (relPath: string, line?: number) => Promise<void>;
  listWorkspaceDir: (relPath: string) => Promise<DirEntry[]>;
  browseRunnerDirs: (runnerId: string, dirPath?: string, includeHidden?: boolean) => Promise<BrowseDirsResult>;
  searchWorkspaceFiles: (query: string) => Promise<string[]>;
  refreshGitSummary: (includeDiffs?: boolean) => Promise<GitSummary | null>;
  gitStage: (relPath?: string) => Promise<void>;
  gitUnstage: (relPath?: string) => Promise<void>;
  gitDiscard: (relPath: string) => Promise<void>;
  gitCommit: (message: string) => Promise<{ ok: boolean; commit?: string; error?: string }>;
  gitListBranches: () => Promise<{ current: string; branches: string[] }>;
  gitCheckout: (branch: string, createBranch?: boolean) => Promise<{ ok: boolean; error?: string }>;
  spawnBottomTerminal: () => Promise<void>;
  closeBottomTerminal: (id: string) => void;

  openBrowserTab: () => void;
  sendBrowserControl: (req: {
    session_id?: string;
    tab_id?: string;
    action: string;
    url?: string;
    width?: number;
    height?: number;
    quality?: number;
    max_width?: number;
    max_height?: number;
  }) => Promise<BrowserSessionState | null>;
  sendBrowserInput: (ev: {
    session_id?: string;
    tab_id?: string;
    kind: 'mouse' | 'wheel' | 'key';
    type?: string;
    x?: number;
    y?: number;
    button?: string;
    click_count?: number;
    delta_x?: number;
    delta_y?: number;
    key?: string;
    code?: string;
    text?: string;
    modifiers?: number;
  }) => void;
  resolveBrowserHandoff: (sessionId: string, tabId: string, resumed: boolean) => void;
  probeRunnerBrowser: (runnerId: string) => Promise<BrowserProbeResult | null>;
  installRunnerBrowser: (runnerId: string, mirror?: string) => Promise<BrowserProbeResult | null>;
  clearRunnerBrowserData: (runnerId: string, mode: 'cache' | 'all') => Promise<boolean>;
}

const DEFAULT_PREFS: UserPreferences = {
  theme: 'light',
  code_theme: 'github-light',
  font_family: 'Inter, -apple-system, BlinkMacSystemFont, "PingFang SC", sans-serif',
  mono_font_family: 'JetBrains Mono, SFMono-Regular, Menlo, monospace',
  expand_thinking: false,
  auto_archive_after_days: 30,
};

const INITIAL_LOCALE: LocaleCode = detectBrowserLocale(safeStorageGet('nex.locale'));
applyDocumentLocale(INITIAL_LOCALE);

export const useNexStore = create<AppState>((set, get) => ({
  hostInfo: null,
  serverConnectRequired: false,
  activeServerUrl: '',
  wsConnected: false,
  authChecked: false,
  authEnabled: false,
  authenticated: true,
  locale: INITIAL_LOCALE,
  mainView: 'workspace',
  settingsSection: 'general',
  sidebarVisible: typeof window !== 'undefined' ? window.innerWidth >= 768 : true,
  sidePaneVisible: false,
  showBottomPanel: false,
  quickSearchOpen: false,
  previewImage: null,
  pinnedSessionIds: (() => {
    try {
      return JSON.parse(safeStorageGet('nex.pinned.sessions') || '{}');
    } catch {
      return {};
    }
  })(),

  runners: [],
  providers: [],
  models: [],
  preferences: DEFAULT_PREFS,
  sessions: [],
  archivedSessions: [],
  agentProfiles: [],
  memories: [],
  sshTargets: [],
  skills: [],
  plugins: [],
  mcpServers: [],
  cronJobs: [],
  todaySummary: null,

  selectedRunnerId: '',
  selectedWorkspacePath: '',
  recentWorkspaces: (() => {
    try {
      const raw = safeStorageGet('nex.recentWorkspaces');
      const parsed = raw ? JSON.parse(raw) : [];
      return Array.isArray(parsed) ? parsed : [];
    } catch {
      return [];
    }
  })(),
  selectedProviderId: '',
  selectedModelId: '',
  draftProviderId: '',
  draftModelId: '',
  sessionModels: {},
  hiddenModelKeys: (() => {
    try {
      const raw = safeStorageGet('nex.hiddenModels');
      return raw ? (JSON.parse(raw) as Record<string, boolean>) : {};
    } catch {
      return {};
    }
  })(),
  modelOrder: (() => {
    try {
      const raw = safeStorageGet('nex.modelOrder');
      return raw ? (JSON.parse(raw) as Record<string, string[]>) : {};
    } catch {
      return {};
    }
  })(),
  thinkingEffort: (() => {
    const saved = safeStorageGet('nex.thinkingEffort');
    if (saved !== null) {
      return saved as ThinkingEffort;
    }
    return 'medium';
  })(),
  permissionMode: 'full_access',
  activeSessionId: '',
  composerText: '',
  composerAttachments: [],

  streams: {},
  followUpQueues: {},
  gitSummary: null,
  gitDiffLoading: false,

  rightTabs: [],
  activeRightTabId: '',
  bottomTerminals: [],
  activeBottomTermId: '',
  browserStates: {},
  browserProbes: {},
  browserInstalling: {},

  openSettings: (section) => {
    set((s) => ({ mainView: 'settings', settingsSection: section || s.settingsSection }));
    void get().refreshRegistries();
  },
  closeSettings: () => set({ mainView: 'workspace' }),
  setLocale: (loc) => {
    safeStorageSet('nex.locale', loc);
    applyDocumentLocale(loc);
    set({ locale: loc });
  },
  togglePinSession: (sessionId) => {
    set((s) => {
      const next = { ...s.pinnedSessionIds };
      if (next[sessionId]) delete next[sessionId];
      else next[sessionId] = true;
      safeStorageSet('nex.pinned.sessions', JSON.stringify(next));
      return { pinnedSessionIds: next };
    });
  },
  toggleSidebar: () => set((s) => ({ sidebarVisible: !s.sidebarVisible })),
  toggleSidePane: () =>
    set((s) => {
      const nextOpen = !s.sidePaneVisible;
      if (nextOpen && s.rightTabs.length === 0) {
        const launcherTab: WorkbenchTab = {
          id: `tab_${crypto.randomUUID().slice(0, 8)}`,
          kind: 'new_tab',
          title: formatMsg(s.locale, 'sidePane.newTab'),
          closable: true,
        };
        return { sidePaneVisible: true, rightTabs: [launcherTab], activeRightTabId: launcherTab.id };
      }
      if (!nextOpen && s.rightTabs.length === 1 && s.rightTabs[0].kind === 'new_tab') {
        return { sidePaneVisible: false, rightTabs: [], activeRightTabId: '' };
      }
      return { sidePaneVisible: nextOpen };
    }),
  toggleBottomPanel: () => {
    const next = !get().showBottomPanel;
    set({ showBottomPanel: next });
    if (next && get().bottomTerminals.length === 0) {
      void get().spawnBottomTerminal();
    }
  },
  setQuickSearchOpen: (open) => set({ quickSearchOpen: open }),
  openImagePreview: (img) => set({ previewImage: img }),
  closeImagePreview: () => set({ previewImage: null }),

  setComposerText: (text) => set({ composerText: text }),
  addComposerAttachment: (att) =>
    set((s) => ({
      composerAttachments: s.composerAttachments.length >= 4 ? s.composerAttachments : [...s.composerAttachments, att],
    })),
  removeComposerAttachment: (id) =>
    set((s) => ({
      composerAttachments: s.composerAttachments.filter((a) => a.id !== id),
    })),
  clearComposerAttachments: () => set({ composerAttachments: [] }),
  appendComposerRef: (refText) =>
    set((s) => ({ composerText: s.composerText ? `${s.composerText} ${refText} ` : `${refText} ` })),
  setSelectedRunner: (id, workspacePath) => {
    const r = get().runners.find((it) => it.id === id);
    const resolvedPath = workspacePath ?? r?.workspace_default ?? '';
    set((s) => {
      let nextRecent = s.recentWorkspaces;
      if (id && resolvedPath) {
        const filtered = s.recentWorkspaces.filter((item) => !(item.runnerId === id && item.path === resolvedPath));
        nextRecent = [{ runnerId: id, path: resolvedPath }, ...filtered].slice(0, 12);
        safeStorageSet('nex.recentWorkspaces', JSON.stringify(nextRecent));
      }
      return {
        selectedRunnerId: id,
        selectedWorkspacePath: resolvedPath,
        recentWorkspaces: nextRecent,
      };
    });
    void get().refreshGitSummary(false);
  },
  setSelectedProvider: (providerId) =>
    set((s) => {
      const providerModels = s.models.filter(
        (m) => m.provider_id === providerId && !s.hiddenModelKeys[`${m.provider_id}::${m.id}`],
      );
      const keepCurrent =
        s.selectedProviderId === providerId && providerModels.some((m) => m.id === s.selectedModelId);
      const fallbackAll = s.models.filter((m) => m.provider_id === providerId);
      const nextModelId = keepCurrent
        ? s.selectedModelId
        : providerModels[0]?.id || fallbackAll[0]?.id || '';
      if (!s.activeSessionId) {
        return {
          selectedProviderId: providerId,
          selectedModelId: nextModelId,
          draftProviderId: providerId,
          draftModelId: nextModelId,
        };
      }
      return {
        selectedProviderId: providerId,
        selectedModelId: nextModelId,
        sessionModels: {
          ...s.sessionModels,
          [s.activeSessionId]: { providerId, modelId: nextModelId },
        },
        sessions: s.sessions.some((sess) => sess.id === s.activeSessionId)
          ? s.sessions.map((sess) =>
              sess.id === s.activeSessionId
                ? { ...sess, provider_id: providerId, model_id: nextModelId }
                : sess,
            )
          : s.sessions,
      };
    }),
  setSelectedModel: (providerId, modelId) =>
    set((s) => {
      if (!s.activeSessionId) {
        return {
          selectedProviderId: providerId,
          selectedModelId: modelId,
          draftProviderId: providerId,
          draftModelId: modelId,
        };
      }
      return {
        selectedProviderId: providerId,
        selectedModelId: modelId,
        sessionModels: {
          ...s.sessionModels,
          [s.activeSessionId]: { providerId, modelId },
        },
        sessions: s.sessions.some((sess) => sess.id === s.activeSessionId)
          ? s.sessions.map((sess) =>
              sess.id === s.activeSessionId
                ? { ...sess, provider_id: providerId, model_id: modelId }
                : sess,
            )
          : s.sessions,
      };
    }),
  toggleModelVisibility: (providerId, modelId) =>
    set((s) => {
      const key = `${providerId}::${modelId}`;
      const next = { ...s.hiddenModelKeys, [key]: !s.hiddenModelKeys[key] };
      safeStorageSet('nex.hiddenModels', JSON.stringify(next));
      return { hiddenModelKeys: next };
    }),
  reorderProviderModels: (providerId, orderedIds) =>
    set((s) => {
      const next = { ...s.modelOrder, [providerId]: orderedIds };
      safeStorageSet('nex.modelOrder', JSON.stringify(next));
      return { modelOrder: next };
    }),
  setThinkingEffort: (effort) => {
    safeStorageSet('nex.thinkingEffort', effort);
    set({ thinkingEffort: effort });
  },
  setPermissionMode: (mode) => set({ permissionMode: mode }),

  updateThemeQuick: async (theme) => {
    const nextPrefs = { ...get().preferences, theme };
    applyDocumentTheme(theme);
    set({ preferences: nextPrefs });
    await apiFetch('/api/v1/preferences', { method: 'PUT', body: JSON.stringify(nextPrefs) }).catch(() => {});
  },

  openServerConnect: () => set({ serverConnectRequired: true }),
  cancelServerConnect: () => {
    if (get().activeServerUrl) {
      set({ serverConnectRequired: false });
    }
  },

  connectToServer: async (rawUrl) => {
    try {
      const info = await hostBridge.setServerUrl(rawUrl);
      const nextUrl = info.server_url || rawUrl.trim().replace(/\/+$/, '');
      setRuntimeServerUrl(nextUrl);
      const tok = (await hostBridge.getAuthToken(nextUrl)) || '';
      setRuntimeAuthToken(tok);
      if (tok) safeStorageSet('nex.auth.token', tok);
      else safeStorageRemove('nex.auth.token');
      wsClient.disconnect();
      persistActiveSessionId('');
      set({
        hostInfo: info,
        activeServerUrl: nextUrl,
        serverConnectRequired: false,
        authChecked: false,
        authenticated: true,
        activeSessionId: '',
        sessions: [],
        runners: [],
        streams: {},
        selectedRunnerId: '',
        selectedWorkspacePath: '',
      });
      await get().bootstrap();
      return { ok: true };
    } catch (err) {
      return { ok: false, error: err instanceof Error ? err.message : 'Failed to connect' };
    }
  },

  useLocalDesktopMode: async () => {
    try {
      const info = await hostBridge.switchConnectionMode('local');
      const nextUrl = info.server_url || info.local_url || '';
      setRuntimeServerUrl(nextUrl);
      const tok = nextUrl ? (await hostBridge.getAuthToken(nextUrl)) || '' : '';
      setRuntimeAuthToken(tok);
      if (tok) safeStorageSet('nex.auth.token', tok);
      else safeStorageRemove('nex.auth.token');
      wsClient.disconnect();
      persistActiveSessionId('');
      set({
        hostInfo: info,
        activeServerUrl: nextUrl,
        serverConnectRequired: false,
        authChecked: false,
        authenticated: true,
        activeSessionId: '',
        sessions: [],
        runners: [],
        streams: {},
        selectedRunnerId: '',
        selectedWorkspacePath: '',
      });
      await get().bootstrap();
      return { ok: true };
    } catch (err) {
      return { ok: false, error: err instanceof Error ? err.message : 'Local mode unavailable' };
    }
  },

  checkAuthStatus: async () => {
    try {
      const st = await apiFetch<{ enabled: boolean; authenticated: boolean }>('/api/v1/auth/status');
      const enabled = Boolean(st?.enabled);
      const authenticated = enabled ? Boolean(st?.authenticated) : true;
      set({ authChecked: true, authEnabled: enabled, authenticated });
      return { enabled, authenticated };
    } catch {
      if (hostBridge.kind !== 'browser') {
        set({ authChecked: false, authenticated: false, serverConnectRequired: true });
        return { enabled: true, authenticated: false };
      }
      set({ authChecked: true, authEnabled: false, authenticated: true });
      return { enabled: false, authenticated: true };
    }
  },

  loginWithPassword: async (password) => {
    try {
      const res = await apiFetch<{ ok?: boolean; enabled?: boolean; token?: string }>('/api/v1/auth/login', {
        method: 'POST',
        body: JSON.stringify({ password }),
      });
      if (res?.token) {
        setRuntimeAuthToken(res.token);
        safeStorageSet('nex.auth.token', res.token);
        await hostBridge.setAuthToken(res.token, get().activeServerUrl || undefined);
      }
      set({ authChecked: true, authEnabled: Boolean(res?.enabled ?? true), authenticated: true });
      await get().bootstrap();
      return { ok: true };
    } catch (err) {
      return { ok: false, error: err instanceof Error ? err.message : 'Login failed' };
    }
  },

  logoutWeb: async () => {
    try {
      await apiFetch('/api/v1/auth/logout', { method: 'POST' });
    } catch {
      // ignore
    }
    setRuntimeAuthToken('');
    safeStorageRemove('nex.auth.token');
    await hostBridge.clearAuthToken(get().activeServerUrl || undefined);
    wsClient.disconnect();
    set({ authenticated: false, wsConnected: false, mainView: 'workspace' });
  },

  updateWebPassword: async (currentPassword, newPassword) => {
    try {
      const res = await apiFetch<{ ok?: boolean; enabled?: boolean; token?: string }>('/api/v1/auth/password', {
        method: 'PUT',
        body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
      });
      if (res?.token) {
        setRuntimeAuthToken(res.token);
        safeStorageSet('nex.auth.token', res.token);
        await hostBridge.setAuthToken(res.token, get().activeServerUrl || undefined);
      } else if (!res?.enabled) {
        setRuntimeAuthToken('');
        safeStorageRemove('nex.auth.token');
        await hostBridge.clearAuthToken(get().activeServerUrl || undefined);
      }
      set({ authEnabled: Boolean(res?.enabled), authenticated: true });
      return { ok: true, enabled: Boolean(res?.enabled) };
    } catch (err) {
      return { ok: false, error: err instanceof Error ? err.message : 'Failed to update password' };
    }
  },

  bootstrap: async () => {
    hostBridge.setDeviceFontScale(hostBridge.getDeviceFontScale());
    applyDocumentLocale(get().locale);

    setAuthRequiredHandler(() => {
      setRuntimeAuthToken('');
      safeStorageRemove('nex.auth.token');
      void hostBridge.clearAuthToken(get().activeServerUrl || undefined);
      wsClient.disconnect();
      set({ authEnabled: true, authenticated: false, wsConnected: false });
    });

    if (!hostEventsWired) {
      hostEventsWired = true;
      hostBridge.onAppResume(() => {
        if (!get().serverConnectRequired && (!get().authEnabled || get().authenticated)) {
          wsClient.reconnectNow();
        }
      });
      hostBridge.onOpenSession((sid) => {
        if (sid) {
          set({ mainView: 'workspace', serverConnectRequired: false });
          void get().selectSession(sid);
        }
      });
      hostBridge.onOpenSettings((section) => {
        set({ serverConnectRequired: false });
        get().openSettings((section || 'general') as SettingsSectionId);
      });
      hostBridge.onNewTask(() => {
        set({ serverConnectRequired: false, mainView: 'workspace' });
        get().startNewSession();
      });
      hostBridge.onServerChanged((info) => {
        const nextUrl = info.server_url || '';
        const prevUrl = get().activeServerUrl;
        set({ hostInfo: info });
        if (!nextUrl) {
          setRuntimeServerUrl('');
          set({ activeServerUrl: '', serverConnectRequired: true });
          wsClient.disconnect();
          return;
        }
        if (nextUrl !== prevUrl) {
          void (async () => {
            setRuntimeServerUrl(nextUrl);
            const tok = (await hostBridge.getAuthToken(nextUrl)) || '';
            setRuntimeAuthToken(tok);
            set({
              activeServerUrl: nextUrl,
              serverConnectRequired: false,
              activeSessionId: '',
              sessions: [],
              runners: [],
              streams: {},
            });
            wsClient.disconnect();
            await get().bootstrap();
          })();
        }
      });
    }

    const info = await hostBridge.getHostInfo();
    if (info) {
      set({ hostInfo: info });
      if (!info.server_url) {
        setRuntimeServerUrl('');
        set({ activeServerUrl: '', serverConnectRequired: true });
        return;
      }
      setRuntimeServerUrl(info.server_url);
      const tok = (await hostBridge.getAuthToken(info.server_url)) || '';
      setRuntimeAuthToken(tok);
      if (tok) safeStorageSet('nex.auth.token', tok);
      set({ activeServerUrl: info.server_url, serverConnectRequired: false });
    }

    const auth = await get().checkAuthStatus();
    if (auth.enabled && !auth.authenticated) {
      return;
    }

    if (!wsHandlersWired) {
      wsHandlersWired = true;
      wsClient.onStatus((connected) => {
        set({ wsConnected: connected });
        if (connected) {
          const activeId = get().activeSessionId;
          if (activeId) {
            const lastSeq = get().streams[activeId]?.lastSeq || 0;
            wsClient.sendEnvelope('client.subscribe', { session_id: activeId, last_seen_seq: lastSeq });
          }
          void get().refreshGitSummary(false);
        }
      });

      // ZCode-style per-frame event coalescing buffer: at most ONE Zustand commit per animation frame!
      const pendingEvents = new Map<string, { runnerId: string; rows: RawSessionEventRow[] }>();
      let flushScheduled = false;

      const flushPendingEvents = () => {
        flushScheduled = false;
        if (pendingEvents.size === 0) return;
        const batch = Array.from(pendingEvents.entries());
        pendingEvents.clear();

        let shouldRefreshGit = false;
        let shouldRefreshData = false;
        const endedSessions: string[] = [];

        set((state) => {
          const nextStreams = { ...state.streams };
          let nextSessions = state.sessions;
          for (const [sid, group] of batch) {
            let cur = nextStreams[sid] || emptyStreamState();
            const prevApproval = cur.pendingApproval;
            for (const raw of group.rows) {
              if (raw.event_type === 'session_title_updated') {
                const newTitle = String((raw.payload || {}).title || '').trim();
                if (newTitle) {
                  nextSessions = nextSessions.map((s) => (s.id === sid ? { ...s, title: newTitle } : s));
                }
                continue;
              }
              cur = applySessionEvent(cur, group.runnerId, raw);
              if (raw.event_type === 'agent_start' && nextSessions.some((s) => s.id === sid && s.status !== 'running')) {
                nextSessions = nextSessions.map((s) => (s.id === sid ? { ...s, status: 'running' } : s));
              }
              if (raw.event_type === 'tool_execution_end') shouldRefreshGit = true;
              if (raw.event_type === 'agent_end') {
                shouldRefreshGit = true;
                shouldRefreshData = true;
                endedSessions.push(sid);
                if (nextSessions.some((s) => s.id === sid && s.status !== 'idle')) {
                  nextSessions = nextSessions.map((s) => (s.id === sid ? { ...s, status: 'idle' } : s));
                }
              }
            }
            if (cur.pendingApproval && cur.pendingApproval !== prevApproval) {
              void hostBridge.notifyApprovalRequested({
                sessionId: sid,
                runnerName: group.runnerId,
                toolName: cur.pendingApproval.toolName,
              });
            }
            nextStreams[sid] = cur;
          }
          return nextSessions !== state.sessions
            ? { streams: nextStreams, sessions: nextSessions }
            : { streams: nextStreams };
        });

        // Automatically drain next queued follow-up message once the session run completes
        for (const sid of endedSessions) {
          const q = get().followUpQueues[sid];
          if (q && q.length > 0) {
            const [nextItem, ...rest] = q;
            set((st) => ({
              followUpQueues: {
                ...st.followUpQueues,
                [sid]: rest,
              },
              streams: {
                ...st.streams,
                [sid]: appendOptimisticUserPrompt(st.streams[sid], nextItem.text),
              },
            }));
            const stNow = get();
            const activeModel = stNow.models.find(
              (m) => m.provider_id === stNow.selectedProviderId && m.id === stNow.selectedModelId,
            );
            const effectiveThinkingEffort = resolveEffectiveEffort(activeModel, stNow.thinkingEffort);
            wsClient.sendEnvelope(
              'session.prompt',
              {
                session_id: sid,
                runner_id: stNow.selectedRunnerId,
                workspace_path: stNow.selectedWorkspacePath || undefined,
                provider_id: stNow.selectedProviderId,
                model_id: stNow.selectedModelId,
                thinking_effort: effectiveThinkingEffort,
                permission_mode: stNow.permissionMode,
                text: nextItem.text,
              },
              { session_id: sid, runner_id: stNow.selectedRunnerId },
            );
          }
        }

        if (shouldRefreshGit) void get().refreshGitSummary(false);
        if (shouldRefreshData) void get().refreshInitialData();
      };

      const scheduleFrameFlush = (immediate: boolean) => {
        if (immediate) {
          flushPendingEvents();
          return;
        }
        if (flushScheduled) return;
        flushScheduled = true;
        if (typeof requestAnimationFrame === 'function') {
          requestAnimationFrame(flushPendingEvents);
        } else {
          setTimeout(flushPendingEvents, 16);
        }
      };

      wsClient.onFrame((env) => {
        if (env.type === 'runner.status') {
          void get().refreshInitialData();
          return;
        }
        if (env.type === 'memory.sync') {
          void get().refreshRegistries();
          return;
        }
        if (env.type === 'browser.state' && env.payload) {
          const st = env.payload as BrowserSessionState;
          const sid = st.session_id || env.session_id || '';
          if (!sid) return;
          set((state) => ({
            browserStates: { ...state.browserStates, [sid]: st },
          }));
          if (sid === get().activeSessionId && (st.tabs || []).length > 0) {
            get().openBrowserTab();
          }
          return;
        }
        if (env.type === 'browser.handoff' && env.payload) {
          const hst = env.payload as BrowserHandoffState;
          const sid = hst.session_id || env.session_id || '';
          if (!sid) return;
          set((state) => {
            const prev = state.browserStates[sid] || { session_id: sid, tabs: [] };
            return {
              browserStates: {
                ...state.browserStates,
                [sid]: {
                  ...prev,
                  handoff: hst.state === 'awaiting_human' ? hst : null,
                },
              },
            };
          });
          if (sid === get().activeSessionId && hst.state === 'awaiting_human') {
            get().openBrowserTab();
          }
          return;
        }
        if (env.type === 'session.event' && env.payload) {
          const raw = env.payload as RawSessionEventRow;
          const sid = raw.session_id || env.session_id || '';
          if (!sid) return;
          const rid = env.runner_id || get().selectedRunnerId;
          let group = pendingEvents.get(sid);
          if (!group) {
            group = { runnerId: rid, rows: [] };
            pendingEvents.set(sid, group);
          }
          group.rows.push(raw);

          const isHighFreqDelta = raw.event_type === 'message_update' || raw.event_type === 'tool_execution_update';
          scheduleFrameFlush(!isHighFreqDelta);
          return;
        }
        if (env.type === 'error' && env.session_id) {
          const sid = env.session_id;
          const errText = String((env.payload as { error?: string } | undefined)?.error || '').trim();
          set((state) => {
            const cur = state.streams[sid];
            if (!cur || !cur.running) return {};
            const last = cur.items[cur.items.length - 1];
            const hasTailErr = Boolean(last && last.role === 'assistant' && errText && last.text.includes(errText));
            const nextItems =
              errText && !hasTailErr
                ? [
                    ...cur.items,
                    {
                      id: `err_${Date.now()}`,
                      role: 'assistant' as const,
                      text: `**Error:** ${errText}`,
                      thinking: '',
                      toolCalls: [],
                      streaming: false,
                    },
                  ]
                : cur.items.map((it) => (it.streaming || it.optimistic ? { ...it, streaming: false, optimistic: false } : it));
            return {
              streams: {
                ...state.streams,
                [sid]: {
                  ...cur,
                  running: false,
                  runStartedAt: undefined,
                  items: nextItems,
                },
              },
            };
          });
        }
      });
    }

    wsClient.connect();
    await get().refreshInitialData();
    await get().refreshRegistries();

    // Restore active conversation across page reload (from URL `?session=` or localStorage)
    if (!get().activeSessionId) {
      const savedSessionId = readPersistedSessionId();
      if (savedSessionId) {
        const exists = get().sessions.some((s) => s.id === savedSessionId);
        if (exists) {
          await get().selectSession(savedSessionId);
        } else {
          persistActiveSessionId('');
        }
      }
    }
  },

  refreshInitialData: async () => {
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || 'Asia/Shanghai';
    const [runners, providers, models, prefs, sessions, archives, todaySummary] = await Promise.all([
      apiFetch<RunnerRecord[]>('/api/v1/runners').catch(() => []),
      apiFetch<ProviderRecord[]>('/api/v1/providers').catch(() => []),
      apiFetch<ModelMeta[]>('/api/v1/models').catch(() => []),
      apiFetch<UserPreferences>('/api/v1/preferences').catch(() => DEFAULT_PREFS),
      apiFetch<SessionRecord[]>('/api/v1/sessions').catch(() => []),
      apiFetch<SessionRecord[]>('/api/v1/archives').catch(() => []),
      apiFetch<UsageSummary>(`/api/v1/stats?window=today&tz=${encodeURIComponent(tz)}`).catch(() => null),
    ]);

    applyDocumentTheme(prefs.theme);
    if (typeof document !== 'undefined') {
      if (prefs.font_family) document.documentElement.style.setProperty('--nex-font-sans', prefs.font_family);
      if (prefs.mono_font_family) document.documentElement.style.setProperty('--nex-font-mono', prefs.mono_font_family);
    }

    set((state) => {
      const onlineRunner = (runners || []).find((r) => r.status === 'online') || (runners || [])[0];
      const nextRunner = state.selectedRunnerId || onlineRunner?.id || '';
      const chosenRunnerObj = (runners || []).find((r) => r.id === nextRunner);
      const loadedModels = models || [];
      const customModel = loadedModels.find((m) => m.source === 'custom') || loadedModels[0];

      // Resolve draft-mode default model without clobbering active session bindings.
      const hadDraftChoice = Boolean(state.draftProviderId || state.draftModelId);
      const resolvedDraft = hadDraftChoice
        ? resolveCatalogModel(loadedModels, state.draftProviderId, state.draftModelId)
        : customModel
          ? { providerId: customModel.provider_id, modelId: customModel.id }
          : { providerId: '', modelId: '' };

      let nextProviderId = resolvedDraft.providerId;
      let nextModelId = resolvedDraft.modelId;
      if (state.activeSessionId) {
        const bound = state.sessionModels[state.activeSessionId];
        const sessMeta = (sessions || []).find((s) => s.id === state.activeSessionId);
        const rawProv = bound?.providerId ?? sessMeta?.provider_id ?? state.selectedProviderId;
        const rawMod = bound?.modelId ?? sessMeta?.model_id ?? state.selectedModelId;
        const resolvedSess = resolveCatalogModel(loadedModels, rawProv, rawMod);
        nextProviderId = resolvedSess.providerId;
        nextModelId = resolvedSess.modelId;
      }

      return {
        runners: runners || [],
        providers: providers || [],
        models: loadedModels,
        preferences: prefs || DEFAULT_PREFS,
        sessions: sessions || [],
        archivedSessions: archives || [],
        todaySummary,
        selectedRunnerId: nextRunner,
        selectedWorkspacePath: state.selectedWorkspacePath || chosenRunnerObj?.workspace_default || '',
        draftProviderId: resolvedDraft.providerId,
        draftModelId: resolvedDraft.modelId,
        selectedProviderId: nextProviderId,
        selectedModelId: nextModelId,
      };
    });
  },

  refreshRegistries: async () => {
    const [agentProfiles, memories, sshTargets, skills, plugins, mcpServers, cronJobs] = await Promise.all([
      apiFetch<AgentProfile[]>('/api/v1/agents').catch(() => []),
      apiFetch<MemoryRecord[]>('/api/v1/memories').catch(() => []),
      apiFetch<SSHTargetRecord[]>('/api/v1/ssh/targets').catch(() => []),
      apiFetch<SkillRecord[]>('/api/v1/skills').catch(() => []),
      apiFetch<PluginRecord[]>('/api/v1/plugins').catch(() => []),
      apiFetch<MCPServerRecord[]>('/api/v1/mcp').catch(() => []),
      apiFetch<CronJobRecord[]>('/api/v1/cron').catch(() => []),
    ]);
    set({
      agentProfiles: agentProfiles || [],
      memories: memories || [],
      sshTargets: sshTargets || [],
      skills: skills || [],
      plugins: plugins || [],
      mcpServers: mcpServers || [],
      cronJobs: cronJobs || [],
    });
  },

  selectSession: async (sessionId) => {
    persistActiveSessionId(sessionId);
    const metaBefore = get().sessions.find((s) => s.id === sessionId);
    const boundBefore = get().sessionModels[sessionId];
    const resolvedBefore = resolveCatalogModel(
      get().models,
      boundBefore?.providerId ?? metaBefore?.provider_id,
      boundBefore?.modelId ?? metaBefore?.model_id,
    );
    set({
      mainView: 'workspace',
      activeSessionId: sessionId,
      selectedProviderId: resolvedBefore.providerId,
      selectedModelId: resolvedBefore.modelId,
    });
    const [evs, tels] = await Promise.all([
      apiFetch<RawSessionEventRow[]>(`/api/v1/sessions/${sessionId}/events`).catch(() => []),
      apiFetch<TurnTelemetry[]>(`/api/v1/sessions/${sessionId}/telemetry`).catch(() => []),
    ]);
    const meta = get().sessions.find((s) => s.id === sessionId);
    const runnerId = meta?.runner_id || get().selectedRunnerId;
    let st = emptyStreamState();
    for (const ev of evs || []) {
      st = applySessionEvent(st, runnerId, ev);
    }
    // If server metadata marks session as idle (e.g., forked or completed historical session),
    // ensure no trailing item stays stuck in streaming/running state (which also blocks Shiki highlight!).
    if (meta && meta.status !== 'running' && (st.running || st.items.some((it) => it.streaming))) {
      st = {
        ...st,
        running: false,
        runStartedAt: undefined,
        items: st.items.map((it) => (it.streaming ? { ...it, streaming: false } : it)),
      };
    } else if (meta?.status === 'running' && st.running && !st.runStartedAt) {
      st = { ...st, runStartedAt: Date.now() };
    }
    if (tels && tels.length > 0) {
      st.latestTelemetry = tels[tels.length - 1];
    }
    set((s) => {
      const bound = s.sessionModels[sessionId];
      const latestMeta = s.sessions.find((it) => it.id === sessionId);
      const resolvedModel = resolveCatalogModel(
        s.models,
        bound?.providerId ?? latestMeta?.provider_id,
        bound?.modelId ?? latestMeta?.model_id,
      );
      return {
        selectedRunnerId: runnerId || s.selectedRunnerId,
        selectedWorkspacePath: latestMeta?.workspace_path || s.selectedWorkspacePath,
        ...(s.activeSessionId === sessionId
          ? {
              selectedProviderId: resolvedModel.providerId,
              selectedModelId: resolvedModel.modelId,
            }
          : {}),
        ...(latestMeta?.status === 'running' && !st.running && (evs || []).length > 0
          ? {
              sessions: s.sessions.map((it) => (it.id === sessionId ? { ...it, status: 'idle' } : it)),
            }
          : {}),
        streams: { ...s.streams, [sessionId]: st },
      };
    });
    wsClient.sendEnvelope('client.subscribe', { session_id: sessionId, last_seen_seq: st.lastSeq });
    void get().refreshGitSummary(false);
  },

  startNewSession: (runnerId, workspacePath) => {
    persistActiveSessionId('');
    set((s) => {
      const resolvedDraft = resolveCatalogModel(s.models, s.draftProviderId, s.draftModelId);
      return {
        mainView: 'workspace',
        activeSessionId: '',
        selectedRunnerId: runnerId || s.selectedRunnerId,
        selectedWorkspacePath: workspacePath !== undefined ? workspacePath : s.selectedWorkspacePath,
        draftProviderId: resolvedDraft.providerId,
        draftModelId: resolvedDraft.modelId,
        selectedProviderId: resolvedDraft.providerId,
        selectedModelId: resolvedDraft.modelId,
      };
    });
  },

  renameSession: async (sessionId, newTitle) => {
    await apiFetch(`/api/v1/sessions/${sessionId}`, {
      method: 'PUT',
      body: JSON.stringify({ title: newTitle }),
    });
    await get().refreshInitialData();
  },

  deleteSession: async (sessionId) => {
    await apiFetch(`/api/v1/sessions/${sessionId}`, { method: 'DELETE' });
    if (get().activeSessionId === sessionId) {
      persistActiveSessionId('');
      set({ activeSessionId: '' });
    }
    await get().refreshInitialData();
  },

  forkSession: async (sessionId, opts) => {
    const res = await apiFetch<{ session?: SessionRecord; copied_events?: number }>(
      `/api/v1/sessions/${encodeURIComponent(sessionId)}/fork`,
      {
        method: 'POST',
        body: JSON.stringify({
          up_to_seq: opts?.upToSeq || 0,
          before_seq: opts?.beforeSeq || 0,
          title: opts?.title || '',
        }),
      },
    );
    const forked = res?.session;
    if (!forked?.id) return null;
    await get().refreshInitialData();
    await get().selectSession(forked.id);
    if (opts?.prefillText !== undefined) {
      set({ composerText: opts.prefillText });
    }
    return forked.id;
  },

  sendPromptOrSteer: (overrideText, mode = 'follow_up') => {
    const s = get();
    const text = (overrideText ?? s.composerText).trim();
    const attachments = overrideText === undefined ? s.composerAttachments : [];
    const activeModel = s.models.find(
      (m) => m.provider_id === s.selectedProviderId && m.id === s.selectedModelId,
    );
    if ((!text && attachments.length === 0) || !s.selectedRunnerId || !activeModel) return;

    let sessionId = s.activeSessionId;
    const currentStream = sessionId ? s.streams[sessionId] : undefined;

    if (sessionId && currentStream?.running) {
      if (mode === 'steer') {
        wsClient.sendEnvelope('session.steer', { text }, { session_id: sessionId, runner_id: s.selectedRunnerId });
      } else {
        const entry: QueuedFollowUp = {
          id: `fu_${crypto.randomUUID().slice(0, 8)}`,
          text,
          createdAt: Date.now(),
        };
        set((prev) => ({
          followUpQueues: {
            ...prev.followUpQueues,
            [sessionId]: [...(prev.followUpQueues[sessionId] || []), entry],
          },
        }));
      }
      if (overrideText === undefined) set({ composerText: '', composerAttachments: [] });
      return;
    }

    if (!sessionId) {
      sessionId = `sess_${crypto.randomUUID()}`;
      persistActiveSessionId(sessionId);
      set((prev) => ({
        mainView: 'workspace',
        activeSessionId: sessionId,
        sessionModels: {
          ...prev.sessionModels,
          [sessionId]: { providerId: activeModel.provider_id, modelId: activeModel.id },
        },
        streams: { ...prev.streams, [sessionId]: emptyStreamState() },
      }));
    } else {
      set((prev) => ({
        sessionModels: {
          ...prev.sessionModels,
          [sessionId]: { providerId: activeModel.provider_id, modelId: activeModel.id },
        },
      }));
    }

    const images =
      attachments.length > 0
        ? attachments.map((a) => ({
            type: 'image',
            mime_type: a.mimeType,
            data_b64: a.dataB64,
          }))
        : undefined;

    const effectiveThinkingEffort = resolveEffectiveEffort(activeModel, s.thinkingEffort);

    let effectivePermMode = s.permissionMode;
    let promptText = text;
    if (/^\/plan(?:\s+|$)/i.test(text)) {
      effectivePermMode = 'plan';
      promptText = text.replace(/^\/plan\s*/i, '').trim() || text;
      set({ permissionMode: 'plan' });
    }

    set((prev) => ({
      streams: {
        ...prev.streams,
        [sessionId]: appendOptimisticUserPrompt(prev.streams[sessionId], promptText, images),
      },
    }));

    hostBridge.haptic('light');
    wsClient.sendEnvelope(
      'session.prompt',
      {
        session_id: sessionId,
        runner_id: s.selectedRunnerId,
        workspace_path: s.selectedWorkspacePath || undefined,
        provider_id: s.selectedProviderId,
        model_id: s.selectedModelId,
        thinking_effort: effectiveThinkingEffort,
        permission_mode: effectivePermMode,
        text: promptText,
        images,
      },
      { session_id: sessionId, runner_id: s.selectedRunnerId },
    );
    if (overrideText === undefined) set({ composerText: '', composerAttachments: [] });
  },

  queueFollowUp: (text) => {
    get().sendPromptOrSteer(text, 'follow_up');
  },

  removeFollowUp: (sessionId, id) => {
    set((prev) => ({
      followUpQueues: {
        ...prev.followUpQueues,
        [sessionId]: (prev.followUpQueues[sessionId] || []).filter((item) => item.id !== id),
      },
    }));
  },

  promoteFollowUpToSteer: (sessionId, id) => {
    const s = get();
    const list = s.followUpQueues[sessionId] || [];
    const target = list.find((item) => item.id === id);
    if (!target) return;
    set((prev) => ({
      followUpQueues: {
        ...prev.followUpQueues,
        [sessionId]: (prev.followUpQueues[sessionId] || []).filter((item) => item.id !== id),
      },
    }));
    const cur = s.streams[sessionId];
    if (cur?.running) {
      wsClient.sendEnvelope('session.steer', { text: target.text }, { session_id: sessionId, runner_id: s.selectedRunnerId });
    } else {
      get().sendPromptOrSteer(target.text, 'steer');
    }
  },

  abortActiveSession: () => {
    const { activeSessionId, selectedRunnerId } = get();
    if (!activeSessionId) return;
    wsClient.sendEnvelope('session.abort', {}, { session_id: activeSessionId, runner_id: selectedRunnerId });
  },

  resolveApproval: (approvalId, approved) => {
    const { activeSessionId, selectedRunnerId } = get();
    hostBridge.haptic('light');
    wsClient.sendEnvelope(
      'approval.resolve',
      { approval_id: approvalId, approved },
      { session_id: activeSessionId, runner_id: selectedRunnerId },
    );
  },

  resolveUIPrompt: (requestId, confirmed, value, canceled = false) => {
    const { activeSessionId, selectedRunnerId, streams } = get();
    wsClient.sendEnvelope(
      'ui.resolve',
      { request_id: requestId, confirmed, value, canceled },
      { session_id: activeSessionId, runner_id: selectedRunnerId },
    );
    if (activeSessionId && streams[activeSessionId]) {
      set({
        streams: {
          ...streams,
          [activeSessionId]: { ...streams[activeSessionId], pendingUIPrompt: null },
        },
      });
    }
  },

  archiveSession: async (sessionId) => {
    await apiFetch(`/api/v1/sessions/${sessionId}/archive`, { method: 'POST' });
    if (get().activeSessionId === sessionId) {
      persistActiveSessionId('');
      set({ activeSessionId: '' });
    }
    await get().refreshInitialData();
  },

  unarchiveSession: async (sessionId) => {
    await apiFetch(`/api/v1/archives/${sessionId}/unarchive`, { method: 'POST' });
    await get().refreshInitialData();
  },

  deleteArchivedPermanently: async (sessionId) => {
    await apiFetch(`/api/v1/archives/${sessionId}`, { method: 'DELETE' });
    await get().refreshInitialData();
  },

  openSubagentTab: (data) => {
    const id = `subagent:${data.toolCallId}`;
    set((s) => {
      const wasOpen = s.sidePaneVisible;
      const existing = s.rightTabs.find((t) => t.id === id);
      if (existing) {
        return {
          rightTabs: s.rightTabs
            .filter((t) => wasOpen || t.kind !== 'new_tab')
            .map((t) => (t.id === id ? { ...t, title: data.title, subagent: data } : t)),
          activeRightTabId: id,
          sidePaneVisible: true,
        };
      }
      const next = insertConcreteRightTab(s.rightTabs, s.activeRightTabId, wasOpen, {
        id,
        kind: 'subagent',
        title: data.title,
        subagent: data,
        closable: true,
      });
      return { ...next, sidePaneVisible: true };
    });
  },

  openRightTab: async (kind, title) => {
    const wasOpen = get().sidePaneVisible;
    const existing = get().rightTabs.find(
      (t) => t.kind === kind && (kind === 'files' || kind === 'review' || kind === 'browser'),
    );
    if (existing) {
      set((s) => ({
        sidePaneVisible: true,
        activeRightTabId: existing.id,
        rightTabs: wasOpen ? s.rightTabs : s.rightTabs.filter((t) => t.kind !== 'new_tab'),
      }));
      if (kind === 'review') void get().refreshGitSummary(true);
      return;
    }

    const id = `tab_${crypto.randomUUID().slice(0, 8)}`;
    const defaultTitles: Record<WorkbenchTabKind, string> = {
      new_tab: formatMsg(get().locale, 'sidePane.newTab'),
      files: formatMsg(get().locale, 'fileManager.title'),
      file_preview: 'Preview',
      review: formatMsg(get().locale, 'sidePane.review'),
      terminal: formatMsg(get().locale, 'terminal.title'),
      side_chat: formatMsg(get().locale, 'sidePane.selectionChat'),
      subagent: 'Subagent',
      browser: formatMsg(get().locale, 'sidePane.browser'),
    };

    if (kind === 'new_tab') {
      const tab: WorkbenchTab = { id, kind: 'new_tab', title: title || defaultTitles.new_tab, closable: true };
      set((s) => ({ sidePaneVisible: true, rightTabs: [...s.rightTabs, tab], activeRightTabId: id }));
      return;
    }

    if (kind === 'terminal') {
      set({ sidePaneVisible: true });
      const rid = get().selectedRunnerId;
      let ptyId = '';
      try {
        const resp = await wsClient.requestRPC<{ cols: number; rows: number }, { pty_id: string }>(
          'pty.open',
          { cols: 90, rows: 28 },
          { runner_id: rid },
        );
        ptyId = resp.payload?.pty_id || '';
      } catch {
        // ignore
      }
      const tab: WorkbenchTab = { id, kind: 'terminal', title: title || defaultTitles.terminal, ptyId, closable: true };
      set((s) => insertConcreteRightTab(s.rightTabs, s.activeRightTabId, wasOpen, tab));
      return;
    }

    const tab: WorkbenchTab = {
      id,
      kind,
      title: title || defaultTitles[kind],
      sideSessionId: kind === 'side_chat' ? `sess_side_${crypto.randomUUID().slice(0, 8)}` : undefined,
      closable: true,
    };
    // Mount/activate tab immediately so UI transitions smoothly without waiting for git diff RPC!
    set((s) => ({
      sidePaneVisible: true,
      ...insertConcreteRightTab(s.rightTabs, s.activeRightTabId, wasOpen, tab),
    }));
    if (kind === 'review') {
      void get().refreshGitSummary(true);
    }
  },

  closeRightTab: (tabId) => {
    const tab = get().rightTabs.find((t) => t.id === tabId);
    if (tab?.kind === 'terminal' && tab.ptyId) {
      wsClient.sendEnvelope('pty.close', { pty_id: tab.ptyId }, { runner_id: get().selectedRunnerId });
    }
    set((s) => {
      const filtered = s.rightTabs.filter((t) => t.id !== tabId);
      if (filtered.length === 0) {
        return { rightTabs: [], activeRightTabId: '', sidePaneVisible: false };
      }
      const nextActive = s.activeRightTabId === tabId ? filtered[filtered.length - 1]?.id || '' : s.activeRightTabId;
      return { rightTabs: filtered, activeRightTabId: nextActive };
    });
  },

  setActiveRightTab: (tabId) => set({ activeRightTabId: tabId }),

  openFileInPreviewTab: async (relPath, line) => {
    const wasOpen = get().sidePaneVisible;
    const cleanRel = relPath.replace(/^\.\//, '');
    const existing = get().rightTabs.find((t) => t.kind === 'file_preview' && t.relPath === cleanRel);
    if (existing) {
      set((s) => ({
        sidePaneVisible: true,
        activeRightTabId: existing.id,
        rightTabs: (wasOpen ? s.rightTabs : s.rightTabs.filter((t) => t.kind !== 'new_tab')).map((t) =>
          typeof line === 'number' && t.id === existing.id ? { ...t, highlightLine: line } : t,
        ),
      }));
      return;
    }
    set({ sidePaneVisible: true });
    const resp = await wsClient.requestRPC<
      { action: string; rel_path: string; workspace_path?: string },
      FilePreview
    >(
      'workspace.req',
      {
        action: 'fs_read_preview',
        rel_path: cleanRel,
        workspace_path: get().selectedWorkspacePath || undefined,
      },
      { runner_id: get().selectedRunnerId },
    );
    const preview = resp.payload;
    if (!preview) return;
    const id = `prev_${crypto.randomUUID().slice(0, 8)}`;
    const tab: WorkbenchTab = {
      id,
      kind: 'file_preview',
      title: preview.name || cleanRel,
      relPath: cleanRel,
      highlightLine: line,
      preview,
      closable: true,
    };
    set((s) => insertConcreteRightTab(s.rightTabs, s.activeRightTabId, wasOpen, tab));
  },

  listWorkspaceDir: async (relPath) => {
    const rid = get().selectedRunnerId;
    if (!rid || !wsClient.connected) return [];
    const resp = await wsClient.requestRPC<
      { action: string; rel_path: string; workspace_path?: string },
      { entries: DirEntry[] }
    >(
      'workspace.req',
      {
        action: 'fs_list',
        rel_path: relPath,
        workspace_path: get().selectedWorkspacePath || undefined,
      },
      { runner_id: rid },
    );
    return resp.payload?.entries || [];
  },

  browseRunnerDirs: async (runnerId, dirPath = '', includeHidden = false) => {
    const rid = runnerId || get().selectedRunnerId;
    if (!rid) {
      throw new Error('No runner selected');
    }
    if (wsClient.connected) {
      const resp = await wsClient.requestRPC<
        { action: string; workspace_path?: string; include_hidden?: boolean },
        BrowseDirsResult
      >(
        'workspace.req',
        {
          action: 'fs_browse_dirs',
          workspace_path: dirPath || undefined,
          include_hidden: includeHidden,
        },
        { runner_id: rid },
      );
      if (resp.payload) return resp.payload;
    }
    const params = new URLSearchParams();
    if (dirPath) params.set('path', dirPath);
    if (includeHidden) params.set('include_hidden', '1');
    const qs = params.toString();
    return apiFetch<BrowseDirsResult>(`/api/v1/runners/${encodeURIComponent(rid)}/dirs${qs ? `?${qs}` : ''}`);
  },

  searchWorkspaceFiles: async (query) => {
    const rid = get().selectedRunnerId;
    if (!rid || !wsClient.connected) return [];
    const resp = await wsClient.requestRPC<
      { action: string; query: string; workspace_path?: string },
      { files: string[] }
    >(
      'workspace.req',
      {
        action: 'fs_search',
        query,
        workspace_path: get().selectedWorkspacePath || undefined,
      },
      { runner_id: rid },
    );
    return resp.payload?.files || [];
  },

  refreshGitSummary: async (includeDiffs = false) => {
    const rid = get().selectedRunnerId;
    if (!rid || !wsClient.connected) return null;
    if (gitSummaryInFlight && (gitSummaryInFlightDiffs || !includeDiffs)) {
      return gitSummaryInFlight;
    }
    if (includeDiffs) set({ gitDiffLoading: true });
    gitSummaryInFlightDiffs = includeDiffs;
    let req: Promise<GitSummary | null> | null = null;
    req = (async () => {
      try {
        const resp = await wsClient.requestRPC<{ action: string; workspace_path?: string }, GitSummary>(
          'workspace.req',
          {
            action: includeDiffs ? 'git_diff' : 'git_summary',
            workspace_path: get().selectedWorkspacePath || undefined,
          },
          { runner_id: rid },
        );
        if (resp.payload) {
          set({ gitSummary: resp.payload });
          return resp.payload;
        }
      } catch {
        // ignore
      } finally {
        if (includeDiffs) set({ gitDiffLoading: false });
        if (gitSummaryInFlight === req) {
          gitSummaryInFlight = null;
          gitSummaryInFlightDiffs = false;
        }
      }
      return null;
    })();
    gitSummaryInFlight = req;
    return req;
  },

  gitStage: async (relPath = '') => {
    const rid = get().selectedRunnerId;
    if (!rid || !wsClient.connected) return;
    const resp = await wsClient.requestRPC<
      { action: string; rel_path?: string; workspace_path?: string },
      GitSummary
    >(
      'workspace.req',
      {
        action: 'git_stage',
        rel_path: relPath || undefined,
        workspace_path: get().selectedWorkspacePath || undefined,
      },
      { runner_id: rid },
    );
    if (resp.payload) set({ gitSummary: resp.payload });
  },

  gitUnstage: async (relPath = '') => {
    const rid = get().selectedRunnerId;
    if (!rid || !wsClient.connected) return;
    const resp = await wsClient.requestRPC<
      { action: string; rel_path?: string; workspace_path?: string },
      GitSummary
    >(
      'workspace.req',
      {
        action: 'git_unstage',
        rel_path: relPath || undefined,
        workspace_path: get().selectedWorkspacePath || undefined,
      },
      { runner_id: rid },
    );
    if (resp.payload) set({ gitSummary: resp.payload });
  },

  gitDiscard: async (relPath) => {
    const rid = get().selectedRunnerId;
    if (!rid || !wsClient.connected || !relPath) return;
    const resp = await wsClient.requestRPC<
      { action: string; rel_path: string; workspace_path?: string },
      GitSummary
    >(
      'workspace.req',
      {
        action: 'git_discard',
        rel_path: relPath,
        workspace_path: get().selectedWorkspacePath || undefined,
      },
      { runner_id: rid },
    );
    if (resp.payload) set({ gitSummary: resp.payload });
  },

  gitCommit: async (message) => {
    const rid = get().selectedRunnerId;
    if (!rid || !wsClient.connected) return { ok: false, error: 'Runner offline' };
    try {
      const resp = await wsClient.requestRPC<
        { action: string; message: string; workspace_path?: string },
        { ok?: boolean; commit?: string; summary?: GitSummary; error?: string }
      >(
        'workspace.req',
        {
          action: 'git_commit',
          message,
          workspace_path: get().selectedWorkspacePath || undefined,
        },
        { runner_id: rid },
      );
      if (resp.type === 'error' || resp.payload?.error) {
        return { ok: false, error: resp.payload?.error || 'Commit failed' };
      }
      if (resp.payload?.summary) set({ gitSummary: resp.payload.summary });
      else void get().refreshGitSummary(true);
      return { ok: true, commit: resp.payload?.commit };
    } catch (err) {
      return { ok: false, error: err instanceof Error ? err.message : String(err) };
    }
  },

  gitListBranches: async () => {
    const rid = get().selectedRunnerId;
    if (!rid || !wsClient.connected) return { current: '', branches: [] };
    try {
      const resp = await wsClient.requestRPC<
        { action: string; workspace_path?: string },
        { current?: string; branches?: string[] }
      >(
        'workspace.req',
        {
          action: 'git_branches',
          workspace_path: get().selectedWorkspacePath || undefined,
        },
        { runner_id: rid },
      );
      return {
        current: resp.payload?.current || '',
        branches: resp.payload?.branches || [],
      };
    } catch {
      return { current: '', branches: [] };
    }
  },

  gitCheckout: async (branch, createBranch = false) => {
    const rid = get().selectedRunnerId;
    if (!rid || !wsClient.connected) return { ok: false, error: 'Runner offline' };
    try {
      const resp = await wsClient.requestRPC<
        { action: string; branch: string; create_branch?: boolean; workspace_path?: string },
        { ok?: boolean; summary?: GitSummary; error?: string }
      >(
        'workspace.req',
        {
          action: 'git_checkout',
          branch,
          create_branch: createBranch,
          workspace_path: get().selectedWorkspacePath || undefined,
        },
        { runner_id: rid },
      );
      if (resp.type === 'error' || resp.payload?.error) {
        return { ok: false, error: resp.payload?.error || 'Checkout failed' };
      }
      if (resp.payload?.summary) set({ gitSummary: resp.payload.summary });
      else void get().refreshGitSummary(true);
      return { ok: true };
    } catch (err) {
      return { ok: false, error: err instanceof Error ? err.message : String(err) };
    }
  },

  spawnBottomTerminal: async () => {
    const rid = get().selectedRunnerId;
    if (!rid || !wsClient.connected) return;
    const resp = await wsClient.requestRPC<{ cols: number; rows: number; title: string }, { pty_id: string; title: string }>(
      'pty.open',
      { cols: 100, rows: 18, title: 'Nex' },
      { runner_id: rid },
    );
    const ptyId = resp.payload?.pty_id;
    if (!ptyId) return;
    const id = `bterm_${ptyId.slice(0, 8)}`;
    set((s) => ({
      showBottomPanel: true,
      bottomTerminals: [...s.bottomTerminals, { id, title: 'Nex', ptyId, createdAt: Date.now() }],
      activeBottomTermId: id,
    }));
  },

  closeBottomTerminal: (id) => {
    const term = get().bottomTerminals.find((t) => t.id === id);
    if (term?.ptyId) {
      wsClient.sendEnvelope('pty.close', { pty_id: term.ptyId }, { runner_id: get().selectedRunnerId });
    }
    set((s) => {
      const rem = s.bottomTerminals.filter((t) => t.id !== id);
      return {
        bottomTerminals: rem,
        activeBottomTermId: s.activeBottomTermId === id ? rem[rem.length - 1]?.id || '' : s.activeBottomTermId,
        showBottomPanel: rem.length > 0 ? s.showBottomPanel : false,
      };
    });
  },

  searchSessions: async (query) => {
    const q = query.trim();
    try {
      const hits = await apiFetch<SessionSearchHit[]>(
        `/api/v1/sessions/search?q=${encodeURIComponent(q)}&limit=20`,
      );
      if (Array.isArray(hits)) return hits;
    } catch {
      // fallback to in-memory session title/workspace filter
    }
    const lower = q.toLowerCase();
    return get()
      .sessions.filter(
        (s) =>
          !lower ||
          s.title.toLowerCase().includes(lower) ||
          (s.workspace_path || '').toLowerCase().includes(lower),
      )
      .slice(0, 20)
      .map((s) => ({
        session: s,
        match_type: 'title' as const,
        snippet: s.workspace_path || '',
      }));
  },

  exportSessionMarkdown: async (sessionId) => {
    if (!sessionId) return null;
    let payload: { filename: string; markdown: string } | null = null;
    try {
      const res = await apiFetch<{ filename?: string; markdown?: string }>(
        `/api/v1/sessions/${encodeURIComponent(sessionId)}/export?format=json`,
      );
      if (res && typeof res.markdown === 'string') {
        payload = {
          filename: res.filename || `${sessionId}.md`,
          markdown: res.markdown,
        };
      }
    } catch {
      // client-side fallback from active stream items
    }
    if (!payload) {
      const sess = get().sessions.find((s) => s.id === sessionId);
      const stream = get().streams[sessionId];
      const title = sess?.title || 'Nex Conversation';
      const lines: string[] = [`# ${title}`, ''];
      if (sess?.model_id) lines.push(`- **Model**: \`${sess.model_id}\``);
      if (sess?.workspace_path) lines.push(`- **Workspace**: \`${sess.workspace_path}\``);
      lines.push('', '---', '');
      for (const item of stream?.items || []) {
        lines.push(item.role === 'user' ? '## User' : '## Assistant', '');
        if (item.thinking) {
          lines.push('<details>', '<summary>Thinking</summary>', '', item.thinking, '', '</details>', '');
        }
        if (item.text) lines.push(item.text, '');
      }
      const safeName = title.replace(/[/\\:*?"<>|]/g, '-').trim().slice(0, 64) || sessionId;
      payload = { filename: `${safeName}.md`, markdown: lines.join('\n') };
    }

    if (typeof document !== 'undefined' && typeof Blob !== 'undefined' && typeof URL !== 'undefined') {
      try {
        const blob = new Blob([payload.markdown], { type: 'text/markdown;charset=utf-8' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = payload.filename;
        document.body.appendChild(a);
        a.click();
        a.remove();
        window.setTimeout(() => URL.revokeObjectURL(url), 1000);
      } catch {
        // ignore download error in headless test envs
      }
    }
    return payload;
  },

  rewindSessionFiles: async (sessionId, beforeSeq) => {
    if (!sessionId) return null;
    try {
      const res = await apiFetch<RewindResult>(
        `/api/v1/sessions/${encodeURIComponent(sessionId)}/rewind`,
        {
          method: 'POST',
          body: JSON.stringify({ before_seq: beforeSeq }),
        },
      );
      void get().refreshGitSummary(false);
      return res;
    } catch {
      return null;
    }
  },

  consolidateMemories: async (opts) => {
    const res = await apiFetch<{
      before_count: number;
      after_count: number;
      summary: string;
      memories: MemoryRecord[];
    }>('/api/v1/memories/consolidate', {
      method: 'POST',
      body: JSON.stringify({
        provider_id: opts?.providerId || undefined,
        model_id: opts?.modelId || undefined,
      }),
    });
    if (res && Array.isArray(res.memories)) {
      set({ memories: res.memories });
    } else {
      await get().refreshRegistries();
    }
    return res;
  },

  openBrowserTab: () => {
    set((s) => {
      const wasOpen = s.sidePaneVisible;
      const existing = s.rightTabs.find((t) => t.kind === 'browser');
      if (existing) {
        return {
          sidePaneVisible: true,
          activeRightTabId: existing.id,
          rightTabs: wasOpen ? s.rightTabs : s.rightTabs.filter((t) => t.kind !== 'new_tab'),
        };
      }
      const tab: WorkbenchTab = {
        id: 'tab_browser',
        kind: 'browser',
        title: formatMsg(s.locale, 'sidePane.browser'),
        closable: true,
      };
      return {
        sidePaneVisible: true,
        ...insertConcreteRightTab(s.rightTabs, s.activeRightTabId, wasOpen, tab),
      };
    });
  },

  sendBrowserControl: async (req) => {
    const rid = get().selectedRunnerId;
    const sid = req.session_id || get().activeSessionId || 'default';
    if (!rid || !wsClient.connected) return null;
    try {
      const resp = await wsClient.requestRPC<typeof req & { session_id: string }, BrowserSessionState>(
        'browser.control',
        { ...req, session_id: sid },
        { runner_id: rid, session_id: sid },
      );
      if (resp.payload) {
        const st = resp.payload;
        const targetSid = st.session_id || sid;
        set((s) => ({
          browserStates: { ...s.browserStates, [targetSid]: st },
        }));
        return st;
      }
    } catch {
      // ignore transient RPC errors
    }
    return null;
  },

  sendBrowserInput: (ev) => {
    const rid = get().selectedRunnerId;
    const sid = ev.session_id || get().activeSessionId || 'default';
    if (!rid || !wsClient.connected) return;
    wsClient.sendEnvelope('browser.input', { ...ev, session_id: sid }, { runner_id: rid, session_id: sid });
  },

  resolveBrowserHandoff: (sessionId, tabId, resumed) => {
    const rid = get().selectedRunnerId;
    if (!rid || !wsClient.connected) return;
    wsClient.sendEnvelope(
      'browser.handoff',
      { session_id: sessionId, tab_id: tabId, state: resumed ? 'resumed' : 'cancelled', resumed },
      { runner_id: rid, session_id: sessionId },
    );
    set((s) => {
      const prev = s.browserStates[sessionId];
      if (!prev) return {};
      return {
        browserStates: {
          ...s.browserStates,
          [sessionId]: { ...prev, handoff: null, human_control: false },
        },
      };
    });
  },

  probeRunnerBrowser: async (runnerId) => {
    if (!runnerId) return null;
    try {
      const resp = await wsClient.requestRPC<Record<string, never>, BrowserProbeResult>(
        'browser.probe',
        {},
        { runner_id: runnerId },
      );
      if (resp.payload) {
        set((s) => ({ browserProbes: { ...s.browserProbes, [runnerId]: resp.payload! } }));
        return resp.payload;
      }
    } catch (err) {
      const errRes: BrowserProbeResult = { status: 'error', error: String(err) };
      set((s) => ({ browserProbes: { ...s.browserProbes, [runnerId]: errRes } }));
      return errRes;
    }
    return null;
  },

  installRunnerBrowser: async (runnerId, mirror) => {
    if (!runnerId) return null;
    set((s) => ({ browserInstalling: { ...s.browserInstalling, [runnerId]: true } }));
    try {
      const resp = await wsClient.requestRPC<{ mirror?: string }, BrowserProbeResult>(
        'browser.install',
        { mirror: mirror || undefined },
        { runner_id: runnerId },
        300000,
      );
      if (resp.payload) {
        set((s) => ({ browserProbes: { ...s.browserProbes, [runnerId]: resp.payload! } }));
        return resp.payload;
      }
    } catch (err) {
      const errRes: BrowserProbeResult = { status: 'error', error: String(err) };
      set((s) => ({ browserProbes: { ...s.browserProbes, [runnerId]: errRes } }));
      return errRes;
    } finally {
      set((s) => ({ browserInstalling: { ...s.browserInstalling, [runnerId]: false } }));
    }
    return null;
  },

  clearRunnerBrowserData: async (runnerId, mode) => {
    if (!runnerId) return false;
    try {
      await wsClient.requestRPC('browser.clear_data', { mode }, { runner_id: runnerId });
      return true;
    } catch {
      return false;
    }
  },
}));
