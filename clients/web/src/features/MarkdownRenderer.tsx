import { Check, Copy, SquareTerminal, WrapText } from 'lucide-react';
import { type ReactNode, useState } from 'react';
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

const TOKEN_RE =
  /(#[^\n]*)|(\/\/[^\n]*)|(`(?:[^`\\]|\\.)*`|"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')|\b([A-Za-z_$][\w$]*)\b|(--?[A-Za-z][\w-]*)|(\b\d+(?:\.\d+)?\b)/g;

const TOKEN_CLASS = {
  comment: 'text-foreground-subtlest',
  string: 'text-sky-600 dark:text-sky-400',
  keyword: 'text-rose-600 dark:text-rose-400',
  flag: 'text-teal-600 dark:text-teal-400',
  number: 'text-amber-600 dark:text-amber-400',
} as const;

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
      nodes.push(
        <span key={`${lineKey}-${seq++}`} className={TOKEN_CLASS.string}>
          {token}
        </span>,
      );
    } else if (match[4]) {
      nodes.push(
        KEYWORDS.has(token) ? (
          <span key={`${lineKey}-${seq++}`} className={TOKEN_CLASS.keyword}>
            {token}
          </span>
        ) : (
          token
        ),
      );
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

function highlightCode(code: string): ReactNode {
  const lines = code.split('\n');
  return (
    <>
      {lines.map((line, idx) => (
        <span key={idx}>
          {highlightLine(line, idx)}
          {idx < lines.length - 1 ? '\n' : null}
        </span>
      ))}
    </>
  );
}

function CodeBlock({ lang, code }: { lang: string; code: string }) {
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
          <code>{highlightCode(code)}</code>
        </pre>
      </div>
    </div>
  );
}

function renderInline(text: string, depth = 0): ReactNode[] {
  if (!text) return [];
  const tokens = text.split(/(`[^`]+`|\*\*[^*]+\*\*|\[[^\]]+\]\([^)]+\))/g);
  return tokens.map((tok, i) => {
    if (tok.startsWith('`') && tok.endsWith('`') && tok.length >= 2) {
      return (
        <code key={i} className="rounded-md bg-tag px-1.5 py-0.5 font-mono text-ui-sm text-foreground">
          {tok.slice(1, -1)}
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

export function MarkdownRenderer({ content }: { content: string }) {
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
      while (
        i < lines.length &&
        lines[i].trim() !== '' &&
        !lines[i].trim().startsWith('#') &&
        !lines[i].trim().startsWith('> ') &&
        !(lines[i].trim().includes('|') && i + 1 < lines.length && isTableSeparator(lines[i + 1])) &&
        !/^([-*]|\d+\.)\s+/.test(lines[i].trim())
      ) {
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
    blocks.push(<CodeBlock key={keySeq++} lang={match[1]} code={match[2].replace(/\n$/, '')} />);
    lastIndex = match.index + match[0].length;
  }

  if (lastIndex < content.length) {
    renderProseSegment(content.slice(lastIndex));
  }

  return <div className="min-w-0 space-y-1">{blocks}</div>;
}
