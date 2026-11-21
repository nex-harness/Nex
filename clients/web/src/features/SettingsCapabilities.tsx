import { formatEffortLabel, resolveEffortLevels } from '../core/effort';
import { formatMsg, type LocaleCode } from '../i18n/messages';
import type { MessageKey } from '../i18n/locales/en-US';
import { ChevronRight, LoaderCircle, Pencil, Search, Sparkles, Trash2 } from 'lucide-react';
import { type ReactNode, useMemo, useState } from 'react';
import { apiFetch } from '../core/protocol';
import { useNexStore } from '../core/store';
import type { AgentProfile, ModelMeta, ProviderRecord, ThinkingEffort } from '../core/types';
import { CodeEditorWithHighlight, HighlightedCode } from './MarkdownRenderer';
import { MCPManagerSection } from './SettingsMCP';
import { SkillsManagerSection } from './SettingsSkills';
import {
  SectionHeading,
  SettingsGroupCard,
  settingsInputClass,
  settingsMonoInputClass,
  settingsSelectClass,
} from './SettingsPage';

const areaClass =
  'w-full rounded-lg border border-border bg-input p-3 text-ui-sm leading-relaxed text-foreground placeholder-foreground-subtlest';

function Field({
  label,
  description,
  children,
}: {
  label: string;
  description?: string;
  children: ReactNode;
}) {
  return (
    <label className="block min-w-0 space-y-1.5">
      <span className="text-ui-xs text-foreground-subtle">{label}</span>
      {description ? <p className="text-ui-xs text-foreground-subtlest">{description}</p> : null}
      {children}
    </label>
  );
}

function SaveButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="h-8 rounded-lg bg-primary px-3.5 text-ui-sm font-medium text-primary-foreground transition-opacity hover:opacity-90"
    >
      {label}
    </button>
  );
}

function EmptyRow({ children }: { children: ReactNode }) {
  return (
    <div className="px-5 py-10 text-center text-ui-sm text-foreground-subtlest">{children}</div>
  );
}

function CardHeading({ title, trailing }: { title: string; trailing?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 px-5 py-3.5">
      <h3 className="text-ui-base font-medium text-foreground">{title}</h3>
      {trailing}
    </div>
  );
}

function MetaBadge({ children, mono = false }: { children: ReactNode; mono?: boolean }) {
  return (
    <span
      className={`shrink-0 rounded-md border border-border bg-surface px-1.5 py-0.5 text-ui-xs text-foreground-subtle ${
        mono ? 'font-mono' : ''
      }`}
    >
      {children}
    </span>
  );
}

/* -------------------------------------------------------------------------- */
/* Subagents                                                                  */
/* -------------------------------------------------------------------------- */

interface BuiltInSubagentMeta {
  slug: 'explore' | 'worker';
  nameKey: MessageKey;
  descKey: MessageKey;
  defaultTools: string[];
  isolation: 'shared' | 'worktree';
}

const BUILT_IN_SUBAGENTS: BuiltInSubagentMeta[] = [
  {
    slug: 'explore',
    nameKey: 'capabilities.builtIn.explore.name',
    descKey: 'capabilities.builtIn.explore.desc',
    defaultTools: ['read', 'grep', 'glob', 'find_files'],
    isolation: 'shared',
  },
  {
    slug: 'worker',
    nameKey: 'capabilities.builtIn.worker.name',
    descKey: 'capabilities.builtIn.worker.desc',
    defaultTools: [],
    isolation: 'shared',
  },
];

function matchSubagentModel(models: ModelMeta[], modelRef: string): ModelMeta | undefined {
  const trimmed = (modelRef || '').trim();
  if (!trimmed) return undefined;
  const sepIdx = trimmed.indexOf('::');
  if (sepIdx >= 0) {
    const prov = trimmed.slice(0, sepIdx).trim();
    const mod = trimmed.slice(sepIdx + 2).trim();
    return models.find((m) => m.provider_id === prov && m.id === mod);
  }
  return models.find((m) => m.id === trimmed);
}

