import { formatMsg } from '../i18n/messages';
import { Check, Copy, FileText, SquareTerminal, WrapText } from 'lucide-react';
import { type CSSProperties, memo, type ReactNode, useRef, useState } from 'react';
import { useNexStore } from '../core/store';

const KEYWORDS = new Set([
  // JS / TS / Go / Python / Rust-ish
  'export',
  'import',
  'from',
  'default',
  'const',
  'let',
  'var',
  'function',
  'func',
  'return',
  'if',
  'else',
  'for',
  'while',
  'switch',
  'case',
  'break',
  'continue',
  'type',
  'interface',
  'struct',
  'package',
  'defer',
  'range',
  'nil',
  'true',
  'false',
  'null',
  'undefined',
  'class',
  'extends',
  'implements',
  'async',
  'await',
  'new',
  'try',
  'catch',
  'finally',
  'throw',
  'def',
  'elif',
  'then',
  'fi',
  'do',
  'done',
  'in',
  'of',
  'as',
  'is',
  'not',
  'and',
  'or',
  'yield',
  'public',
  'private',
  'static',
  'void',
  'int',
  'string',
  'bool',
  'map',
  'chan',
  'go',
  'select',
  'echo',
  'cd',
  'source',
  'use',
  'set',
  'local',
  'sudo',
  'npm',
  'npx',
  'pnpm',
  'yarn',
  'node',
  'git',
  'make',
  'docker',
  'curl',
  'grep',
  'sed',
  'awk',
  'cat',
  'ls',
  'rm',
  'mkdir',
  'chmod',
]);

const TYPES = new Set([
  'any',
  'unknown',
  'never',
  'boolean',
  'number',
  'object',
  'symbol',
  'bigint',
  'Promise',
  'Record',
  'Array',
  'Map',
  'Set',
  'Error',
  'Date',
  'RegExp',
  'Partial',
  'Required',
  'Readonly',
  'Context',
  'error',
  'byte',
  'rune',
  'int64',
  'uint64',
  'float64',
]);

const TOKEN_RE =
  /(#[^\n]*)|(\/\/[^\n]*)|(`(?:[^`\\]|\\.)*`|"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')|\b([A-Za-z_$][\w$]*)\b|(--?[A-Za-z][\w-]*)|(\b\d+(?:\.\d+)?\b)/g;

const TOKEN_CLASS = {
  comment: 'text-foreground-subtlest italic',
  string: 'text-sky-600 dark:text-sky-400',
  jsonKey: 'text-violet-600 dark:text-violet-400',
  keyword: 'text-rose-600 dark:text-rose-400',
  type: 'text-emerald-600 dark:text-emerald-400',
  func: 'text-blue-600 dark:text-blue-400',
  flag: 'text-teal-600 dark:text-teal-400',
  number: 'text-amber-600 dark:text-amber-400',
} as const;

const MD_INLINE_RE = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\[[^\]]+\]\([^)]+\))/g;

