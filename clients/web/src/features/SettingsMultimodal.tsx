import { Cable, Check, Eye, EyeOff, Package, Plus, Trash2 } from 'lucide-react';
import { useEffect, useState } from 'react';
import { apiFetch } from '../core/protocol';
import { useNexStore } from '../core/store';
import { MultimodalProviderProfile, ProviderRecord, UserPreferences } from '../core/types';
import { formatMsg, type MessageKey } from '../i18n/messages';
import {
  SettingsGroupCard,
  SettingsRow,
  Toggle,
  settingsInputClass,
  settingsMonoInputClass,
  settingsSelectClass,
} from './SettingsPage';

type ModalityKind = 'image' | 'classifier';

interface DriverOption {
  id: string;
  labelKey: MessageKey;
  defaultUrl: string;
  defaultModel: string;
}

const IMAGE_DRIVERS: DriverOption[] = [
  {
    id: 'openrouter_images',
    labelKey: 'settings.imageDriverOpenRouter',
    defaultUrl: 'https://openrouter.ai/api/v1',
    defaultModel: 'google/gemini-2.5-flash-image',
  },
  {
    id: 'openai_images',
    labelKey: 'settings.imageDriverOpenAI',
    defaultUrl: 'https://api.openai.com/v1',
    defaultModel: 'gpt-image-1',
  },
  {
    id: 'xai_images',
    labelKey: 'settings.imageDriverXAI',
    defaultUrl: 'https://api.x.ai/v1',
    defaultModel: 'grok-2-image',
  },
  {
    id: 'minimax_images',
    labelKey: 'settings.imageDriverMiniMax',
    defaultUrl: 'https://api.minimax.io/v1',
    defaultModel: 'image-01',
  },
];

const CLASSIFIER_DRIVERS: DriverOption[] = [
  {
    id: 'typesafe_system_one',
    labelKey: 'settings.classifierDriverTypeSafe',
    defaultUrl: 'https://api.typesafe.ai/v1',
    defaultModel: 'jev-latest',
  },
  {
    id: 'openrouter_system_one',
    labelKey: 'settings.classifierDriverOpenRouter',
    defaultUrl: 'https://openrouter.ai/api/v1',
    defaultModel: 'typesafe/jev-1.14',
  },
];

function inferDriverFromChatProvider(kind: ModalityKind, p: ProviderRecord): DriverOption {
  const id = (p.id || '').toLowerCase();
  const url = (p.base_url || '').toLowerCase();
  if (kind === 'classifier') {
    if (id.includes('openrouter') || url.includes('openrouter.ai')) {
      return CLASSIFIER_DRIVERS[1];
    }
    return CLASSIFIER_DRIVERS[0];
  }
  if (id.includes('openrouter') || url.includes('openrouter.ai')) return IMAGE_DRIVERS[0];
  if (id.includes('xai') || id.includes('grok') || url.includes('api.x.ai')) return IMAGE_DRIVERS[2];
  if (id.includes('minimax') || url.includes('minimax')) return IMAGE_DRIVERS[3];
  return IMAGE_DRIVERS[1];
}

