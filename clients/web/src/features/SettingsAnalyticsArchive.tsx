import { formatMsg } from '../i18n/messages';
import { ArchiveRestore, BarChart3, Trash2 } from 'lucide-react';
import { useEffect, useState } from 'react';
import { apiFetch, formatCompactTokens } from '../core/protocol';
import { useNexStore } from '../core/store';
import { SessionRecord, UsageSummary, UsageTimePoint } from '../core/types';
import {
  SettingsGroupCard,
  SettingsRow,
  settingsInputClass,
  settingsSelectClass,
} from './SettingsPage';

const EMPTY_USAGE: UsageSummary = {
  window: 'today',
  turn_count: 0,
  input_tokens: 0,
  output_tokens: 0,
  cache_read_tokens: 0,
  cache_write_tokens: 0,
  total_tokens: 0,
  cache_hit_rate_pct: 0,
  estimated_cost_usd: 0,
  cache_saved_usd: 0,
  by_source_tokens: { chat: 0, subagent: 0, cron: 0 },
  models: [],
  series: [],
};

const WINDOWS = [
  { id: 'today' as const, labelKey: 'analytics.window.today' as const },
  { id: '7d' as const, labelKey: 'analytics.window.7d' as const },
  { id: '30d' as const, labelKey: 'analytics.window.30d' as const },
];

type ChartMetric = 'tokens' | 'cost' | 'turns';

function formatYAxisTick(metric: ChartMetric, value: number): string {
  if (metric === 'cost') {
    if (value === 0) return '$0';
    if (value < 0.01) return `$${value.toFixed(4)}`;
    if (value < 1) return `$${value.toFixed(2)}`;
    return `$${value.toFixed(1)}`;
  }
  if (metric === 'turns') {
    return `${Math.round(value)}`;
  }
  return formatCompactTokens(Math.round(value));
}

