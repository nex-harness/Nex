import { useState } from 'react';
import { apiFetchText, formatCompactTokens } from '../core/protocol';
import { TurnTelemetry } from '../core/types';

const CATEGORY_LABELS: Record<string, { label: string; color: string }> = {
  messages: { label: 'Messages', color: '#3b82f6' },
  system_tools: { label: 'System tools', color: '#60a5fa' },
  system_prompt: { label: 'System prompt', color: '#2563eb' },
  mcp_tools: { label: 'MCP tools', color: '#818cf8' },
  skills: { label: 'Skills', color: '#64748b' },
  meta_context: { label: 'Meta context', color: '#475569' },
};

export function ContextWindowsButton({
  telemetry,
  contextLimit,
}: {
  telemetry: TurnTelemetry | null;
  contextLimit: number;
}) {
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

  const radius = 7;
  const circ = 2 * Math.PI * radius;
  const dashOffset = circ - (pct / 100) * circ;

  return (
    <div className="relative inline-flex items-center">
      <button
        type="button"
        data-testid="context-ring-btn"
        onClick={() => setOpen((v) => !v)}
        title={`Context: ${formatCompactTokens(used)} / ${formatCompactTokens(limit)} (${pct}%)`}
        className="flex items-center gap-1.5 rounded-md px-2 py-1 text-xs text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200 transition"
      >
        <svg width="18" height="18" className="-rotate-90">
          <circle cx="9" cy="9" r={radius} stroke="#3f3f46" strokeWidth="2.2" fill="none" />
          <circle
            cx="9"
            cy="9"
            r={radius}
            stroke="#3b82f6"
            strokeWidth="2.2"
            fill="none"
            strokeDasharray={circ}
            strokeDashoffset={dashOffset}
            strokeLinecap="round"
          />
        </svg>
        <span className="font-mono text-[11px]">{pct}%</span>
      </button>

      {open && (
        <div
          data-testid="context-popover"
          className="absolute bottom-9 right-0 z-40 w-80 rounded-xl border border-zinc-800 bg-zinc-900/95 p-4 shadow-2xl backdrop-blur"
        >
          <div className="flex items-center justify-between text-xs">
            <span className="font-semibold text-zinc-100">Context windows</span>
            <span className="font-mono text-zinc-400">
              {formatCompactTokens(used)}/{formatCompactTokens(limit)} ({pct}%)
            </span>
          </div>

          <div className="mt-2.5 h-1.5 w-full overflow-hidden rounded-full bg-zinc-800">
            <div className="h-full rounded-full bg-blue-500 transition-all" style={{ width: `${Math.max(pct, 1.5)}%` }} />
          </div>

          {telemetry && (
            <div className="mt-2 flex items-center justify-between text-[11px] text-zinc-400">
              <span>Cache hit: {telemetry.cache_hit_rate_pct.toFixed(1)}%</span>
              <span>Est. turn: ${telemetry.estimated_cost_usd.toFixed(4)}</span>
            </div>
          )}

          <div className="mt-3 space-y-1.5 text-xs">
            {['messages', 'system_tools', 'system_prompt', 'mcp_tools', 'skills', 'meta_context'].map((catKey) => {
              const found = telemetry?.breakdown?.find((b) => b.category === catKey);
              const meta = CATEGORY_LABELS[catKey] || { label: catKey, color: '#64748b' };
              const itemPct = found ? found.pct : 0;
              return (
                <div
                  key={catKey}
                  onClick={() => found?.blob_hash && inspectBlob(meta.label, found.blob_hash)}
                  className={`flex items-center justify-between rounded px-1.5 py-0.5 ${
                    found?.blob_hash ? 'cursor-pointer hover:bg-zinc-800' : ''
                  }`}
                >
                  <span className="flex items-center gap-2 text-zinc-300">
                    <span className="h-2 w-2 rounded-full" style={{ backgroundColor: meta.color }} />
                    {meta.label}
                  </span>
                  <span className="font-mono text-zinc-400">{itemPct}%</span>
                </div>
              );
            })}
          </div>

          <div className="mt-3.5 flex gap-2">
            <button
              type="button"
              disabled={!telemetry?.system_prompt_blob_hash || loadingBlob}
              onClick={() => inspectBlob('System Prompt', telemetry?.system_prompt_blob_hash)}
              className="flex-1 rounded-lg border border-zinc-700 bg-zinc-800/80 py-1.5 text-xs font-medium text-zinc-200 hover:bg-zinc-700 disabled:opacity-40"
            >
              {loadingBlob ? 'Loading...' : 'Show system prompt'}
            </button>
            {telemetry?.raw_trace_blob_hash && (
              <button
                type="button"
                disabled={loadingBlob}
                onClick={() => inspectBlob('Full Turn Trace (JSON)', telemetry.raw_trace_blob_hash)}
                className="rounded-lg border border-zinc-700 bg-zinc-800/80 px-2.5 py-1.5 text-xs text-zinc-300 hover:bg-zinc-700"
              >
                Trace
              </button>
            )}
          </div>
        </div>
      )}

      {traceModal && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-6">
          <div className="flex max-h-[85vh] w-full max-w-4xl flex-col rounded-xl border border-zinc-800 bg-zinc-950 shadow-2xl">
            <div className="flex items-center justify-between border-b border-zinc-800 px-4 py-3">
              <h3 className="text-sm font-semibold text-zinc-100">{traceModal.title}</h3>
              <button
                type="button"
                onClick={() => setTraceModal(null)}
                className="rounded px-2 py-1 text-xs text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
              >
                Close
              </button>
            </div>
            <pre className="flex-1 overflow-auto p-4 font-mono text-xs whitespace-pre-wrap text-zinc-300">
              {traceModal.content}
            </pre>
          </div>
        </div>
      )}
    </div>
  );
}
