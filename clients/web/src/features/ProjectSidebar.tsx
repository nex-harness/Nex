import {
  Archive,
  ArrowLeft,
  ArrowRight,
  Blocks,
  CalendarClock,
  Check,
  CirclePlus,
  Folder,
  ListFilter,
  PanelLeftClose,
  Pencil,
  Pin,
  Plus,
  Search,
  Settings,
  Trash2,
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

const TASKS_PER_PROJECT = 8;

interface ProjectGroup {
  key: string;
  runnerId: string;
  runnerName: string;
  runnerStatus?: 'online' | 'offline';
  workspacePath: string;
  name: string;
  sessions: SessionRecord[];
}

function SessionRunningDot({ sessionId, status }: { sessionId: string; status: string }) {
  const running = useNexStore((s) => {
    const st = s.streams[sessionId];
    return st ? st.running : status === 'running';
  });
  if (!running) return null;
  return <span className="size-1.5 animate-pulse rounded-full bg-brand-blue" />;
}

export function ProjectSidebar() {
  const locale = useNexStore((s) => s.locale);
  const mainView = useNexStore((s) => s.mainView);
  const settingsSection = useNexStore((s) => s.settingsSection);
  const runners = useNexStore((s) => s.runners);
  const sessions = useNexStore((s) => s.sessions);
  const activeSessionId = useNexStore((s) => s.activeSessionId);
  const pinnedSessionIds = useNexStore((s) => s.pinnedSessionIds);
  const startNewSession = useNexStore((s) => s.startNewSession);
  const selectSession = useNexStore((s) => s.selectSession);
  const renameSession = useNexStore((s) => s.renameSession);
  const archiveSession = useNexStore((s) => s.archiveSession);
  const deleteSession = useNexStore((s) => s.deleteSession);
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
  const [groupMode, setGroupMode] = useState<'project' | 'recent'>('project');
  const [expandedGroups, setExpandedGroups] = useState<Record<string, boolean>>({});
  const [collapsedGroups, setCollapsedGroups] = useState<Record<string, boolean>>({});
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editingTitle, setEditingTitle] = useState('');

  const runnerById = useMemo(() => {
    const map = new Map<string, { name: string; status: 'online' | 'offline' }>();
    for (const r of runners) {
      map.set(r.id, { name: r.name || r.hostname || r.id, status: r.status });
    }
    return map;
  }, [runners]);

  const filteredSessions = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return sessions;
    return sessions.filter((s) => {
      const rName = runnerById.get(s.runner_id)?.name || '';
      return (
        s.title.toLowerCase().includes(q) ||
        (s.workspace_path || '').toLowerCase().includes(q) ||
        s.runner_id.toLowerCase().includes(q) ||
        rName.toLowerCase().includes(q)
      );
    });
  }, [sessions, search, runnerById]);

  const pinnedSessions = useMemo(
    () => filteredSessions.filter((s) => pinnedSessionIds[s.id]),
    [filteredSessions, pinnedSessionIds],
  );

  const groups = useMemo<ProjectGroup[]>(() => {
    const map = new Map<string, ProjectGroup>();
    for (const sess of filteredSessions) {
      if (pinnedSessionIds[sess.id]) continue;
      const key = `${sess.runner_id}::${sess.workspace_path || ''}`;
      const existing = map.get(key);
      if (existing) {
        existing.sessions.push(sess);
        continue;
      }
      const runnerMeta = runnerById.get(sess.runner_id);
      map.set(key, {
        key,
        runnerId: sess.runner_id,
        runnerName: runnerMeta?.name || sess.runner_id || '',
        runnerStatus: runnerMeta?.status,
        workspacePath: sess.workspace_path || '',
        name: projectLeaf(sess.workspace_path || '', sess.runner_id || 'Workspace'),
        sessions: [sess],
      });
    }
    return Array.from(map.values()).sort(
      (a, b) => a.name.localeCompare(b.name) || a.runnerName.localeCompare(b.runnerName),
    );
  }, [filteredSessions, pinnedSessionIds, runnerById]);

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

  const renderSessionRow = (sess: SessionRecord, indent = true) => {
    if (editingId === sess.id) return renderEditingRow(sess);

    const active = mainView === 'workspace' && sess.id === activeSessionId;
    const pinned = Boolean(pinnedSessionIds[sess.id]);

    return (
      <div
        key={sess.id}
        data-testid={`session-row-${sess.id}`}
        onClick={() => handleSelectSession(sess.id)}
        className={`group flex h-8 cursor-pointer items-center justify-between gap-2 rounded-lg pr-2 transition-colors ${
          indent ? 'pl-5' : 'pl-2'
        } ${
          active ? 'bg-selected font-medium text-foreground' : 'text-foreground-subtle hover:bg-hover hover:text-foreground'
        }`}
      >
        <span className="min-w-0 flex-1 overflow-hidden whitespace-nowrap text-ui-base" style={FADE_MASK}>
          {sess.title}
        </span>

        <span className="flex shrink-0 items-center gap-1.5 text-ui-xs tabular-nums text-foreground-subtlest group-hover:hidden">
          <SessionRunningDot sessionId={sess.id} status={sess.status} />
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
          <button
            type="button"
            title={formatMsg(locale, 'taskList.delete')}
            onClick={(e) => {
              e.stopPropagation();
              void deleteSession(sess.id);
            }}
            className="flex size-5 items-center justify-center rounded-md text-foreground-subtle transition-colors hover:bg-destructive-surface hover:text-destructive"
          >
            <Trash2 className="size-3" />
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
            className="shrink-0 text-foreground-subtle transition-colors hover:text-foreground"
          >
            <Pin className="size-3.5" />
          </button>
          <span className="min-w-0 flex-1 overflow-hidden whitespace-nowrap text-ui-base" style={FADE_MASK}>
            {sess.title}
          </span>
        </span>

        <span className="flex shrink-0 items-center gap-1.5 text-ui-xs tabular-nums text-foreground-subtlest">
          <SessionRunningDot sessionId={sess.id} status={sess.status} />
          {formatRelativeTimeShort(sess.last_active_at || sess.updated_at, locale)}
        </span>
      </div>
    );
  };

  const renderGroup = (group: ProjectGroup) => {
    const collapsed = Boolean(collapsedGroups[group.key]);
    const showAll = Boolean(expandedGroups[group.key]);
    const shown = showAll ? group.sessions : group.sessions.slice(0, TASKS_PER_PROJECT);
    const hasMore = group.sessions.length > TASKS_PER_PROJECT;

    return (
      <div key={group.key} className="space-y-0.5">
        <div
          data-testid={`project-group-${group.key}`}
          className="group flex h-8 cursor-pointer items-center justify-between gap-1.5 rounded-lg px-2 transition-colors hover:bg-hover"
          onClick={() => setCollapsedGroups((p) => ({ ...p, [group.key]: !p[group.key] }))}
        >
          <span className="flex min-w-0 flex-1 items-center gap-2 text-ui-base text-foreground">
            <Folder className="size-4 shrink-0 text-foreground-subtle transition-transform duration-150 group-hover:scale-105" />
            <span className="truncate">{group.name}</span>
            {group.runnerName && (
              <span
                data-testid={`project-runner-badge-${group.key}`}
                title={group.workspacePath ? `${group.runnerName}: ${group.workspacePath}` : group.runnerName}
                className="inline-flex max-w-[96px] shrink-0 items-center gap-1 rounded bg-tag px-1.5 py-0.5 text-[11px] leading-tight text-foreground-subtle"
              >
                {group.runnerStatus && (
                  <span
                    className={`size-1.5 shrink-0 rounded-full ${
                      group.runnerStatus === 'online' ? 'bg-success' : 'bg-foreground-subtlest'
                    }`}
                  />
                )}
                <span className="truncate">{group.runnerName}</span>
              </span>
            )}
          </span>
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

        <div className="zcode-collapsible" data-open={!collapsed}>
          <div className="zcode-collapsible-inner">
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
                  className="flex h-7 w-full items-center rounded-lg pl-5 pr-2 text-left text-ui-xs text-foreground-subtlest transition-colors hover:text-foreground-subtle"
                >
                  {showAll
                    ? formatMsg(locale, 'sidebar.showLess')
                    : formatMsg(locale, 'sidebar.showMore')}
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
          onClick={() => handleStartNewSession()}
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

      {/* Task List Header */}
      <div className="mx-3 flex shrink-0 items-center justify-between border-t border-border/70 pb-1 pt-3">
        <span className="text-ui-sm font-medium text-foreground-subtle">{taskListLabel}</span>
        <div className="flex items-center gap-0.5">
          <button
            type="button"
            title={formatMsg(locale, 'sidebar.search')}
            onClick={() => {
              setSearchOpen((v) => {
                if (v) setSearch('');
                return !v;
              });
            }}
            className={`flex size-6 items-center justify-center rounded-md transition-colors hover:bg-hover ${
              searchOpen ? 'text-foreground' : 'text-foreground-subtlest hover:text-foreground-subtle'
            }`}
          >
            <Search className="size-3.5" />
          </button>
          <button
            type="button"
            title={formatMsg(locale, groupMode === 'project' ? 'project.viewByProject' : 'project.viewRecentlyUpdated')}
            onClick={() => setGroupMode((m) => (m === 'project' ? 'recent' : 'project'))}
            className={`flex size-6 items-center justify-center rounded-md transition-colors hover:bg-hover ${
              groupMode === 'recent' ? 'text-foreground' : 'text-foreground-subtlest hover:text-foreground-subtle'
            }`}
          >
            <ListFilter className="size-3.5" />
          </button>
        </div>
      </div>

      {searchOpen && (
        <div className="zcode-popover-down mx-3 mb-1 flex h-7 shrink-0 items-center gap-1.5 rounded-lg bg-surface px-2">
          <Search className="size-3 shrink-0 text-foreground-subtlest" />
          <input
            autoFocus
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
        {pinnedSessions.length > 0 && (
          <div className="space-y-0.5">
            <div className="px-2 pb-1 text-ui-xs font-medium text-foreground-subtlest">
              {formatMsg(locale, 'taskList.pinnedSection')}
            </div>
            {pinnedSessions.map(renderPinnedRow)}
          </div>
        )}

        {groupMode === 'recent' ? (
          <div className="space-y-0.5">
            <div className="px-2 pb-1 text-ui-xs font-medium text-foreground-subtlest">
              {formatMsg(locale, 'project.viewRecentlyUpdated')}
            </div>
            {filteredSessions.filter((s) => !pinnedSessionIds[s.id]).length === 0 ? (
              <div className="px-3 py-6 text-center text-ui-sm text-foreground-subtlest">
                {formatMsg(locale, 'taskList.noTasks')}
              </div>
            ) : (
              [...filteredSessions]
                .filter((s) => !pinnedSessionIds[s.id])
                .sort((a, b) => (b.last_active_at || b.updated_at) - (a.last_active_at || a.updated_at))
                .map((s) => renderSessionRow(s, false))
            )}
          </div>
        ) : (
          <div className="space-y-3">
            <div className="px-2 text-ui-xs font-medium text-foreground-subtlest">
              {formatMsg(locale, 'sidebar.projects')}
            </div>
            {groups.length === 0 ? (
              <div className="px-3 py-6 text-center text-ui-sm text-foreground-subtlest">
                {formatMsg(locale, 'taskList.noTasks')}
              </div>
            ) : (
              groups.map(renderGroup)
            )}
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
