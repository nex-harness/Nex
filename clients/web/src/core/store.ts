import { create } from 'zustand';
import { hostBridge } from '../host/bridge';
import { apiFetch } from './protocol';
import { applySessionEvent, emptyStreamState, RawSessionEventRow, SessionStreamState } from './reducer';
import {
  DirEntry,
  FilePreview,
  GitSummary,
  ModelMeta,
  ProviderRecord,
  RunnerRecord,
  SessionRecord,
  TurnTelemetry,
  UserPreferences,
  WorkbenchTab,
  WorkbenchTabKind,
} from './types';
import { wsClient } from './wsClient';

export interface AppState {
  wsConnected: boolean;
  runners: RunnerRecord[];
  providers: ProviderRecord[];
  models: ModelMeta[];
  preferences: UserPreferences;
  sessions: SessionRecord[];
  archivedSessions: SessionRecord[];

  selectedRunnerId: string;
  selectedProviderId: string;
  selectedModelId: string;
  thinkingEffort: '' | 'low' | 'medium' | 'high';
  permissionMode: 'full_access' | 'interactive' | 'read_only';
  activeSessionId: string;
  composerText: string;

  streams: Record<string, SessionStreamState>;
  gitSummary: GitSummary | null;

  rightTabs: WorkbenchTab[];
  activeRightTabId: string;
  bottomTerminals: Array<{ id: string; title: string; ptyId: string }>;
  activeBottomTermId: string;
  showBottomPanel: boolean;

  setComposerText: (text: string) => void;
  appendComposerRef: (refText: string) => void;
  setSelectedRunner: (id: string) => void;
  setSelectedModel: (providerId: string, modelId: string) => void;
  setThinkingEffort: (effort: '' | 'low' | 'medium' | 'high') => void;
  setPermissionMode: (mode: 'full_access' | 'interactive' | 'read_only') => void;

  bootstrap: () => Promise<void>;
  refreshInitialData: () => Promise<void>;
  selectSession: (sessionId: string) => Promise<void>;
  startNewSession: () => void;
  sendPromptOrSteer: (overrideText?: string) => void;
  abortActiveSession: () => void;
  resolveApproval: (approvalId: string, approved: boolean) => void;
  resolveUIPrompt: (requestId: string, confirmed: boolean, value: string, canceled?: boolean) => void;

  archiveSession: (sessionId: string) => Promise<void>;
  unarchiveSession: (sessionId: string) => Promise<void>;
  deleteArchivedPermanently: (sessionId: string) => Promise<void>;

  openRightTab: (kind: WorkbenchTabKind, title?: string) => Promise<void>;
  closeRightTab: (tabId: string) => void;
  setActiveRightTab: (tabId: string) => void;
  openFileInPreviewTab: (relPath: string) => Promise<void>;
  listWorkspaceDir: (relPath: string) => Promise<DirEntry[]>;
  searchWorkspaceFiles: (query: string) => Promise<string[]>;
  refreshGitSummary: (includeDiffs?: boolean) => Promise<GitSummary | null>;
  spawnBottomTerminal: () => Promise<void>;
  closeBottomTerminal: (id: string) => void;
}

const DEFAULT_PREFS: UserPreferences = {
  theme: 'dark',
  code_theme: 'github-dark',
  font_family: 'Inter, system-ui, sans-serif',
  mono_font_family: 'JetBrains Mono, Menlo, monospace',
  expand_thinking: false,
  auto_archive_after_days: 30,
};