function MultimodalProviderManager({
  kind,
  onFlash,
}: {
  kind: ModalityKind;
  onFlash: (msg: string) => void;
}) {
  const { locale, providers: chatProviders, preferences, refreshInitialData } = useNexStore();
  const drivers = kind === 'image' ? IMAGE_DRIVERS : CLASSIFIER_DRIVERS;

  const storedProfiles: MultimodalProviderProfile[] =
    kind === 'image' ? preferences.image_providers ?? [] : preferences.classifier_providers ?? [];
  const storedActiveProviderId =
    kind === 'image'
      ? preferences.active_image_provider_id ?? ''
      : preferences.active_classifier_provider_id ?? '';
  const storedActiveModelId =
    kind === 'image'
      ? preferences.active_image_model_id ?? ''
      : preferences.active_classifier_model_id ?? '';

  const [profiles, setProfiles] = useState<MultimodalProviderProfile[]>(storedProfiles);
  const [selectedId, setSelectedId] = useState<string>(storedActiveProviderId || storedProfiles[0]?.id || '');
  const [activeProviderId, setActiveProviderId] = useState<string>(storedActiveProviderId);
  const [activeModelId, setActiveModelId] = useState<string>(storedActiveModelId);
  const [showKey, setShowKey] = useState(false);
  const [newModelInput, setNewModelInput] = useState('');
  const [saving, setSaving] = useState(false);
  const [testingModel, setTestingModel] = useState<string | null>(null);
  const [testResults, setTestResults] = useState<Record<string, { ok: boolean; msg: string }>>({});

  useEffect(() => {
    setProfiles(storedProfiles);
    setActiveProviderId(storedActiveProviderId);
    setActiveModelId(storedActiveModelId);
    if (!selectedId && storedProfiles.length > 0) {
      setSelectedId(storedActiveProviderId || storedProfiles[0].id);
    }
  }, [storedProfiles, storedActiveProviderId, storedActiveModelId]);

  const selected = profiles.find((p) => p.id === selectedId) || null;
  const selectedDriver = drivers.find((d) => d.id === selected?.driver) || drivers[0];

  const persist = async (
    nextProfiles: MultimodalProviderProfile[],
    nextActivePid: string,
    nextActiveMid: string,
    flashKey?: MessageKey,
  ) => {
    setSaving(true);
    try {
      const patch: Partial<UserPreferences> =
        kind === 'image'
          ? {
              image_providers: nextProfiles,
              active_image_provider_id: nextActivePid,
              active_image_model_id: nextActiveMid,
            }
          : {
              classifier_providers: nextProfiles,
              active_classifier_provider_id: nextActivePid,
              active_classifier_model_id: nextActiveMid,
            };
      await apiFetch<UserPreferences>('/api/v1/preferences', {
        method: 'PUT',
        body: JSON.stringify({ ...preferences, ...patch }),
      });
      await refreshInitialData();
      if (flashKey) onFlash(formatMsg(locale, flashKey));
    } catch (err) {
      onFlash(formatMsg(locale, 'settings.multimodalSaveFailed', { error: String(err) }));
    } finally {
      setSaving(false);
    }
  };

  const addNewProvider = () => {
    const def = drivers[0];
    const id = `${kind}-${Date.now().toString(36)}`;
    const prof: MultimodalProviderProfile = {
      id,
      name: kind === 'image' ? 'New Image Provider' : 'New Classifier Provider',
      driver: def.id,
      base_url: def.defaultUrl,
      api_key: '',
      models: [def.defaultModel],
    };
    const next = [...profiles, prof];
    setProfiles(next);
    setSelectedId(id);
    const nextActivePid = activeProviderId || id;
    const nextActiveMid = activeModelId || def.defaultModel;
    setActiveProviderId(nextActivePid);
    setActiveModelId(nextActiveMid);
    void persist(next, nextActivePid, nextActiveMid);
  };

  const importFromChatProvider = (chatId: string) => {
    if (!chatId) return;
    const cp = chatProviders.find((p) => p.id === chatId);
    if (!cp) return;
    const drv = inferDriverFromChatProvider(kind, cp);
    const id = `${kind}-${cp.id}-${Date.now().toString(36).slice(-4)}`;
    const prof: MultimodalProviderProfile = {
      id,
      name: cp.name || cp.id,
      driver: drv.id,
      base_url: cp.base_url || drv.defaultUrl,
      api_key: cp.api_key || '',
      models: [drv.defaultModel],
    };
    const next = [...profiles, prof];
    setProfiles(next);
    setSelectedId(id);
    const nextActivePid = activeProviderId || id;
    const nextActiveMid = activeModelId || drv.defaultModel;
    setActiveProviderId(nextActivePid);
    setActiveModelId(nextActiveMid);
    void persist(
      next,
      nextActivePid,
      nextActiveMid,
      kind === 'image' ? 'settings.imageSaved' : 'settings.classifierSaved',
    );
  };

  const updateSelected = (patch: Partial<MultimodalProviderProfile>) => {
    if (!selected) return;
    setProfiles((prev) => prev.map((p) => (p.id === selected.id ? { ...p, ...patch } : p)));
  };

  const deleteSelected = () => {
    if (!selected) return;
    const next = profiles.filter((p) => p.id !== selected.id);
    let nextActivePid = activeProviderId;
    let nextActiveMid = activeModelId;
    if (activeProviderId === selected.id) {
      nextActivePid = next[0]?.id || '';
      nextActiveMid = next[0]?.models?.[0] || '';
    }
    setProfiles(next);
    setSelectedId(next[0]?.id || '');
    setActiveProviderId(nextActivePid);
    setActiveModelId(nextActiveMid);
    void persist(next, nextActivePid, nextActiveMid);
  };

  const addModelToSelected = () => {
    if (!selected) return;
    const m = newModelInput.trim();
    if (!m) return;
    const cur = selected.models ?? [];
    if (cur.includes(m)) {
      setNewModelInput('');
      return;
    }
    const nextProfiles = profiles.map((p) => (p.id === selected.id ? { ...p, models: [...cur, m] } : p));
    setProfiles(nextProfiles);
    setNewModelInput('');
    const nextActivePid = activeProviderId || selected.id;
    const nextActiveMid = activeModelId || m;
    setActiveProviderId(nextActivePid);
    setActiveModelId(nextActiveMid);
    void persist(nextProfiles, nextActivePid, nextActiveMid);
  };

  const removeModelFromSelected = (modelId: string) => {
    if (!selected) return;
    const nextModels = (selected.models ?? []).filter((m) => m !== modelId);
    const nextProfiles = profiles.map((p) => (p.id === selected.id ? { ...p, models: nextModels } : p));
    let nextActivePid = activeProviderId;
    let nextActiveMid = activeModelId;
    if (activeProviderId === selected.id && activeModelId === modelId) {
      nextActiveMid = nextModels[0] || '';
      if (!nextActiveMid) nextActivePid = '';
    }
    setProfiles(nextProfiles);
    setActiveProviderId(nextActivePid);
    setActiveModelId(nextActiveMid);
    void persist(nextProfiles, nextActivePid, nextActiveMid);
  };

  const activateModel = (providerId: string, modelId: string) => {
    setActiveProviderId(providerId);
    setActiveModelId(modelId);
    void persist(
      profiles,
      providerId,
      modelId,
      kind === 'image' ? 'settings.imageSaved' : 'settings.classifierSaved',
    );
  };

  const testModel = async (modelId: string) => {
    if (!selected) return;
    const key = `${selected.id}:${modelId}`;
    setTestingModel(key);
    try {
      const endpoint = kind === 'image' ? '/api/v1/image/test' : '/api/v1/classifier/test';
      const res = await apiFetch<{ ok: boolean; latency_ms?: number; error?: string; summary?: string }>(endpoint, {
        method: 'POST',
        body: JSON.stringify({
          driver: selected.driver,
          base_url: selected.base_url,
          api_key: selected.api_key || '',
          model_id: modelId,
        }),
      });
      if (res.ok) {
        const msg = formatMsg(locale, 'settings.multimodalTestPassed', { ms: String(res.latency_ms ?? 0) });
        setTestResults((prev) => ({
          ...prev,
          [key]: { ok: true, msg: res.summary ? `${msg} · ${res.summary}` : msg },
        }));
      } else {
        setTestResults((prev) => ({
          ...prev,
          [key]: {
            ok: false,
            msg: formatMsg(locale, 'settings.multimodalTestFailed', { error: res.error || 'unknown' }),
          },
        }));
      }
    } catch (err) {
      setTestResults((prev) => ({
        ...prev,
        [key]: { ok: false, msg: formatMsg(locale, 'settings.multimodalTestFailed', { error: String(err) }) },
      }));
    } finally {
      setTestingModel(null);
    }
  };

  const enabledChatProviders = chatProviders.filter((p) => p.enabled);

  return (
    <div className="mt-5 space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3 className="text-ui-lg font-semibold text-foreground">
            {formatMsg(locale, kind === 'image' ? 'settings.imageSectionTitle' : 'settings.classifierSectionTitle')}
          </h3>
          <p className="mt-0.5 text-ui-sm text-foreground-subtle">
            {formatMsg(locale, kind === 'image' ? 'settings.imageSectionDesc' : 'settings.classifierSectionDesc')}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {enabledChatProviders.length > 0 ? (
            <select
              value=""
              onChange={(e) => importFromChatProvider(e.target.value)}
              className="h-8 rounded-lg border border-border bg-input px-2.5 text-ui-xs text-foreground"
            >
              <option value="">{formatMsg(locale, 'settings.multimodalImportFromChat')}</option>
              {enabledChatProviders.map((cp) => (
                <option key={cp.id} value={cp.id}>
                  {cp.name || cp.id}
                </option>
              ))}
            </select>
          ) : null}
          <button
            type="button"
            onClick={addNewProvider}
            className="flex h-8 items-center gap-1 rounded-lg bg-primary px-3 text-ui-sm font-medium text-primary-foreground hover:opacity-90"
          >
            <Plus className="size-3.5" />
            <span>{formatMsg(locale, 'settings.multimodalAddProvider')}</span>
          </button>
        </div>
      </div>

      <div className="grid grid-cols-1 overflow-hidden rounded-2xl border border-border bg-card md:grid-cols-[220px_minmax(0,1fr)]">
        {/* Left: provider list */}
        <div className="space-y-1 border-b border-border p-3 md:border-r md:border-b-0">
          {profiles.length === 0 ? (
            <p className="px-2.5 py-3 text-ui-xs text-foreground-subtlest">
              {formatMsg(locale, 'settings.multimodalNoProviders')}
            </p>
          ) : (
            profiles.map((p) => {
              const isSelected = p.id === selected?.id;
              const isActive = p.id === activeProviderId;
              return (
                <button
                  key={p.id}
                  type="button"
                  onClick={() => setSelectedId(p.id)}
                  className={`flex w-full items-center justify-between gap-2 rounded-lg px-2.5 py-2 text-left text-ui-sm transition-colors ${
                    isSelected
                      ? 'bg-hover font-medium text-foreground'
                      : 'text-foreground-subtle hover:bg-hover/60 hover:text-foreground'
                  }`}
                >
                  <span className="flex min-w-0 items-center gap-2">
                    <span
                      className={`size-2 shrink-0 rounded-full ${isActive ? 'bg-primary' : 'bg-border'}`}
                    />
                    <span className="truncate">{p.name || p.id}</span>
                  </span>
                  <span className="shrink-0 font-mono text-ui-xs text-foreground-subtlest">
                    {(p.models ?? []).length}
                  </span>
                </button>
              );
            })
          )}
        </div>

        {/* Right: provider details + models + test */}
        <div className="min-w-0 p-5">
          {!selected ? (
            <div className="flex min-h-[220px] flex-col items-center justify-center gap-2 text-center">
              <Package className="size-6 text-foreground-subtlest" />
              <p className="text-ui-sm text-foreground-subtle">
                {formatMsg(locale, 'settings.multimodalSelectProvider')}
              </p>
            </div>
          ) : (
            <div className="space-y-4">
              <div className="flex items-center justify-between gap-2">
                <input
                  value={selected.name}
                  onChange={(e) => updateSelected({ name: e.target.value })}
                  onBlur={() => void persist(profiles, activeProviderId, activeModelId)}
                  className="h-8 min-w-0 flex-1 rounded-lg border border-border/70 bg-input/60 px-2.5 text-ui-base font-semibold text-foreground"
                />
                <button
                  type="button"
                  onClick={deleteSelected}
                  title={formatMsg(locale, 'settings.multimodalDeleteProvider')}
                  className="flex size-8 items-center justify-center rounded-lg text-foreground-subtlest hover:bg-destructive-surface hover:text-destructive-surface-fg"
                >
                  <Trash2 className="size-4" />
                </button>
              </div>

              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <label className="space-y-1">
                  <span className="text-ui-xs font-medium text-foreground-subtle">
                    {formatMsg(locale, kind === 'image' ? 'settings.imageDriver' : 'settings.classifierDriver')}
                  </span>
                  <select
                    value={selected.driver}
                    onChange={(e) => {
                      const nextDrv = drivers.find((d) => d.id === e.target.value) || drivers[0];
                      updateSelected({
                        driver: nextDrv.id,
                        base_url: selected.base_url || nextDrv.defaultUrl,
                      });
                    }}
                    className={settingsSelectClass}
                  >
                    {drivers.map((d) => (
                      <option key={d.id} value={d.id}>
                        {formatMsg(locale, d.labelKey)}
                      </option>
                    ))}
                  </select>
                </label>

                <label className="space-y-1">
                  <span className="text-ui-xs font-medium text-foreground-subtle">
                    {formatMsg(locale, 'settings.apiEndpoint')}
                  </span>
                  <input
                    value={selected.base_url}
                    onChange={(e) => updateSelected({ base_url: e.target.value })}
                    placeholder={selectedDriver.defaultUrl}
                    className={settingsInputClass}
                  />
                </label>
              </div>

              <label className="block space-y-1">
                <span className="text-ui-xs font-medium text-foreground-subtle">
                  {formatMsg(locale, 'settings.apiKey')}
                </span>
                <div className="relative flex items-center">
                  <input
                    type={showKey ? 'text' : 'password'}
                    value={selected.api_key ?? ''}
                    onChange={(e) => updateSelected({ api_key: e.target.value })}
                    placeholder={kind === 'classifier' ? 'ts-... (optional for llama.cpp)' : 'sk-...'}
                    className={`${settingsMonoInputClass} pr-9`}
                  />
                  <button
                    type="button"
                    onClick={() => setShowKey((v) => !v)}
                    className="absolute right-2 flex size-6 items-center justify-center rounded text-foreground-subtlest hover:text-foreground"
                  >
                    {showKey ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}
                  </button>
                </div>
              </label>

              <div className="flex justify-end">
                <button
                  type="button"
                  disabled={saving}
                  onClick={() =>
                    void persist(
                      profiles,
                      activeProviderId,
                      activeModelId,
                      kind === 'image' ? 'settings.imageSaved' : 'settings.classifierSaved',
                    )
                  }
                  className="h-8 rounded-lg bg-primary px-4 text-ui-sm font-medium text-primary-foreground hover:opacity-90 disabled:opacity-50"
                >
                  {saving ? formatMsg(locale, 'settings.saving') : formatMsg(locale, 'settings.saveChanges')}
                </button>
              </div>

              {/* Models under this provider */}
              <div className="space-y-2 border-t border-border pt-4">
                <div className="text-ui-xs font-medium text-foreground-subtle">
                  {formatMsg(locale, 'settings.multimodalModelsTitle')}
                </div>

                <div className="flex items-center gap-2">
                  <input
                    value={newModelInput}
                    onChange={(e) => setNewModelInput(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') {
                        e.preventDefault();
                        addModelToSelected();
                      }
                    }}
                    placeholder={selectedDriver.defaultModel}
                    className={settingsMonoInputClass}
                  />
                  <button
                    type="button"
                    onClick={addModelToSelected}
                    className="h-9 shrink-0 rounded-lg border border-border bg-surface px-3 text-ui-xs font-medium text-foreground hover:bg-hover"
                  >
                    {formatMsg(locale, 'settings.multimodalAddModel')}
                  </button>
                </div>

                <div className="divide-y divide-border rounded-xl border border-border bg-surface/40">
                  {(selected.models ?? []).map((m) => {
                    const isActive = activeProviderId === selected.id && activeModelId === m;
                    const testKey = `${selected.id}:${m}`;
                    const testing = testingModel === testKey;
                    const tRes = testResults[testKey];
                    return (
                      <div key={m} className="flex flex-col gap-1 px-3 py-2.5">
                        <div className="flex items-center justify-between gap-2">
                          <span className="min-w-0 truncate font-mono text-ui-sm text-foreground">{m}</span>
                          <div className="flex shrink-0 items-center gap-1.5">
                            <button
                              type="button"
                              disabled={testing}
                              onClick={() => void testModel(m)}
                              title={formatMsg(locale, 'settings.testModelConnection')}
                              className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest hover:bg-hover hover:text-foreground disabled:opacity-40"
                            >
                              <Cable className={`size-3.5 ${testing ? 'animate-pulse text-primary' : ''}`} />
                            </button>
                            <button
                              type="button"
                              onClick={() => removeModelFromSelected(m)}
                              title={formatMsg(locale, 'settings.deleteModel')}
                              className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest hover:bg-destructive-surface hover:text-destructive-surface-fg"
                            >
                              <Trash2 className="size-3.5" />
                            </button>
                            <button
                              type="button"
                              onClick={() => activateModel(selected.id, m)}
                              className={`flex h-7 items-center gap-1 rounded-md px-2.5 text-ui-xs font-medium transition-colors ${
                                isActive
                                  ? 'bg-primary text-primary-foreground'
                                  : 'border border-border bg-card text-foreground-subtle hover:text-foreground'
                              }`}
                            >
                              {isActive ? <Check className="size-3" /> : null}
                              <span>
                                {formatMsg(
                                  locale,
                                  isActive ? 'settings.multimodalActiveBadge' : 'settings.multimodalSetActive',
                                )}
                              </span>
                            </button>
                          </div>
                        </div>
                        {tRes ? (
                          <div
                            className={`text-ui-xs ${tRes.ok ? 'text-emerald-500' : 'text-destructive-surface-fg'}`}
                          >
                            {tRes.msg}
                          </div>
                        ) : null}
                      </div>
                    );
                  })}
                </div>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

export function ImageGenerationSettingsCard({ onFlash }: { onFlash: (msg: string) => void }) {
  return <MultimodalProviderManager kind="image" onFlash={onFlash} />;
}

export function ClassifierSettingsCard({ onFlash }: { onFlash: (msg: string) => void }) {
  const { locale, preferences, refreshInitialData } = useNexStore();
  const [jevAutoApprove, setJevAutoApprove] = useState(Boolean(preferences.jev_auto_approve_enabled));
  const [jevAutoRouter, setJevAutoRouter] = useState(Boolean(preferences.jev_auto_router_enabled));

  useEffect(() => {
    setJevAutoApprove(Boolean(preferences.jev_auto_approve_enabled));
    setJevAutoRouter(Boolean(preferences.jev_auto_router_enabled));
  }, [preferences.jev_auto_approve_enabled, preferences.jev_auto_router_enabled]);

  const saveToggle = async (patch: Partial<UserPreferences>) => {
    try {
      await apiFetch<UserPreferences>('/api/v1/preferences', {
        method: 'PUT',
        body: JSON.stringify({ ...preferences, ...patch }),
      });
      await refreshInitialData();
      onFlash(formatMsg(locale, 'settings.classifierSaved'));
    } catch (err) {
      onFlash(formatMsg(locale, 'settings.multimodalSaveFailed', { error: String(err) }));
    }
  };

  return (
    <div className="space-y-5">
      <MultimodalProviderManager kind="classifier" onFlash={onFlash} />

      <div className="space-y-2">
        <h4 className="text-ui-base font-semibold text-foreground">
          {formatMsg(locale, 'settings.multimodalJevBehavior')}
        </h4>
        <SettingsGroupCard>
          <SettingsRow
            label={formatMsg(locale, 'settings.jevAutoApprove')}
            description={formatMsg(locale, 'settings.jevAutoApproveDesc')}
          >
            <Toggle
              checked={jevAutoApprove}
              onChange={(checked) => {
                setJevAutoApprove(checked);
                void saveToggle({ jev_auto_approve_enabled: checked });
              }}
              label={formatMsg(locale, 'settings.jevAutoApprove')}
            />
          </SettingsRow>
          <SettingsRow
            label={formatMsg(locale, 'settings.jevAutoRouter')}
            description={formatMsg(locale, 'settings.jevAutoRouterDesc')}
          >
            <Toggle
              checked={jevAutoRouter}
              onChange={(checked) => {
                setJevAutoRouter(checked);
                void saveToggle({ jev_auto_router_enabled: checked });
              }}
              label={formatMsg(locale, 'settings.jevAutoRouter')}
            />
          </SettingsRow>
        </SettingsGroupCard>
      </div>
    </div>
  );
}

const STT_PRESETS = [
  {
    id: 'openai',
    label: 'OpenAI',
    baseUrl: 'https://api.openai.com/v1',
    model: 'gpt-4o-mini-transcribe',
  },
  {
    id: 'groq',
    label: 'Groq',
    baseUrl: 'https://api.groq.com/openai/v1',
    model: 'whisper-large-v3-turbo',
  },
  {
    id: 'siliconflow',
    label: 'SiliconFlow',
    baseUrl: 'https://api.siliconflow.cn/v1',
    model: 'FunAudioLLM/SenseVoiceSmall',
  },
  {
    id: 'local',
    label: 'Local Whisper',
    baseUrl: 'http://127.0.0.1:8000/v1',
    model: 'whisper-1',
  },
] as const;

export function VoiceInputSettingsCard({ onFlash }: { onFlash: (msg: string) => void }) {
  const { locale, preferences, refreshInitialData } = useNexStore();

  const [enabled, setEnabled] = useState(preferences?.stt_enabled !== false);
  const [engine, setEngine] = useState<string>(preferences?.stt_engine || 'auto');
  const [baseUrl, setBaseUrl] = useState(preferences?.stt_base_url || '');
  const [apiKey, setApiKey] = useState(preferences?.stt_api_key || '');
  const [showKey, setShowKey] = useState(false);
  const [model, setModel] = useState(preferences?.stt_model || 'gpt-4o-mini-transcribe');
  const [language, setLanguage] = useState(preferences?.stt_language || '');
  const [autoSend, setAutoSend] = useState(Boolean(preferences?.stt_auto_send));
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setEnabled(preferences?.stt_enabled !== false);
    setEngine(preferences?.stt_engine || 'auto');
    setBaseUrl(preferences?.stt_base_url || '');
    setApiKey(preferences?.stt_api_key || '');
    setModel(preferences?.stt_model || 'gpt-4o-mini-transcribe');
    setLanguage(preferences?.stt_language || '');
    setAutoSend(Boolean(preferences?.stt_auto_send));
  }, [
    preferences?.stt_enabled,
    preferences?.stt_engine,
    preferences?.stt_base_url,
    preferences?.stt_api_key,
    preferences?.stt_model,
    preferences?.stt_language,
    preferences?.stt_auto_send,
  ]);

  const persist = async (patch: Partial<UserPreferences>, flashKey?: MessageKey) => {
    if (!preferences) return;
    setSaving(true);
    try {
      const next: UserPreferences = { ...preferences, ...patch };
      await apiFetch('/api/v1/preferences', {
        method: 'POST',
        body: JSON.stringify(next),
      });
      await refreshInitialData();
      if (flashKey) {
        onFlash(formatMsg(locale, flashKey));
      }
    } catch (err) {
      onFlash(formatMsg(locale, 'settings.multimodalSaveFailed', { error: String(err) }));
    } finally {
      setSaving(false);
    }
  };

  const handleSaveApi = () => {
    void persist(
      {
        stt_enabled: enabled,
        stt_engine: engine,
        stt_base_url: baseUrl.trim(),
        stt_api_key: apiKey.trim(),
        stt_model: model.trim() || 'gpt-4o-mini-transcribe',
        stt_language: language,
        stt_auto_send: autoSend,
      },
      'settings.voiceSaved',
    );
  };

  return (
    <div className="mt-5 space-y-5">
      <SettingsGroupCard>
        <SettingsRow
          label={formatMsg(locale, 'settings.voiceEnabled')}
          description={formatMsg(locale, 'settings.voiceEnabledDesc')}
        >
          <Toggle
            checked={enabled}
            onChange={(checked) => {
              setEnabled(checked);
              void persist({ stt_enabled: checked });
            }}
            label={formatMsg(locale, 'settings.voiceEnabled')}
          />
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.voiceEngine')}
          description={formatMsg(locale, 'settings.voiceEngineDesc')}
        >
          <select
            value={engine}
            onChange={(e) => {
              const v = e.target.value;
              setEngine(v);
              void persist({ stt_engine: v });
            }}
            className={`${settingsSelectClass} w-56`}
          >
            <option value="auto">{formatMsg(locale, 'settings.voiceEngineAuto')}</option>
            <option value="api">{formatMsg(locale, 'settings.voiceEngineApi')}</option>
            <option value="browser">{formatMsg(locale, 'settings.voiceEngineBrowser')}</option>
          </select>
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.voiceLanguage')}
          description={formatMsg(locale, 'settings.voiceLanguageDesc')}
        >
          <select
            value={language}
            onChange={(e) => {
              const v = e.target.value;
              setLanguage(v);
              void persist({ stt_language: v });
            }}
            className={`${settingsSelectClass} w-44`}
          >
            <option value="">{formatMsg(locale, 'settings.voiceLangAuto')}</option>
            <option value="zh">{formatMsg(locale, 'settings.voiceLangZh')}</option>
            <option value="en">{formatMsg(locale, 'settings.voiceLangEn')}</option>
            <option value="ja">{formatMsg(locale, 'settings.voiceLangJa')}</option>
          </select>
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.voiceAutoSend')}
          description={formatMsg(locale, 'settings.voiceAutoSendDesc')}
        >
          <Toggle
            checked={autoSend}
            onChange={(checked) => {
              setAutoSend(checked);
              void persist({ stt_auto_send: checked });
            }}
            label={formatMsg(locale, 'settings.voiceAutoSend')}
          />
        </SettingsRow>
      </SettingsGroupCard>

      {engine !== 'browser' && (
        <SettingsGroupCard>
          <div className="space-y-4 px-5 py-4">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div>
                <h4 className="text-ui-base font-semibold text-foreground">
                  {formatMsg(locale, 'settings.voiceApiConfigTitle')}
                </h4>
                <p className="mt-0.5 text-ui-xs text-foreground-subtlest">
                  {formatMsg(locale, 'settings.voiceApiConfigDesc')}
                </p>
              </div>
              <div className="flex flex-wrap gap-1.5">
                {STT_PRESETS.map((preset) => (
                  <button
                    key={preset.id}
                    type="button"
                    onClick={() => {
                      setBaseUrl(preset.baseUrl);
                      setModel(preset.model);
                    }}
                    className="rounded-md border border-border bg-surface px-2.5 py-1 text-ui-xs text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
                  >
                    {preset.label}
                  </button>
                ))}
              </div>
            </div>

            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <label className="block space-y-1.5">
                <span className="text-ui-xs text-foreground-subtle">
                  {formatMsg(locale, 'settings.voiceBaseUrl')}
                </span>
                <input
                  value={baseUrl}
                  onChange={(e) => setBaseUrl(e.target.value)}
                  placeholder="https://api.openai.com/v1"
                  className={settingsMonoInputClass}
                />
              </label>

              <label className="block space-y-1.5">
                <span className="text-ui-xs text-foreground-subtle">
                  {formatMsg(locale, 'settings.voiceModel')}
                </span>
                <input
                  value={model}
                  onChange={(e) => setModel(e.target.value)}
                  placeholder="gpt-4o-mini-transcribe"
                  className={settingsMonoInputClass}
                />
              </label>
            </div>

            <label className="block space-y-1.5">
              <span className="text-ui-xs text-foreground-subtle">
                {formatMsg(locale, 'settings.voiceApiKey')}
              </span>
              <div className="relative">
                <input
                  type={showKey ? 'text' : 'password'}
                  value={apiKey}
                  onChange={(e) => setApiKey(e.target.value)}
                  placeholder="sk-..."
                  className={`${settingsMonoInputClass} pr-9`}
                />
                <button
                  type="button"
                  onClick={() => setShowKey((v) => !v)}
                  className="absolute right-2 top-1/2 -translate-y-1/2 rounded p-1 text-foreground-subtlest hover:text-foreground"
                  aria-label={showKey ? 'Hide API key' : 'Show API key'}
                >
                  {showKey ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}
                </button>
              </div>
            </label>

            <div className="flex justify-end">
              <button
                type="button"
                disabled={saving}
                onClick={handleSaveApi}
                className="inline-flex items-center gap-1.5 rounded-lg bg-foreground px-3.5 py-1.5 text-ui-sm font-medium text-background transition-opacity hover:opacity-90 disabled:opacity-50"
              >
                <Check className="size-3.5" />
                {formatMsg(locale, 'common.save')}
              </button>
            </div>
          </div>
        </SettingsGroupCard>
      )}
    </div>
  );
}