function SubagentModelControl({
  locale,
  providers,
  models,
  modelRef,
  thinkingEffort,
  onChange,
  testIdPrefix,
  disabled = false,
}: {
  locale: LocaleCode;
  providers: ProviderRecord[];
  models: ModelMeta[];
  modelRef: string;
  thinkingEffort: ThinkingEffort | '';
  onChange: (nextModelRef: string, nextEffort: ThinkingEffort | '') => void;
  testIdPrefix: string;
  disabled?: boolean;
}) {
  const providerNameMap = useMemo(() => {
    const map = new Map<string, string>();
    for (const p of providers) map.set(p.id, p.name || p.id);
    return map;
  }, [providers]);

  const groupedModels = useMemo(() => {
    const enabledProviderIds = new Set(providers.filter((p) => p.enabled).map((p) => p.id));
    const groups = new Map<string, ModelMeta[]>();
    for (const m of models) {
      if (providers.length > 0 && !enabledProviderIds.has(m.provider_id)) continue;
      const list = groups.get(m.provider_id) ?? [];
      list.push(m);
      groups.set(m.provider_id, list);
    }
    return Array.from(groups.entries());
  }, [providers, models]);

  const matchedModel = useMemo(() => matchSubagentModel(models, modelRef), [models, modelRef]);
  const isMissingModel = Boolean((modelRef || '').trim()) && !matchedModel;
  const normalizedValue = matchedModel
    ? `${matchedModel.provider_id}::${matchedModel.id}`
    : (modelRef || '').trim();
  const effortOptions = useMemo(() => resolveEffortLevels(matchedModel), [matchedModel]);

  return (
    <div className="flex flex-wrap items-center gap-2">
      <select
        data-testid={`${testIdPrefix}-model-select`}
        value={normalizedValue}
        disabled={disabled}
        onChange={(e) => {
          const nextRef = e.target.value;
          const nextModel = matchSubagentModel(models, nextRef);
          const nextEfforts = resolveEffortLevels(nextModel);
          const keepEffort =
            nextRef &&
            nextModel &&
            thinkingEffort &&
            nextEfforts.some((opt) => opt.value === thinkingEffort)
              ? thinkingEffort
              : '';
          onChange(nextRef, keepEffort);
        }}
        className={`h-8 min-w-48 max-w-full rounded-lg border px-2.5 text-ui-xs outline-none transition-colors ${
          isMissingModel
            ? 'border-amber-500/60 bg-amber-500/5 text-amber-700 dark:text-amber-300'
            : 'border-border bg-input text-foreground'
        }`}
      >
        <option value="">{formatMsg(locale, 'capabilities.subagentModelDefaultMain')}</option>
        {isMissingModel ? (
          <option value={normalizedValue}>
            {`${normalizedValue} — ${formatMsg(locale, 'capabilities.subagentModelUnavailable')}`}
          </option>
        ) : null}
        {groupedModels.map(([providerId, items]) => (
          <optgroup key={providerId} label={providerNameMap.get(providerId) || providerId}>
            {items.map((m) => {
              const refValue = `${m.provider_id}::${m.id}`;
              return (
                <option key={refValue} value={refValue}>
                  {m.display_name || m.id}
                </option>
              );
            })}
          </optgroup>
        ))}
      </select>

      {matchedModel && effortOptions.length > 0 ? (
        <select
          data-testid={`${testIdPrefix}-effort-select`}
          value={thinkingEffort || ''}
          disabled={disabled}
          onChange={(e) => onChange(normalizedValue, e.target.value as ThinkingEffort | '')}
          className="h-8 rounded-lg border border-border bg-input px-2.5 text-ui-xs text-foreground outline-none"
        >
          <option value="">{formatMsg(locale, 'capabilities.subagentEffortDefault')}</option>
          {effortOptions.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {formatEffortLabel(opt, locale)}
            </option>
          ))}
        </select>
      ) : null}
    </div>
  );
}

