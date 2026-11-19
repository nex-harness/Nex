import { create } from 'zustand';
import { hostBridge } from '../host/bridge';
import { apiFetch } from './protocol';
import { applySessionEvent, emptyStreamState, RawSessionEventRow, SessionStreamState } from './reducer';
import {
  AgentProfile,
  CronJobRecord,
  DirEntry,
  FilePreview,
  GitSummary,
  MCPServerRecord,
  ModelMeta,
  PluginRecord,
  ProviderRecord,
  RunnerRecord,
  SessionRecord,
  SettingsSectionId,
  SkillRecord,
  TurnTelemetry,
  UsageSummary,
  UserPreferences,
  WorkbenchTab,
  WorkbenchTabKind,
} from './types';
import { wsClient } from './wsClient';

export function applyDocumentTheme(theme: UserPreferences['theme']): void {
  if (typeof document === 'undefined') return;
  const prefersDark =
    typeof window !== 'undefined' && typeof window.matchMedia === 'function'
      ? window.matchMedia('(prefers-color-scheme: dark)').matches
      : true;
  const isDark = theme === 'dark' || (theme === 'system' && prefersDark);
  document.documentElement.classList.toggle('dark', isDark);
}

export interface AppState {
  wsConnected: boolean;
  locale: 'zh-CN' | 'en-US';
  mainView: 'workspace' | 'settings';
  settingsSection: SettingsSectionId;
  sidebarVisible: boolean;
  sidePaneVisible: boolean;
  showBottomPanel: boolean;
  quickSearchOpen: boolean;
  pinnedSessionIds: Record<string, boolean>;

  runners: RunnerRecord[];
  providers: ProviderRecord[];
  models: ModelMeta[];
  preferences: UserPreferences;
  sessions: SessionRecord[];
  archivedSessions: SessionRecord[];
  agentProfiles: AgentProfile[];
  skills: SkillRecord[];
  plugins: PluginRecord[];
  mcpServers: MCPServerRecord[];
  cronJobs: CronJobRecord[];
  todaySummary: UsageSummary | null;

  selectedRunnerId: string;
  selectedWorkspacePath: string;
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
  bottomTerminals: Array<{ id: string; title: string; ptyId: string; createdAt: number }>;
  activeBottomTermId: string;

  openSettings: (section?: SettingsSectionId) => void;
  closeSettings: () => void;
  setLocale: (loc: 'zh-CN' | 'en-US') => void;
  togglePinSession: (sessionId: string) => void;
  toggleSidebar: () => void;
  toggleSidePane: () => void;
  toggleBottomPanel: () => void;
  setQuickSearchOpen: (open: boolean) => void;

  setComposerText: (text: string) => void;
  appendComposerRef: (refText: string) => void;
  setSelectedRunner: (id: string, workspacePath?: string) => void;
  setSelectedModel: (providerId: string, modelId: string) => void;
  setThinkingEffort: (effort: '' | 'low' | 'medium' | 'high') => void;
  setPermissionMode: (mode: 'full_access' | 'interactive' | 'read_only') => void;
  updateThemeQuick: (theme: UserPreferences['theme']) => Promise<void>;

  bootstrap: () => Promise<void>;
  refreshInitialData: () => Promise<void>;
  refreshRegistries: () => Promise<void>;
  selectSession: (sessionId: string) => Promise<void>;
  startNewSession: (runnerId?: string, workspacePath?: string) => void;
  renameSession: (sessionId: string, newTitle: string) => Promise<void>;
  deleteSession: (sessionId: string) => Promise<void>;
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
  theme: 'light',
  code_theme: 'github-light',
  font_family: 'Inter, -apple-system, BlinkMacSystemFont, "PingFang SC", sans-serif',
  mono_font_family: 'JetBrains Mono, SFMono-Regular, Menlo, monospace',
  expand_thinking: false,
  auto_archive_after_days: 30,
};

