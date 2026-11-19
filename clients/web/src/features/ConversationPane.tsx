import {
  Archive,
  ArrowUp,
  AtSign,
  Bot,
  Bug,
  CalendarDays,
  Check,
  ChevronDown,
  ChevronRight,
  CircleHelp,
  Copy,
  Ellipsis,
  FileText,
  Folder,
  FolderTree,
  GitPullRequest,
  Hand,
  LoaderCircle,
  PanelLeft,
  PanelRight,
  Pencil,
  Pin,
  PinOff,
  Plug,
  Plus,
  Search,
  ShieldAlert,
  ShieldCheck,
  Sparkles,
  Square,
  SquareTerminal,
  X,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { emptyStreamState } from '../core/reducer';
import { useNexStore } from '../core/store';
import { ChatTurnItem, ToolCallCard } from '../core/types';
import { formatMsg } from '../i18n/messages';
import type { MessageKey } from '../i18n/messages';
import { ContextWindowsButton } from './ContextPopover';
import { ConversationStatusPanel } from './ConversationStatusPanel';
import { MarkdownRenderer } from './MarkdownRenderer';

function getGreetingKey(hour: number): MessageKey {
  if (hour >= 5 && hour < 9) return 'chat.empty.greeting.morningEarly';
  if (hour >= 9 && hour < 12) return 'chat.empty.greeting.morning';
  if (hour >= 12 && hour < 14) return 'chat.empty.greeting.noon';
  if (hour >= 14 && hour < 18) return 'chat.empty.greeting.afternoon';
  if (hour >= 18 && hour < 23) return 'chat.empty.greeting.evening';
  return 'chat.empty.greeting.lateNight';
}

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

function truncateText(raw: string, max = 64): string {
  const single = raw.replace(/\s+/g, ' ').trim();
  return single.length > max ? `${single.slice(0, max)}…` : single;
}

function DiffBody({ diff }: { diff: string }) {
  const lines = diff.split('\n');
  return (
    <>
      {lines.map((line, idx) => {
        const added = line.startsWith('+') && !line.startsWith('+++');
        const removed = line.startsWith('-') && !line.startsWith('---');
        const style = added
          ? { backgroundColor: 'var(--color-diff-added-bg)', color: 'var(--color-diff-added-fg)' }
          : removed
            ? { backgroundColor: 'var(--color-diff-removed-bg)', color: 'var(--color-diff-removed-fg)' }
            : undefined;
        return (
          <div key={idx} style={style} className="whitespace-pre px-2.5 py-[1px] text-foreground-subtle">
            {line.length > 0 ? line : ' '}
          </div>
        );
      })}
    </>
  );
}

function ToolSummaryRow({ tc, dense = true }: { tc: ToolCallCard; dense?: boolean }) {
  const locale = useNexStore((s) => s.locale);
  const openFileInPreviewTab = useNexStore((s) => s.openFileInPreviewTab);
  const [open, setOpen] = useState(false);

  const args = parseToolArgs(tc);
  const running = tc.status === 'running';
  const output = tc.finalOutput || tc.streamOutput || '';
  const filePath = String(args.file_path || args.path || '');
  const additions = tc.additions || 0;
  const deletions = tc.deletions || 0;
  const rowClass = dense ? 'text-ui-sm' : 'text-ui-base';

  const label = (key: MessageKey) =>
    running ? (
      <span className="shrink-0 font-medium animated-gradient-text">{formatMsg(locale, key)}</span>
    ) : (
      <span className={`shrink-0 font-medium ${tc.status === 'error' ? 'text-destructive' : 'text-foreground-subtle'}`}>
        {formatMsg(locale, key)}
      </span>
    );

  const chevron = () => (
    <ChevronRight
      className={`size-3.5 shrink-0 text-foreground-subtlest transition-transform ${
        open ? 'rotate-90' : 'opacity-0 group-hover/tool:opacity-100'
      }`}
    />
  );

  if (tc.name === 'read') {
    return (
      <div className={`group/tool inline-flex max-w-full items-center gap-2 py-0.5 text-left ${rowClass}`}>
        <Search className="size-3.5 shrink-0 text-foreground-subtlest" />
        {label(running ? 'tool.reading' : 'tool.read')}
        {filePath && (
          <button
            type="button"
            onClick={() => void openFileInPreviewTab(filePath)}
            title={filePath}
            className="inline-flex min-w-0 items-center gap-1 text-foreground-subtle transition-colors hover:text-foreground hover:underline"
          >
            <FileText className="size-3.5 shrink-0" />
            <span className="truncate font-mono text-ui-sm">{baseName(filePath)}</span>
          </button>
        )}
      </div>
    );
  }

  if (tc.name === 'grep' || tc.name === 'glob') {
    const pattern = String(args.pattern || args.query || '');
    const summary =
      tc.name === 'grep'
        ? locale === 'en-US'
          ? `Find "${pattern}"`
          : `查找 "${pattern}"`
        : locale === 'en-US'
          ? `List files matching ${pattern}`
          : `列出匹配 ${pattern} 的文件`;
    return (
      <div className="flex max-w-full flex-col">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className={`group/tool inline-flex max-w-full items-center gap-2 py-0.5 text-left ${rowClass}`}
        >
          <Search className="size-3.5 shrink-0 text-foreground-subtlest" />
          {label(running ? 'tool.searching' : 'tool.searched')}
          {summary && <span className="min-w-0 truncate text-foreground-subtle">{summary}</span>}
          {chevron()}
        </button>
        {open && output && (
          <div className="ml-2 mt-1 max-h-60 overflow-auto border-l border-border pl-3.5 font-mono text-ui-sm whitespace-pre-wrap text-foreground-subtle">
            {output}
          </div>
        )}
      </div>
    );
  }

  if (tc.name === 'edit' || tc.name === 'write') {
    const isWrite = tc.name === 'write';
    return (
      <div className="flex max-w-full flex-col">
        <div className={`group/tool inline-flex max-w-full items-center gap-2 py-0.5 text-left ${rowClass}`}>
          <Pencil className="size-3.5 shrink-0 text-foreground-subtlest" />
          {label(running ? (isWrite ? 'tool.writing' : 'tool.editing') : isWrite ? 'tool.wrote' : 'tool.edited')}
          {filePath && (
            <button
              type="button"
              onClick={() => void openFileInPreviewTab(filePath)}
              title={filePath}
              className="inline-flex min-w-0 items-center gap-1 text-foreground-subtle transition-colors hover:text-foreground hover:underline"
            >
              <FileText className="size-3.5 shrink-0" />
              <span className="truncate font-mono text-ui-sm">{baseName(filePath)}</span>
            </button>
          )}
          {(additions > 0 || deletions > 0) && (
            <span className="shrink-0 font-mono text-ui-xs tabular-nums">
              <span className="text-success">+{additions}</span> <span className="text-destructive">-{deletions}</span>
            </span>
          )}
          <button type="button" onClick={() => setOpen((v) => !v)} className="flex shrink-0 items-center">
            {chevron()}
          </button>
        </div>
        {open && (tc.diff || output) && (
          <div
            className="mb-2 mt-1.5 max-h-60 overflow-auto rounded-xl border border-border bg-card font-mono text-[12px] leading-5"
            style={{
              boxShadow: `inset 3px 0 0 ${
                deletions > 0 && additions === 0 ? 'var(--color-destructive)' : 'var(--color-success)'
              }`,
            }}
          >
            {tc.diff ? (
              <DiffBody diff={tc.diff} />
            ) : (
              <pre className="whitespace-pre-wrap p-2.5 text-foreground-subtle">{output}</pre>
            )}
          </div>
        )}
      </div>
    );
  }

  if (tc.name === 'bash') {
    const command = String(args.command || '');
    return (
      <div className="flex max-w-full flex-col">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className={`group/tool inline-flex max-w-full items-center gap-2 py-0.5 text-left ${rowClass}`}
        >
          <SquareTerminal className="size-3.5 shrink-0 text-foreground-subtlest" />
          {label(running ? 'tool.running' : 'tool.ran')}
          {command && (
            <span className="min-w-0 truncate font-mono text-ui-sm text-foreground-subtle">{truncateText(command, 68)}</span>
          )}
          {chevron()}
        </button>
        {open && (
          <div className="mb-2 mt-1.5 rounded-xl border border-border bg-card px-3.5 py-2.5 font-mono text-ui-sm">
            <div className="truncate text-foreground-subtle">
              <span className="text-foreground-subtlest">$ </span>
              {command}
            </div>
            {output && (
              <pre className="mt-1.5 max-h-52 overflow-auto whitespace-pre-wrap text-foreground-subtle">{output}</pre>
            )}
          </div>
        )}
      </div>
    );
  }

  if (tc.name === 'codemode') {
    const summary = truncateText(String(args.title || args.description || args.code || ''), 56);
    return (
      <div className="flex max-w-full flex-col">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className={`group/tool inline-flex max-w-full items-center gap-2 py-0.5 text-left ${rowClass}`}
        >
          <Plug className="size-3.5 shrink-0 text-foreground-subtlest" />
          <span className="shrink-0 font-medium text-foreground-subtle">{formatMsg(locale, 'tool.mcp')}</span>
          <span className="shrink-0 font-mono text-foreground-subtle">codemode</span>
          {summary && <span className="min-w-0 truncate text-foreground-subtle">{summary}</span>}
          {chevron()}
        </button>
        {open && output && (
          <div className="mb-2 mt-1.5 max-h-60 overflow-auto rounded-xl border border-border bg-card p-2.5 font-mono text-[12px] leading-5 whitespace-pre-wrap text-foreground-subtle">
            {output}
          </div>
        )}
      </div>
    );
  }

  if (tc.name === 'subagent') {
    const summary = truncateText(String(args.prompt || args.description || args.title || ''), 60);
    return (
      <div className="flex max-w-full flex-col">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className={`group/tool inline-flex max-w-full items-center gap-2 py-0.5 text-left ${rowClass}`}
        >
          <Bot className="size-3.5 shrink-0 text-success" />
          <span className="shrink-0 font-medium text-foreground-subtle">{formatMsg(locale, 'tool.subagent')}</span>
          {summary && <span className="min-w-0 truncate text-foreground-subtle">{summary}</span>}
          {chevron()}
        </button>
        {open && output && (
          <div className="ml-2 mt-1 max-h-60 overflow-auto border-l border-border pl-3.5 text-ui-sm whitespace-pre-wrap text-foreground-subtle">
            {output}
          </div>
        )}
      </div>
    );
  }

  return (
    <div className="flex max-w-full flex-col">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className={`group/tool inline-flex max-w-full items-center gap-2 py-0.5 text-left ${rowClass}`}
      >
        <Plug className="size-3.5 shrink-0 text-foreground-subtlest" />
        <span className={`shrink-0 font-mono font-medium ${running ? 'animated-gradient-text' : 'text-foreground-subtle'}`}>
          {tc.name}
        </span>
        <span className="min-w-0 truncate text-foreground-subtle">
          {truncateText(String(args.file_path || args.command || args.pattern || args.query || ''), 56)}
        </span>
        {chevron()}
      </button>
      {open && output && (
        <div className="mb-2 mt-1.5 max-h-60 overflow-auto rounded-xl border border-border bg-card p-2.5 font-mono text-[12px] leading-5 whitespace-pre-wrap text-foreground-subtle">
          {output}
        </div>
      )}
    </div>
  );
}

function ChangeSummaryCard({ item }: { item: ChatTurnItem }) {
  const locale = useNexStore((s) => s.locale);
  const openRightTab = useNexStore((s) => s.openRightTab);
  const openFileInPreviewTab = useNexStore((s) => s.openFileInPreviewTab);
  const [open, setOpen] = useState(false);

  const files = useMemo(() => {
    const map = new Map<string, { path: string; adds: number; dels: number }>();
    for (const tc of item.toolCalls) {
      if ((tc.name !== 'edit' && tc.name !== 'write') || tc.status !== 'completed') continue;
      const args = parseToolArgs(tc);
      const path = String(args.file_path || args.path || '');
      if (!path) continue;
      const prev = map.get(path) || { path, adds: 0, dels: 0 };
      prev.adds += tc.additions || 0;
      prev.dels += tc.deletions || 0;
      map.set(path, prev);
    }
    return Array.from(map.values());
  }, [item.toolCalls]);

  if (files.length === 0) return null;

  const totalAdds = files.reduce((sum, f) => sum + f.adds, 0);
  const totalDels = files.reduce((sum, f) => sum + f.dels, 0);

  return (
    <div className="rounded-xl border border-border bg-card">
      <div className="flex h-10 items-center justify-between gap-2 px-3">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className="flex min-w-0 items-center gap-1.5 text-left text-ui-base text-foreground summary-hover-text"
        >
          <ChevronRight
            className={`size-3.5 shrink-0 text-foreground-subtle transition-transform ${open ? 'rotate-90' : ''}`}
          />
          <span className="truncate">{formatMsg(locale, 'tool.filesChanged', { n: files.length })}</span>
          <span className="shrink-0 font-mono text-ui-xs tabular-nums">
            <span className="text-success">+{totalAdds}</span> <span className="text-destructive">-{totalDels}</span>
          </span>
        </button>
        <div className="flex shrink-0 items-center gap-1">
          <button
            type="button"
            onClick={() => void openRightTab('review')}
            className="h-6 rounded-md px-2 text-ui-sm font-medium text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
          >
            {formatMsg(locale, 'sidePane.review')}
          </button>
          <button
            type="button"
            onClick={() => void openFileInPreviewTab(files[0].path)}
            className="h-6 rounded-md px-2 text-ui-sm font-medium text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
          >
            {formatMsg(locale, 'tool.open')}
          </button>
        </div>
      </div>
      {open && (
        <div className="border-t border-border py-1">
          {files.map((f) => (
            <button
              key={f.path}
              type="button"
              onClick={() => void openFileInPreviewTab(f.path)}
              className="flex w-full items-center justify-between gap-2 px-3 py-1 text-left text-ui-sm hover:bg-hover"
            >
              <span className="truncate font-mono text-foreground-subtle">{f.path}</span>
              <span className="shrink-0 font-mono text-ui-xs tabular-nums">
                <span className="text-success">+{f.adds}</span> <span className="text-destructive">-{f.dels}</span>
              </span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function UserBubble({ text }: { text: string }) {
  const locale = useNexStore((s) => s.locale);
  const setComposerText = useNexStore((s) => s.setComposerText);
  const [copied, setCopied] = useState(false);

  const copy = () => {
    void navigator.clipboard?.writeText(text);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1400);
  };

  return (
    <div className="group flex flex-col items-end">
      <div className="max-w-xl whitespace-pre-wrap break-words rounded-2xl bg-black/[0.045] px-4 py-2.5 text-ui-base text-foreground dark:bg-white/[0.08]">
        {text}
      </div>
      <div className="mt-0.5 flex items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100">
        <button
          type="button"
          onClick={copy}
          title={locale === 'en-US' ? 'Copy' : '复制'}
          className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-ui-xs text-foreground-subtlest transition-colors hover:text-foreground"
        >
          {copied ? <Check className="size-3 text-success" /> : <Copy className="size-3" />}
          <span>{copied ? (locale === 'en-US' ? 'Copied' : '已复制') : locale === 'en-US' ? 'Copy' : '复制'}</span>
        </button>
        <button
          type="button"
          onClick={() => setComposerText(text)}
          title={locale === 'en-US' ? 'Edit in composer' : '在输入框中编辑'}
          className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-ui-xs text-foreground-subtlest transition-colors hover:text-foreground"
        >
          <Pencil className="size-3" />
          <span>{locale === 'en-US' ? 'Edit' : '编辑'}</span>
        </button>
      </div>
    </div>
  );
}

function toolActivityLabel(tc: ToolCallCard, locale: 'zh-CN' | 'en-US'): string {
  const args = parseToolArgs(tc);
  const file = baseName(String(args.file_path || args.path || ''));
  if (tc.name === 'read') return locale === 'en-US' ? `Reading ${file || 'file'}…` : `正在读取 ${file || '文件'}…`;
  if (tc.name === 'edit') return locale === 'en-US' ? `Editing ${file || 'file'}…` : `正在编辑 ${file || '文件'}…`;
  if (tc.name === 'write') return locale === 'en-US' ? `Writing ${file || 'file'}…` : `正在写入 ${file || '文件'}…`;
  if (tc.name === 'bash') {
    const cmd = truncateText(String(args.command || ''), 48);
    return locale === 'en-US' ? `Running ${cmd}…` : `正在运行 ${cmd}…`;
  }
  if (tc.name === 'grep' || tc.name === 'glob') {
    return locale === 'en-US' ? 'Searching workspace…' : '正在搜索工作区…';
  }
  if (tc.name === 'subagent') return locale === 'en-US' ? 'Delegating to subagent…' : '正在委派子智能体…';
  return locale === 'en-US' ? `Running ${tc.name}…` : `正在执行 ${tc.name}…`;
}

function WorkGroup({
  item,
  locale,
  open,
  onToggle,
}: {
  item: ChatTurnItem;
  locale: 'zh-CN' | 'en-US';
  open: boolean;
  onToggle: () => void;
}) {
  const activeTool = [...item.toolCalls].reverse().find((tc) => tc.status === 'running');
  const stepCount = item.toolCalls.length + (item.thinking ? 1 : 0);
  const visibleTools = item.toolCalls.filter((tc) => tc.status === 'running' || tc.status === 'error');
  const hiddenTools = item.toolCalls.filter((tc) => tc.status !== 'running' && tc.status !== 'error');

  const shimmerText = activeTool
    ? toolActivityLabel(activeTool, locale)
    : item.thinking
      ? locale === 'en-US'
        ? 'Thinking…'
        : '正在思考...'
      : locale === 'en-US'
        ? 'Working…'
        : '正在执行...';

  return (
    <div className="min-w-0">
      <button
        type="button"
        onClick={onToggle}
        className="inline-flex items-center gap-1 py-0.5 text-ui-sm text-foreground-subtle transition-colors hover:text-foreground"
      >
        {item.streaming ? (
          <span className="animated-gradient-text">{shimmerText}</span>
        ) : (
          <span>{locale === 'en-US' ? `Worked on ${stepCount} steps` : `思考并执行了 ${stepCount} 步`}</span>
        )}
        <ChevronRight className={`size-3.5 shrink-0 transition-transform ${open ? 'rotate-90' : ''}`} />
      </button>

      {open && (
        <div className="ml-1 mb-3 mt-2 space-y-1.5 border-l border-border pl-3.5">
          {item.thinking && (
            <div className="max-h-60 overflow-auto whitespace-pre-wrap py-0.5 text-ui-sm leading-relaxed text-foreground-subtle">
              {item.thinking}
            </div>
          )}
          {hiddenTools.map((tc) => (
            <ToolSummaryRow key={tc.id} tc={tc} />
          ))}
        </div>
      )}

      {visibleTools.length > 0 && (
        <div className="mt-0.5 flex flex-col gap-0.5">
          {visibleTools.map((tc) => (
            <ToolSummaryRow key={tc.id} tc={tc} />
          ))}
        </div>
      )}
    </div>
  );
}

const PERMISSION_OPTIONS: Array<{
  value: 'interactive' | 'full_access' | 'read_only';
  icon: typeof Hand;
  labelKey: MessageKey;
  descKey: MessageKey;
}> = [
  { value: 'interactive', icon: Hand, labelKey: 'mode.default', descKey: 'mode.default.desc' },
  { value: 'full_access', icon: ShieldCheck, labelKey: 'mode.fullAccess', descKey: 'mode.fullAccess.desc' },
  { value: 'read_only', icon: ShieldAlert, labelKey: 'mode.readOnly', descKey: 'mode.readOnly.desc' },
];

const EFFORT_OPTIONS: Array<{ value: '' | 'low' | 'medium' | 'high'; labelKey: MessageKey }> = [
  { value: 'low', labelKey: 'chat.thought.low' },
  { value: 'medium', labelKey: 'chat.thought.medium' },
  { value: 'high', labelKey: 'chat.thought.high' },
  { value: '', labelKey: 'chat.thought.off' },
];

const CHIPS = {
  'zh-CN': [
    { icon: CalendarDays, label: '周报总结', prompt: '请根据当前工作区最近的改动，生成一份简洁的周报总结。' },
    { icon: Bug, label: '排查报错', prompt: '请排查当前项目的报错与失败用例，定位根因并给出修复方案。' },
    { icon: GitPullRequest, label: '代码审查', prompt: '请审查当前未提交的改动，指出潜在问题与优化点。' },
    { icon: FolderTree, label: '架构梳理', prompt: '请梳理当前仓库的目录结构与关键入口模块。' },
  ],
  'en-US': [
    { icon: CalendarDays, label: 'Weekly Summary', prompt: 'Summarize the recent changes in this workspace as a concise weekly report.' },
    { icon: Bug, label: 'Error Fix', prompt: 'Investigate current errors and failing tests, find the root cause and propose a fix.' },
    { icon: GitPullRequest, label: 'Code Review', prompt: 'Review the current uncommitted changes and flag potential issues.' },
    { icon: FolderTree, label: 'Repo Overview', prompt: 'Give me an overview of this repository structure and key entry modules.' },
  ],
};

const PROMPT_TEMPLATES_ZH = [
  '请概览当前工作区的目录结构与关键入口模块。',
  '请审查当前未提交的改动，指出潜在问题与优化点。',
  '请运行测试并汇总失败用例与修复建议。',
];

const PROMPT_TEMPLATES_EN = [
  'Give me an overview of this workspace structure and key entry modules.',
  'Review the current uncommitted changes and flag potential issues.',
  'Run the test suite and summarize failures with fix suggestions.',
];

type ComposerMenu = 'plus' | 'model' | 'perm' | 'runner' | null;

function Composer({
  isDraft,
  locale,
  stream,
  models,
  runners,
  selectedProviderId,
  selectedModelId,
  thinkingEffort,
  permissionMode,
  selectedRunnerId,
  selectedWorkspacePath,
  activeModelLimit,
  setComposerText,
  setSelectedRunner,
  setSelectedModel,
  setThinkingEffort,
  setPermissionMode,
  sendPromptOrSteer,
  abortActiveSession,
  composerText,
}: {
  isDraft: boolean;
  locale: 'zh-CN' | 'en-US';
  stream: ReturnType<typeof emptyStreamState>;
  models: ReturnType<typeof useNexStore.getState>['models'];
  runners: ReturnType<typeof useNexStore.getState>['runners'];
  selectedProviderId: string;
  selectedModelId: string;
  thinkingEffort: '' | 'low' | 'medium' | 'high';
  permissionMode: 'full_access' | 'interactive' | 'read_only';
  selectedRunnerId: string;
  selectedWorkspacePath: string;
  activeModelLimit: number;
  setComposerText: (text: string) => void;
  setSelectedRunner: (id: string, workspacePath?: string) => void;
  setSelectedModel: (providerId: string, modelId: string) => void;
  setThinkingEffort: (effort: '' | 'low' | 'medium' | 'high') => void;
  setPermissionMode: (mode: 'full_access' | 'interactive' | 'read_only') => void;
  sendPromptOrSteer: (overrideText?: string) => void;
  abortActiveSession: () => void;
  composerText: string;
}) {
  const [openMenu, setOpenMenu] = useState<ComposerMenu>(null);
  const rootRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!openMenu) return;
    const onDown = (event: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) setOpenMenu(null);
    };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  }, [openMenu]);

  const permissionMeta = PERMISSION_OPTIONS.find((o) => o.value === permissionMode) || PERMISSION_OPTIONS[0];
  const PermissionIcon = permissionMode === 'full_access' ? ShieldAlert : permissionMeta.icon;
  const effortMeta = EFFORT_OPTIONS.find((o) => o.value === thinkingEffort) || EFFORT_OPTIONS[0];
  const activeModel = models.find((m) => m.provider_id === selectedProviderId && m.id === selectedModelId);
  const modelLabel = activeModel?.id || selectedModelId || (locale === 'en-US' ? 'Select model' : '选择模型');

  const activeRunner = runners.find((r) => r.id === selectedRunnerId);
  const runnerLabel =
    activeRunner?.name ||
    (selectedWorkspacePath.split(/[\\/]/).filter(Boolean).pop() || '') ||
    (locale === 'en-US' ? 'Select project' : '选择项目');

  const templates = locale === 'en-US' ? PROMPT_TEMPLATES_EN : PROMPT_TEMPLATES_ZH;
  const isRunning = stream.running;
  const canSend = Boolean(composerText.trim()) && Boolean(selectedRunnerId);

  const textarea = (
    <textarea
      data-testid="composer-input"
      rows={isDraft ? 2 : 1}
      value={composerText}
      onChange={(e) => setComposerText(e.target.value)}
      onKeyDown={(e) => {
        if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
          e.preventDefault();
          sendPromptOrSteer();
        }
      }}
      placeholder={
        isDraft
          ? locale === 'en-US'
            ? 'Ask Nex anything, @ to add context, / for commands or capabilities'
            : '向 Nex 提问，@ 添加上下文，/ 调用命令或能力'
          : locale === 'en-US'
            ? 'Ask for follow-up changes'
            : '继续追问或提出修改建议'
      }
      className="w-full resize-none bg-transparent text-ui-base leading-relaxed text-foreground placeholder-foreground-subtlest"
    />
  );

  const toolbar = (
    <div className="mt-1.5 flex items-center justify-between gap-2">
      {/* Left: add context + permission mode */}
      <div className="relative flex min-w-0 items-center gap-0.5">
        <button
          type="button"
          onClick={() => setOpenMenu((m) => (m === 'plus' ? null : 'plus'))}
          className="flex size-7 shrink-0 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
          title={locale === 'en-US' ? 'Add prompt or context' : '添加提示或上下文'}
        >
          <Plus className="size-4" />
        </button>

        {openMenu === 'plus' && (
          <div className="absolute bottom-9 left-0 z-40 w-72 rounded-xl border border-border bg-popover p-1.5 shadow-md">
            {templates.map((tpl) => (
              <button
                key={tpl}
                type="button"
                onClick={() => {
                  setComposerText(tpl);
                  setOpenMenu(null);
                }}
                className="flex w-full items-start gap-2 rounded-lg px-2 py-1.5 text-left text-ui-sm text-foreground hover:bg-menu-hover"
              >
                <span className="mt-0.5 shrink-0 text-foreground-subtle">
                  <Sparkles className="size-3.5" />
                </span>
                <span className="line-clamp-2">{tpl}</span>
              </button>
            ))}
            <div className="my-1 h-px bg-border" />
            <button
              type="button"
              onClick={() => {
                setComposerText(`${composerText}@`);
                setOpenMenu(null);
              }}
              className="flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left text-ui-sm text-foreground hover:bg-menu-hover"
            >
              <AtSign className="size-3.5 shrink-0 text-foreground-subtle" />
              <span>{locale === 'en-US' ? 'Reference a workspace file' : '引用工作区文件 (@)'}</span>
            </button>
          </div>
        )}

        <button
          type="button"
          data-testid="permission-mode-btn"
          onClick={() => setOpenMenu((m) => (m === 'perm' ? null : 'perm'))}
          title={formatMsg(locale, permissionMeta.labelKey)}
          className={`flex h-7 shrink-0 items-center gap-1.5 rounded-lg px-2 text-ui-sm transition-colors hover:bg-hover ${
            permissionMode === 'full_access'
              ? 'text-amber-600 hover:text-amber-600 dark:text-amber-400 dark:hover:text-amber-400'
              : 'text-foreground-subtle hover:text-foreground'
          }`}
        >
          <PermissionIcon className="size-3.5 shrink-0" />
          <span className="font-medium">{formatMsg(locale, permissionMeta.labelKey)}</span>
          <ChevronDown className="size-3.5 shrink-0 opacity-70" />
        </button>

        {openMenu === 'perm' && (
          <div className="absolute bottom-9 left-0 z-40 w-72 rounded-xl border border-border bg-popover p-1.5 shadow-md">
            {PERMISSION_OPTIONS.map((opt) => {
              const Icon = opt.icon;
              const active = opt.value === permissionMode;
              return (
                <button
                  key={opt.value}
                  type="button"
                  onClick={() => {
                    setPermissionMode(opt.value);
                    setOpenMenu(null);
                  }}
                  className={`flex w-full items-start gap-2 rounded-lg px-2 py-1.5 text-left hover:bg-menu-hover ${
                    active ? 'bg-selected' : ''
                  }`}
                >
                  <Icon className="mt-0.5 size-4 shrink-0 text-foreground-subtle" />
                  <span className="min-w-0">
                    <span className="block text-ui-sm font-medium text-foreground">
                      {formatMsg(locale, opt.labelKey)}
                    </span>
                    <span className="mt-0.5 block text-ui-xs text-foreground-subtlest">
                      {formatMsg(locale, opt.descKey)}
                    </span>
                  </span>
                  {active && <Check className="ml-auto mt-0.5 size-3.5 shrink-0 text-success" />}
                </button>
              );
            })}
          </div>
        )}
      </div>

      {/* Right: context ring + model/effort + send */}
      <div className="flex shrink-0 items-center gap-0.5">
        <ContextWindowsButton telemetry={stream.latestTelemetry} contextLimit={activeModelLimit} />

        <div className="relative">
          <button
            type="button"
            data-testid="model-effort-btn"
            onClick={() => setOpenMenu((m) => (m === 'model' ? null : 'model'))}
            title={`${modelLabel} · ${formatMsg(locale, effortMeta.labelKey)}`}
            className="flex h-7 max-w-[240px] items-center gap-1.5 rounded-lg px-2 text-ui-sm text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
          >
            <span className="truncate">{modelLabel}</span>
            <span className="shrink-0 opacity-70">{formatMsg(locale, effortMeta.labelKey)}</span>
            <ChevronDown className="size-3.5 shrink-0 opacity-70" />
          </button>

          {openMenu === 'model' && (
            <div className="absolute bottom-9 right-0 z-40 max-h-80 w-72 overflow-auto rounded-xl border border-border bg-popover p-1.5 text-ui-sm shadow-md">
              <div className="px-2 pb-1 pt-1 text-ui-xs font-medium text-foreground-subtlest">
                {locale === 'en-US' ? 'Model' : '模型'}
              </div>
              {models.length === 0 && (
                <div className="px-2 py-2.5 text-ui-sm text-foreground-subtlest">
                  {locale === 'en-US' ? 'No models configured' : '暂无可用模型'}
                </div>
              )}
              {models.map((m) => {
                const active = m.provider_id === selectedProviderId && m.id === selectedModelId;
                return (
                  <button
                    key={`${m.provider_id}::${m.id}`}
                    type="button"
                    onClick={() => {
                      setSelectedModel(m.provider_id, m.id);
                      setOpenMenu(null);
                    }}
                    className={`flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left hover:bg-menu-hover ${
                      active ? 'bg-selected' : ''
                    }`}
                  >
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-foreground">{m.id}</span>
                      <span className="block truncate text-ui-xs text-foreground-subtlest">{m.provider_id}</span>
                    </span>
                    {active && <Check className="size-3.5 shrink-0 text-success" />}
                  </button>
                );
              })}
              <div className="my-1 h-px bg-border" />
              <div className="px-2 pb-1 pt-0.5 text-ui-xs font-medium text-foreground-subtlest">
                {formatMsg(locale, 'chat.toolbar.thoughtLevel')}
              </div>
              {EFFORT_OPTIONS.map((opt) => {
                const active = opt.value === thinkingEffort;
                return (
                  <button
                    key={opt.value || 'off'}
                    type="button"
                    onClick={() => {
                      setThinkingEffort(opt.value);
                      setOpenMenu(null);
                    }}
                    className={`flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left hover:bg-menu-hover ${
                      active ? 'bg-selected' : ''
                    }`}
                  >
                    <span className="flex-1 text-foreground">{formatMsg(locale, opt.labelKey)}</span>
                    {active && <Check className="size-3.5 shrink-0 text-success" />}
                  </button>
                );
              })}
            </div>
          )}
        </div>

        {/* Automated-test compatible hidden selects (synced with the visual triggers) */}
        <select
          data-testid="model-select"
          aria-label={locale === 'en-US' ? 'Model' : '模型'}
          value={selectedProviderId && selectedModelId ? `${selectedProviderId}::${selectedModelId}` : ''}
          onChange={(e) => {
            const [pid, mid] = e.target.value.split('::');
            if (pid && mid) setSelectedModel(pid, mid);
          }}
          className="sr-only"
        >
          {models.map((m) => (
            <option key={`${m.provider_id}::${m.id}`} value={`${m.provider_id}::${m.id}`}>
              {m.provider_id}/{m.id}
            </option>
          ))}
        </select>

        {isRunning && !composerText.trim() ? (
          <button
            type="button"
            onClick={abortActiveSession}
            title={formatMsg(locale, 'chat.stop')}
            className="ml-0.5 flex size-7 shrink-0 items-center justify-center rounded-full bg-foreground text-background transition-opacity hover:opacity-90"
          >
            <Square className="size-3.5" />
          </button>
        ) : (
          <button
            type="button"
            data-testid="send-btn"
            disabled={!canSend}
            onClick={() => sendPromptOrSteer()}
            title={formatMsg(locale, isRunning ? 'chat.steer' : 'chat.send')}
            className="ml-0.5 flex size-7 shrink-0 items-center justify-center rounded-full bg-foreground text-background transition-colors hover:bg-foreground/90 disabled:bg-foreground/20 disabled:text-background/70"
          >
            <ArrowUp className="size-4" />
          </button>
        )}
      </div>
    </div>
  );

  const card = (
    <div className="rounded-2xl border border-border bg-input px-4 pb-2.5 pt-3 composer-shadow">
      {textarea}
      {toolbar}
    </div>
  );

  if (isDraft) {
    return (
      <div ref={rootRef} className="mx-auto w-full max-w-2xl">
        <div className="w-full rounded-2xl bg-black/[0.035] p-1.5 dark:bg-white/[0.04]">
          <div className="relative flex items-center px-2.5 py-1.5">
            <button
              type="button"
              data-testid="project-select-btn"
              onClick={() => setOpenMenu((m) => (m === 'runner' ? null : 'runner'))}
              className="flex min-w-0 items-center gap-1.5 rounded-lg px-1 py-0.5 text-ui-sm text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
            >
              <Folder className="size-3.5 shrink-0 text-foreground-subtle" />
              <span className="max-w-[260px] truncate">{runnerLabel}</span>
              <ChevronDown className="size-3.5 shrink-0" />
            </button>

            {openMenu === 'runner' && (
              <div className="absolute left-1.5 top-9 z-40 w-72 rounded-xl border border-border bg-popover p-1.5 shadow-md">
                {runners.length === 0 && (
                  <div className="px-2 py-3 text-center text-ui-sm text-foreground-subtlest">
                    {locale === 'en-US' ? 'No runner connected' : '暂无已连接的 Runner'}
                  </div>
                )}
                {runners.map((r) => {
                  const active = r.id === selectedRunnerId;
                  return (
                    <button
                      key={r.id}
                      type="button"
                      onClick={() => {
                        setSelectedRunner(r.id);
                        setOpenMenu(null);
                      }}
                      className={`flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left text-ui-sm hover:bg-menu-hover ${
                        active ? 'bg-selected' : ''
                      }`}
                    >
                      <Folder className="size-3.5 shrink-0 text-foreground-subtle" />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-foreground">{r.name}</span>
                        <span className="block truncate font-mono text-ui-xs text-foreground-subtlest">
                          {r.workspace_default}
                        </span>
                      </span>
                      {active && <Check className="size-3.5 shrink-0 text-success" />}
                    </button>
                  );
                })}
              </div>
            )}

            <select
              data-testid="runner-select"
              aria-label={locale === 'en-US' ? 'Runner' : 'Runner'}
              value={selectedRunnerId}
              onChange={(e) => setSelectedRunner(e.target.value)}
              className="sr-only"
            >
              {runners.length === 0 && <option value="">{locale === 'en-US' ? 'No runner' : '无 Runner'}</option>}
              {runners.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name}
                </option>
              ))}
            </select>
          </div>

          <div className="px-1">
            <div className="rounded-2xl border border-border bg-input px-4 py-3 composer-shadow">
              <textarea
                data-testid="composer-input"
                rows={2}
                value={composerText}
                onChange={(e) => setComposerText(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
                    e.preventDefault();
                    sendPromptOrSteer();
                  }
                }}
                placeholder={
                  locale === 'en-US'
                    ? 'Ask Nex anything, @ to add context, / for commands or capabilities'
                    : '向 Nex 提问，@ 添加上下文，/ 调用命令或能力'
                }
                className="min-h-[58px] w-full resize-none bg-transparent text-ui-base leading-relaxed text-foreground placeholder-foreground-subtlest"
              />
              {toolbar}
            </div>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div ref={rootRef} className="mx-auto w-full max-w-3xl">
      {card}
    </div>
  );
}

