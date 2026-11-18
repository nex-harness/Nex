import { useEffect, useState } from 'react';
import { apiFetch, formatCompactTokens } from '../core/protocol';
import { useNexStore } from '../core/store';
import { PendingApproval, PendingUIPrompt, UsageSummary } from '../core/types';
import { hostBridge } from '../host/bridge';

export function ApprovalModal({ approval }: { approval: PendingApproval }) {
  const resolveApproval = useNexStore((s) => s.resolveApproval);

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4">
      <div className="w-full max-w-lg rounded-xl border border-amber-500/40 bg-zinc-900 p-5 shadow-2xl">
        <div className="flex items-center justify-between">
          <span className="rounded bg-amber-500/20 px-2 py-0.5 text-xs font-medium text-amber-300">
            Approval Required
          </span>
          <span className="font-mono text-xs text-zinc-400">{approval.runnerId}</span>
        </div>
        <h3 className="mt-2 text-sm font-semibold text-zinc-100">
          Allow tool <code className="text-blue-400">{approval.toolName}</code>?
        </h3>
        <pre className="mt-3 max-h-56 overflow-auto rounded-lg border border-zinc-800 bg-zinc-950 p-3 font-mono text-xs text-zinc-300">
          {approval.argsJson}
        </pre>
        <div className="mt-4 flex justify-end gap-2">
          <button
            type="button"
            onClick={() => resolveApproval(approval.approvalId, false)}
            className="rounded-lg border border-zinc-700 px-3.5 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800"
          >
            Deny
          </button>
          <button
            type="button"
            onClick={() => resolveApproval(approval.approvalId, true)}
            className="rounded-lg bg-blue-600 px-4 py-1.5 text-xs font-medium text-white hover:bg-blue-500"
          >
            Approve
          </button>
        </div>
      </div>
    </div>
  );
}

