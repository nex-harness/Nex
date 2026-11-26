import {
  Archive,
  ArrowLeft,
  ArrowRight,
  Blocks,
  CalendarClock,
  Check,
  ChevronRight,
  CirclePlus,
  CloudOff,
  Folder,
  PanelLeftClose,
  Pencil,
  Pin,
  Plus,
  Search,
  Settings,
  X,
} from 'lucide-react';
import { useMemo, useState } from 'react';
import { useNexStore } from '../core/store';
import { SessionRecord } from '../core/types';
import { formatMsg, formatRelativeTimeShort } from '../i18n/messages';

function projectLeaf(workspacePath: string, fallback: string): string {
  return workspacePath.split(/[\\/]/).filter(Boolean).pop() || fallback;
}

const FADE_MASK = {
  WebkitMaskImage: 'linear-gradient(to right, black 82%, transparent 100%)',
  maskImage: 'linear-gradient(to right, black 82%, transparent 100%)',
};

const TASKS_PER_PROJECT = 4;
const SIDEBAR_GROUP_MODE_KEY = 'nex.sidebar.group_mode.v1';

function readPersistedGroupMode(): 'project' | 'recent' {
  try {
    if (typeof window !== 'undefined' && window.localStorage) {
      const v = window.localStorage.getItem(SIDEBAR_GROUP_MODE_KEY);
      if (v === 'recent' || v === 'project') return v;
    }
  } catch {
    // ignore storage errors
  }
  return 'project';
}

function persistGroupMode(mode: 'project' | 'recent') {
  try {
    if (typeof window !== 'undefined' && window.localStorage) {
      window.localStorage.setItem(SIDEBAR_GROUP_MODE_KEY, mode);
    }
  } catch {
    // ignore storage errors
  }
}

interface ProjectGroup {
  key: string;
  runnerId: string;
  runnerName: string;
  runnerStatus?: 'online' | 'offline';
  workspacePath: string;
  name: string;
  latestAt: number;
  sessions: SessionRecord[];
}

interface RunnerTab {
  id: string;
  name: string;
  status: 'online' | 'offline';
  hasAttention: boolean;
}

type SessionIndicatorState = 'awaiting' | 'running' | 'error' | 'unread' | 'idle';

function useSessionIndicator(sessionId: string, status: string): SessionIndicatorState {
  return useNexStore((s) => {
    const st = s.streams[sessionId];
    if (st?.pendingUIPrompt || st?.pendingApproval) return 'awaiting';
    if (st ? st.running : status === 'running') return 'running';
    if (status === 'error') return 'error';
    if (s.unreadSessionIds[sessionId]) return 'unread';
    return 'idle';
  });
}

function SessionStatusDot({
  sessionId,
  status,
  className = '',
}: {
  sessionId: string;
  status: string;
  className?: string;
}) {
  const indicator = useSessionIndicator(sessionId, status);
  if (indicator === 'idle') return null;

  if (indicator === 'running') {
    return (
      <span
        data-testid={`session-status-dot-${sessionId}`}
        data-status="running"
        className={`nex-running-dots shrink-0 text-foreground-subtle ${className}`.trim()}
      >
        <span />
        <span />
        <span />
        <span />
      </span>
    );
  }

  const toneClass =
    indicator === 'awaiting'
      ? 'animate-pulse bg-amber-500'
      : indicator === 'unread'
        ? 'bg-brand-blue'
        : 'bg-destructive';

  return (
    <span
      data-testid={`session-status-dot-${sessionId}`}
      data-status={indicator}
      className={`size-1.5 shrink-0 rounded-full ${toneClass} ${className}`.trim()}
    />
  );
}

function PinnedLeadingSlot({ sessionId, status }: { sessionId: string; status: string }) {
  const indicator = useSessionIndicator(sessionId, status);
  if (indicator === 'idle') {
    return <Pin className="size-3.5" />;
  }
  return (
    <>
      <SessionStatusDot sessionId={sessionId} status={status} className="group-hover:hidden" />
      <Pin className="hidden size-3.5 group-hover:block" />
    </>
  );
}