const WATERMARK_PATHS = ['M22 24h88L74 62H8z', 'M120 24h64l-44 96H82z', 'M120 24l44 96'];

export function ConversationPane() {
  const {
    locale,
    sidebarVisible,
    sidePaneVisible,
    showBottomPanel,
    runners,
    models,
    sessions,
    selectedRunnerId,
    selectedWorkspacePath,
    selectedProviderId,
    selectedModelId,
    thinkingEffort,
    permissionMode,
    activeSessionId,
    composerText,
    streams,
    pinnedSessionIds,
    toggleSidebar,
    toggleSidePane,
    toggleBottomPanel,
    setComposerText,
    setSelectedRunner,
    setSelectedModel,
    setThinkingEffort,
    setPermissionMode,
    sendPromptOrSteer,
    abortActiveSession,
    togglePinSession,
    renameSession,
    archiveSession,
  } = useNexStore();

  const [workOpen, setWorkOpen] = useState<Record<string, boolean>>({});
  const [headerMenu, setHeaderMenu] = useState<'session' | 'help' | null>(null);
  const [renaming, setRenaming] = useState(false);
  const [renameDraft, setRenameDraft] = useState('');
  const [copiedId, setCopiedId] = useState(false);
  const [scrolledUp, setScrolledUp] = useState(false);

  const scrollRef = useRef<HTMLDivElement | null>(null);
  const headerMenuRef = useRef<HTMLDivElement | null>(null);
  const turnRefs = useRef<Record<string, HTMLDivElement | null>>({});

  const currentStream = (activeSessionId && streams[activeSessionId]) || emptyStreamState();
  const activeSession = sessions.find((s) => s.id === activeSessionId);
  const activeRunner = runners.find((r) => r.id === selectedRunnerId);
  const activeModelMeta = models.find((m) => m.provider_id === selectedProviderId && m.id === selectedModelId);

  const isDraft = currentStream.items.length === 0 && !activeSessionId;
  const items = currentStream.items;

  const scrollToBottom = useCallback((behavior: ScrollBehavior = 'auto') => {
    const el = scrollRef.current;
    if (!el) return;
    el.scrollTo({ top: el.scrollHeight, behavior });
    setScrolledUp(false);
  }, []);

  useEffect(() => {
    if (!scrolledUp) {
      const el = scrollRef.current;
      if (el) el.scrollTop = el.scrollHeight;
    }
  }, [currentStream.items.length, currentStream.lastSeq, scrolledUp]);

  useEffect(() => {
    if (!headerMenu) return;
    const onDown = (event: MouseEvent) => {
      if (headerMenuRef.current && !headerMenuRef.current.contains(event.target as Node)) {
        setHeaderMenu(null);
        setRenaming(false);
      }
    };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  }, [headerMenu]);

  const sessionTitle = activeSession?.title || (locale === 'en-US' ? 'New task' : '新任务');
  const pinned = Boolean(activeSessionId && pinnedSessionIds[activeSessionId]);

  const toggleWork = (item: ChatTurnItem) => {
    const current = workOpen[item.id] ?? Boolean(item.streaming);
    setWorkOpen((p) => ({ ...p, [item.id]: !current }));
  };

  const commitRename = async () => {
    const next = renameDraft.trim();
    if (activeSessionId && next) {
      await renameSession(activeSessionId, next);
    }
    setRenaming(false);
    setHeaderMenu(null);
  };

  const copySessionId = () => {
    if (!activeSessionId) return;
    void navigator.clipboard?.writeText(activeSessionId);
    setCopiedId(true);
    window.setTimeout(() => setCopiedId(false), 1400);
    setHeaderMenu(null);
  };

  const headerControls = (
    <div className="flex shrink-0 items-center gap-1">
      <div className="relative">
        <button
          type="button"
          onClick={() => setHeaderMenu((m) => (m === 'help' ? null : 'help'))}
          title={formatMsg(locale, 'header.help')}
          className="flex size-7 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
        >
          <CircleHelp className="size-4" />
        </button>
        {headerMenu === 'help' && (
          <div className="absolute right-0 top-8 z-50 w-64 rounded-xl border border-border bg-popover p-2 shadow-md">
            <div className="px-1 pb-1.5 text-ui-sm font-medium text-foreground">{formatMsg(locale, 'header.help')}</div>
            <div className="space-y-1 px-1 text-ui-sm text-foreground-subtle">
              <div className="flex items-center justify-between gap-3">
                <span>{locale === 'en-US' ? 'Send prompt' : '发送提示'}</span>
                <kbd className="rounded bg-tag px-1.5 font-mono text-ui-xs">Enter</kbd>
              </div>
              <div className="flex items-center justify-between gap-3">
                <span>{locale === 'en-US' ? 'New line' : '换行'}</span>
                <kbd className="rounded bg-tag px-1.5 font-mono text-ui-xs">Shift+Enter</kbd>
              </div>
              <div className="flex items-center justify-between gap-3">
                <span>{locale === 'en-US' ? 'Reference file' : '引用文件'}</span>
                <kbd className="rounded bg-tag px-1.5 font-mono text-ui-xs">@</kbd>
              </div>
              <div className="flex items-center justify-between gap-3">
                <span>{locale === 'en-US' ? 'Steer running agent' : '引导运行中的 Agent'}</span>
                <kbd className="rounded bg-tag px-1.5 font-mono text-ui-xs">Enter</kbd>
              </div>
            </div>
          </div>
        )}
      </div>

      <button
        type="button"
        data-testid="terminal-toggle"
        onClick={toggleBottomPanel}
        title={formatMsg(locale, 'terminal.toggle')}
        className={`flex size-7 items-center justify-center rounded-lg transition-colors ${
          showBottomPanel ? 'bg-selected text-foreground' : 'text-foreground-subtle hover:bg-hover hover:text-foreground'
        }`}
      >
        <SquareTerminal className="size-4" />
      </button>

      {!sidePaneVisible && (
        <button
          type="button"
          data-testid="side-pane-toggle"
          onClick={toggleSidePane}
          title={formatMsg(locale, 'sidePane.expand')}
          className="flex size-7 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
        >
          <PanelRight className="size-4" />
        </button>
      )}
    </div>
  );

  return (
    <section className="relative flex h-full min-h-0 min-w-0 flex-1 flex-col bg-background text-foreground">
      {isDraft ? (
        <header className="flex h-12 shrink-0 items-center justify-between px-4">
          <div className="flex min-w-0 items-center gap-1">
            {sidebarVisible && (
              <button
                type="button"
                data-testid="sidebar-toggle-btn"
                onClick={toggleSidebar}
                title={locale === 'en-US' ? 'Toggle sidebar' : '切换侧边栏'}
                className="flex size-7 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
              >
                <PanelLeft className="size-4" />
              </button>
            )}
          </div>
          {headerControls}
        </header>
      ) : (
        <header className="flex h-12 shrink-0 items-center justify-between gap-2 border-b border-border bg-background px-4">
          <div className="flex min-w-0 items-center gap-1.5">
            {sidebarVisible && (
              <button
                type="button"
                data-testid="sidebar-toggle-btn"
                onClick={toggleSidebar}
                title={locale === 'en-US' ? 'Toggle sidebar' : '切换侧边栏'}
                className="flex size-7 shrink-0 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
              >
                <PanelLeft className="size-4" />
              </button>
            )}
            <h1 className="max-w-md truncate text-ui-base font-semibold text-foreground">{sessionTitle}</h1>

            <div className="relative" ref={headerMenuRef}>
              <button
                type="button"
                data-testid="session-menu-btn"
                onClick={() => {
                  setRenaming(false);
                  setHeaderMenu((m) => (m === 'session' ? null : 'session'));
                }}
                className="flex size-7 shrink-0 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
              >
                <Ellipsis className="size-4" />
              </button>

              {headerMenu === 'session' && (
                <div className="absolute left-0 top-8 z-50 w-52 rounded-xl border border-border bg-popover p-1 shadow-md">
                  {renaming ? (
                    <form
                      onSubmit={(e) => {
                        e.preventDefault();
                        void commitRename();
                      }}
                      className="flex items-center gap-1 p-1"
                    >
                      <input
                        autoFocus
                        value={renameDraft}
                        onChange={(e) => setRenameDraft(e.target.value)}
                        onKeyDown={(e) => {
                          if (e.key === 'Escape') setRenaming(false);
                        }}
                        className="h-7 min-w-0 flex-1 rounded-md border border-border bg-input px-2 text-ui-sm text-foreground"
                      />
                      <button
                        type="submit"
                        className="flex size-7 shrink-0 items-center justify-center rounded-md text-success hover:bg-hover"
                      >
                        <Check className="size-3.5" />
                      </button>
                      <button
                        type="button"
                        onClick={() => setRenaming(false)}
                        className="flex size-7 shrink-0 items-center justify-center rounded-md text-foreground-subtle hover:bg-hover"
                      >
                        <X className="size-3.5" />
                      </button>
                    </form>
                  ) : (
                    <>
                      <button
                        type="button"
                        onClick={() => {
                          if (activeSessionId) togglePinSession(activeSessionId);
                          setHeaderMenu(null);
                        }}
                        className="flex h-8 w-full items-center gap-2 rounded-lg px-2 text-left text-ui-sm text-foreground hover:bg-menu-hover"
                      >
                        {pinned ? (
                          <PinOff className="size-3.5 shrink-0 text-foreground-subtle" />
                        ) : (
                          <Pin className="size-3.5 shrink-0 text-foreground-subtle" />
                        )}
                        <span>{formatMsg(locale, pinned ? 'taskList.unpin' : 'taskList.pin')}</span>
                      </button>
                      <button
                        type="button"
                        onClick={() => {
                          setRenameDraft(sessionTitle);
                          setRenaming(true);
                        }}
                        className="flex h-8 w-full items-center gap-2 rounded-lg px-2 text-left text-ui-sm text-foreground hover:bg-menu-hover"
                      >
                        <Pencil className="size-3.5 shrink-0 text-foreground-subtle" />
                        <span>{formatMsg(locale, 'taskList.rename')}</span>
                      </button>
                      <button
                        type="button"
                        onClick={() => {
                          if (activeSessionId) void archiveSession(activeSessionId);
                          setHeaderMenu(null);
                        }}
                        className="flex h-8 w-full items-center gap-2 rounded-lg px-2 text-left text-ui-sm text-foreground hover:bg-menu-hover"
                      >
                        <Archive className="size-3.5 shrink-0 text-foreground-subtle" />
                        <span>{formatMsg(locale, 'taskList.archive')}</span>
                      </button>
                      <div className="my-1 h-px bg-border" />
                      <button
                        type="button"
                        onClick={copySessionId}
                        className="flex h-8 w-full items-center gap-2 rounded-lg px-2 text-left text-ui-sm text-foreground hover:bg-menu-hover"
                      >
                        {copiedId ? (
                          <Check className="size-3.5 shrink-0 text-success" />
                        ) : (
                          <Copy className="size-3.5 shrink-0 text-foreground-subtle" />
                        )}
                        <span>
                          {copiedId
                            ? locale === 'en-US'
                              ? 'Copied'
                              : '已复制'
                            : locale === 'en-US'
                              ? 'Copy session ID'
                              : '复制会话 ID'}
                        </span>
                      </button>
                    </>
                  )}
                </div>
              )}
            </div>
          </div>

          {headerControls}
        </header>
      )}

      {/* Scrollable conversation timeline */}
      <div className="relative min-h-0 flex-1">
        <div
          ref={scrollRef}
          data-testid="chat-stream"
          onScroll={(e) => {
            const el = e.currentTarget;
            setScrolledUp(el.scrollHeight - el.scrollTop - el.clientHeight > 120);
          }}
          className="absolute inset-0 overflow-y-auto px-4 py-5 sm:px-8"
        >
          {isDraft ? (
            <div className="flex h-full flex-col items-center justify-center pb-10">
              <svg
                viewBox="0 0 224 144"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.25"
                strokeLinejoin="round"
                aria-hidden="true"
                className="pointer-events-none mb-2 h-36 w-56 text-foreground/[0.06]"
              >
                {WATERMARK_PATHS.map((d) => (
                  <path key={d} d={d} />
                ))}
              </svg>
              <h1 className="mb-7 text-center text-[26px] font-semibold tracking-tight text-foreground">
                {formatMsg(locale, getGreetingKey(new Date().getHours()))}
              </h1>

              <Composer
                isDraft
                locale={locale}
                stream={currentStream}
                models={models}
                runners={runners}
                selectedProviderId={selectedProviderId}
                selectedModelId={selectedModelId}
                thinkingEffort={thinkingEffort}
                permissionMode={permissionMode}
                selectedRunnerId={selectedRunnerId}
                selectedWorkspacePath={selectedWorkspacePath}
                activeModelLimit={activeModelMeta?.context_window || 200000}
                setComposerText={setComposerText}
                setSelectedRunner={setSelectedRunner}
                setSelectedModel={setSelectedModel}
                setThinkingEffort={setThinkingEffort}
                setPermissionMode={setPermissionMode}
                sendPromptOrSteer={sendPromptOrSteer}
                abortActiveSession={abortActiveSession}
                composerText={composerText}
              />

              <div className="mt-5 flex flex-wrap items-center justify-center gap-2.5">
                {CHIPS[locale].map((chip) => {
                  const Icon = chip.icon;
                  return (
                    <button
                      key={chip.label}
                      type="button"
                      onClick={() => {
                        setComposerText(chip.prompt);
                      }}
                      className="flex h-8 items-center gap-2 rounded-xl border border-border bg-card px-3.5 text-ui-sm text-foreground-subtle shadow-2xs transition-colors hover:border-border-hover hover:text-foreground"
                    >
                      <Icon className="size-3.5 shrink-0" />
                      <span>{chip.label}</span>
                    </button>
                  );
                })}
              </div>
            </div>
          ) : (
            <div className="mx-auto max-w-3xl space-y-5">
              {items.map((item) => (
                <div
                  key={item.id}
                  ref={(el) => {
                    turnRefs.current[item.id] = el;
                  }}
                  className={`flex flex-col ${item.role === 'user' ? 'items-end' : 'items-start'}`}
                >
                  {item.role === 'user' ? (
                    <UserBubble text={item.text} />
                  ) : (
                    <div className="min-w-0 w-full space-y-2.5">
                      {(item.thinking || item.toolCalls.length > 0) && (
                        <WorkGroup
                          item={item}
                          locale={locale}
                          open={workOpen[item.id] ?? Boolean(item.streaming)}
                          onToggle={() => toggleWork(item)}
                        />
                      )}

                      {item.text && <MarkdownRenderer content={item.text} />}

                      <ChangeSummaryCard item={item} />

                      {item.streaming && !item.thinking && item.toolCalls.length === 0 && (
                        <div className="flex items-center gap-1.5 text-ui-xs text-foreground-subtlest">
                          <LoaderCircle className="size-3 animate-spin" />
                          <span>{locale === 'en-US' ? 'Generating…' : '正在流式生成...'}</span>
                        </div>
                      )}
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>

        {/* Left-edge turn tick navigator */}
        {!isDraft && items.length > 0 && (
          <div className="absolute left-3 top-1/2 hidden -translate-y-1/2 flex-col items-center gap-2 lg:flex">
            {items.map((item, idx) => (
              <button
                key={item.id}
                type="button"
                title={`Turn ${idx + 1}`}
                onClick={() => {
                  const el = turnRefs.current[item.id];
                  el?.scrollIntoView({ behavior: 'smooth', block: 'center' });
                }}
                className="h-[1.5px] w-2.5 bg-foreground/20 transition-colors hover:bg-foreground"
              />
            ))}
          </div>
        )}

        {/* Floating scroll-to-bottom control */}
        {!isDraft && scrolledUp && (
          <button
            type="button"
            onClick={() => scrollToBottom('smooth')}
            title={locale === 'en-US' ? 'Scroll to bottom' : '回到底部'}
            className="absolute bottom-3 left-1/2 z-20 flex size-7 -translate-x-1/2 items-center justify-center rounded-full border border-border bg-card text-foreground-subtle shadow-sm transition-colors hover:text-foreground"
          >
            <ChevronDown className="size-4" />
          </button>
        )}

        <ConversationStatusPanel />
      </div>

      {/* Sticky bottom Composer (draft mode renders its own centered tray inside the canvas) */}
      {!isDraft && (
        <div className="shrink-0 px-4 pb-3.5 pt-2 sm:px-8">
          <Composer
            isDraft={false}
            locale={locale}
            stream={currentStream}
            models={models}
            runners={runners}
            selectedProviderId={selectedProviderId}
            selectedModelId={selectedModelId}
            thinkingEffort={thinkingEffort}
            permissionMode={permissionMode}
            selectedRunnerId={selectedRunnerId}
            selectedWorkspacePath={selectedWorkspacePath}
            activeModelLimit={activeModelMeta?.context_window || 200000}
            setComposerText={setComposerText}
            setSelectedRunner={setSelectedRunner}
            setSelectedModel={setSelectedModel}
            setThinkingEffort={setThinkingEffort}
            setPermissionMode={setPermissionMode}
            sendPromptOrSteer={sendPromptOrSteer}
            abortActiveSession={abortActiveSession}
            composerText={composerText}
          />
        </div>
      )}
    </section>
  );
}