function UsageTrendChart({
  series,
  windowTab,
  totalTokens,
  inputTokens,
  outputTokens,
  cacheReadTokens,
  cacheWriteTokens,
}: {
  series: UsageTimePoint[];
  windowTab: 'today' | '7d' | '30d';
  totalTokens: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
}) {
  const locale = useNexStore((s) => s.locale);
  const [metric, setMetric] = useState<ChartMetric>('tokens');
  const [hoveredIdx, setHoveredIdx] = useState<number | null>(null);

  const safeSeries = series || [];
  const count = safeSeries.length;
  const maxValue = safeSeries.reduce((acc, pt) => {
    const v =
      metric === 'tokens' ? pt.total_tokens : metric === 'cost' ? pt.estimated_cost_usd : pt.turn_count;
    return v > acc ? v : acc;
  }, 0);
  const scaleMax = maxValue > 0 ? maxValue : 1;

  // Pick highlighted point: hovered > most recent non-zero point > null
  let inspectIdx = hoveredIdx;
  if (inspectIdx === null && count > 0) {
    for (let i = count - 1; i >= 0; i--) {
      const pt = safeSeries[i];
      if (pt.total_tokens > 0 || pt.turn_count > 0 || pt.estimated_cost_usd > 0) {
        inspectIdx = i;
        break;
      }
    }
  }
  const activePoint = inspectIdx !== null && inspectIdx >= 0 && inspectIdx < count ? safeSeries[inspectIdx] : null;

  const svgWidth = 720;
  const svgHeight = 196;
  const padLeft = 48;
  const padRight = 12;
  const padTop = 14;
  const padBottom = 26;
  const plotW = svgWidth - padLeft - padRight;
  const plotH = svgHeight - padTop - padBottom;
  const stepW = count > 0 ? plotW / count : plotW;
  const barW = Math.max(4, Math.min(26, stepW * 0.62));

  const compTotal = Math.max(1, inputTokens + outputTokens + cacheReadTokens + cacheWriteTokens);
  const inputPct = (inputTokens / compTotal) * 100;
  const outputPct = (outputTokens / compTotal) * 100;
  const cacheReadPct = (cacheReadTokens / compTotal) * 100;
  const cacheWritePct = (cacheWriteTokens / compTotal) * 100;

  return (
    <div
      data-testid="usage-trend-chart"
      className="rounded-2xl border border-border bg-card px-5 py-4 space-y-4"
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <BarChart3 className="size-4 text-brand-blue" />
          <div>
            <h3 className="text-ui-base font-medium text-foreground">
              {formatMsg(locale, 'analytics.usageTrend')}
            </h3>
            <p className="text-ui-xs text-foreground-subtlest">
              {windowTab === 'today'
                ? formatMsg(locale, 'analytics.trendWindowHourly')
                : formatMsg(locale, 'analytics.trendWindowDaily', { days: windowTab === '7d' ? 7 : 30 })}
            </p>
          </div>
        </div>

        <div className="inline-flex items-center gap-0.5 rounded-lg border border-border bg-surface p-0.5">
          {(
            [
              { id: 'tokens', labelKey: 'analytics.metric.tokens' },
              { id: 'cost', labelKey: 'analytics.metric.cost' },
              { id: 'turns', labelKey: 'analytics.metric.turns' },
            ] as const
          ).map((m) => (
            <button
              key={m.id}
              type="button"
              data-testid={`usage-chart-metric-${m.id}`}
              onClick={() => setMetric(m.id)}
              className={`h-7 rounded-md px-2.5 text-ui-xs transition-colors ${
                metric === m.id
                  ? 'bg-card font-medium text-foreground shadow-2xs'
                  : 'text-foreground-subtle hover:text-foreground'
              }`}
            >
              {formatMsg(locale, m.labelKey)}
            </button>
          ))}
        </div>
      </div>

      {/* Token Composition Ratio Strip */}
      {totalTokens > 0 && metric === 'tokens' && (
        <div className="space-y-2">
          <div className="flex h-2 w-full overflow-hidden rounded-full bg-surface">
            {inputPct > 0 && (
              <div
                className="h-full bg-sky-500 transition-all"
                style={{ width: `${inputPct}%` }}
              />
            )}
            {outputPct > 0 && (
              <div
                className="h-full bg-violet-500 transition-all"
                style={{ width: `${outputPct}%` }}
              />
            )}
            {cacheReadPct > 0 && (
              <div
                className="h-full bg-emerald-500 transition-all"
                style={{ width: `${cacheReadPct}%` }}
              />
            )}
            {cacheWritePct > 0 && (
              <div
                className="h-full bg-amber-500 transition-all"
                style={{ width: `${cacheWritePct}%` }}
              />
            )}
          </div>
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-ui-xs text-foreground-subtle">
            <span className="inline-flex items-center gap-1.5">
              <span className="size-2 rounded-full bg-sky-500" />
              {formatMsg(locale, 'analytics.legend.input')}:{' '}
              <span className="font-mono text-foreground">{formatCompactTokens(inputTokens)}</span>
            </span>
            <span className="inline-flex items-center gap-1.5">
              <span className="size-2 rounded-full bg-violet-500" />
              {formatMsg(locale, 'analytics.legend.output')}:{' '}
              <span className="font-mono text-foreground">{formatCompactTokens(outputTokens)}</span>
            </span>
            <span className="inline-flex items-center gap-1.5">
              <span className="size-2 rounded-full bg-emerald-500" />
              {formatMsg(locale, 'analytics.legend.cacheRead')}:{' '}
              <span className="font-mono text-foreground">{formatCompactTokens(cacheReadTokens)}</span>
            </span>
            {cacheWriteTokens > 0 && (
              <span className="inline-flex items-center gap-1.5">
                <span className="size-2 rounded-full bg-amber-500" />
                {formatMsg(locale, 'analytics.legend.cacheWrite')}:{' '}
                <span className="font-mono text-foreground">{formatCompactTokens(cacheWriteTokens)}</span>
              </span>
            )}
          </div>
        </div>
      )}

      {/* SVG Column Chart */}
      {count === 0 ? (
        <div className="py-10 text-center text-ui-sm text-foreground-subtlest">
          {formatMsg(locale, 'analytics.noModelCallsInThis')}
        </div>
      ) : (
        <>
          <div className="w-full overflow-hidden">
            <svg
              viewBox={`0 0 ${svgWidth} ${svgHeight}`}
              className="h-44 w-full select-none overflow-visible"
              onMouseLeave={() => setHoveredIdx(null)}
            >
              {[0, 0.5, 1].map((frac) => {
                const y = padTop + plotH * (1 - frac);
                const tickVal = maxValue * frac;
                return (
                  <g key={frac}>
                    <line
                      x1={padLeft}
                      y1={y}
                      x2={svgWidth - padRight}
                      y2={y}
                      stroke="currentColor"
                      strokeDasharray={frac === 0 ? undefined : '3 3'}
                      className="text-border"
                    />
                    <text
                      x={padLeft - 8}
                      y={y + 3.5}
                      textAnchor="end"
                      className="fill-foreground-subtlest font-mono text-[10px]"
                    >
                      {formatYAxisTick(metric, tickVal)}
                    </text>
                  </g>
                );
              })}

              {safeSeries.map((pt, idx) => {
                const cx = padLeft + stepW * idx + stepW / 2;
                const x = cx - barW / 2;
                const rawVal =
                  metric === 'tokens'
                    ? pt.total_tokens
                    : metric === 'cost'
                      ? pt.estimated_cost_usd
                      : pt.turn_count;
                const totalH = maxValue > 0 && rawVal > 0 ? Math.max(2, (rawVal / scaleMax) * plotH) : 0;
                const isHovered = hoveredIdx === idx;
                const showXLabel =
                  idx === 0 ||
                  idx === count - 1 ||
                  count <= 8 ||
                  (count === 24 && idx % 3 === 0) ||
                  (count > 24 && idx % 5 === 0);

                // Stacked token segments inside totalH
                const ptTokSum = Math.max(
                  1,
                  pt.input_tokens + pt.output_tokens + pt.cache_read_tokens + pt.cache_write_tokens,
                );
                const hCacheRead = totalH * (pt.cache_read_tokens / ptTokSum);
                const hCacheWrite = totalH * (pt.cache_write_tokens / ptTokSum);
                const hOutput = totalH * (pt.output_tokens / ptTokSum);
                const hInput = Math.max(0, totalH - hCacheRead - hCacheWrite - hOutput);

                const baseY = padTop + plotH;

                return (
                  <g
                    key={pt.bucket_unix_sec || idx}
                    data-testid={`usage-bar-${idx}`}
                    onMouseEnter={() => setHoveredIdx(idx)}
                    className="cursor-pointer"
                  >
                    <rect
                      x={padLeft + stepW * idx}
                      y={padTop}
                      width={stepW}
                      height={plotH}
                      rx={4}
                      className={isHovered ? 'fill-hover/60' : 'fill-transparent'}
                    />

                    {metric === 'tokens' && totalH > 0 ? (
                      <>
                        {hInput > 0 && (
                          <rect
                            x={x}
                            y={baseY - hInput}
                            width={barW}
                            height={hInput}
                            rx={1.5}
                            className="fill-sky-500 transition-opacity"
                            opacity={isHovered ? 1 : 0.88}
                          />
                        )}
                        {hOutput > 0 && (
                          <rect
                            x={x}
                            y={baseY - hInput - hOutput}
                            width={barW}
                            height={hOutput}
                            rx={1.5}
                            className="fill-violet-500 transition-opacity"
                            opacity={isHovered ? 1 : 0.88}
                          />
                        )}
                        {hCacheRead > 0 && (
                          <rect
                            x={x}
                            y={baseY - hInput - hOutput - hCacheRead}
                            width={barW}
                            height={hCacheRead}
                            rx={1.5}
                            className="fill-emerald-500 transition-opacity"
                            opacity={isHovered ? 1 : 0.88}
                          />
                        )}
                        {hCacheWrite > 0 && (
                          <rect
                            x={x}
                            y={baseY - totalH}
                            width={barW}
                            height={hCacheWrite}
                            rx={1.5}
                            className="fill-amber-500 transition-opacity"
                            opacity={isHovered ? 1 : 0.88}
                          />
                        )}
                      </>
                    ) : totalH > 0 ? (
                      <rect
                        x={x}
                        y={baseY - totalH}
                        width={barW}
                        height={totalH}
                        rx={3}
                        className={metric === 'cost' ? 'fill-emerald-500' : 'fill-brand-blue'}
                        opacity={isHovered ? 1 : 0.85}
                      />
                    ) : (
                      <line
                        x1={x}
                        y1={baseY - 0.5}
                        x2={x + barW}
                        y2={baseY - 0.5}
                        stroke="currentColor"
                        strokeWidth={1}
                        className="text-border"
                      />
                    )}

                    {showXLabel && (
                      <text
                        x={cx}
                        y={svgHeight - 6}
                        textAnchor="middle"
                        className={`font-mono text-[10px] ${
                          isHovered ? 'fill-foreground font-medium' : 'fill-foreground-subtlest'
                        }`}
                      >
                        {pt.label}
                      </text>
                    )}
                  </g>
                );
              })}
            </svg>
          </div>

          <div
            data-testid="usage-chart-inspector"
            className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-border bg-surface/70 px-3.5 py-2 text-ui-xs text-foreground-subtle"
          >
            {activePoint ? (
              <>
                <span className="font-mono font-medium text-foreground">{activePoint.label}</span>
                <div className="flex flex-wrap items-center gap-x-4 gap-y-1 font-mono tabular-nums">
                  <span>
                    {formatMsg(locale, 'analytics.totalTokens')}:{' '}
                    <strong className="text-foreground">{formatCompactTokens(activePoint.total_tokens)}</strong>
                  </span>
                  <span>
                    {formatMsg(locale, 'analytics.legend.input')}:{' '}
                    <span className="text-foreground">{formatCompactTokens(activePoint.input_tokens)}</span>
                  </span>
                  <span>
                    {formatMsg(locale, 'analytics.legend.output')}:{' '}
                    <span className="text-foreground">{formatCompactTokens(activePoint.output_tokens)}</span>
                  </span>
                  <span>
                    {formatMsg(locale, 'analytics.legend.cacheRead')}:{' '}
                    <span className="text-foreground">{formatCompactTokens(activePoint.cache_read_tokens)}</span>
                  </span>
                  <span className="text-success">${activePoint.estimated_cost_usd.toFixed(4)}</span>
                  <span>{formatMsg(locale, 'analytics.turns', { turn_count: activePoint.turn_count })}</span>
                </div>
              </>
            ) : (
              <span className="text-foreground-subtlest">{formatMsg(locale, 'analytics.hoverToInspect')}</span>
            )}
          </div>
        </>
      )}
    </div>
  );
}

