import {
  ArrowRight,
  Bot,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  ChevronUp,
  Circle,
  Ellipsis,
  FileDiff,
  GitBranch,
  GitCommitHorizontal,
  Maximize2,
  Minimize2,
  Pause,
  RefreshCw,
  SquareTerminal,
  Target,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { formatMsg } from '../i18n/messages';
import { useNexStore } from '../core/store';
import { ChatTurnItem, GitSummary, GoalEntry, ToolCallCard, TodoItem } from '../core/types';

export type StatusPanelMode = 'auto' | 'panel' | 'mini';

function hasGitChanges(gitSummary: GitSummary | null | undefined): boolean {
  return Boolean(
    gitSummary?.is_git_repo &&
      (gitSummary.changed_files > 0 || gitSummary.additions + gitSummary.deletions > 0),
  );
}

interface BackgroundBashEntry {
  tc: ToolCallCard;
  command: string;
  description: string;
}

function extractBackgroundBashEntries(items: ChatTurnItem[], sessionRunning: boolean): BackgroundBashEntry[] {
  const stoppedTaskIds = new Set<string>();
  for (const item of items) {
    for (const tc of item.toolCalls) {
      if (tc.name !== 'task_stop' || tc.status !== 'completed') continue;
      const args = parseToolArgs(tc);
      const tid = String(args.task_id || '').trim();
      if (tid) stoppedTaskIds.add(tid);
    }
  }

  const list: BackgroundBashEntry[] = [];
  for (const item of items) {
    for (const tc of item.toolCalls) {
      if (tc.name !== 'bash') continue;
      const args = parseToolArgs(tc);
      if (!args.run_in_background) continue;
      if (tc.status === 'running') {
        list.push({
          tc,
          command: String(args.command || ''),
          description: String(args.description || '').trim(),
        });
        continue;
      }
      if (tc.status === 'completed' && sessionRunning) {
        const m = /Started background task ([A-Za-z0-9_-]+)/.exec(tc.finalOutput || tc.streamOutput || '');
        const tid = m?.[1] || '';
        if (tid && !stoppedTaskIds.has(tid)) {
          list.push({
            tc,
            command: String(args.command || ''),
            description: String(args.description || '').trim(),
          });
        }
      }
    }
  }
  return list;
}

/** Shared visibility selector so ConversationPane layout spacing and StatusPanel agree 1:1. */
export function useConversationStatusPanelHasContent(): boolean {
  const activeSessionId = useNexStore((s) => s.activeSessionId);
  const stream = useNexStore((s) => (s.activeSessionId ? s.streams[s.activeSessionId] : undefined));
  const gitSummary = useNexStore((s) => s.gitSummary);
  const bottomTerminals = useNexStore((s) => s.bottomTerminals);
  const rightTabs = useNexStore((s) => s.rightTabs);

  if (!activeSessionId) return false;
  if (hasGitChanges(gitSummary)) return true;
  if ((stream?.goals || []).length > 0) return true;
  if ((stream?.todos || []).length > 0) return true;
  if (bottomTerminals.length > 0 || rightTabs.some((t) => t.kind === 'terminal')) return true;
  if ((stream?.backgroundTasks || []).length > 0) return true;
  if (extractBackgroundBashEntries(stream?.items || [], Boolean(stream?.running)).length > 0) return true;
  for (const item of stream?.items || []) {
    for (const tc of item.toolCalls) {
      if (tc.name === 'subagent') return true;
    }
  }
  return false;
}

function parseToolArgs(tc: ToolCallCard): Record<string, unknown> {
  try {
    const parsed = JSON.parse(tc.argsText || '{}');
    return parsed && typeof parsed === 'object' ? (parsed as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

function shortCommand(raw: string): string {
  const cmd = raw.replace(/\s+/g, ' ').trim();
  return cmd.length > 46 ? `${cmd.slice(0, 46)}…` : cmd;
}

/** `2h 1m 36s` / `1m 24s` / `14s` — matching the reference screenshots. */
export function formatElapsed(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h > 0) return `${h}h ${m}m ${s}s`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

type PanelMode = 'panel' | 'mini';

interface TerminalEntry {
  id: string;
  title: string;
  startedAt: number;
  kind: 'pty' | 'bash';
  testId?: string;
  onStop: () => void;
}

export function ConversationStatusPanel({
  mode: controlledMode,
  onModeChange,
}: {
  mode?: StatusPanelMode;
  onModeChange?: (next: StatusPanelMode) => void;
} = {}) {
  const locale = useNexStore((s) => s.locale);
  const activeSessionId = useNexStore((s) => s.activeSessionId);
  const stream = useNexStore((s) => (s.activeSessionId ? s.streams[s.activeSessionId] : undefined));
  const gitSummary = useNexStore((s) => s.gitSummary);
  const bottomTerminals = useNexStore((s) => s.bottomTerminals);
  const rightTabs = useNexStore((s) => s.rightTabs);
  const openRightTab = useNexStore((s) => s.openRightTab);
  const openSubagentTab = useNexStore((s) => s.openSubagentTab);
  const sendPromptOrSteer = useNexStore((s) => s.sendPromptOrSteer);
  const refreshGitSummary = useNexStore((s) => s.refreshGitSummary);
  const closeBottomTerminal = useNexStore((s) => s.closeBottomTerminal);
  const closeRightTab = useNexStore((s) => s.closeRightTab);
  const abortActiveSession = useNexStore((s) => s.abortActiveSession);
  const stopBackgroundTask = useNexStore((s) => s.stopBackgroundTask);

  const [internalMode, setInternalMode] = useState<StatusPanelMode>('auto');
  const mode = controlledMode ?? internalMode;
  const setMode = useCallback(
    (next: StatusPanelMode) => {
      if (onModeChange) onModeChange(next);
      else setInternalMode(next);
    },
    [onModeChange],
  );

  const [menuOpen, setMenuOpen] = useState(false);
  const [autoExpand, setAutoExpand] = useState(true);
  const [commitOpen, setCommitOpen] = useState(false);
  const [commitPrompt, setCommitPrompt] = useState('');
  const [endedOpen, setEndedOpen] = useState(false);
  const [, setTick] = useState(0);
  const panelRef = useRef<HTMLElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const prevGitChangesRef = useRef(0);
  const terminalSeenRef = useRef<Record<string, number>>({});
  const bashSeenRef = useRef<Record<string, number>>({});
  const defaultCommitPrompt =
    formatMsg(locale, 'statusPanel.reviewTheCurrentGitStatus');

  const todos: TodoItem[] = stream?.todos || [];
  const goals = stream?.goals || [];
  const items = stream?.items || [];
  const bgTasks = useMemo(() => stream?.backgroundTasks || [], [stream?.backgroundTasks]);
  const bgBashTasks = useMemo(() => bgTasks.filter((t) => t.kind === 'local_bash'), [bgTasks]);
  const bgAgentTasks = useMemo(() => bgTasks.filter((t) => t.kind === 'local_agent'), [bgTasks]);

  const subagents = useMemo(() => {
    const list: Array<{ tc: ToolCallCard; summary: string }> = [];
    for (const item of items) {
      for (const tc of item.toolCalls) {
        if (tc.name !== 'subagent') continue;
        const args = parseToolArgs(tc);
        const summary = String(args.description || args.prompt || args.title || '')
          .replace(/\s+/g, ' ')
          .trim();
        list.push({ tc, summary });
      }
    }
    return list;
  }, [items]);

  const runningSubagents = subagents.filter((a) => a.tc.status === 'running');
  const endedSubagents = subagents.filter((a) => a.tc.status !== 'running');

  const runningBash = useMemo(
    () => extractBackgroundBashEntries(items, Boolean(stream?.running)),
    [items, stream?.running],
  );

  // Track first-seen timestamps for right-pane terminal tabs (WorkbenchTab has no createdAt).
  const rightTerminalTabs = rightTabs.filter((t) => t.kind === 'terminal');
  for (const tab of rightTerminalTabs) {
    if (!terminalSeenRef.current[tab.id]) terminalSeenRef.current[tab.id] = Date.now();
  }

  const activeTerminals = useMemo<TerminalEntry[]>(() => {
    const list: TerminalEntry[] = [];
    for (const term of bottomTerminals) {
      list.push({
        id: term.id,
        title: term.title,
        startedAt: term.createdAt,
        kind: 'pty',
        onStop: () => closeBottomTerminal(term.id),
      });
    }
    for (const tab of rightTerminalTabs) {
      list.push({
        id: tab.id,
        title: tab.title,
        startedAt: terminalSeenRef.current[tab.id] || Date.now(),
        kind: 'pty',
        onStop: () => closeRightTab(tab.id),
      });
    }
    const bgBashIds = new Set(bgBashTasks.map((t) => t.task_id));
    for (const bash of runningBash) {
      const m = /Started background task ([A-Za-z0-9_-]+)/.exec(bash.tc.finalOutput || bash.tc.streamOutput || '');
      const tid = m?.[1] || '';
      if (tid && bgBashIds.has(tid)) continue;
      if (!bashSeenRef.current[bash.tc.id]) bashSeenRef.current[bash.tc.id] = Date.now();
      list.push({
        id: bash.tc.id,
        title: bash.description || shortCommand(bash.command) || 'bash',
        startedAt: bashSeenRef.current[bash.tc.id],
        kind: 'bash',
        testId: tid ? `stop-bg-task-${tid}` : undefined,
        onStop: tid ? () => stopBackgroundTask(tid) : () => abortActiveSession(),
      });
    }
    for (const bt of bgBashTasks) {
      list.push({
        id: bt.task_id,
        title: bt.title || bt.task_id,
        startedAt: bt.started_at || Date.now(),
        kind: 'bash',
        testId: `stop-bg-task-${bt.task_id}`,
        onStop: () => stopBackgroundTask(bt.task_id),
      });
    }
    return list;
  }, [
    bottomTerminals,
    rightTerminalTabs,
    runningBash,
    bgBashTasks,
    closeBottomTerminal,
    closeRightTab,
    abortActiveSession,
    stopBackgroundTask,
  ]);

  const hasGit = hasGitChanges(gitSummary);
  const hasGoal = goals.length > 0;
  const hasProgress = todos.length > 0;
  const hasTerminals = activeTerminals.length > 0;
  const hasAgents = subagents.length > 0 || bgAgentTasks.length > 0;

  const completedCount = todos.filter((t) => t.status === 'completed').length;
  const totalCount = todos.length;

  const needsTicker = hasGoal || hasTerminals || bgAgentTasks.length > 0;
  useEffect(() => {
    if (!needsTicker) return;
    const timer = window.setInterval(() => setTick((v) => v + 1), 1000);
    return () => window.clearInterval(timer);
  }, [needsTicker]);

  useEffect(() => {
    if (!menuOpen && mode !== 'panel') return;
    const handleDown = (event: MouseEvent) => {
      if (menuOpen && menuRef.current && !menuRef.current.contains(event.target as Node)) {
        setMenuOpen(false);
      }
      if (mode === 'panel' && panelRef.current && !panelRef.current.contains(event.target as Node)) {
        const containerEl = panelRef.current.closest('[data-conversation-container="true"]');
        const containerWidth = containerEl
          ? containerEl.getBoundingClientRect().width
          : typeof window !== 'undefined'
            ? window.innerWidth
            : 0;
        if (containerWidth > 0 && containerWidth < 960) {
          setMenuOpen(false);
          setMode('mini');
        }
      }
    };
    document.addEventListener('mousedown', handleDown);
    return () => document.removeEventListener('mousedown', handleDown);
  }, [menuOpen, mode, setMode]);

  useEffect(() => {
    const changes = (gitSummary?.additions || 0) + (gitSummary?.deletions || 0);
    const prev = prevGitChangesRef.current;
    prevGitChangesRef.current = changes;
    if (autoExpand && mode === 'mini' && changes > prev) {
      setMode('auto');
    }
  }, [gitSummary?.additions, gitSummary?.deletions, autoExpand, mode, setMode]);

  if (!activeSessionId || !(hasGit || hasGoal || hasProgress || hasTerminals || hasAgents)) return null;

  const startCommit = () => {
    setCommitPrompt(defaultCommitPrompt);
    setCommitOpen(true);
    setMenuOpen(false);
  };

  const submitCommit = () => {
    const text = commitPrompt.trim();
    if (!text) return;
    sendPromptOrSteer(text);
    setCommitOpen(false);
  };

  const renderGoalRow = (g: GoalEntry, active: boolean) => (
    <div key={g.id} className="flex items-start gap-2">
      {active ? (
        <span className="mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border border-success text-[10px] font-semibold text-success">
          {g.index}
        </span>
      ) : (
        <Target className="mt-0.5 size-4 shrink-0 text-foreground-subtlest" />
      )}
      <span className="line-clamp-2 flex-1 text-ui-base leading-snug text-foreground">{g.title}</span>
      {g.totalTodos > 0 && (
        <span className="shrink-0 pt-0.5 tabular-nums text-ui-sm text-foreground-subtlest">
          {g.completedTodos}/{g.totalTodos}
        </span>
      )}
    </div>
  );

  const activeGoal = goals.length > 0 ? goals[goals.length - 1] : null;
  const earlierGoals = goals.slice(0, Math.max(0, goals.length - 1)).reverse();
  const goalElapsed = activeGoal
    ? formatElapsed((activeGoal.completedAt || Date.now()) - activeGoal.startedAt)
    : '';

  return (
    <div className="pointer-events-none absolute top-0 right-4 z-20 pt-3">
      <aside
        ref={panelRef}
        data-testid="conversation-status-panel"
        data-display-mode={mode}
        data-state={mode === 'mini' ? 'collapsed' : 'expanded'}
        className={`pointer-events-auto relative flex-col overflow-hidden rounded-2xl border border-border bg-popover text-foreground shadow-md transition-[background-color,box-shadow] duration-200 ease-out ${
          mode === 'mini'
            ? 'inline-flex max-h-8.5 w-max max-w-[min(20rem,calc(100cqw-2rem))]'
            : mode === 'panel'
              ? 'nex-popover-down flex max-h-[min(74dvh,38rem)] w-80 max-w-[calc(100cqw-2rem)]'
              : 'inline-flex max-h-8.5 w-max max-w-[min(20rem,calc(100cqw-2rem))] @min-[1280px]/conversation:flex @min-[1280px]/conversation:max-h-[min(74dvh,38rem)] @min-[1280px]/conversation:w-80'
        }`}
      >
        {/* Mini capsule trigger: shown in 'mini' mode or below 1280px in 'auto' mode */}
        <button
          type="button"
          data-testid="status-panel-mini"
          onClick={() => setMode('panel')}
          title={formatMsg(locale, 'chat.statusPanel.autoExpand')}
          className={`group h-8 items-center gap-2 px-3 text-ui-sm text-foreground transition-colors hover:bg-surface-hover ${
            mode === 'mini'
              ? 'flex'
              : mode === 'panel'
                ? 'hidden'
                : 'flex @min-[1280px]/conversation:hidden'
          }`}
        >
          <span className="relative size-3.5 shrink-0">
            <FileDiff className="absolute inset-0 size-3.5 text-foreground-subtle transition-opacity duration-150 group-hover:opacity-0" />
            <Maximize2 className="absolute inset-0 size-3.5 text-foreground opacity-0 transition-opacity duration-150 group-hover:opacity-100" />
          </span>
          {gitSummary?.is_git_repo && (
            <span className="flex items-center gap-1 font-mono text-ui-xs tabular-nums">
              <span className="text-success">+{gitSummary?.additions || 0}</span>
              <span className="text-destructive">-{gitSummary?.deletions || 0}</span>
            </span>
          )}
          {totalCount > 0 && (
            <span className="font-mono text-ui-xs tabular-nums text-foreground-subtle">
              {completedCount}/{totalCount}
            </span>
          )}
          <ChevronDown className="size-3.5 shrink-0 text-foreground-subtlest" />
        </button>

        {/* Expanded panel shell: shown in 'panel' mode or >= 1280px in 'auto' mode */}
        <div
          className={
            mode === 'mini'
              ? 'hidden'
              : mode === 'panel'
                ? 'flex min-h-0 flex-1 flex-col'
                : 'hidden min-h-0 flex-1 flex-col @min-[1280px]/conversation:flex'
          }
        >
          {/* Sticky top-right control overlay */}
          <div className="relative">
            <div className="absolute right-3 top-3 z-10 flex items-center gap-0.5" ref={menuRef}>
              <button
                type="button"
                title={formatMsg(locale, 'chat.statusPanel.autoExpand')}
                onClick={() => setMenuOpen((v) => !v)}
                className="flex size-6 items-center justify-center rounded-md text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
              >
                <Ellipsis className="size-4" />
              </button>
              <button
                type="button"
                data-testid="status-panel-collapse"
                title={formatMsg(locale, 'chat.statusPanel.collapse')}
                onClick={() => {
                  setMenuOpen(false);
                  setMode('mini');
                }}
                className="flex size-6 items-center justify-center rounded-md text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
              >
                <Minimize2 className="size-4" />
              </button>

              {menuOpen && (
                <div className="nex-popover-down absolute right-0 top-7 z-30 w-44 rounded-lg border border-border bg-menu p-1 shadow-md">
                  <button
                    type="button"
                    onClick={() => {
                      setMode('auto');
                      setMenuOpen(false);
                    }}
                    className="flex h-7 w-full items-center justify-between rounded-md px-2 text-left text-ui-sm text-foreground hover:bg-menu-hover"
                  >
                    <span>{formatMsg(locale, 'chat.statusPanel.modeAuto')}</span>
                    {mode === 'auto' && <Check className="size-3.5 text-success" />}
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      setAutoExpand((v) => !v);
                      setMenuOpen(false);
                    }}
                    className="flex h-7 w-full items-center justify-between rounded-md px-2 text-left text-ui-sm text-foreground hover:bg-menu-hover"
                  >
                    <span>{formatMsg(locale, 'chat.statusPanel.autoExpand')}</span>
                    <span className="font-mono text-ui-xs text-foreground-subtlest">{autoExpand ? 'ON' : 'OFF'}</span>
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      void refreshGitSummary(true);
                      setMenuOpen(false);
                    }}
                    className="flex h-7 w-full items-center gap-2 rounded-md px-2 text-left text-ui-sm text-foreground hover:bg-menu-hover"
                  >
                    <RefreshCw className="size-3.5 text-foreground-subtle" />
                    <span>{formatMsg(locale, 'statusPanel.refreshChanges')}</span>
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      setMenuOpen(false);
                      setMode('mini');
                    }}
                    className="flex h-7 w-full items-center gap-2 rounded-md px-2 text-left text-ui-sm text-foreground hover:bg-menu-hover"
                  >
                    <Minimize2 className="size-3.5 text-foreground-subtle" />
                    <span>{formatMsg(locale, 'chat.statusPanel.collapse')}</span>
                  </button>
                </div>
              )}
            </div>
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto px-3.5 pb-3.5 pt-3 space-y-3.5">
              {(() => {
                const firstSection: 'git' | 'goal' | 'progress' | 'terminals' | 'agents' = hasGit
                  ? 'git'
                  : hasGoal
                    ? 'goal'
                    : hasProgress
                      ? 'progress'
                      : hasTerminals
                        ? 'terminals'
                        : 'agents';
                return (
                  <>
              {/* 1. Git tools */}
              {hasGit && (
                <section className="space-y-0.5">
                  <div className="flex min-h-6 items-center pr-14 text-ui-sm font-medium text-foreground-subtle">
                    {formatMsg(locale, 'chat.statusPanel.environment')}
                  </div>

                  <button
                    type="button"
                    data-testid="status-panel-changes"
                    onClick={() => void openRightTab('review')}
                    className="flex w-full items-center gap-2 rounded-lg py-1 text-left text-ui-base text-foreground transition-colors hover:bg-hover"
                  >
                    <FileDiff className="size-4 shrink-0 text-foreground-subtle" />
                    <span className="min-w-0 flex-1 truncate">{formatMsg(locale, 'chat.statusPanel.changes')}</span>
                    <span className="shrink-0 font-mono text-ui-base tabular-nums">
                      <span className="text-success">+{gitSummary?.additions || 0}</span>{' '}
                      <span className="text-destructive">-{gitSummary?.deletions || 0}</span>
                    </span>
                  </button>

                  <div className="flex w-full items-center gap-2 rounded-lg py-1 text-ui-base text-foreground">
                    <GitBranch className="size-4 shrink-0 text-foreground-subtle" />
                    <span className="min-w-0 flex-1 truncate">{gitSummary?.branch || 'HEAD'}</span>
                    <ChevronDown className="size-3.5 shrink-0 text-foreground-subtlest" />
                  </div>

                  <button
                    type="button"
                    data-testid="status-panel-commit"
                    onClick={() => (commitOpen ? setCommitOpen(false) : startCommit())}
                    className="flex w-full items-center gap-2 rounded-lg py-1 text-left text-ui-base text-foreground transition-colors hover:bg-hover"
                  >
                    <GitCommitHorizontal className="size-4 shrink-0 text-foreground-subtle" />
                    <span className="min-w-0 flex-1 truncate">{formatMsg(locale, 'git.actionMenu.trigger')}</span>
                    {commitOpen ? (
                      <ChevronUp className="size-3.5 shrink-0 text-foreground-subtlest" />
                    ) : (
                      <ChevronDown className="size-3.5 shrink-0 text-foreground-subtlest" />
                    )}
                  </button>

                  <div className="nex-collapsible" data-open={commitOpen}>
                    <div className="nex-collapsible-inner">
                      <div className="rounded-lg border border-border bg-surface p-2">
                        <textarea
                          rows={3}
                          value={commitPrompt}
                          onChange={(e) => setCommitPrompt(e.target.value)}
                          placeholder={formatMsg(locale, 'git.actionMenu.trigger')}
                          className="w-full resize-none bg-transparent text-ui-sm leading-relaxed text-foreground placeholder-foreground-subtlest"
                        />
                        <div className="mt-1 flex justify-end gap-1.5">
                          <button
                            type="button"
                            onClick={() => setCommitOpen(false)}
                            className="h-6 rounded-md px-2 text-ui-xs text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
                          >
                            {formatMsg(locale, 'common.cancel')}
                          </button>
                          <button
                            type="button"
                            disabled={!commitPrompt.trim()}
                            onClick={submitCommit}
                            className="h-6 rounded-md bg-primary px-2.5 text-ui-xs font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-30"
                          >
                            {formatMsg(locale, 'chat.send')}
                          </button>
                        </div>
                      </div>
                    </div>
                  </div>
                </section>
              )}

              {/* 2. Goal */}
              {hasGoal && (
                <section className={`space-y-2 ${firstSection !== 'goal' ? 'border-t border-border pt-3' : ''}`}>
                  <div className={`flex items-center gap-1.5 ${firstSection === 'goal' ? 'min-h-6 pr-14' : ''}`}>
                    <span className="text-ui-sm font-medium text-foreground-subtle">
                      {formatMsg(locale, 'chat.statusPanel.goal')}
                    </span>
                    <span className="tabular-nums text-ui-sm text-foreground-subtlest">{goalElapsed}</span>
                    <span className="text-ui-sm text-foreground-subtlest">·</span>
                    {activeGoal?.status === 'running' ? (
                      <Pause className="size-3.5 shrink-0 text-foreground-subtle" />
                    ) : (
                      <CheckCircle2 className="size-4 shrink-0 text-success" />
                    )}
                  </div>

                  <div className="space-y-2">
                    {activeGoal && renderGoalRow(activeGoal, true)}
                    {earlierGoals.map((g) => renderGoalRow(g, false))}
                  </div>
                </section>
              )}

              {/* 3. Progress — strictly stream.todos written by todo_write */}
              {hasProgress && (
                <section
                  data-testid="status-panel-progress-section"
                  className={firstSection !== 'progress' ? 'border-t border-border pt-3' : ''}
                >
                  <div
                    data-testid="status-panel-progress-header"
                    className={`flex items-center justify-between gap-2 ${firstSection === 'progress' ? 'min-h-6 pr-14' : ''}`}
                  >
                    <span className="text-ui-sm font-medium text-foreground-subtle">
                      {formatMsg(locale, 'chat.statusPanel.todo')}
                    </span>
                    <span
                      className={`text-ui-sm font-medium tabular-nums ${
                        completedCount === totalCount ? 'text-success' : 'text-foreground-subtlest'
                      }`}
                    >
                      {completedCount}/{totalCount}
                    </span>
                  </div>

                  <div className="mt-2 space-y-2">
                    {todos.map((todo, idx) => (
                      <div key={`${idx}_${todo.content}`} className="flex items-start gap-2">
                        {todo.status === 'completed' ? (
                          <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-success" />
                        ) : todo.status === 'in_progress' ? (
                          <ArrowRight className="mt-0.5 size-4 shrink-0 text-foreground" />
                        ) : (
                          <Circle className="mt-0.5 size-4 shrink-0 text-foreground-subtlest" />
                        )}
                        <span
                          className={`line-clamp-2 text-ui-base leading-snug ${
                            todo.status === 'completed'
                              ? 'text-foreground-subtle line-through'
                              : todo.status === 'in_progress'
                                ? 'font-medium text-foreground'
                                : 'text-foreground-subtle'
                          }`}
                        >
                          {todo.content}
                        </span>
                      </div>
                    ))}
                  </div>
                </section>
              )}

              {/* 4. Terminals */}
              {hasTerminals && (
                <section className={firstSection !== 'terminals' ? 'border-t border-border pt-3' : ''}>
                  <div className={`flex items-center justify-between gap-2 pb-2 ${firstSection === 'terminals' ? 'min-h-6 pr-14' : ''}`}>
                    <span className="text-ui-sm font-medium text-foreground-subtle">Terminals</span>
                    <span className="text-ui-sm text-foreground-subtlest">
                      {activeTerminals.length} in background
                    </span>
                  </div>

                  <div className="space-y-2">
                    {activeTerminals.map((term) => (
                      <div key={`${term.kind}_${term.id}`} className="rounded-xl bg-surface p-3 space-y-2">
                        <div className="flex items-start gap-2">
                          <SquareTerminal className="mt-0.5 size-4 shrink-0 text-foreground-subtle" />
                          <span className="line-clamp-2 text-ui-base font-medium leading-snug text-foreground">
                            {term.title}
                          </span>
                        </div>
                        <div className="flex items-center justify-between pl-6">
                          <span className="text-ui-sm tabular-nums text-foreground-subtle">
                            Running for {formatElapsed(Date.now() - term.startedAt)}
                          </span>
                          <button
                            type="button"
                            data-testid={term.testId}
                            onClick={term.onStop}
                            className="inline-flex items-center gap-1.5 rounded-md bg-black/[0.06] px-2.5 py-1 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover dark:bg-white/[0.1]"
                          >
                            <span className="size-2.5 rounded-[2px] bg-foreground" />
                            {formatMsg(locale, 'chat.statusPanel.stopTask')}
                          </button>
                        </div>
                      </div>
                    ))}
                  </div>
                </section>
              )}

              {/* 5. Agents */}
              {hasAgents && (
                <section className={firstSection !== 'agents' ? 'border-t border-border pt-3' : ''}>
                  <div className={`text-ui-sm font-medium text-foreground-subtle ${firstSection === 'agents' ? 'flex min-h-6 items-center pr-14' : ''}`}>
                    {formatMsg(locale, 'chat.statusPanel.agents')}
                  </div>

                  {bgAgentTasks.length > 0 && (
                    <div className="mt-2 space-y-2">
                      {bgAgentTasks.map((bt) => (
                        <div key={bt.task_id} className="rounded-xl bg-surface p-3 space-y-2">
                          <div className="flex items-start gap-2">
                            <Bot className="mt-0.5 size-4 shrink-0 animate-pulse text-success" />
                            <div className="min-w-0 flex-1">
                              <div className="truncate text-ui-xs font-medium text-foreground-subtle">
                                {formatMsg(locale, 'chat.statusPanel.backgroundAgent')}
                              </div>
                              <div className="line-clamp-2 text-ui-base font-medium leading-snug text-foreground">
                                {bt.title || bt.task_id}
                              </div>
                            </div>
                          </div>
                          <div className="flex items-center justify-between pl-6">
                            <span className="text-ui-sm tabular-nums text-foreground-subtle">
                              {formatElapsed(Date.now() - (bt.started_at || Date.now()))}
                            </span>
                            <button
                              type="button"
                              data-testid={`stop-bg-task-${bt.task_id}`}
                              onClick={() => stopBackgroundTask(bt.task_id)}
                              className="inline-flex items-center gap-1.5 rounded-md bg-black/[0.06] px-2.5 py-1 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover dark:bg-white/[0.1]"
                            >
                              <span className="size-2.5 rounded-[2px] bg-foreground" />
                              {formatMsg(locale, 'chat.statusPanel.stopTask')}
                            </button>
                          </div>
                        </div>
                      ))}
                    </div>
                  )}

                  {runningSubagents.length > 0 && (
                    <div className="mt-2 space-y-1.5">
                      {runningSubagents.map(({ tc, summary }) => {
                        const args = parseToolArgs(tc);
                        const agentName = String(args.agent || args.name || 'Subagent');
                        const title = summary || agentName;
                        return (
                          <button
                            key={tc.id}
                            type="button"
                            onClick={() =>
                              openSubagentTab({
                                toolCallId: tc.id,
                                title,
                                agentName,
                                status: 'running',
                                prompt: String(args.prompt || args.description || ''),
                                resultText: tc.finalOutput || tc.streamOutput || '',
                              })
                            }
                            className="flex w-full items-start gap-2 rounded-lg px-1 py-1 text-left transition-colors hover:bg-hover"
                          >
                            <Bot className="mt-0.5 size-4 shrink-0 animate-pulse text-success" />
                            <div className="min-w-0 flex-1">
                              <div className="truncate text-ui-xs font-medium text-foreground-subtle">
                                {agentName}
                              </div>
                              <div className="line-clamp-2 text-ui-base leading-snug text-foreground">
                                {title}
                              </div>
                            </div>
                          </button>
                        );
                      })}
                    </div>
                  )}

                  {endedSubagents.length > 0 && (
                    <div className="mt-1">
                      <button
                        type="button"
                        onClick={() => setEndedOpen((v) => !v)}
                        className="flex w-full items-center justify-between rounded-lg px-1 py-1.5 text-ui-base transition-colors hover:bg-hover"
                      >
                        <span className="flex min-w-0 items-center gap-2">
                          <CheckCircle2 className="size-4 shrink-0 text-foreground-subtle" />
                          <span className="text-foreground">Ended</span>
                        </span>
                        <span className="flex shrink-0 items-center gap-2">
                          <span className="tabular-nums text-foreground-subtle">{endedSubagents.length}</span>
                          <ChevronRight className={`size-4 text-foreground-subtlest transition-transform ${endedOpen ? 'rotate-90' : ''}`} />
                        </span>
                      </button>

                      <div className="nex-collapsible" data-open={endedOpen}>
                        <div className="nex-collapsible-inner">
                          <div className="space-y-1 pb-1 pl-6 pr-1">
                            {endedSubagents.map(({ tc, summary }) => {
                              const args = parseToolArgs(tc);
                              const agentName = String(args.agent || args.name || 'Subagent');
                              const title = summary || agentName;
                              return (
                                <button
                                  key={tc.id}
                                  type="button"
                                  onClick={() =>
                                    openSubagentTab({
                                      toolCallId: tc.id,
                                      title,
                                      agentName,
                                      status: tc.status === 'error' ? 'error' : 'done',
                                      prompt: String(args.prompt || args.description || ''),
                                      resultText: tc.finalOutput || tc.streamOutput || '',
                                    })
                                  }
                                  className="flex w-full flex-col rounded-md px-2 py-1 text-left transition-colors hover:bg-hover"
                                >
                                  <span className="truncate text-ui-xs font-medium text-foreground-subtle">
                                    {agentName}
                                  </span>
                                  <span className="line-clamp-2 text-ui-sm leading-snug text-foreground">
                                    {title}
                                  </span>
                                </button>
                              );
                            })}
                          </div>
                        </div>
                      </div>
                    </div>
                  )}
                </section>
              )}
                  </>
                );
              })()}
            </div>
        </div>
      </aside>
    </div>
  );
}
