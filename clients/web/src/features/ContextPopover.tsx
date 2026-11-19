import { X } from 'lucide-react';
import { useState } from 'react';
import { apiFetchText, formatCompactTokens } from '../core/protocol';
import { useNexStore } from '../core/store';
import { TurnTelemetry } from '../core/types';
import { formatMsg, type MessageKey } from '../i18n/messages';

interface CategoryMeta {
  labelKey: MessageKey;
  colorVar: string;
}

const CATEGORY_META: Array<{ key: string; meta: CategoryMeta }> = [
  { key: 'messages', meta: { labelKey: 'chat.contextUsage.messages', colorVar: 'var(--color-ctx-1)' } },
  { key: 'system_tools', meta: { labelKey: 'chat.contextUsage.systemTools', colorVar: 'var(--color-ctx-2)' } },
  { key: 'system_prompt', meta: { labelKey: 'chat.contextUsage.systemPrompt', colorVar: 'var(--color-ctx-3)' } },
  { key: 'mcp_tools', meta: { labelKey: 'chat.contextUsage.mcpTools', colorVar: 'var(--color-ctx-4)' } },
  { key: 'skills', meta: { labelKey: 'chat.contextUsage.skills', colorVar: 'var(--color-ctx-5)' } },
  { key: 'meta_context', meta: { labelKey: 'chat.contextUsage.metaUserContext', colorVar: 'var(--color-ctx-6)' } },
];