function highlightMarkdownLine(line: string, lineKey: number): ReactNode[] {
  if (/^\s*```/.test(line)) {
    return [
      <span key={`${lineKey}-fence`} className={TOKEN_CLASS.flag}>
        {line}
      </span>,
    ];
  }
  if (/^\s*#{1,6}\s/.test(line)) {
    return [
      <span key={`${lineKey}-h`} className="font-semibold text-blue-600 dark:text-blue-400">
        {line}
      </span>,
    ];
  }
  if (/^\s*>\s/.test(line)) {
    return [
      <span key={`${lineKey}-bq`} className="italic text-foreground-subtle">
        {line}
      </span>,
    ];
  }

  const nodes: ReactNode[] = [];
  let rest = line;
  let seq = 0;

  // Highlight leading list marker (`- `, `* `, `1. `)
  const listMatch = /^(\s*(?:[-*+]|\d+\.)\s+)(.*)$/.exec(line);
  if (listMatch) {
    nodes.push(
      <span key={`${lineKey}-lm`} className={TOKEN_CLASS.number}>
        {listMatch[1]}
      </span>,
    );
    rest = listMatch[2];
  }

  const re = new RegExp(MD_INLINE_RE.source, 'g');
  let cursor = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(rest)) !== null) {
    if (m.index > cursor) nodes.push(rest.slice(cursor, m.index));
    if (m[1]) {
      nodes.push(
        <span key={`${lineKey}-${seq++}`} className={TOKEN_CLASS.string}>
          {m[0]}
        </span>,
      );
    } else if (m[2]) {
      nodes.push(
        <span key={`${lineKey}-${seq++}`} className="font-semibold text-amber-600 dark:text-amber-400">
          {m[0]}
        </span>,
      );
    } else if (m[3]) {
      nodes.push(
        <span key={`${lineKey}-${seq++}`} className={TOKEN_CLASS.type}>
          {m[0]}
        </span>,
      );
    }
    cursor = m.index + m[0].length;
  }
  if (cursor < rest.length) nodes.push(rest.slice(cursor));
  return nodes;
}

export function highlightSourceLine(line: string, lineKey: number, language?: string): ReactNode[] {
  const lang = (language || '').trim().toLowerCase();
  if (lang === 'markdown' || lang === 'md') {
    return highlightMarkdownLine(line, lineKey);
  }
  return highlightLine(line, lineKey);
}

function highlightLine(line: string, lineKey: number): ReactNode[] {
  const nodes: ReactNode[] = [];
  const re = new RegExp(TOKEN_RE.source, 'g');
  let cursor = 0;
  let seq = 0;
  let match: RegExpExecArray | null;

  while ((match = re.exec(line)) !== null) {
    const start = match.index;
    const token = match[0];

    // `https://…` / `a//b` should not turn the rest of the line into a comment.
    if (token.startsWith('//') && start > 0 && !/[\s(;|&]/.test(line[start - 1])) {
      re.lastIndex = start + 2;
      continue;
    }

    if (start > cursor) nodes.push(line.slice(cursor, start));

    if (match[1] || match[2]) {
      nodes.push(
        <span key={`${lineKey}-${seq++}`} className={TOKEN_CLASS.comment}>
          {token}
        </span>,
      );
    } else if (match[3]) {
      const after = line.slice(start + token.length);
      const isJsonKey = /^\s*:/.test(after);
      nodes.push(
        <span key={`${lineKey}-${seq++}`} className={isJsonKey ? TOKEN_CLASS.jsonKey : TOKEN_CLASS.string}>
          {token}
        </span>,
      );
    } else if (match[4]) {
      const afterChar = line[start + token.length];
      if (KEYWORDS.has(token)) {
        nodes.push(
          <span key={`${lineKey}-${seq++}`} className={TOKEN_CLASS.keyword}>
            {token}
          </span>,
        );
      } else if (TYPES.has(token)) {
        nodes.push(
          <span key={`${lineKey}-${seq++}`} className={TOKEN_CLASS.type}>
            {token}
          </span>,
        );
      } else if (afterChar === '(') {
        nodes.push(
          <span key={`${lineKey}-${seq++}`} className={TOKEN_CLASS.func}>
            {token}
          </span>,
        );
      } else {
        nodes.push(token);
      }
    } else if (match[5]) {
      nodes.push(
        <span key={`${lineKey}-${seq++}`} className={TOKEN_CLASS.flag}>
          {token}
        </span>,
      );
    } else if (match[6]) {
      nodes.push(
        <span key={`${lineKey}-${seq++}`} className={TOKEN_CLASS.number}>
          {token}
        </span>,
      );
    } else {
      nodes.push(token);
    }

    cursor = start + token.length;
  }

  if (cursor < line.length) nodes.push(line.slice(cursor));
  return nodes;
}

function highlightCode(code: string, language?: string): ReactNode {
  const lines = code.split('\n');
  return (
    <>
      {lines.map((line, idx) => (
        <span key={idx}>
          {highlightSourceLine(line, idx, language)}
          {idx < lines.length - 1 ? '\n' : null}
        </span>
      ))}
    </>
  );
}

export function HighlightedCode({
  code,
  language,
  className = '',
  style,
}: {
  code: string;
  language?: string;
  className?: string;
  style?: CSSProperties;
}) {
  return (
    <pre
      className={`overflow-x-auto rounded-lg border border-border bg-surface p-3 font-mono text-ui-sm leading-relaxed text-foreground ${className}`}
      style={style}
    >
      <code data-highlighted-lang={language || 'code'}>{highlightCode(code, language)}</code>
    </pre>
  );
}

/**
 * Lightweight syntax-highlighted code editor using a scroll-synchronized highlighted `<pre>`
 * behind a transparent-text `<textarea>` (zero external editor bundle overhead).
 */
export function CodeEditorWithHighlight({
  value,
  onChange,
  language = 'ts',
  rows = 8,
  placeholder,
  className = '',
  testId,
}: {
  value: string;
  onChange: (next: string) => void;
  language?: string;
  rows?: number;
  placeholder?: string;
  className?: string;
  testId?: string;
}) {
  const preRef = useRef<HTMLPreElement | null>(null);
  const taRef = useRef<HTMLTextAreaElement | null>(null);

  const syncScroll = () => {
    if (preRef.current && taRef.current) {
      preRef.current.scrollTop = taRef.current.scrollTop;
      preRef.current.scrollLeft = taRef.current.scrollLeft;
    }
  };

  const minHeightPx = Math.max(96, rows * 21 + 24);

  return (
    <div
      className={`relative overflow-hidden rounded-lg border border-border bg-input focus-within:border-border-hover ${className}`}
      style={{ minHeight: `${minHeightPx}px` }}
    >
      <pre
        ref={preRef}
        aria-hidden="true"
        className="pointer-events-none absolute inset-0 m-0 overflow-hidden whitespace-pre-wrap break-words p-3 font-mono text-ui-xs leading-[1.6] text-foreground"
      >
        <code data-highlighted-lang={language}>
          {value ? highlightCode(value, language) : <span className="text-foreground-subtlest">{placeholder || ''}</span>}
          {value.endsWith('\n') ? ' ' : ''}
        </code>
      </pre>
      <textarea
        ref={taRef}
        data-testid={testId}
        rows={rows}
        value={value}
        spellCheck={false}
        onScroll={syncScroll}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Tab') {
            e.preventDefault();
            const el = e.currentTarget;
            const start = el.selectionStart;
            const end = el.selectionEnd;
            const next = value.slice(0, start) + '  ' + value.slice(end);
            onChange(next);
            window.requestAnimationFrame(() => {
              el.selectionStart = el.selectionEnd = start + 2;
            });
          }
        }}
        placeholder=""
        style={{ minHeight: `${minHeightPx}px`, WebkitTextFillColor: 'transparent' }}
        className="relative z-10 block h-full w-full resize-y bg-transparent p-3 font-mono text-ui-xs leading-[1.6] text-transparent caret-foreground outline-none selection:bg-brand-blue/25"
      />
    </div>
  );
}

