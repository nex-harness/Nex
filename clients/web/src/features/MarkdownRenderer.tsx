import { Check, Copy, SquareTerminal, WrapText } from 'lucide-react';
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
    <div className="my-3 overflow-hidden rounded-xl border border-border bg-card">
      <div className="flex items-center justify-between gap-2 px-3.5 pb-1 pt-2.5">
        <span className="flex min-w-0 items-center gap-2">
          <SquareTerminal className="size-3.5 shrink-0 text-amber-600 dark:text-amber-400" />
          <span className="truncate font-mono text-ui-sm font-semibold lowercase text-foreground">{language}</span>
        </span>
        <span className="flex shrink-0 items-center gap-0.5">
          <button
            type="button"
            onClick={() => setWrap((v) => !v)}
            title={
              wrap
                ? locale === 'en-US'
                  ? 'Disable line wrap'
                  : '关闭自动换行'
                : locale === 'en-US'
                  ? 'Wrap lines'
                  : '自动换行'
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
            title={locale === 'en-US' ? 'Copy code' : '复制代码'}
            className="flex size-6 items-center justify-center rounded-md text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
          >
            {copied ? <Check className="size-3.5 text-success" /> : <Copy className="size-3.5" />}
          </button>
        </span>
      </div>
      <div className={`px-3.5 pb-3 pt-1 ${wrap ? '' : 'overflow-x-auto'}`}>
        <pre
          className={`font-mono text-[13px] leading-6 text-foreground ${
            wrap ? 'whitespace-pre-wrap break-words' : 'whitespace-pre'
          }`}
        >
          {/* ZCode Streaming Markdown Rule: disable expensive regex syntax highlighting while actively streaming! */}
          <code>{streaming || code.length > 24000 ? code : highlightCode(code, language)}</code>
        </pre>
      </div>
    </div>
  );
}

// Matches `path/to/file.go:42`, `App.tsx:10-24`, `pkg/ai/types.go:L18` without false-matching URLs or times (`10:30`)
const FILE_LINE_REF_RE =
  /^((?:[A-Za-z0-9._-]+\/)*[A-Za-z0-9._-]+\.[A-Za-z0-9]{1,10}):L?(\d+)(?:[-–]L?(\d+))?$/;

function parseFileLineRef(raw: string): { path: string; line: number } | null {
  const trimmed = raw.trim();
  if (!trimmed || trimmed.includes('://') || trimmed.startsWith('http')) return null;
  const m = FILE_LINE_REF_RE.exec(trimmed);
  if (!m) return null;
  const line = Number(m[2]);
  if (!Number.isFinite(line) || line <= 0) return null;
  return { path: m[1], line };
}

