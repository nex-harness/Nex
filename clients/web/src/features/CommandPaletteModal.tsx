import { formatMsg } from '../i18n/messages';
import {
  Archive,
  Blocks,
  Brain,
  CirclePlus,
  Cpu,
  Download,
  ListTodo,
  LoaderCircle,
  MessageSquare,
  Moon,
  PanelLeft,
  PanelRight,
  Search,
  Sparkles,
  SquareTerminal,
  Sun,
} from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useNexStore } from '../core/store';
import { SessionSearchHit } from '../core/types';

interface PaletteItem {
  id: string;
  section: 'command' | 'session';
  title: string;
  subtitle?: string;
  badge?: string;
  shortcut?: string;
  keywords?: string;
  icon: typeof Search;
  onSelect: () => void | Promise<void>;
}

function leafName(p?: string): string {
  if (!p) return '';
  return p.split(/[\\/]/).filter(Boolean).pop() || p;
}

export function CommandPaletteModal() {
  const open = useNexStore((s) => s.quickSearchOpen);
  const setOpen = useNexStore((s) => s.setQuickSearchOpen);
  const locale = useNexStore((s) => s.locale);
  const activeSessionId = useNexStore((s) => s.activeSessionId);
  const sessions = useNexStore((s) => s.sessions);
  const permissionMode = useNexStore((s) => s.permissionMode);
  const preferences = useNexStore((s) => s.preferences);

  const startNewSession = useNexStore((s) => s.startNewSession);
  const selectSession = useNexStore((s) => s.selectSession);
  const searchSessions = useNexStore((s) => s.searchSessions);
  const exportSessionMarkdown = useNexStore((s) => s.exportSessionMarkdown);
  const setPermissionMode = useNexStore((s) => s.setPermissionMode);
  const toggleSidebar = useNexStore((s) => s.toggleSidebar);
  const toggleSidePane = useNexStore((s) => s.toggleSidePane);
  const toggleBottomPanel = useNexStore((s) => s.toggleBottomPanel);
  const updateThemeQuick = useNexStore((s) => s.updateThemeQuick);
  const openSettings = useNexStore((s) => s.openSettings);

  const [query, setQuery] = useState('');
  const [hits, setHits] = useState<SessionSearchHit[]>([]);
  const [searching, setSearching] = useState(false);
  const [activeIdx, setActiveIdx] = useState(0);
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    if (!open) {
      setQuery('');
      setActiveIdx(0);
      return;
    }
    window.setTimeout(() => inputRef.current?.focus(), 0);
  }, [open]);

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    const trimmed = query.trim();

    if (!trimmed) {
      setHits(
        sessions.slice(0, 8).map((s) => ({
          session: s,
          match_type: 'title',
          snippet: s.workspace_path || '',
        })),
      );
      setSearching(false);
      return;
    }

    setSearching(true);
    const timer = window.setTimeout(async () => {
      try {
        const res = await searchSessions(trimmed);
        if (!cancelled) setHits(res);
      } finally {
        if (!cancelled) setSearching(false);
      }
    }, 120);

    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [open, query, sessions, searchSessions]);

  const commands = useMemo<PaletteItem[]>(() => {
    const closeAnd = (fn: () => void | Promise<void>) => () => {
      setOpen(false);
      void fn();
    };
    const nextTheme = preferences.theme === 'dark' ? 'light' : 'dark';
    const items: PaletteItem[] = [
      {
        id: 'cmd-new-task',
        section: 'command',
        title: formatMsg(locale, 'sidebar.newTask'),
        subtitle: formatMsg(locale, 'palette.startAFreshConversationDraft'),
        shortcut: '⌘N',
        keywords: 'new task session chat 新建 任务 对话',
        icon: CirclePlus,
        onSelect: closeAnd(() => startNewSession()),
      },
      {
        id: 'cmd-plan-mode',
        section: 'command',
        title:
          permissionMode === 'plan'
            ? formatMsg(locale, 'palette.exitPlanModeSwitchTo')
            : formatMsg(locale, 'palette.switchToPlanMode'),
        subtitle: formatMsg(locale, 'palette.readOnlyPlanningApprovalFlow'),
        badge: permissionMode === 'plan' ? 'ON' : 'Plan',
        keywords: 'plan mode architecture 计划 规划 模式',
        icon: ListTodo,
        onSelect: closeAnd(() => setPermissionMode(permissionMode === 'plan' ? 'full_access' : 'plan')),
      },
    ];

    if (activeSessionId) {
      items.push({
        id: 'cmd-export-md',
        section: 'command',
        title: formatMsg(locale, 'palette.exportCurrentConversationAsMarkdown'),
        subtitle: formatMsg(locale, 'palette.downloadTranscriptIncludingThinkingTool'),
        badge: '.md',
        keywords: 'export markdown download transcript 导出 下载 记录',
        icon: Download,
        onSelect: closeAnd(() => {
          void exportSessionMarkdown(activeSessionId);
        }),
      });
    }

    items.push(
      {
        id: 'cmd-toggle-sidebar',
        section: 'command',
        title: formatMsg(locale, 'palette.toggleLeftSidebar'),
        keywords: 'sidebar left panel 侧边栏 任务栏',
        icon: PanelLeft,
        onSelect: closeAnd(() => toggleSidebar()),
      },
      {
        id: 'cmd-toggle-sidepane',
        section: 'command',
        title: formatMsg(locale, 'palette.toggleRightWorkbenchPanel'),
        keywords: 'workbench right side pane files git review 右侧 工作台',
        icon: PanelRight,
        onSelect: closeAnd(() => toggleSidePane()),
      },
      {
        id: 'cmd-toggle-terminal',
        section: 'command',
        title: formatMsg(locale, 'palette.toggleBottomTerminal'),
        keywords: 'terminal pty shell bash 终端',
        icon: SquareTerminal,
        onSelect: closeAnd(() => toggleBottomPanel()),
      },
      {
        id: 'cmd-toggle-theme',
        section: 'command',
        title:
          nextTheme === 'dark'
            ? formatMsg(locale, 'palette.switchToDarkTheme')
            : formatMsg(locale, 'palette.switchToLightTheme'),
        keywords: 'theme dark light appearance 主题 深色 浅色 外观',
        icon: nextTheme === 'dark' ? Moon : Sun,
        onSelect: closeAnd(() => updateThemeQuick(nextTheme)),
      },
      {
        id: 'cmd-settings-memories',
        section: 'command',
        title: formatMsg(locale, 'palette.openLongTermMemory'),
        keywords: 'memory memories preferences facts 长期记忆 记忆 偏好',
        icon: Brain,
        onSelect: closeAnd(() => openSettings('memories')),
      },
      {
        id: 'cmd-settings-providers',
        section: 'command',
        title: formatMsg(locale, 'palette.openProvidersModelsSettings'),
        keywords: 'providers models api key 模型 供应商 设置',
        icon: Cpu,
        onSelect: closeAnd(() => openSettings('providers')),
      },
      {
        id: 'cmd-settings-skills',
        section: 'command',
        title: formatMsg(locale, 'palette.openSkillsSettings'),
        keywords: 'skills prompt 技能',
        icon: Sparkles,
        onSelect: closeAnd(() => openSettings('skills')),
      },
      {
        id: 'cmd-settings-plugins',
        section: 'command',
        title: formatMsg(locale, 'palette.openPluginsSettings'),
        keywords: 'plugins extensions 插件 扩展',
        icon: Blocks,
        onSelect: closeAnd(() => openSettings('plugins')),
      },
      {
        id: 'cmd-settings-archives',
        section: 'command',
        title: formatMsg(locale, 'palette.openArchivedSessions'),
        keywords: 'archive history 归档 历史',
        icon: Archive,
        onSelect: closeAnd(() => openSettings('archives')),
      },
    );

    const q = query.trim().toLowerCase();
    if (!q) return items.slice(0, 6);
    return items.filter(
      (it) =>
        it.title.toLowerCase().includes(q) ||
        (it.subtitle || '').toLowerCase().includes(q) ||
        (it.keywords || '').toLowerCase().includes(q),
    );
  }, [
    activeSessionId,
    exportSessionMarkdown,
    locale,
    openSettings,
    permissionMode,
    preferences.theme,
    query,
    setOpen,
    setPermissionMode,
    startNewSession,
    toggleBottomPanel,
    toggleSidePane,
    toggleSidebar,
    updateThemeQuick,
  ]);

  const sessionItems = useMemo<PaletteItem[]>(() => {
    return hits.map((hit) => ({
      id: `sess-${hit.session.id}`,
      section: 'session',
      title: hit.session.title || hit.session.id,
      subtitle:
        hit.match_type === 'message' && hit.snippet
          ? hit.snippet
          : leafName(hit.session.workspace_path) || hit.session.runner_id,
      badge:
        hit.match_type === 'message'
          ? formatMsg(locale, 'palette.messageMatch')
          : leafName(hit.session.workspace_path) || undefined,
      icon: MessageSquare,
      onSelect: () => {
        setOpen(false);
        void selectSession(hit.session.id);
      },
    }));
  }, [hits, locale, selectSession, setOpen]);

  const flatItems = useMemo(() => [...sessionItems, ...commands], [sessionItems, commands]);

  useEffect(() => {
    setActiveIdx(0);
  }, [query, flatItems.length]);

  if (!open) return null;

  return (
    <div
      data-testid="command-palette-backdrop"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) setOpen(false);
      }}
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/35 px-4 pt-[12vh] backdrop-blur-[2px]"
    >
      <div
        data-testid="command-palette-modal"
        role="dialog"
        aria-modal="true"
        aria-label={formatMsg(locale, 'palette.commandPalette')}
        className="w-full max-w-[580px] overflow-hidden rounded-2xl border border-border bg-popover text-foreground shadow-2xl"
      >
        {/* Search bar */}
        <div className="flex items-center gap-2.5 border-b border-border px-3.5 py-3">
          {searching ? (
            <LoaderCircle className="size-4 shrink-0 animate-spin text-foreground-subtlest" />
          ) : (
            <Search className="size-4 shrink-0 text-foreground-subtlest" />
          )}
          <input
            ref={inputRef}
            data-testid="command-palette-input"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape') {
                e.preventDefault();
                setOpen(false);
              } else if (e.key === 'ArrowDown') {
                e.preventDefault();
                if (flatItems.length > 0) setActiveIdx((i) => (i + 1) % flatItems.length);
              } else if (e.key === 'ArrowUp') {
                e.preventDefault();
                if (flatItems.length > 0) setActiveIdx((i) => (i - 1 + flatItems.length) % flatItems.length);
              } else if (e.key === 'Enter') {
                e.preventDefault();
                const chosen = flatItems[activeIdx];
                if (chosen) void chosen.onSelect();
              }
            }}
            placeholder={
              formatMsg(locale, 'palette.searchConversationsMessageContentOr')
            }
            className="min-w-0 flex-1 bg-transparent text-ui-base text-foreground outline-none placeholder:text-foreground-subtlest"
          />
          <kbd className="rounded-md border border-border bg-surface px-1.5 py-0.5 font-mono text-[10px] text-foreground-subtlest">
            Esc
          </kbd>
        </div>

        {/* Results list */}
        <div className="max-h-[60vh] overflow-y-auto p-1.5">
          {flatItems.length === 0 ? (
            <div className="px-3 py-8 text-center text-ui-sm text-foreground-subtlest">
              {formatMsg(locale, 'palette.noMatchingConversationsOrCommands')}
            </div>
          ) : (
            <>
              {sessionItems.length > 0 && (
                <div className="mb-1.5">
                  <div className="px-2.5 py-1 text-[11px] font-medium text-foreground-subtlest">
                    {query.trim()
                      ? formatMsg(locale, 'palette.matchingConversations')
                      : formatMsg(locale, 'palette.recentConversations')}
                  </div>
                  {sessionItems.map((item, idx) => {
                    const Icon = item.icon;
                    const active = idx === activeIdx;
                    return (
                      <button
                        key={item.id}
                        type="button"
                        data-testid={`palette-item-${item.id}`}
                        onMouseEnter={() => setActiveIdx(idx)}
                        onClick={() => void item.onSelect()}
                        className={`flex w-full items-start gap-2.5 rounded-xl px-2.5 py-2 text-left transition-colors ${
                          active ? 'bg-menu-hover text-foreground' : 'text-foreground hover:bg-menu-hover/60'
                        }`}
                      >
                        <Icon className="mt-0.5 size-4 shrink-0 text-foreground-subtle" />
                        <div className="min-w-0 flex-1">
                          <div className="flex items-center gap-2">
                            <span className="truncate text-ui-sm font-medium">{item.title}</span>
                            {item.badge && (
                              <span className="shrink-0 rounded-full bg-surface px-1.5 py-px text-[10px] text-foreground-subtle">
                                {item.badge}
                              </span>
                            )}
                          </div>
                          {item.subtitle && (
                            <div className="mt-0.5 truncate text-ui-xs text-foreground-subtlest">{item.subtitle}</div>
                          )}
                        </div>
                      </button>
                    );
                  })}
                </div>
              )}

              {commands.length > 0 && (
                <div>
                  <div className="px-2.5 py-1 text-[11px] font-medium text-foreground-subtlest">
                    {formatMsg(locale, 'palette.commands')}
                  </div>
                  {commands.map((item, idx) => {
                    const flatIdx = sessionItems.length + idx;
                    const Icon = item.icon;
                    const active = flatIdx === activeIdx;
                    return (
                      <button
                        key={item.id}
                        type="button"
                        data-testid={`palette-item-${item.id}`}
                        onMouseEnter={() => setActiveIdx(flatIdx)}
                        onClick={() => void item.onSelect()}
                        className={`flex w-full items-center justify-between gap-2.5 rounded-xl px-2.5 py-2 text-left transition-colors ${
                          active ? 'bg-menu-hover text-foreground' : 'text-foreground hover:bg-menu-hover/60'
                        }`}
                      >
                        <div className="flex min-w-0 items-center gap-2.5">
                          <Icon className="size-4 shrink-0 text-foreground-subtle" />
                          <div className="min-w-0">
                            <div className="truncate text-ui-sm font-medium">{item.title}</div>
                            {item.subtitle && (
                              <div className="truncate text-ui-xs text-foreground-subtlest">{item.subtitle}</div>
                            )}
                          </div>
                        </div>
                        <div className="flex shrink-0 items-center gap-1.5">
                          {item.badge && (
                            <span className="rounded bg-surface px-1.5 py-0.5 font-mono text-[10px] text-foreground-subtle">
                              {item.badge}
                            </span>
                          )}
                          {item.shortcut && (
                            <kbd className="rounded border border-border bg-surface px-1.5 py-0.5 font-mono text-[10px] text-foreground-subtlest">
                              {item.shortcut}
                            </kbd>
                          )}
                        </div>
                      </button>
                    );
                  })}
                </div>
              )}
            </>
          )}
        </div>

        {/* Footer hints */}
        <div className="flex items-center justify-between border-t border-border bg-surface/40 px-3.5 py-1.5 text-[11px] text-foreground-subtlest">
          <span>{formatMsg(locale, 'palette.fullTextSearchAcrossTitles')}</span>
          <span className="font-mono">↑↓ · Enter · Esc</span>
        </div>
      </div>
    </div>
  );
}