export function ContextWindowsButton({
  telemetry,
  contextLimit,
}: {
  telemetry: TurnTelemetry | null;
  contextLimit: number;
}) {
  const locale = useNexStore((s) => s.locale);
  const [open, setOpen] = useState(false);
  const [traceModal, setTraceModal] = useState<{ title: string; content: string } | null>(null);
  const [loadingBlob, setLoadingBlob] = useState(false);

  const used = telemetry?.total_context_tokens || 0;
  const limit = telemetry?.context_window || contextLimit || 200000;
  const pct = limit > 0 ? Math.min(100, Number(((used * 100) / limit).toFixed(1))) : 0;

  const inspectBlob = async (title: string, hash?: string) => {
    if (!hash) return;
    setLoadingBlob(true);
    try {
      const txt = await apiFetchText(`/api/v1/blobs/${hash}`);
      setTraceModal({ title, content: txt });
    } catch (e) {
      setTraceModal({ title, content: `Failed to fetch trace blob: ${(e as Error).message}` });
    } finally {
      setLoadingBlob(false);
    }
  };

  const radius = 5.4;
  const size = 14;
  const center = size / 2;
  const circ = 2 * Math.PI * radius;
  const dashOffset = circ - (pct / 100) * circ;

  return (
    <div className="relative inline-flex items-center">
      <button
        type="button"
        data-testid="context-ring-btn"
        onClick={() => setOpen((v) => !v)}
        title={`${formatMsg(locale, 'chat.contextUsage.title')}: ${formatCompactTokens(used)} / ${formatCompactTokens(
          limit,
        )} (${pct}%)`}
        className="flex size-7 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
      >
        <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} className="-rotate-90">
          <circle cx={center} cy={center} r={radius} stroke="var(--color-border-hover)" strokeWidth="1.6" fill="none" />
          <circle
            cx={center}
            cy={center}
            r={radius}
            stroke="var(--color-brand-blue)"
            strokeWidth="1.6"
            fill="none"
            strokeDasharray={circ}
            strokeDashoffset={dashOffset}
            strokeLinecap="round"
          />
        </svg>
      </button>

      {open && (
        <div
          data-testid="context-popover"
          className="absolute bottom-9 right-0 z-40 w-80 rounded-xl border border-border bg-popover p-3.5 text-foreground shadow-lg"
        >
          <div className="flex items-center justify-between text-ui-sm">
            <span className="font-semibold text-foreground">{formatMsg(locale, 'chat.contextUsage.title')}</span>
            <span className="font-mono text-ui-xs tabular-nums text-foreground-subtle">
              {formatCompactTokens(used)}/{formatCompactTokens(limit)} ({pct}%)
            </span>
          </div>

          <div className="mt-2.5 h-1.5 w-full overflow-hidden rounded-full bg-surface">
            <div className="h-full rounded-full bg-brand-blue transition-all" style={{ width: `${Math.max(pct, 2)}%` }} />
          </div>

          {telemetry && (
            <div className="mt-2 flex items-center justify-between text-ui-xs tabular-nums text-foreground-subtle">
              <span>
                {formatMsg(locale, 'chat.contextUsage.cacheHitRate')}: {telemetry.cache_hit_rate_pct.toFixed(1)}%
              </span>
              <span>${telemetry.estimated_cost_usd.toFixed(4)}</span>
            </div>
          )}

          <div className="mt-3 space-y-0.5 text-ui-sm">
            {CATEGORY_META.map(({ key, meta }) => {
              const found = telemetry?.breakdown?.find((b) => b.category === key);
              const itemPct = found ? found.pct : 0;
              return (
                <div
                  key={key}
                  onClick={() => found?.blob_hash && void inspectBlob(formatMsg(locale, meta.labelKey), found.blob_hash)}
                  className={`flex items-center justify-between rounded-md px-1.5 py-1 ${
                    found?.blob_hash ? 'cursor-pointer hover:bg-menu-hover' : ''
                  }`}
                >
                  <span className="flex min-w-0 items-center gap-2 text-foreground">
                    <span className="size-2 shrink-0 rounded-full" style={{ backgroundColor: meta.colorVar }} />
                    <span className="truncate">{formatMsg(locale, meta.labelKey)}</span>
                  </span>
                  <span className="shrink-0 font-mono text-ui-xs tabular-nums text-foreground-subtle">
                    {itemPct.toFixed(1)}%
                  </span>
                </div>
              );
            })}
          </div>

          <div className="mt-3 flex gap-2">
            <button
              type="button"
              disabled={!telemetry?.system_prompt_blob_hash || loadingBlob}
              onClick={() =>
                void inspectBlob(formatMsg(locale, 'chat.contextUsage.systemPrompt'), telemetry?.system_prompt_blob_hash)
              }
              className="h-7 flex-1 rounded-lg border border-border bg-surface text-ui-sm font-medium text-foreground transition-colors hover:bg-surface-hover disabled:opacity-40"
            >
              {loadingBlob
                ? locale === 'en-US'
                  ? 'Loading...'
                  : '加载中...'
                : locale === 'en-US'
                  ? 'Show system prompt'
                  : '查看系统提示词'}
            </button>
            {telemetry?.raw_trace_blob_hash && (
              <button
                type="button"
                disabled={loadingBlob}
                onClick={() =>
                  void inspectBlob(
                    locale === 'en-US' ? 'Full Turn Trace (JSON)' : '完整轮次轨迹 (JSON)',
                    telemetry.raw_trace_blob_hash,
                  )
                }
                className="h-7 rounded-lg border border-border bg-surface px-2.5 text-ui-sm text-foreground-subtle transition-colors hover:bg-surface-hover hover:text-foreground"
              >
                {locale === 'en-US' ? 'Trace' : '轨迹'}
              </button>
            )}
          </div>
        </div>
      )}

      {traceModal && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-6 backdrop-blur-[1px]">
          <div className="flex max-h-[85vh] w-full max-w-4xl flex-col rounded-2xl border border-border bg-popover shadow-xl">
            <div className="flex items-center justify-between border-b border-border px-4 py-3">
              <h3 className="text-ui-base font-semibold text-foreground">{traceModal.title}</h3>
              <button
                type="button"
                onClick={() => setTraceModal(null)}
                className="flex size-7 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
              >
                <X className="size-4" />
              </button>
            </div>
            <pre className="flex-1 overflow-auto p-4 font-mono text-ui-sm whitespace-pre-wrap text-foreground">
              {traceModal.content}
            </pre>
          </div>
        </div>
      )}
    </div>
  );
}
