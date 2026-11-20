import {
  Bot,
  Check,
  ChevronDown,
  ChevronRight,
  FileCode,
  FileDiff,
  Folder,
  FolderOpen,
  GitBranch,
  GitCommitHorizontal,
  MessageSquare,
  MessageSquareText,
  Minus,
  PanelRightClose,
  Plus,
  RefreshCw,
  RotateCcw,
  SquareTerminal,
  X,
} from 'lucide-react';
import { type ReactNode, useEffect, useState } from 'react';
import { useNexStore } from '../core/store';
import { DirEntry, FilePreview, WorkbenchTab, WorkbenchTabKind } from '../core/types';
import { wsClient } from '../core/wsClient';
import { formatMsg } from '../i18n/messages';
import { GhosttyTerminal } from './GhosttyTerminal';
import { MarkdownRenderer } from './MarkdownRenderer';

const TAB_ICON: Record<WorkbenchTabKind, ReactNode> = {
  new_tab: <Plus className="size-3.5" />,
  files: <Folder className="size-3.5" />,
  file_preview: <FileCode className="size-3.5" />,
  review: <FileDiff className="size-3.5" />,
  terminal: <SquareTerminal className="size-3.5" />,
  side_chat: <MessageSquareText className="size-3.5" />,
  subagent: <Bot className="size-3.5" />,
};

