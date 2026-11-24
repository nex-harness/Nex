import { formatMsg, type LocaleCode } from '../i18n/messages';
import {
  ArrowRight,
  Check,
  ChevronDown,
  ChevronRight,
  Code2,
  Copy,
  FileText,
  Maximize2,
  Minimize2,
  Search,
  Sparkles,
  Wrench,
  X,
} from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { createPortal } from 'react-dom';
import { apiFetchText, formatCompactTokens } from '../core/protocol';
import { TurnTelemetry } from '../core/types';
import { MarkdownRenderer } from './MarkdownRenderer';

// ---------- Shared Raw Trace Schema ----------

export interface TraceContentBlock {
  type?: string;
  text?: string;
  thinking?: string;
  tool_call_id?: string;
  tool_name?: string;
  arguments?: unknown;
  mime_type?: string;
  image_url?: string;
}

export interface TraceMessage {
  role?: string;
  content?: TraceContentBlock[];
  tool_call_id?: string;
  tool_name?: string;
  is_error?: boolean;
}

export interface TraceSystemSection {
  category?: string;
  name?: string;
  content?: string;
}

export interface TraceToolDef {
  name: string;
  description?: string;
  category?: string;
  input_schema?: Record<string, unknown>;
}

export interface RawTraceSnapshot {
  model?: string;
  system_prompt?: string;
  system_sections?: TraceSystemSection[];
  tools?: TraceToolDef[];
  messages?: TraceMessage[];
  response?: TraceMessage;
  usage?: {
    input_tokens?: number;
    output_tokens?: number;
    reasoning_tokens?: number;
    cache_read_tokens?: number;
    cache_write_tokens?: number;
  };
}

type VisualRole = 'system' | 'user' | 'assistant' | 'thinking' | 'tool-call' | 'tool-result';

const ROLE_STYLES: Record<VisualRole, { text: string; pill: string; label: string }> = {
  system: {
    text: 'text-foreground-subtle',
    pill: 'bg-surface text-foreground-subtle border-border',
    label: 'SYSTEM',
  },
  user: {
    text: 'text-blue-600 dark:text-blue-400',
    pill: 'bg-blue-500/10 text-blue-600 dark:text-blue-400 border-blue-500/20',
    label: 'USER',
  },
  assistant: {
    text: 'text-emerald-600 dark:text-emerald-400',
    pill: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-500/20',
    label: 'ASSISTANT',
  },
  thinking: {
    text: 'text-violet-600 dark:text-violet-400',
    pill: 'bg-violet-500/10 text-violet-600 dark:text-violet-400 border-violet-500/20',
    label: 'THINKING',
  },
  'tool-call': {
    text: 'text-amber-600 dark:text-amber-400',
    pill: 'bg-amber-500/10 text-amber-600 dark:text-amber-400 border-amber-500/20',
    label: 'TOOL CALL',
  },
  'tool-result': {
    text: 'text-cyan-600 dark:text-cyan-400',
    pill: 'bg-cyan-500/10 text-cyan-600 dark:text-cyan-400 border-cyan-500/20',
    label: 'TOOL',
  },
};

