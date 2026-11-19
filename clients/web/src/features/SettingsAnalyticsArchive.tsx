import { ArchiveRestore, Trash2 } from 'lucide-react';
import { useEffect, useState } from 'react';
import { apiFetch, formatCompactTokens } from '../core/protocol';
import { useNexStore } from '../core/store';
import { SessionRecord, UsageSummary } from '../core/types';
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
};

const WINDOWS = [
  { id: 'today' as const, zh: '今天', en: 'Today' },
  { id: '7d' as const, zh: '近 7 天', en: '7 days' },
  { id: '30d' as const, zh: '近 30 天', en: '30 days' },
];

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
  const isEn = locale === 'en-US';

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

  const maxModelTok = Math.max(1, ...(stats?.models.map((m) => m.total_tokens) || [1]));

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
              {isEn ? w.en : w.zh}
            </button>
          ))}
        </div>

        <select
          value={runnerFilter}
          onChange={(e) => setRunnerFilter(e.target.value)}
          aria-label={isEn ? 'Filter by runner' : '按设备筛选'}
          className={`${settingsSelectClass} h-8 w-full max-w-[220px] text-ui-sm`}
        >
          <option value="">{isEn ? 'All runners' : '全部设备'}</option>
          {runners.map((r) => (
            <option key={r.id} value={r.id}>
              {r.name} ({r.os}/{r.arch})
            </option>
          ))}
        </select>
      </div>

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <MetricCard
          label={isEn ? 'Total tokens' : '总消耗 Token'}
          value={formatCompactTokens(stats.total_tokens)}
          hint={isEn ? `${stats.turn_count} model turns` : `共 ${stats.turn_count} 轮模型推理`}
        />
        <MetricCard
          label={isEn ? 'Estimated cost' : '预计开销'}
          value={`$${stats.estimated_cost_usd.toFixed(4)}`}
          hint={
            isEn
              ? `Cache saved $${stats.cache_saved_usd.toFixed(4)}`
              : `Prompt Cache 已省 $${stats.cache_saved_usd.toFixed(4)}`
          }
          tone="success"
        />
        <MetricCard
          label={isEn ? 'Cache hit rate' : '缓存命中率'}
          value={`${stats.cache_hit_rate_pct.toFixed(1)}%`}
          hint={
            isEn
              ? `${formatCompactTokens(stats.cache_read_tokens)} tok read`
              : `命中 ${formatCompactTokens(stats.cache_read_tokens)} tok`
          }
          tone="accent"
        />
        <MetricCard
          label={isEn ? 'Source breakdown' : '来源分布'}
          value={formatCompactTokens(
            (stats.by_source_tokens?.chat || 0) +
              (stats.by_source_tokens?.subagent || 0) +
              (stats.by_source_tokens?.cron || 0),
          )}
          hint={
            isEn
              ? `Chat ${formatCompactTokens(stats.by_source_tokens?.chat || 0)} · Sub ${formatCompactTokens(
                  stats.by_source_tokens?.subagent || 0,
                )} · Cron ${formatCompactTokens(stats.by_source_tokens?.cron || 0)}`
              : `对话 ${formatCompactTokens(stats.by_source_tokens?.chat || 0)} · 子智能体 ${formatCompactTokens(
                  stats.by_source_tokens?.subagent || 0,
                )} · 定时 ${formatCompactTokens(stats.by_source_tokens?.cron || 0)}`
          }
        />
      </div>

      <div className="space-y-3">
        <h3 className="text-ui-base font-medium text-foreground">
          {isEn ? 'Model usage breakdown' : '模型用量与成本排行'}
        </h3>
        <SettingsGroupCard>
          {stats.models.length === 0 ? (
            <div className="px-5 py-10 text-center text-ui-sm text-foreground-subtlest">
              {isEn ? 'No model calls in this window.' : '所选时间窗内暂无模型调用。'}
            </div>
          ) : (
            stats.models.map((m) => {
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
                        {isEn ? `${m.turn_count} turns` : `${m.turn_count} 轮`}
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
  const isEn = locale === 'en-US';

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
      isEn
        ? `Auto-archive policy updated (${days} days)`
        : `自动归档策略已更新（闲置满 ${days} 天自动迁入 archive.db）`,
    );
  };

  return (
    <div className="space-y-6">
      <SettingsGroupCard>
        <SettingsRow
          label={isEn ? 'Auto-archive policy' : '自动归档策略'}
          description={
            isEn
              ? 'Idle sessions move from main.db to archive.db. Historical stats are preserved.'
              : '闲置超过指定天数的会话将从热库 main.db 原子迁移至冷库 archive.db，历史统计不受影响。'
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
            {isEn ? 'days' : '天'}
          </span>
          <button
            type="button"
            onClick={() => void savePolicy()}
            className="h-8 shrink-0 rounded-lg bg-primary px-3.5 text-ui-sm font-medium text-primary-foreground transition-opacity hover:opacity-90"
          >
            {isEn ? 'Save' : '保存'}
          </button>
        </SettingsRow>
      </SettingsGroupCard>

      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex h-8 min-w-0 flex-1 items-center gap-1.5 rounded-lg border border-border bg-input px-2.5 sm:max-w-xs">
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={isEn ? 'Search archived sessions' : '搜索已归档会话'}
            className={`${settingsInputClass} h-7 border-0 bg-transparent p-0 text-ui-sm`}
          />
        </div>
        <select
          value={sortOrder}
          onChange={(e) => setSortOrder(e.target.value as 'updated' | 'created')}
          aria-label={isEn ? 'Sort order' : '排序方式'}
          className={`${settingsSelectClass} h-8 w-full max-w-[180px] text-ui-sm`}
        >
          <option value="updated">{isEn ? 'Last updated' : '更新时间'}</option>
          <option value="created">{isEn ? 'Created' : '创建时间'}</option>
        </select>
      </div>

      <div className="space-y-3">
        <h3 className="text-ui-base font-medium text-foreground">
          {isEn
            ? `Archived sessions (${visibleSessions.length})`
            : `已归档会话 (${visibleSessions.length})`}
        </h3>
        <SettingsGroupCard>
          {visibleSessions.length === 0 ? (
            <div className="px-5 py-10 text-center text-ui-sm text-foreground-subtlest">
              {archivedSessions.length === 0
                ? isEn
                  ? 'No archived sessions in archive.db.'
                  : '冷库 archive.db 暂无已归档会话。'
                : isEn
                  ? 'No archived sessions match your search.'
                  : '没有匹配的归档会话。'}
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
                        isEn
                          ? `Restored "${s.title}"`
                          : `已将 "${s.title}" 无损恢复至活跃工作区`,
                      );
                    }}
                    className="flex h-8 items-center gap-1.5 rounded-lg border border-border bg-card px-3 text-ui-sm text-foreground transition-colors hover:bg-hover"
                  >
                    <ArchiveRestore className="size-3.5" />
                    <span>{isEn ? 'Unarchive' : '取消归档'}</span>
                  </button>
                  <button
                    type="button"
                    onClick={async () => {
                      await deleteArchivedPermanently(s.id);
                      onFlash(
                        isEn
                          ? 'Session permanently deleted'
                          : '已永久删除会话并回收底层孤立 CAS Blobs',
                      );
                    }}
                    className="flex h-8 items-center gap-1.5 rounded-lg bg-destructive-surface px-3 text-ui-sm font-medium text-destructive-surface-fg transition-opacity hover:opacity-90"
                  >
                    <Trash2 className="size-3.5" />
                    <span>{isEn ? 'Permanently delete' : '永久删除'}</span>
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