export function WorkbenchSidebar() {
  const {
    rightTabs,
    activeRightTabId,
    selectedRunnerId,
    wsConnected,
    gitSummary,
    locale,
    openRightTab,
    closeRightTab,
    setActiveRightTab,
    openFileInPreviewTab,
    listWorkspaceDir,
    searchWorkspaceFiles,
    refreshGitSummary,
    appendComposerRef,
    toggleSidePane,
  } = useNexStore();

  const [plusMenuOpen, setPlusMenuOpen] = useState(false);
  const activeTab = rightTabs.find((t) => t.id === activeRightTabId) || rightTabs[0];

  const menuItems: Array<{ kind: WorkbenchTabKind; label: string; icon: ReactNode }> = [
    { kind: 'new_tab', label: formatMsg(locale, 'sidePane.newTab'), icon: <Plus className="size-3.5" /> },
    { kind: 'side_chat', label: 'Side conversation', icon: <MessageSquare className="size-3.5" /> },
    { kind: 'review', label: 'Review', icon: <FileDiff className="size-3.5" /> },
    { kind: 'files', label: 'Files', icon: <Folder className="size-3.5" /> },
    { kind: 'terminal', label: 'Terminal', icon: <SquareTerminal className="size-3.5" /> },
  ];

  return (
    <section className="flex h-full w-full flex-col overflow-hidden border-l border-border bg-background text-foreground">
      {/* Tab Strip */}
      <div className="flex h-12 shrink-0 items-center justify-between gap-1 border-b border-border px-3">
        <div className="flex min-w-0 flex-1 items-center gap-1">
          <div className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
            {rightTabs.map((tab) => (
              <TabChip
                key={tab.id}
                tab={tab}
                active={tab.id === activeTab?.id}
                canClose={tab.closable && rightTabs.length > 1}
                onSelect={() => setActiveRightTab(tab.id)}
                onClose={() => closeRightTab(tab.id)}
              />
            ))}
          </div>

          <div className="relative shrink-0">
            <button
              type="button"
              data-testid="workbench-plus-btn"
              onClick={() => setPlusMenuOpen((v) => !v)}
              className="flex size-7 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
            >
              <Plus className="size-3.5" />
            </button>

            {plusMenuOpen && (
              <>
                <div className="fixed inset-0 z-40" onClick={() => setPlusMenuOpen(false)} />
                <div className="absolute left-0 top-8 z-50 w-52 space-y-0.5 rounded-lg border border-border bg-menu p-1 shadow-md">
                  {menuItems.map((m) => (
                    <button
                      key={m.kind}
                      type="button"
                      onClick={() => {
                        setPlusMenuOpen(false);
                        void openRightTab(m.kind, m.label);
                      }}
                      className="flex h-8 w-full items-center gap-2 rounded-md px-2.5 text-left text-ui-base text-foreground hover:bg-menu-hover"
                    >
                      <span className="text-foreground-subtle">{m.icon}</span>
                      <span>{m.label}</span>
                    </button>
                  ))}
                </div>
              </>
            )}
          </div>
        </div>

        <button
          type="button"
          data-testid="side-pane-close"
          onClick={toggleSidePane}
          title={formatMsg(locale, 'sidePane.collapse')}
          className="flex size-7 shrink-0 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
        >
          <PanelRightClose className="size-4" />
        </button>
      </div>

      {/* Tab Body */}
      <div className="min-h-0 flex-1 overflow-auto">
        {activeTab?.kind === 'new_tab' && (
          <TabLauncher
            locale={locale}
            gitSummary={gitSummary}
            onOpen={(kind) => void openRightTab(kind)}
          />
        )}

        {activeTab?.kind === 'files' && (
          <FilesTreePanel
            wsConnected={wsConnected}
            runnerId={selectedRunnerId}
            listDir={listWorkspaceDir}
            searchFiles={searchWorkspaceFiles}
            onOpenFile={(rel) => void openFileInPreviewTab(rel)}
          />
        )}

        {activeTab?.kind === 'file_preview' && activeTab.preview && (
          <FilePreviewPanel preview={activeTab.preview} onAddRef={appendComposerRef} />
        )}

        {activeTab?.kind === 'review' && (
          <GitReviewPanel
            summary={gitSummary}
            onRefresh={() => void refreshGitSummary(true)}
            onOpenFile={(rel) => void openFileInPreviewTab(rel)}
          />
        )}

        {activeTab?.kind === 'terminal' && (
          <GhosttyTerminal ptyId={activeTab.ptyId || ''} runnerId={selectedRunnerId} />
        )}

        {activeTab?.kind === 'side_chat' && (
          <SideConversationPanel runnerId={selectedRunnerId} sideSessionId={activeTab.sideSessionId || ''} />
        )}

        {activeTab?.kind === 'subagent' && activeTab.subagent && (
          <div className="zcode-view-enter flex h-full flex-col overflow-y-auto p-4 text-ui-base">
            <div className="flex items-center gap-2 border-b border-border pb-3">
              <Bot className={`size-4 shrink-0 ${activeTab.subagent.status === 'running' ? 'animate-pulse text-success' : 'text-foreground-subtle'}`} />
              <div className="min-w-0 flex-1">
                <div className="truncate font-semibold text-foreground">{activeTab.subagent.title}</div>
                <div className="text-ui-xs text-foreground-subtle">
                  {activeTab.subagent.agentName || formatMsg(locale, 'tool.subagent')} · {activeTab.subagent.status}
                </div>
              </div>
            </div>
            {activeTab.subagent.prompt && (
              <div className="mt-3 rounded-lg border border-border bg-secondary/40 p-3">
                <div className="mb-1 text-ui-xs font-medium text-foreground-subtle">
                  {formatMsg(locale, 'subagent.taskPrompt')}
                </div>
                <div className="whitespace-pre-wrap text-ui-sm text-foreground">{activeTab.subagent.prompt}</div>
              </div>
            )}
            <div className="mt-3 flex-1">
              <div className="mb-1 text-ui-xs font-medium text-foreground-subtle">
                {formatMsg(locale, 'subagent.output')}
              </div>
              {activeTab.subagent.resultText ? (
                <MarkdownRenderer content={activeTab.subagent.resultText} />
              ) : (
                <div className="text-ui-sm text-foreground-subtlest">
                  {activeTab.subagent.status === 'running'
                    ? formatMsg(locale, 'subagent.running')
                    : formatMsg(locale, 'subagent.noOutput')}
                </div>
              )}
            </div>
          </div>
        )}
      </div>
    </section>
  );
}

function TabChip({
  tab,
  active,
  canClose,
  onSelect,
  onClose,
}: {
  tab: WorkbenchTab;
  active: boolean;
  canClose: boolean;
  onSelect: () => void;
  onClose: () => void;
}) {
  return (
    <div
      onClick={onSelect}
      className={`group flex h-7 shrink-0 cursor-pointer items-center gap-1.5 rounded-lg px-2 text-ui-sm transition-colors ${
        active
          ? 'bg-selected font-medium text-foreground'
          : 'text-foreground-subtle hover:bg-hover hover:text-foreground'
      }`}
    >
      <span className={active ? 'text-foreground-subtle' : 'text-foreground-subtlest'}>{TAB_ICON[tab.kind]}</span>
      <span className="max-w-[140px] truncate">{tab.title}</span>
      {canClose && (
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            onClose();
          }}
          className="rounded-sm p-0.5 text-foreground-subtlest hover:bg-selected hover:text-foreground"
        >
          <X className="size-3" />
        </button>
      )}
    </div>
  );
}

