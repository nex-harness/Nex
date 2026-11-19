import {
  ArrowRight,
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  Circle,
  Ellipsis,
  FileDiff,
  GitBranch,
  GitCommitHorizontal,
  Minimize2,
  RefreshCw,
} from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { formatMsg } from '../i18n/messages';
import { useNexStore } from '../core/store';
import { ToolCallCard } from '../core/types';

function parseToolArgs(tc: ToolCallCard): Record<string, unknown> {
  try {
    const parsed = JSON.parse(tc.argsText || '{}');
    return parsed && typeof parsed === 'object' ? (parsed as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

function baseName(pathText: string): string {
  return pathText.split(/[\\/]/).filter(Boolean).pop() || pathText;
}

function shortCommand(raw: string): string {
  const cmd = raw.replace(/\s+/g, ' ').trim();
  return cmd.length > 46 ? `${cmd.slice(0, 46)}…` : cmd;
}

interface Milestone {
  id: string;
  label: string;
  state: 'completed' | 'running' | 'pending';
}

type PanelMode = 'panel' | 'mini';

export function ConversationStatusPanel() {
  const locale = useNexStore((s) => s.locale);
  const activeSessionId = useNexStore((s) => s.activeSessionId);
  const stream = useNexStore((s) => (s.activeSessionId ? s.streams[s.activeSessionId] : undefined));
  const gitSummary = useNexStore((s) => s.gitSummary);
  const openRightTab = useNexStore((s) => s.openRightTab);
  const sendPromptOrSteer = useNexStore((s) => s.sendPromptOrSteer);
  const refreshGitSummary = useNexStore((s) => s.refreshGitSummary);

  const [mode, setMode] = useState<PanelMode>('mini');
  const [menuOpen, setMenuOpen] = useState(false);
  const [autoExpand, setAutoExpand] = useState(true);
  const [commitOpen, setCommitOpen] = useState(false);
  const [commitPrompt, setCommitPrompt] = useState('');
  const menuRef = useRef<HTMLDivElement | null>(null);
  const prevGitChangesRef = useRef(0);

  const defaultCommitPrompt =
    locale === 'en-US'
      ? 'Review the current git status and diff, then create a clear commit message and commit the changes. Push as well if a remote is configured.'
      : '请先检查当前 git status 与 diff，然后生成语义清晰的提交信息并提交改动；如已配置远端则一并推送。';

  const milestones = useMemo<Milestone[]>(() => {
    const list: Milestone[] = [];
    for (const item of stream?.items || []) {
      for (const tc of item.toolCalls) {
        const args = parseToolArgs(tc);
        let label = tc.name;
        if (tc.name === 'read') {
          label = `${formatMsg(locale, 'tool.read')} ${baseName(String(args.file_path || args.path || ''))}`;
        } else if (tc.name === 'edit' || tc.name === 'write') {
          label = `${formatMsg(locale, tc.name === 'write' ? 'tool.wrote' : 'tool.edited')} ${baseName(
            String(args.file_path || args.path || ''),
          )}`;
        } else if (tc.name === 'bash') {
          label = `${formatMsg(locale, 'tool.ran')} ${shortCommand(String(args.command || ''))}`;
        } else if (tc.name === 'grep' || tc.name === 'glob') {
          label = `${formatMsg(locale, 'tool.searched')} ${String(args.pattern || args.query || '')}`;
        } else if (tc.name === 'subagent') {
          label = formatMsg(locale, 'tool.subagent');
        }
        list.push({
          id: tc.id,
          label,
          state: tc.status === 'completed' ? 'completed' : tc.status === 'error' ? 'pending' : 'running',
        });
      }
    }
    return list;
  }, [stream?.items, locale]);

  useEffect(() => {
    if (!menuOpen) return;
    const handleDown = (event: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(event.target as Node)) {
        setMenuOpen(false);
      }
    };
    document.addEventListener('mousedown', handleDown);
    return () => document.removeEventListener('mousedown', handleDown);
  }, [menuOpen]);

  useEffect(() => {
    const changes = (gitSummary?.additions || 0) + (gitSummary?.deletions || 0);
    const prev = prevGitChangesRef.current;
    prevGitChangesRef.current = changes;
    if (autoExpand && mode === 'mini' && changes > prev) {
      setMode('panel');
    }
  }, [gitSummary?.additions, gitSummary?.deletions, autoExpand, mode]);

  const items = stream?.items || [];
  const totalCalls = items.reduce((count, item) => count + item.toolCalls.length, 0);
  const isGitRepo = Boolean(gitSummary?.is_git_repo);
  const gitChanges = isGitRepo ? (gitSummary?.additions || 0) + (gitSummary?.deletions || 0) : 0;
  const hasActivity = totalCalls > 0 || Boolean(stream?.running);

  if (!activeSessionId || items.length === 0) return null;
  if (!(isGitRepo && gitChanges > 0) && !hasActivity) return null;

  const completedCount = milestones.filter((m) => m.state === 'completed').length;
  const totalCount = milestones.length;
  const shownMilestones = milestones.slice(-12);

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

  return (
    <div className="pointer-events-none absolute top-0 right-4 z-20 pt-3">
      <div className="pointer-events-auto">
        {mode === 'mini' ? (
          <button
            type="button"
            data-testid="conversation-status-panel"
            onClick={() => setMode('panel')}
            title={formatMsg(locale, 'chat.statusPanel.autoExpand')}
            className="flex h-8 items-center gap-2 rounded-full border border-border bg-popover px-3 text-ui-sm text-foreground shadow-md transition-colors hover:bg-surface-hover"
          >
            <FileDiff className="size-3.5 shrink-0 text-foreground-subtle" />
            {isGitRepo && (
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
        ) : (
          <div
            data-testid="conversation-status-panel"
            className="w-76 rounded-2xl border border-border bg-popover p-2 text-foreground shadow-md"
          >
            <div className="flex items-center justify-between px-1 pb-1">
              <span className="text-ui-sm font-medium text-foreground-subtle">
                {formatMsg(locale, 'chat.statusPanel.environment')}
              </span>
              <div className="relative flex items-center gap-0.5" ref={menuRef}>
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
                  <div className="absolute right-0 top-7 z-30 w-44 rounded-lg border border-border bg-menu p-1 shadow-md">
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
                      <span>{locale === 'en-US' ? 'Refresh changes' : '刷新变更'}</span>
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

            {isGitRepo && (
              <div className="space-y-0.5">
                <button
                  type="button"
                  data-testid="status-panel-changes"
                  onClick={() => void openRightTab('review')}
                  className="flex w-full items-center gap-2 rounded-lg px-1.5 py-1 text-left text-ui-sm text-foreground transition-colors hover:bg-hover"
                >
                  <FileDiff className="size-4 shrink-0 text-foreground-subtle" />
                  <span className="min-w-0 flex-1 truncate">{formatMsg(locale, 'chat.statusPanel.changes')}</span>
                  <span className="shrink-0 font-mono text-ui-xs tabular-nums">
                    <span className="text-success">+{gitSummary?.additions || 0}</span>{' '}
                    <span className="text-destructive">-{gitSummary?.deletions || 0}</span>
                  </span>
                </button>

                <div className="flex w-full items-center gap-2 rounded-lg px-1.5 py-1 text-ui-sm text-foreground-subtle">
                  <GitBranch className="size-4 shrink-0 text-foreground-subtle" />
                  <span className="min-w-0 flex-1 truncate font-mono text-ui-xs">{gitSummary?.branch || 'HEAD'}</span>
                  <ChevronDown className="size-3.5 shrink-0 text-foreground-subtlest" />
                </div>

                <button
                  type="button"
                  data-testid="status-panel-commit"
                  onClick={() => (commitOpen ? setCommitOpen(false) : startCommit())}
                  className="flex w-full items-center gap-2 rounded-lg px-1.5 py-1 text-left text-ui-sm text-foreground transition-colors hover:bg-hover"
                >
                  <GitCommitHorizontal className="size-4 shrink-0 text-foreground-subtle" />
                  <span className="min-w-0 flex-1 truncate">{formatMsg(locale, 'git.actionMenu.trigger')}</span>
                  {commitOpen ? (
                    <ChevronUp className="size-3.5 shrink-0 text-foreground-subtlest" />
                  ) : (
                    <ChevronDown className="size-3.5 shrink-0 text-foreground-subtlest" />
                  )}
                </button>

                {commitOpen && (
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
                        {locale === 'en-US' ? 'Cancel' : '取消'}
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
                )}
              </div>
            )}

            {totalCount > 0 && (
              <div className={`${isGitRepo ? 'mt-1.5 border-t border-border pt-2' : 'pt-0.5'}`}>
                <div className="flex items-center justify-between px-1 pb-1">
                  <span className="text-ui-sm font-medium text-foreground-subtle">
                    {formatMsg(locale, 'chat.statusPanel.todo')}
                  </span>
                  <span className="font-mono text-ui-xs tabular-nums text-foreground-subtlest">
                    {completedCount}/{totalCount}
                  </span>
                </div>
                <div className="max-h-48 space-y-0.5 overflow-y-auto pr-0.5">
                  {shownMilestones.map((milestone) => (
                    <div key={milestone.id} className="flex items-center gap-2 rounded-md px-1.5 py-0.5 text-ui-sm">
                      {milestone.state === 'completed' ? (
                        <CheckCircle2 className="size-3.5 shrink-0 text-success" />
                      ) : milestone.state === 'running' ? (
                        <ArrowRight className="size-3.5 shrink-0 text-foreground" />
                      ) : (
                        <Circle className="size-3.5 shrink-0 text-foreground-subtlest" />
                      )}
                      <span
                        className={`min-w-0 flex-1 truncate ${
                          milestone.state === 'completed'
                            ? 'text-success line-through'
                            : milestone.state === 'running'
                              ? 'font-medium text-foreground'
                              : 'text-foreground-subtlest'
                        }`}
                      >
                        {milestone.label}
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