export const useNexStore = create<AppState>((set, get) => ({
  wsConnected: false,
  locale: ((typeof localStorage !== 'undefined' && localStorage.getItem('nex.locale')) as 'zh-CN' | 'en-US') || 'zh-CN',
  mainView: 'workspace',
  settingsSection: 'general',
  sidebarVisible: true,
  sidePaneVisible: false,
  showBottomPanel: false,
  quickSearchOpen: false,
  pinnedSessionIds: (() => {
    try {
      return JSON.parse(localStorage.getItem('nex.pinned.sessions') || '{}');
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
  skills: [],
  plugins: [],
  mcpServers: [],
  cronJobs: [],
  todaySummary: null,

  selectedRunnerId: '',
  selectedWorkspacePath: '',
  selectedProviderId: '',
  selectedModelId: '',
  thinkingEffort: 'low',
  permissionMode: 'full_access',
  activeSessionId: '',
  composerText: '',

  streams: {},
  gitSummary: null,

  rightTabs: [{ id: 'tab_launcher', kind: 'new_tab', title: '新标签页', closable: true }],
  activeRightTabId: 'tab_launcher',
  bottomTerminals: [],
  activeBottomTermId: '',

  openSettings: (section) => {
    set((s) => ({ mainView: 'settings', settingsSection: section || s.settingsSection }));
    void get().refreshRegistries();
  },
  closeSettings: () => set({ mainView: 'workspace' }),
  setLocale: (loc) => {
    try {
      localStorage.setItem('nex.locale', loc);
    } catch {
      // ignore
    }
    set({ locale: loc });
  },
  togglePinSession: (sessionId) => {
    set((s) => {
      const next = { ...s.pinnedSessionIds };
      if (next[sessionId]) delete next[sessionId];
      else next[sessionId] = true;
      try {
        localStorage.setItem('nex.pinned.sessions', JSON.stringify(next));
      } catch {
        // ignore
      }
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
          title: s.locale === 'en-US' ? 'New tab' : '新标签页',
          closable: true,
        };
        return { sidePaneVisible: true, rightTabs: [launcherTab], activeRightTabId: launcherTab.id };
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

  setComposerText: (text) => set({ composerText: text }),
  appendComposerRef: (refText) =>
    set((s) => ({ composerText: s.composerText ? `${s.composerText} ${refText} ` : `${refText} ` })),
  setSelectedRunner: (id, workspacePath) => {
    const r = get().runners.find((it) => it.id === id);
    set({
      selectedRunnerId: id,
      selectedWorkspacePath: workspacePath ?? r?.workspace_default ?? '',
    });
    void get().refreshGitSummary(false);
  },
  setSelectedModel: (providerId, modelId) => set({ selectedProviderId: providerId, selectedModelId: modelId }),
  setThinkingEffort: (effort) => set({ thinkingEffort: effort }),
  setPermissionMode: (mode) => set({ permissionMode: mode }),

  updateThemeQuick: async (theme) => {
    const nextPrefs = { ...get().preferences, theme };
    applyDocumentTheme(theme);
    set({ preferences: nextPrefs });
    await apiFetch('/api/v1/preferences', { method: 'PUT', body: JSON.stringify(nextPrefs) }).catch(() => {});
  },

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

      set((state) => {
        const nextStreams = { ...state.streams };
        for (const [sid, group] of batch) {
          let cur = nextStreams[sid] || emptyStreamState();
          const prevApproval = cur.pendingApproval;
          for (const raw of group.rows) {
            cur = applySessionEvent(cur, group.runnerId, raw);
            if (raw.event_type === 'tool_execution_end') shouldRefreshGit = true;
            if (raw.event_type === 'agent_end') {
              shouldRefreshGit = true;
              shouldRefreshData = true;
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
        return { streams: nextStreams };
      });

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
      }
    });

    wsClient.connect();
    await get().refreshInitialData();
    await get().refreshRegistries();
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
      const customModel = (models || []).find((m) => m.source === 'custom') || (models || [])[0];
      return {
        runners: runners || [],
        providers: providers || [],
        models: models || [],
        preferences: prefs || DEFAULT_PREFS,
        sessions: sessions || [],
        archivedSessions: archives || [],
        todaySummary,
        selectedRunnerId: nextRunner,
        selectedWorkspacePath: state.selectedWorkspacePath || chosenRunnerObj?.workspace_default || '',
        selectedProviderId: state.selectedProviderId || customModel?.provider_id || '',
        selectedModelId: state.selectedModelId || customModel?.id || '',
      };
    });
  },

  refreshRegistries: async () => {
    const [agentProfiles, skills, plugins, mcpServers, cronJobs] = await Promise.all([
      apiFetch<AgentProfile[]>('/api/v1/agents').catch(() => []),
      apiFetch<SkillRecord[]>('/api/v1/skills').catch(() => []),
      apiFetch<PluginRecord[]>('/api/v1/plugins').catch(() => []),
      apiFetch<MCPServerRecord[]>('/api/v1/mcp').catch(() => []),
      apiFetch<CronJobRecord[]>('/api/v1/cron').catch(() => []),
    ]);
    set({
      agentProfiles: agentProfiles || [],
      skills: skills || [],
      plugins: plugins || [],
      mcpServers: mcpServers || [],
      cronJobs: cronJobs || [],
    });
  },

  selectSession: async (sessionId) => {
    set({ mainView: 'workspace', activeSessionId: sessionId });
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
    if (tels && tels.length > 0) {
      st.latestTelemetry = tels[tels.length - 1];
    }
    set((s) => ({
      selectedRunnerId: runnerId || s.selectedRunnerId,
      selectedWorkspacePath: meta?.workspace_path || s.selectedWorkspacePath,
      streams: { ...s.streams, [sessionId]: st },
    }));
    wsClient.sendEnvelope('client.subscribe', { session_id: sessionId, last_seen_seq: st.lastSeq });
    void get().refreshGitSummary(false);
  },

  startNewSession: (runnerId, workspacePath) => {
    set((s) => ({
      mainView: 'workspace',
      activeSessionId: '',
      selectedRunnerId: runnerId || s.selectedRunnerId,
      selectedWorkspacePath: workspacePath !== undefined ? workspacePath : s.selectedWorkspacePath,
    }));
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
    if (get().activeSessionId === sessionId) set({ activeSessionId: '' });
    await get().refreshInitialData();
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
        mainView: 'workspace',
        activeSessionId: sessionId,
        streams: { ...prev.streams, [sessionId]: emptyStreamState() },
      }));
    }

    wsClient.sendEnvelope(
      'session.prompt',
      {
        session_id: sessionId,
        runner_id: s.selectedRunnerId,
        workspace_path: s.selectedWorkspacePath || undefined,
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
    set({ sidePaneVisible: true });
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
        // ignore
      }
      const tab: WorkbenchTab = { id, kind: 'terminal', title: title || 'Terminal', ptyId, closable: true };
      set((s) => ({ rightTabs: [...s.rightTabs, tab], activeRightTabId: id }));
      return;
    }

    if (kind === 'review') {
      await get().refreshGitSummary(true);
    }

    const defaultTitles: Record<WorkbenchTabKind, string> = {
      new_tab: get().locale === 'en-US' ? 'New tab' : '新标签页',
      files: get().locale === 'en-US' ? 'Files' : '文件',
      file_preview: 'Preview',
      review: get().locale === 'en-US' ? 'Review' : '审查',
      terminal: get().locale === 'en-US' ? 'Terminal' : '终端',
      side_chat: get().locale === 'en-US' ? 'Side conversation' : '辅助对话',
    };
    const tab: WorkbenchTab = {
      id,
      kind,
      title: title || defaultTitles[kind],
      sideSessionId: kind === 'side_chat' ? `sess_side_${crypto.randomUUID().slice(0, 8)}` : undefined,
      closable: true,
    };
    // If active tab is a blank 'new_tab' launcher and user picked a concrete app, replace the launcher tab cleanly!
    set((s) => {
      const activeIdx = s.rightTabs.findIndex((t) => t.id === s.activeRightTabId);
      if (kind !== 'new_tab' && activeIdx >= 0 && s.rightTabs[activeIdx].kind === 'new_tab') {
        const nextTabs = [...s.rightTabs];
        nextTabs[activeIdx] = tab;
        return { rightTabs: nextTabs, activeRightTabId: id };
      }
      return { rightTabs: [...s.rightTabs, tab], activeRightTabId: id };
    });
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
    set({ sidePaneVisible: true });
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
      // ignore
    }
    return null;
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
}));