function TabLauncher({
  locale,
  gitSummary,
  onOpen,
}: {
  locale: 'zh-CN' | 'en-US';
  gitSummary: ReturnType<typeof useNexStore.getState>['gitSummary'];
  onOpen: (kind: WorkbenchTabKind) => void;
}) {
  const dirty = Boolean(gitSummary?.is_git_repo && (gitSummary?.changed_files || 0) > 0);

  return (
    <div className="mx-auto max-w-2xl px-6 py-6">
      <h2 className="mb-4 text-ui-xl font-semibold text-foreground">{formatMsg(locale, 'sidePane.newTab')}</h2>
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        <LauncherRow
          icon={<FolderOpen className="size-5" />}
          title={formatMsg(locale, 'fileManager.title')}
          description={formatMsg(locale, 'fileManager.desc')}
          onClick={() => onOpen('files')}
        />
        <LauncherRow
          icon={<FileDiff className="size-5" />}
          title={formatMsg(locale, 'sidePane.review')}
          description={formatMsg(locale, 'sidePane.launcher.review')}
          onClick={() => onOpen('review')}
          badge={
            dirty ? (
              <span className="flex items-center gap-1 font-mono text-ui-xs tabular-nums">
                <span className="text-success">+{gitSummary?.additions || 0}</span>
                <span className="text-destructive">-{gitSummary?.deletions || 0}</span>
              </span>
            ) : null
          }
        />
        <LauncherRow
          icon={<SquareTerminal className="size-5" />}
          title={formatMsg(locale, 'terminal.title')}
          description={formatMsg(locale, 'sidePane.launcher.terminal')}
          onClick={() => onOpen('terminal')}
        />
        <LauncherRow
          icon={<MessageSquareText className="size-5" />}
          title={formatMsg(locale, 'sidePane.selectionChat')}
          description={formatMsg(locale, 'sidePane.launcher.sideChat')}
          onClick={() => onOpen('side_chat')}
        />
      </div>
    </div>
  );
}

function LauncherRow({
  icon,
  title,
  description,
  onClick,
  badge,
}: {
  icon: ReactNode;
  title: string;
  description: string;
  onClick: () => void;
  badge?: ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex items-center gap-3 rounded-xl px-2.5 py-2.5 text-left transition-colors hover:bg-hover"
    >
      <span className="flex size-11 shrink-0 items-center justify-center rounded-xl bg-surface text-foreground-subtle">
        {icon}
      </span>
      <span className="min-w-0 flex-1">
        <span className="flex items-center gap-2">
          <span className="truncate text-ui-base font-semibold text-foreground">{title}</span>
          {badge}
        </span>
        <span className="mt-0.5 block truncate text-ui-sm text-foreground-subtle">{description}</span>
      </span>
    </button>
  );
}