export function AgentsSettingsSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const { locale, agentProfiles, providers, models, refreshRegistries } = useNexStore();

  const [slug, setSlug] = useState('code-reviewer');
  const [name, setName] = useState('Code Reviewer');
  const [prompt, setPrompt] = useState(
    'You are a strict read-only code reviewer focusing on concurrency and edge cases.',
  );
  const [isolation, setIsolation] = useState<'shared' | 'worktree'>('shared');
  const [toolsCsv, setToolsCsv] = useState('read,grep,glob,codemode');
  const [newModelRef, setNewModelRef] = useState('');
  const [newEffort, setNewEffort] = useState<ThinkingEffort | ''>('');
  const [savingSlug, setSavingSlug] = useState<string | null>(null);

  const profileBySlug = useMemo(() => {
    const map = new Map<string, AgentProfile>();
    for (const ap of agentProfiles) {
      map.set(ap.slug.toLowerCase(), ap);
    }
    return map;
  }, [agentProfiles]);

  const builtInSlugs = useMemo(() => new Set(BUILT_IN_SUBAGENTS.map((b) => b.slug)), []);
  const customProfiles = useMemo(
    () => agentProfiles.filter((ap) => !builtInSlugs.has(ap.slug.toLowerCase() as 'explore' | 'worker')),
    [agentProfiles, builtInSlugs],
  );

  const saveBuiltInOverride = async (
    meta: BuiltInSubagentMeta,
    nextModelRef: string,
    nextEffort: ThinkingEffort | '',
  ) => {
    const existing = profileBySlug.get(meta.slug);
    setSavingSlug(meta.slug);
    try {
      await apiFetch('/api/v1/agents', {
        method: 'POST',
        body: JSON.stringify({
          slug: meta.slug,
          display_name: existing?.display_name || formatMsg(locale, meta.nameKey),
          description: existing?.description || formatMsg(locale, meta.descKey),
          system_prompt: existing?.system_prompt || '',
          model_ref: nextModelRef,
          thinking_effort: nextEffort,
          tools_allow: existing?.tools_allow ?? meta.defaultTools,
          max_turns: existing?.max_turns ?? 25,
          isolation_mode: existing?.isolation_mode || meta.isolation,
        }),
      });
      await refreshRegistries();
      onFlash(formatMsg(locale, 'capabilities.subagentProfileSaved', { slug: meta.slug }));
    } finally {
      setSavingSlug(null);
    }
  };

  const saveCustomModelOverride = async (
    ap: AgentProfile,
    nextModelRef: string,
    nextEffort: ThinkingEffort | '',
  ) => {
    setSavingSlug(ap.slug);
    try {
      await apiFetch('/api/v1/agents', {
        method: 'POST',
        body: JSON.stringify({
          ...ap,
          model_ref: nextModelRef,
          thinking_effort: nextEffort,
        }),
      });
      await refreshRegistries();
      onFlash(formatMsg(locale, 'capabilities.subagentProfileSaved', { slug: ap.slug }));
    } finally {
      setSavingSlug(null);
    }
  };

  const saveAgent = async () => {
    await apiFetch('/api/v1/agents', {
      method: 'POST',
      body: JSON.stringify({
        slug: slug.trim(),
        display_name: name.trim() || slug.trim(),
        description: name.trim(),
        system_prompt: prompt,
        model_ref: newModelRef,
        thinking_effort: newEffort,
        tools_allow: toolsCsv
          .split(',')
          .map((s) => s.trim())
          .filter(Boolean),
        max_turns: 25,
        isolation_mode: isolation,
      }),
    });
    await refreshRegistries();
    onFlash(formatMsg(locale, 'capabilities.subagentProfileSaved', { slug }));
  };

  const removeAgent = async (targetSlug: string) => {
    await apiFetch(`/api/v1/agents/${encodeURIComponent(targetSlug)}`, { method: 'DELETE' });
    await refreshRegistries();
  };

  return (
    <div className="space-y-6">
      <SectionHeading
        title={formatMsg(locale, 'capabilities.builtInSubagents')}
        description={formatMsg(locale, 'capabilities.builtInSubagentsDesc')}
      />

      <SettingsGroupCard>
        <div data-testid="builtin-subagents-section" className="divide-y divide-border">
          {BUILT_IN_SUBAGENTS.map((b) => {
            const override = profileBySlug.get(b.slug);
            const currentModelRef = override?.model_ref || '';
            const currentEffort = (override?.thinking_effort as ThinkingEffort | undefined) || '';
            const matched = matchSubagentModel(models, currentModelRef);
            return (
              <div
                key={b.slug}
                data-testid={`builtin-subagent-${b.slug}`}
                className="flex flex-col gap-3 px-5 py-4 sm:flex-row sm:items-center sm:justify-between"
              >
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="text-ui-base font-medium text-foreground">
                      {formatMsg(locale, b.nameKey)}
                    </span>
                    <MetaBadge mono>{b.slug}</MetaBadge>
                    {currentModelRef && !matched ? (
                      <MetaBadge>{formatMsg(locale, 'capabilities.subagentModelUnavailable')}</MetaBadge>
                    ) : null}
                  </div>
                  <p className="mt-1 text-ui-xs text-foreground-subtle">
                    {formatMsg(locale, b.descKey)}
                  </p>
                </div>
                <div className="shrink-0">
                  <SubagentModelControl
                    locale={locale}
                    providers={providers}
                    models={models}
                    modelRef={currentModelRef}
                    thinkingEffort={currentEffort}
                    disabled={savingSlug === b.slug}
                    testIdPrefix={`builtin-subagent-${b.slug}`}
                    onChange={(nextRef, nextEffort) =>
                      void saveBuiltInOverride(b, nextRef, nextEffort)
                    }
                  />
                </div>
              </div>
            );
          })}
        </div>
      </SettingsGroupCard>

      <SectionHeading
        title={formatMsg(locale, 'capabilities.newSubagentProfile')}
        description={
          formatMsg(locale, 'capabilities.spawnReadOnlyOrIsolated')
        }
      />

      <SettingsGroupCard>
        <div className="grid grid-cols-1 gap-4 px-5 py-4 sm:grid-cols-2">
          <Field label={formatMsg(locale, 'capabilities.slug')}>
            <input
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
              placeholder="explore / reviewer"
              className={settingsMonoInputClass}
            />
          </Field>
          <Field label={formatMsg(locale, 'capabilities.displayName')}>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              className={settingsInputClass}
            />
          </Field>
        </div>

        <div className="grid grid-cols-1 gap-4 px-5 py-4 sm:grid-cols-2">
          <Field
            label={formatMsg(locale, 'capabilities.allowedTools')}
            description={formatMsg(locale, 'capabilities.commaSeparatedToolAllowlist')}
          >
            <input
              value={toolsCsv}
              onChange={(e) => setToolsCsv(e.target.value)}
              placeholder="read,grep,glob"
              className={settingsMonoInputClass}
            />
          </Field>
          <Field
            label={formatMsg(locale, 'capabilities.isolation')}
            description={
              formatMsg(locale, 'capabilities.sharedWorkspaceOrAnIndependent')
            }
          >
            <select
              value={isolation}
              onChange={(e) => setIsolation(e.target.value as 'shared' | 'worktree')}
              className={settingsSelectClass}
            >
              <option value="shared">{formatMsg(locale, 'capabilities.sharedWorkspace')}</option>
              <option value="worktree">{formatMsg(locale, 'capabilities.gitWorktree')}</option>
            </select>
          </Field>
        </div>

        <div className="px-5 py-4">
          <Field label={formatMsg(locale, 'capabilities.subagentModel')}>
            <SubagentModelControl
              locale={locale}
              providers={providers}
              models={models}
              modelRef={newModelRef}
              thinkingEffort={newEffort}
              testIdPrefix="new-subagent"
              onChange={(nextRef, nextEffort) => {
                setNewModelRef(nextRef);
                setNewEffort(nextEffort);
              }}
            />
          </Field>
        </div>

        <div className="space-y-3 px-5 py-4">
          <Field
            label={formatMsg(locale, 'chat.contextUsage.systemPrompt')}
            description={
              formatMsg(locale, 'capabilities.appendedWhenThisSubagentIs')
            }
          >
            <CodeEditorWithHighlight
              rows={4}
              language="markdown"
              value={prompt}
              onChange={setPrompt}
              placeholder={formatMsg(locale, 'capabilities.roleSpecificSystemPrompt')}
            />
          </Field>
          <div className="flex justify-end">
            <SaveButton
              label={formatMsg(locale, 'capabilities.saveSubagent')}
              onClick={() => void saveAgent()}
            />
          </div>
        </div>
      </SettingsGroupCard>

      <SectionHeading
        title={
          formatMsg(locale, 'capabilities.configuredProfiles', { length: customProfiles.length })
        }
      />

      <SettingsGroupCard>
        {customProfiles.length === 0 ? (
          <EmptyRow>
            {formatMsg(locale, 'capabilities.usingTheBuiltInExplore')}
          </EmptyRow>
        ) : (
          customProfiles.map((ap) => {
            const matched = matchSubagentModel(models, ap.model_ref || '');
            return (
              <div
                key={ap.slug}
                data-testid={`custom-subagent-${ap.slug}`}
                className="flex flex-col gap-3 px-5 py-3.5 sm:flex-row sm:items-center sm:justify-between"
              >
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="text-ui-base font-medium text-foreground">
                      {ap.display_name}
                    </span>
                    <MetaBadge mono>{ap.slug}</MetaBadge>
                    <MetaBadge mono>{ap.isolation_mode}</MetaBadge>
                    <MetaBadge>
                      {matched
                        ? matched.display_name || matched.id
                        : ap.model_ref
                          ? `${ap.model_ref} — ${formatMsg(locale, 'capabilities.subagentModelUnavailable')}`
                          : formatMsg(locale, 'capabilities.subagentModelDefaultMain')}
                    </MetaBadge>
                  </div>
                  <p className="mt-1 truncate text-ui-sm text-foreground-subtle">{ap.system_prompt}</p>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <SubagentModelControl
                    locale={locale}
                    providers={providers}
                    models={models}
                    modelRef={ap.model_ref || ''}
                    thinkingEffort={(ap.thinking_effort as ThinkingEffort | undefined) || ''}
                    disabled={savingSlug === ap.slug}
                    testIdPrefix={`custom-subagent-${ap.slug}`}
                    onChange={(nextRef, nextEffort) =>
                      void saveCustomModelOverride(ap, nextRef, nextEffort)
                    }
                  />
                  <button
                    type="button"
                    onClick={() => void removeAgent(ap.slug)}
                    aria-label={formatMsg(locale, 'capabilities.deleteSubagent')}
                    className="flex size-7 shrink-0 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-destructive-surface hover:text-destructive-surface-fg"
                  >
                    <Trash2 className="size-4" />
                  </button>
                </div>
              </div>
            );
          })
        )}
      </SettingsGroupCard>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* MCP servers (ZCode + Pi style install, exposure & workspace visibility)    */
/* -------------------------------------------------------------------------- */

export function MCPSettingsSection({ onFlash }: { onFlash: (msg: string) => void }) {
  return <MCPManagerSection onFlash={onFlash} />;
}

/* -------------------------------------------------------------------------- */
/* Skills (discovery + visibility manager) & Plugins                          */
/* -------------------------------------------------------------------------- */

export function SkillsAndPluginsSection({
  mode,
  onFlash,
}: {
  mode: 'skills' | 'plugins';
  onFlash: (msg: string) => void;
}) {
  const { locale, plugins, refreshRegistries } = useNexStore();

  const [plName, setPlName] = useState('safety-guard');
  const [plTS, setPlTS] = useState(
    `export default function (nex: any) {\n  nex.on("tool_call", (ev: any) => {\n    if (ev.toolName === "bash" && String(ev.input.command).includes("rm -rf /")) {\n      return { block: true, reason: "Blocked destructive root command" };\n    }\n  });\n}`,
  );
  const [expandedPlugin, setExpandedPlugin] = useState<string | null>(null);

  if (mode === 'skills') {
    return <SkillsManagerSection onFlash={onFlash} />;
  }

  const savePlugin = async () => {
    await apiFetch('/api/v1/plugins', {
      method: 'POST',
      body: JSON.stringify({
        name: plName.trim(),
        source_ts: plTS,
        scope: 'global',
        enabled: true,
      }),
    });
    await refreshRegistries();
    onFlash(
      formatMsg(locale, 'capabilities.pluginCompiledAndSynced', { plName }),
    );
  };

  return (
    <div className="space-y-6">
      <SettingsGroupCard>
        <CardHeading
          title={formatMsg(locale, 'capabilities.typescriptPluginEditor')}
          trailing={<MetaBadge mono>esbuild + goja</MetaBadge>}
        />
        <div className="space-y-3 border-t border-border px-5 py-4">
          <Field label={formatMsg(locale, 'capabilities.pluginName')}>
            <input
              value={plName}
              onChange={(e) => setPlName(e.target.value)}
              className={settingsMonoInputClass}
            />
          </Field>
          <CodeEditorWithHighlight
            testId="plugin-ts-editor"
            rows={8}
            language="ts"
            value={plTS}
            onChange={setPlTS}
            placeholder="export default function (nex: any) { ... }"
          />
          <div className="flex justify-end">
            <SaveButton
              label={formatMsg(locale, 'capabilities.compileDeploy')}
              onClick={() => void savePlugin()}
            />
          </div>
        </div>
      </SettingsGroupCard>

      <SectionHeading
        title={formatMsg(locale, 'capabilities.plugins', { length: plugins.length })}
      />

      <SettingsGroupCard>
        {plugins.length === 0 ? (
          <EmptyRow>{formatMsg(locale, 'capabilities.noDynamicPluginsInstalled')}</EmptyRow>
        ) : (
          plugins.map((p) => {
            const isOpen = expandedPlugin === p.name;
            return (
              <div key={p.name} className="px-5 py-3.5">
                <div className="flex items-center justify-between gap-3">
                  <button
                    type="button"
                    onClick={() => setExpandedPlugin(isOpen ? null : p.name)}
                    className="flex min-w-0 flex-1 items-center gap-2 text-left"
                  >
                    <ChevronRight
                      className={`size-3.5 shrink-0 text-foreground-subtlest transition-transform ${
                        isOpen ? 'rotate-90' : ''
                      }`}
                    />
                    <div className="flex min-w-0 flex-wrap items-center gap-1.5">
                      <span className="font-mono text-ui-base font-medium text-foreground">{p.name}</span>
                      {p.compiled_js_hash ? (
                        <span className="shrink-0 rounded-md bg-success-surface px-1.5 py-0.5 font-mono text-ui-xs text-success-surface-fg">
                          sha256:{p.compiled_js_hash.slice(0, 10)}
                        </span>
                      ) : null}
                    </div>
                  </button>
                  <button
                    type="button"
                    onClick={async () => {
                      await apiFetch(`/api/v1/plugins/${encodeURIComponent(p.name)}`, {
                        method: 'DELETE',
                      });
                      await refreshRegistries();
                    }}
                    aria-label={formatMsg(locale, 'capabilities.deletePlugin')}
                    className="flex size-7 shrink-0 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-destructive-surface hover:text-destructive-surface-fg"
                  >
                    <Trash2 className="size-4" />
                  </button>
                </div>
                {isOpen && p.source_ts && (
                  <div className="mt-3 pl-5">
                    <HighlightedCode code={p.source_ts} language="ts" className="max-h-72 text-ui-xs" />
                  </div>
                )}
              </div>
            );
          })
        )}
      </SettingsGroupCard>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* Automations                                                                */
/* -------------------------------------------------------------------------- */

export function AutomationsSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const { locale, runners, cronJobs, selectedRunnerId, refreshRegistries } = useNexStore();

  const [runnerId, setRunnerId] = useState(selectedRunnerId || runners[0]?.id || '');
  const [title, setTitle] = useState(
    formatMsg(locale, 'capabilities.dailyWorkspaceHealthCheckAt'),
  );
  const [cronExpr, setCronExpr] = useState('0 9 * * *');
  const [prompt, setPrompt] = useState(
    formatMsg(locale, 'capabilities.runTheTestSuiteAnd'),
  );

  const createCron = async () => {
    const targetRunner = runnerId || runners[0]?.id || 'local';
    await apiFetch('/api/v1/cron', {
      method: 'POST',
      body: JSON.stringify({
        runner_id: targetRunner,
        title: title.trim(),
        cron_expr: cronExpr.trim(),
        timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'Local',
        prompt,
        permission_mode: 'autonomous',
        recurring: true,
        enabled: true,
      }),
    });
    await refreshRegistries();
    onFlash(
      formatMsg(locale, 'capabilities.automationScheduledOnRunner', { title }),
    );
  };

  return (
    <div className="space-y-6">
      <SettingsGroupCard>
        <div className="grid grid-cols-1 gap-4 px-5 py-4 sm:grid-cols-3">
          <Field label={formatMsg(locale, 'capabilities.runner')}>
            <select
              value={runnerId}
              onChange={(e) => setRunnerId(e.target.value)}
              className={settingsSelectClass}
            >
              {runners.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name} ({r.os})
                </option>
              ))}
            </select>
          </Field>
          <Field label={formatMsg(locale, 'common.title')}>
            <input
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              className={settingsInputClass}
            />
          </Field>
          <Field
            label={formatMsg(locale, 'capabilities.cronExpression')}
            description={formatMsg(locale, 'capabilities.runsInTheRunnerTimezone')}
          >
            <input
              value={cronExpr}
              onChange={(e) => setCronExpr(e.target.value)}
              placeholder="*/30 * * * *"
              className={settingsMonoInputClass}
            />
          </Field>
        </div>
        <div className="space-y-3 px-5 py-4">
          <Field
            label={formatMsg(locale, 'capabilities.prompt')}
            description={
              formatMsg(locale, 'capabilities.executedAutonomouslyWhenTheSchedule')
            }
          >
            <textarea
              rows={3}
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              className={areaClass}
            />
          </Field>
          <div className="flex justify-end">
            <SaveButton
              label={formatMsg(locale, 'capabilities.createAutomation')}
              onClick={() => void createCron()}
            />
          </div>
        </div>
      </SettingsGroupCard>

      <SectionHeading
        title={formatMsg(locale, 'capabilities.automations', { length: cronJobs.length })}
      />

      <SettingsGroupCard>
        {cronJobs.length === 0 ? (
          <EmptyRow>{formatMsg(locale, 'capabilities.noAutomationsYet')}</EmptyRow>
        ) : (
          cronJobs.map((j) => (
            <div key={j.id} className="flex items-center justify-between gap-3 px-5 py-3.5">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="text-ui-base font-medium text-foreground">{j.title}</span>
                  <MetaBadge mono>{j.cron_expr}</MetaBadge>
                  <span className="text-ui-xs text-foreground-subtlest">
                    {formatMsg(locale, 'capabilities.runs', { runner_id: j.runner_id, v: j.run_count || 0 })}
                  </span>
                </div>
                <p className="mt-0.5 truncate text-ui-sm text-foreground-subtle">{j.prompt}</p>
              </div>
              <button
                type="button"
                onClick={async () => {
                  if (j.id) {
                    await apiFetch(`/api/v1/cron/${encodeURIComponent(j.id)}`, {
                      method: 'DELETE',
                    });
                    await refreshRegistries();
                  }
                }}
                aria-label={formatMsg(locale, 'capabilities.deleteAutomation')}
                className="flex size-7 shrink-0 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-destructive-surface hover:text-destructive-surface-fg"
              >
                <Trash2 className="size-4" />
              </button>
            </div>
          ))
        )}
      </SettingsGroupCard>
    </div>
  );
}


