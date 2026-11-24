import {
  AlertTriangle,
  Archive,
  ArrowUp,
  ArrowUpCircle,
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
  Download,
  Ellipsis,
  FileText,
  Folder,
  FolderPlus,
  FolderTree,
  GitBranch,
  GitPullRequest,
  Globe,
  Hand,
  Hash,
  ImagePlus,
  Info,
  ListTodo,
  LoaderCircle,
  MessageSquare,
  PanelLeft,
  PanelRight,
  Pencil,
  Pin,
  PinOff,
  Plug,
  Plus,
  RotateCcw,
  Search,
  Settings,
  ShieldAlert,
  ShieldCheck,
  Sparkles,
  Square,
  SquareTerminal,
  X,
  Zap,
} from 'lucide-react';
import { type CSSProperties, memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { clampEffortIndex, formatEffortLabel, resolveEffortLevels } from '../core/effort';
import { emptyStreamState } from '../core/reducer';
import { useNexStore } from '../core/store';
import { ChatTurnItem, ModelMeta, ThinkingEffort, ToolCallCard } from '../core/types';
import { formatMsg, type LocaleCode, type MessageKey } from '../i18n/messages';
import { AboutModal } from './AboutModal';
import { ChatLoading } from './ChatLoading';
import { ContextWindowsButton } from './ContextPopover';
import { InlineQuestionCard } from './ControlModals';
import {
  ConversationStatusPanel,
  type StatusPanelMode,
  useConversationStatusPanelHasContent,
} from './ConversationStatusPanel';
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

function ToolSummaryRow({ tc, dense = false }: { tc: ToolCallCard; dense?: boolean }) {
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
      <span className="shrink-0 font-normal animated-gradient-text">{formatMsg(locale, key)}</span>
    ) : (
      <span className={`shrink-0 font-normal ${tc.status === 'error' ? 'text-destructive' : 'text-foreground'}`}>
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
        ? formatMsg(locale, 'chat.find', { pattern })
        : formatMsg(locale, 'chat.listFilesMatching', { pattern });
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
          <div className="nex-collapsible" data-open={true}>
            <div className="nex-collapsible-inner">
              <div className="ml-2 mt-1 max-h-60 overflow-auto border-l border-border pl-3.5 font-mono text-ui-sm whitespace-pre-wrap text-foreground-subtle">
                {output}
              </div>
            </div>
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
        {open && Boolean(tc.diff || output) && (
          <div className="nex-collapsible" data-open={true}>
            <div className="nex-collapsible-inner">
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
            </div>
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
        <ToolIndeterminateBar active={running} />
        {open && (
          <div className="nex-collapsible" data-open={true}>
            <div className="nex-collapsible-inner">
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
        )}
      </div>
    );
  }

  if (tc.name === 'browser') {
    const action = String(args.action || '');
    const targetDetail = String(
      args.url || args.ref || args.selector || args.text || args.reason || args.tab_id || '',
    );
    const images = tc.images || [];
    return (
      <div className="flex max-w-full flex-col">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className={`group/tool inline-flex max-w-full items-center gap-2 py-0.5 text-left ${rowClass}`}
        >
          <Globe className="size-3.5 shrink-0 text-foreground-subtlest" />
          <span className="shrink-0 font-medium text-foreground-subtle">
            {formatMsg(locale, running ? 'tool.browsing' : 'tool.browser')}
          </span>
          {(action || targetDetail) && (
            <span className="min-w-0 truncate font-mono text-ui-sm text-foreground-subtle">
              {truncateText([action, targetDetail].filter(Boolean).join(' '), 72)}
            </span>
          )}
          {chevron()}
        </button>
        <ToolIndeterminateBar active={running} />
        {images.length > 0 && (
          <div className="ml-5 mt-1.5 flex flex-wrap gap-2">
            {images.map((img, idx) => {
              const src =
                img.image_url ||
                (img.data_b64 ? `data:${img.mime_type || 'image/png'};base64,${img.data_b64}` : '');
              if (!src) return null;
              return (
                <button
                  key={`${tc.id}-img-${idx}`}
                  type="button"
                  data-testid={`tool-image-${idx}`}
                  onClick={() =>
                    useNexStore.getState().openImagePreview({
                      src,
                      alt: `browser-screenshot-${idx + 1}`,
                    })
                  }
                  className="group/thumb overflow-hidden rounded-lg border border-border bg-card transition-colors hover:border-foreground-subtle"
                >
                  <img
                    src={src}
                    alt={`browser-screenshot-${idx + 1}`}
                    className="max-h-28 w-auto object-cover"
                  />
                </button>
              );
            })}
          </div>
        )}
        {open && output && (
          <div className="nex-collapsible" data-open={true}>
            <div className="nex-collapsible-inner">
              <div className="ml-2 mt-1 max-h-60 overflow-auto border-l border-border pl-3.5 font-mono text-ui-sm whitespace-pre-wrap text-foreground-subtle">
                {output}
              </div>
            </div>
          </div>
        )}
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
            {running ? (formatMsg(locale, 'chat.fetching')) : formatMsg(locale, 'chat.fetched')}
          </span>
          {url && <span className="min-w-0 truncate font-mono text-ui-sm text-foreground-subtle">{truncateText(url, 72)}</span>}
          {chevron()}
        </button>
        <ToolIndeterminateBar active={running} />
        {open && output && (
          <div className="nex-collapsible" data-open={true}>
            <div className="nex-collapsible-inner">
              <div className="ml-2 mt-1 max-h-60 overflow-auto border-l border-border pl-3.5 font-mono text-ui-sm whitespace-pre-wrap text-foreground-subtle">
                {output}
              </div>
            </div>
          </div>
        )}
      </div>
    );
  }

  if (tc.name === 'ask_user_question') {
    const firstListed = Array.isArray(args.questions)
      ? (args.questions[0] as { question?: string } | undefined)?.question
      : undefined;
    const question = String(args.question || firstListed || '');
    return (
      <div className="flex max-w-full flex-col">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className={`group/tool inline-flex max-w-full items-center gap-2 py-0.5 text-left ${rowClass}`}
        >
          <CircleHelp className="size-3.5 shrink-0 text-foreground-subtlest" />
          <span className="shrink-0 font-medium text-foreground-subtle">
            {running ? (formatMsg(locale, 'chat.asking')) : formatMsg(locale, 'chat.asked')}
          </span>
          {question && <span className="min-w-0 truncate text-foreground-subtle">{truncateText(question, 72)}</span>}
          {chevron()}
        </button>
        {open && output && (
          <div className="nex-collapsible" data-open={true}>
            <div className="nex-collapsible-inner">
              <div className="ml-2 mt-1 max-h-40 overflow-auto border-l border-border pl-3.5 text-ui-sm whitespace-pre-wrap text-foreground-subtle">
                {output}
              </div>
            </div>
          </div>
        )}
      </div>
    );
  }

  if (tc.name === 'task_output' || tc.name === 'task_stop') {
    const taskId = String(args.task_id || '');
    const title =
      tc.name === 'task_stop'
        ? formatMsg(locale, 'chat.stoppedTask')
        : running
          ? formatMsg(locale, 'chat.checkingTask')
          : formatMsg(locale, 'chat.checkedTask');
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
        {open && output && (
          <div className="nex-collapsible" data-open={true}>
            <div className="nex-collapsible-inner">
              <pre className="ml-2 mt-1 max-h-60 overflow-auto border-l border-border pl-3.5 font-mono text-ui-sm whitespace-pre-wrap text-foreground-subtle">
                {output}
              </pre>
            </div>
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
        <ToolIndeterminateBar active={running} />
        {open && output && (
          <div className="nex-collapsible" data-open={true}>
            <div className="nex-collapsible-inner">
              <div className="mb-2 mt-1.5 max-h-60 overflow-auto rounded-xl border border-border bg-card p-2.5 font-mono text-[12px] leading-5 whitespace-pre-wrap text-foreground-subtle">
                {output}
              </div>
            </div>
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
          <Bot className={`size-3.5 shrink-0 text-success ${running ? 'animate-pulse' : ''}`} />
          <span className={`shrink-0 font-medium ${running ? 'animated-gradient-text' : 'text-foreground-subtle'}`}>
            {formatMsg(locale, 'tool.subagent')}
          </span>
          {summary && <span className="min-w-0 truncate text-foreground-subtle">{summary}</span>}
          {chevron()}
        </button>
        <ToolIndeterminateBar active={running} />
        {open && output && (
          <div className="nex-collapsible" data-open={true}>
            <div className="nex-collapsible-inner">
              <div className="ml-2 mt-1 max-h-60 overflow-auto border-l border-border pl-3.5 text-ui-sm whitespace-pre-wrap text-foreground-subtle">
                {output}
              </div>
            </div>
          </div>
        )}
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
          {formatMsg(locale, 'chat.updatedPlan')}
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
      {open && (
        <div className="nex-collapsible" data-open={true}>
          <div className="nex-collapsible-inner">
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
      )}
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

const SESSION_REF_REGEX = /#((?:sess|session)_[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*)/g;

function extractPromptSessionRefs(text: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const m of text.matchAll(SESSION_REF_REGEX)) {
    const id = m[1];
    if (id && !seen.has(id)) {
      seen.add(id);
      out.push(id);
    }
  }
  return out;
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
  const sessions = useNexStore((s) => s.sessions);
  const selectSession = useNexStore((s) => s.selectSession);
  const setComposerText = useNexStore((s) => s.setComposerText);
  const forkSession = useNexStore((s) => s.forkSession);
  const editAndResendMessage = useNexStore((s) => s.editAndResendMessage);
  const openImagePreview = useNexStore((s) => s.openImagePreview);
  const [copied, setCopied] = useState(false);
  const [editing, setEditing] = useState(false);
  const [editDraft, setEditDraft] = useState('');
  const displayText = cleanUserDisplayText(text);

  const renderRichUserText = (raw: string) => {
    const parts: Array<string | { refId: string }> = [];
    let last = 0;
    for (const m of raw.matchAll(SESSION_REF_REGEX)) {
      const idx = m.index ?? 0;
      if (idx > last) parts.push(raw.slice(last, idx));
      parts.push({ refId: m[1] });
      last = idx + m[0].length;
    }
    if (last < raw.length) parts.push(raw.slice(last));
    if (parts.length === 1 && typeof parts[0] === 'string') return raw;
    return parts.map((p, i) => {
      if (typeof p === 'string') return <span key={i}>{p}</span>;
      const target = sessions.find((s) => s.id === p.refId);
      const label = target?.title || p.refId;
      return (
        <button
          key={i}
          type="button"
          onClick={() => void selectSession(p.refId)}
          title={formatMsg(locale, 'chat.openReferencedSession', { refId: p.refId })}
          className="mx-0.5 inline-flex max-w-[240px] items-center gap-1 rounded-md border border-brand-blue/30 bg-brand-blue/10 px-1.5 py-0.5 align-baseline text-ui-xs font-medium text-brand-blue transition-colors hover:bg-brand-blue/20"
        >
          <Hash className="size-3 shrink-0" />
          <span className="truncate">{label}</span>
        </button>
      );
    });
  };

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
              <button
                key={idx}
                type="button"
                data-testid={`user-image-${idx}`}
                onClick={() => openImagePreview({ src, alt: `attachment-${idx + 1}` })}
                className="group/img relative cursor-zoom-in overflow-hidden rounded-xl border border-border shadow-xs transition-shadow hover:shadow-md"
              >
                <img
                  src={src}
                  alt={`attachment-${idx + 1}`}
                  className="max-h-36 max-w-48 object-cover transition-transform duration-150 group-hover/img:scale-[1.02]"
                />
              </button>
            );
          })}
        </div>
      )}
      {editing ? (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const next = editDraft.trim();
            setEditing(false);
            if (!next) return;
            if (activeSessionId && seq && seq > 0) {
              void editAndResendMessage(activeSessionId, seq, next);
            } else {
              setComposerText(next);
            }
          }}
          className="w-full max-w-xl space-y-2 rounded-xl border border-primary/50 bg-surface p-3"
        >
          <textarea
            autoFocus
            rows={3}
            value={editDraft}
            onChange={(e) => setEditDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape') {
                setEditing(false);
              } else if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault();
                const next = editDraft.trim();
                setEditing(false);
                if (!next) return;
                if (activeSessionId && seq && seq > 0) {
                  void editAndResendMessage(activeSessionId, seq, next);
                } else {
                  setComposerText(next);
                }
              }
            }}
            className="w-full resize-y rounded-lg border border-border bg-input p-2 text-ui-base text-foreground outline-none focus:border-primary"
          />
          <div className="flex items-center justify-end gap-1.5">
            <button
              type="button"
              onClick={() => setEditing(false)}
              className="h-7 rounded-lg border border-border bg-card px-2.5 text-ui-xs text-foreground-subtle hover:text-foreground"
            >
              {formatMsg(locale, 'common.cancel')}
            </button>
            <button
              type="submit"
              className="h-7 rounded-lg bg-primary px-3 text-ui-xs font-medium text-primary-foreground hover:opacity-90"
            >
              {formatMsg(locale, 'common.confirm')}
            </button>
          </div>
        </form>
      ) : (
        displayText && (
          <div className="max-w-full whitespace-pre-wrap break-words rounded-xl rounded-tr-xs border border-border bg-surface px-4 py-3 text-ui-base leading-[1.5] text-foreground @min-[624px]/conversation:max-w-xl">
            {renderRichUserText(displayText)}
          </div>
        )
      )}
      {!editing && (
      <div className="mt-0.5 flex items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100">
        <button
          type="button"
          onClick={copy}
          title={formatMsg(locale, 'common.copy')}
          className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-ui-xs text-foreground-subtlest transition-colors hover:text-foreground"
        >
          {copied ? <Check className="nex-reaction-pop size-3 text-success" /> : <Copy className="size-3" />}
          <span>{copied ? (formatMsg(locale, 'common.copied')) : formatMsg(locale, 'common.copy')}</span>
        </button>
        <button
          type="button"
          onClick={() => {
            if (activeSessionId && seq && seq > 0) {
              setEditDraft(displayText);
              setEditing(true);
            } else {
              setComposerText(displayText);
            }
          }}
          title={formatMsg(locale, 'chat.editInComposer')}
          className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-ui-xs text-foreground-subtlest transition-colors hover:text-foreground"
        >
          <Pencil className="size-3" />
          <span>{formatMsg(locale, 'common.edit')}</span>
        </button>
        {activeSessionId && seq && seq > 0 && (
          <button
            type="button"
            data-testid={`fork-before-${seq}`}
            onClick={() => void forkSession(activeSessionId, { beforeSeq: seq, prefillText: displayText })}
            title={formatMsg(locale, 'chat.branchEditFromBeforeThis')}
            className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-ui-xs text-foreground-subtlest transition-colors hover:text-foreground"
          >
            <GitBranch className="size-3" />
            <span>{formatMsg(locale, 'chat.branchEdit')}</span>
          </button>
        )}
      </div>
      )}
    </div>
  );
}