function FilesTreePanel({
  wsConnected,
  runnerId,
  listDir,
  searchFiles,
  onOpenFile,
}: {
  wsConnected: boolean;
  runnerId: string;
  listDir: (rel: string) => Promise<DirEntry[]>;
  searchFiles: (q: string) => Promise<string[]>;
  onOpenFile: (relPath: string) => void;
}) {
  const [rootEntries, setRootEntries] = useState<DirEntry[]>([]);
  const [expanded, setExpanded] = useState<Record<string, DirEntry[]>>({});
  const [query, setQuery] = useState('');
  const [searchResults, setSearchResults] = useState<string[]>([]);

  useEffect(() => {
    if (wsConnected && runnerId) {
      void listDir('').then(setRootEntries).catch(() => {});
    }
  }, [wsConnected, runnerId, listDir]);

  useEffect(() => {
    if (!query.trim()) {
      setSearchResults([]);
      return;
    }
    const timer = setTimeout(() => {
      void searchFiles(query).then(setSearchResults).catch(() => {});
    }, 160);
    return () => clearTimeout(timer);
  }, [query, searchFiles]);

  const toggleFolder = async (relPath: string) => {
    if (expanded[relPath]) {
      const cp = { ...expanded };
      delete cp[relPath];
      setExpanded(cp);
      return;
    }
    const children = await listDir(relPath);
    setExpanded((prev) => ({ ...prev, [relPath]: children }));
  };

  const renderNodes = (entries: DirEntry[], depth: number) =>
    entries.map((entry) => {
      const isDir = entry.kind === 'dir';
      const isOpen = Boolean(expanded[entry.rel_path]);
      return (
        <div key={entry.rel_path}>
          <div
            style={{ paddingLeft: `${depth * 14 + 10}px` }}
            onClick={() => (isDir ? void toggleFolder(entry.rel_path) : onOpenFile(entry.rel_path))}
            className={`flex h-7 cursor-pointer items-center justify-between pr-3 text-ui-sm hover:bg-hover ${
              entry.git_ignored ? 'text-foreground-subtlest' : 'text-foreground'
            }`}
          >
            <span className="flex min-w-0 items-center gap-1.5">
              {isDir ? (
                isOpen ? (
                  <ChevronDown className="size-3.5 shrink-0 text-foreground-subtlest" />
                ) : (
                  <ChevronRight className="size-3.5 shrink-0 text-foreground-subtlest" />
                )
              ) : (
                <FileCode className="size-3.5 shrink-0 text-brand-blue" />
              )}
              <span className="truncate">{entry.name}</span>
            </span>
            {entry.git_status && <span className="font-mono text-ui-xs text-warning">{entry.git_status}</span>}
          </div>
          {isDir && isOpen && expanded[entry.rel_path] && renderNodes(expanded[entry.rel_path], depth + 1)}
        </div>
      );
    });

  return (
    <div className="flex flex-col">
      <div className="border-b border-border/60 p-2">
        <input
          type="text"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="搜索文件..."
          className="h-7 w-full rounded-lg border border-border bg-input px-2.5 text-ui-sm text-foreground placeholder-foreground-subtlest focus:border-border-hover"
        />
      </div>

      {query.trim() ? (
        <div className="py-1">
          {searchResults.map((rel) => (
            <div
              key={rel}
              onClick={() => onOpenFile(rel)}
              className="cursor-pointer truncate px-3 py-1.5 font-mono text-ui-sm text-foreground hover:bg-hover"
            >
              {rel}
            </div>
          ))}
          {searchResults.length === 0 && (
            <div className="p-6 text-center text-ui-sm text-foreground-subtlest">无匹配文件</div>
          )}
        </div>
      ) : (
        <div className="py-1">{renderNodes(rootEntries, 0)}</div>
      )}
    </div>
  );
}