function MetricCard({
  label,
  value,
  hint,
  tone = 'default',
}: {
  label: string;
  value: string;
  hint?: string;
  tone?: 'default' | 'accent' | 'success';
}) {
  const valueTone =
    tone === 'accent' ? 'text-brand-blue' : tone === 'success' ? 'text-success' : 'text-foreground';
  return (
    <div className="rounded-2xl border border-border bg-card px-5 py-4">
      <div className="text-ui-xs text-foreground-subtle">{label}</div>
      <div className={`mt-1.5 font-mono text-ui-xl font-semibold tabular-nums ${valueTone}`}>
        {value}
      </div>
      {hint ? <div className="mt-1 text-ui-xs text-foreground-subtlest">{hint}</div> : null}
    </div>
  );
}

export function UsageAnalyticsSection() {
  const { locale, runners, selectedRunnerId, todaySummary } = useNexStore();

  const [runnerFilter, setRunnerFilter] = useState<string>('');
  const [windowTab, setWindowTab] = useState<'today' | '7d' | '30d'>('today');
  const [stats, setStats] = useState<UsageSummary>(todaySummary || EMPTY_USAGE);

  useEffect(() => {
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || 'Asia/Shanghai';
    const q = new URLSearchParams({ window: windowTab, tz });
    if (runnerFilter) q.set('runner_id', runnerFilter);
    void apiFetch<UsageSummary>(`/api/v1/stats?${q.toString()}`)
      .then(setStats)
      .catch(() => {});
  }, [runnerFilter, windowTab, selectedRunnerId]);

  const safeModels = Array.isArray(stats?.models) ? stats.models : [];
  const maxModelTok = Math.max(1, ...safeModels.map((m) => m.total_tokens));

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-2">
        {/* Segmented window tabs */}
        <div className="inline-flex items-center gap-0.5 rounded-lg border border-border bg-surface p-0.5">
          {WINDOWS.map((w) => (
            <button
              key={w.id}
              type="button"
              onClick={() => setWindowTab(w.id)}
              className={`h-7 rounded-md px-3 text-ui-sm transition-colors ${
                windowTab === w.id
                  ? 'bg-card font-medium text-foreground shadow-2xs'
                  : 'text-foreground-subtle hover:text-foreground'
              }`}
            >
              {formatMsg(locale, w.labelKey)}
            </button>
          ))}
        </div>

        <select
          value={runnerFilter}
          onChange={(e) => setRunnerFilter(e.target.value)}
          aria-label={formatMsg(locale, 'analytics.filterByRunner')}
          className={`${settingsSelectClass} h-8 w-full max-w-[220px] text-ui-sm`}
        >
          <option value="">{formatMsg(locale, 'analytics.allRunners')}</option>
          {runners.map((r) => (
            <option key={r.id} value={r.id}>
              {r.name} ({r.os}/{r.arch})
            </option>
          ))}
        </select>
      </div>

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <MetricCard
          label={formatMsg(locale, 'analytics.totalTokens')}
          value={formatCompactTokens(stats.total_tokens || 0)}
          hint={formatMsg(locale, 'analytics.modelTurns', { turn_count: stats.turn_count || 0 })}
        />
        <MetricCard
          label={formatMsg(locale, 'analytics.estimatedCost')}
          value={`$${(stats.estimated_cost_usd || 0).toFixed(4)}`}
          hint={
            formatMsg(locale, 'analytics.cacheSaved', { v: (stats.cache_saved_usd || 0).toFixed(4) })
          }
          tone="success"
        />
        <MetricCard
          label={formatMsg(locale, 'analytics.cacheHitRate')}
          value={`${(stats.cache_hit_rate_pct || 0).toFixed(1)}%`}
          hint={
            formatMsg(locale, 'analytics.tokRead', { v: formatCompactTokens(stats.cache_read_tokens || 0) })
          }
          tone="accent"
        />
        <MetricCard
          label={formatMsg(locale, 'analytics.sourceBreakdown')}
          value={formatCompactTokens(
            (stats.by_source_tokens?.chat || 0) +
              (stats.by_source_tokens?.subagent || 0) +
              (stats.by_source_tokens?.cron || 0),
          )}
          hint={
            formatMsg(locale, 'analytics.chatSubCron', { v: formatCompactTokens(stats.by_source_tokens?.chat || 0), v2: formatCompactTokens(
                  stats.by_source_tokens?.subagent || 0,
                ), v3: formatCompactTokens(stats.by_source_tokens?.cron || 0) })
          }
        />
      </div>

      <UsageTrendChart
        series={stats.series || []}
        windowTab={windowTab}
        totalTokens={stats.total_tokens || 0}
        inputTokens={stats.input_tokens || 0}
        outputTokens={stats.output_tokens || 0}
        cacheReadTokens={stats.cache_read_tokens || 0}
        cacheWriteTokens={stats.cache_write_tokens || 0}
      />

      <div className="space-y-3">
        <h3 className="text-ui-base font-medium text-foreground">
          {formatMsg(locale, 'analytics.modelUsageBreakdown')}
        </h3>
        <SettingsGroupCard>
          {safeModels.length === 0 ? (
            <div className="px-5 py-10 text-center text-ui-sm text-foreground-subtlest">
              {formatMsg(locale, 'analytics.noModelCallsInThis')}
            </div>
          ) : (
            safeModels.map((m) => {
              const ratio = Math.max(2, Math.round((m.total_tokens / maxModelTok) * 100));
              return (
                <div key={`${m.provider_id}/${m.model_id}`} className="space-y-1.5 px-5 py-3.5">
                  <div className="flex flex-wrap items-center justify-between gap-2 text-ui-sm">
                    <div className="flex min-w-0 flex-wrap items-center gap-1.5">
                      <span className="font-mono font-medium text-foreground">{m.model_id}</span>
                      <span className="shrink-0 rounded-md border border-border bg-surface px-1.5 py-0.5 font-mono text-ui-xs text-foreground-subtle">
                        {m.provider_id}
                      </span>
                      <span className="text-ui-xs text-foreground-subtlest">
                        {formatMsg(locale, 'analytics.turns', { turn_count: m.turn_count })}
                      </span>
                    </div>
                    <div className="font-mono text-ui-sm tabular-nums text-foreground">
                      {formatCompactTokens(m.total_tokens)} tok ·{' '}
                      <span className="text-success">${m.estimated_cost_usd.toFixed(4)}</span>
                    </div>
                  </div>
                  <div className="h-1.5 w-full overflow-hidden rounded-full bg-surface">
                    <div className="h-full rounded-full bg-brand-blue" style={{ width: `${ratio}%` }} />
                  </div>
                </div>
              );
            })
          )}
        </SettingsGroupCard>
      </div>
    </div>
  );
}