function formatDurationMs(ms?: number): string {
  if (!ms || ms <= 0) return '—';
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(2)}s`;
}

function prettyJSON(val: unknown): string {
  if (typeof val === 'string') {
    const trimmed = val.trim();
    if ((trimmed.startsWith('{') && trimmed.endsWith('}')) || (trimmed.startsWith('[') && trimmed.endsWith(']'))) {
      try {
        return JSON.stringify(JSON.parse(trimmed), null, 2);
      } catch {
        return val;
      }
    }
    return val;
  }
  try {
    return JSON.stringify(val ?? {}, null, 2);
  } catch {
    return String(val);
  }
}

function previewLine(text: string, maxLen = 120): string {
  const oneLine = text.replace(/\s+/g, ' ').trim();
  if (!oneLine) return '—';
  return oneLine.length > maxLen ? `${oneLine.slice(0, maxLen)}…` : oneLine;
}

function CopyIconButton({
  text,
  locale,
  className = '',
}: {
  text: string;
  locale: LocaleCode;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation();
        void navigator.clipboard?.writeText(text);
        setCopied(true);
        window.setTimeout(() => setCopied(false), 1400);
      }}
      title={copied ? (formatMsg(locale, 'common.copied')) : formatMsg(locale, 'common.copy')}
      className={`inline-flex size-6 shrink-0 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground ${className}`}
    >
      {copied ? <Check className="size-3.5 text-success" /> : <Copy className="size-3.5" />}
    </button>
  );
}

// ---------- Structured Tool List Renderer (NDJSON Tool Definitions) ----------

function parseToolDefs(raw: string): TraceToolDef[] | null {
  const trimmed = raw.trim();
  if (!trimmed) return [];
  try {
    const parsed = JSON.parse(trimmed);
    if (Array.isArray(parsed)) {
      return parsed.filter((x) => x && typeof x === 'object' && typeof x.name === 'string') as TraceToolDef[];
    }
    if (parsed && typeof parsed === 'object' && typeof (parsed as TraceToolDef).name === 'string') {
      return [parsed as TraceToolDef];
    }
  } catch {
    // Try NDJSON (one tool JSON per line, as stored by pkg/agent/telemetry.go)
  }
  const lines = trimmed.split('\n').filter((l) => l.trim().length > 0);
  const defs: TraceToolDef[] = [];
  for (const line of lines) {
    try {
      const obj = JSON.parse(line);
      if (obj && typeof obj === 'object' && typeof obj.name === 'string') {
        defs.push(obj as TraceToolDef);
      } else {
        return null;
      }
    } catch {
      return null;
    }
  }
  return defs.length > 0 ? defs : null;
}

function ToolDefinitionCard({
  tool,
  locale,
  defaultOpen = false,
}: {
  tool: TraceToolDef;
  locale: LocaleCode;
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const [showSchema, setShowSchema] = useState(false);

  const schema = tool.input_schema || {};
  const propsObj = (schema.properties && typeof schema.properties === 'object'
    ? schema.properties
    : {}) as Record<string, Record<string, unknown>>;
  const requiredSet = new Set(Array.isArray(schema.required) ? schema.required.map(String) : []);
  const propEntries = Object.entries(propsObj);

  return (
    <div className="overflow-hidden rounded-xl border border-border bg-card transition-colors hover:border-border-hover">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2.5 px-3.5 py-2.5 text-left transition-colors hover:bg-hover/40"
      >
        <ChevronRight
          className={`size-3.5 shrink-0 text-foreground-subtlest transition-transform ${open ? 'rotate-90' : ''}`}
        />
        <Wrench className="size-3.5 shrink-0 text-amber-500/80" />
        <span className="shrink-0 font-mono text-ui-sm font-semibold text-foreground">{tool.name}</span>
        {tool.description && (
          <span className="min-w-0 flex-1 truncate text-ui-xs text-foreground-subtle">
            {previewLine(tool.description, 100)}
          </span>
        )}
        <span className="shrink-0 rounded-full border border-border bg-surface px-2 py-0.5 font-mono text-[11px] tabular-nums text-foreground-subtlest">
          {propEntries.length} {formatMsg(locale, 'trajectory.params')}
        </span>
      </button>

      {open && (
        <div className="space-y-3 border-t border-border/70 bg-surface/35 px-4 py-3">
          {tool.description && (
            <p className="whitespace-pre-wrap text-ui-sm leading-relaxed text-foreground">{tool.description}</p>
          )}

          {propEntries.length > 0 && (
            <div className="overflow-hidden rounded-lg border border-border bg-card">
              <div className="grid grid-cols-[minmax(120px,auto)_84px_minmax(0,1fr)] border-b border-border bg-surface/60 px-3 py-1.5 text-[11px] font-medium uppercase tracking-wide text-foreground-subtlest">
                <span>{formatMsg(locale, 'trajectory.parameter')}</span>
                <span>{formatMsg(locale, 'trajectory.type')}</span>
                <span>{formatMsg(locale, 'common.description')}</span>
              </div>
              <div className="divide-y divide-border/60">
                {propEntries.map(([pName, pDef]) => {
                  const isReq = requiredSet.has(pName);
                  const typeStr = Array.isArray(pDef?.type)
                    ? pDef.type.join(' | ')
                    : String(pDef?.type || (pDef?.enum ? 'enum' : 'any'));
                  const desc = typeof pDef?.description === 'string' ? pDef.description : '';
                  const enumVals = Array.isArray(pDef?.enum) ? pDef.enum.map(String) : null;
                  return (
                    <div
                      key={pName}
                      className="grid grid-cols-[minmax(120px,auto)_84px_minmax(0,1fr)] items-start gap-2 px-3 py-2 text-ui-xs"
                    >
                      <span className="flex flex-wrap items-center gap-1.5 font-mono font-medium text-foreground">
                        <span>{pName}</span>
                        {isReq && (
                          <span className="rounded bg-destructive/10 px-1 py-px text-[10px] font-semibold text-destructive">
                            {formatMsg(locale, 'trajectory.req')}
                          </span>
                        )}
                      </span>
                      <span className="font-mono text-foreground-subtle">{typeStr}</span>
                      <div className="min-w-0 space-y-1 text-foreground-subtle">
                        {desc && <p className="whitespace-pre-wrap break-words leading-relaxed">{desc}</p>}
                        {enumVals && enumVals.length > 0 && (
                          <div className="flex flex-wrap gap-1">
                            {enumVals.map((ev) => (
                              <code
                                key={ev}
                                className="rounded bg-surface px-1.5 py-0.5 font-mono text-[11px] text-foreground"
                              >
                                {ev}
                              </code>
                            ))}
                          </div>
                        )}
                      </div>
                    </div>
                  );
                })}
              </div>
            </div>
          )}

          <div className="flex items-center justify-between">
            <button
              type="button"
              onClick={() => setShowSchema((v) => !v)}
              className="inline-flex items-center gap-1 text-ui-xs text-foreground-subtlest transition-colors hover:text-foreground"
            >
              <Code2 className="size-3.5" />
              <span>
                {showSchema
                  ? formatMsg(locale, 'trajectory.hideRawSchema')
                  : formatMsg(locale, 'trajectory.viewRawSchema')}
              </span>
            </button>
            <CopyIconButton text={prettyJSON(tool)} locale={locale} />
          </div>

          {showSchema && (
            <pre className="max-h-64 overflow-auto rounded-lg border border-border bg-surface p-3 font-mono text-ui-xs leading-relaxed text-foreground">
              {prettyJSON(schema)}
            </pre>
          )}
        </div>
      )}
    </div>
  );
}

// ---------- Expandable Trajectory / Message Row (Nex ModelTrajectoryExpandableMessage style) ----------

interface TrajectoryRowEntry {
  id: string;
  role: VisualRole;
  title?: string;
  toolName?: string;
  toolCallId?: string;
  direction?: 'input' | 'output';
  isError?: boolean;
  body: string;
  isCode?: boolean;
  isMarkdown?: boolean;
}

function ExpandableRow({
  entry,
  locale,
  expanded,
  onToggle,
  isAlt,
}: {
  entry: TrajectoryRowEntry;
  locale: LocaleCode;
  expanded: boolean;
  onToggle: () => void;
  isAlt?: boolean;
}) {
  const style = ROLE_STYLES[entry.role];
  const roleLabel = style.label;
  const dirLabel =
    entry.direction === 'input'
      ? formatMsg(locale, 'trajectory.input')
      : entry.direction === 'output'
        ? formatMsg(locale, 'trajectory.output')
        : null;

  return (
    <div className={`border-t border-border/60 first:border-t-0 ${isAlt ? 'bg-surface/25' : ''}`}>
      <div className="flex min-h-9 items-center gap-2 px-3 py-1.5">
        <button
          type="button"
          onClick={onToggle}
          className="flex min-w-0 flex-1 items-center gap-2.5 text-left transition-colors hover:text-foreground"
        >
          <span className={`w-[78px] shrink-0 font-mono text-[11px] font-semibold uppercase tracking-wide ${style.text}`}>
            {roleLabel}
          </span>

          {dirLabel && (
            <span
              className={`inline-flex shrink-0 items-center gap-1 rounded border px-1.5 py-px font-mono text-[10px] font-medium uppercase ${
                entry.isError
                  ? 'border-destructive/30 bg-destructive/10 text-destructive'
                  : 'border-border bg-surface text-foreground-subtle'
              }`}
            >
              <span>{dirLabel}</span>
              <ArrowRight className="size-2.5 opacity-70" />
              <span className="text-foreground">{entry.toolName || 'tool'}</span>
            </span>
          )}

          {entry.title && !dirLabel && (
            <span className="shrink-0 rounded border border-border bg-surface px-1.5 py-px font-mono text-[11px] text-foreground-subtle">
              {entry.title}
            </span>
          )}

          <span className="min-w-0 flex-1 truncate text-ui-xs text-foreground-subtle">
            {previewLine(entry.body, 110)}
          </span>
        </button>

        <div className="flex shrink-0 items-center gap-1">
          {entry.toolCallId && (
            <span
              title={entry.toolCallId}
              className="hidden max-w-[110px] truncate rounded border border-border/70 bg-surface/60 px-1.5 py-px font-mono text-[10px] text-foreground-subtlest sm:inline-block"
            >
              {entry.toolCallId}
            </span>
          )}
          <CopyIconButton text={entry.body} locale={locale} />
          <button
            type="button"
            onClick={onToggle}
            className="flex size-6 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
          >
            <ChevronDown className={`size-3.5 transition-transform ${expanded ? 'rotate-180' : ''}`} />
          </button>
        </div>
      </div>

      {expanded && (
        <div className="border-t border-border/40 bg-surface/35 px-3 py-2.5 pl-[100px]">
          {entry.isCode ? (
            <pre className="max-h-80 overflow-auto rounded-lg border border-border bg-card p-3 font-mono text-ui-xs leading-relaxed text-foreground">
              {entry.body || '—'}
            </pre>
          ) : entry.isMarkdown ? (
            <div className="max-h-96 overflow-auto rounded-lg border border-border/70 bg-card px-3.5 py-2.5 text-ui-sm">
              <MarkdownRenderer content={entry.body} streaming={false} />
            </div>
          ) : (
            <div className="max-h-80 overflow-auto whitespace-pre-wrap break-words rounded-lg border border-border/70 bg-card px-3.5 py-2.5 text-ui-sm leading-relaxed text-foreground">
              {entry.body || '—'}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function messagesToRows(messages: TraceMessage[], keyPrefix: string): TrajectoryRowEntry[] {
  const rows: TrajectoryRowEntry[] = [];
  messages.forEach((msg, mIdx) => {
    const role = (msg.role || 'user').toLowerCase();
    const blocks = Array.isArray(msg.content) ? msg.content : [];

    if (role === 'tool' || role === 'tool_result') {
      const txt = blocks
        .map((b) => b.text || '')
        .filter(Boolean)
        .join('\n');
      rows.push({
        id: `${keyPrefix}:m${mIdx}:tr`,
        role: 'tool-result',
        toolName: msg.tool_name,
        toolCallId: msg.tool_call_id,
        direction: 'output',
        isError: Boolean(msg.is_error),
        body: prettyJSON(txt),
        isCode: true,
      });
      return;
    }

    if (blocks.length === 0) {
      return;
    }

    blocks.forEach((b, bIdx) => {
      const bType = (b.type || 'text').toLowerCase();
      if (bType === 'thinking') {
        if (!b.thinking) return;
        rows.push({
          id: `${keyPrefix}:m${mIdx}:b${bIdx}:think`,
          role: 'thinking',
          body: b.thinking,
        });
      } else if (bType === 'tool_call') {
        rows.push({
          id: `${keyPrefix}:m${mIdx}:b${bIdx}:tc`,
          role: 'tool-call',
          toolName: b.tool_name || 'tool',
          toolCallId: b.tool_call_id,
          direction: 'input',
          body: prettyJSON(b.arguments),
          isCode: true,
        });
      } else if (bType === 'image') {
        rows.push({
          id: `${keyPrefix}:m${mIdx}:b${bIdx}:img`,
          role: role === 'assistant' ? 'assistant' : 'user',
          title: b.mime_type || 'image',
          body: `[image · ${b.mime_type || 'unknown'}]`,
        });
      } else if (b.text) {
        rows.push({
          id: `${keyPrefix}:m${mIdx}:b${bIdx}:txt`,
          role: role === 'assistant' ? 'assistant' : role === 'system' ? 'system' : 'user',
          body: b.text,
          isMarkdown: role === 'assistant',
        });
      }
    });
  });
  return rows;
}

// ---------- 1. Structured Context Category Inspector Modal ----------

export function ContextInspectorModal({
  category,
  title,
  colorVar,
  tokens,
  rawContent,
  locale,
  onClose,
}: {
  category: string;
  title: string;
  colorVar?: string;
  tokens?: number;
  rawContent: string;
  locale: LocaleCode;
  onClose: () => void;
}) {
  const [viewMode, setViewMode] = useState<'formatted' | 'raw'>('formatted');
  const [search, setSearch] = useState('');
  const [expandedRows, setExpandedRows] = useState<Record<string, boolean>>({});

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        onClose();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const parsedTools = useMemo(() => {
    if (category !== 'system_tools' && category !== 'mcp_tools') return null;
    return parseToolDefs(rawContent);
  }, [category, rawContent]);

  const parsedMessages = useMemo(() => {
    if (category !== 'messages') return null;
    try {
      const parsed = JSON.parse(rawContent);
      if (Array.isArray(parsed)) return parsed as TraceMessage[];
      if (parsed && typeof parsed === 'object' && Array.isArray((parsed as RawTraceSnapshot).messages)) {
        return (parsed as RawTraceSnapshot).messages || [];
      }
    } catch {
      return null;
    }
    return null;
  }, [category, rawContent]);

  const messageRows = useMemo(
    () => (parsedMessages ? messagesToRows(parsedMessages, 'ctx-msg') : []),
    [parsedMessages],
  );

  const filteredTools = useMemo(() => {
    if (!parsedTools) return null;
    const q = search.trim().toLowerCase();
    if (!q) return parsedTools;
    return parsedTools.filter(
      (t) =>
        t.name.toLowerCase().includes(q) ||
        (t.description || '').toLowerCase().includes(q) ||
        JSON.stringify(t.input_schema || '').toLowerCase().includes(q),
    );
  }, [parsedTools, search]);

  const filteredMessageRows = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return messageRows;
    return messageRows.filter(
      (r) =>
        r.role.includes(q) ||
        (r.toolName || '').toLowerCase().includes(q) ||
        r.body.toLowerCase().includes(q),
    );
  }, [messageRows, search]);

  const isMarkdownCategory =
    category === 'system_prompt' || category === 'skills' || category === 'meta_context';

  const prettyRaw = useMemo(() => {
    const trimmed = rawContent.trim();
    if (parsedTools) return JSON.stringify(parsedTools, null, 2);
    if (parsedMessages) return JSON.stringify(parsedMessages, null, 2);
    if ((trimmed.startsWith('{') && trimmed.endsWith('}')) || (trimmed.startsWith('[') && trimmed.endsWith(']'))) {
      try {
        return JSON.stringify(JSON.parse(trimmed), null, 2);
      } catch {
        return rawContent;
      }
    }
    return rawContent;
  }, [rawContent, parsedTools, parsedMessages]);

  if (typeof document === 'undefined') return null;

  return createPortal(
    <div
      data-testid="context-inspector-modal"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
      className="nex-dialog-overlay fixed inset-0 z-50 flex items-center justify-center bg-black/55 p-4 backdrop-blur-[2px] sm:p-6"
    >
      <div className="nex-dialog-content flex max-h-[86vh] w-full max-w-4xl flex-col overflow-hidden rounded-2xl border border-border bg-popover shadow-2xl">
        {/* Header */}
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-4 py-3">
          <div className="flex min-w-0 items-center gap-2.5">
            {colorVar && <span className="size-2.5 shrink-0 rounded-full" style={{ backgroundColor: colorVar }} />}
            <h3 className="truncate text-ui-base font-semibold text-foreground">{title}</h3>
            {typeof tokens === 'number' && tokens > 0 && (
              <span className="shrink-0 rounded-full border border-border bg-surface px-2 py-0.5 font-mono text-[11px] tabular-nums text-foreground-subtle">
                {formatCompactTokens(tokens)} tokens
              </span>
            )}
            {parsedTools && (
              <span className="shrink-0 rounded-full border border-border bg-surface px-2 py-0.5 font-mono text-[11px] tabular-nums text-foreground-subtle">
                {parsedTools.length} {formatMsg(locale, 'trajectory.tools')}
              </span>
            )}
            {parsedMessages && (
              <span className="shrink-0 rounded-full border border-border bg-surface px-2 py-0.5 font-mono text-[11px] tabular-nums text-foreground-subtle">
                {parsedMessages.length} {formatMsg(locale, 'trajectory.messages')}
              </span>
            )}
          </div>

          <div className="flex items-center gap-1.5">
            <div className="inline-flex rounded-lg border border-border bg-surface p-0.5 text-ui-xs">
              <button
                type="button"
                onClick={() => setViewMode('formatted')}
                className={`rounded-md px-2 py-0.5 font-medium transition-colors ${
                  viewMode === 'formatted'
                    ? 'bg-popover text-foreground shadow-xs'
                    : 'text-foreground-subtle hover:text-foreground'
                }`}
              >
                {formatMsg(locale, 'trajectory.formatted')}
              </button>
              <button
                type="button"
                onClick={() => setViewMode('raw')}
                className={`rounded-md px-2 py-0.5 font-medium transition-colors ${
                  viewMode === 'raw'
                    ? 'bg-popover text-foreground shadow-xs'
                    : 'text-foreground-subtle hover:text-foreground'
                }`}
              >
                {formatMsg(locale, 'trajectory.raw')}
              </button>
            </div>

            <CopyIconButton text={prettyRaw} locale={locale} className="size-7 rounded-lg" />

            <button
              type="button"
              onClick={onClose}
              aria-label={formatMsg(locale, 'common.close')}
              className="flex size-7 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
            >
              <X className="size-4" />
            </button>
          </div>
        </div>

        {/* Search Bar for tools / messages */}
        {viewMode === 'formatted' && (parsedTools || parsedMessages) && (
          <div className="border-b border-border/70 bg-surface/40 px-4 py-2">
            <div className="relative flex items-center">
              <Search className="pointer-events-none absolute left-2.5 size-3.5 text-foreground-subtlest" />
              <input
                type="text"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder={
                  parsedTools
                    ? formatMsg(locale, 'trajectory.filterToolsByNameDescription')
                    : formatMsg(locale, 'trajectory.searchMessages')
                }
                className="h-8 w-full rounded-lg border border-border bg-input pl-8 pr-3 text-ui-sm text-foreground placeholder-foreground-subtlest outline-none focus:border-border-hover"
              />
            </div>
          </div>
        )}

        {/* Body */}
        <div className="flex-1 overflow-auto p-4">
          {viewMode === 'raw' ? (
            <pre className="whitespace-pre-wrap break-words rounded-xl border border-border bg-surface p-4 font-mono text-ui-xs leading-relaxed text-foreground">
              {prettyRaw || '—'}
            </pre>
          ) : filteredTools ? (
            <div className="space-y-2">
              {filteredTools.length === 0 ? (
                <div className="py-10 text-center text-ui-sm text-foreground-subtlest">
                  {formatMsg(locale, 'trajectory.noMatchingTools')}
                </div>
              ) : (
                filteredTools.map((td, idx) => (
                  <ToolDefinitionCard
                    key={`${td.name}-${idx}`}
                    tool={td}
                    locale={locale}
                    defaultOpen={filteredTools.length <= 2}
                  />
                ))
              )}
            </div>
          ) : parsedMessages ? (
            <div className="overflow-hidden rounded-xl border border-border bg-card">
              {filteredMessageRows.length === 0 ? (
                <div className="py-10 text-center text-ui-sm text-foreground-subtlest">
                  {formatMsg(locale, 'trajectory.noMatchingMessages')}
                </div>
              ) : (
                filteredMessageRows.map((row, idx) => {
                  const isOpen = expandedRows[row.id] ?? idx >= filteredMessageRows.length - 3;
                  return (
                    <ExpandableRow
                      key={row.id}
                      entry={row}
                      locale={locale}
                      expanded={isOpen}
                      isAlt={idx % 2 === 1}
                      onToggle={() => setExpandedRows((prev) => ({ ...prev, [row.id]: !isOpen }))}
                    />
                  );
                })
              )}
            </div>
          ) : isMarkdownCategory ? (
            <div className="rounded-xl border border-border bg-card px-5 py-4">
              <MarkdownRenderer content={rawContent} streaming={false} />
            </div>
          ) : (
            <pre className="whitespace-pre-wrap break-words rounded-xl border border-border bg-surface p-4 font-mono text-ui-xs leading-relaxed text-foreground">
              {prettyRaw || '—'}
            </pre>
          )}
        </div>
      </div>
    </div>,
    document.body,
  );
}

// ---------- 2. Nex-Aligned Model Trajectory Modal ----------

interface LoadedCallRecord {
  telemetry: TurnTelemetry;
  trace: RawTraceSnapshot | null;
  rawText: string;
  error?: string;
}

function sourceLabel(trigger: string | undefined, locale: LocaleCode): string {
  switch (trigger) {
    case 'subagent':
      return formatMsg(locale, 'tool.subagent');
    case 'cron':
      return formatMsg(locale, 'trajectory.automation');
    default:
      return formatMsg(locale, 'trajectory.main');
  }
}

export function ModelTrajectoryModal({
  telemetries,
  sessionTitle,
  locale,
  onClose,
}: {
  telemetries: TurnTelemetry[];
  sessionTitle?: string;
  locale: LocaleCode;
  onClose: () => void;
}) {
  const [records, setRecords] = useState<LoadedCallRecord[]>([]);
  const [loading, setLoading] = useState(true);
  const [search, setSearch] = useState('');
  const [roleFilter, setRoleFilter] = useState<VisualRole | 'all'>('all');
  const [rawView, setRawView] = useState(false);
  const [globalExpanded, setGlobalExpanded] = useState(true);
  const [expandVersion, setExpandVersion] = useState(0);
  const [rowOverrides, setRowOverrides] = useState<Record<string, { open: boolean; version: number }>>({});
  const [showFullInputCalls, setShowFullInputCalls] = useState<Record<number, boolean>>({});

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        onClose();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    const list = telemetries.filter((t) => Boolean(t.raw_trace_blob_hash));
    Promise.all(
      list.map(async (tel): Promise<LoadedCallRecord> => {
        try {
          const rawText = await apiFetchText(`/api/v1/blobs/${tel.raw_trace_blob_hash}`);
          const parsed = JSON.parse(rawText) as RawTraceSnapshot;
          return { telemetry: tel, trace: parsed, rawText };
        } catch (err) {
          return { telemetry: tel, trace: null, rawText: '', error: (err as Error).message };
        }
      }),
    ).then((loaded) => {
      if (!cancelled) {
        setRecords(loaded);
        setLoading(false);
      }
    });
    return () => {
      cancelled = true;
    };
  }, [telemetries]);

  const aggregate = useMemo(() => {
    let inTok = 0;
    let outTok = 0;
    let reasonTok = 0;
    let cacheRead = 0;
    let cost = 0;
    for (const r of records) {
      inTok += r.telemetry.input_tokens || 0;
      outTok += r.telemetry.output_tokens || 0;
      reasonTok += r.telemetry.reasoning_tokens || 0;
      cacheRead += r.telemetry.cache_read_tokens || 0;
      cost += r.telemetry.estimated_cost_usd || 0;
    }
    const totalIn = inTok + cacheRead;
    const cachePct = totalIn > 0 ? (cacheRead * 100) / totalIn : 0;
    return { inTok, outTok, reasonTok, cachePct, cost };
  }, [records]);

  const isRowOpen = (key: string, defaultOpen: boolean): boolean => {
    const ov = rowOverrides[key];
    if (ov && ov.version === expandVersion) return ov.open;
    return expandVersion > 0 ? globalExpanded : defaultOpen;
  };

  const toggleRow = (key: string, current: boolean) => {
    setRowOverrides((prev) => ({ ...prev, [key]: { open: !current, version: expandVersion } }));
  };

  const handleToggleAll = () => {
    const next = !globalExpanded;
    setGlobalExpanded(next);
    setExpandVersion((v) => v + 1);
  };

  const q = search.trim().toLowerCase();
  const matchesFilter = (row: TrajectoryRowEntry): boolean => {
    if (roleFilter !== 'all') {
      if (roleFilter === 'tool-call' && row.role !== 'tool-call' && row.role !== 'tool-result') return false;
      if (roleFilter !== 'tool-call' && row.role !== roleFilter) return false;
    }
    if (!q) return true;
    return (
      row.body.toLowerCase().includes(q) ||
      (row.toolName || '').toLowerCase().includes(q) ||
      (row.title || '').toLowerCase().includes(q) ||
      row.role.toLowerCase().includes(q)
    );
  };

  if (typeof document === 'undefined') return null;

  return createPortal(
    <div
      data-testid="model-trajectory-modal"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
      className="nex-dialog-overlay fixed inset-0 z-50 flex items-center justify-center bg-black/55 p-3 backdrop-blur-[2px] sm:p-5"
    >
      <div className="nex-dialog-content flex h-[88vh] w-full max-w-5xl flex-col overflow-hidden rounded-2xl border border-border bg-background shadow-2xl">
        {/* Nex ModelTrajectoryPane-style Header */}
        <header className="shrink-0 border-b border-border bg-popover/95 px-4 py-3">
          <div className="flex flex-wrap items-center gap-2">
            <div className="flex min-w-0 flex-1 items-center gap-2">
              <Sparkles className="size-4 shrink-0 text-brand-blue" />
              <h3 className="truncate text-ui-base font-semibold text-foreground">
                {formatMsg(locale, 'trajectory.modelTrajectory')}
              </h3>
              {sessionTitle && (
                <span className="hidden truncate text-ui-xs text-foreground-subtlest sm:inline">
                  · {sessionTitle}
                </span>
              )}
            </div>

            <div className="flex items-center gap-1">
              <button
                type="button"
                onClick={handleToggleAll}
                title={
                  globalExpanded
                    ? formatMsg(locale, 'trajectory.collapseAll')
                    : formatMsg(locale, 'trajectory.expandAll')
                }
                className="inline-flex h-7 items-center gap-1 rounded-lg border border-border bg-surface px-2 text-ui-xs text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
              >
                {globalExpanded ? <Minimize2 className="size-3.5" /> : <Maximize2 className="size-3.5" />}
                <span className="hidden sm:inline">
                  {globalExpanded
                    ? formatMsg(locale, 'trajectory.collapse')
                    : formatMsg(locale, 'trajectory.expand')}
                </span>
              </button>

              <button
                type="button"
                onClick={() => setRawView((v) => !v)}
                className={`inline-flex h-7 items-center gap-1 rounded-lg border px-2 text-ui-xs transition-colors ${
                  rawView
                    ? 'border-brand-blue/50 bg-brand-blue/10 text-brand-blue'
                    : 'border-border bg-surface text-foreground-subtle hover:bg-hover hover:text-foreground'
                }`}
              >
                <Code2 className="size-3.5" />
                <span>JSON</span>
              </button>

              <button
                type="button"
                onClick={onClose}
                aria-label={formatMsg(locale, 'common.close')}
                className="flex size-7 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
              >
                <X className="size-4" />
              </button>
            </div>
          </div>

          {/* Summary Telemetry + Search & Role Filter Strip */}
          <div className="mt-2.5 flex flex-wrap items-center justify-between gap-2">
            <div className="flex flex-wrap items-center gap-1.5 font-mono text-[11px] tabular-nums text-foreground-subtle">
              <span className="rounded-md border border-border bg-surface px-2 py-0.5 text-foreground">
                {records.length} {formatMsg(locale, 'trajectory.calls')}
              </span>
              <span className="rounded-md border border-border bg-surface px-2 py-0.5">
                IN {formatCompactTokens(aggregate.inTok)}
              </span>
              <span className="rounded-md border border-border bg-surface px-2 py-0.5">
                OUT {formatCompactTokens(aggregate.outTok)}
              </span>
              {aggregate.reasonTok > 0 && (
                <span className="rounded-md border border-violet-500/25 bg-violet-500/10 px-2 py-0.5 text-violet-600 dark:text-violet-400">
                  REASON {formatCompactTokens(aggregate.reasonTok)}
                </span>
              )}
              {aggregate.cachePct > 0 && (
                <span className="rounded-md border border-emerald-500/25 bg-emerald-500/10 px-2 py-0.5 text-emerald-600 dark:text-emerald-400">
                  CACHE {aggregate.cachePct.toFixed(0)}%
                </span>
              )}
              <span className="rounded-md border border-border bg-surface px-2 py-0.5">
                ${aggregate.cost.toFixed(4)}
              </span>
            </div>

            {!rawView && (
              <div className="flex flex-wrap items-center gap-1.5">
                <div className="relative flex items-center">
                  <Search className="pointer-events-none absolute left-2 size-3.5 text-foreground-subtlest" />
                  <input
                    type="text"
                    value={search}
                    onChange={(e) => setSearch(e.target.value)}
                    placeholder={formatMsg(locale, 'trajectory.searchTrajectory')}
                    className="h-7 w-44 rounded-lg border border-border bg-input pl-7 pr-2.5 text-ui-xs text-foreground placeholder-foreground-subtlest outline-none focus:border-border-hover sm:w-52"
                  />
                </div>
                {(
                  [
                    ['all', formatMsg(locale, 'common.all')],
                    ['system', 'SYS'],
                    ['user', 'USER'],
                    ['thinking', 'THINK'],
                    ['assistant', 'AST'],
                    ['tool-call', 'TOOL'],
                  ] as const
                ).map(([key, label]) => (
                  <button
                    key={key}
                    type="button"
                    onClick={() => setRoleFilter(key)}
                    className={`h-7 rounded-lg border px-2 font-mono text-[11px] transition-colors ${
                      roleFilter === key
                        ? 'border-brand-blue/50 bg-brand-blue/10 font-medium text-brand-blue'
                        : 'border-border bg-surface text-foreground-subtle hover:text-foreground'
                    }`}
                  >
                    {label}
                  </button>
                ))}
              </div>
            )}
          </div>
        </header>

        {/* Timeline Body */}
        <div className="flex-1 overflow-auto">
          {loading ? (
            <div className="flex h-48 items-center justify-center text-ui-sm text-foreground-subtle">
              {formatMsg(locale, 'trajectory.loadingModelTrajectory')}
            </div>
          ) : records.length === 0 ? (
            <div className="flex h-48 flex-col items-center justify-center gap-1.5 text-ui-sm text-foreground-subtlest">
              <FileText className="size-6 opacity-50" />
              <span>{formatMsg(locale, 'trajectory.noTrajectoryRecordsAvailable')}</span>
            </div>
          ) : rawView ? (
            <div className="space-y-4 p-4">
              {records.map((rec, idx) => (
                <div key={idx} className="overflow-hidden rounded-xl border border-border bg-card">
                  <div className="flex items-center justify-between border-b border-border bg-surface/60 px-3.5 py-2">
                    <span className="font-mono text-ui-xs font-semibold text-foreground">
                      #{String(idx + 1).padStart(2, '0')} · {rec.telemetry.model_id}
                    </span>
                    <CopyIconButton text={prettyJSON(rec.trace || rec.rawText)} locale={locale} />
                  </div>
                  <pre className="max-h-[420px] overflow-auto p-3.5 font-mono text-ui-xs leading-relaxed text-foreground">
                    {prettyJSON(rec.trace || rec.rawText)}
                  </pre>
                </div>
              ))}
            </div>
          ) : (
            <div className="divide-y divide-border">
              {records.map((rec, idx) => {
                const trace = rec.trace;
                const prevMsgCount = idx > 0 ? records[idx - 1].trace?.messages?.length || 0 : 0;
                const allInputMsgs = trace?.messages || [];
                const showFull = Boolean(showFullInputCalls[idx]) || idx === 0 || allInputMsgs.length <= prevMsgCount;
                const visibleInputMsgs = showFull
                  ? allInputMsgs
                  : allInputMsgs.slice(prevMsgCount).filter((m) => (m.role || '').toLowerCase() !== 'assistant');
                const hiddenPriorCount = showFull ? 0 : allInputMsgs.length - visibleInputMsgs.length;

                // System sections & tools only shown on Call #1 (or when expanded full)
                const sysRows: TrajectoryRowEntry[] = [];
                if (idx === 0 || showFull) {
                  const sections = trace?.system_sections || [];
                  if (sections.length > 0) {
                    const combinedSys = sections
                      .map((s) => `### ${s.name || s.category || 'system'}\n\n${s.content || ''}`)
                      .join('\n\n---\n\n');
                    sysRows.push({
                      id: `c${idx}:sys`,
                      role: 'system',
                      title: `${sections.length} ${formatMsg(locale, 'trajectory.sections')}`,
                      body: combinedSys,
                      isMarkdown: true,
                    });
                  } else if (trace?.system_prompt) {
                    sysRows.push({
                      id: `c${idx}:sys`,
                      role: 'system',
                      body: trace.system_prompt,
                      isMarkdown: true,
                    });
                  }
                  if (trace?.tools && trace.tools.length > 0) {
                    sysRows.push({
                      id: `c${idx}:tools`,
                      role: 'system',
                      title: `TOOLS (${trace.tools.length})`,
                      body: trace.tools.map((t) => `${t.name}: ${previewLine(t.description || '', 90)}`).join('\n'),
                    });
                  }
                }

                const inputRows = [...sysRows, ...messagesToRows(visibleInputMsgs, `c${idx}:in`)].filter(
                  matchesFilter,
                );
                const outputRows = trace?.response
                  ? messagesToRows([trace.response], `c${idx}:out`).filter(matchesFilter)
                  : [];

                const hasToolCall = Boolean(
                  trace?.response?.content?.some((b) => (b.type || '').toLowerCase() === 'tool_call'),
                );
                const finishLabel = hasToolCall
                  ? formatMsg(locale, 'trajectory.toolCall')
                  : formatMsg(locale, 'trajectory.completed');

                return (
                  <article key={idx} className="bg-background">
                    {/* Sticky Call Card Header (matching Nex CallCard) */}
                    <div className="sticky top-0 z-10 flex flex-wrap items-center gap-2 border-b border-border/60 bg-surface/95 px-4 py-2 backdrop-blur-sm">
                      <span className="flex size-5 shrink-0 items-center justify-center rounded font-mono text-ui-xs font-semibold text-foreground-subtlest">
                        {String(idx + 1).padStart(2, '0')}
                      </span>
                      <span className="text-ui-sm font-medium text-foreground">
                        {sourceLabel(rec.telemetry.trigger_source, locale)}
                      </span>
                      <span className="rounded-full border border-border bg-card px-2 py-px font-mono text-[11px] text-foreground-subtle">
                        {rec.telemetry.model_id || trace?.model || 'model'}
                      </span>
                      <span
                        className={`rounded-full border px-2 py-px text-[11px] font-medium ${
                          hasToolCall
                            ? 'border-amber-500/25 bg-amber-500/10 text-amber-600 dark:text-amber-400'
                            : 'border-emerald-500/25 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400'
                        }`}
                      >
                        {finishLabel}
                      </span>

                      <div className="ml-auto flex flex-wrap items-center gap-2 font-mono text-[11px] tabular-nums text-foreground-subtlest">
                        {rec.telemetry.ttft_ms > 0 && <span>TTFT {formatDurationMs(rec.telemetry.ttft_ms)}</span>}
                        {rec.telemetry.duration_ms > 0 && <span>{formatDurationMs(rec.telemetry.duration_ms)}</span>}
                        <span>IN {formatCompactTokens(rec.telemetry.input_tokens || 0)}</span>
                        <span>OUT {formatCompactTokens(rec.telemetry.output_tokens || 0)}</span>
                        {(rec.telemetry.reasoning_tokens || 0) > 0 && (
                          <span className="text-violet-600 dark:text-violet-400">
                            R {formatCompactTokens(rec.telemetry.reasoning_tokens)}
                          </span>
                        )}
                      </div>
                    </div>

                    {/* Call Card Body: Input & Output Sections */}
                    <div className="space-y-3 p-3.5">
                      {rec.error ? (
                        <div className="rounded-xl border border-destructive/30 bg-destructive/10 px-3.5 py-2.5 text-ui-sm text-destructive">
                          {rec.error}
                        </div>
                      ) : (
                        <>
                          {/* INPUT Section */}
                          {(inputRows.length > 0 || hiddenPriorCount > 0) && (
                            <section className="overflow-hidden rounded-xl border border-border bg-card">
                              <div className="flex items-center justify-between border-b border-border/70 bg-surface/60 px-3 py-1.5">
                                <span className="font-mono text-[11px] font-semibold uppercase tracking-wider text-foreground-subtle">
                                  {formatMsg(locale, 'trajectory.input2')}
                                </span>
                                {hiddenPriorCount > 0 && (
                                  <button
                                    type="button"
                                    onClick={() =>
                                      setShowFullInputCalls((prev) => ({ ...prev, [idx]: !prev[idx] }))
                                    }
                                    className="text-ui-xs text-brand-blue hover:underline"
                                  >
                                    {formatMsg(locale, 'trajectory.showPriorMessages', { hiddenPriorCount })}
                                  </button>
                                )}
                              </div>
                              <div>
                                {inputRows.map((row, rIdx) => {
                                  const defaultOpen = row.role !== 'system';
                                  const open = isRowOpen(row.id, defaultOpen);
                                  return (
                                    <ExpandableRow
                                      key={row.id}
                                      entry={row}
                                      locale={locale}
                                      expanded={open}
                                      isAlt={rIdx % 2 === 1}
                                      onToggle={() => toggleRow(row.id, open)}
                                    />
                                  );
                                })}
                              </div>
                            </section>
                          )}

                          {/* OUTPUT Section */}
                          {outputRows.length > 0 && (
                            <section className="overflow-hidden rounded-xl border border-border bg-card">
                              <div className="flex items-center justify-between border-b border-border/70 bg-surface/60 px-3 py-1.5">
                                <span className="font-mono text-[11px] font-semibold uppercase tracking-wider text-foreground-subtle">
                                  {formatMsg(locale, 'trajectory.output2')}
                                </span>
                              </div>
                              <div>
                                {outputRows.map((row, rIdx) => {
                                  const open = isRowOpen(row.id, true);
                                  return (
                                    <ExpandableRow
                                      key={row.id}
                                      entry={row}
                                      locale={locale}
                                      expanded={open}
                                      isAlt={rIdx % 2 === 1}
                                      onToggle={() => toggleRow(row.id, open)}
                                    />
                                  );
                                })}
                              </div>
                            </section>
                          )}
                        </>
                      )}
                    </div>
                  </article>
                );
              })}
            </div>
          )}
        </div>
      </div>
    </div>,
    document.body,
  );
}