export function UIPromptModal({ prompt }: { prompt: PendingUIPrompt }) {
  const resolveUIPrompt = useNexStore((s) => s.resolveUIPrompt);
  const [val, setVal] = useState(prompt.defaultValue || '');

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4">
      <div className="w-full max-w-md rounded-xl border border-zinc-800 bg-zinc-900 p-5 shadow-2xl">
        <h3 className="text-sm font-semibold text-zinc-100">{prompt.title}</h3>
        {prompt.message && <p className="mt-1.5 text-xs text-zinc-400">{prompt.message}</p>}

        {prompt.kind === 'select' && (
          <div className="mt-3 space-y-1.5">
            {(prompt.options || []).map((opt) => (
              <button
                key={opt}
                type="button"
                onClick={() => resolveUIPrompt(prompt.requestId, true, opt)}
                className="w-full rounded-lg border border-zinc-800 bg-zinc-950 px-3 py-2 text-left text-xs text-zinc-200 hover:border-blue-500 hover:bg-zinc-800"
              >
                {opt}
              </button>
            ))}
          </div>
        )}

        {prompt.kind === 'input' && (
          <input
            value={val}
            onChange={(e) => setVal(e.target.value)}
            placeholder={prompt.placeholder || ''}
            className="mt-3 w-full rounded-lg border border-zinc-800 bg-zinc-950 px-3 py-2 text-xs text-zinc-100 outline-none focus:border-blue-500"
          />
        )}

        <div className="mt-4 flex justify-end gap-2">
          <button
            type="button"
            onClick={() => resolveUIPrompt(prompt.requestId, false, '', true)}
            className="rounded-lg border border-zinc-700 px-3 py-1.5 text-xs text-zinc-400 hover:bg-zinc-800"
          >
            Cancel
          </button>
          {prompt.kind !== 'select' && (
            <button
              type="button"
              onClick={() => resolveUIPrompt(prompt.requestId, true, val)}
              className="rounded-lg bg-blue-600 px-4 py-1.5 text-xs font-medium text-white hover:bg-blue-500"
            >
              Confirm
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

export function StatsModal({ onClose }: { onClose: () => void }) {
  const { runners, selectedRunnerId } = useNexStore();
  const [runnerFilter, setRunnerFilter] = useState<string>(selectedRunnerId || '');
  const [windowTab, setWindowTab] = useState<'today' | '7d' | '30d'>('today');
  const [stats, setStats] = useState<UsageSummary | null>(null);

  useEffect(() => {
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || 'Asia/Shanghai';
    const q = new URLSearchParams({ window: windowTab, tz });
    if (runnerFilter) q.set('runner_id', runnerFilter);
    void apiFetch<UsageSummary>(`/api/v1/stats?${q.toString()}`).then(setStats).catch(() => {});
  }, [runnerFilter, windowTab]);

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4">
      <div className="flex max-h-[85vh] w-full max-w-2xl flex-col rounded-xl border border-zinc-800 bg-zinc-950 p-5 shadow-2xl">
        <div className="flex items-center justify-between border-b border-zinc-800 pb-3">
          <h3 className="text-sm font-semibold text-zinc-100">Runner & Model Token / Cost Analytics</h3>
          <button type="button" onClick={onClose} className="text-xs text-zinc-400 hover:text-zinc-100">
            Close
          </button>
        </div>

        <div className="mt-3 flex flex-wrap items-center justify-between gap-2">
          <select
            value={runnerFilter}
            onChange={(e) => setRunnerFilter(e.target.value)}
            className="rounded-lg border border-zinc-800 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-200"
          >
            <option value="">All Runners (Global)</option>
            {runners.map((r) => (
              <option key={r.id} value={r.id}>
                {r.name} ({r.os}/{r.arch})
              </option>
            ))}
          </select>

          <div className="flex rounded-lg border border-zinc-800 bg-zinc-900 p-0.5 text-xs">
            {(['today', '7d', '30d'] as const).map((w) => (
              <button
                key={w}
                type="button"
                onClick={() => setWindowTab(w)}
                className={`rounded-md px-3 py-1 ${
                  windowTab === w ? 'bg-blue-600 text-white font-medium' : 'text-zinc-400 hover:text-zinc-200'
                }`}
              >
                {w === 'today' ? '今天' : w === '7d' ? '近 7 天' : '近 30 天'}
              </button>
            ))}
          </div>
        </div>

        {stats && (
          <div className="mt-4 flex-1 overflow-auto space-y-4">
            <div className="grid grid-cols-3 gap-3">
              <div className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-3">
                <div className="text-[11px] text-zinc-400">Total Tokens ({stats.turn_count} turns)</div>
                <div className="mt-1 font-mono text-lg font-semibold text-zinc-100">
                  {formatCompactTokens(stats.total_tokens)}
                </div>
                <div className="mt-1 text-[11px] text-zinc-500">Cache Hit: {stats.cache_hit_rate_pct.toFixed(1)}%</div>
              </div>
              <div className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-3">
                <div className="text-[11px] text-zinc-400">Estimated Spend (models.dev)</div>
                <div className="mt-1 font-mono text-lg font-semibold text-emerald-400">
                  ${stats.estimated_cost_usd.toFixed(4)}
                </div>
                <div className="mt-1 text-[11px] text-emerald-500/80">
                  Cache saved: ${stats.cache_saved_usd.toFixed(4)}
                </div>
              </div>
              <div className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-3">
                <div className="text-[11px] text-zinc-400">By Source</div>
                <div className="mt-1 space-y-0.5 font-mono text-[11px] text-zinc-300">
                  <div>Chat: {formatCompactTokens(stats.by_source_tokens?.chat || 0)}</div>
                  <div>Subagent: {formatCompactTokens(stats.by_source_tokens?.subagent || 0)}</div>
                  <div>Cron: {formatCompactTokens(stats.by_source_tokens?.cron || 0)}</div>
                </div>
              </div>
            </div>

            <div>
              <h4 className="mb-2 text-xs font-medium text-zinc-400">Models Used</h4>
              {stats.models.length === 0 ? (
                <div className="rounded-lg border border-zinc-800 p-4 text-center text-xs text-zinc-500">
                  该时间窗暂无调用记录
                </div>
              ) : (
                <div className="divide-y divide-zinc-800 rounded-xl border border-zinc-800 bg-zinc-900/40">
                  {stats.models.map((m) => (
                    <div key={`${m.provider_id}/${m.model_id}`} className="flex items-center justify-between px-3.5 py-2 text-xs">
                      <div>
                        <span className="font-mono font-medium text-zinc-200">{m.model_id}</span>
                        <span className="ml-2 text-zinc-500">({m.provider_id} · {m.turn_count} turns)</span>
                      </div>
                      <div className="font-mono text-zinc-300">
                        {formatCompactTokens(m.total_tokens)} tok · <span className="text-emerald-400">${m.estimated_cost_usd.toFixed(4)}</span>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

export function SettingsAndArchiveModal({ onClose }: { onClose: () => void }) {
  const {
    preferences,
    archivedSessions,
    refreshInitialData,
    unarchiveSession,
    deleteArchivedPermanently,
  } = useNexStore();

  const [theme, setTheme] = useState(preferences.theme);
  const [archiveDays, setArchiveDays] = useState(preferences.auto_archive_after_days);
  const [fontPx, setFontPx] = useState(() => hostBridge.getDeviceFontScale());
  const [provId, setProvId] = useState('new-provider');
  const [provBase, setProvBase] = useState('http://127.0.0.1:3425/v1');
  const [provKey, setProvKey] = useState('');
  const [provDriver, setProvDriver] = useState<'openai_responses' | 'anthropic'>('openai_responses');
  const [newModelId, setNewModelId] = useState('auto/deepseek-v4.1-flash');
  const [statusMsg, setStatusMsg] = useState('');

  const savePrefs = async () => {
    hostBridge.setDeviceFontScale(fontPx);
    await apiFetch('/api/v1/preferences', {
      method: 'PUT',
      body: JSON.stringify({ ...preferences, theme, auto_archive_after_days: archiveDays }),
    });
    await refreshInitialData();
    setStatusMsg('UI & Auto-Archive preferences saved.');
  };

  const addProviderAndModel = async () => {
    await apiFetch('/api/v1/providers', {
      method: 'POST',
      body: JSON.stringify({
        id: provId,
        name: provId,
        driver: provDriver,
        base_url: provBase,
        api_key: provKey,
        proxy_mode: 'direct',
        enabled: true,
      }),
    });
    if (newModelId.trim()) {
      await apiFetch('/api/v1/models', {
        method: 'POST',
        body: JSON.stringify({
          id: newModelId.trim(),
          provider_id: provId,
          display_name: newModelId.trim(),
          source: 'custom',
          context_window: 1000000,
          max_output_tokens: 65536,
          supports_tools: true,
          supports_reasoning: true,
          supports_vision: true,
        }),
      });
    }
    await refreshInitialData();
    setStatusMsg(`Saved provider ${provId} & model ${newModelId}.`);
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4">
      <div className="flex max-h-[88vh] w-full max-w-2xl flex-col rounded-xl border border-zinc-800 bg-zinc-950 p-5 shadow-2xl">
        <div className="flex items-center justify-between border-b border-zinc-800 pb-3">
          <h3 className="text-sm font-semibold text-zinc-100">Settings, Providers & Cold Archives</h3>
          <button type="button" onClick={onClose} className="text-xs text-zinc-400 hover:text-zinc-100">
            Close
          </button>
        </div>

        <div className="mt-4 flex-1 space-y-5 overflow-auto text-xs">
          {statusMsg && <div className="rounded-lg bg-emerald-500/15 px-3 py-2 text-emerald-300">{statusMsg}</div>}

          {/* UI & Auto-Archive Policy */}
          <section className="space-y-2.5 rounded-xl border border-zinc-800 bg-zinc-900/40 p-3.5">
            <h4 className="font-semibold text-zinc-200">UI Appearance & Auto-Archive Policy</h4>
            <div className="grid grid-cols-3 gap-3">
              <label className="space-y-1">
                <span className="text-zinc-400">Theme</span>
                <select
                  value={theme}
                  onChange={(e) => setTheme(e.target.value as 'dark' | 'light' | 'system')}
                  className="w-full rounded border border-zinc-800 bg-zinc-900 px-2 py-1.5 text-zinc-200"
                >
                  <option value="dark">Dark</option>
                  <option value="light">Light</option>
                </select>
              </label>
              <label className="space-y-1">
                <span className="text-zinc-400">Device Font Size (px)</span>
                <input
                  type="number"
                  value={fontPx}
                  onChange={(e) => setFontPx(Number(e.target.value))}
                  className="w-full rounded border border-zinc-800 bg-zinc-900 px-2 py-1.5 text-zinc-200"
                />
              </label>
              <label className="space-y-1">
                <span className="text-zinc-400">Auto-Archive Idle Days</span>
                <input
                  type="number"
                  value={archiveDays}
                  onChange={(e) => setArchiveDays(Number(e.target.value))}
                  className="w-full rounded border border-zinc-800 bg-zinc-900 px-2 py-1.5 text-zinc-200"
                />
              </label>
            </div>
            <button
              type="button"
              onClick={() => void savePrefs()}
              className="rounded-lg bg-blue-600 px-3 py-1.5 font-medium text-white hover:bg-blue-500"
            >
              Save Preferences
            </button>
          </section>

          {/* Add / Update Provider & Model */}
          <section className="space-y-2.5 rounded-xl border border-zinc-800 bg-zinc-900/40 p-3.5">
            <h4 className="font-semibold text-zinc-200">Add / Update LLM Provider & Model</h4>
            <div className="grid grid-cols-2 gap-2.5">
              <input
                value={provId}
                onChange={(e) => setProvId(e.target.value)}
                placeholder="Provider ID"
                className="rounded border border-zinc-800 bg-zinc-900 px-2.5 py-1.5 text-zinc-200"
              />
              <select
                value={provDriver}
                onChange={(e) => setProvDriver(e.target.value as 'openai_responses' | 'anthropic')}
                className="rounded border border-zinc-800 bg-zinc-900 px-2.5 py-1.5 text-zinc-200"
              >
                <option value="openai_responses">Codex / OpenAI Responses (/v1/responses)</option>
                <option value="anthropic">Anthropic Messages (/v1/messages)</option>
              </select>
              <input
                value={provBase}
                onChange={(e) => setProvBase(e.target.value)}
                placeholder="Base URL"
                className="rounded border border-zinc-800 bg-zinc-900 px-2.5 py-1.5 text-zinc-200"
              />
              <input
                value={provKey}
                onChange={(e) => setProvKey(e.target.value)}
                placeholder="API Key (plaintext)"
                className="rounded border border-zinc-800 bg-zinc-900 px-2.5 py-1.5 text-zinc-200"
              />
              <input
                value={newModelId}
                onChange={(e) => setNewModelId(e.target.value)}
                placeholder="Model ID (e.g. group/gemini-4-carbon)"
                className="col-span-2 rounded border border-zinc-800 bg-zinc-900 px-2.5 py-1.5 text-zinc-200"
              />
            </div>
            <button
              type="button"
              onClick={() => void addProviderAndModel()}
              className="rounded-lg bg-zinc-800 px-3 py-1.5 text-zinc-200 hover:bg-zinc-700"
            >
              Upsert Provider & Model
            </button>
          </section>

          {/* Cold Archive Sessions (Unarchive / Delete Permanently) */}
          <section className="space-y-2 rounded-xl border border-zinc-800 bg-zinc-900/40 p-3.5">
            <h4 className="font-semibold text-zinc-200">Archived Sessions (archive.db)</h4>
            {archivedSessions.length === 0 ? (
              <p className="text-zinc-500">No archived sessions.</p>
            ) : (
              <div className="divide-y divide-zinc-800">
                {archivedSessions.map((s) => (
                  <div key={s.id} className="flex items-center justify-between py-2">
                    <span className="truncate text-zinc-300">{s.title}</span>
                    <div className="flex gap-2">
                      <button
                        type="button"
                        onClick={() => void unarchiveSession(s.id)}
                        className="rounded border border-zinc-700 px-2 py-1 text-zinc-200 hover:bg-zinc-800"
                      >
                        Unarchive
                      </button>
                      <button
                        type="button"
                        onClick={() => void deleteArchivedPermanently(s.id)}
                        className="rounded bg-rose-600/20 px-2 py-1 text-rose-300 hover:bg-rose-600/30"
                      >
                        Delete Permanently
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </section>
        </div>
      </div>
    </div>
  );
}
