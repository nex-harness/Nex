import { Activity } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { apiFetchText, formatCompactTokens } from '../core/protocol';
import { useNexStore } from '../core/store';
import { TurnTelemetry } from '../core/types';
import { formatMsg, type MessageKey } from '../i18n/messages';
import { ContextInspectorModal, ModelTrajectoryModal } from './ModelTrajectoryModal';

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
  turnTelemetries = [],
  sessionTitle,
  contextLimit,
}: {
  telemetry: TurnTelemetry | null;
  turnTelemetries?: TurnTelemetry[];
  sessionTitle?: string;
  contextLimit: number;
}) {
  const locale = useNexStore((s) => s.locale);
  const rootRef = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [inspectorModal, setInspectorModal] = useState<{
    category: string;
    title: string;
    colorVar: string;
    tokens?: number;
    content: string;
  } | null>(null);
  const [trajectoryOpen, setTrajectoryOpen] = useState(false);
  const [loadingBlob, setLoadingBlob] = useState(false);

  // Close popover on outside click or Escape key
  useEffect(() => {
    if (!open) return;
    const onMouseDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false);
      }
    };
    document.addEventListener('mousedown', onMouseDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('mousedown', onMouseDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [open]);

  const used = telemetry?.total_context_tokens || 0;
  const limit = telemetry?.context_window || contextLimit || 200000;
  const pct = limit > 0 ? Math.min(100, Number(((used * 100) / limit).toFixed(1))) : 0;

  const allTelemetries =
    turnTelemetries.length > 0 ? turnTelemetries : telemetry ? [telemetry] : [];
  const hasTrajectory = allTelemetries.some((t) => Boolean(t.raw_trace_blob_hash));

  const inspectCategory = async (
    category: string,
    title: string,
    colorVar: string,
    tokens: number | undefined,
    hash?: string,
    fallbackTraceHash?: string,
  ) => {
    const targetHash = hash || fallbackTraceHash;
    if (!targetHash) return;
    setOpen(false);
    setLoadingBlob(true);
    try {
      let txt = await apiFetchText(`/api/v1/blobs/${targetHash}`);
      // Older sessions without a standalone messages blob fall back to extracting `.messages` from raw trace
      if (!hash && fallbackTraceHash && category === 'messages') {
        try {
          const parsed = JSON.parse(txt);
          if (parsed && Array.isArray(parsed.messages)) {
            txt = JSON.stringify(parsed.messages, null, 2);
          }
        } catch {
          // Keep raw text as fallback
        }
      }
      setInspectorModal({ category, title, colorVar, tokens, content: txt });
    } catch (e) {
      setInspectorModal({
        category,
        title,
        colorVar,
        tokens,
        content: `Failed to fetch context blob: ${(e as Error).message}`,
      });
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
    <div ref={rootRef} className="relative inline-flex items-center">
      <button
        type="button"
        data-testid="context-ring-btn"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        title={`${formatMsg(locale, 'chat.contextUsage.title')}: ${formatCompactTokens(used)} / ${formatCompactTokens(
          limit,
        )} (${pct}%)`}
        className={`flex size-7 items-center justify-center rounded-lg transition-colors hover:bg-hover hover:text-foreground ${
          open ? 'bg-hover text-foreground' : 'text-foreground-subtle'
        }`}
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
          className="zcode-popover-up absolute bottom-9 right-0 z-40 w-80 rounded-xl border border-border bg-popover p-3.5 text-foreground shadow-lg"
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

          <div className="-mx-2 mt-3 space-y-0.5 text-ui-sm">
            {CATEGORY_META.map(({ key, meta }) => {
              const found = telemetry?.breakdown?.find((b) => b.category === key);
              const itemPct = found ? found.pct : 0;
              const blobHash =
                found?.blob_hash || (key === 'system_prompt' ? telemetry?.system_prompt_blob_hash : undefined);
              const fallbackTrace = key === 'messages' ? telemetry?.raw_trace_blob_hash : undefined;
              const clickable = Boolean(blobHash || fallbackTrace);
              const label = formatMsg(locale, meta.labelKey);
              return (
                <button
                  key={key}
                  type="button"
                  disabled={!clickable || loadingBlob}
                  data-testid={`context-cat-${key}`}
                  onClick={() =>
                    clickable && void inspectCategory(key, label, meta.colorVar, found?.tokens, blobHash, fallbackTrace)
                  }
                  title={
                    found?.tokens
                      ? `${label}: ${formatCompactTokens(found.tokens)} tokens (${itemPct.toFixed(1)}%)`
                      : label
                  }
                  className={`flex w-full items-center justify-between rounded-lg px-2 py-1.5 text-left transition-colors ${
                    clickable ? 'cursor-pointer hover:bg-menu-hover' : 'cursor-default opacity-65'
                  }`}
                >
                  <span className="flex min-w-0 items-center gap-2 text-foreground">
                    <span className="size-2 shrink-0 rounded-full" style={{ backgroundColor: meta.colorVar }} />
                    <span className="truncate">{label}</span>
                  </span>
                  <span className="shrink-0 font-mono text-ui-xs tabular-nums text-foreground-subtle">
                    {itemPct.toFixed(1)}%
                  </span>
                </button>
              );
            })}
          </div>

          {hasTrajectory && (
            <div className="mt-3 border-t border-border/70 pt-2.5">
              <button
                type="button"
                data-testid="open-trajectory-btn"
                disabled={loadingBlob}
                onClick={() => {
                  setOpen(false);
                  setTrajectoryOpen(true);
                }}
                className="flex h-8 w-full items-center justify-center gap-1.5 rounded-lg border border-border bg-surface text-ui-sm font-medium text-foreground transition-colors hover:bg-surface-hover disabled:opacity-40"
              >
                <Activity className="size-3.5 text-foreground-subtle" />
                <span>
                  {locale === 'en-US'
                    ? allTelemetries.length > 1
                      ? `Model Trajectory (${allTelemetries.length} calls)`
                      : 'Model Trajectory'
                    : allTelemetries.length > 1
                      ? `查看调用轨迹（${allTelemetries.length} 次）`
                      : '查看调用轨迹'}
                </span>
              </button>
            </div>
          )}
        </div>
      )}

      {inspectorModal && (
        <ContextInspectorModal
          category={inspectorModal.category}
          title={inspectorModal.title}
          colorVar={inspectorModal.colorVar}
          tokens={inspectorModal.tokens}
          rawContent={inspectorModal.content}
          locale={locale}
          onClose={() => setInspectorModal(null)}
        />
      )}

      {trajectoryOpen && (
        <ModelTrajectoryModal
          telemetries={allTelemetries}
          sessionTitle={sessionTitle}
          locale={locale}
          onClose={() => setTrajectoryOpen(false)}
        />
      )}
    </div>
  );
}