/* -------------------------------------------------------------------------- */
/* Long-term Memories                                                         */
/* -------------------------------------------------------------------------- */

export function MemoriesSettingsSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const {
    locale,
    memories,
    preferences,
    providers,
    models,
    selectedWorkspacePath,
    refreshInitialData,
    refreshRegistries,
    consolidateMemories,
  } = useNexStore();

  const [editingId, setEditingId] = useState('');
  const [scope, setScope] = useState<'global' | 'project'>('global');
  const [category, setCategory] = useState<'preference' | 'architecture' | 'convention' | 'fact'>('preference');
  const [title, setTitle] = useState('');
  const [content, setContent] = useState('');
  const [filterScope, setFilterScope] = useState<'all' | 'global' | 'project'>('all');
  const [searchQuery, setSearchQuery] = useState('');
  const [consolidating, setConsolidating] = useState(false);

  const enabledProviderIds = new Set(providers.filter((p) => p.enabled).map((p) => p.id));
  const selectableModels = models.filter((m) => enabledProviderIds.size === 0 || enabledProviderIds.has(m.provider_id));
  const currentMemModelValue =
    preferences.memory_provider_id && preferences.memory_model_id
      ? `${preferences.memory_provider_id}::${preferences.memory_model_id}`
      : '';

  const saveMemoryPreferences = async (patch: Partial<typeof preferences>) => {
    const next = { ...preferences, ...patch };
    useNexStore.setState({ preferences: next });
    await apiFetch('/api/v1/preferences', {
      method: 'PUT',
      body: JSON.stringify(next),
    });
    await refreshInitialData();
    onFlash(formatMsg(locale, 'capabilities.memoryEngineSettingsUpdatedSynced'));
  };

  const runConsolidate = async () => {
    if (consolidating || memories.length < 2) return;
    setConsolidating(true);
    try {
      const res = await consolidateMemories({
        providerId: preferences.memory_provider_id || undefined,
        modelId: preferences.memory_model_id || undefined,
      });
      if (res) {
        onFlash(
          formatMsg(locale, 'capabilities.consolidated', { before_count: res.before_count, after_count: res.after_count, summary: res.summary }),
        );
      }
    } catch (err) {
      onFlash(
        formatMsg(locale, 'capabilities.consolidationFailed', { v: err instanceof Error ? err.message : String(err) }),
      );
    } finally {
      setConsolidating(false);
    }
  };

  const saveMemory = async () => {
    if (!title.trim() || !content.trim()) return;
    await apiFetch('/api/v1/memories', {
      method: 'POST',
      body: JSON.stringify({
        id: editingId || undefined,
        scope,
        workspace_path: scope === 'project' ? selectedWorkspacePath : '',
        category,
        title: title.trim(),
        content: content.trim(),
      }),
    });
    setEditingId('');
    setTitle('');
    setContent('');
    await refreshRegistries();
    onFlash(formatMsg(locale, 'capabilities.memorySavedAndSyncedTo'));
  };

  const startEdit = (m: (typeof memories)[number]) => {
    setEditingId(m.id);
    setScope(m.scope === 'project' ? 'project' : 'global');
    setCategory(
      (m.category === 'preference' || m.category === 'architecture' || m.category === 'convention' || m.category === 'fact'
        ? m.category
        : 'fact') as 'preference' | 'architecture' | 'convention' | 'fact',
    );
    setTitle(m.title);
    setContent(m.content);
  };

  const removeMemory = async (id: string) => {
    await apiFetch(`/api/v1/memories/${encodeURIComponent(id)}`, { method: 'DELETE' });
    if (editingId === id) {
      setEditingId('');
      setTitle('');
      setContent('');
    }
    await refreshRegistries();
  };

  const qLower = searchQuery.trim().toLowerCase();
  const visibleMemories = memories.filter((m) => {
    if (filterScope !== 'all' && m.scope !== filterScope) return false;
    if (!qLower) return true;
    return (
      m.title.toLowerCase().includes(qLower) ||
      m.content.toLowerCase().includes(qLower) ||
      m.category.toLowerCase().includes(qLower)
    );
  });

  return (
    <div className="space-y-6">
      {/* Memory Engine Automation & Dream Consolidation Card */}
      <SettingsGroupCard>
        <div className="flex flex-wrap items-center justify-between gap-4 px-5 py-4">
          <div className="min-w-0 flex-1 space-y-0.5">
            <div className="text-ui-base font-medium text-foreground">
              {formatMsg(locale, 'capabilities.backgroundAutoExtraction')}
            </div>
            <p className="text-ui-xs text-foreground-subtlest">
              {formatMsg(locale, 'capabilities.asynchronouslyDistillsDurablePreferencesConventions')}
            </p>
          </div>
          <label className="inline-flex cursor-pointer items-center gap-2 text-ui-sm text-foreground">
            <input
              type="checkbox"
              data-testid="memory-auto-extract-toggle"
              checked={Boolean(preferences.memory_auto_extract)}
              onChange={(e) => void saveMemoryPreferences({ memory_auto_extract: e.target.checked })}
              className="size-4 accent-brand-blue"
            />
            <span>{preferences.memory_auto_extract ? (formatMsg(locale, 'common.enabled')) : formatMsg(locale, 'common.disabled')}</span>
          </label>
        </div>

        <div className="flex flex-wrap items-end justify-between gap-4 border-t border-border px-5 py-4">
          <div className="min-w-[240px] flex-1">
            <Field
              label={formatMsg(locale, 'capabilities.dedicatedModelForExtractionConsolidation')}
              description={
                formatMsg(locale, 'capabilities.optionalFastLowCostModel')
              }
            >
              <select
                data-testid="memory-model-select"
                value={currentMemModelValue}
                onChange={(e) => {
                  const val = e.target.value;
                  if (!val) {
                    void saveMemoryPreferences({ memory_provider_id: '', memory_model_id: '' });
                  } else {
                    const [pid, mid] = val.split('::');
                    void saveMemoryPreferences({ memory_provider_id: pid, memory_model_id: mid });
                  }
                }}
                className={settingsSelectClass}
              >
                <option value="">{formatMsg(locale, 'capabilities.followActiveSessionModel')}</option>
                {selectableModels.map((m) => (
                  <option key={`${m.provider_id}::${m.id}`} value={`${m.provider_id}::${m.id}`}>
                    {m.display_name || m.id} ({m.provider_id})
                  </option>
                ))}
              </select>
            </Field>
          </div>

          <button
            type="button"
            data-testid="consolidate-memories-btn"
            disabled={consolidating || memories.length < 2}
            onClick={() => void runConsolidate()}
            title={
              memories.length < 2
                ? formatMsg(locale, 'capabilities.requiresAtLeast2Memories')
                : formatMsg(locale, 'capabilities.deduplicateMergeConflictsAndTighten')
            }
            className="inline-flex h-9 shrink-0 items-center gap-1.5 rounded-lg border border-border bg-surface px-3.5 text-ui-sm font-medium text-foreground transition-colors hover:bg-hover disabled:cursor-not-allowed disabled:opacity-45"
          >
            {consolidating ? (
              <LoaderCircle className="size-3.5 animate-spin text-brand-blue" />
            ) : (
              <Sparkles className="size-3.5 text-brand-blue" />
            )}
            <span>
              {consolidating
                ? formatMsg(locale, 'capabilities.consolidating')
                : formatMsg(locale, 'capabilities.consolidateDream')}
            </span>
          </button>
        </div>
      </SettingsGroupCard>

      {/* Add / Edit Memory Card */}
      <SettingsGroupCard>
        <div className="grid grid-cols-1 gap-4 px-5 py-4 sm:grid-cols-3">
          <Field label={formatMsg(locale, 'capabilities.scope')}>
            <select
              value={scope}
              onChange={(e) => setScope(e.target.value as 'global' | 'project')}
              className={settingsSelectClass}
            >
              <option value="global">{formatMsg(locale, 'capabilities.globalAllWorkspaces')}</option>
              <option value="project">{formatMsg(locale, 'capabilities.currentProject')}</option>
            </select>
          </Field>
          <Field label={formatMsg(locale, 'capabilities.category')}>
            <select
              value={category}
              onChange={(e) => setCategory(e.target.value as 'preference' | 'architecture' | 'convention' | 'fact')}
              className={settingsSelectClass}
            >
              <option value="preference">{formatMsg(locale, 'capabilities.preference')}</option>
              <option value="convention">{formatMsg(locale, 'capabilities.convention')}</option>
              <option value="architecture">{formatMsg(locale, 'capabilities.architecture')}</option>
              <option value="fact">{formatMsg(locale, 'capabilities.fact')}</option>
            </select>
          </Field>
          <Field label={formatMsg(locale, 'common.title2')}>
            <input
              data-testid="memory-title-input"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder={formatMsg(locale, 'capabilities.eGPreferCgoEnabled')}
              className={settingsInputClass}
            />
          </Field>
        </div>

        <div className="space-y-3 border-t border-border px-5 py-4">
          <Field label={formatMsg(locale, 'capabilities.memoryContent')}>
            <CodeEditorWithHighlight
              testId="memory-content-editor"
              rows={4}
              language="markdown"
              value={content}
              onChange={setContent}
              placeholder={
                formatMsg(locale, 'capabilities.conciseFactOrPreferenceRecalled')
              }
            />
          </Field>
          <div className="flex items-center justify-end gap-2">
            {editingId && (
              <button
                type="button"
                onClick={() => {
                  setEditingId('');
                  setTitle('');
                  setContent('');
                }}
                className="h-8 rounded-lg border border-border px-3 text-ui-sm text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
              >
                {formatMsg(locale, 'capabilities.cancelEdit')}
              </button>
            )}
            <SaveButton
              label={
                editingId
                  ? formatMsg(locale, 'capabilities.updateMemory')
                  : formatMsg(locale, 'capabilities.saveMemory')
              }
              onClick={() => void saveMemory()}
            />
          </div>
        </div>
      </SettingsGroupCard>

      <div className="flex flex-wrap items-center justify-between gap-2">
        <SectionHeading
          title={formatMsg(locale, 'capabilities.savedMemories', { length: visibleMemories.length })}
        />
        <div className="flex flex-wrap items-center gap-2">
          <div className="relative">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-foreground-subtlest" />
            <input
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              placeholder={formatMsg(locale, 'capabilities.filterMemories')}
              className="h-7 w-44 rounded-lg border border-border bg-input pl-8 pr-2.5 text-ui-xs text-foreground placeholder-foreground-subtlest"
            />
          </div>
          <div className="inline-flex rounded-lg border border-border bg-surface p-0.5 text-ui-xs">
            {(['all', 'global', 'project'] as const).map((sc) => (
              <button
                key={sc}
                type="button"
                onClick={() => setFilterScope(sc)}
                className={`rounded-md px-2.5 py-1 font-medium transition-colors ${
                  filterScope === sc ? 'bg-popover text-foreground shadow-xs' : 'text-foreground-subtle hover:text-foreground'
                }`}
              >
                {sc === 'all'
                  ? formatMsg(locale, 'common.all')
                  : sc === 'global'
                    ? formatMsg(locale, 'common.global')
                    : formatMsg(locale, 'common.project')}
              </button>
            ))}
          </div>
        </div>
      </div>

      <SettingsGroupCard>
        {visibleMemories.length === 0 ? (
          <EmptyRow>
            {formatMsg(locale, 'capabilities.noMemoriesSavedYetAdd')}
          </EmptyRow>
        ) : (
          visibleMemories.map((m) => (
            <div key={m.id} className="flex items-start justify-between gap-3 px-5 py-3.5">
              <div className="min-w-0 space-y-1">
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="text-ui-base font-medium text-foreground">{m.title}</span>
                  <MetaBadge mono>{m.id}</MetaBadge>
                  <MetaBadge mono>{m.scope}</MetaBadge>
                  <MetaBadge mono>{m.category}</MetaBadge>
                </div>
                <p className="whitespace-pre-wrap text-ui-sm text-foreground-subtle">{m.content}</p>
                {m.workspace_path ? (
                  <p className="font-mono text-ui-xs text-foreground-subtlest">{m.workspace_path}</p>
                ) : null}
              </div>
              <div className="flex shrink-0 items-center gap-1">
                <button
                  type="button"
                  onClick={() => startEdit(m)}
                  aria-label={formatMsg(locale, 'capabilities.editMemory')}
                  className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
                >
                  <Pencil className="size-3.5" />
                </button>
                <button
                  type="button"
                  onClick={() => void removeMemory(m.id)}
                  aria-label={formatMsg(locale, 'capabilities.deleteMemory')}
                  className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-destructive-surface hover:text-destructive-surface-fg"
                >
                  <Trash2 className="size-4" />
                </button>
              </div>
            </div>
          ))
        )}
      </SettingsGroupCard>
    </div>
  );
}
