import {
  ChevronRight,
  Folder,
  Globe,
  LoaderCircle,
  RefreshCw,
  RotateCcw,
  Search,
  Sparkles,
  UserRound,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { apiFetch } from '../core/protocol';
import { useNexStore } from '../core/store';
import type { DiscoveredSkill } from '../core/types';
import { formatMsg } from '../i18n/messages';
import { HighlightedCode, MarkdownRenderer } from './MarkdownRenderer';
import { SectionHeading, SettingsGroupCard, settingsInputClass, settingsSelectClass } from './SettingsPage';

function workspaceLeaf(p: string): string {
  const parts = p.split(/[\\/]/).filter(Boolean);
  return parts[parts.length - 1] || p;
}

function ToggleSwitch({
  checked,
  disabled,
  label,
  onChange,
}: {
  checked: boolean;
  disabled?: boolean;
  label: string;
  onChange: (next: boolean) => void;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={(e) => {
        e.stopPropagation();
        onChange(!checked);
      }}
      className={`relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition-colors ${
        checked
          ? 'border-primary bg-primary'
          : 'border-border bg-surface hover:border-foreground-subtlest'
      } ${disabled ? 'cursor-not-allowed opacity-50' : 'cursor-pointer'}`}
    >
      <span
        className={`inline-block size-3.5 rounded-full bg-white shadow-xs transition-transform ${
          checked ? 'translate-x-4' : 'translate-x-0.5'
        }`}
      />
    </button>
  );
}

function ScopePillBadge({ scope, workspaceName, locale }: { scope: string; workspaceName?: string; locale: ReturnType<typeof useNexStore.getState>['locale'] }) {
  if (scope === 'workspace') {
    return (
      <span className="inline-flex shrink-0 items-center gap-1 rounded-full border border-border bg-surface px-2 py-0.5 text-ui-xs text-foreground-subtle">
        <Folder className="size-3" />
        <span className="max-w-32 truncate">{workspaceName || formatMsg(locale, 'skills.scope.workspace')}</span>
      </span>
    );
  }
  if (scope === 'registry') {
    return (
      <span className="inline-flex shrink-0 items-center gap-1 rounded-full border border-border bg-surface px-2 py-0.5 text-ui-xs text-foreground-subtle">
        <Globe className="size-3" />
        <span>{formatMsg(locale, 'skills.scope.registry')}</span>
      </span>
    );
  }
  return (
    <span className="inline-flex shrink-0 items-center gap-1 rounded-full border border-border bg-surface px-2 py-0.5 text-ui-xs text-foreground-subtle">
      <UserRound className="size-3" />
      <span>{formatMsg(locale, 'skills.scope.user')}</span>
    </span>
  );
}

export function SkillsManagerSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const {
    locale,
    runners,
    sessions,
    recentWorkspaces,
    selectedRunnerId,
    selectedWorkspacePath,
    skills: legacySkills,
  } = useNexStore();

  const knownWorkspaces = useMemo(() => {
    const set = new Set<string>();
    const push = (p?: string) => {
      const clean = (p || '').trim().replace(/\/+$/, '');
      if (clean) set.add(clean);
    };
    push(selectedWorkspacePath);
    for (const rw of recentWorkspaces) push(rw.path);
    for (const s of sessions) push(s.workspace_path);
    for (const r of runners) push(r.workspace_default);
    return Array.from(set).sort((a, b) => a.localeCompare(b));
  }, [recentWorkspaces, runners, selectedWorkspacePath, sessions]);

  // Empty string means Global (default visibility across all workspaces)
  const [targetWorkspace, setTargetWorkspace] = useState<string>(selectedWorkspacePath || '');
  const [targetRunner, setTargetRunner] = useState<string>(
    selectedRunnerId || runners.find((r) => r.status === 'online')?.id || runners[0]?.id || '',
  );
  const [scopeFilter, setScopeFilter] = useState<'all' | 'workspace' | 'user'>('all');
  const [searchQuery, setSearchQuery] = useState('');
  const [discovered, setDiscovered] = useState<DiscoveredSkill[]>([]);
  const [loading, setLoading] = useState(false);
  const [busyKey, setBusyKey] = useState<string | null>(null);
  const [expandedKey, setExpandedKey] = useState<string | null>(null);
  const [previewMode, setPreviewMode] = useState<'rendered' | 'raw'>('rendered');
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const loadDiscoveredSkills = useCallback(async () => {
    setLoading(true);
    try {
      const params = new URLSearchParams();
      if (targetRunner) params.set('runner_id', targetRunner);
      if (targetWorkspace) params.set('workspace_path', targetWorkspace);
      const res = await apiFetch<{ skills?: DiscoveredSkill[] }>(
        `/api/v1/skills/discover${params.toString() ? `?${params.toString()}` : ''}`,
      );
      if (mountedRef.current && Array.isArray(res?.skills)) {
        setDiscovered(res.skills);
      }
    } catch {
      // Fallback gracefully to any synced registry skills already in store (e.g. offline/jsdom)
      if (mountedRef.current) {
        setDiscovered(
          legacySkills.map((s) => ({
            name: s.name,
            description: s.description,
            scope: 'registry',
            source_kind: 'registry',
            source_dir: '',
            file_path: '',
            content_md: s.content_md,
            global_enabled: s.enabled,
            workspace_enabled: s.enabled,
            workspace_overridden: false,
          })),
        );
      }
    } finally {
      if (mountedRef.current) setLoading(false);
    }
  }, [legacySkills, targetRunner, targetWorkspace]);

  useEffect(() => {
    void loadDiscoveredSkills();
  }, [loadDiscoveredSkills]);

  const updateVisibility = async (
    skillName: string,
    scopeTarget: 'global' | 'workspace',
    workspacePath: string,
    enabled: boolean | null,
  ) => {
    const k = `${skillName}:${scopeTarget}:${workspacePath}`;
    setBusyKey(k);
    try {
      await apiFetch('/api/v1/skills/visibility', {
        method: 'PUT',
        body: JSON.stringify({
          name: skillName,
          scope_target: scopeTarget,
          workspace_path: workspacePath,
          enabled,
        }),
      });
      await loadDiscoveredSkills();
      onFlash(formatMsg(locale, 'skills.visibilitySaved', { name: skillName }));
    } finally {
      if (mountedRef.current) setBusyKey(null);
    }
  };

  const filteredSkills = useMemo(() => {
    const q = searchQuery.trim().toLowerCase();
    return discovered.filter((sk) => {
      if (scopeFilter === 'workspace' && sk.scope !== 'workspace') return false;
      if (scopeFilter === 'user' && sk.scope !== 'user' && sk.scope !== 'registry') return false;
      if (!q) return true;
      return (
        sk.name.toLowerCase().includes(q) ||
        (sk.description || '').toLowerCase().includes(q) ||
        (sk.source_kind || '').toLowerCase().includes(q) ||
        (sk.file_path || '').toLowerCase().includes(q)
      );
    });
  }, [discovered, scopeFilter, searchQuery]);

  const workspaceSkills = useMemo(
    () => filteredSkills.filter((s) => s.scope === 'workspace'),
    [filteredSkills],
  );
  const userSkills = useMemo(
    () => filteredSkills.filter((s) => s.scope === 'user'),
    [filteredSkills],
  );
  const registrySkills = useMemo(
    () => filteredSkills.filter((s) => s.scope === 'registry'),
    [filteredSkills],
  );

  const countByScope = useMemo(
    () => ({
      all: discovered.length,
      workspace: discovered.filter((s) => s.scope === 'workspace').length,
      user: discovered.filter((s) => s.scope === 'user' || s.scope === 'registry').length,
    }),
    [discovered],
  );

  const renderSkillRow = (sk: DiscoveredSkill) => {
    const rowKey = `${sk.scope}:${sk.source_kind}:${sk.name}`;
    const isOpen = expandedKey === rowKey;
    const isWorkspaceView = Boolean(targetWorkspace);
    const effectiveEnabled = isWorkspaceView ? sk.workspace_enabled : sk.global_enabled;
    const isOverridden = isWorkspaceView && sk.workspace_overridden;
    const toggleBusy = busyKey?.startsWith(`${sk.name}:`);

    return (
      <div key={rowKey} data-testid={`skill-row-${sk.name}`} className="px-5 py-3.5">
        <div className="flex items-start justify-between gap-3">
          <button
            type="button"
            onClick={() => setExpandedKey(isOpen ? null : rowKey)}
            className="flex min-w-0 flex-1 items-start gap-2 text-left"
          >
            <ChevronRight
              className={`mt-1 size-3.5 shrink-0 text-foreground-subtlest transition-transform ${
                isOpen ? 'rotate-90' : ''
              }`}
            />
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-1.5">
                <span className="font-mono text-ui-base font-medium text-foreground">
                  {sk.name}
                </span>
                <ScopePillBadge
                  scope={sk.scope}
                  workspaceName={sk.workspace_path ? workspaceLeaf(sk.workspace_path) : undefined}
                  locale={locale}
                />
                {sk.source_kind ? (
                  <span className="rounded-md border border-border/70 bg-background px-1.5 py-0.5 font-mono text-[11px] text-foreground-subtlest">
                    {sk.source_kind}
                  </span>
                ) : null}
                {sk.has_scripts ? (
                  <span className="rounded-md bg-surface px-1.5 py-0.5 font-mono text-[11px] text-foreground-subtle">
                    {formatMsg(locale, 'skills.bundle.scripts')}
                  </span>
                ) : null}
                {sk.has_references ? (
                  <span className="rounded-md bg-surface px-1.5 py-0.5 font-mono text-[11px] text-foreground-subtle">
                    {formatMsg(locale, 'skills.bundle.references')}
                  </span>
                ) : null}
                {sk.has_assets ? (
                  <span className="rounded-md bg-surface px-1.5 py-0.5 font-mono text-[11px] text-foreground-subtle">
                    {formatMsg(locale, 'skills.bundle.assets')}
                  </span>
                ) : null}
                {sk.shadowed_by ? (
                  <span className="rounded-md border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 text-[11px] text-amber-600 dark:text-amber-400">
                    {formatMsg(locale, 'skills.shadowedBy', { scope: sk.shadowed_by })}
                  </span>
                ) : null}
              </div>
              {sk.description ? (
                <p className="mt-1 line-clamp-2 text-ui-sm text-foreground-subtle">
                  {sk.description}
                </p>
              ) : null}
            </div>
          </button>

          <div className="flex shrink-0 items-center gap-2 pt-0.5">
            {isOverridden ? (
              <button
                type="button"
                title={formatMsg(locale, 'skills.resetOverride')}
                onClick={() => void updateVisibility(sk.name, 'workspace', targetWorkspace, null)}
                className="inline-flex items-center gap-1 rounded-md border border-brand-blue/30 bg-brand-blue/10 px-1.5 py-0.5 text-[11px] font-medium text-brand-blue transition-opacity hover:opacity-80"
              >
                <span>{formatMsg(locale, 'skills.overrideBadge')}</span>
                <RotateCcw className="size-3" />
              </button>
            ) : null}
            <span className="hidden text-ui-xs text-foreground-subtlest sm:inline">
              {effectiveEnabled
                ? formatMsg(locale, 'skills.visible')
                : formatMsg(locale, 'skills.hidden')}
            </span>
            <ToggleSwitch
              checked={effectiveEnabled}
              disabled={Boolean(toggleBusy)}
              label={`${sk.name} visibility`}
              onChange={(next) =>
                void updateVisibility(
                  sk.name,
                  isWorkspaceView ? 'workspace' : 'global',
                  targetWorkspace,
                  next,
                )
              }
            />
          </div>
        </div>

        {isOpen ? (
          <div className="mt-3.5 space-y-4 border-t border-border/60 pt-3.5 pl-5">
            {/* Metadata & file path */}
            {sk.file_path ? (
              <div className="space-y-1">
                <div className="text-ui-xs font-medium text-foreground-subtle">
                  {formatMsg(locale, 'skills.sourcePath')}
                </div>
                <div className="break-all font-mono text-ui-xs text-foreground-subtlest">
                  {sk.file_path}
                </div>
              </div>
            ) : null}

            {/* Per-workspace visibility matrix */}
            <div className="space-y-2 rounded-xl border border-border bg-background/60 p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div>
                  <div className="text-ui-xs font-medium text-foreground">
                    {formatMsg(locale, 'skills.workspaceMatrix')}
                  </div>
                  <p className="text-[11px] text-foreground-subtlest">
                    {formatMsg(locale, 'skills.workspaceMatrixHint')}
                  </p>
                </div>
                <div className="flex items-center gap-2 text-ui-xs text-foreground-subtle">
                  <span>{formatMsg(locale, 'skills.globalDefault')}</span>
                  <ToggleSwitch
                    checked={sk.global_enabled}
                    disabled={Boolean(toggleBusy)}
                    label={`${sk.name} global visibility`}
                    onChange={(next) => void updateVisibility(sk.name, 'global', '', next)}
                  />
                </div>
              </div>

              {knownWorkspaces.length === 0 ? (
                <p className="pt-1 text-ui-xs text-foreground-subtlest">
                  {formatMsg(locale, 'skills.noKnownWorkspaces')}
                </p>
              ) : (
                <div className="divide-y divide-border/50 rounded-lg border border-border/70 bg-surface/50">
                  {knownWorkspaces.map((ws) => {
                    const hasOv =
                      sk.workspace_overrides != null &&
                      Object.prototype.hasOwnProperty.call(sk.workspace_overrides, ws);
                    const wsVis = hasOv ? Boolean(sk.workspace_overrides![ws]) : sk.global_enabled;
                    return (
                      <div
                        key={ws}
                        className="flex items-center justify-between gap-3 px-3 py-2 text-ui-xs"
                      >
                        <div className="flex min-w-0 items-center gap-2">
                          <Folder className="size-3.5 shrink-0 text-foreground-subtlest" />
                          <span className="truncate font-medium text-foreground">
                            {workspaceLeaf(ws)}
                          </span>
                          <span className="hidden truncate font-mono text-[11px] text-foreground-subtlest md:inline">
                            {ws}
                          </span>
                        </div>
                        <div className="flex shrink-0 items-center gap-2">
                          {hasOv ? (
                            <button
                              type="button"
                              onClick={() => void updateVisibility(sk.name, 'workspace', ws, null)}
                              title={formatMsg(locale, 'skills.resetOverride')}
                              className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] text-brand-blue hover:bg-brand-blue/10"
                            >
                              <span>{formatMsg(locale, 'skills.overrideBadge')}</span>
                              <RotateCcw className="size-2.5" />
                            </button>
                          ) : (
                            <span className="text-[11px] text-foreground-subtlest">
                              {formatMsg(locale, 'skills.inheritedBadge')}
                            </span>
                          )}
                          <ToggleSwitch
                            checked={wsVis}
                            disabled={Boolean(toggleBusy)}
                            label={`${sk.name} in ${ws}`}
                            onChange={(next) =>
                              void updateVisibility(sk.name, 'workspace', ws, next)
                            }
                          />
                        </div>
                      </div>
                    );
                  })}
                </div>
              )}
            </div>

            {/* Read-only SKILL.md inspection */}
            {sk.content_md ? (
              <div className="space-y-2">
                <div className="flex items-center justify-between">
                  <span className="text-ui-xs font-medium text-foreground-subtle">SKILL.md</span>
                  <div className="inline-flex rounded-lg border border-border bg-surface p-0.5 text-[11px]">
                    <button
                      type="button"
                      onClick={() => setPreviewMode('rendered')}
                      className={`rounded-md px-2 py-0.5 font-medium transition-colors ${
                        previewMode === 'rendered'
                          ? 'bg-popover text-foreground shadow-xs'
                          : 'text-foreground-subtle hover:text-foreground'
                      }`}
                    >
                      {formatMsg(locale, 'skills.renderedPreview')}
                    </button>
                    <button
                      type="button"
                      onClick={() => setPreviewMode('raw')}
                      className={`rounded-md px-2 py-0.5 font-medium transition-colors ${
                        previewMode === 'raw'
                          ? 'bg-popover text-foreground shadow-xs'
                          : 'text-foreground-subtle hover:text-foreground'
                      }`}
                    >
                      {formatMsg(locale, 'skills.rawSource')}
                    </button>
                  </div>
                </div>
                {previewMode === 'rendered' ? (
                  <div className="max-h-72 overflow-y-auto rounded-lg border border-border bg-surface/60 px-4 py-3 text-ui-sm">
                    <MarkdownRenderer content={sk.content_md} streaming={false} />
                  </div>
                ) : (
                  <HighlightedCode
                    code={sk.content_md}
                    language="markdown"
                    className="max-h-72 text-ui-xs"
                  />
                )}
              </div>
            ) : null}
          </div>
        ) : null}
      </div>
    );
  };

  return (
    <div data-testid="skills-manager-section" className="space-y-6">
      {/* Scope & Discovery Control Bar */}
      <SettingsGroupCard>
        <div className="space-y-3.5 px-5 py-4">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="min-w-0">
              <div className="text-ui-base font-medium text-foreground">
                {formatMsg(locale, 'skills.managerTitle')}
              </div>
              <p className="mt-0.5 text-ui-xs text-foreground-subtle">
                {formatMsg(locale, 'skills.managerDesc')}
              </p>
            </div>
            <button
              type="button"
              onClick={() => void loadDiscoveredSkills()}
              disabled={loading}
              className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border bg-surface px-3 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover disabled:opacity-50"
            >
              {loading ? (
                <LoaderCircle className="size-3.5 animate-spin" />
              ) : (
                <RefreshCw className="size-3.5" />
              )}
              <span>
                {loading
                  ? formatMsg(locale, 'skills.refreshing')
                  : formatMsg(locale, 'skills.refresh')}
              </span>
            </button>
          </div>

          <div className="grid grid-cols-1 gap-3 sm:grid-cols-12">
            <div className={runners.length > 1 ? 'sm:col-span-5' : 'sm:col-span-6'}>
              <label className="block space-y-1">
                <span className="text-ui-xs text-foreground-subtle">
                  {formatMsg(locale, 'skills.targetWorkspace')}
                </span>
                <select
                  data-testid="skills-workspace-select"
                  value={targetWorkspace}
                  onChange={(e) => setTargetWorkspace(e.target.value)}
                  className={settingsSelectClass}
                >
                  <option value="">{formatMsg(locale, 'skills.globalDefault')}</option>
                  {knownWorkspaces.map((ws) => (
                    <option key={ws} value={ws}>
                      {workspaceLeaf(ws)} — {ws}
                    </option>
                  ))}
                </select>
              </label>
            </div>

            {runners.length > 1 ? (
              <div className="sm:col-span-3">
                <label className="block space-y-1">
                  <span className="text-ui-xs text-foreground-subtle">
                    {formatMsg(locale, 'capabilities.runner')}
                  </span>
                  <select
                    value={targetRunner}
                    onChange={(e) => setTargetRunner(e.target.value)}
                    className={settingsSelectClass}
                  >
                    {runners.map((r) => (
                      <option key={r.id} value={r.id}>
                        {r.name} {r.status === 'online' ? '●' : '○'}
                      </option>
                    ))}
                  </select>
                </label>
              </div>
            ) : null}

            <div className={runners.length > 1 ? 'sm:col-span-4' : 'sm:col-span-6'}>
              <label className="block space-y-1">
                <span className="text-ui-xs text-foreground-subtle">&nbsp;</span>
                <div className="relative">
                  <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-foreground-subtlest" />
                  <input
                    value={searchQuery}
                    onChange={(e) => setSearchQuery(e.target.value)}
                    placeholder={formatMsg(locale, 'skills.searchPlaceholder')}
                    className={`${settingsInputClass} pl-8`}
                  />
                </div>
              </label>
            </div>
          </div>

          {/* Scope filter tabs */}
          <div className="flex flex-wrap items-center gap-1.5">
            {(
              [
                { id: 'all', labelKey: 'skills.scope.all', count: countByScope.all },
                { id: 'workspace', labelKey: 'skills.scope.workspace', count: countByScope.workspace },
                { id: 'user', labelKey: 'skills.scope.user', count: countByScope.user },
              ] as const
            ).map((tab) => (
              <button
                key={tab.id}
                type="button"
                onClick={() => setScopeFilter(tab.id)}
                className={`inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-ui-xs font-medium transition-colors ${
                  scopeFilter === tab.id
                    ? 'border-foreground/20 bg-selected text-foreground'
                    : 'border-border bg-surface text-foreground-subtle hover:text-foreground'
                }`}
              >
                <span>{formatMsg(locale, tab.labelKey)}</span>
                <span className="text-[11px] tabular-nums text-foreground-subtlest">
                  {tab.count}
                </span>
              </button>
            ))}
          </div>
        </div>
      </SettingsGroupCard>

      {/* Discovered Skills grouped by Scope (Workspace -> User -> Registry) */}
      {filteredSkills.length === 0 ? (
        <SettingsGroupCard>
          <div className="flex flex-col items-center gap-2 px-6 py-10 text-center">
            <Sparkles className="size-6 text-foreground-subtlest" />
            <div className="text-ui-base font-medium text-foreground">
              {discovered.length === 0
                ? formatMsg(locale, 'skills.emptyTitle')
                : formatMsg(locale, 'skills.noMatches')}
            </div>
            <p className="max-w-lg text-ui-xs leading-relaxed text-foreground-subtlest">
              {formatMsg(locale, 'skills.emptyHint')}
            </p>
          </div>
        </SettingsGroupCard>
      ) : (
        <div className="space-y-6">
          {workspaceSkills.length > 0 ? (
            <div className="space-y-3">
              <SectionHeading
                title={`${formatMsg(locale, 'skills.group.workspace')} (${workspaceSkills.length})`}
              />
              <SettingsGroupCard>{workspaceSkills.map(renderSkillRow)}</SettingsGroupCard>
            </div>
          ) : null}

          {userSkills.length > 0 ? (
            <div className="space-y-3">
              <SectionHeading
                title={`${formatMsg(locale, 'skills.group.user')} (${userSkills.length})`}
              />
              <SettingsGroupCard>{userSkills.map(renderSkillRow)}</SettingsGroupCard>
            </div>
          ) : null}

          {registrySkills.length > 0 ? (
            <div className="space-y-3">
              <SectionHeading
                title={`${formatMsg(locale, 'skills.group.registry')} (${registrySkills.length})`}
              />
              <SettingsGroupCard>{registrySkills.map(renderSkillRow)}</SettingsGroupCard>
            </div>
          ) : null}
        </div>
      )}
    </div>
  );
}