export const useNexStore = create<AppState>((set, get) => ({
  wsConnected: false,
  runners: [],
  providers: [],
  models: [],
  preferences: DEFAULT_PREFS,
  sessions: [],
  archivedSessions: [],

  selectedRunnerId: '',
  selectedProviderId: '',
  selectedModelId: '',
  thinkingEffort: 'low',
  permissionMode: 'full_access',
  activeSessionId: '',
  composerText: '',

  streams: {},
  gitSummary: null,

  rightTabs: [{ id: 'tab_files', kind: 'files', title: 'Files', closable: true }],
  activeRightTabId: 'tab_files',
  bottomTerminals: [],
  activeBottomTermId: '',
  showBottomPanel: true,

  setComposerText: (text) => set({ composerText: text }),
  appendComposerRef: (refText) =>
    set((s) => ({ composerText: s.composerText ? `${s.composerText} ${refText} ` : `${refText} ` })),
  setSelectedRunner: (id) => {
    set({ selectedRunnerId: id });
    void get().refreshGitSummary(false);
  },
  setSelectedModel: (providerId, modelId) => set({ selectedProviderId: providerId, selectedModelId: modelId }),
  setThinkingEffort: (effort) => set({ thinkingEffort: effort }),
  setPermissionMode: (mode) => set({ permissionMode: mode }),

  bootstrap: async () => {
    hostBridge.setDeviceFontScale(hostBridge.getDeviceFontScale());

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

    wsClient.onFrame((env) => {
      if (env.type === 'runner.status') {
        void get().refreshInitialData();
        return;
      }
      if (env.type === 'session.event' && env.payload) {
        const raw = env.payload as RawSessionEventRow;
        const sid = raw.session_id || env.session_id || '';
        if (!sid) return;
        const rid = env.runner_id || get().selectedRunnerId;
        set((state) => {
          const prev = state.streams[sid] || emptyStreamState();
          const updated = applySessionEvent(prev, rid, raw);
          if (updated.pendingApproval && updated.pendingApproval !== prev.pendingApproval) {
            void hostBridge.notifyApprovalRequested({
              sessionId: sid,
              runnerName: rid,
              toolName: updated.pendingApproval.toolName,
            });
          }
          return { streams: { ...state.streams, [sid]: updated } };
        });
        if (raw.event_type === 'tool_execution_end' || raw.event_type === 'agent_end') {
          void get().refreshGitSummary(false);
          if (raw.event_type === 'agent_end') {
            void apiFetch<SessionRecord[]>('/api/v1/sessions').then((list) => set({ sessions: list || [] })).catch(() => {});
          }
        }
      }
    });

    wsClient.connect();
    await get().refreshInitialData();
  },

  refreshInitialData: async () => {
    const [runners, providers, models, prefs, sessions, archives] = await Promise.all([
      apiFetch<RunnerRecord[]>('/api/v1/runners').catch(() => []),
      apiFetch<ProviderRecord[]>('/api/v1/providers').catch(() => []),
      apiFetch<ModelMeta[]>('/api/v1/models').catch(() => []),
      apiFetch<UserPreferences>('/api/v1/preferences').catch(() => DEFAULT_PREFS),
      apiFetch<SessionRecord[]>('/api/v1/sessions').catch(() => []),
      apiFetch<SessionRecord[]>('/api/v1/archives').catch(() => []),
    ]);

    if (typeof document !== 'undefined') {
      document.documentElement.classList.toggle('dark', prefs.theme !== 'light');
      if (prefs.font_family) document.documentElement.style.setProperty('--nex-font-sans', prefs.font_family);
      if (prefs.mono_font_family) document.documentElement.style.setProperty('--nex-font-mono', prefs.mono_font_family);
    }

    set((state) => {
      const onlineRunner = (runners || []).find((r) => r.status === 'online') || (runners || [])[0];
      const nextRunner = state.selectedRunnerId || onlineRunner?.id || '';
      const firstModel = (models || [])[0];
      return {
        runners: runners || [],
        providers: providers || [],
        models: models || [],
        preferences: prefs || DEFAULT_PREFS,
        sessions: sessions || [],
        archivedSessions: archives || [],
        selectedRunnerId: nextRunner,
        selectedProviderId: state.selectedProviderId || firstModel?.provider_id || '',
        selectedModelId: state.selectedModelId || firstModel?.id || '',
      };
    });
  },

  selectSession: async (sessionId) => {
    set({ activeSessionId: sessionId });
    const [evs, tels] = await Promise.all([
      apiFetch<RawSessionEventRow[]>(`/api/v1/sessions/${sessionId}/events`).catch(() => []),
      apiFetch<TurnTelemetry[]>(`/api/v1/sessions/${sessionId}/telemetry`).catch(() => []),
    ]);
    const runnerId = get().sessions.find((s) => s.id === sessionId)?.runner_id || get().selectedRunnerId;
    let st = emptyStreamState();
    for (const ev of evs || []) {
      st = applySessionEvent(st, runnerId, ev);
    }
    if (tels && tels.length > 0) {
      st.latestTelemetry = tels[tels.length - 1];
    }
    set((s) => ({
      selectedRunnerId: runnerId || s.selectedRunnerId,
      streams: { ...s.streams, [sessionId]: st },
    }));
    wsClient.sendEnvelope('client.subscribe', { session_id: sessionId, last_seen_seq: st.lastSeq });
  },

  startNewSession: () => {
    set({ activeSessionId: '' });
  },

  sendPromptOrSteer: (overrideText) => {
    const s = get();
    const text = (overrideText ?? s.composerText).trim();
    if (!text || !s.selectedRunnerId) return;

    let sessionId = s.activeSessionId;
    const currentStream = sessionId ? s.streams[sessionId] : undefined;

    if (sessionId && currentStream?.running) {
      wsClient.sendEnvelope('session.steer', { text }, { session_id: sessionId, runner_id: s.selectedRunnerId });
      if (overrideText === undefined) set({ composerText: '' });
      return;
    }

    if (!sessionId) {
      sessionId = `sess_${crypto.randomUUID()}`;
      set((prev) => ({
        activeSessionId: sessionId,
        streams: { ...prev.streams, [sessionId]: emptyStreamState() },
      }));
    }

    wsClient.sendEnvelope(
      'session.prompt',
      {
        session_id: sessionId,
        runner_id: s.selectedRunnerId,
        provider_id: s.selectedProviderId,
        model_id: s.selectedModelId,
        thinking_effort: s.thinkingEffort,
        permission_mode: s.permissionMode,
        text,
      },
      { session_id: sessionId, runner_id: s.selectedRunnerId },
    );
    if (overrideText === undefined) set({ composerText: '' });
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
    if (get().activeSessionId === sessionId) set({ activeSessionId: '' });
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

  openRightTab: async (kind, title) => {
    const existing = get().rightTabs.find((t) => t.kind === kind && (kind === 'files' || kind === 'review'));
    if (existing) {
      set({ activeRightTabId: existing.id });
      if (kind === 'review') await get().refreshGitSummary(true);
      return;
    }

    const id = `tab_${crypto.randomUUID().slice(0, 8)}`;
    if (kind === 'terminal') {
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
        // fallback empty ptyId until connected
      }
      const tab: WorkbenchTab = { id, kind: 'terminal', title: title || 'Terminal', ptyId, closable: true };
      set((s) => ({ rightTabs: [...s.rightTabs, tab], activeRightTabId: id }));
      return;
    }

    if (kind === 'review') {
      await get().refreshGitSummary(true);
    }

    const defaultTitles: Record<WorkbenchTabKind, string> = {
      files: 'Files',
      file_preview: 'Preview',
      review: 'Review',
      terminal: 'Terminal',
      side_chat: 'Side conversation',
    };
    const tab: WorkbenchTab = {
      id,
      kind,
      title: title || defaultTitles[kind],
      sideSessionId: kind === 'side_chat' ? `sess_side_${crypto.randomUUID().slice(0, 8)}` : undefined,
      closable: true,
    };
    set((s) => ({ rightTabs: [...s.rightTabs, tab], activeRightTabId: id }));
  },

  closeRightTab: (tabId) => {
    const tab = get().rightTabs.find((t) => t.id === tabId);
    if (tab?.kind === 'terminal' && tab.ptyId) {
      wsClient.sendEnvelope('pty.close', { pty_id: tab.ptyId }, { runner_id: get().selectedRunnerId });
    }
    set((s) => {
      const filtered = s.rightTabs.filter((t) => t.id !== tabId);
      const nextActive = s.activeRightTabId === tabId ? filtered[filtered.length - 1]?.id || '' : s.activeRightTabId;
      return { rightTabs: filtered, activeRightTabId: nextActive };
    });
  },

  setActiveRightTab: (tabId) => set({ activeRightTabId: tabId }),

  openFileInPreviewTab: async (relPath) => {
    const existing = get().rightTabs.find((t) => t.kind === 'file_preview' && t.relPath === relPath);
    if (existing) {
      set({ activeRightTabId: existing.id });
      return;
    }
    const resp = await wsClient.requestRPC<{ action: string; rel_path: string }, FilePreview>(
      'workspace.req',
      { action: 'fs_read_preview', rel_path: relPath },
      { runner_id: get().selectedRunnerId },
    );
    const preview = resp.payload;
    if (!preview) return;
    const id = `prev_${crypto.randomUUID().slice(0, 8)}`;
    const tab: WorkbenchTab = {
      id,
      kind: 'file_preview',
      title: preview.name || relPath,
      relPath,
      preview,
      closable: true,
    };
    set((s) => ({ rightTabs: [...s.rightTabs, tab], activeRightTabId: id }));
  },

  listWorkspaceDir: async (relPath) => {
    const rid = get().selectedRunnerId;
    if (!rid || !wsClient.connected) return [];
    const resp = await wsClient.requestRPC<{ action: string; rel_path: string }, { entries: DirEntry[] }>(
      'workspace.req',
      { action: 'fs_list', rel_path: relPath },
      { runner_id: rid },
    );
    return resp.payload?.entries || [];
  },

  searchWorkspaceFiles: async (query) => {
    const rid = get().selectedRunnerId;
    if (!rid || !wsClient.connected) return [];
    const resp = await wsClient.requestRPC<{ action: string; query: string }, { files: string[] }>(
      'workspace.req',
      { action: 'fs_search', query },
      { runner_id: rid },
    );
    return resp.payload?.files || [];
  },

  refreshGitSummary: async (includeDiffs = false) => {
    const rid = get().selectedRunnerId;
    if (!rid || !wsClient.connected) return null;
    try {
      const resp = await wsClient.requestRPC<{ action: string }, GitSummary>(
        'workspace.req',
        { action: includeDiffs ? 'git_diff' : 'git_summary' },
        { runner_id: rid },
      );
      if (resp.payload) {
        set({ gitSummary: resp.payload });
        return resp.payload;
      }
    } catch {
      // ignore offline
    }
    return null;
  },

  spawnBottomTerminal: async () => {
    const rid = get().selectedRunnerId;
    if (!rid || !wsClient.connected) return;
    const resp = await wsClient.requestRPC<{ cols: number; rows: number; title: string }, { pty_id: string; title: string }>(
      'pty.open',
      { cols: 100, rows: 20, title: 'Nex' },
      { runner_id: rid },
    );
    const ptyId = resp.payload?.pty_id;
    if (!ptyId) return;
    const id = `bterm_${ptyId.slice(0, 8)}`;
    set((s) => ({
      showBottomPanel: true,
      bottomTerminals: [...s.bottomTerminals, { id, title: 'Nex', ptyId }],
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
      };
    });
  },
}));