export function ArchivesSettingsSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const {
    locale,
    preferences,
    archivedSessions,
    refreshInitialData,
    unarchiveSession,
    deleteArchivedPermanently,
  } = useNexStore();

  const [days, setDays] = useState(preferences.auto_archive_after_days);
  const [search, setSearch] = useState('');
  const [sortOrder, setSortOrder] = useState<'updated' | 'created'>('updated');

  const sortedSessions = [...archivedSessions].sort((a: SessionRecord, b: SessionRecord) =>
    sortOrder === 'updated' ? b.last_active_at - a.last_active_at : b.created_at - a.created_at,
  );
  const query = search.trim().toLowerCase();
  const visibleSessions = query
    ? sortedSessions.filter(
        (s) => s.title.toLowerCase().includes(query) || s.id.toLowerCase().includes(query),
      )
    : sortedSessions;

  const savePolicy = async () => {
    await apiFetch('/api/v1/preferences', {
      method: 'PUT',
      body: JSON.stringify({ ...preferences, auto_archive_after_days: days }),
    }).catch(() => {});
    await refreshInitialData();
    onFlash(
      formatMsg(locale, 'analytics.autoArchivePolicyUpdatedDays', { days }),
    );
  };

  return (
    <div className="space-y-6">
      <SettingsGroupCard>
        <SettingsRow
          label={formatMsg(locale, 'analytics.autoArchivePolicy')}
          description={
            formatMsg(locale, 'analytics.idleSessionsMoveFromMain')
          }
        >
          <input
            type="number"
            min={0}
            value={days}
            onChange={(e) => setDays(Number(e.target.value))}
            className="h-9 w-20 rounded-lg border border-border bg-input px-3 font-mono text-ui-base tabular-nums text-foreground"
          />
          <span className="shrink-0 text-ui-sm text-foreground-subtle">
            {formatMsg(locale, 'analytics.days')}
          </span>
          <button
            type="button"
            onClick={() => void savePolicy()}
            className="h-8 shrink-0 rounded-lg bg-primary px-3.5 text-ui-sm font-medium text-primary-foreground transition-opacity hover:opacity-90"
          >
            {formatMsg(locale, 'common.save')}
          </button>
        </SettingsRow>
      </SettingsGroupCard>

      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex h-8 min-w-0 flex-1 items-center gap-1.5 rounded-lg border border-border bg-input px-2.5 sm:max-w-xs">
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={formatMsg(locale, 'analytics.searchArchivedSessions')}
            className={`${settingsInputClass} h-7 border-0 bg-transparent p-0 text-ui-sm`}
          />
        </div>
        <select
          value={sortOrder}
          onChange={(e) => setSortOrder(e.target.value as 'updated' | 'created')}
          aria-label={formatMsg(locale, 'analytics.sortOrder')}
          className={`${settingsSelectClass} h-8 w-full max-w-[180px] text-ui-sm`}
        >
          <option value="updated">{formatMsg(locale, 'analytics.lastUpdated')}</option>
          <option value="created">{formatMsg(locale, 'analytics.created')}</option>
        </select>
      </div>

      <div className="space-y-3">
        <h3 className="text-ui-base font-medium text-foreground">
          {formatMsg(locale, 'analytics.archivedSessions', { length: visibleSessions.length })}
        </h3>
        <SettingsGroupCard>
          {visibleSessions.length === 0 ? (
            <div className="px-5 py-10 text-center text-ui-sm text-foreground-subtlest">
              {archivedSessions.length === 0
                ? formatMsg(locale, 'analytics.noArchivedSessionsInArchive')
                : formatMsg(locale, 'analytics.noArchivedSessionsMatchYour')}
            </div>
          ) : (
            visibleSessions.map((s) => (
              <div
                key={s.id}
                className="grid grid-cols-1 items-center gap-3 px-5 py-3.5 sm:grid-cols-[minmax(0,1fr)_auto]"
              >
                <div className="min-w-0">
                  <div className="truncate text-ui-base font-medium text-foreground">{s.title}</div>
                  <div className="mt-0.5 truncate font-mono text-ui-xs text-foreground-subtlest">
                    Runner: {s.runner_id} · ID: {s.id.slice(0, 18)}
                  </div>
                </div>
                <div className="flex shrink-0 items-center justify-end gap-2">
                  <button
                    type="button"
                    onClick={async () => {
                      await unarchiveSession(s.id);
                      onFlash(
                        formatMsg(locale, 'analytics.restored', { title: s.title }),
                      );
                    }}
                    className="flex h-8 items-center gap-1.5 rounded-lg border border-border bg-card px-3 text-ui-sm text-foreground transition-colors hover:bg-hover"
                  >
                    <ArchiveRestore className="size-3.5" />
                    <span>{formatMsg(locale, 'taskList.unarchive')}</span>
                  </button>
                  <button
                    type="button"
                    onClick={async () => {
                      await deleteArchivedPermanently(s.id);
                      onFlash(
                        formatMsg(locale, 'analytics.sessionPermanentlyDeleted'),
                      );
                    }}
                    className="flex h-8 items-center gap-1.5 rounded-lg bg-destructive-surface px-3 text-ui-sm font-medium text-destructive-surface-fg transition-opacity hover:opacity-90"
                  >
                    <Trash2 className="size-3.5" />
                    <span>{formatMsg(locale, 'analytics.permanentlyDelete')}</span>
                  </button>
                </div>
              </div>
            ))
          )}
        </SettingsGroupCard>
      </div>
    </div>
  );
}