function renderInline(text: string, depth = 0): ReactNode[] {
  if (!text) return [];
  const tokens = text.split(/(`[^`]+`|\*\*[^*]+\*\*|\[[^\]]+\]\([^)]+\))/g);
  return tokens.map((tok, i) => {
    if (tok.startsWith('`') && tok.endsWith('`') && tok.length >= 2) {
      const inner = tok.slice(1, -1);
      const fileRef = parseFileLineRef(inner);
      if (fileRef) {
        return (
          <button
            key={i}
            type="button"
            data-file-ref={`${fileRef.path}:${fileRef.line}`}
            title={`Open ${fileRef.path} at line ${fileRef.line}`}
            onClick={() => void useNexStore.getState().openFileInPreviewTab(fileRef.path, fileRef.line)}
            className="mx-0.5 inline-flex items-center rounded-md border border-brand-blue/25 bg-brand-blue/10 px-1.5 py-0.5 font-mono text-ui-xs text-brand-blue transition-colors hover:bg-brand-blue/20"
          >
            {inner}
          </button>
        );
      }
      return (
        <code key={i} className="rounded-md bg-tag px-1.5 py-0.5 font-mono text-ui-sm text-foreground">
          {inner}
        </code>
      );
    }
    if (tok.startsWith('**') && tok.endsWith('**') && tok.length >= 4) {
      const inner = tok.slice(2, -2);
      return (
        <strong key={i} className="font-semibold text-foreground">
          {depth < 2 ? renderInline(inner, depth + 1) : inner}
        </strong>
      );
    }
    const linkMatch = /^\[([^\]]+)\]\(([^)]+)\)$/.exec(tok);
    if (linkMatch) {
      return (
        <a
          key={i}
          href={linkMatch[2]}
          target="_blank"
          rel="noreferrer"
          className="text-brand-blue underline underline-offset-4 hover:opacity-85"
        >
          {linkMatch[1]}
        </a>
      );
    }
    return tok;
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

      // GFM Table block (`| col1 | col2 |` followed by `|---|---|`)
      if (trimmed.includes('|') && i + 1 < lines.length && isTableSeparator(lines[i + 1])) {
        const headers = splitTableCells(trimmed);
        i += 2;
        const rows: string[][] = [];
        while (i < lines.length && lines[i].trim().includes('|') && lines[i].trim() !== '') {
          rows.push(splitTableCells(lines[i]));
          i++;
        }
        blocks.push(
          <div key={keySeq++} className="my-3 overflow-x-auto rounded-xl border border-border">
            <table className="w-full border-collapse text-left text-ui-base">
              <thead className="bg-black/[0.02] text-ui-sm font-normal text-foreground-subtle dark:bg-white/[0.03]">
                <tr>
                  {headers.map((h, hi) => (
                    <th key={hi} className="px-4 py-2.5 font-normal">
                      {renderInline(h)}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {rows.map((row, ri) => (
                  <tr key={ri}>
                    {headers.map((_, ci) => (
                      <td key={ci} className="px-4 py-2.5 align-top text-ui-base text-foreground">
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

      if (trimmed.startsWith('###### ') || trimmed.startsWith('##### ')) {
        const text = trimmed.replace(/^#{5,6}\s+/, '');
        blocks.push(
          <h5 key={keySeq++} className="mt-2.5 mb-1 text-ui-sm font-semibold text-foreground">
            {renderInline(text)}
          </h5>,
        );
        i++;
        continue;
      }
      if (trimmed.startsWith('#### ')) {
        blocks.push(
          <h4 key={keySeq++} className="mt-3 mb-1 text-ui-base font-semibold text-foreground">
            {renderInline(trimmed.slice(5))}
          </h4>,
        );
        i++;
        continue;
      }
      if (trimmed.startsWith('### ')) {
        blocks.push(
          <h3 key={keySeq++} className="mt-3.5 mb-1.5 text-ui-base font-semibold text-foreground">
            {renderInline(trimmed.slice(4))}
          </h3>,
        );
        i++;
        continue;
      }
      if (trimmed.startsWith('## ')) {
        blocks.push(
          <h2 key={keySeq++} className="mt-4 mb-1.5 text-ui-lg font-semibold text-foreground">
            {renderInline(trimmed.slice(3))}
          </h2>,
        );
        i++;
        continue;
      }
      if (trimmed.startsWith('# ')) {
        blocks.push(
          <h1 key={keySeq++} className="mt-4 mb-2 text-ui-xl font-semibold text-foreground">
            {renderInline(trimmed.slice(2))}
          </h1>,
        );
        i++;
        continue;
      }

      if (trimmed.startsWith('> ')) {
        blocks.push(
          <blockquote
            key={keySeq++}
            className="my-2 border-l-2 border-border-hover pl-3 text-ui-base text-foreground-subtle"
          >
            {renderInline(trimmed.slice(2))}
          </blockquote>,
        );
        i++;
        continue;
      }

      if (/^[-*]\s+/.test(trimmed) || /^\d+\.\s+/.test(trimmed)) {
        const items: string[] = [];
        const ordered = /^\d+\.\s+/.test(trimmed);
        while (i < lines.length && (/^[-*]\s+/.test(lines[i].trim()) || /^\d+\.\s+/.test(lines[i].trim()))) {
          items.push(lines[i].trim().replace(/^([-*]|\d+\.)\s+/, ''));
          i++;
        }
        const ListTag = ordered ? 'ol' : 'ul';
        blocks.push(
          <ListTag
            key={keySeq++}
            className={`my-2 space-y-1 pl-5 text-ui-base leading-relaxed text-foreground ${
              ordered ? 'list-decimal' : 'list-disc'
            }`}
          >
            {items.map((it, idx) => (
              <li key={idx}>{renderInline(it)}</li>
            ))}
          </ListTag>,
        );
        continue;
      }

      const paraLines: string[] = [];
      while (i < lines.length) {
        const curTrim = lines[i].trim();
        if (
          paraLines.length > 0 &&
          (curTrim === '' ||
            /^#{1,6}\s+/.test(curTrim) ||
            curTrim.startsWith('> ') ||
            (curTrim.includes('|') && i + 1 < lines.length && isTableSeparator(lines[i + 1])) ||
            /^([-*]|\d+\.)\s+/.test(curTrim))
        ) {
          break;
        }
        paraLines.push(lines[i]);
        i++;
      }
      blocks.push(
        <p key={keySeq++} className="my-1.5 text-ui-base leading-relaxed whitespace-pre-wrap text-foreground">
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
      <CodeBlock key={keySeq++} lang={match[1]} code={match[2].replace(/\n$/, '')} streaming={streaming} />,
    );
    lastIndex = match.index + match[0].length;
  }

  if (lastIndex < content.length) {
    const tail = content.slice(lastIndex);
    // Tolerate unclosed fenced code blocks mid-stream (ZCode incomplete-markdown rule)
    const unclosedFence = /^([\s\S]*?)```([a-zA-Z0-9_-]*)\n([\s\S]*)$/.exec(tail);
    if (unclosedFence) {
      if (unclosedFence[1]) renderProseSegment(unclosedFence[1]);
      blocks.push(<CodeBlock key={keySeq++} lang={unclosedFence[2]} code={unclosedFence[3]} streaming={true} />);
    } else {
      renderProseSegment(tail);
    }
  }

  return <div className="min-w-0 space-y-1">{blocks}</div>;
});
