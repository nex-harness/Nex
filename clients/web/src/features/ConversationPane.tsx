import {
  Archive,
  ArrowUp,
  AtSign,
  BookOpen,
  Bot,
  Bug,
  CalendarDays,
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  CircleHelp,
  Copy,
  Cpu,
  Ellipsis,
  FileText,
  Folder,
  FolderPlus,
  FolderTree,
  GitBranch,
  GitPullRequest,
  Globe,
  Hand,
  ImagePlus,
  ListTodo,
  LoaderCircle,
  PanelLeft,
  PanelRight,
  Pencil,
  Pin,
  PinOff,
  Plug,
  Plus,
  Search,
  Settings,
  ShieldAlert,
  ShieldCheck,
  Sparkles,
  Square,
  SquareTerminal,
  X,
} from 'lucide-react';
import { type CSSProperties, memo, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { emptyStreamState } from '../core/reducer';
import { useNexStore } from '../core/store';
import { ChatTurnItem, ModelMeta, ThinkingEffort, ToolCallCard } from '../core/types';
import { formatMsg } from '../i18n/messages';
import type { MessageKey } from '../i18n/messages';
import { ContextWindowsButton } from './ContextPopover';
import { ConversationStatusPanel } from './ConversationStatusPanel';
import { DirectoryBrowserModal } from './DirectoryBrowserModal';
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

function ToolIndeterminateBar({ active }: { active: boolean }) {
  if (!active) return null;
  return (
    <div className="relative mt-1 h-[2px] w-48 overflow-hidden rounded-full bg-foreground/10">
      <div className="tool-call-progress-indeterminate absolute inset-y-0 w-1/3 rounded-full bg-foreground/45" />
    </div>
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
        <div className="zcode-collapsible" data-open={open && Boolean(output)}>
          <div className="zcode-collapsible-inner">
            {output && (
              <div className="ml-2 mt-1 max-h-60 overflow-auto border-l border-border pl-3.5 font-mono text-ui-sm whitespace-pre-wrap text-foreground-subtle">
                {output}
              </div>
            )}
          </div>
        </div>
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
        <div className="zcode-collapsible" data-open={open && Boolean(tc.diff || output)}>
          <div className="zcode-collapsible-inner">
            {(tc.diff || output) && (
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
        </div>
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
        <ToolIndeterminateBar active={running} />
        <div className="zcode-collapsible" data-open={open}>
          <div className="zcode-collapsible-inner">
            <div className="mb-2 mt-1.5 rounded-xl border border-border bg-card px-3.5 py-2.5 font-mono text-ui-sm">
              <div className="truncate text-foreground-subtle">
                <span className="text-foreground-subtlest">$ </span>
                {command}
              </div>
              {output && (
                <pre className="mt-1.5 max-h-52 overflow-auto whitespace-pre-wrap text-foreground-subtle">{output}</pre>
              )}
            </div>
          </div>
        </div>
      </div>
    );
  }

  if (tc.name === 'webfetch') {
    const url = String(args.url || '');
    return (
      <div className="flex max-w-full flex-col">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className={`group/tool inline-flex max-w-full items-center gap-2 py-0.5 text-left ${rowClass}`}
        >
          <Globe className="size-3.5 shrink-0 text-foreground-subtlest" />
          <span className="shrink-0 font-medium text-foreground-subtle">
            {running ? (locale === 'en-US' ? 'Fetching' : '抓取中') : locale === 'en-US' ? 'Fetched' : '已抓取'}
          </span>
          {url && <span className="min-w-0 truncate font-mono text-ui-sm text-foreground-subtle">{truncateText(url, 72)}</span>}
          {chevron()}
        </button>
        <ToolIndeterminateBar active={running} />
        <div className="zcode-collapsible" data-open={open && Boolean(output)}>
          <div className="zcode-collapsible-inner">
            {output && (
              <div className="ml-2 mt-1 max-h-60 overflow-auto border-l border-border pl-3.5 font-mono text-ui-sm whitespace-pre-wrap text-foreground-subtle">
                {output}
              </div>
            )}
          </div>
        </div>
      </div>
    );
  }

  if (tc.name === 'ask_user_question') {
    const question = String(args.question || '');
    return (
      <div className="flex max-w-full flex-col">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className={`group/tool inline-flex max-w-full items-center gap-2 py-0.5 text-left ${rowClass}`}
        >
          <CircleHelp className="size-3.5 shrink-0 text-foreground-subtlest" />
          <span className="shrink-0 font-medium text-foreground-subtle">
            {running ? (locale === 'en-US' ? 'Asking' : '等待回答') : locale === 'en-US' ? 'Asked' : '已询问'}
          </span>
          {question && <span className="min-w-0 truncate text-foreground-subtle">{truncateText(question, 72)}</span>}
          {chevron()}
        </button>
        <div className="zcode-collapsible" data-open={open && Boolean(output)}>
          <div className="zcode-collapsible-inner">
            {output && (
              <div className="ml-2 mt-1 max-h-40 overflow-auto border-l border-border pl-3.5 text-ui-sm whitespace-pre-wrap text-foreground-subtle">
                {output}
              </div>
            )}
          </div>
        </div>
      </div>
    );
  }

  if (tc.name === 'task_output' || tc.name === 'task_stop') {
    const taskId = String(args.task_id || '');
    const title =
      tc.name === 'task_stop'
        ? locale === 'en-US'
          ? 'Stopped task'
          : '已停止后台任务'
        : running
          ? locale === 'en-US'
            ? 'Checking task'
            : '读取后台任务'
          : locale === 'en-US'
            ? 'Checked task'
            : '已读取后台任务';
    return (
      <div className="flex max-w-full flex-col">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className={`group/tool inline-flex max-w-full items-center gap-2 py-0.5 text-left ${rowClass}`}
        >
          <SquareTerminal className="size-3.5 shrink-0 text-foreground-subtlest" />
          <span className="shrink-0 font-medium text-foreground-subtle">{title}</span>
          {taskId && <span className="min-w-0 truncate font-mono text-ui-sm text-foreground-subtle">{taskId}</span>}
          {chevron()}
        </button>
        <div className="zcode-collapsible" data-open={open && Boolean(output)}>
          <div className="zcode-collapsible-inner">
            {output && (
              <pre className="ml-2 mt-1 max-h-60 overflow-auto border-l border-border pl-3.5 font-mono text-ui-sm whitespace-pre-wrap text-foreground-subtle">
                {output}
              </pre>
            )}
          </div>
        </div>
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
        <ToolIndeterminateBar active={running} />
        <div className="zcode-collapsible" data-open={open && Boolean(output)}>
          <div className="zcode-collapsible-inner">
            {output && (
              <div className="mb-2 mt-1.5 max-h-60 overflow-auto rounded-xl border border-border bg-card p-2.5 font-mono text-[12px] leading-5 whitespace-pre-wrap text-foreground-subtle">
                {output}
              </div>
            )}
          </div>
        </div>
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
          <Bot className={`size-3.5 shrink-0 text-success ${running ? 'animate-pulse' : ''}`} />
          <span className={`shrink-0 font-medium ${running ? 'animated-gradient-text' : 'text-foreground-subtle'}`}>
            {formatMsg(locale, 'tool.subagent')}
          </span>
          {summary && <span className="min-w-0 truncate text-foreground-subtle">{summary}</span>}
          {chevron()}
        </button>
        <ToolIndeterminateBar active={running} />
        <div className="zcode-collapsible" data-open={open && Boolean(output)}>
          <div className="zcode-collapsible-inner">
            {output && (
              <div className="ml-2 mt-1 max-h-60 overflow-auto border-l border-border pl-3.5 text-ui-sm whitespace-pre-wrap text-foreground-subtle">
                {output}
              </div>
            )}
          </div>
        </div>
      </div>
    );
  }

  if (tc.name === 'todo_write' || tc.name === 'todo_read') {
    const rawTodos = Array.isArray(args.todos) ? (args.todos as Array<{ content?: string; status?: string }>) : [];
    const doneCount = rawTodos.filter((it) => it.status === 'completed').length;
    const activeTodo = rawTodos.find((it) => it.status === 'in_progress') || rawTodos[0];
    return (
      <div className={`group/tool inline-flex max-w-full items-center gap-2 py-0.5 text-left ${rowClass}`}>
        <ListTodo className="size-3.5 shrink-0 text-foreground-subtlest" />
        <span className="shrink-0 font-medium text-foreground-subtle">
          {locale === 'en-US' ? 'Updated plan' : '更新进度清单'}
        </span>
        {activeTodo?.content && <span className="min-w-0 truncate text-foreground-subtle">{activeTodo.content}</span>}
        {rawTodos.length > 0 && (
          <span className="shrink-0 font-mono text-ui-xs tabular-nums text-foreground-subtlest">
            {doneCount}/{rawTodos.length}
          </span>
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
      <div className="zcode-collapsible" data-open={open}>
        <div className="zcode-collapsible-inner">
          <div className="border-t border-border py-1">
            {files.map((f) => (
              <button
                key={f.path}
                type="button"
                onClick={() => void openFileInPreviewTab(f.path)}
                className="flex w-full items-center justify-between gap-2 px-3 py-1 text-left text-ui-sm transition-colors hover:bg-hover"
              >
                <span className="truncate font-mono text-foreground-subtle">{f.path}</span>
                <span className="shrink-0 font-mono text-ui-xs tabular-nums">
                  <span className="text-success">+{f.adds}</span> <span className="text-destructive">-{f.dels}</span>
                </span>
              </button>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

function cleanUserDisplayText(raw: string): string {
  const goalMatch = /<untrusted_objective>\s*([\s\S]*?)\s*<\/untrusted_objective>/i.exec(raw);
  if (goalMatch && goalMatch[1]) {
    return `/goal ${goalMatch[1].trim()}`;
  }
  return raw.replace(/<system-reminder>[\s\S]*?<\/system-reminder>/gi, '').trim() || raw;
}

function UserBubble({
  text,
  images,
  seq,
}: {
  text: string;
  images?: ChatTurnItem['images'];
  seq?: number;
}) {
  const locale = useNexStore((s) => s.locale);
  const activeSessionId = useNexStore((s) => s.activeSessionId);
  const setComposerText = useNexStore((s) => s.setComposerText);
  const forkSession = useNexStore((s) => s.forkSession);
  const [copied, setCopied] = useState(false);
  const displayText = cleanUserDisplayText(text);

  const copy = () => {
    void navigator.clipboard?.writeText(displayText);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1400);
  };

  return (
    <div className="group flex flex-col items-end">
      {images && images.length > 0 && (
        <div className="mb-1.5 flex flex-wrap justify-end gap-1.5">
          {images.map((img, idx) => {
            const src = img.image_url || (img.data_b64 ? `data:${img.mime_type || 'image/png'};base64,${img.data_b64}` : '');
            if (!src) return null;
            return (
              <img
                key={idx}
                src={src}
                alt={`attachment-${idx + 1}`}
                className="max-h-36 max-w-48 rounded-xl border border-border object-cover shadow-xs"
              />
            );
          })}
        </div>
      )}
      {displayText && (
        <div className="max-w-xl whitespace-pre-wrap break-words rounded-2xl bg-black/[0.045] px-4 py-2.5 text-ui-base text-foreground dark:bg-white/[0.08]">
          {displayText}
        </div>
      )}
      <div className="mt-0.5 flex items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100">
        <button
          type="button"
          onClick={copy}
          title={locale === 'en-US' ? 'Copy' : '复制'}
          className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-ui-xs text-foreground-subtlest transition-colors hover:text-foreground"
        >
          {copied ? <Check className="zcode-reaction-pop size-3 text-success" /> : <Copy className="size-3" />}
          <span>{copied ? (locale === 'en-US' ? 'Copied' : '已复制') : locale === 'en-US' ? 'Copy' : '复制'}</span>
        </button>
        <button
          type="button"
          onClick={() => setComposerText(displayText)}
          title={locale === 'en-US' ? 'Edit in composer' : '在输入框中编辑'}
          className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-ui-xs text-foreground-subtlest transition-colors hover:text-foreground"
        >
          <Pencil className="size-3" />
          <span>{locale === 'en-US' ? 'Edit' : '编辑'}</span>
        </button>
        {activeSessionId && seq && seq > 0 && (
          <button
            type="button"
            data-testid={`fork-before-${seq}`}
            onClick={() => void forkSession(activeSessionId, { beforeSeq: seq, prefillText: displayText })}
            title={locale === 'en-US' ? 'Branch & edit from before this message' : '从此处创建新分支并编辑重发'}
            className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-ui-xs text-foreground-subtlest transition-colors hover:text-foreground"
          >
            <GitBranch className="size-3" />
            <span>{locale === 'en-US' ? 'Branch & Edit' : '编辑分支'}</span>
          </button>
        )}
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

      <div className="zcode-collapsible" data-open={open}>
        <div className="zcode-collapsible-inner">
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
        </div>
      </div>

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
  { value: 'read_only', icon: BookOpen, labelKey: 'mode.readOnly', descKey: 'mode.readOnly.desc' },
];

interface EffortLevelOption {
  value: ThinkingEffort;
  labelKey?: MessageKey;
  customLabel?: string;
  rank: number;
}

const EFFORT_CATALOG: Record<string, EffortLevelOption> = {
  off: { value: '', labelKey: 'chat.thought.off', rank: 0 },
  none: { value: '', labelKey: 'chat.thought.off', rank: 0 },
  '': { value: '', labelKey: 'chat.thought.off', rank: 0 },
  minimal: { value: 'minimal', labelKey: 'chat.thought.minimal', rank: 1 },
  low: { value: 'low', labelKey: 'chat.thought.low', rank: 2 },
  medium: { value: 'medium', labelKey: 'chat.thought.medium', rank: 3 },
  on: { value: 'on', labelKey: 'chat.thought.on', rank: 3 },
  high: { value: 'high', labelKey: 'chat.thought.high', rank: 4 },
  xhigh: { value: 'xhigh', labelKey: 'chat.thought.xhigh', rank: 5 },
  max: { value: 'max', labelKey: 'chat.thought.max', rank: 6 },
};

const DEFAULT_EFFORT_LEVELS: EffortLevelOption[] = [
  EFFORT_CATALOG.off,
  EFFORT_CATALOG.low,
  EFFORT_CATALOG.medium,
  EFFORT_CATALOG.high,
];

function formatEffortLabel(opt: EffortLevelOption | undefined, locale: 'zh-CN' | 'en-US'): string {
  if (!opt) return '';
  if (opt.labelKey) return formatMsg(locale, opt.labelKey);
  return opt.customLabel || String(opt.value || '');
}

function canonicalEffortRank(val: string): number {
  const lower = (val || '').trim().toLowerCase();
  const known = EFFORT_CATALOG[lower];
  return known ? known.rank : -1;
}

function resolveEffortLevels(model?: ModelMeta): EffortLevelOption[] {
  const raw = model?.reasoning_levels;
  if (model && !model.supports_reasoning && (!raw || raw.length === 0)) {
    return [];
  }
  if (!raw || raw.length < 2) return DEFAULT_EFFORT_LEVELS;
  const seen = new Set<string>();
  const resolved: EffortLevelOption[] = [];
  for (let i = 0; i < raw.length; i++) {
    const clean = raw[i].trim();
    if (!clean) continue;
    const lower = clean.toLowerCase();
    const known = EFFORT_CATALOG[lower];
    const entry: EffortLevelOption = known
      ? known
      : {
          value: clean,
          customLabel: clean.charAt(0).toUpperCase() + clean.slice(1),
          // Interpolate rank from position so custom words (e.g. turbo/ultra) clamp cleanly!
          rank: Math.max(1, Math.round((i / Math.max(1, raw.length - 1)) * 6)),
        };
    if (seen.has(entry.value)) continue;
    seen.add(entry.value);
    resolved.push(entry);
  }
  return resolved.length >= 2 ? resolved : DEFAULT_EFFORT_LEVELS;
}

function clampEffortIndex(levels: EffortLevelOption[], requested: ThinkingEffort): number {
  if (levels.length === 0) return 0;
  const exactIdx = levels.findIndex((o) => o.value === requested);
  if (exactIdx >= 0) return exactIdx;

  const reqRank = canonicalEffortRank(String(requested));
  if (reqRank === 0) {
    const offIdx = levels.findIndex((o) => o.rank === 0);
    return offIdx >= 0 ? offIdx : 0;
  }

  // Clamp by canonical rank: find the highest supported non-off level <= reqRank (or closest positive level)
  if (reqRank > 0) {
    let bestIdx = -1;
    let bestRank = -1;
    for (let i = 0; i < levels.length; i++) {
      if (levels[i].rank > 0 && levels[i].rank <= reqRank && levels[i].rank > bestRank) {
        bestRank = levels[i].rank;
        bestIdx = i;
      }
    }
    if (bestIdx >= 0) return bestIdx;
    const firstPos = levels.findIndex((o) => o.rank > 0);
    if (firstPos >= 0) return firstPos;
  }

  const medIdx = levels.findIndex((o) => o.value === 'medium' || o.value === 'on');
  return medIdx >= 0 ? medIdx : Math.min(1, levels.length - 1);
}

function formatContextLabel(n: number): string {
  if (n >= 1_000_000) return `${Number((n / 1_000_000).toFixed(1))}M`;
  if (n >= 1_000) return `${Math.round(n / 1_000)}K`;
  return String(n);
}

function isGlmHighspeedModel(name: string): boolean {
  return /glm/i.test(name) && /highspeed|flashx/i.test(name);
}

// Fixed seeds matching ZCode ComposerModelMenu.tsx so bubbles flow smoothly without re-randomizing.
const EFFORT_BUBBLES = [
  { top: 4, size: 2, duration: 2.6, delay: -0.4 },
  { top: 14, size: 3, duration: 3.2, delay: -2.1 },
  { top: 8, size: 4, duration: 2.8, delay: -1.1 },
  { top: 18, size: 2, duration: 3.6, delay: -0.8 },
  { top: 3, size: 3, duration: 3.0, delay: -2.5 },
  { top: 12, size: 2, duration: 3.4, delay: -1.7 },
  { top: 6, size: 3, duration: 3.1, delay: -0.9 },
  { top: 17, size: 4, duration: 2.9, delay: -2.3 },
  { top: 10, size: 2, duration: 3.5, delay: -0.2 },
  { top: 2, size: 2, duration: 2.7, delay: -1.8 },
  { top: 15, size: 3, duration: 3.3, delay: -1.2 },
  { top: 7, size: 4, duration: 3.6, delay: -3.1 },
];

const HIGHSPEED_BUBBLES = Array.from({ length: 32 }, (_, index) => ({
  ...EFFORT_BUBBLES[index % EFFORT_BUBBLES.length],
  duration: (8 + (index % 5)) / 10,
  delay: -index * 0.137,
}));

function RollingLabel({ text, className = '' }: { text: string; className?: string }) {
  return (
    <span className={`relative inline-flex h-[1.3em] items-center overflow-hidden ${className}`}>
      <span key={text} className="zcode-label-roll inline-flex whitespace-nowrap leading-[1.25]">
        {text}
      </span>
    </span>
  );
}

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

interface SlashCmdEntry {
  command: string;
  insertText: string;
  badge: string;
  description: string;
}

interface AutocompleteTrigger {
  kind: 'mention' | 'slash';
  query: string;
  replaceStart: number;
  replaceEnd: number;
}

function detectAutocomplete(text: string, cursor: number): AutocompleteTrigger | null {
  const safeCursor = Math.max(0, Math.min(cursor, text.length));
  const before = text.slice(0, safeCursor);

  // Leading `/command` autocomplete (only on the first token)
  const slashMatch = /^\/([^\s]*)$/.exec(before);
  if (slashMatch) {
    return {
      kind: 'slash',
      query: slashMatch[1] || '',
      replaceStart: 0,
      replaceEnd: safeCursor,
    };
  }

  // Inline `@file` mention autocomplete (at start or after whitespace)
  const mentionMatch = /(?:^|\s)@([^\s@]*)$/.exec(before);
  if (mentionMatch) {
    const query = mentionMatch[1] || '';
    const atPos = safeCursor - query.length - 1;
    return {
      kind: 'mention',
      query,
      replaceStart: atPos,
      replaceEnd: safeCursor,
    };
  }

  return null;
}

function readImageFileAsAttachment(file: File): Promise<import('../core/types').ComposerAttachment | null> {
  return new Promise((resolve) => {
    if (!file.type.startsWith('image/') || file.size > 8 * 1024 * 1024) {
      resolve(null);
      return;
    }
    const reader = new FileReader();
    reader.onload = () => {
      const dataUrl = typeof reader.result === 'string' ? reader.result : '';
      const commaIdx = dataUrl.indexOf(',');
      if (commaIdx < 0) {
        resolve(null);
        return;
      }
      resolve({
        id: `img_${crypto.randomUUID().slice(0, 8)}`,
        name: file.name || `image.${file.type.split('/')[1] || 'png'}`,
        mimeType: file.type,
        dataB64: dataUrl.slice(commaIdx + 1),
        previewUrl: dataUrl,
        size: file.size,
      });
    };
    reader.onerror = () => resolve(null);
    reader.readAsDataURL(file);
  });
}

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
  setSelectedProvider,
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
  thinkingEffort: ThinkingEffort;
  permissionMode: 'full_access' | 'interactive' | 'read_only';
  selectedRunnerId: string;
  selectedWorkspacePath: string;
  activeModelLimit: number;
  setComposerText: (text: string) => void;
  setSelectedRunner: (id: string, workspacePath?: string) => void;
  setSelectedProvider: (providerId: string) => void;
  setSelectedModel: (providerId: string, modelId: string) => void;
  setThinkingEffort: (effort: ThinkingEffort) => void;
  setPermissionMode: (mode: 'full_access' | 'interactive' | 'read_only') => void;
  sendPromptOrSteer: (overrideText?: string) => void;
  abortActiveSession: () => void;
  composerText: string;
}) {
  const [openMenu, setOpenMenu] = useState<ComposerMenu>(null);
  const [providerMenuOpen, setProviderMenuOpen] = useState(false);
  const [providerSubmenuSide, setProviderSubmenuSide] = useState<'side' | 'inline'>('side');
  const [dirBrowserOpen, setDirBrowserOpen] = useState(false);
  const [projectSearch, setProjectSearch] = useState('');
  const [hoveredStop, setHoveredStop] = useState<string | null>(null);
  const [cursorPos, setCursorPos] = useState<number>(composerText.length);
  const [mentionFiles, setMentionFiles] = useState<string[]>([]);
  const [mentionLoading, setMentionLoading] = useState(false);
  const [acDismissed, setAcDismissed] = useState(false);
  const [activeAcIdx, setActiveAcIdx] = useState(0);
  const [isDraggingImage, setIsDraggingImage] = useState(false);
  const rootRef = useRef<HTMLDivElement | null>(null);
  const modelPopoverRef = useRef<HTMLDivElement | null>(null);
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);

  const providers = useNexStore((s) => s.providers);
  const sessions = useNexStore((s) => s.sessions);
  const skills = useNexStore((s) => s.skills);
  const plugins = useNexStore((s) => s.plugins);
  const recentWorkspaces = useNexStore((s) => s.recentWorkspaces);
  const hiddenModelKeys = useNexStore((s) => s.hiddenModelKeys);
  const modelOrder = useNexStore((s) => s.modelOrder);
  const composerAttachments = useNexStore((s) => s.composerAttachments);
  const addComposerAttachment = useNexStore((s) => s.addComposerAttachment);
  const removeComposerAttachment = useNexStore((s) => s.removeComposerAttachment);
  const searchWorkspaceFiles = useNexStore((s) => s.searchWorkspaceFiles);

  useEffect(() => {
    if (!openMenu) {
      setProviderMenuOpen(false);
      return;
    }
    const onDown = (event: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) {
        setOpenMenu(null);
        setProviderMenuOpen(false);
      }
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        if (providerMenuOpen) {
          setProviderMenuOpen(false);
        } else {
          setOpenMenu(null);
        }
      }
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [openMenu, providerMenuOpen]);

  const permissionMeta = PERMISSION_OPTIONS.find((o) => o.value === permissionMode) || PERMISSION_OPTIONS[0];
  const PermissionIcon = permissionMode === 'full_access' ? ShieldAlert : permissionMeta.icon;

  const activeProvider = providers.find((p) => p.id === selectedProviderId) || providers[0];
  const effectiveProviderId = selectedProviderId || activeProvider?.id || '';
  const activeProviderName = activeProvider?.name || effectiveProviderId || formatMsg(locale, 'chat.model.providers');

  const activeModel = models.find((m) => m.provider_id === effectiveProviderId && m.id === selectedModelId);
  const effortLevels = useMemo(() => resolveEffortLevels(activeModel), [activeModel]);
  const effortSupported = effortLevels.length >= 2;
  const currentEffortIdx = useMemo(
    () => clampEffortIndex(effortLevels, thinkingEffort),
    [effortLevels, thinkingEffort],
  );

  const effortMeta = effortLevels[currentEffortIdx] || effortLevels[0];
  const effortDisplayLabel = formatEffortLabel(effortMeta, locale);
  const sliderProgress =
    effortLevels.length > 1 ? (currentEffortIdx / (effortLevels.length - 1)) * 100 : 0;
  const sliderFilled = effortSupported && Boolean(effortMeta && effortMeta.value !== '');
  const isMaxEffort = sliderFilled && currentEffortIdx === effortLevels.length - 1;
  const isHighspeed = isGlmHighspeedModel(activeModel?.display_name || activeModel?.id || '');

  // Bind Ctrl+T (^T) globally to cycle thinking effort level smoothly (ZCode parity)
  useEffect(() => {
    if (!effortSupported) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey && e.key.toLowerCase() === 't') {
        e.preventDefault();
        const nextIdx = (currentEffortIdx + 1) % effortLevels.length;
        const nextOpt = effortLevels[nextIdx];
        if (nextOpt) setThinkingEffort(nextOpt.value);
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [effortSupported, currentEffortIdx, effortLevels, setThinkingEffort]);

  const allVisibleModels = useMemo(
    () => models.filter((m) => !hiddenModelKeys[`${m.provider_id}::${m.id}`]),
    [models, hiddenModelKeys],
  );
  const providerModels = useMemo(() => {
    const base = !effectiveProviderId
      ? allVisibleModels
      : allVisibleModels.filter((m) => m.provider_id === effectiveProviderId);
    const customOrder = effectiveProviderId ? modelOrder[effectiveProviderId] : undefined;
    if (!customOrder || customOrder.length === 0) return base;
    const rank = new Map(customOrder.map((id, i) => [id, i]));
    return [...base].sort((a, b) => {
      const ra = rank.has(a.id) ? rank.get(a.id)! : Number.MAX_SAFE_INTEGER;
      const rb = rank.has(b.id) ? rank.get(b.id)! : Number.MAX_SAFE_INTEGER;
      return ra - rb;
    });
  }, [allVisibleModels, effectiveProviderId, modelOrder]);

  const modelLabel =
    activeModel?.display_name || activeModel?.id || selectedModelId || formatMsg(locale, 'chat.model.selectModel');

  // Deduplicated known workspaces (runner defaults + recent chosen folders + session folders)
  const knownProjects = useMemo(() => {
    const items: Array<{ key: string; runnerId: string; runnerName: string; path: string; folderName: string; isDefault?: boolean }> = [];
    const seen = new Set<string>();
    const runnerMap = new Map(runners.map((r) => [r.id, r]));

    const add = (runnerId: string, path: string, isDefault = false) => {
      const cleanPath = (path || '').trim();
      if (!runnerId || !cleanPath) return;
      const key = `${runnerId}::${cleanPath}`;
      if (seen.has(key)) return;
      seen.add(key);
      const r = runnerMap.get(runnerId);
      items.push({
        key,
        runnerId,
        runnerName: r?.name || runnerId,
        path: cleanPath,
        folderName: cleanPath.split(/[\\/]/).filter(Boolean).pop() || cleanPath,
        isDefault,
      });
    };

    for (const rw of recentWorkspaces) {
      add(rw.runnerId, rw.path);
    }
    for (const r of runners) {
      if (r.workspace_default) add(r.id, r.workspace_default, true);
    }
    for (const sess of sessions) {
      if (sess.runner_id && sess.workspace_path) add(sess.runner_id, sess.workspace_path);
    }
    return items;
  }, [runners, recentWorkspaces, sessions]);

  const filteredProjects = useMemo(() => {
    const q = projectSearch.trim().toLowerCase();
    if (!q) return knownProjects;
    return knownProjects.filter(
      (p) =>
        p.folderName.toLowerCase().includes(q) ||
        p.path.toLowerCase().includes(q) ||
        p.runnerName.toLowerCase().includes(q),
    );
  }, [knownProjects, projectSearch]);

  const activeRunner = runners.find((r) => r.id === selectedRunnerId);
  const projectLabel =
    (selectedWorkspacePath.split(/[\\/]/).filter(Boolean).pop() || '') ||
    activeRunner?.name ||
    (locale === 'en-US' ? 'Select project' : '选择项目');

  const templates = locale === 'en-US' ? PROMPT_TEMPLATES_EN : PROMPT_TEMPLATES_ZH;
  const isRunning = stream.running;
  const canSend = (Boolean(composerText.trim()) || composerAttachments.length > 0) && Boolean(selectedRunnerId);

  // Detect active `@` file mention or `/` slash command autocomplete trigger
  const acTrigger = useMemo(() => {
    if (acDismissed) return null;
    return detectAutocomplete(composerText, cursorPos);
  }, [composerText, cursorPos, acDismissed]);

  // Build available `/` slash commands (built-ins + enabled plugins + enabled skills)
  const slashCommands = useMemo<SlashCmdEntry[]>(() => {
    const list: SlashCmdEntry[] = [
      {
        command: '/goal',
        insertText: '/goal ',
        badge: locale === 'en-US' ? 'Built-in' : '内置',
        description:
          locale === 'en-US'
            ? 'Goal-driven thorough execution loop until deliverables are verified'
            : '启动目标导向彻底执行闭环，直到交付物验证通过',
      },
      {
        command: '/compact',
        insertText: '/compact ',
        badge: locale === 'en-US' ? 'Built-in' : '内置',
        description:
          locale === 'en-US'
            ? 'Compact conversation context (optional focus instructions)'
            : '压缩当前会话上下文（可附带重点保留指令）',
      },
    ];

    const seen = new Set(list.map((c) => c.command));
    for (const pl of plugins) {
      if (!pl.enabled || !pl.source_ts) continue;
      const re = /registerCommand\(\s*["']([^"']+)["'](?:\s*,\s*\{[\s\S]*?description\s*:\s*["']([^"']*)["'])?/g;
      let m: RegExpExecArray | null;
      while ((m = re.exec(pl.source_ts)) !== null) {
        const name = '/' + m[1].replace(/^\/+/, '');
        if (seen.has(name)) continue;
        seen.add(name);
        list.push({
          command: name,
          insertText: `${name} `,
          badge: pl.name || 'Plugin',
          description: m[2] || (locale === 'en-US' ? `Plugin command from ${pl.name}` : `来自插件 ${pl.name} 的命令`),
        });
      }
    }

    for (const sk of skills) {
      if (!sk.enabled || !sk.name) continue;
      const cmd = `/${sk.name}`;
      if (seen.has(cmd)) continue;
      seen.add(cmd);
      list.push({
        command: cmd,
        insertText: locale === 'en-US' ? `Use skill "${sk.name}": ` : `使用技能「${sk.name}」：`,
        badge: 'Skill',
        description: sk.description || sk.name,
      });
    }
    return list;
  }, [plugins, skills, locale]);

  const filteredSlashCommands = useMemo(() => {
    if (acTrigger?.kind !== 'slash') return [];
    const q = acTrigger.query.toLowerCase();
    return slashCommands
      .filter((c) => !q || c.command.toLowerCase().includes(q) || c.description.toLowerCase().includes(q))
      .slice(0, 12);
  }, [acTrigger, slashCommands]);

  // Debounced workspace file search when `@` trigger is active
  useEffect(() => {
    if (acTrigger?.kind !== 'mention') {
      setMentionFiles([]);
      setMentionLoading(false);
      return;
    }
    let cancelled = false;
    setMentionLoading(true);
    const timer = window.setTimeout(() => {
      void searchWorkspaceFiles(acTrigger.query)
        .then((files) => {
          if (!cancelled) {
            setMentionFiles((files || []).slice(0, 12));
            setMentionLoading(false);
          }
        })
        .catch(() => {
          if (!cancelled) {
            setMentionFiles([]);
            setMentionLoading(false);
          }
        });
    }, 100);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [acTrigger?.kind, acTrigger?.query, searchWorkspaceFiles]);

  useEffect(() => {
    setActiveAcIdx(0);
  }, [acTrigger?.kind, acTrigger?.query, mentionFiles.length, filteredSlashCommands.length]);

  const applyAutocompleteChoice = (insertValue: string) => {
    if (!acTrigger) return;
    const nextText =
      composerText.slice(0, acTrigger.replaceStart) + insertValue + composerText.slice(acTrigger.replaceEnd);
    const nextCursor = acTrigger.replaceStart + insertValue.length;
    setComposerText(nextText);
    setCursorPos(nextCursor);
    setAcDismissed(false);
    window.requestAnimationFrame(() => {
      const el = textareaRef.current;
      if (el) {
        el.focus();
        el.setSelectionRange(nextCursor, nextCursor);
      }
    });
  };

  const handleAttachFiles = async (fileList: FileList | File[]) => {
    const arr = Array.from(fileList);
    for (const file of arr) {
      if (!file.type.startsWith('image/')) continue;
      const att = await readImageFileAsAttachment(file);
      if (att) addComposerAttachment(att);
    }
  };

  const acItemCount = acTrigger?.kind === 'slash' ? filteredSlashCommands.length : acTrigger?.kind === 'mention' ? mentionFiles.length : 0;
  const showAcPopover = Boolean(acTrigger) && (acItemCount > 0 || (acTrigger?.kind === 'mention' && mentionLoading));

  const selectEffortByIndex = (idx: number) => {
    const opt = effortLevels[idx];
    if (opt && opt.value !== thinkingEffort) {
      setThinkingEffort(opt.value);
    }
  };

  const providerListContent = (
    <>
      <div className="max-h-64 overflow-y-auto p-1">
        {providers.length === 0 ? (
          <div className="px-2.5 py-3 text-ui-sm text-foreground-subtlest">
            {locale === 'en-US' ? 'No providers configured' : '暂无已配置供应商'}
          </div>
        ) : (
          providers.map((p) => {
            const isSelected = p.id === effectiveProviderId;
            const count = allVisibleModels.filter((m) => m.provider_id === p.id).length;
            return (
              <button
                key={p.id}
                type="button"
                data-testid={`provider-option-${p.id}`}
                onClick={() => {
                  setSelectedProvider(p.id);
                  setProviderMenuOpen(false);
                }}
                className={`flex min-h-8 w-full items-center justify-between gap-2 rounded-lg px-2.5 py-1.5 text-left transition-colors hover:bg-menu-hover ${
                  isSelected ? 'bg-selected' : ''
                }`}
              >
                <span className="flex min-w-0 items-center gap-2">
                  <Cpu className="size-3.5 shrink-0 text-foreground-subtle" />
                  <span className="truncate text-ui-base text-foreground">{p.name || p.id}</span>
                  <span className="shrink-0 rounded-full bg-surface px-1.5 py-px text-[10px] font-medium uppercase tracking-wide text-foreground-subtle">
                    API
                  </span>
                </span>
                <span className="flex shrink-0 items-center gap-1.5">
                  <span className="font-mono text-ui-xs tabular-nums text-foreground-subtlest">{count}</span>
                  {isSelected && <Check className="size-3.5 text-foreground-subtle" />}
                </span>
              </button>
            );
          })
        )}
      </div>
      <div className="border-t border-border p-1">
        <button
          type="button"
          data-testid="manage-providers-btn"
          onClick={() => {
            setProviderMenuOpen(false);
            setOpenMenu(null);
            useNexStore.getState().openSettings('providers');
          }}
          className="flex h-8 w-full items-center gap-2 rounded-lg px-2.5 text-left text-ui-sm text-foreground-subtle transition-colors hover:bg-menu-hover hover:text-foreground"
        >
          <Settings className="size-3.5 shrink-0" />
          <span>{formatMsg(locale, 'chat.model.manageProviders')}</span>
        </button>
      </div>
    </>
  );

  const autocompletePopover = showAcPopover && acTrigger ? (
    <div
      data-testid={acTrigger.kind === 'slash' ? 'composer-slash-menu' : 'composer-mention-menu'}
      className="zcode-popover-up absolute bottom-full left-0 right-0 z-50 mb-2 max-h-64 overflow-y-auto rounded-xl border border-border bg-popover p-1.5 shadow-xl"
    >
      <div className="flex items-center justify-between px-2 py-1 text-[11px] font-medium text-foreground-subtlest">
        <span>
          {acTrigger.kind === 'slash'
            ? locale === 'en-US'
              ? 'Commands & Skills'
              : '命令与技能'
            : locale === 'en-US'
              ? 'Workspace Files'
              : '工作区文件'}
        </span>
        <span className="font-mono text-[10px]">↑↓ · Tab/Enter · Esc</span>
      </div>
      {acTrigger.kind === 'slash' ? (
        filteredSlashCommands.map((cmd, idx) => (
          <button
            key={cmd.command}
            type="button"
            data-testid={`slash-option-${cmd.command.slice(1)}`}
            onMouseDown={(e) => {
              e.preventDefault();
              applyAutocompleteChoice(cmd.insertText);
            }}
            onMouseEnter={() => setActiveAcIdx(idx)}
            className={`flex w-full items-center gap-2 rounded-lg px-2.5 py-1.5 text-left transition-colors ${
              idx === activeAcIdx ? 'bg-menu-hover text-foreground' : 'text-foreground hover:bg-menu-hover/60'
            }`}
          >
            <span className="shrink-0 font-mono text-ui-sm font-semibold text-brand-blue">{cmd.command}</span>
            <span className="min-w-0 flex-1 truncate text-ui-xs text-foreground-subtle">{cmd.description}</span>
            <span className="shrink-0 rounded bg-surface px-1.5 py-0.5 text-[10px] text-foreground-subtlest">
              {cmd.badge}
            </span>
          </button>
        ))
      ) : mentionLoading && mentionFiles.length === 0 ? (
        <div className="flex items-center gap-2 px-2.5 py-2 text-ui-xs text-foreground-subtlest">
          <LoaderCircle className="size-3.5 animate-spin" />
          <span>{locale === 'en-US' ? 'Searching files…' : '正在搜索文件…'}</span>
        </div>
      ) : (
        mentionFiles.map((relPath, idx) => {
          const fname = baseName(relPath);
          const dir = relPath.includes('/') ? relPath.slice(0, relPath.lastIndexOf('/')) : '';
          return (
            <button
              key={relPath}
              type="button"
              data-testid={`mention-option-${idx}`}
              onMouseDown={(e) => {
                e.preventDefault();
                applyAutocompleteChoice(`@${relPath} `);
              }}
              onMouseEnter={() => setActiveAcIdx(idx)}
              className={`flex w-full items-center gap-2 rounded-lg px-2.5 py-1.5 text-left transition-colors ${
                idx === activeAcIdx ? 'bg-menu-hover text-foreground' : 'text-foreground hover:bg-menu-hover/60'
              }`}
            >
              <FileText className="size-3.5 shrink-0 text-foreground-subtlest" />
              <span className="shrink-0 font-mono text-ui-sm font-medium">{fname}</span>
              {dir && <span className="min-w-0 flex-1 truncate font-mono text-ui-xs text-foreground-subtlest">{dir}</span>}
            </button>
          );
        })
      )}
    </div>
  ) : null;

  const attachmentStrip = composerAttachments.length > 0 ? (
    <div data-testid="composer-attachments" className="mb-2 flex flex-wrap gap-2">
      {composerAttachments.map((att) => (
        <div
          key={att.id}
          className="group relative flex items-center gap-1.5 rounded-xl border border-border bg-surface/80 p-1 pr-2"
        >
          <img
            src={att.previewUrl}
            alt={att.name}
            className="size-10 rounded-lg object-cover"
          />
          <div className="min-w-0 max-w-[120px]">
            <div className="truncate text-ui-xs font-medium text-foreground">{att.name}</div>
            <div className="text-[10px] text-foreground-subtlest">{Math.max(1, Math.round(att.size / 1024))} KB</div>
          </div>
          <button
            type="button"
            aria-label={locale === 'en-US' ? 'Remove image' : '移除图片'}
            onClick={() => removeComposerAttachment(att.id)}
            className="absolute -right-1.5 -top-1.5 flex size-4 items-center justify-center rounded-full border border-border bg-popover text-foreground-subtle shadow-xs transition-colors hover:bg-destructive hover:text-white"
          >
            <X className="size-2.5" />
          </button>
        </div>
      ))}
    </div>
  ) : null;

  const textarea = (
    <>
      <input
        ref={fileInputRef}
        type="file"
        accept="image/png,image/jpeg,image/webp,image/gif"
        multiple
        className="sr-only"
        onChange={(e) => {
          if (e.target.files) {
            void handleAttachFiles(e.target.files);
            e.target.value = '';
          }
        }}
      />
      {attachmentStrip}
      <textarea
        ref={textareaRef}
        data-testid="composer-input"
        rows={isDraft ? 2 : 1}
        value={composerText}
        onChange={(e) => {
          setComposerText(e.target.value);
          setCursorPos(e.target.selectionStart ?? e.target.value.length);
          setAcDismissed(false);
        }}
        onClick={(e) => setCursorPos(e.currentTarget.selectionStart ?? composerText.length)}
        onKeyUp={(e) => setCursorPos(e.currentTarget.selectionStart ?? composerText.length)}
        onPaste={(e) => {
          const items = e.clipboardData?.items;
          if (!items) return;
          const imgFiles: File[] = [];
          for (let i = 0; i < items.length; i++) {
            const it = items[i];
            if (it.kind === 'file' && it.type.startsWith('image/')) {
              const f = it.getAsFile();
              if (f) imgFiles.push(f);
            }
          }
          if (imgFiles.length > 0) {
            e.preventDefault();
            void handleAttachFiles(imgFiles);
          }
        }}
        onKeyDown={(e) => {
          if (showAcPopover && acTrigger && acItemCount > 0) {
            if (e.key === 'ArrowDown') {
              e.preventDefault();
              setActiveAcIdx((i) => (i + 1) % acItemCount);
              return;
            }
            if (e.key === 'ArrowUp') {
              e.preventDefault();
              setActiveAcIdx((i) => (i - 1 + acItemCount) % acItemCount);
              return;
            }
            if (e.key === 'Escape') {
              e.preventDefault();
              setAcDismissed(true);
              return;
            }
            if ((e.key === 'Enter' || e.key === 'Tab') && !e.shiftKey && !e.nativeEvent.isComposing) {
              e.preventDefault();
              if (acTrigger.kind === 'slash') {
                const chosen = filteredSlashCommands[activeAcIdx];
                if (chosen) applyAutocompleteChoice(chosen.insertText);
              } else {
                const chosenFile = mentionFiles[activeAcIdx];
                if (chosenFile) applyAutocompleteChoice(`@${chosenFile} `);
              }
              return;
            }
          }
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
              ? 'Ask for follow-up changes (@ file, / command, paste image)'
              : '继续追问或提出修改建议（@ 引用文件，/ 命令，支持粘贴图片）'
        }
        className={`${isDraft ? 'min-h-[58px]' : ''} w-full resize-none bg-transparent text-ui-base leading-relaxed text-foreground placeholder-foreground-subtlest`}
      />
    </>
  );

  const toolbar = (
    <div className="mt-1.5 flex min-w-0 items-center justify-between gap-1.5">
      {/* Left: add context + permission mode */}
      <div className="relative flex min-w-0 shrink items-center gap-0.5">
        <button
          type="button"
          onClick={() => setOpenMenu((m) => (m === 'plus' ? null : 'plus'))}
          className="flex size-7 shrink-0 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
          title={locale === 'en-US' ? 'Add prompt or context' : '添加提示或上下文'}
        >
          <Plus className="size-4" />
        </button>

        {openMenu === 'plus' && (
          <div className="zcode-popover-up absolute bottom-9 left-0 z-40 w-72 rounded-xl border border-border bg-popover p-1.5 shadow-md">
            {templates.map((tpl) => (
              <button
                key={tpl}
                type="button"
                onClick={() => {
                  setComposerText(tpl);
                  setOpenMenu(null);
                }}
                className="flex w-full items-start gap-2 rounded-lg px-2 py-1.5 text-left text-ui-sm text-foreground transition-colors hover:bg-menu-hover"
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
                const next = `${composerText}@`;
                setComposerText(next);
                setCursorPos(next.length);
                setAcDismissed(false);
                setOpenMenu(null);
                window.requestAnimationFrame(() => textareaRef.current?.focus());
              }}
              className="flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left text-ui-sm text-foreground transition-colors hover:bg-menu-hover"
            >
              <AtSign className="size-3.5 shrink-0 text-foreground-subtle" />
              <span>{locale === 'en-US' ? 'Reference a workspace file' : '引用工作区文件 (@)'}</span>
            </button>
            <button
              type="button"
              data-testid="attach-image-btn"
              onClick={() => {
                setOpenMenu(null);
                fileInputRef.current?.click();
              }}
              className="flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left text-ui-sm text-foreground transition-colors hover:bg-menu-hover"
            >
              <ImagePlus className="size-3.5 shrink-0 text-foreground-subtle" />
              <span>{locale === 'en-US' ? 'Attach image (or paste)' : '上传图片（或直接粘贴）'}</span>
            </button>
          </div>
        )}

        <button
          type="button"
          data-testid="permission-mode-btn"
          onClick={() => setOpenMenu((m) => (m === 'perm' ? null : 'perm'))}
          title={formatMsg(locale, permissionMeta.labelKey)}
          className={`flex h-7 min-w-0 shrink items-center gap-1 rounded-lg px-2 text-ui-sm transition-colors hover:bg-hover ${
            permissionMode === 'full_access'
              ? 'text-amber-600 hover:text-amber-600 dark:text-amber-400 dark:hover:text-amber-400'
              : permissionMode === 'read_only'
                ? 'text-brand-blue hover:text-brand-blue'
                : 'text-foreground-subtle hover:text-foreground'
          }`}
        >
          <PermissionIcon className="size-3.5 shrink-0" />
          <RollingLabel text={formatMsg(locale, permissionMeta.labelKey)} className="min-w-0 max-w-[92px] font-medium" />
          <ChevronDown className="size-3.5 shrink-0 opacity-70" />
        </button>

        {openMenu === 'perm' && (
          <div className="zcode-popover-up absolute bottom-9 left-0 z-40 w-72 rounded-xl border border-border bg-popover p-1.5 shadow-md">
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
                  className={`flex w-full items-start gap-2 rounded-lg px-2 py-1.5 text-left transition-colors hover:bg-menu-hover ${
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
      <div className="flex min-w-0 shrink items-center gap-0.5">
        <ContextWindowsButton telemetry={stream.latestTelemetry} contextLimit={activeModelLimit} />

        <div className="min-w-0 shrink">
          <button
            type="button"
            data-testid="model-effort-btn"
            onClick={() => {
              setProviderMenuOpen(false);
              setOpenMenu((m) => (m === 'model' ? null : 'model'));
            }}
            title={effortSupported ? `${modelLabel} · ${effortDisplayLabel}` : modelLabel}
            className="flex h-7 w-full max-w-[240px] min-w-0 items-center gap-1.5 rounded-lg px-2 text-ui-sm text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
          >
            <span className="min-w-0 truncate">{modelLabel}</span>
            {effortSupported && (
              <RollingLabel
                text={effortDisplayLabel}
                className="shrink-0 text-foreground-subtlest"
              />
            )}
            <ChevronDown
              className={`size-3.5 shrink-0 opacity-70 transition-transform duration-150 ${
                openMenu === 'model' ? 'rotate-180' : ''
              }`}
            />
          </button>

          {openMenu === 'model' && (
            <div
              ref={modelPopoverRef}
              className="zcode-popover-up absolute bottom-12 right-2 z-50 w-80 max-w-[calc(100%-1rem)] rounded-2xl border border-border bg-popover shadow-xl"
            >
              {/* Provider Selector Trigger Row (click selects/switches provider via submenu — NEVER jumps straight to Settings!) */}
              <div className="relative">
                <button
                  type="button"
                  data-testid="composer-provider-trigger"
                  aria-expanded={providerMenuOpen}
                  onClick={() => {
                    if (!providerMenuOpen && modelPopoverRef.current) {
                      const rect = modelPopoverRef.current.getBoundingClientRect();
                      const container =
                        modelPopoverRef.current.closest('[data-testid="chat-stream"]') ||
                        modelPopoverRef.current.closest('section');
                      const containerLeft = container ? container.getBoundingClientRect().left : 0;
                      setProviderSubmenuSide(rect.left - containerLeft >= 272 ? 'side' : 'inline');
                    }
                    setProviderMenuOpen((v) => !v);
                  }}
                  className={`flex w-full items-center justify-between rounded-t-2xl border-b border-border px-3.5 py-2.5 text-left transition-colors hover:bg-menu-hover ${
                    providerMenuOpen ? 'bg-menu-hover/70' : ''
                  }`}
                >
                  <div className="flex min-w-0 items-center gap-2">
                    <Cpu className="size-4 shrink-0 text-foreground-subtle" />
                    <span className="truncate text-ui-base font-semibold text-foreground">
                      {activeProviderName}
                    </span>
                    <span className="shrink-0 rounded-full bg-surface px-1.5 py-px text-[10px] font-medium uppercase tracking-wide text-foreground-subtle">
                      API
                    </span>
                  </div>
                  <ChevronRight
                    className={`size-4 shrink-0 text-foreground-subtlest transition-transform duration-150 ${
                      providerMenuOpen && providerSubmenuSide === 'inline' ? 'rotate-90' : ''
                    }`}
                  />
                </button>

                {/* Side Submenu when there is ample space to the left of the model popover */}
                {providerMenuOpen && providerSubmenuSide === 'side' && (
                  <div
                    data-testid="composer-provider-submenu"
                    className="zcode-submenu-left absolute right-full top-0 z-50 mr-1.5 w-64 overflow-hidden rounded-xl border border-border bg-popover shadow-xl"
                  >
                    <div className="border-b border-border px-3 py-2 text-ui-xs font-medium text-foreground-subtlest">
                      {formatMsg(locale, 'chat.model.providers')}
                    </div>
                    {providerListContent}
                  </div>
                )}
              </div>

              {/* Inline Drill-down when viewport / center column is narrow */}
              {providerMenuOpen && providerSubmenuSide === 'inline' && (
                <div data-testid="composer-provider-submenu" className="border-b border-border">
                  <button
                    type="button"
                    onClick={() => setProviderMenuOpen(false)}
                    className="flex h-8 w-full items-center gap-1.5 border-b border-border px-3 text-ui-sm text-foreground-subtle hover:bg-menu-hover"
                  >
                    <ChevronLeft className="size-3.5" />
                    <span>{locale === 'en-US' ? 'Back' : '返回'}</span>
                  </button>
                  {providerListContent}
                </div>
              )}

              {/* Models List (scoped to selected provider) */}
              <div className={providerMenuOpen && providerSubmenuSide === 'inline' ? 'hidden' : 'block'}>
                <div className="px-3 pt-2.5 pb-1 text-ui-xs font-medium text-foreground-subtlest">
                  {formatMsg(locale, 'chat.model.models')}
                </div>
                <div className="max-h-56 overflow-y-auto px-1.5 pb-2">
                  {providerModels.length === 0 ? (
                    <div className="space-y-1.5 px-2.5 py-3 text-ui-sm text-foreground-subtlest">
                      <div>{formatMsg(locale, 'chat.model.noModels')}</div>
                      <button
                        type="button"
                        onClick={() => {
                          setOpenMenu(null);
                          useNexStore.getState().openSettings('providers');
                        }}
                        className="text-ui-xs font-medium text-brand-blue hover:underline"
                      >
                        {formatMsg(locale, 'chat.model.manageProviders')} →
                      </button>
                    </div>
                  ) : (
                    providerModels.map((m) => {
                      const active = m.provider_id === selectedProviderId && m.id === selectedModelId;
                      return (
                        <button
                          key={`${m.provider_id}::${m.id}`}
                          type="button"
                          data-testid={`model-option-${m.id}`}
                          onClick={() => {
                            setSelectedModel(m.provider_id, m.id);
                            setOpenMenu(null);
                          }}
                          className={`flex w-full items-center justify-between gap-2 rounded-lg px-2.5 py-1.5 text-left transition-colors hover:bg-menu-hover ${
                            active ? 'bg-selected' : ''
                          }`}
                        >
                          <span className="flex min-w-0 items-center gap-1.5">
                            <span className="truncate text-ui-base text-foreground">{m.display_name || m.id}</span>
                            {m.context_window > 0 && (
                              <span className="shrink-0 rounded bg-secondary/70 px-1.5 py-0.5 font-mono text-[10px] text-foreground-subtle">
                                {formatContextLabel(m.context_window)}
                              </span>
                            )}
                            {m.supports_vision && (
                              <span className="shrink-0 rounded-full border border-border px-1.5 py-px text-[10px] text-foreground-subtle">
                                Vision
                              </span>
                            )}
                          </span>
                          {active && <Check className="size-4 shrink-0 text-foreground-subtle" />}
                        </button>
                      );
                    })
                  )}
                </div>

                {/* ZCode Effort Slider (strictly ordered per-model levels; hidden/disabled for non-reasoning models) */}
                {effortSupported ? (
                  <div className="border-t border-border px-3.5 py-3">
                    <div className="mb-2 flex items-center justify-between">
                      <div className="flex items-center gap-2">
                        <span className="text-ui-xs text-foreground-subtlest">
                          {formatMsg(locale, 'chat.toolbar.thoughtLevel')}
                        </span>
                        <RollingLabel
                          text={effortDisplayLabel}
                          className="text-ui-sm font-medium text-foreground"
                        />
                      </div>
                      <kbd className="rounded bg-secondary px-1.5 py-0.5 font-mono text-[10px] text-foreground-subtle">
                        ^ T
                      </kbd>
                    </div>

                    <div className="composer-thought-slider relative h-8 w-full rounded-full">
                      <div aria-hidden="true" className="pointer-events-none absolute inset-0">
                        {/* Track + Dynamic Fill */}
                        <div
                          data-testid="composer-thought-track"
                          data-progress={sliderFilled ? sliderProgress : 0}
                          className="absolute inset-x-0 top-1 h-6 overflow-hidden rounded-full bg-foreground/10"
                        >
                          <div className="absolute inset-0 rounded-full border border-foreground/10" />
                          <div
                            className="composer-thought-slider-fill absolute inset-y-0 left-0 overflow-hidden rounded-full"
                            data-max={isMaxEffort}
                            data-highspeed={isHighspeed}
                            style={{
                              width: sliderFilled
                                ? `calc(14px + (100% - 28px) * ${sliderProgress / 100})`
                                : 0,
                            }}
                          >
                            {isMaxEffort && (
                              <div
                                data-testid="composer-thought-bubbles"
                                className="composer-thought-bubbles absolute inset-0"
                              >
                                {(isHighspeed ? HIGHSPEED_BUBBLES : EFFORT_BUBBLES).map((bubble, idx) => (
                                  <span
                                    key={idx}
                                    className="composer-thought-bubble absolute inset-x-0"
                                    style={{
                                      top: bubble.top,
                                      animationDuration: `${bubble.duration}s`,
                                      animationDelay: `${bubble.delay}s`,
                                    }}
                                  >
                                    <span
                                      className="block rounded-full bg-white"
                                      style={{ width: bubble.size, height: bubble.size }}
                                    />
                                  </span>
                                ))}
                              </div>
                            )}
                          </div>
                        </div>

                        {/* Discrete Stop Dots */}
                        <div className="absolute inset-x-3.5 inset-y-0 flex items-center justify-between">
                          {effortLevels.map((lvl, idx) => (
                            <span
                              key={lvl.value || 'off'}
                              data-composer-thought-stop
                              data-filled={sliderFilled && idx <= currentEffortIdx}
                              data-hovered={hoveredStop === (lvl.value || 'off')}
                              className="composer-thought-slider-stop size-1 shrink-0 rounded-full"
                            />
                          ))}
                        </div>

                        {/* White Circular Thumb */}
                        <div
                          className="composer-thought-slider-thumb absolute top-0.5 size-7 rounded-full shadow-sm"
                          style={{ left: `calc((100% - 28px) * ${sliderProgress / 100})` }}
                        />
                      </div>

                      <input
                        type="range"
                        min={0}
                        max={Math.max(0, effortLevels.length - 1)}
                        step={1}
                        value={currentEffortIdx}
                        aria-label={formatMsg(locale, 'chat.toolbar.thoughtLevel')}
                        aria-valuetext={effortDisplayLabel}
                        data-testid="composer-thought-slider"
                        className="composer-thought-range absolute inset-0 m-0 h-8 w-full cursor-pointer appearance-none bg-transparent outline-none"
                        onPointerLeave={() => setHoveredStop(null)}
                        onPointerMove={(event) => {
                          const rect = event.currentTarget.getBoundingClientRect();
                          const trackWidth = Math.max(1, rect.width - 28);
                          const maxIdx = Math.max(1, effortLevels.length - 1);
                          const idx = Math.max(
                            0,
                            Math.min(maxIdx, Math.round(((event.clientX - rect.left - 14) / trackWidth) * maxIdx)),
                          );
                          const level = effortLevels[idx];
                          const center = rect.left + 14 + (trackWidth * idx) / maxIdx;
                          setHoveredStop(
                            event.pointerType !== 'touch' && level && Math.abs(event.clientX - center) <= 12
                              ? level.value || 'off'
                              : null,
                          );
                        }}
                        onChange={(e) => selectEffortByIndex(Number(e.target.value))}
                      />
                    </div>
                  </div>
                ) : (
                  <div
                    data-testid="composer-thought-unsupported"
                    className="border-t border-border px-3.5 py-2.5 text-ui-xs text-foreground-subtlest"
                  >
                    {formatMsg(locale, 'chat.thought.unsupported')}
                  </div>
                )}
              </div>
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
            className="ml-0.5 flex size-7 shrink-0 items-center justify-center rounded-full bg-foreground text-background transition-transform hover:opacity-90 active:scale-95"
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
            className="ml-0.5 flex size-7 shrink-0 items-center justify-center rounded-full bg-foreground text-background transition-all hover:bg-foreground/90 active:scale-95 disabled:bg-foreground/20 disabled:text-background/70"
          >
            <ArrowUp className="size-4" />
          </button>
        )}
      </div>
    </div>
  );

  const dropProps = {
    onDragOver: (e: React.DragEvent) => {
      if (Array.from(e.dataTransfer.types).includes('Files')) {
        e.preventDefault();
        setIsDraggingImage(true);
      }
    },
    onDragLeave: () => setIsDraggingImage(false),
    onDrop: (e: React.DragEvent) => {
      setIsDraggingImage(false);
      if (e.dataTransfer.files && e.dataTransfer.files.length > 0) {
        e.preventDefault();
        void handleAttachFiles(e.dataTransfer.files);
      }
    },
  };

  const card = (
    <div
      {...dropProps}
      className={`relative rounded-2xl border bg-input px-4 pb-2.5 pt-3 transition-colors focus-within:border-border-hover composer-shadow ${
        isDraggingImage ? 'border-brand-blue ring-2 ring-brand-blue/25' : 'border-border'
      }`}
    >
      {autocompletePopover}
      {textarea}
      {toolbar}
    </div>
  );

  if (isDraft) {
    return (
      <div ref={rootRef} className="zcode-bottom-dock relative z-30 mx-auto w-full max-w-4xl">
        <div className="w-full rounded-2xl bg-black/[0.035] p-1.5 dark:bg-white/[0.04]">
          <div className="relative flex items-center px-2.5 py-1.5">
            <button
              type="button"
              data-testid="project-select-btn"
              onClick={() => setOpenMenu((m) => (m === 'runner' ? null : 'runner'))}
              title={selectedWorkspacePath || projectLabel}
              className="flex min-w-0 items-center gap-1.5 rounded-full px-2 py-0.5 text-ui-sm text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
            >
              <Folder className="size-3.5 shrink-0 text-foreground-subtle" />
              <span className="max-w-[260px] truncate">{projectLabel}</span>
              <ChevronDown
                className={`size-3.5 shrink-0 transition-transform duration-150 ${
                  openMenu === 'runner' ? 'rotate-180' : ''
                }`}
              />
            </button>

            {openMenu === 'runner' && (
              <div
                data-testid="composer-project-menu"
                className="zcode-popover-up absolute bottom-9 left-1.5 z-50 flex w-80 max-w-[calc(100vw-2rem)] flex-col overflow-hidden rounded-xl border border-border bg-popover shadow-lg"
              >
                {/* Search Projects */}
                <div className="flex items-center gap-2 border-b border-border px-3 py-2">
                  <Search className="size-3.5 shrink-0 text-foreground-subtlest" />
                  <input
                    value={projectSearch}
                    onChange={(e) => setProjectSearch(e.target.value)}
                    placeholder={formatMsg(locale, 'dirBrowser.searchProjects')}
                    className="min-w-0 flex-1 bg-transparent text-ui-sm text-foreground placeholder-foreground-subtlest"
                  />
                  {projectSearch && (
                    <button
                      type="button"
                      onClick={() => setProjectSearch('')}
                      className="text-foreground-subtlest hover:text-foreground"
                    >
                      <X className="size-3.5" />
                    </button>
                  )}
                </div>

                {/* Known & Recent Projects */}
                <div className="max-h-56 overflow-y-auto p-1">
                  {runners.length === 0 ? (
                    <div className="px-2 py-3 text-center text-ui-sm text-foreground-subtlest">
                      {formatMsg(locale, 'dirBrowser.noRunner')}
                    </div>
                  ) : filteredProjects.length === 0 ? (
                    <div className="px-2 py-3 text-center text-ui-sm text-foreground-subtlest">
                      {locale === 'en-US' ? 'No matching projects' : '未找到匹配的项目'}
                    </div>
                  ) : (
                    filteredProjects.map((proj) => {
                      const active =
                        proj.runnerId === selectedRunnerId && proj.path === selectedWorkspacePath;
                      return (
                        <button
                          key={proj.key}
                          type="button"
                          onClick={() => {
                            setSelectedRunner(proj.runnerId, proj.path);
                            setProjectSearch('');
                            setOpenMenu(null);
                          }}
                          className={`flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left text-ui-sm transition-colors hover:bg-menu-hover ${
                            active ? 'bg-selected' : ''
                          }`}
                        >
                          <Folder className="size-3.5 shrink-0 text-foreground-subtle" />
                          <span className="min-w-0 flex-1">
                            <span className="flex items-baseline gap-1.5">
                              <span className="truncate font-medium text-foreground">{proj.folderName}</span>
                              <span className="shrink-0 text-ui-xs text-foreground-subtlest">
                                {proj.runnerName}
                              </span>
                            </span>
                            <span className="block truncate font-mono text-ui-xs text-foreground-subtlest">
                              {proj.path}
                            </span>
                          </span>
                          {active && <Check className="size-3.5 shrink-0 text-success" />}
                        </button>
                      );
                    })
                  )}
                </div>

                {/* Browse & Choose Folder Action (replaces manual path input!) */}
                <div className="border-t border-border p-1">
                  <button
                    type="button"
                    data-testid="choose-folder-btn"
                    disabled={runners.length === 0}
                    onClick={() => {
                      setOpenMenu(null);
                      setDirBrowserOpen(true);
                    }}
                    className="flex h-8 w-full items-center gap-2 rounded-lg px-2.5 text-left text-ui-sm font-medium text-foreground transition-colors hover:bg-menu-hover disabled:opacity-40"
                  >
                    <FolderPlus className="size-4 shrink-0 text-foreground-subtle" />
                    <span>{formatMsg(locale, 'dirBrowser.chooseFolder')}</span>
                  </button>
                </div>
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
            <div
              {...dropProps}
              className={`relative rounded-2xl border bg-input px-4 py-3 transition-colors focus-within:border-border-hover composer-shadow ${
                isDraggingImage ? 'border-brand-blue ring-2 ring-brand-blue/25' : 'border-border'
              }`}
            >
              {autocompletePopover}
              {textarea}
              {toolbar}
            </div>
          </div>
        </div>

        <DirectoryBrowserModal
          open={dirBrowserOpen}
          initialRunnerId={selectedRunnerId || runners[0]?.id}
          initialPath={selectedWorkspacePath}
          onSelect={(rid, pickedPath) => {
            setSelectedRunner(rid, pickedPath);
            setDirBrowserOpen(false);
          }}
          onClose={() => setDirBrowserOpen(false)}
        />
      </div>
    );
  }

  return (
    <div ref={rootRef} className="zcode-bottom-dock mx-auto w-full max-w-4xl">
      {card}
    </div>
  );
}

const WATERMARK_PATHS = ['M22 24h88L74 62H8z', 'M120 24h64l-44 96H82z', 'M120 24l44 96'];
const EMPTY_STREAM = emptyStreamState();

const MemoTurnItem = memo(function MemoTurnItemImpl({
  item,
  locale,
  registerRef,
}: {
  item: ChatTurnItem;
  locale: 'zh-CN' | 'en-US';
  registerRef: (id: string, el: HTMLDivElement | null) => void;
}) {
  const [manualOpen, setManualOpen] = useState<boolean | null>(null);
  const [copiedReply, setCopiedReply] = useState(false);
  const activeSessionId = useNexStore((s) => s.activeSessionId);
  const forkSession = useNexStore((s) => s.forkSession);
  const workExpanded = manualOpen ?? Boolean(item.streaming);

  return (
    <div
      ref={(el) => registerRef(item.id, el)}
      style={{ contain: 'layout style' }}
      className={`flex flex-col ${item.role === 'user' ? 'items-end' : 'items-start'}`}
    >
      {item.role === 'user' ? (
        <UserBubble text={item.text} images={item.images} seq={item.seq} />
      ) : (
        <div className={`group/assistant min-w-0 w-full space-y-2.5 ${item.streaming ? 'zcode-stream-in' : ''}`}>
          {(item.thinking || item.toolCalls.length > 0) && (
            <WorkGroup
              item={item}
              locale={locale}
              open={workExpanded}
              onToggle={() => setManualOpen((prev) => !(prev ?? Boolean(item.streaming)))}
            />
          )}

          {item.text && <MarkdownRenderer content={item.text} streaming={Boolean(item.streaming)} />}

          <ChangeSummaryCard item={item} />

          {!item.streaming && (item.text || item.toolCalls.length > 0) && (
            <div className="flex items-center gap-1 pt-0.5 opacity-0 transition-opacity group-hover/assistant:opacity-100">
              {item.text && (
                <button
                  type="button"
                  onClick={() => {
                    void navigator.clipboard?.writeText(item.text);
                    setCopiedReply(true);
                    window.setTimeout(() => setCopiedReply(false), 1400);
                  }}
                  className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-ui-xs text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
                >
                  {copiedReply ? <Check className="zcode-reaction-pop size-3 text-success" /> : <Copy className="size-3" />}
                  <span>{copiedReply ? (locale === 'en-US' ? 'Copied' : '已复制') : locale === 'en-US' ? 'Copy' : '复制'}</span>
                </button>
              )}
              {activeSessionId && item.seq && item.seq > 0 && (
                <button
                  type="button"
                  data-testid={`fork-after-${item.seq}`}
                  onClick={() => void forkSession(activeSessionId, { upToSeq: item.seq })}
                  className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-ui-xs text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
                >
                  <GitBranch className="size-3" />
                  <span>{locale === 'en-US' ? 'Fork here' : '从此分支'}</span>
                </button>
              )}
            </div>
          )}

          {item.streaming && !item.thinking && item.toolCalls.length === 0 && !item.text && (
            <div className="zcode-stream-in flex items-center gap-1.5 text-ui-xs text-foreground-subtlest">
              <LoaderCircle className="size-3 animate-spin" />
              <span className="animated-gradient-text">{locale === 'en-US' ? 'Generating…' : '正在流式生成...'}</span>
            </div>
          )}
        </div>
      )}
    </div>
  );
});

export function ConversationPane() {
  const locale = useNexStore((s) => s.locale);
  const sidebarVisible = useNexStore((s) => s.sidebarVisible);
  const sidePaneVisible = useNexStore((s) => s.sidePaneVisible);
  const showBottomPanel = useNexStore((s) => s.showBottomPanel);
  const runners = useNexStore((s) => s.runners);
  const models = useNexStore((s) => s.models);
  const sessions = useNexStore((s) => s.sessions);
  const selectedRunnerId = useNexStore((s) => s.selectedRunnerId);
  const selectedWorkspacePath = useNexStore((s) => s.selectedWorkspacePath);
  const selectedProviderId = useNexStore((s) => s.selectedProviderId);
  const selectedModelId = useNexStore((s) => s.selectedModelId);
  const thinkingEffort = useNexStore((s) => s.thinkingEffort);
  const permissionMode = useNexStore((s) => s.permissionMode);
  const activeSessionId = useNexStore((s) => s.activeSessionId);
  const composerText = useNexStore((s) => s.composerText);
  const pinnedSessionIds = useNexStore((s) => s.pinnedSessionIds);
  const currentStream = useNexStore((s) => (s.activeSessionId ? s.streams[s.activeSessionId] || EMPTY_STREAM : EMPTY_STREAM));

  const toggleSidebar = useNexStore((s) => s.toggleSidebar);
  const toggleSidePane = useNexStore((s) => s.toggleSidePane);
  const toggleBottomPanel = useNexStore((s) => s.toggleBottomPanel);
  const setComposerText = useNexStore((s) => s.setComposerText);
  const setSelectedRunner = useNexStore((s) => s.setSelectedRunner);
  const setSelectedProvider = useNexStore((s) => s.setSelectedProvider);
  const setSelectedModel = useNexStore((s) => s.setSelectedModel);
  const setThinkingEffort = useNexStore((s) => s.setThinkingEffort);
  const setPermissionMode = useNexStore((s) => s.setPermissionMode);
  const sendPromptOrSteer = useNexStore((s) => s.sendPromptOrSteer);
  const abortActiveSession = useNexStore((s) => s.abortActiveSession);
  const togglePinSession = useNexStore((s) => s.togglePinSession);
  const renameSession = useNexStore((s) => s.renameSession);
  const archiveSession = useNexStore((s) => s.archiveSession);

  const [headerMenu, setHeaderMenu] = useState<'session' | 'help' | null>(null);
  const [renaming, setRenaming] = useState(false);
  const [renameDraft, setRenameDraft] = useState('');
  const [copiedId, setCopiedId] = useState(false);
  const [scrolledUp, setScrolledUp] = useState(false);

  const scrollRef = useRef<HTMLDivElement | null>(null);
  const contentRef = useRef<HTMLDivElement | null>(null);
  const followingRef = useRef(true);
  const headerMenuRef = useRef<HTMLDivElement | null>(null);
  const turnRefs = useRef<Record<string, HTMLDivElement | null>>({});

  const activeSession = sessions.find((s) => s.id === activeSessionId);
  const activeModelMeta = models.find((m) => m.provider_id === selectedProviderId && m.id === selectedModelId);

  const isDraft = currentStream.items.length === 0 && !activeSessionId;
  const items = currentStream.items;

  const registerTurnRef = useCallback((id: string, el: HTMLDivElement | null) => {
    turnRefs.current[id] = el;
  }, []);

  const scrollToBottom = useCallback((behavior: ScrollBehavior = 'auto') => {
    const el = scrollRef.current;
    if (!el) return;
    followingRef.current = true;
    setScrolledUp(false);
    if (behavior === 'auto') {
      el.scrollTop = el.scrollHeight;
    } else {
      el.scrollTo({ top: el.scrollHeight, behavior });
    }
  }, []);

  // Reset bottom-following intent when switching sessions
  useEffect(() => {
    followingRef.current = true;
    setScrolledUp(false);
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [activeSessionId]);

  // ZCode ResizeObserver Scroll Anchor: tokens never drive scroll directly; ResizeObserver sticks cleanly when height grows!
  useEffect(() => {
    const contentEl = contentRef.current;
    const scrollEl = scrollRef.current;
    if (!contentEl || !scrollEl || typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(() => {
      if (followingRef.current) {
        scrollEl.scrollTop = scrollEl.scrollHeight;
      }
    });
    ro.observe(contentEl);
    return () => ro.disconnect();
  }, [isDraft]);

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
          <div className="zcode-popover-down absolute right-0 top-8 z-50 w-64 rounded-xl border border-border bg-popover p-2 shadow-md">
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
                <span>{locale === 'en-US' ? 'Cycle reasoning effort' : '切换推理强度'}</span>
                <kbd className="rounded bg-tag px-1.5 font-mono text-ui-xs">^ T</kbd>
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
                <div className="zcode-popover-down absolute left-0 top-8 z-50 w-52 rounded-xl border border-border bg-popover p-1 shadow-md">
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
          onWheelCapture={(e) => {
            if (e.deltaY < 0) {
              followingRef.current = false;
            }
          }}
          onScroll={(e) => {
            const el = e.currentTarget;
            const distFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
            if (distFromBottom <= 48) {
              followingRef.current = true;
              if (scrolledUp) setScrolledUp(false);
            } else if (!followingRef.current && !scrolledUp) {
              setScrolledUp(true);
            }
          }}
          className="absolute inset-0 overflow-y-auto px-4 py-5 sm:px-8"
        >
          {isDraft ? (
            <div className="flex h-full flex-col items-center justify-center pb-10">
              <div className="zcode-draft-hero flex flex-col items-center">
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
              </div>

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
                setSelectedProvider={setSelectedProvider}
                setSelectedModel={setSelectedModel}
                setThinkingEffort={setThinkingEffort}
                setPermissionMode={setPermissionMode}
                sendPromptOrSteer={sendPromptOrSteer}
                abortActiveSession={abortActiveSession}
                composerText={composerText}
              />

              <div className="mt-5 flex flex-wrap items-center justify-center gap-2.5">
                {CHIPS[locale].map((chip, index) => {
                  const Icon = chip.icon;
                  return (
                    <button
                      key={chip.label}
                      type="button"
                      style={{ '--zcode-draft-prompt-waterfall-delay': `${index * 45}ms` } as CSSProperties}
                      onClick={() => {
                        setComposerText(chip.prompt);
                      }}
                      className="zcode-draft-prompt-waterfall flex h-8 items-center gap-2 rounded-xl border border-border bg-card px-3.5 text-ui-sm text-foreground-subtle shadow-2xs transition-all duration-150 hover:-translate-y-px hover:border-border-hover hover:text-foreground hover:shadow-xs active:translate-y-0"
                    >
                      <Icon className="size-3.5 shrink-0" />
                      <span>{chip.label}</span>
                    </button>
                  );
                })}
              </div>
            </div>
          ) : (
            <div ref={contentRef} className="mx-auto max-w-4xl space-y-5">
              {items.map((item) => (
                <MemoTurnItem key={item.id} item={item} locale={locale} registerRef={registerTurnRef} />
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
                className="h-[1.5px] w-2.5 bg-foreground/20 transition-all duration-150 hover:w-4 hover:bg-foreground"
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
            className="zcode-popover-up absolute bottom-3 left-1/2 z-20 flex size-7 -translate-x-1/2 items-center justify-center rounded-full border border-border bg-card text-foreground-subtle shadow-sm transition-all hover:scale-105 hover:text-foreground active:scale-95"
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
            setSelectedProvider={setSelectedProvider}
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