function CodeBlock({ lang, code, streaming = false }: { lang: string; code: string; streaming?: boolean }) {
  const locale = useNexStore((s) => s.locale);
  const [copied, setCopied] = useState(false);
  const [wrap, setWrap] = useState(false);

  const onCopy = () => {
    void navigator.clipboard?.writeText(code);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  const language = lang || 'code';

  return (
    <div className="my-4 overflow-hidden rounded-xl border border-border bg-card">
      <div className="flex items-center justify-between gap-2 pl-3 pr-2 pt-2 pb-1 text-ui-base text-foreground-subtle">
        <span className="flex min-w-0 items-center gap-2">
          <SquareTerminal className="size-3.5 shrink-0 text-amber-600 dark:text-amber-400" />
          <span className="truncate font-mono text-ui-sm lowercase text-foreground-subtle">{language}</span>
        </span>
        <span className="flex shrink-0 items-center gap-0.5">
          <button
            type="button"
            onClick={() => setWrap((v) => !v)}
            title={
              wrap
                ? formatMsg(locale, 'markdown.disableLineWrap')
                : formatMsg(locale, 'markdown.wrapLines')
            }
            className={`flex size-6 items-center justify-center rounded-md transition-colors hover:bg-hover ${
              wrap ? 'bg-hover text-foreground' : 'text-foreground-subtle hover:text-foreground'
            }`}
          >
            <WrapText className="size-3.5" />
          </button>
          <button
            type="button"
            onClick={onCopy}
            title={formatMsg(locale, 'markdown.copyCode')}
            className="flex size-6 items-center justify-center rounded-md text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
          >
            {copied ? <Check className="size-3.5 text-success" /> : <Copy className="size-3.5" />}
          </button>
        </span>
      </div>
      <div className={`px-3.5 pb-3 pt-1 ${wrap ? '' : 'overflow-x-auto'}`}>
        <pre
          className={`font-mono text-[13px] leading-6 tracking-normal text-foreground ${
            wrap ? 'whitespace-pre-wrap break-words' : 'whitespace-pre'
          }`}
        >
          {/* Nex Streaming Markdown Rule: disable expensive regex syntax highlighting while actively streaming! */}
          <code>{streaming || code.length > 24000 ? code : highlightCode(code, language)}</code>
        </pre>
      </div>
    </div>
  );
}

// Matches `path/to/file.go:42`, `App.tsx:10-24`, `pkg/ai/types.go:L18` without false-matching URLs or times (`10:30`)
const FILE_LINE_REF_RE =
  /^((?:[A-Za-z0-9._-]+\/)*[A-Za-z0-9._-]+\.[A-Za-z0-9]{1,10}):L?(\d+)(?:[-–]L?(\d+))?$/;

// Matches workspace-ish file paths like `pkg/ai/models.go` or `clients/web/src/index.css`
const FILE_PATH_REF_RE =
  /^(?:[A-Za-z0-9._-]+\/)+[A-Za-z0-9._-]+\.[A-Za-z0-9]{1,10}$/;

function parseFileLineRef(raw: string): { path: string; line?: number } | null {
  const trimmed = raw.trim();
  if (!trimmed || trimmed.includes('://') || trimmed.startsWith('http')) return null;
  const m = FILE_LINE_REF_RE.exec(trimmed);
  if (m) {
    const line = Number(m[2]);
    if (!Number.isFinite(line) || line <= 0) return null;
    return { path: m[1], line };
  }
  if (FILE_PATH_REF_RE.test(trimmed)) {
    return { path: trimmed };
  }
  return null;
}

function isHorizontalRule(trimmed: string): boolean {
  return /^(?:-\s*){3,}$|^(?:\*\s*){3,}$|^(?:_\s*){3,}$/.test(trimmed);
}

const LIST_LINE_RE = /^(\s*)([-*+]|\d+\.)\s+(.*)$/;

function renderPlainWithAutoLinks(text: string, keyPrefix: string): ReactNode {
  if (!text || !text.includes('://')) return text;
  const parts = text.split(/(https?:\/\/[^\s<>()[\]"'`]+)/g);
  if (parts.length === 1) return text;
  return parts.map((part, idx) => {
    if (!part) return null;
    if (/^https?:\/\//.test(part)) {
      let url = part;
      let trailing = '';
      while (url.length > 0 && /[.,;:!?，。；：！？）】》」』"'’”]$/.test(url)) {
        trailing = url.slice(-1) + trailing;
        url = url.slice(0, -1);
      }
      if (!url || url === 'http://' || url === 'https://') {
        return <span key={`${keyPrefix}-u-${idx}`}>{part}</span>;
      }
      return (
        <span key={`${keyPrefix}-u-${idx}`}>
          <a
            href={url}
            target="_blank"
            rel="noreferrer"
            className="text-icon-blue underline decoration-dotted underline-offset-4 hover:opacity-80 break-all"
          >
            {url}
          </a>
          {trailing}
        </span>
      );
    }
    return <span key={`${keyPrefix}-t-${idx}`}>{part}</span>;
  });
}

function renderInline(text: string, depth = 0): ReactNode[] {
  if (!text) return [];
  const tokens = text.split(/(`[^`]+`|\*\*[^*]+\*\*|~~[^~]+~~|\*(?!\s)[^*\n]+(?<!\s)\*|!\[[^\]]*\]\([^)]+\)|\[[^\]]+\]\([^)]+\))/g);
  return tokens.map((tok, i) => {
    if (!tok) return null;
    if (tok.startsWith('`') && tok.endsWith('`') && tok.length >= 2) {
      const inner = tok.slice(1, -1);
      const fileRef = parseFileLineRef(inner);
      if (fileRef) {
        const refAttr = fileRef.line ? `${fileRef.path}:${fileRef.line}` : fileRef.path;
        return (
          <button
            key={i}
            type="button"
            data-file-ref={refAttr}
            title={fileRef.line ? `Open ${fileRef.path} at line ${fileRef.line}` : `Open ${fileRef.path}`}
            onClick={() => void useNexStore.getState().openFileInPreviewTab(fileRef.path, fileRef.line)}
            className="mx-0.5 inline-flex max-w-full cursor-pointer items-baseline gap-1 align-baseline text-ui-base text-icon-blue no-underline decoration-dotted underline-offset-4 hover:underline"
          >
            <FileText className="ml-0.5 size-3.5 shrink-0 self-center" />
            <span className="min-w-0 truncate self-baseline font-mono text-ui-sm">{inner}</span>
          </button>
        );
      }
      return (
        <code
          key={i}
          className="mx-0.5 rounded-md bg-markdown-inline-code/50 px-1.5 py-0.5 font-mono text-ui-sm text-foreground"
        >
          {inner}
        </code>
      );
    }
    if (tok.startsWith('**') && tok.endsWith('**') && tok.length >= 4) {
      const inner = tok.slice(2, -2);
      return (
        <strong key={i} className="font-medium text-foreground">
          {depth < 2 ? renderInline(inner, depth + 1) : inner}
        </strong>
      );
    }
    if (tok.startsWith('~~') && tok.endsWith('~~') && tok.length >= 4) {
      const inner = tok.slice(2, -2);
      return (
        <del key={i} className="text-foreground-subtle line-through">
          {depth < 2 ? renderInline(inner, depth + 1) : inner}
        </del>
      );
    }
    if (tok.startsWith('*') && tok.endsWith('*') && tok.length >= 3) {
      const inner = tok.slice(1, -1);
      return (
        <em key={i} className="italic">
          {depth < 2 ? renderInline(inner, depth + 1) : inner}
        </em>
      );
    }
    const imgMatch = /^!\[([^\]]*)\]\(([^)]+)\)$/.exec(tok);
    if (imgMatch) {
      const alt = imgMatch[1] || 'image';
      const rawUrl = imgMatch[2].trim();
      const src = rawUrl.split(/\s+/)[0] || '';
      if (src) {
        return (
          <button
            key={i}
            type="button"
            data-testid="markdown-image-btn"
            onClick={() => useNexStore.getState().openImagePreview({ src, alt })}
            className="my-2 block max-w-full cursor-zoom-in overflow-hidden rounded-xl border border-border bg-surface/40 transition-shadow hover:shadow-md"
          >
            <img src={src} alt={alt} loading="lazy" className="max-h-80 max-w-full object-contain" />
          </button>
        );
      }
    }
    const linkMatch = /^\[([^\]]+)\]\(([^)]+)\)$/.exec(tok);
    if (linkMatch) {
      const label = linkMatch[1];
      const href = linkMatch[2];
      const localFile = parseFileLineRef(href);
      if (localFile) {
        const refAttr = localFile.line ? `${localFile.path}:${localFile.line}` : localFile.path;
        return (
          <button
            key={i}
            type="button"
            data-file-ref={refAttr}
            title={href}
            onClick={() => void useNexStore.getState().openFileInPreviewTab(localFile.path, localFile.line)}
            className="inline-flex max-w-full cursor-pointer items-baseline gap-1 align-baseline text-ui-base text-icon-blue no-underline decoration-dotted underline-offset-4 hover:underline"
          >
            <FileText className="ml-0.5 size-3.5 shrink-0 self-center" />
            <span className="min-w-0 truncate self-baseline">{label}</span>
          </button>
        );
      }
      return (
        <a
          key={i}
          href={href}
          target="_blank"
          rel="noreferrer"
          className="wrap-anywhere text-ui-base font-medium text-icon-blue no-underline decoration-dotted underline-offset-4 hover:underline"
        >
          {label}
        </a>
      );
    }
    return <span key={i}>{renderPlainWithAutoLinks(tok, `${depth}-${i}`)}</span>;
  });
}

function isTableSeparator(line: string): boolean {
  const t = line.trim();
  if (!t.includes('|') || !t.includes('-')) return false;
  return /^\|?(\s*:?-{2,}:?\s*\|)+\s*:?-{2,}:?\s*\|?$/.test(t);
}

function splitTableCells(line: string): string[] {
  let t = line.trim();
  if (t.startsWith('|')) t = t.slice(1);
  if (t.endsWith('|')) t = t.slice(0, -1);
  return t.split('|').map((c) => c.trim());
}

interface ParsedListItem {
  text: string;
  children: ReactNode[];
}

function parseNestedList(
  lines: string[],
  startIdx: number,
  nextKey: () => number,
): { node: ReactNode; nextIdx: number } {
  const firstMatch = LIST_LINE_RE.exec(lines[startIdx]);
  if (!firstMatch) {
    return { node: null, nextIdx: startIdx + 1 };
  }
  const baseIndent = firstMatch[1].replace(/\t/g, '  ').length;
  const ordered = /^\d+\./.test(firstMatch[2]);
  const items: ParsedListItem[] = [];
  let i = startIdx;

  while (i < lines.length) {
    const rawLine = lines[i];
    if (!rawLine.trim()) {
      // Peek ahead: if next non-empty line continues the list at >= baseIndent, skip blank line
      let peek = i + 1;
      while (peek < lines.length && !lines[peek].trim()) peek++;
      const peekMatch = peek < lines.length ? LIST_LINE_RE.exec(lines[peek]) : null;
      if (peekMatch && peekMatch[1].replace(/\t/g, '  ').length >= baseIndent) {
        i = peek;
        continue;
      }
      break;
    }

    const m = LIST_LINE_RE.exec(rawLine);
    if (!m) {
      const lineIndent = /^(\s*)/.exec(rawLine)?.[1].replace(/\t/g, '  ').length ?? 0;
      if (items.length > 0 && lineIndent > baseIndent && !isHorizontalRule(rawLine.trim()) && !/^#{1,6}\s+/.test(rawLine.trim())) {
        items[items.length - 1].text += `\n${rawLine.trim()}`;
        i++;
        continue;
      }
      break;
    }

    const indent = m[1].replace(/\t/g, '  ').length;
    if (indent < baseIndent) {
      break;
    }
    if (indent > baseIndent) {
      const sub = parseNestedList(lines, i, nextKey);
      if (items.length > 0 && sub.node) {
        items[items.length - 1].children.push(sub.node);
      }
      i = sub.nextIdx;
      continue;
    }

    const isItemOrdered = /^\d+\./.test(m[2]);
    if (isItemOrdered !== ordered && items.length > 0) {
      break;
    }
    items.push({ text: m[3], children: [] });
    i++;
  }

  const ListTag = ordered ? 'ol' : 'ul';
  const listClass = ordered
    ? 'my-3 list-inside list-decimal space-y-1.5 pl-0 marker:text-foreground-subtlest [&_ul]:my-1.5 [&_ol]:my-1.5'
    : 'my-3 list-outside list-disc space-y-1.5 pl-5 marker:text-foreground-subtlest [&_ul]:my-1.5 [&_ol]:my-1.5';

  const node = (
    <ListTag key={nextKey()} className={listClass}>
      {items.map((it, idx) => (
        <li key={idx} className="pl-1">
          <span>{renderInline(it.text)}</span>
          {it.children}
        </li>
      ))}
    </ListTag>
  );

  return { node, nextIdx: i };
}

export const MarkdownRenderer = memo(function MarkdownRendererImpl({
  content,
  streaming = false,
}: {
  content: string;
  streaming?: boolean;
}) {
  if (!content) return null;

  const blocks: ReactNode[] = [];
  const fenceRegex = /```([a-zA-Z0-9_-]*)\n([\s\S]*?)```/g;
  let lastIndex = 0;
  let match: RegExpExecArray | null;
  let keySeq = 0;
  const nextKey = () => keySeq++;

  const renderProseSegment = (segment: string) => {
    const lines = segment.replace(/\r\n/g, '\n').split('\n');
    let i = 0;
    while (i < lines.length) {
      const line = lines[i];
      const trimmed = line.trim();
      if (!trimmed) {
        i++;
        continue;
      }

      if (isHorizontalRule(trimmed)) {
        blocks.push(<hr key={nextKey()} className="my-6 border-0 border-t border-border" />);
        i++;
        continue;
      }

      // GFM Table block (`| col1 | col2 |` followed by `|---|---|`) — borderless Nex style
      if (trimmed.includes('|') && i + 1 < lines.length && isTableSeparator(lines[i + 1])) {
        const headers = splitTableCells(trimmed);
        i += 2;
        const rows: string[][] = [];
        while (i < lines.length && lines[i].trim().includes('|') && lines[i].trim() !== '') {
          rows.push(splitTableCells(lines[i]));
          i++;
        }
        blocks.push(
          <div key={nextKey()} className="my-4 w-full overflow-x-auto">
            <table className="w-max min-w-full border-separate border-spacing-0 text-ui-base">
              <thead>
                <tr>
                  {headers.map((h, hi) => (
                    <th
                      key={hi}
                      className="min-w-16 max-w-md border-b border-border px-3 py-2 text-left font-normal whitespace-normal break-words text-foreground-subtlest"
                    >
                      {renderInline(h)}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((row, ri) => (
                  <tr key={ri} className="transition-colors last:[&>td]:border-b-0 hover:bg-hover/40">
                    {headers.map((_, ci) => (
                      <td
                        key={ci}
                        className="min-w-16 max-w-md border-b border-border px-3 py-2 align-top whitespace-normal break-words text-foreground"
                      >
                        {renderInline(row[ci] || '')}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>,
        );
        continue;
      }

      if (trimmed.startsWith('###### ')) {
        blocks.push(
          <h6 key={nextKey()} className="mt-6 mb-4 text-ui-base font-normal text-foreground">
            {renderInline(trimmed.slice(7))}
          </h6>,
        );
        i++;
        continue;
      }
      if (trimmed.startsWith('##### ')) {
        blocks.push(
          <h5 key={nextKey()} className="mt-6 mb-4 text-ui-base font-medium text-foreground">
            {renderInline(trimmed.slice(6))}
          </h5>,
        );
        i++;
        continue;
      }
      if (trimmed.startsWith('#### ')) {
        blocks.push(
          <h4 key={nextKey()} className="mt-6 mb-4 text-ui-base font-semibold text-foreground">
            {renderInline(trimmed.slice(5))}
          </h4>,
        );
        i++;
        continue;
      }
      if (trimmed.startsWith('### ')) {
        blocks.push(
          <h3 key={nextKey()} className="mt-6 mb-4 text-ui-base font-semibold text-foreground">
            {renderInline(trimmed.slice(4))}
          </h3>,
        );
        i++;
        continue;
      }
      if (trimmed.startsWith('## ')) {
        blocks.push(
          <h2 key={nextKey()} className="mt-6 mb-4 text-ui-lg font-semibold text-foreground">
            {renderInline(trimmed.slice(3))}
          </h2>,
        );
        i++;
        continue;
      }
      if (trimmed.startsWith('# ')) {
        blocks.push(
          <h1 key={nextKey()} className="mt-6 mb-4 text-ui-xl font-semibold text-foreground">
            {renderInline(trimmed.slice(2))}
          </h1>,
        );
        i++;
        continue;
      }

      if (trimmed.startsWith('>')) {
        const quoteParas: string[][] = [[]];
        while (i < lines.length && lines[i].trim().startsWith('>')) {
          const stripped = lines[i].trim().replace(/^>\s?/, '');
          if (stripped === '') {
            if (quoteParas[quoteParas.length - 1].length > 0) quoteParas.push([]);
          } else {
            quoteParas[quoteParas.length - 1].push(stripped);
          }
          i++;
        }
        const nonEmptyParas = quoteParas.filter((p) => p.length > 0);
        blocks.push(
          <blockquote
            key={nextKey()}
            className="my-4 border-l-2 border-border pl-3 text-foreground-subtle [&_p]:my-0 [&_p+p]:mt-2"
          >
            {nonEmptyParas.map((p, pi) => (
              <p key={pi} className="whitespace-pre-wrap">
                {renderInline(p.join('\n'))}
              </p>
            ))}
          </blockquote>,
        );
        continue;
      }

      if (LIST_LINE_RE.test(line)) {
        const parsed = parseNestedList(lines, i, nextKey);
        if (parsed.node) blocks.push(parsed.node);
        i = parsed.nextIdx;
        continue;
      }

      const paraLines: string[] = [];
      while (i < lines.length) {
        const curLine = lines[i];
        const curTrim = curLine.trim();
        if (
          paraLines.length > 0 &&
          (curTrim === '' ||
            isHorizontalRule(curTrim) ||
            /^#{1,6}\s+/.test(curTrim) ||
            curTrim.startsWith('>') ||
            (curTrim.includes('|') && i + 1 < lines.length && isTableSeparator(lines[i + 1])) ||
            LIST_LINE_RE.test(curLine))
        ) {
          break;
        }
        paraLines.push(curLine);
        i++;
      }
      blocks.push(
        <p key={nextKey()} className="whitespace-pre-wrap break-words text-foreground">
          {renderInline(paraLines.join('\n'))}
        </p>,
      );
    }
  };

  while ((match = fenceRegex.exec(content)) !== null) {
    if (match.index > lastIndex) {
      renderProseSegment(content.slice(lastIndex, match.index));
    }
    blocks.push(
      <CodeBlock key={nextKey()} lang={match[1]} code={match[2].replace(/\n$/, '')} streaming={streaming} />,
    );
    lastIndex = match.index + match[0].length;
  }

  if (lastIndex < content.length) {
    const tail = content.slice(lastIndex);
    // Tolerate unclosed fenced code blocks mid-stream (Nex incomplete-markdown rule)
    const unclosedFence = /^([\s\S]*?)```([a-zA-Z0-9_-]*)\n([\s\S]*)$/.exec(tail);
    if (unclosedFence) {
      if (unclosedFence[1]) renderProseSegment(unclosedFence[1]);
      blocks.push(<CodeBlock key={nextKey()} lang={unclosedFence[2]} code={unclosedFence[3]} streaming={true} />);
    } else {
      renderProseSegment(tail);
    }
  }

  return (
    <div className="min-w-0 size-full space-y-4 text-ui-base leading-[1.75] tracking-wide [&>*:first-child]:mt-0 [&>*:last-child]:mb-0">
      {blocks}
    </div>
  );
});