function toolActivityLabel(tc: ToolCallCard, locale: LocaleCode): string {
  const args = parseToolArgs(tc);
  const file = baseName(String(args.file_path || args.path || '')) || formatMsg(locale, 'common.file');
  if (tc.name === 'read') return formatMsg(locale, 'chat.activity.reading', { file });
  if (tc.name === 'edit') return formatMsg(locale, 'chat.activity.editing', { file });
  if (tc.name === 'write') return formatMsg(locale, 'chat.activity.writing', { file });
  if (tc.name === 'bash') {
    const cmd = truncateText(String(args.command || ''), 48);
    return formatMsg(locale, 'chat.running', { cmd });
  }
  if (tc.name === 'grep' || tc.name === 'glob') {
    return formatMsg(locale, 'chat.searchingWorkspace');
  }
  if (tc.name === 'subagent') return formatMsg(locale, 'chat.delegatingToSubagent');
  return formatMsg(locale, 'chat.running2', { name: tc.name });
}

function WorkGroup({
  item,
  locale,
  open,
  onToggle,
}: {
  item: ChatTurnItem;
  locale: LocaleCode;
  open: boolean;
  onToggle: () => void;
}) {
  const activeTool = [...item.toolCalls].reverse().find((tc) => tc.status === 'running');
  const stepCount = item.toolCalls.length + (item.thinking ? 1 : 0);
  const isProminentTool = (tc: ToolCallCard) =>
    tc.status === 'running' || tc.status === 'error' || Boolean(tc.images && tc.images.length > 0);
  const visibleTools = item.toolCalls.filter(isProminentTool);
  const hiddenTools = item.toolCalls.filter((tc) => !isProminentTool(tc));

  const shimmerText = activeTool
    ? toolActivityLabel(activeTool, locale)
    : item.thinking
      ? formatMsg(locale, 'chat.thinking')
      : formatMsg(locale, 'chat.working');

  const hasThinking = Boolean(item.thinking && item.thinking.trim());
  const hasTools = item.toolCalls.length > 0;
  const completedLabel =
    hasThinking && !hasTools
      ? formatMsg(locale, 'chat.thoughtProcess')
      : formatMsg(locale, 'chat.workedOnSteps', { stepCount });

  return (
    <div className="min-w-0">
      <button
        type="button"
        onClick={onToggle}
        className="inline-flex items-center gap-2 py-0.5 text-ui-base text-foreground-subtle transition-colors hover:text-foreground"
      >
        {hasThinking && !activeTool && (
          <Sparkles className="size-4 shrink-0 text-foreground-subtlest" />
        )}
        {item.streaming ? (
          <span className="animated-gradient-text">{shimmerText}</span>
        ) : (
          <span>{completedLabel}</span>
        )}
        <ChevronRight className={`size-3.5 shrink-0 transition-transform ${open ? 'rotate-90' : ''}`} />
      </button>

      {open && (
        <div className="nex-collapsible" data-open={true}>
          <div className="nex-collapsible-inner">
            <div className="ml-1.5 mb-3 mt-2 space-y-2 border-l border-border pl-3.5">
              {hasThinking && (
                <div className="space-y-1">
                  {hasTools && (
                    <div className="text-ui-xs font-medium text-foreground-subtlest">
                      {formatMsg(locale, 'chat.thinking2')}
                    </div>
                  )}
                  <div className="max-h-72 overflow-auto whitespace-pre-wrap py-0.5 text-ui-base leading-[1.65] tracking-wide text-foreground-subtle">
                    {item.thinking}
                  </div>
                </div>
              )}
              {hiddenTools.map((tc) => (
                <ToolSummaryRow key={tc.id} tc={tc} />
              ))}
            </div>
          </div>
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
  value: 'interactive' | 'full_access' | 'read_only' | 'plan';
  icon: typeof Hand;
  labelKey: MessageKey;
  descKey: MessageKey;
}> = [
  { value: 'interactive', icon: Hand, labelKey: 'mode.default', descKey: 'mode.default.desc' },
  { value: 'full_access', icon: ShieldCheck, labelKey: 'mode.fullAccess', descKey: 'mode.fullAccess.desc' },
  { value: 'plan', icon: ListTodo, labelKey: 'mode.plan', descKey: 'mode.plan.desc' },
  { value: 'read_only', icon: BookOpen, labelKey: 'mode.readOnly', descKey: 'mode.readOnly.desc' },
];

function formatContextLabel(n: number): string {
  if (n >= 1_000_000) return `${Number((n / 1_000_000).toFixed(1))}M`;
  if (n >= 1_000) return `${Math.round(n / 1_000)}K`;
  return String(n);
}

function isGlmHighspeedModel(name: string): boolean {
  return /glm/i.test(name) && /highspeed|flashx/i.test(name);
}

// Fixed seeds matching Nex ComposerModelMenu so bubbles flow smoothly without re-randomizing.
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
      <span key={text} className="nex-label-roll inline-flex whitespace-nowrap leading-[1.25]">
        {text}
      </span>
    </span>
  );
}

const CHIPS: Array<{ icon: typeof CalendarDays; labelKey: MessageKey; promptKey: MessageKey }> = [
  { icon: CalendarDays, labelKey: 'chat.chip.weekly.label', promptKey: 'chat.chip.weekly.prompt' },
  { icon: Bug, labelKey: 'chat.chip.bug.label', promptKey: 'chat.chip.bug.prompt' },
  { icon: GitPullRequest, labelKey: 'chat.chip.review.label', promptKey: 'chat.chip.review.prompt' },
  { icon: FolderTree, labelKey: 'chat.chip.arch.label', promptKey: 'chat.chip.arch.prompt' },
];

const PROMPT_TEMPLATE_KEYS: MessageKey[] = [
  'chat.template.overview',
  'chat.template.review',
  'chat.template.tests',
];

type ComposerMenu = 'plus' | 'model' | 'perm' | 'runner' | null;

interface SlashCmdEntry {
  command: string;
  insertText: string;
  badge: string;
  description: string;
}

interface AutocompleteTrigger {
  kind: 'mention' | 'slash' | 'session';
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

  // Inline `#session` conversation reference autocomplete (Nex `#` parity)
  const sessionMatch = /(?:^|\s)#([^\s#]*)$/.exec(before);
  if (sessionMatch) {
    const query = sessionMatch[1] || '';
    const hashPos = safeCursor - query.length - 1;
    return {
      kind: 'session',
      query,
      replaceStart: hashPos,
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
  locale: LocaleCode;
  stream: ReturnType<typeof emptyStreamState>;
  models: ReturnType<typeof useNexStore.getState>['models'];
  runners: ReturnType<typeof useNexStore.getState>['runners'];
  selectedProviderId: string;
  selectedModelId: string;
  thinkingEffort: ThinkingEffort;
  permissionMode: 'full_access' | 'interactive' | 'read_only' | 'plan';
  selectedRunnerId: string;
  selectedWorkspacePath: string;
  activeModelLimit: number;
  setComposerText: (text: string) => void;
  setSelectedRunner: (id: string, workspacePath?: string) => void;
  setSelectedProvider: (providerId: string) => void;
  setSelectedModel: (providerId: string, modelId: string) => void;
  setThinkingEffort: (effort: ThinkingEffort) => void;
  setPermissionMode: (mode: 'full_access' | 'interactive' | 'read_only' | 'plan') => void;
  sendPromptOrSteer: (overrideText?: string, mode?: 'follow_up' | 'steer') => void;
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

  const MAX_COMPOSER_HEIGHT = 200;
  const syncTextareaHeight = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = 'auto';
    const scrollH = el.scrollHeight;
    if (scrollH > 0) {
      const clamped = Math.min(scrollH, MAX_COMPOSER_HEIGHT);
      el.style.height = `${clamped}px`;
    }
    el.style.overflowY = scrollH > MAX_COMPOSER_HEIGHT ? 'auto' : 'hidden';
  }, []);

  useLayoutEffect(() => {
    syncTextareaHeight();
  }, [composerText, isDraft, syncTextareaHeight]);

  useEffect(() => {
    const el = textareaRef.current;
    if (!el || typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(() => {
      syncTextareaHeight();
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [syncTextareaHeight]);

  const providers = useNexStore((s) => s.providers);
  const sessions = useNexStore((s) => s.sessions);
  const activeSessionId = useNexStore((s) => s.activeSessionId);
  const skills = useNexStore((s) => s.skills);
  const plugins = useNexStore((s) => s.plugins);
  const recentWorkspaces = useNexStore((s) => s.recentWorkspaces);
  const hiddenModelKeys = useNexStore((s) => s.hiddenModelKeys);
  const modelOrder = useNexStore((s) => s.modelOrder);
  const composerAttachments = useNexStore((s) => s.composerAttachments);
  const addComposerAttachment = useNexStore((s) => s.addComposerAttachment);
  const removeComposerAttachment = useNexStore((s) => s.removeComposerAttachment);
  const openImagePreview = useNexStore((s) => s.openImagePreview);
  const searchWorkspaceFiles = useNexStore((s) => s.searchWorkspaceFiles);
  const followUpQueues = useNexStore((s) => s.followUpQueues);
  const removeFollowUp = useNexStore((s) => s.removeFollowUp);
  const promoteFollowUpToSteer = useNexStore((s) => s.promoteFollowUpToSteer);
  const triggerRunnerUpdate = useNexStore((s) => s.triggerRunnerUpdate);
  const [upgradingRunner, setUpgradingRunner] = useState(false);
  const activeFollowUps = activeSessionId ? followUpQueues[activeSessionId] || [] : [];

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

  const activeProvider = (selectedProviderId ? providers.find((p) => p.id === selectedProviderId) : undefined) || providers[0];
  const effectiveProviderId = selectedProviderId || activeProvider?.id || '';
  const activeProviderName = activeProvider?.name || effectiveProviderId || formatMsg(locale, 'chat.model.providers');

  const activeModel =
    selectedProviderId && selectedModelId
      ? models.find((m) => m.provider_id === selectedProviderId && m.id === selectedModelId)
      : undefined;
  const hasValidModel = Boolean(activeModel);
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

  // Bind Ctrl+T (^T) globally to cycle thinking effort level smoothly (Nex parity)
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

  const modelLabel = activeModel
    ? activeModel.display_name || activeModel.id
    : formatMsg(locale, 'chat.model.selectModel');

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
  const isRunnerIncompatible = Boolean(
    activeRunner &&
      activeRunner.status === 'online' &&
      (activeRunner.incompatible || activeRunner.update_status === 'update_required'),
  );
  const projectLabel =
    (selectedWorkspacePath.split(/[\\/]/).filter(Boolean).pop() || '') ||
    activeRunner?.name ||
    (formatMsg(locale, 'chat.selectProject'));

  const templates = useMemo(() => PROMPT_TEMPLATE_KEYS.map((k) => formatMsg(locale, k)), [locale]);
  const isRunning = stream.running;
  const canSend =
    (Boolean(composerText.trim()) || composerAttachments.length > 0) &&
    Boolean(selectedRunnerId) &&
    hasValidModel &&
    !isRunnerIncompatible;

  const incompatibleRunnerBanner =
    isRunnerIncompatible && activeRunner ? (
      <div
        data-testid="runner-incompatible-banner"
        className="mb-2 flex flex-wrap items-center justify-between gap-2 rounded-xl border border-destructive/40 bg-destructive-surface px-3.5 py-2.5 text-ui-sm text-destructive-surface-fg"
      >
        <div className="flex min-w-0 items-start gap-2">
          <ShieldAlert className="mt-0.5 size-4 shrink-0" />
          <div className="min-w-0">
            <div className="font-medium">
              {formatMsg(locale, 'chat.runnerIncompatibleTitle', { name: activeRunner.name || activeRunner.id })}
            </div>
            {activeRunner.update_reason && (
              <div className="text-ui-xs opacity-90">
                {formatMsg(locale, 'chat.runnerIncompatibleDesc', { reason: activeRunner.update_reason })}
              </div>
            )}
          </div>
        </div>
        <button
          type="button"
          data-testid="banner-upgrade-runner-btn"
          disabled={upgradingRunner}
          onClick={async () => {
            setUpgradingRunner(true);
            try {
              await triggerRunnerUpdate(activeRunner.id, { mode: 'immediate', force: true });
            } finally {
              setUpgradingRunner(false);
            }
          }}
          className="inline-flex h-7 shrink-0 items-center gap-1.5 rounded-lg bg-destructive px-3 text-ui-xs font-medium text-white transition-opacity hover:opacity-90 disabled:opacity-50"
        >
          {upgradingRunner ? <LoaderCircle className="size-3.5 animate-spin" /> : <ArrowUpCircle className="size-3.5" />}
          <span>
            {upgradingRunner
              ? formatMsg(locale, 'chat.upgradingRunner')
              : formatMsg(locale, 'chat.upgradeRunnerNow')}
          </span>
        </button>
      </div>
    ) : null;

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
        badge: formatMsg(locale, 'chat.builtIn'),
        description:
          formatMsg(locale, 'chat.goalDrivenThoroughExecutionLoop'),
      },
      {
        command: '/plan',
        insertText: '/plan ',
        badge: formatMsg(locale, 'chat.builtIn'),
        description:
          formatMsg(locale, 'chat.switchToPlanModeExplore'),
      },
      {
        command: '/compact',
        insertText: '/compact ',
        badge: formatMsg(locale, 'chat.builtIn'),
        description:
          formatMsg(locale, 'chat.compactConversationContextOptionalFocus'),
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
          description: m[2] || (formatMsg(locale, 'chat.pluginCommandFrom', { name: pl.name })),
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
        insertText: formatMsg(locale, 'chat.useSkill', { name: sk.name }),
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

  const filteredSessions = useMemo(() => {
    if (acTrigger?.kind !== 'session') return [];
    const q = acTrigger.query.toLowerCase();
    return sessions
      .filter((s) => s.id !== activeSessionId && (s.depth ?? 0) === 0)
      .filter(
        (s) =>
          !q ||
          (s.title || '').toLowerCase().includes(q) ||
          s.id.toLowerCase().includes(q) ||
          (s.workspace_path || '').toLowerCase().includes(q),
      )
      .sort((a, b) => (b.updated_at || 0) - (a.updated_at || 0))
      .slice(0, 12);
  }, [acTrigger, sessions, activeSessionId]);

  const referencedSessionIds = useMemo(() => extractPromptSessionRefs(composerText), [composerText]);
  const activeSessionTitle = useMemo(
    () => sessions.find((s) => s.id === activeSessionId)?.title,
    [sessions, activeSessionId],
  );

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
  }, [acTrigger?.kind, acTrigger?.query, mentionFiles.length, filteredSlashCommands.length, filteredSessions.length]);

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

  const acItemCount =
    acTrigger?.kind === 'slash'
      ? filteredSlashCommands.length
      : acTrigger?.kind === 'mention'
        ? mentionFiles.length
        : acTrigger?.kind === 'session'
          ? filteredSessions.length
          : 0;
  const showAcPopover =
    Boolean(acTrigger) &&
    (acItemCount > 0 ||
      (acTrigger?.kind === 'mention' && mentionLoading) ||
      acTrigger?.kind === 'session');

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
            {formatMsg(locale, 'chat.noProvidersConfigured')}
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
      data-testid={
        acTrigger.kind === 'slash'
          ? 'composer-slash-menu'
          : acTrigger.kind === 'session'
            ? 'composer-session-menu'
            : 'composer-mention-menu'
      }
      className="nex-popover-up absolute bottom-full left-0 right-0 z-50 mb-2 max-h-64 overflow-y-auto rounded-xl border border-border bg-popover p-1.5 shadow-xl"
    >
      <div className="flex items-center justify-between px-2 py-1 text-[11px] font-medium text-foreground-subtlest">
        <span>
          {acTrigger.kind === 'slash'
            ? formatMsg(locale, 'chat.commandsSkills')
            : acTrigger.kind === 'session'
              ? formatMsg(locale, 'chat.referenceSession')
              : formatMsg(locale, 'chat.workspaceFiles')}
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
      ) : acTrigger.kind === 'session' ? (
        filteredSessions.length === 0 ? (
          <div className="px-2.5 py-2 text-ui-xs text-foreground-subtlest">
            {formatMsg(locale, 'chat.noOtherSessionsToReference')}
          </div>
        ) : (
          filteredSessions.map((sess, idx) => {
            const folder = sess.workspace_path ? baseName(sess.workspace_path) : '';
            const shortId = sess.id.length > 16 ? `${sess.id.slice(0, 14)}…` : sess.id;
            return (
              <button
                key={sess.id}
                type="button"
                data-testid={`session-ref-option-${idx}`}
                onMouseDown={(e) => {
                  e.preventDefault();
                  applyAutocompleteChoice(`#${sess.id} `);
                }}
                onMouseEnter={() => setActiveAcIdx(idx)}
                className={`flex w-full items-center gap-2 rounded-lg px-2.5 py-1.5 text-left transition-colors ${
                  idx === activeAcIdx ? 'bg-menu-hover text-foreground' : 'text-foreground hover:bg-menu-hover/60'
                }`}
              >
                <MessageSquare className="size-3.5 shrink-0 text-brand-blue/80" />
                <span className="min-w-0 flex-1 truncate text-ui-sm font-medium text-foreground">
                  {sess.title || sess.id}
                </span>
                {folder && (
                  <span className="hidden shrink-0 rounded bg-surface px-1.5 py-0.5 text-[10px] text-foreground-subtlest sm:inline">
                    {folder}
                  </span>
                )}
                <span className="shrink-0 font-mono text-[10px] text-foreground-subtlest">{shortId}</span>
              </button>
            );
          })
        )
      ) : mentionLoading && mentionFiles.length === 0 ? (
        <div className="flex items-center gap-2 px-2.5 py-2 text-ui-xs text-foreground-subtlest">
          <LoaderCircle className="size-3.5 animate-spin" />
          <span>{formatMsg(locale, 'chat.searchingFiles')}</span>
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

  const referencedSessionStrip = referencedSessionIds.length > 0 ? (
    <div data-testid="composer-session-refs" className="mb-1.5 flex flex-wrap gap-1.5">
      {referencedSessionIds.map((refId) => {
        const target = sessions.find((s) => s.id === refId);
        return (
          <span
            key={refId}
            className="inline-flex max-w-[260px] items-center gap-1 rounded-lg border border-brand-blue/30 bg-brand-blue/10 px-2 py-0.5 text-ui-xs text-brand-blue"
          >
            <Hash className="size-3 shrink-0" />
            <span className="truncate font-medium">{target?.title || refId}</span>
            <button
              type="button"
              aria-label={formatMsg(locale, 'chat.removeSessionReference')}
              onClick={() => {
                const next = composerText.replace(new RegExp(`#${refId.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\s?`, 'g'), '');
                setComposerText(next);
              }}
              className="ml-0.5 rounded p-0.5 transition-colors hover:bg-brand-blue/20"
            >
              <X className="size-2.5" />
            </button>
          </span>
        );
      })}
    </div>
  ) : null;

  const attachmentStrip = composerAttachments.length > 0 ? (
    <div data-testid="composer-attachments" className="mb-2 flex flex-wrap gap-2">
      {composerAttachments.map((att) => (
        <div
          key={att.id}
          className="group relative flex items-center gap-1.5 rounded-xl border border-border bg-surface/80 p-1 pr-2"
        >
          <button
            type="button"
            onClick={() => openImagePreview({ src: att.previewUrl, alt: att.name })}
            className="cursor-zoom-in overflow-hidden rounded-lg"
          >
            <img
              src={att.previewUrl}
              alt={att.name}
              className="size-10 rounded-lg object-cover"
            />
          </button>
          <div className="min-w-0 max-w-[120px]">
            <div className="truncate text-ui-xs font-medium text-foreground">{att.name}</div>
            <div className="text-[10px] text-foreground-subtlest">{Math.max(1, Math.round(att.size / 1024))} KB</div>
          </div>
          <button
            type="button"
            aria-label={formatMsg(locale, 'chat.removeImage')}
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
      {referencedSessionStrip}
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
            if (e.key === 'ArrowDown' || (e.ctrlKey && !e.metaKey && !e.altKey && e.key.toLowerCase() === 'n')) {
              e.preventDefault();
              setActiveAcIdx((i) => (i + 1) % acItemCount);
              return;
            }
            if (e.key === 'ArrowUp' || (e.ctrlKey && !e.metaKey && !e.altKey && e.key.toLowerCase() === 'p')) {
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
              } else if (acTrigger.kind === 'session') {
                const chosenSess = filteredSessions[activeAcIdx];
                if (chosenSess) applyAutocompleteChoice(`#${chosenSess.id} `);
              } else {
                const chosenFile = mentionFiles[activeAcIdx];
                if (chosenFile) applyAutocompleteChoice(`@${chosenFile} `);
              }
              return;
            }
          }
          if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault();
            if (canSend) {
              sendPromptOrSteer(undefined, e.altKey ? 'steer' : 'follow_up');
            }
          }
        }}
        placeholder={
          isDraft
            ? formatMsg(locale, 'chat.askNexAnythingFileSession')
            : formatMsg(locale, 'chat.askForFollowUpFile')
        }
        className={`${isDraft ? 'min-h-[52px]' : 'min-h-[24px]'} block max-h-[200px] w-full resize-none overflow-y-hidden bg-transparent text-ui-base leading-relaxed text-foreground placeholder-foreground-subtlest`}
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
          title={formatMsg(locale, 'chat.addPromptOrContext')}
        >
          <Plus className="size-4" />
        </button>

        {openMenu === 'plus' && (
          <div className="nex-popover-up absolute bottom-9 left-0 z-40 w-72 rounded-xl border border-border bg-popover p-1.5 shadow-md">
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
              <span>{formatMsg(locale, 'chat.referenceAWorkspaceFile')}</span>
            </button>
            <button
              type="button"
              onClick={() => {
                const next = `${composerText}#`;
                setComposerText(next);
                setCursorPos(next.length);
                setAcDismissed(false);
                setOpenMenu(null);
                window.requestAnimationFrame(() => textareaRef.current?.focus());
              }}
              className="flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left text-ui-sm text-foreground transition-colors hover:bg-menu-hover"
            >
              <Hash className="size-3.5 shrink-0 text-foreground-subtle" />
              <span>{formatMsg(locale, 'chat.referenceAnotherSession')}</span>
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
              <span>{formatMsg(locale, 'chat.attachImageOrPaste')}</span>
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
              : permissionMode === 'plan'
                ? 'text-violet-600 hover:text-violet-600 dark:text-violet-400 dark:hover:text-violet-400'
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
          <div className="nex-popover-up absolute bottom-9 left-0 z-40 w-72 rounded-xl border border-border bg-popover p-1.5 shadow-md">
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
        <ContextWindowsButton
          telemetry={stream.latestTelemetry}
          turnTelemetries={stream.turnTelemetries}
          sessionTitle={activeSessionTitle}
          contextLimit={activeModelLimit}
        />

        <div className="min-w-0 shrink">
          <button
            type="button"
            data-testid="model-effort-btn"
            onClick={() => {
              setProviderMenuOpen(false);
              setOpenMenu((m) => (m === 'model' ? null : 'model'));
            }}
            title={effortSupported ? `${modelLabel} · ${effortDisplayLabel}` : modelLabel}
            className={`flex h-7 w-full max-w-[240px] min-w-0 items-center gap-1.5 rounded-lg px-2 text-ui-sm transition-colors hover:bg-hover hover:text-foreground ${
              !hasValidModel ? 'text-amber-600 dark:text-amber-400' : 'text-foreground-subtle'
            }`}
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
              className="nex-popover-up absolute bottom-12 right-2 z-50 w-80 max-w-[calc(100%-1rem)] rounded-2xl border border-border bg-popover shadow-xl"
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
                    className="nex-submenu-left absolute right-full top-0 z-50 mr-1.5 w-64 overflow-hidden rounded-xl border border-border bg-popover shadow-xl"
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
                    <span>{formatMsg(locale, 'chat.back')}</span>
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

                {/* Nex Effort Slider (strictly ordered per-model levels; hidden/disabled for non-reasoning models) */}
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
          aria-label={formatMsg(locale, 'chat.model')}
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
          <div className="ml-0.5 flex items-center gap-1">
            {isRunning && canSend && (
              <button
                type="button"
                data-testid="steer-now-btn"
                onClick={() => sendPromptOrSteer(undefined, 'steer')}
                title={formatMsg(locale, 'chat.steerImmediatelyAltEnter')}
                className="flex h-7 shrink-0 items-center gap-1 rounded-full border border-amber-500/35 bg-amber-500/10 px-2 text-ui-xs font-medium text-amber-600 transition-colors hover:bg-amber-500/20 dark:text-amber-400"
              >
                <Zap className="size-3" />
                <span>{formatMsg(locale, 'chat.steer2')}</span>
              </button>
            )}
            <button
              type="button"
              data-testid="send-btn"
              disabled={!canSend}
              onClick={() => sendPromptOrSteer(undefined, 'follow_up')}
              title={
                !hasValidModel
                  ? formatMsg(locale, 'chat.model.missingTooltip')
                  : isRunning
                    ? formatMsg(locale, 'chat.queueFollowUpRunsAfter')
                    : formatMsg(locale, 'chat.send')
              }
              className="flex size-7 shrink-0 items-center justify-center rounded-full bg-foreground text-background transition-all hover:bg-foreground/90 active:scale-95 disabled:bg-foreground/20 disabled:text-background/70"
            >
              <ArrowUp className="size-4" />
            </button>
          </div>
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

  const followUpQueueStrip = activeFollowUps.length > 0 && activeSessionId ? (
    <div
      data-testid="followup-queue"
      className="mb-2 space-y-1 rounded-xl border border-border/80 bg-surface/70 p-2"
    >
      <div className="flex items-center justify-between px-1 text-[11px] font-medium text-foreground-subtlest">
        <span>
          {formatMsg(locale, 'chat.queuedFollowUpsRunsAfter', { length: activeFollowUps.length })}
        </span>
      </div>
      {activeFollowUps.map((qItem, idx) => (
        <div
          key={qItem.id}
          className="flex items-center gap-2 rounded-lg border border-border/60 bg-card px-2.5 py-1.5 text-ui-xs"
        >
          <span className="shrink-0 font-mono text-[10px] text-foreground-subtlest">#{idx + 1}</span>
          <span className="min-w-0 flex-1 truncate text-foreground">{qItem.text}</span>
          <button
            type="button"
            data-testid={`followup-steer-${idx}`}
            onClick={() => promoteFollowUpToSteer(activeSessionId, qItem.id)}
            title={formatMsg(locale, 'chat.interruptSteerNow')}
            className="inline-flex shrink-0 items-center gap-0.5 rounded px-1.5 py-0.5 text-[11px] font-medium text-amber-600 transition-colors hover:bg-amber-500/15 dark:text-amber-400"
          >
            <Zap className="size-3" />
            <span>{formatMsg(locale, 'chat.steer3')}</span>
          </button>
          <button
            type="button"
            data-testid={`followup-remove-${idx}`}
            onClick={() => removeFollowUp(activeSessionId, qItem.id)}
            aria-label={formatMsg(locale, 'chat.removeQueuedFollowUp')}
            className="flex size-5 shrink-0 items-center justify-center rounded text-foreground-subtlest transition-colors hover:bg-hover hover:text-destructive"
          >
            <X className="size-3" />
          </button>
        </div>
      ))}
    </div>
  ) : null;

  const card = (
    <div
      {...dropProps}
      className={`relative rounded-2xl border bg-input px-4 pb-2.5 pt-3 transition-colors focus-within:border-border-hover composer-shadow ${
        isDraggingImage ? 'border-brand-blue ring-2 ring-brand-blue/25' : 'border-border'
      }`}
    >
      {autocompletePopover}
      {followUpQueueStrip}
      {textarea}
      {toolbar}
    </div>
  );

  if (isDraft) {
    return (
      <div ref={rootRef} className="nex-bottom-dock relative z-30 mx-auto w-full max-w-4xl">
        {incompatibleRunnerBanner}
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
                className="nex-popover-up absolute bottom-9 left-1.5 z-50 flex w-80 max-w-[calc(100vw-2rem)] flex-col overflow-hidden rounded-xl border border-border bg-popover shadow-lg"
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
                      {formatMsg(locale, 'chat.noMatchingProjects')}
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
              aria-label={formatMsg(locale, 'chat.runner')}
              value={selectedRunnerId}
              onChange={(e) => setSelectedRunner(e.target.value)}
              className="sr-only"
            >
              {runners.length === 0 && <option value="">{formatMsg(locale, 'chat.noRunner')}</option>}
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
    <div ref={rootRef} className="nex-bottom-dock mx-auto w-full max-w-4xl">
      {incompatibleRunnerBanner}
      {card}
    </div>
  );
}

const EMPTY_STREAM = emptyStreamState();
const INITIAL_VISIBLE_TURNS = 24;
const LOAD_MORE_TURNS_STEP = 24;

const MemoTurnItem = memo(function MemoTurnItemImpl({
  item,
  isLast,
  locale,
  registerRef,
}: {
  item: ChatTurnItem;
  isLast: boolean;
  locale: LocaleCode;
  registerRef: (id: string, el: HTMLDivElement | null) => void;
}) {
  const [manualOpen, setManualOpen] = useState<boolean | null>(null);
  const [copiedReply, setCopiedReply] = useState(false);
  const [rewindArmed, setRewindArmed] = useState(false);
  const [rewinding, setRewinding] = useState(false);
  const [rewindNotice, setRewindNotice] = useState('');
  const activeSessionId = useNexStore((s) => s.activeSessionId);
  const permissionMode = useNexStore((s) => s.permissionMode);
  const setPermissionMode = useNexStore((s) => s.setPermissionMode);
  const setComposerText = useNexStore((s) => s.setComposerText);
  const sendPromptOrSteer = useNexStore((s) => s.sendPromptOrSteer);
  const forkSession = useNexStore((s) => s.forkSession);
  const rewindSessionFiles = useNexStore((s) => s.rewindSessionFiles);
  const expandThinking = useNexStore((s) => s.preferences.expand_thinking);
  const defaultOpen = Boolean(item.streaming) || Boolean(expandThinking && item.thinking);
  const workExpanded = manualOpen ?? defaultOpen;
  const hasExitPlanCall = item.toolCalls.some((tc) => tc.name === 'exit_plan_mode');
  const hasFileMutations = item.toolCalls.some((tc) => tc.name === 'write' || tc.name === 'edit');
  const rewindBeforeSeq = item.startSeq || item.seq || 0;
  const showPlanApproval =
    isLast && !item.streaming && (hasExitPlanCall || (permissionMode === 'plan' && Boolean(item.text)));

  return (
    <div
      ref={(el) => registerRef(item.id, el)}
      style={{ contain: 'layout style' }}
      className={`flex flex-col ${item.role === 'user' ? 'items-end' : 'items-start'}`}
    >
      {item.role === 'user' ? (
        <UserBubble text={item.text} images={item.images} seq={item.seq} />
      ) : (
        <div className={`group/assistant min-w-0 w-full space-y-4 ${item.streaming ? 'nex-stream-in' : ''}`}>
          {(item.thinking || item.toolCalls.length > 0) && (
            <WorkGroup
              item={item}
              locale={locale}
              open={workExpanded}
              onToggle={() => setManualOpen((prev) => !(prev ?? defaultOpen))}
            />
          )}

          {item.text && <MarkdownRenderer content={item.text} streaming={Boolean(item.streaming)} />}

          <ChangeSummaryCard item={item} />

          {showPlanApproval && (
            <div
              data-testid="plan-approval-bar"
              className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-violet-500/30 bg-violet-500/8 px-3.5 py-2.5"
            >
              <div className="flex items-center gap-2 text-ui-sm text-foreground">
                <ListTodo className="size-4 shrink-0 text-violet-600 dark:text-violet-400" />
                <span className="font-medium">
                  {formatMsg(locale, 'chat.planReadyForReview')}
                </span>
              </div>
              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={() => setComposerText(formatMsg(locale, 'chat.pleaseReviseThePlan'))}
                  className="h-7 rounded-lg border border-border bg-card px-2.5 text-ui-xs font-medium text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
                >
                  {formatMsg(locale, 'chat.revisePlan')}
                </button>
                <button
                  type="button"
                  data-testid="approve-plan-btn"
                  onClick={() => {
                    setPermissionMode('full_access');
                    sendPromptOrSteer(
                      formatMsg(locale, 'chat.planApprovedProceedWithImplementation'),
                      'follow_up',
                    );
                  }}
                  className="inline-flex h-7 items-center gap-1 rounded-lg bg-violet-600 px-3 text-ui-xs font-medium text-white transition-opacity hover:opacity-90"
                >
                  <Check className="size-3.5" />
                  <span>{formatMsg(locale, 'chat.approveExecute')}</span>
                </button>
              </div>
            </div>
          )}

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
                  {copiedReply ? <Check className="nex-reaction-pop size-3 text-success" /> : <Copy className="size-3" />}
                  <span>{copiedReply ? (formatMsg(locale, 'common.copied')) : formatMsg(locale, 'common.copy')}</span>
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
                  <span>{formatMsg(locale, 'chat.forkHere')}</span>
                </button>
              )}
              {activeSessionId && hasFileMutations && rewindBeforeSeq > 0 && (
                <button
                  type="button"
                  data-testid={`rewind-turn-${rewindBeforeSeq}`}
                  disabled={rewinding}
                  onClick={async () => {
                    if (!rewindArmed) {
                      setRewindArmed(true);
                      window.setTimeout(() => setRewindArmed(false), 4000);
                      return;
                    }
                    setRewindArmed(false);
                    setRewinding(true);
                    const res = await rewindSessionFiles(activeSessionId, rewindBeforeSeq);
                    setRewinding(false);
                    if (res) {
                      const restoredCount = (res.restored_files?.length || 0) + (res.deleted_files?.length || 0);
                      setRewindNotice(
                        restoredCount > 0
                          ? formatMsg(locale, 'chat.rewoundFileS', { restoredCount })
                          : formatMsg(locale, 'chat.noCheckpointsToRewind'),
                      );
                      window.setTimeout(() => setRewindNotice(''), 4000);
                    }
                  }}
                  className={`inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-ui-xs transition-colors ${
                    rewindArmed
                      ? 'bg-amber-500/15 font-medium text-amber-600 dark:text-amber-400'
                      : 'text-foreground-subtlest hover:bg-hover hover:text-foreground'
                  }`}
                >
                  <RotateCcw className="size-3" />
                  <span>
                    {rewinding
                      ? formatMsg(locale, 'chat.rewinding')
                      : rewindArmed
                        ? formatMsg(locale, 'chat.confirmRewindFiles')
                        : formatMsg(locale, 'chat.rewindFiles')}
                  </span>
                </button>
              )}
              {rewindNotice && (
                <span
                  data-testid="rewind-notice"
                  className="rounded-md bg-success/12 px-2 py-0.5 text-ui-xs font-medium text-success"
                >
                  {rewindNotice}
                </span>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
});

const TICK_RAIL_DEFAULT_BUDGET_PX = 360;
const TICK_RAIL_MAX_BUDGET_PX = 420;
const TICK_RAIL_MIN_BUDGET_PX = 120;
const TICK_DEFAULT_SLOT_PX = 16;
const TICK_DEFAULT_GAP_PX = 2;
const TICK_MIN_SLOT_PX = 4;

export interface TurnTickLayoutEntry {
  item: ChatTurnItem;
  idx: number;
}

export interface TurnTickLayout {
  entries: TurnTickLayoutEntry[];
  slotHeightPx: number;
  gapPx: number;
  totalHeightPx: number;
}

/**
 * Fits the left-edge turn tick rail within `railBudgetPx` by progressively increasing
 * vertical tick density and, for very long sessions, evenly sampling ticks while always
 * keeping the first, last, and active turn accessible.
 */
export function computeTurnTickLayout(
  items: ChatTurnItem[],
  activeTurnId: string,
  railBudgetPx: number = TICK_RAIL_DEFAULT_BUDGET_PX,
): TurnTickLayout {
  if (items.length === 0) {
    return { entries: [], slotHeightPx: TICK_DEFAULT_SLOT_PX, gapPx: TICK_DEFAULT_GAP_PX, totalHeightPx: 0 };
  }

  const budget = Math.max(40, Math.floor(railBudgetPx));
  const maxTicks = Math.max(2, Math.floor(budget / TICK_MIN_SLOT_PX));
  const rawActiveIdx = activeTurnId ? items.findIndex((it) => it.id === activeTurnId) : -1;
  const activeIdx = rawActiveIdx >= 0 ? rawActiveIdx : items.length - 1;

  let indices: number[];
  if (items.length <= maxTicks) {
    indices = items.map((_, i) => i);
  } else {
    const sampled: number[] = [];
    for (let k = 0; k < maxTicks; k++) {
      const idx = Math.round((k * (items.length - 1)) / (maxTicks - 1));
      if (sampled.length === 0 || sampled[sampled.length - 1] !== idx) {
        sampled.push(idx);
      }
    }
    if (!sampled.includes(activeIdx) && sampled.length >= 3) {
      let bestSlot = 1;
      let bestDist = Number.POSITIVE_INFINITY;
      for (let s = 1; s < sampled.length - 1; s++) {
        const d = Math.abs(sampled[s] - activeIdx);
        if (d < bestDist) {
          bestDist = d;
          bestSlot = s;
        }
      }
      sampled[bestSlot] = activeIdx;
      sampled.sort((a, b) => a - b);
    }
    indices = sampled;
  }

  const count = indices.length;
  const defaultTotal = count * (TICK_DEFAULT_SLOT_PX + TICK_DEFAULT_GAP_PX) - TICK_DEFAULT_GAP_PX;
  let slotHeightPx = TICK_DEFAULT_SLOT_PX;
  let gapPx = TICK_DEFAULT_GAP_PX;
  if (defaultTotal > budget) {
    gapPx = 0;
    slotHeightPx = Math.max(TICK_MIN_SLOT_PX, Math.floor(budget / count));
  }

  return {
    entries: indices.map((idx) => ({ item: items[idx], idx })),
    slotHeightPx,
    gapPx,
    totalHeightPx: count * slotHeightPx + Math.max(0, count - 1) * gapPx,
  };
}

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
  const exportSessionMarkdown = useNexStore((s) => s.exportSessionMarkdown);

  const [headerMenu, setHeaderMenu] = useState<'session' | 'help' | null>(null);
  const [aboutOpen, setAboutOpen] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const [renameDraft, setRenameDraft] = useState('');
  const [copiedId, setCopiedId] = useState(false);
  const [scrolledUp, setScrolledUp] = useState(false);
  const [activeTurnId, setActiveTurnId] = useState('');
  const [visibleTurnLimit, setVisibleTurnLimit] = useState(INITIAL_VISIBLE_TURNS);
  const [pendingJumpId, setPendingJumpId] = useState('');
  const [statusPanelMode, setStatusPanelMode] = useState<StatusPanelMode>('auto');
  const [timelineHeight, setTimelineHeight] = useState(0);
  const hasStatusPanel = useConversationStatusPanelHasContent();

  const scrollRef = useRef<HTMLDivElement | null>(null);
  const contentRef = useRef<HTMLDivElement | null>(null);
  const followingRef = useRef(true);
  const headerMenuRef = useRef<HTMLDivElement | null>(null);
  const turnRefs = useRef<Record<string, HTMLDivElement | null>>({});

  const activeSession = sessions.find((s) => s.id === activeSessionId);
  const activeModelMeta = models.find((m) => m.provider_id === selectedProviderId && m.id === selectedModelId);

  const isDraft = currentStream.items.length === 0 && !activeSessionId;
  const items = currentStream.items;

  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const updateHeight = () => {
      if (el.clientHeight > 0) {
        setTimelineHeight(el.clientHeight);
      }
    };
    updateHeight();
    if (typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(updateHeight);
    ro.observe(el);
    return () => ro.disconnect();
  }, [isDraft]);

  const railBudgetPx =
    timelineHeight > 0
      ? Math.min(
          TICK_RAIL_MAX_BUDGET_PX,
          Math.max(80, Math.min(timelineHeight - 32, Math.max(TICK_RAIL_MIN_BUDGET_PX, Math.floor(timelineHeight * 0.72)))),
        )
      : TICK_RAIL_DEFAULT_BUDGET_PX;

  const tickLayout = useMemo(
    () => computeTurnTickLayout(items, activeTurnId, railBudgetPx),
    [items, activeTurnId, railBudgetPx],
  );

  const hiddenTurnCount = items.length > visibleTurnLimit ? items.length - visibleTurnLimit : 0;
  const visibleItems = useMemo(
    () => (hiddenTurnCount > 0 ? items.slice(hiddenTurnCount) : items),
    [items, hiddenTurnCount],
  );
  const prependScrollAnchorRef = useRef<{ prevScrollHeight: number; prevScrollTop: number } | null>(null);

  const loadEarlierTurns = useCallback(
    (expandAll = false) => {
      const scrollEl = scrollRef.current;
      if (scrollEl) {
        prependScrollAnchorRef.current = {
          prevScrollHeight: scrollEl.scrollHeight,
          prevScrollTop: scrollEl.scrollTop,
        };
      }
      followingRef.current = false;
      setVisibleTurnLimit((prev) =>
        expandAll ? Number.MAX_SAFE_INTEGER : prev + LOAD_MORE_TURNS_STEP,
      );
    },
    [],
  );

  useLayoutEffect(() => {
    const anchor = prependScrollAnchorRef.current;
    if (!anchor) return;
    prependScrollAnchorRef.current = null;
    const scrollEl = scrollRef.current;
    if (!scrollEl) return;
    const delta = scrollEl.scrollHeight - anchor.prevScrollHeight;
    if (delta > 0) {
      scrollEl.scrollTop = anchor.prevScrollTop + delta;
    }
  }, [hiddenTurnCount]);

  const registerTurnRef = useCallback((id: string, el: HTMLDivElement | null) => {
    if (el) {
      turnRefs.current[id] = el;
    } else {
      delete turnRefs.current[id];
    }
  }, []);

  const jumpToTurn = useCallback(
    (id: string) => {
      const el = turnRefs.current[id];
      if (!el) {
        const idx = items.findIndex((it) => it.id === id);
        if (idx >= 0) {
          followingRef.current = false;
          setActiveTurnId(id);
          setPendingJumpId(id);
          setVisibleTurnLimit((prev) => Math.max(prev, items.length - idx + 4));
        }
        return;
      }
      const scrollEl = scrollRef.current;
      followingRef.current = false;
      setActiveTurnId(id);
      if (scrollEl) {
        const elRect = el.getBoundingClientRect();
        const scrollRect = scrollEl.getBoundingClientRect();
        const targetTop = Math.max(0, scrollEl.scrollTop + (elRect.top - scrollRect.top) - 20);
        if (typeof scrollEl.scrollTo === 'function') {
          scrollEl.scrollTo({ top: targetTop, behavior: 'smooth' });
        } else {
          scrollEl.scrollTop = targetTop;
        }
        const distFromBottom = scrollEl.scrollHeight - targetTop - scrollEl.clientHeight;
        setScrolledUp(distFromBottom > 120);
      } else if (typeof el.scrollIntoView === 'function') {
        el.scrollIntoView({ behavior: 'smooth', block: 'start' });
      }
    },
    [items],
  );

  useEffect(() => {
    if (!pendingJumpId) return;
    const el = turnRefs.current[pendingJumpId];
    if (!el) return;
    setPendingJumpId('');
    jumpToTurn(pendingJumpId);
  }, [pendingJumpId, visibleItems, jumpToTurn]);

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

  // Reset bottom-following intent & tail window synchronously BEFORE paint when switching sessions
  useLayoutEffect(() => {
    followingRef.current = true;
    prependScrollAnchorRef.current = null;
    setScrolledUp(false);
    setVisibleTurnLimit(INITIAL_VISIBLE_TURNS);
    setPendingJumpId('');
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
    const rafId = window.requestAnimationFrame(() => {
      if (!followingRef.current) return;
      const cur = scrollRef.current;
      if (cur) cur.scrollTop = cur.scrollHeight;
    });
    return () => window.cancelAnimationFrame(rafId);
  }, [activeSessionId]);

  // Keep scroll pinned to bottom synchronously when async history replaces/populates items for the active session.
  useLayoutEffect(() => {
    if (!followingRef.current || prependScrollAnchorRef.current) return;
    const el = scrollRef.current;
    if (el) {
      el.scrollTop = el.scrollHeight;
    }
  }, [items, visibleItems.length]);

  // Nex ResizeObserver Scroll Anchor: tokens never drive scroll directly; ResizeObserver sticks cleanly when height grows!
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

  const sessionTitle = activeSession?.title || (formatMsg(locale, 'chat.newTask'));
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
          data-testid="header-help-btn"
          onClick={() => setHeaderMenu((m) => (m === 'help' ? null : 'help'))}
          title={formatMsg(locale, 'header.help')}
          className="flex size-7 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
        >
          <CircleHelp className="size-4" />
        </button>
        {headerMenu === 'help' && (
          <div className="nex-popover-down absolute right-0 top-8 z-50 w-64 rounded-xl border border-border bg-popover p-2 shadow-md">
            <div className="px-1 pb-1.5 text-ui-sm font-medium text-foreground">{formatMsg(locale, 'header.help')}</div>
            <div className="space-y-1 px-1 text-ui-sm text-foreground-subtle">
              <div className="flex items-center justify-between gap-3">
                <span>{formatMsg(locale, 'chat.sendPrompt')}</span>
                <kbd className="rounded bg-tag px-1.5 font-mono text-ui-xs">Enter</kbd>
              </div>
              <div className="flex items-center justify-between gap-3">
                <span>{formatMsg(locale, 'chat.newLine')}</span>
                <kbd className="rounded bg-tag px-1.5 font-mono text-ui-xs">Shift+Enter</kbd>
              </div>
              <div className="flex items-center justify-between gap-3">
                <span>{formatMsg(locale, 'chat.cycleReasoningEffort')}</span>
                <kbd className="rounded bg-tag px-1.5 font-mono text-ui-xs">^ T</kbd>
              </div>
              <div className="flex items-center justify-between gap-3">
                <span>{formatMsg(locale, 'chat.referenceFile')}</span>
                <kbd className="rounded bg-tag px-1.5 font-mono text-ui-xs">@</kbd>
              </div>
              <div className="flex items-center justify-between gap-3">
                <span>{formatMsg(locale, 'chat.steerRunningAgent')}</span>
                <kbd className="rounded bg-tag px-1.5 font-mono text-ui-xs">Enter</kbd>
              </div>
            </div>
            <div className="my-1.5 h-px bg-border" />
            <button
              type="button"
              data-testid="open-about-btn"
              onClick={() => {
                setHeaderMenu(null);
                setAboutOpen(true);
              }}
              className="flex h-8 w-full items-center gap-2 rounded-lg px-2 text-left text-ui-sm font-medium text-foreground transition-colors hover:bg-menu-hover"
            >
              <Info className="size-3.5 text-foreground-subtle" />
              <span>{formatMsg(locale, 'about.menuItem')}</span>
            </button>
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

  const statusRightReserveClass =
    !isDraft && hasStatusPanel
      ? statusPanelMode === 'panel'
        ? '@min-[960px]/conversation:!pr-[22rem]'
        : statusPanelMode === 'auto'
          ? '@min-[1280px]/conversation:!pr-[22rem]'
          : ''
      : '';
  const statusTopClearanceClass =
    !isDraft && hasStatusPanel
      ? statusPanelMode === 'mini'
        ? 'pt-14 pb-5'
        : statusPanelMode === 'auto'
          ? 'pt-14 pb-5 @min-[1280px]/conversation:pt-5'
          : 'pt-14 pb-5 @min-[960px]/conversation:pt-5'
      : 'py-5';

  const collapsedHeaderPad = 'pl-[132px] nex-titlebar-collapsed-header';

  return (
    <section
      data-conversation-container="true"
      className="@container/conversation relative flex h-full min-h-0 min-w-0 flex-1 flex-col bg-background text-foreground"
    >
      {isDraft ? (
        <header
          className={`nex-drag-region flex h-12 shrink-0 items-center justify-between pr-4 transition-[padding] duration-200 ${
            sidebarVisible ? 'pl-4' : collapsedHeaderPad
          }`}
        >
          <div className="nex-no-drag flex min-w-0 items-center gap-1">
            {sidebarVisible && (
              <button
                type="button"
                data-testid="sidebar-toggle-btn"
                onClick={toggleSidebar}
                title={formatMsg(locale, 'chat.toggleSidebar')}
                className="flex size-7 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
              >
                <PanelLeft className="size-4" />
              </button>
            )}
          </div>
          {headerControls}
        </header>
      ) : (
        <header
          data-testid="conversation-header"
          className={`nex-drag-region flex h-12 shrink-0 items-center justify-between gap-2 border-b border-border bg-background pr-4 transition-[padding] duration-200 ${
            sidebarVisible ? 'pl-4' : collapsedHeaderPad
          }`}
        >
          <div className="nex-no-drag flex min-w-0 flex-1 items-center gap-1.5">
            {sidebarVisible && (
              <button
                type="button"
                data-testid="sidebar-toggle-btn"
                onClick={toggleSidebar}
                title={formatMsg(locale, 'chat.toggleSidebar')}
                className="flex size-7 shrink-0 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
              >
                <PanelLeft className="size-4" />
              </button>
            )}
            {renaming ? (
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  void commitRename();
                }}
                className="flex min-w-0 flex-1 max-w-xl items-center gap-1"
              >
                <input
                  autoFocus
                  data-testid="session-inline-rename-input"
                  value={renameDraft}
                  onChange={(e) => setRenameDraft(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Escape') {
                      setRenaming(false);
                    }
                  }}
                  onBlur={() => {
                    // Delay slightly so clicking the Check/X buttons still fires before blur
                    window.setTimeout(() => {
                      setRenaming((cur) => (cur ? false : cur));
                    }, 120);
                  }}
                  className="h-8 min-w-0 flex-1 rounded-lg border border-primary/50 bg-input px-2.5 text-ui-base font-semibold text-foreground outline-none focus:border-primary"
                />
                <button
                  type="submit"
                  onMouseDown={(e) => e.preventDefault()}
                  title={formatMsg(locale, 'common.confirm')}
                  className="flex size-7 shrink-0 items-center justify-center rounded-lg text-success hover:bg-hover"
                >
                  <Check className="size-4" />
                </button>
                <button
                  type="button"
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => setRenaming(false)}
                  title={formatMsg(locale, 'common.cancel')}
                  className="flex size-7 shrink-0 items-center justify-center rounded-lg text-foreground-subtle hover:bg-hover"
                >
                  <X className="size-4" />
                </button>
              </form>
            ) : (
              <h1
                onDoubleClick={() => {
                  if (!activeSessionId) return;
                  setRenameDraft(sessionTitle);
                  setHeaderMenu(null);
                  setRenaming(true);
                }}
                title={sessionTitle}
                className="max-w-md cursor-text truncate text-ui-base font-semibold text-foreground"
              >
                {sessionTitle}
              </h1>
            )}

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
                <div className="nex-popover-down absolute left-0 top-8 z-50 w-52 rounded-xl border border-border bg-popover p-1 shadow-md">
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
                          setHeaderMenu(null);
                          setRenaming(true);
                        }}
                        className="flex h-8 w-full items-center gap-2 rounded-lg px-2 text-left text-ui-sm text-foreground hover:bg-menu-hover"
                      >
                        <Pencil className="size-3.5 shrink-0 text-foreground-subtle" />
                        <span>{formatMsg(locale, 'taskList.rename')}</span>
                      </button>
                      <button
                        type="button"
                        data-testid="export-markdown-btn"
                        onClick={() => {
                          if (activeSessionId) void exportSessionMarkdown(activeSessionId);
                          setHeaderMenu(null);
                        }}
                        className="flex h-8 w-full items-center gap-2 rounded-lg px-2 text-left text-ui-sm text-foreground hover:bg-menu-hover"
                      >
                        <Download className="size-3.5 shrink-0 text-foreground-subtle" />
                        <span>{formatMsg(locale, 'chat.exportMarkdown')}</span>
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
                            ? formatMsg(locale, 'common.copied')
                            : formatMsg(locale, 'chat.copySessionId')}
                        </span>
                      </button>
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
            if (
              hiddenTurnCount > 0 &&
              !followingRef.current &&
              el.scrollTop <= 80 &&
              !prependScrollAnchorRef.current
            ) {
              loadEarlierTurns(false);
              return;
            }
            const scrollRect = el.getBoundingClientRect();
            const probeY = scrollRect.top + scrollRect.height * 0.25;
            let closestId = '';
            let closestDist = Number.POSITIVE_INFINITY;
            for (const it of visibleItems) {
              const node = turnRefs.current[it.id];
              if (!node) continue;
              const d = Math.abs(node.getBoundingClientRect().top - probeY);
              if (d < closestDist) {
                closestDist = d;
                closestId = it.id;
              }
            }
            if (closestId && closestId !== activeTurnId) {
              setActiveTurnId(closestId);
            }
          }}
          className={`absolute inset-0 overflow-y-auto px-4 sm:px-8 [overflow-anchor:none] transition-[padding] duration-200 ease-out ${statusTopClearanceClass} ${statusRightReserveClass}`}
        >
          {isDraft ? (
            <div className="flex h-full flex-col items-center justify-center pb-10">
              <div className="nex-draft-hero flex flex-col items-center">
                <h1 className="mb-7 text-center text-[26px] font-semibold tracking-tight text-foreground">
                  {formatMsg(locale, getGreetingKey(new Date().getHours()))}
                </h1>
              </div>

              {currentStream.pendingUIPrompt ? (
                <InlineQuestionCard key={currentStream.pendingUIPrompt.requestId} prompt={currentStream.pendingUIPrompt} />
              ) : (
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
              )}

              <div className="mt-5 flex flex-wrap items-center justify-center gap-2.5">
                {CHIPS.map((chip, index) => {
                  const Icon = chip.icon;
                  const chipLabel = formatMsg(locale, chip.labelKey);
                  const chipPrompt = formatMsg(locale, chip.promptKey);
                  return (
                    <button
                      key={chip.labelKey}
                      type="button"
                      style={{ '--nex-draft-prompt-waterfall-delay': `${index * 45}ms` } as CSSProperties}
                      onClick={() => {
                        setComposerText(chipPrompt);
                      }}
                      className="nex-draft-prompt-waterfall flex h-8 items-center gap-2 rounded-xl border border-border bg-card px-3.5 text-ui-sm text-foreground-subtle shadow-2xs transition-all duration-150 hover:-translate-y-px hover:border-border-hover hover:text-foreground hover:shadow-xs active:translate-y-0"
                    >
                      <Icon className="size-3.5 shrink-0" />
                      <span>{chipLabel}</span>
                    </button>
                  );
                })}
              </div>
            </div>
          ) : (
            <div ref={contentRef} className="mx-auto max-w-4xl space-y-5">
              {hiddenTurnCount > 0 && (
                <div
                  data-testid="load-earlier-turns-bar"
                  className="flex items-center justify-center gap-2 py-1"
                >
                  <button
                    type="button"
                    data-testid="load-earlier-turns-btn"
                    onClick={() => loadEarlierTurns(false)}
                    className="inline-flex h-7 items-center gap-1.5 rounded-full border border-border bg-card px-3.5 text-ui-xs font-medium text-foreground-subtle shadow-2xs transition-colors hover:border-border-hover hover:bg-hover hover:text-foreground"
                  >
                    <span>
                      {formatMsg(locale, 'chat.loadEarlierTurns', {
                        count: Math.min(hiddenTurnCount, LOAD_MORE_TURNS_STEP),
                      })}
                    </span>
                  </button>
                  {hiddenTurnCount > LOAD_MORE_TURNS_STEP && (
                    <button
                      type="button"
                      data-testid="show-all-turns-btn"
                      onClick={() => loadEarlierTurns(true)}
                      className="inline-flex h-7 items-center rounded-full px-2.5 text-ui-xs text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground-subtle"
                    >
                      {formatMsg(locale, 'chat.showAllTurns', { total: items.length })}
                    </button>
                  )}
                </div>
              )}
              {visibleItems.map((item, idx) => (
                <MemoTurnItem
                  key={item.id}
                  item={item}
                  isLast={hiddenTurnCount + idx === items.length - 1}
                  locale={locale}
                  registerRef={registerTurnRef}
                />
              ))}
              <ChatLoading
                loading={Boolean(currentStream.running && !currentStream.pendingApproval && !currentStream.pendingUIPrompt)}
                startedAt={currentStream.runStartedAt}
                locale={locale}
                className="pt-0.5"
              />
              {!currentStream.running && currentStream.lastError && (
                <div
                  data-testid="stream-error-banner"
                  className="flex items-start gap-2.5 rounded-xl border border-destructive/30 bg-destructive/10 px-3.5 py-2.5 text-ui-sm text-destructive"
                >
                  <AlertTriangle className="mt-0.5 size-4 shrink-0" />
                  <div className="min-w-0 space-y-0.5">
                    <div className="font-medium">{formatMsg(locale, 'chat.streamError.title')}</div>
                    <div className="break-all font-mono text-ui-xs opacity-90">{currentStream.lastError}</div>
                  </div>
                </div>
              )}
            </div>
          )}
        </div>

        {/* Left-edge turn tick navigator */}
        {!isDraft && tickLayout.entries.length > 0 && (
          <div
            data-testid="turn-tick-rail"
            data-tick-slot-height={tickLayout.slotHeightPx}
            style={{ maxHeight: 'calc(100% - 24px)', gap: `${tickLayout.gapPx}px` }}
            className="absolute left-1.5 top-1/2 z-20 hidden -translate-y-1/2 flex-col items-start lg:flex"
          >
            {tickLayout.entries.map(({ item, idx }) => {
              const isActive = activeTurnId ? activeTurnId === item.id : idx === items.length - 1;
              const rawPreview = item.role === 'user' ? cleanUserDisplayText(item.text) : item.text || item.thinking || '';
              const preview = rawPreview.replace(/\s+/g, ' ').trim().slice(0, 36);
              const label = `${item.role === 'user' ? formatMsg(locale, 'chat.you') : 'Nex'} #${idx + 1}${
                preview ? ` · ${preview}` : ''
              }`;
              return (
                <button
                  key={item.id}
                  type="button"
                  data-testid={`turn-tick-${idx}`}
                  title={label}
                  aria-label={label}
                  onClick={() => jumpToTurn(item.id)}
                  style={{ height: `${tickLayout.slotHeightPx}px` }}
                  className="group/tick relative flex cursor-pointer items-center px-1.5 focus:outline-none"
                >
                  <span
                    className={`block ${tickLayout.slotHeightPx <= 5 ? 'h-[1.5px]' : 'h-[2px]'} rounded-full transition-all duration-150 ${
                      isActive
                        ? 'w-4 bg-brand-blue'
                        : item.role === 'user'
                          ? 'w-3 bg-foreground/35 group-hover/tick:w-5 group-hover/tick:bg-foreground'
                          : 'w-2 bg-foreground/20 group-hover/tick:w-4 group-hover/tick:bg-foreground/80'
                    }`}
                  />
                  <span className="pointer-events-none absolute left-full top-1/2 z-30 ml-1.5 hidden max-w-[200px] -translate-y-1/2 truncate whitespace-nowrap rounded-md border border-border bg-popover px-2 py-0.5 text-ui-xs text-foreground shadow-md group-hover/tick:block">
                    {label}
                  </span>
                </button>
              );
            })}
          </div>
        )}

        {/* Floating scroll-to-bottom control */}
        {!isDraft && scrolledUp && (
          <button
            type="button"
            onClick={() => scrollToBottom('smooth')}
            title={formatMsg(locale, 'chat.scrollToBottom')}
            className="nex-popover-up absolute bottom-3 left-1/2 z-20 flex size-7 -translate-x-1/2 items-center justify-center rounded-full border border-border bg-card text-foreground-subtle shadow-sm transition-all hover:scale-105 hover:text-foreground active:scale-95"
          >
            <ChevronDown className="size-4" />
          </button>
        )}

        <ConversationStatusPanel mode={statusPanelMode} onModeChange={setStatusPanelMode} />
      </div>

      {/* Sticky bottom Question Card or Composer (draft mode renders its own centered tray inside the canvas) */}
      {!isDraft && (
        <div className={`shrink-0 px-4 pb-3.5 pt-2 sm:px-8 transition-[padding] duration-200 ease-out ${statusRightReserveClass}`}>
          {currentStream.pendingUIPrompt ? (
            <InlineQuestionCard key={currentStream.pendingUIPrompt.requestId} prompt={currentStream.pendingUIPrompt} />
          ) : (
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
          )}
        </div>
      )}
      <AboutModal open={aboutOpen} onClose={() => setAboutOpen(false)} />
    </section>
  );
}