export function ProjectSidebar() {
  const locale = useNexStore((s) => s.locale);
  const mainView = useNexStore((s) => s.mainView);
  const settingsSection = useNexStore((s) => s.settingsSection);
  const runners = useNexStore((s) => s.runners);
  const sessions = useNexStore((s) => s.sessions);
  const streams = useNexStore((s) => s.streams);
  const unreadSessionIds = useNexStore((s) => s.unreadSessionIds);
  const activeSessionId = useNexStore((s) => s.activeSessionId);
  const pinnedSessionIds = useNexStore((s) => s.pinnedSessionIds);
  const startNewSession = useNexStore((s) => s.startNewSession);
  const selectSession = useNexStore((s) => s.selectSession);
  const renameSession = useNexStore((s) => s.renameSession);
  const archiveSession = useNexStore((s) => s.archiveSession);
  const togglePinSession = useNexStore((s) => s.togglePinSession);
  const toggleSidebar = useNexStore((s) => s.toggleSidebar);
  const openSettings = useNexStore((s) => s.openSettings);
  const closeSettings = useNexStore((s) => s.closeSettings);
  const setQuickSearchOpen = useNexStore((s) => s.setQuickSearchOpen);

  const maybeCollapseOnMobile = () => {
    if (typeof window !== 'undefined' && window.innerWidth < 768 && useNexStore.getState().sidebarVisible) {
      toggleSidebar();
    }
  };
  const handleSelectSession = (id: string) => {
    void selectSession(id);
    maybeCollapseOnMobile();
  };
  const handleStartNewSession = (runnerId?: string, workspacePath?: string) => {
    startNewSession(runnerId, workspacePath);
    maybeCollapseOnMobile();
  };
  const handleOpenSettings = (section?: 'automations' | 'plugins') => {
    openSettings(section);
    maybeCollapseOnMobile();
  };

  const [search, setSearch] = useState('');
  const [searchOpen, setSearchOpen] = useState(false);
  const [selectedRunnerFilter, setSelectedRunnerFilter] = useState<string>('all');
  const [groupMode, setGroupMode] = useState<'project' | 'recent'>(() => readPersistedGroupMode());
  const [expandedGroups, setExpandedGroups] = useState<Record<string, boolean>>({});
  const [groupOpenOverrides, setGroupOpenOverrides] = useState<Record<string, boolean>>({});
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editingTitle, setEditingTitle] = useState('');

  const switchGroupMode = (mode: 'project' | 'recent') => {
    setGroupMode(mode);
    persistGroupMode(mode);
  };

  const runnerById = useMemo(() => {
    const map = new Map<string, { name: string; status: 'online' | 'offline' }>();
    for (const r of runners) {
      map.set(r.id, { name: r.name || r.hostname || r.id, status: r.status });
    }
    return map;
  }, [runners]);

  const isSessionAttention = (s: SessionRecord): boolean => {
    const st = streams[s.id];
    if (st?.pendingUIPrompt || st?.pendingApproval) return true;
    if (st ? st.running : s.status === 'running') return true;
    return Boolean(unreadSessionIds[s.id]);
  };

  const runnerTabs = useMemo<RunnerTab[]>(() => {
    const map = new Map<string, RunnerTab>();
    for (const r of runners) {
      map.set(r.id, {
        id: r.id,
        name: r.name || r.hostname || r.id,
        status: r.status,
        hasAttention: false,
      });
    }
    for (const s of sessions) {
      if (!s.runner_id) continue;
      const existing = map.get(s.runner_id);
      const attn = isSessionAttention(s);
      if (existing) {
        if (attn) existing.hasAttention = true;
      } else {
        map.set(s.runner_id, {
          id: s.runner_id,
          name: s.runner_id,
          status: 'offline',
          hasAttention: attn,
        });
      }
    }
    return Array.from(map.values()).sort((a, b) => {
      if (a.status !== b.status) return a.status === 'online' ? -1 : 1;
      return a.name.localeCompare(b.name);
    });
  }, [runners, sessions, streams, unreadSessionIds]);

  const effectiveRunnerFilter =
    selectedRunnerFilter !== 'all' && runnerTabs.some((r) => r.id === selectedRunnerFilter)
      ? selectedRunnerFilter
      : 'all';

  const filteredSessions = useMemo(() => {
    const tokens = search.trim().toLowerCase().split(/\s+/).filter(Boolean);
    return sessions.filter((s) => {
      if (effectiveRunnerFilter !== 'all' && s.runner_id !== effectiveRunnerFilter) {
        return false;
      }
      if (tokens.length === 0) return true;
      const rName = runnerById.get(s.runner_id)?.name || '';
      const haystack = `${s.title} ${s.workspace_path || ''} ${rName} ${s.runner_id}`.toLowerCase();
      return tokens.every((tok) => haystack.includes(tok));
    });
  }, [sessions, search, runnerById, effectiveRunnerFilter]);

  const attentionSessions = useMemo(() => {
    return filteredSessions
      .filter((s) => !pinnedSessionIds[s.id] && isSessionAttention(s))
      .sort((a, b) => (b.last_active_at || b.updated_at) - (a.last_active_at || a.updated_at));
  }, [filteredSessions, pinnedSessionIds, streams, unreadSessionIds]);

  const attentionIdSet = useMemo(() => new Set(attentionSessions.map((s) => s.id)), [attentionSessions]);

  const pinnedSessions = useMemo(
    () => filteredSessions.filter((s) => pinnedSessionIds[s.id]),
    [filteredSessions, pinnedSessionIds],
  );

  const groups = useMemo<ProjectGroup[]>(() => {
    const map = new Map<string, ProjectGroup>();
    for (const sess of filteredSessions) {
      const key = `${sess.runner_id}::${sess.workspace_path || ''}`;
      const ts = sess.last_active_at || sess.updated_at || sess.created_at || 0;
      const hiddenFromGroup = Boolean(pinnedSessionIds[sess.id] || attentionIdSet.has(sess.id));
      let grp = map.get(key);
      if (!grp) {
        const runnerMeta = runnerById.get(sess.runner_id);
        grp = {
          key,
          runnerId: sess.runner_id,
          runnerName: runnerMeta?.name || sess.runner_id || '',
          runnerStatus: runnerMeta?.status,
          workspacePath: sess.workspace_path || '',
          name: projectLeaf(sess.workspace_path || '', sess.runner_id || 'Workspace'),
          latestAt: ts,
          sessions: [],
        };
        map.set(key, grp);
      } else if (ts > grp.latestAt) {
        grp.latestAt = ts;
      }
      if (!hiddenFromGroup) {
        grp.sessions.push(sess);
      }
    }
    const list = Array.from(map.values());
    for (const grp of list) {
      grp.sessions.sort((a, b) => (b.last_active_at || b.updated_at) - (a.last_active_at || a.updated_at));
    }
    return list.sort((a, b) => {
      if (b.latestAt !== a.latestAt) return b.latestAt - a.latestAt;
      return a.name.localeCompare(b.name) || a.runnerName.localeCompare(b.runnerName);
    });
  }, [filteredSessions, pinnedSessionIds, attentionIdSet, runnerById]);

  const recentBuckets = useMemo(() => {
    const items = filteredSessions
      .filter((s) => !pinnedSessionIds[s.id] && !attentionIdSet.has(s.id))
      .sort((a, b) => (b.last_active_at || b.updated_at) - (a.last_active_at || a.updated_at));

    const now = new Date();
    const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
    const dayMs = 24 * 60 * 60 * 1000;
    const startOfYesterday = startOfToday - dayMs;
    const startOf7Days = startOfToday - 6 * dayMs;

    const buckets: Array<{ key: 'today' | 'yesterday' | 'week' | 'earlier'; label: string; sessions: SessionRecord[] }> = [
      { key: 'today', label: formatMsg(locale, 'sidebar.bucketToday'), sessions: [] },
      { key: 'yesterday', label: formatMsg(locale, 'sidebar.bucketYesterday'), sessions: [] },
      { key: 'week', label: formatMsg(locale, 'sidebar.bucketLast7Days'), sessions: [] },
      { key: 'earlier', label: formatMsg(locale, 'sidebar.bucketEarlier'), sessions: [] },
    ];

    for (const s of items) {
      const ts = s.last_active_at || s.updated_at || s.created_at || 0;
      if (ts >= startOfToday) {
        buckets[0].sessions.push(s);
      } else if (ts >= startOfYesterday) {
        buckets[1].sessions.push(s);
      } else if (ts >= startOf7Days) {
        buckets[2].sessions.push(s);
      } else {
        buckets[3].sessions.push(s);
      }
    }
    return buckets.filter((b) => b.sessions.length > 0);
  }, [filteredSessions, pinnedSessionIds, attentionIdSet, locale]);

  const formatSessionContext = (sess: SessionRecord): string => {
    const wsLeaf = sess.workspace_path ? projectLeaf(sess.workspace_path, '') : '';
    const rName = runnerById.get(sess.runner_id)?.name || sess.runner_id || '';
    if (effectiveRunnerFilter !== 'all') {
      return wsLeaf;
    }
    return [rName, wsLeaf].filter(Boolean).join(' · ');
  };

  const commitRename = async (id: string) => {
    if (editingTitle.trim()) {
      await renameSession(id, editingTitle.trim());
    }
    setEditingId(null);
  };

  const startRename = (sess: SessionRecord) => {
    setEditingId(sess.id);
    setEditingTitle(sess.title);
  };

  const renderEditingRow = (sess: SessionRecord) => (
    <div key={sess.id} className="flex h-8 items-center gap-1 rounded-lg bg-selected pl-4 pr-1">
      <input
        autoFocus
        value={editingTitle}
        onChange={(e) => setEditingTitle(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') void commitRename(sess.id);
          if (e.key === 'Escape') setEditingId(null);
        }}
        className="min-w-0 flex-1 bg-transparent text-ui-sm text-foreground"
      />
      <button
        type="button"
        onClick={() => void commitRename(sess.id)}
        className="flex size-6 items-center justify-center rounded-md text-success hover:bg-hover"
      >
        <Check className="size-3.5" />
      </button>
      <button
        type="button"
        onClick={() => setEditingId(null)}
        className="flex size-6 items-center justify-center rounded-md text-foreground-subtle hover:bg-hover"
      >
        <X className="size-3.5" />
      </button>
    </div>
  );

  const renderSessionRow = (sess: SessionRecord, indent = true, contextLabel?: string) => {
    if (editingId === sess.id) return renderEditingRow(sess);

    const active = mainView === 'workspace' && sess.id === activeSessionId;
    const pinned = Boolean(pinnedSessionIds[sess.id]);

    return (
      <div
        key={sess.id}
        data-testid={`session-row-${sess.id}`}
        onClick={() => handleSelectSession(sess.id)}
        className={`group relative flex cursor-pointer items-center justify-between gap-2 rounded-lg pr-2 transition-colors ${
          contextLabel ? 'min-h-8 py-1.5' : 'h-8'
        } ${indent ? 'pl-6' : 'pl-2'} ${
          active ? 'bg-selected font-medium text-foreground' : 'text-foreground-subtle hover:bg-hover hover:text-foreground'
        }`}
      >
        {indent && (
          <span className="pointer-events-none absolute left-2 flex w-4 items-center justify-center">
            <SessionStatusDot sessionId={sess.id} status={sess.status} />
          </span>
        )}
        <span className="flex min-w-0 flex-1 items-center gap-1.5">
          {!indent && <SessionStatusDot sessionId={sess.id} status={sess.status} />}
          <span className="min-w-0 flex-1">
            <span className="block overflow-hidden whitespace-nowrap text-ui-base" style={FADE_MASK}>
              {sess.title}
            </span>
            {contextLabel && (
              <span className="block truncate text-[11px] leading-tight text-foreground-subtlest">
                {contextLabel}
              </span>
            )}
          </span>
        </span>

        <span className="flex shrink-0 items-center text-ui-xs tabular-nums text-foreground-subtlest group-hover:hidden">
          {formatRelativeTimeShort(sess.last_active_at || sess.updated_at, locale)}
        </span>

        <div className="hidden shrink-0 items-center gap-0.5 group-hover:flex">
          <button
            type="button"
            title={formatMsg(locale, pinned ? 'taskList.unpin' : 'taskList.pin')}
            onClick={(e) => {
              e.stopPropagation();
              togglePinSession(sess.id);
            }}
            className="flex size-5 items-center justify-center rounded-md text-foreground-subtle transition-colors hover:bg-selected hover:text-foreground"
          >
            <Pin className="size-3" />
          </button>
          <button
            type="button"
            title={formatMsg(locale, 'taskList.rename')}
            onClick={(e) => {
              e.stopPropagation();
              startRename(sess);
            }}
            className="flex size-5 items-center justify-center rounded-md text-foreground-subtle transition-colors hover:bg-selected hover:text-foreground"
          >
            <Pencil className="size-3" />
          </button>
          <button
            type="button"
            title={formatMsg(locale, 'taskList.archive')}
            onClick={(e) => {
              e.stopPropagation();
              void archiveSession(sess.id);
            }}
            className="flex size-5 items-center justify-center rounded-md text-foreground-subtle transition-colors hover:bg-selected hover:text-foreground"
          >
            <Archive className="size-3" />
          </button>
        </div>
      </div>
    );
  };

  const renderPinnedRow = (sess: SessionRecord) => {
    if (editingId === sess.id) return renderEditingRow(sess);

    const active = mainView === 'workspace' && sess.id === activeSessionId;

    return (
      <div
        key={sess.id}
        data-testid={`session-row-${sess.id}`}
        onClick={() => handleSelectSession(sess.id)}
        className={`group flex h-8 cursor-pointer items-center justify-between gap-2 rounded-lg px-2 transition-colors ${
          active ? 'bg-selected font-medium text-foreground' : 'text-foreground-subtle hover:bg-hover hover:text-foreground'
        }`}
      >
        <span className="flex min-w-0 flex-1 items-center gap-2">
          <button
            type="button"
            title={formatMsg(locale, 'taskList.unpin')}
            onClick={(e) => {
              e.stopPropagation();
              togglePinSession(sess.id);
            }}
            className="flex size-4 shrink-0 items-center justify-center text-foreground-subtle transition-colors hover:text-foreground"
          >
            <PinnedLeadingSlot sessionId={sess.id} status={sess.status} />
          </button>
          <span className="min-w-0 flex-1 overflow-hidden whitespace-nowrap text-ui-base" style={FADE_MASK}>
            {sess.title}
          </span>
        </span>

        <span className="flex shrink-0 items-center text-ui-xs tabular-nums text-foreground-subtlest">
          {formatRelativeTimeShort(sess.last_active_at || sess.updated_at, locale)}
        </span>
      </div>
    );
  };

  const isGroupExpanded = (group: ProjectGroup, idx: number): boolean => {
    if (search.trim() !== '') return true;
    const override = groupOpenOverrides[group.key];
    if (typeof override === 'boolean') return override;
    if (activeSessionId && group.sessions.some((s) => s.id === activeSessionId)) return true;
    return idx < 2;
  };

  const renderGroup = (group: ProjectGroup, idx: number) => {
    const expanded = isGroupExpanded(group, idx);
    const collapsed = !expanded;
    const showAll = Boolean(expandedGroups[group.key]);
    const shown = showAll ? group.sessions : group.sessions.slice(0, TASKS_PER_PROJECT);
    const remainingCount = group.sessions.length - TASKS_PER_PROJECT;
    const hasMore = remainingCount > 0;

    return (
      <div key={group.key} className="space-y-0.5">
        <div
          data-testid={`project-group-${group.key}`}
          data-collapsed={collapsed ? 'true' : 'false'}
          className="group flex h-8 cursor-pointer items-center justify-between gap-1.5 rounded-lg px-2 transition-colors hover:bg-hover"
          onClick={() => setGroupOpenOverrides((p) => ({ ...p, [group.key]: collapsed }))}
        >
          <span className="flex min-w-0 flex-1 items-center gap-1.5 text-ui-base text-foreground">
            <ChevronRight
              className={`size-3 shrink-0 text-foreground-subtlest transition-transform duration-150 ${
                expanded ? 'rotate-90' : ''
              }`}
            />
            <Folder className="size-3.5 shrink-0 text-foreground-subtle transition-transform duration-150 group-hover:scale-105" />
            <span className="truncate">{group.name}</span>
            {effectiveRunnerFilter === 'all' && group.runnerName && (
              <span
                data-testid={`project-runner-badge-${group.key}`}
                title={group.workspacePath ? `${group.runnerName}: ${group.workspacePath}` : group.runnerName}
                className="inline-flex max-w-[96px] shrink-0 items-center gap-1 rounded bg-tag px-1.5 py-0.5 text-[11px] leading-tight text-foreground-subtle"
              >
                {group.runnerStatus === 'offline' && (
                  <CloudOff
                    data-testid={`project-runner-offline-${group.key}`}
                    className="size-3 shrink-0 text-foreground-subtlest"
                  />
                )}
                <span className="truncate">{group.runnerName}</span>
              </span>
            )}
          </span>
          <div className="flex shrink-0 items-center">
            {collapsed && group.sessions.length > 0 && (
              <span className="px-1 text-ui-xs tabular-nums text-foreground-subtlest group-hover:hidden">
                {group.sessions.length}
              </span>
            )}
            <button
              type="button"
              title={formatMsg(locale, 'sidebar.newTask')}
              onClick={(e) => {
                e.stopPropagation();
                handleStartNewSession(group.runnerId, group.workspacePath);
              }}
              className="flex size-5 shrink-0 items-center justify-center rounded-md text-foreground-subtle opacity-0 transition-all hover:bg-selected hover:text-foreground group-hover:opacity-100"
            >
              <Plus className="size-3.5" />
            </button>
          </div>
        </div>

        <div className="nex-collapsible" data-open={expanded}>
          <div className="nex-collapsible-inner">
            <div className="space-y-0.5">
              {group.sessions.length === 0 ? (
                <button
                  type="button"
                  onClick={() => handleStartNewSession(group.runnerId, group.workspacePath)}
                  className="flex h-8 w-full items-center gap-2 rounded-lg pl-5 pr-2 text-left text-ui-sm text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground-subtle"
                >
                  <Plus className="size-3.5 shrink-0" />
                  <span className="truncate">{formatMsg(locale, 'sidebar.newTask')}</span>
                </button>
              ) : (
                shown.map((s) => renderSessionRow(s))
              )}

              {hasMore && (
                <button
                  type="button"
                  onClick={() => setExpandedGroups((p) => ({ ...p, [group.key]: !p[group.key] }))}
                  className="flex h-7 w-full items-center rounded-lg pl-6 pr-2 text-left text-ui-xs text-foreground-subtlest transition-colors hover:text-foreground-subtle"
                >
                  {showAll
                    ? formatMsg(locale, 'sidebar.showLess')
                    : formatMsg(locale, 'sidebar.showMoreCount', { count: remainingCount })}
                </button>
              )}
            </div>
          </div>
        </div>
      </div>
    );
  };

  const taskListLabel = formatMsg(locale, 'sidebar.taskList');

  return (
    <aside
      data-testid="project-sidebar"
      className="group/sidebar flex h-full w-full flex-col border-r border-border bg-sidebar text-foreground select-none"
    >
      {/* History Bar */}
      <div className="nex-drag-region nex-titlebar-inset-left flex h-10 shrink-0 items-center justify-between pr-3">
        <div className="nex-no-drag flex items-center gap-1">
          <button
            type="button"
            onClick={() => window.history.back()}
            className="flex size-6 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground-subtle"
          >
            <ArrowLeft className="size-3.5" />
          </button>
          <button
            type="button"
            onClick={() => window.history.forward()}
            className="flex size-6 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground-subtle"
          >
            <ArrowRight className="size-3.5" />
          </button>
        </div>
        <button
          type="button"
          onClick={toggleSidebar}
          title={formatMsg(locale, 'sidePane.collapse')}
          className="nex-no-drag flex size-6 items-center justify-center rounded-md text-foreground-subtlest opacity-0 transition-colors hover:bg-hover hover:text-foreground-subtle focus-visible:opacity-100 group-hover/sidebar:opacity-100"
        >
          <PanelLeftClose className="size-3.5" />
        </button>
      </div>

      {/* Primary Navigation */}
      <div className="shrink-0 space-y-0.5 px-2 pb-2.5">
        <button
          type="button"
          data-testid="new-session-btn"
          onClick={() => handleStartNewSession(effectiveRunnerFilter !== 'all' ? effectiveRunnerFilter : undefined)}
          className="flex h-8 w-full items-center justify-between rounded-lg px-2.5 transition-colors hover:bg-hover"
        >
          <span className="flex items-center gap-2.5 text-ui-base font-normal text-foreground">
            <CirclePlus className="size-4 shrink-0" />
            <span className="truncate">{formatMsg(locale, 'sidebar.newTask')}</span>
          </span>
          <span className="shrink-0 text-ui-xs text-foreground-subtlest">⌘ N</span>
        </button>

        <button
          type="button"
          data-testid="open-command-palette-btn"
          onClick={() => setQuickSearchOpen(true)}
          className="flex h-8 w-full items-center justify-between rounded-lg px-2.5 transition-colors hover:bg-hover"
        >
          <span className="flex items-center gap-2.5 text-ui-base font-normal text-foreground">
            <Search className="size-4 shrink-0" />
            <span className="truncate">{formatMsg(locale, 'sidebar.searchCommands')}</span>
          </span>
          <span className="shrink-0 text-ui-xs text-foreground-subtlest">⌘ K</span>
        </button>

        <button
          type="button"
          onClick={() => handleOpenSettings('automations')}
          className={`flex h-8 w-full items-center gap-2.5 rounded-lg px-2.5 text-ui-base text-foreground transition-colors hover:bg-hover ${
            mainView === 'settings' && settingsSection === 'automations' ? 'bg-selected' : ''
          }`}
        >
          <CalendarClock className="size-4 shrink-0" />
          <span className="truncate">{formatMsg(locale, 'workspace.openScheduledSettings')}</span>
        </button>

        <button
          type="button"
          onClick={() => handleOpenSettings('plugins')}
          className={`flex h-8 w-full items-center gap-2.5 rounded-lg px-2.5 text-ui-base text-foreground transition-colors hover:bg-hover ${
            mainView === 'settings' && settingsSection === 'plugins' ? 'bg-selected' : ''
          }`}
        >
          <Blocks className="size-4 shrink-0" />
          <span className="truncate">{formatMsg(locale, 'workspace.openPluginsSettings')}</span>
        </button>
      </div>

      {/* Task List Header + Segmented View Switcher */}
      <div className="mx-2.5 flex shrink-0 items-center justify-between gap-2 border-t border-border/70 px-0.5 pb-1.5 pt-2.5">
        <span className="truncate text-ui-sm font-medium text-foreground-subtle">{taskListLabel}</span>
        <div className="flex shrink-0 items-center gap-1">
          <div className="inline-flex h-6 items-center rounded-md bg-selected/75 p-0.5 text-[11px]">
            <button
              type="button"
              data-testid="sidebar-mode-project"
              onClick={() => switchGroupMode('project')}
              className={`rounded-[4px] px-1.5 py-0.5 font-medium transition-all ${
                groupMode === 'project'
                  ? 'bg-background text-foreground shadow-[0_1px_2px_rgba(0,0,0,0.06)]'
                  : 'text-foreground-subtlest hover:text-foreground-subtle'
              }`}
            >
              {formatMsg(locale, 'sidebar.modeProject')}
            </button>
            <button
              type="button"
              data-testid="sidebar-mode-recent"
              onClick={() => switchGroupMode('recent')}
              className={`rounded-[4px] px-1.5 py-0.5 font-medium transition-all ${
                groupMode === 'recent'
                  ? 'bg-background text-foreground shadow-[0_1px_2px_rgba(0,0,0,0.06)]'
                  : 'text-foreground-subtlest hover:text-foreground-subtle'
              }`}
            >
              {formatMsg(locale, 'sidebar.modeRecent')}
            </button>
          </div>
          <button
            type="button"
            data-testid="sidebar-search-toggle"
            title={formatMsg(locale, 'sidebar.search')}
            onClick={() => {
              setSearchOpen((v) => {
                if (v) setSearch('');
                return !v;
              });
            }}
            className={`flex size-6 items-center justify-center rounded-md transition-colors hover:bg-hover ${
              searchOpen ? 'bg-hover text-foreground' : 'text-foreground-subtlest hover:text-foreground-subtle'
            }`}
          >
            <Search className="size-3.5" />
          </button>
        </div>
      </div>

      {/* Runner Filter Chips (auto-shown when 2+ runners exist) */}
      {runnerTabs.length > 1 && (
        <div
          data-testid="sidebar-runner-filter"
          className="nex-no-scrollbar mx-2.5 mb-2 flex shrink-0 flex-wrap items-center gap-1.5"
        >
          <button
            type="button"
            data-testid="sidebar-runner-pill-all"
            onClick={() => setSelectedRunnerFilter('all')}
            className={`rounded-md px-2 py-0.5 text-[11.5px] transition-all ${
              effectiveRunnerFilter === 'all'
                ? 'bg-background font-medium text-foreground shadow-[0_1px_2px_rgba(0,0,0,0.06)] ring-1 ring-border-hover'
                : 'bg-background/45 text-foreground-subtle ring-1 ring-border/60 hover:bg-background/80 hover:text-foreground'
            }`}
          >
            {formatMsg(locale, 'sidebar.allRunners')}
          </button>
          {runnerTabs.map((rt) => {
            const active = effectiveRunnerFilter === rt.id;
            return (
              <button
                key={rt.id}
                type="button"
                data-testid={`sidebar-runner-pill-${rt.id}`}
                onClick={() => setSelectedRunnerFilter(active ? 'all' : rt.id)}
                className={`inline-flex max-w-[120px] items-center gap-1.5 rounded-md px-2 py-0.5 text-[11.5px] transition-all ${
                  active
                    ? 'bg-background font-medium text-foreground shadow-[0_1px_2px_rgba(0,0,0,0.06)] ring-1 ring-border-hover'
                    : 'bg-background/45 text-foreground-subtle ring-1 ring-border/60 hover:bg-background/80 hover:text-foreground'
                }`}
              >
                <span
                  className={`size-1.5 shrink-0 rounded-full ${
                    rt.status === 'online'
                      ? rt.hasAttention
                        ? 'animate-pulse bg-success'
                        : 'bg-success'
                      : 'bg-foreground-subtlest/50'
                  }`}
                />
                <span className="truncate">{rt.name}</span>
              </button>
            );
          })}
        </div>
      )}

      {searchOpen && (
        <div className="nex-popover-down mx-3 mb-1 flex h-7 shrink-0 items-center gap-1.5 rounded-lg bg-surface px-2">
          <Search className="size-3 shrink-0 text-foreground-subtlest" />
          <input
            autoFocus
            data-testid="sidebar-search-input"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={formatMsg(locale, 'sidebar.search')}
            className="min-w-0 flex-1 bg-transparent text-ui-sm text-foreground placeholder-foreground-subtlest"
          />
          {search && (
            <button
              type="button"
              onClick={() => setSearch('')}
              className="flex size-4 items-center justify-center rounded text-foreground-subtlest hover:text-foreground"
            >
              <X className="size-3" />
            </button>
          )}
        </div>
      )}

      {/* Task Sections */}
      <div className="flex-1 space-y-3 overflow-y-auto px-2 py-1">
        {attentionSessions.length > 0 && (
          <div data-testid="sidebar-active-section" className="space-y-0.5">
            <div className="px-2 pb-1 text-ui-xs font-medium text-foreground-subtlest">
              {formatMsg(locale, 'sidebar.activeSection')}
            </div>
            {attentionSessions.map((s) => renderSessionRow(s, false, formatSessionContext(s)))}
          </div>
        )}

        {pinnedSessions.length > 0 && (
          <div className="space-y-0.5">
            <div className="px-2 pb-1 text-ui-xs font-medium text-foreground-subtlest">
              {formatMsg(locale, 'taskList.pinnedSection')}
            </div>
            {pinnedSessions.map(renderPinnedRow)}
          </div>
        )}

        {groupMode === 'recent' ? (
          recentBuckets.length === 0 ? (
            attentionSessions.length === 0 &&
            pinnedSessions.length === 0 && (
              <div className="px-3 py-6 text-center text-ui-sm text-foreground-subtlest">
                {formatMsg(locale, 'taskList.noTasks')}
              </div>
            )
          ) : (
            <div data-testid="sidebar-recent-view" className="space-y-3">
              {recentBuckets.map((bucket) => (
                <div key={bucket.key} className="space-y-0.5">
                  <div className="px-2 pb-0.5 text-ui-xs font-medium text-foreground-subtlest">
                    {bucket.label}
                  </div>
                  {bucket.sessions.map((s) => renderSessionRow(s, false, formatSessionContext(s)))}
                </div>
              ))}
            </div>
          )
        ) : groups.length === 0 ? (
          attentionSessions.length === 0 &&
          pinnedSessions.length === 0 && (
            <div className="px-3 py-6 text-center text-ui-sm text-foreground-subtlest">
              {formatMsg(locale, 'taskList.noTasks')}
            </div>
          )
        ) : (
          <div className="space-y-1">
            {(attentionSessions.length > 0 || pinnedSessions.length > 0) && (
              <div className="px-2 pb-0.5 text-ui-xs font-medium text-foreground-subtlest">
                {formatMsg(locale, 'sidebar.projects')}
              </div>
            )}
            {groups.map((grp, idx) => renderGroup(grp, idx))}
          </div>
        )}
      </div>

      {/* Minimal Footer */}
      <div className="flex h-11 shrink-0 items-center justify-end px-3">
        <button
          type="button"
          data-testid="sidebar-settings-btn"
          onClick={() => (mainView === 'settings' ? closeSettings() : handleOpenSettings())}
          title={formatMsg(locale, mainView === 'settings' ? 'workspace.backToWorkspace' : 'settings.title')}
          className="flex size-7 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
        >
          <Settings className="size-4" />
        </button>
      </div>
    </aside>
  );
}