function FilePreviewPanel({
  preview,
  onAddRef,
}: {
  preview: FilePreview;
  onAddRef: (ref: string) => void;
}) {
  const [rawView, setRawView] = useState(false);

  if (preview.preview_as === 'binary') {
    return (
      <div className="p-6 text-center text-ui-sm text-foreground-subtle">
        <p className="font-medium text-foreground">{preview.rel_path}</p>
        <p className="mt-1">二进制文件 ({preview.total_size} bytes) — 暂不支持文本预览</p>
      </div>
    );
  }

  if (preview.preview_as === 'image') {
    return (
      <div className="flex flex-col items-center p-4">
        <img
          src={`data:${preview.mime_type || 'image/png'};base64,${preview.content || ''}`}
          alt={preview.name}
          className="max-h-[70vh] max-w-full rounded-lg border border-border object-contain"
        />
        <span className="mt-2 font-mono text-ui-xs text-foreground-subtle">
          {preview.rel_path} ({preview.total_size} bytes)
        </span>
      </div>
    );
  }

  const lines = (preview.content || '').split('\n');

  return (
    <div className="flex flex-col">
      <div className="flex items-center justify-between border-b border-border/60 bg-surface px-3 py-1.5 text-ui-xs">
        <span className="truncate font-mono text-foreground-subtle">{preview.rel_path}</span>
        <div className="flex items-center gap-1.5">
          {preview.preview_as === 'markdown' && (
            <button
              type="button"
              onClick={() => setRawView((v) => !v)}
              className="rounded-md border border-border px-2 py-0.5 text-ui-xs text-foreground hover:bg-hover"
            >
              {rawView ? 'Rendered' : 'Source'}
            </button>
          )}
          <button
            type="button"
            onClick={() => onAddRef(`@${preview.rel_path}`)}
            className="rounded-md bg-selected px-2 py-0.5 text-ui-xs font-medium text-foreground hover:bg-hover"
          >
            @ 引用到对话
          </button>
        </div>
      </div>

      {preview.preview_as === 'markdown' && !rawView ? (
        <div className="p-4">
          <MarkdownRenderer content={preview.content || ''} />
        </div>
      ) : (
        <div className="overflow-x-auto py-2 font-mono text-[12.5px] leading-5">
          {lines.map((line, idx) => (
            <div key={idx} className="flex hover:bg-hover">
              <span
                onClick={() => onAddRef(`@${preview.rel_path}:L${idx + 1}`)}
                title="点击行号引用至输入框"
                className="w-11 shrink-0 cursor-pointer select-none pr-3 text-right tabular-nums text-foreground-subtlest hover:text-brand-blue"
              >
                {idx + 1}
              </span>
              <span className="whitespace-pre pr-4 text-foreground">{line}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function GitReviewPanel({
  summary,
  onRefresh,
  onOpenFile,
}: {
  summary: ReturnType<typeof useNexStore.getState>['gitSummary'];
  onRefresh: () => void;
  onOpenFile: (rel: string) => void;
}) {
  const locale = useNexStore((s) => s.locale);
  const gitStage = useNexStore((s) => s.gitStage);
  const gitUnstage = useNexStore((s) => s.gitUnstage);
  const gitDiscard = useNexStore((s) => s.gitDiscard);
  const gitCommit = useNexStore((s) => s.gitCommit);
  const gitListBranches = useNexStore((s) => s.gitListBranches);
  const gitCheckout = useNexStore((s) => s.gitCheckout);

  const [viewMode, setViewMode] = useState<'unified' | 'split'>('unified');
  const [sourceFilter, setSourceFilter] = useState<'all' | 'staged' | 'unstaged'>('all');
  const [commitMsg, setCommitMsg] = useState('');
  const [commitStatus, setCommitStatus] = useState<{ ok: boolean; text: string } | null>(null);
  const [busyAction, setBusyAction] = useState(false);
  const [branchMenuOpen, setBranchMenuOpen] = useState(false);
  const [branches, setBranches] = useState<string[]>([]);
  const [newBranchName, setNewBranchName] = useState('');

  useEffect(() => {
    onRefresh();
  }, [onRefresh]);

  if (!summary || !summary.is_git_repo) {
    return (
      <div className="p-6 text-center text-ui-sm text-foreground-subtlest">
        {locale === 'en-US' ? 'No Git repository detected in workspace' : '当前工作区未检测到 Git 仓库'}
      </div>
    );
  }

  const files = summary.files || [];
  const isStaged = (f: (typeof files)[number]) =>
    f.staged !== undefined ? f.staged : f.status === 'A' || f.status === 'R';
  const isUnstaged = (f: (typeof files)[number]) =>
    f.unstaged !== undefined ? f.unstaged : !isStaged(f);

  const stagedCount = files.filter(isStaged).length;
  const unstagedCount = files.filter(isUnstaged).length;

  const filteredFiles =
    sourceFilter === 'all'
      ? files
      : files.filter((f) => (sourceFilter === 'staged' ? isStaged(f) : isUnstaged(f)));

  const openBranches = async () => {
    const next = !branchMenuOpen;
    setBranchMenuOpen(next);
    if (next) {
      const res = await gitListBranches();
      setBranches(res.branches);
    }
  };

  const handleCommit = async () => {
    const msg = commitMsg.trim();
    if (!msg || busyAction) return;
    setBusyAction(true);
    setCommitStatus(null);
    const res = await gitCommit(msg);
    setBusyAction(false);
    if (res.ok) {
      setCommitMsg('');
      setCommitStatus({
        ok: true,
        text: locale === 'en-US' ? `Committed ${res.commit || ''}` : `已提交 ${res.commit || ''}`,
      });
    } else {
      setCommitStatus({ ok: false, text: res.error || (locale === 'en-US' ? 'Commit failed' : '提交失败') });
    }
  };

  return (
    <div className="flex flex-col">
      {/* Header: Branch Switcher + Stats + Stage All / Refresh */}
      <div className="relative flex items-center justify-between gap-2 border-b border-border/60 px-3 py-2 text-ui-sm">
        <div className="flex min-w-0 items-center gap-2">
          <button
            type="button"
            data-testid="git-branch-btn"
            onClick={() => void openBranches()}
            className="inline-flex max-w-[160px] items-center gap-1 rounded-md bg-tag px-2 py-0.5 font-mono text-ui-xs text-foreground transition-colors hover:bg-hover"
          >
            <GitBranch className="size-3 shrink-0 text-foreground-subtle" />
            <span className="truncate">{summary.branch || 'HEAD'}</span>
            <ChevronDown className="size-3 shrink-0 text-foreground-subtlest" />
          </button>
          <span className="font-mono text-ui-xs tabular-nums text-success">+{summary.additions}</span>
          <span className="font-mono text-ui-xs tabular-nums text-destructive">-{summary.deletions}</span>
        </div>

        <div className="flex shrink-0 items-center gap-1">
          {unstagedCount > 0 && (
            <button
              type="button"
              disabled={busyAction}
              onClick={() => void gitStage('')}
              title={locale === 'en-US' ? 'Stage all changes' : '全部暂存'}
              className="flex h-6 items-center gap-1 rounded-md border border-border px-2 text-ui-xs text-foreground-subtle hover:bg-hover hover:text-foreground disabled:opacity-40"
            >
              <Plus className="size-3" />
              <span>{locale === 'en-US' ? 'Stage all' : '全部暂存'}</span>
            </button>
          )}
          {stagedCount > 0 && (
            <button
              type="button"
              disabled={busyAction}
              onClick={() => void gitUnstage('')}
              title={locale === 'en-US' ? 'Unstage all' : '取消全部暂存'}
              className="flex h-6 items-center gap-1 rounded-md border border-border px-2 text-ui-xs text-foreground-subtle hover:bg-hover hover:text-foreground disabled:opacity-40"
            >
              <Minus className="size-3" />
            </button>
          )}
          <button
            type="button"
            onClick={onRefresh}
            className="flex h-6 items-center gap-1 rounded-md border border-border px-2 text-ui-xs text-foreground-subtle hover:bg-hover hover:text-foreground"
          >
            <RefreshCw className="size-3" />
            <span>{locale === 'en-US' ? 'Refresh' : '刷新'}</span>
          </button>
        </div>

        {branchMenuOpen && (
          <div className="zcode-popover-down absolute left-3 top-10 z-40 w-64 rounded-xl border border-border bg-popover p-1.5 shadow-xl">
            <div className="mb-1 flex items-center gap-1 px-1">
              <input
                value={newBranchName}
                onChange={(e) => setNewBranchName(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && newBranchName.trim()) {
                    void gitCheckout(newBranchName.trim(), true).then(() => {
                      setNewBranchName('');
                      setBranchMenuOpen(false);
                    });
                  }
                }}
                placeholder={locale === 'en-US' ? 'New branch name…' : '新建分支名…'}
                className="h-7 min-w-0 flex-1 rounded-md border border-border bg-input px-2 font-mono text-ui-xs text-foreground"
              />
              <button
                type="button"
                disabled={!newBranchName.trim()}
                onClick={() =>
                  void gitCheckout(newBranchName.trim(), true).then(() => {
                    setNewBranchName('');
                    setBranchMenuOpen(false);
                  })
                }
                className="flex h-7 items-center rounded-md bg-foreground px-2 text-ui-xs text-background disabled:opacity-40"
              >
                <Plus className="size-3" />
              </button>
            </div>
            <div className="max-h-44 overflow-y-auto">
              {branches.map((br) => (
                <button
                  key={br}
                  type="button"
                  onClick={() => void gitCheckout(br, false).then(() => setBranchMenuOpen(false))}
                  className={`flex w-full items-center justify-between rounded-lg px-2 py-1.5 text-left font-mono text-ui-xs hover:bg-menu-hover ${
                    br === summary.branch ? 'bg-selected font-medium text-foreground' : 'text-foreground-subtle'
                  }`}
                >
                  <span className="truncate">{br}</span>
                  {br === summary.branch && <Check className="size-3 text-success" />}
                </button>
              ))}
            </div>
          </div>
        )}
      </div>

      {/* Quick Commit Bar (shown when there are staged changes or any changes) */}
      {files.length > 0 && (
        <div className="border-b border-border/60 bg-surface/40 px-3 py-2">
          <div className="flex items-center gap-1.5">
            <input
              data-testid="git-commit-input"
              type="text"
              value={commitMsg}
              onChange={(e) => setCommitMsg(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault();
                  void handleCommit();
                }
              }}
              placeholder={
                stagedCount > 0
                  ? locale === 'en-US'
                    ? `Commit ${stagedCount} staged file(s)…`
                    : `输入提交信息（将提交 ${stagedCount} 个已暂存文件）…`
                  : locale === 'en-US'
                    ? 'Stage files first to commit…'
                    : '请先暂存文件再提交…'
              }
              className="h-7 min-w-0 flex-1 rounded-lg border border-border bg-input px-2.5 text-ui-xs text-foreground placeholder-foreground-subtlest focus:border-border-hover"
            />
            <button
              type="button"
              data-testid="git-commit-btn"
              disabled={busyAction || !commitMsg.trim() || stagedCount === 0}
              onClick={() => void handleCommit()}
              className="flex h-7 shrink-0 items-center gap-1 rounded-lg bg-foreground px-2.5 text-ui-xs font-medium text-background transition-opacity hover:opacity-90 disabled:opacity-35"
            >
              <GitCommitHorizontal className="size-3.5" />
              <span>{locale === 'en-US' ? 'Commit' : '提交'}</span>
            </button>
          </div>
          {commitStatus && (
            <div className={`mt-1 text-ui-xs ${commitStatus.ok ? 'text-success' : 'text-destructive'}`}>
              {commitStatus.text}
            </div>
          )}
        </div>
      )}

      <div className="flex items-center justify-between border-b border-border/60 px-3 py-1.5">
        <div className="flex items-center gap-0.5">
          {(
            [
              { id: 'all', label: `${locale === 'en-US' ? 'All' : '全部'} (${files.length})` },
              { id: 'staged', label: `${locale === 'en-US' ? 'Staged' : '已暂存'} (${stagedCount})` },
              { id: 'unstaged', label: `${locale === 'en-US' ? 'Unstaged' : '未暂存'} (${unstagedCount})` },
            ] as const
          ).map((f) => (
            <button
              key={f.id}
              type="button"
              onClick={() => setSourceFilter(f.id)}
              className={`h-6 rounded-md px-2 text-ui-xs transition-colors ${
                sourceFilter === f.id
                  ? 'bg-selected font-medium text-foreground'
                  : 'text-foreground-subtle hover:bg-hover hover:text-foreground'
              }`}
            >
              {f.label}
            </button>
          ))}
        </div>

        <div className="flex items-center rounded-md border border-border bg-surface p-0.5">
          {(
            [
              { id: 'unified', label: 'Unified' },
              { id: 'split', label: 'Split' },
            ] as const
          ).map((m) => (
            <button
              key={m.id}
              type="button"
              onClick={() => setViewMode(m.id)}
              className={`h-5 rounded px-1.5 text-ui-xs transition-colors ${
                viewMode === m.id ? 'bg-card text-foreground shadow-2xs' : 'text-foreground-subtlest hover:text-foreground'
              }`}
            >
              {m.label}
            </button>
          ))}
        </div>
      </div>

      {filteredFiles.length === 0 && (
        <div className="p-8 text-center text-ui-sm text-foreground-subtlest">
          {locale === 'en-US' ? 'Working tree clean' : '工作区干净，无未提交改动'}
        </div>
      )}

      <div className="divide-y divide-border">
        {filteredFiles.map((f) => {
          const staged = isStaged(f);
          const unstaged = isUnstaged(f);
          const diffLines = (f.diff || '').split('\n');
          const unifiedLines = viewMode === 'unified' ? diffLines : splitDiffLines(diffLines);
          return (
            <div key={f.rel_path} className="p-2.5">
              <div className="flex items-center justify-between gap-2 text-ui-sm">
                <button
                  type="button"
                  onClick={() => onOpenFile(f.rel_path)}
                  className="min-w-0 truncate font-mono font-medium text-foreground hover:text-brand-blue"
                >
                  [{f.status}] {f.rel_path}
                </button>
                <div className="flex shrink-0 items-center gap-1.5">
                  <span className="font-mono text-ui-xs tabular-nums">
                    <span className="text-success">+{f.additions}</span>{' '}
                    <span className="text-destructive">-{f.deletions}</span>
                  </span>
                  {unstaged && (
                    <button
                      type="button"
                      onClick={() => void gitStage(f.rel_path)}
                      title={locale === 'en-US' ? 'Stage file' : '暂存此文件'}
                      className="flex size-5 items-center justify-center rounded border border-border text-foreground-subtle hover:bg-hover hover:text-success"
                    >
                      <Plus className="size-3" />
                    </button>
                  )}
                  {staged && (
                    <button
                      type="button"
                      onClick={() => void gitUnstage(f.rel_path)}
                      title={locale === 'en-US' ? 'Unstage file' : '取消暂存'}
                      className="flex size-5 items-center justify-center rounded border border-border text-foreground-subtle hover:bg-hover hover:text-warning"
                    >
                      <Minus className="size-3" />
                    </button>
                  )}
                  {unstaged && (
                    <button
                      type="button"
                      onClick={() => void gitDiscard(f.rel_path)}
                      title={locale === 'en-US' ? 'Discard unstaged changes' : '放弃未暂存更改'}
                      className="flex size-5 items-center justify-center rounded border border-border text-foreground-subtle hover:bg-hover hover:text-destructive"
                    >
                      <RotateCcw className="size-3" />
                    </button>
                  )}
                </div>
              </div>
              {f.folded && !f.diff && (
                <div className="mt-2 rounded-lg border border-border/60 bg-surface px-2 py-1.5 text-ui-xs text-foreground-subtlest">
                  大文件差异已折叠（{f.additions} 行新增 / {f.deletions} 行删除）
                </div>
              )}
              {f.diff && (
                <pre className="mt-2 overflow-x-auto rounded-lg border border-border/60 bg-surface p-2 font-mono text-[11.5px] leading-5">
                  {unifiedLines.map((dl, i) => {
                    const style = dl.startsWith('+')
                      ? { backgroundColor: 'var(--color-diff-added-bg)', color: 'var(--color-diff-added-fg)' }
                      : dl.startsWith('-')
                        ? { backgroundColor: 'var(--color-diff-removed-bg)', color: 'var(--color-diff-removed-fg)' }
                        : undefined;
                    return (
                      <div key={i} style={style} className="px-1.5 text-foreground-subtle">
                        {dl || ' '}
                      </div>
                    );
                  })}
                </pre>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function splitDiffLines(lines: string[]): string[] {
  const out: string[] = [];
  let removals: string[] = [];
  let additions: string[] = [];

  const flush = () => {
    const rows = Math.max(removals.length, additions.length);
    for (let i = 0; i < rows; i++) {
      out.push(`${(removals[i] || '').padEnd(44)} │ ${additions[i] || ''}`);
    }
    removals = [];
    additions = [];
  };

  for (const line of lines) {
    if (line.startsWith('-') && !line.startsWith('---')) {
      if (additions.length > 0) flush();
      removals.push(line);
      continue;
    }
    if (line.startsWith('+') && !line.startsWith('+++')) {
      additions.push(line);
      continue;
    }
    flush();
    out.push(line);
  }
  flush();
  return out;
}

function SideConversationPanel({ runnerId, sideSessionId }: { runnerId: string; sideSessionId: string }) {
  const { streams, selectedProviderId, selectedModelId, locale } = useNexStore();
  const [input, setInput] = useState('');
  const st = streams[sideSessionId];

  const sendSide = () => {
    const text = input.trim();
    if (!text || !runnerId || !sideSessionId) return;
    wsClient.sendEnvelope('client.subscribe', { session_id: sideSessionId, last_seen_seq: st?.lastSeq || 0 });
    wsClient.sendEnvelope(
      'session.prompt',
      {
        session_id: sideSessionId,
        runner_id: runnerId,
        provider_id: selectedProviderId,
        model_id: selectedModelId,
        permission_mode: 'read_only',
        text,
      },
      { session_id: sideSessionId, runner_id: runnerId },
    );
    setInput('');
  };

  return (
    <div className="flex h-full flex-col">
      <div className="flex-1 space-y-3 overflow-auto p-3 text-ui-sm">
        {(!st || st.items.length === 0) && (
          <p className="text-foreground-subtlest">{formatMsg(locale, 'sidePane.launcher.sideChat')}</p>
        )}
        {st?.items.map((m) => (
          <div
            key={m.id}
            className={`rounded-xl p-2.5 ${
              m.role === 'user' ? 'bg-user-bubble text-foreground' : 'bg-surface text-foreground'
            }`}
          >
            <MarkdownRenderer content={m.text} />
          </div>
        ))}
      </div>
      <div className="flex gap-1.5 border-t border-border/60 p-2">
        <input
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && sendSide()}
          placeholder="输入旁路问题..."
          className="h-8 flex-1 rounded-lg border border-border bg-input px-2.5 text-ui-sm text-foreground placeholder-foreground-subtlest"
        />
        <button
          type="button"
          onClick={sendSide}
          className="h-8 rounded-lg bg-primary px-3 text-ui-sm font-medium text-primary-foreground hover:opacity-90"
        >
          {formatMsg(locale, 'chat.send')}
        </button>
      </div>
    </div>
  );
}
