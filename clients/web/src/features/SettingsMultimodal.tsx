import { Eye, EyeOff } from 'lucide-react';
import { useEffect, useState } from 'react';
import { apiFetch } from '../core/protocol';
import { useNexStore } from '../core/store';
import { UserPreferences } from '../core/types';
import { formatMsg } from '../i18n/messages';
import {
  SettingsGroupCard,
  SettingsRow,
  Toggle,
  settingsInputClass,
  settingsMonoInputClass,
  settingsSelectClass,
} from './SettingsPage';

const IMAGE_DRIVER_PLACEHOLDERS: Record<string, { url: string; model: string }> = {
  openai_images: { url: 'https://api.openai.com/v1', model: 'gpt-image-1' },
  openrouter_images: { url: 'https://openrouter.ai/api/v1', model: 'google/gemini-2.5-flash-image' },
  xai_images: { url: 'https://api.x.ai/v1', model: 'grok-2-image' },
  minimax_images: { url: 'https://api.minimax.io/v1', model: 'image-01' },
};

export function ImageGenerationSettingsCard({
  onFlash,
}: {
  onFlash: (msg: string) => void;
}) {
  const { locale, providers, preferences, refreshInitialData } = useNexStore();

  const initialMode =
    preferences.image_mode === 'standalone' || (!preferences.image_mode && Boolean(preferences.image_base_url))
      ? 'standalone'
      : 'provider';
  const [mode, setMode] = useState<'provider' | 'standalone'>(initialMode);
  const [driver, setDriver] = useState(preferences.image_driver || 'openrouter_images');
  const [baseUrl, setBaseUrl] = useState(preferences.image_base_url ?? '');
  const [apiKey, setApiKey] = useState(preferences.image_api_key ?? '');
  const [showKey, setShowKey] = useState(false);
  const [imageProviderId, setImageProviderId] = useState(preferences.image_provider_id ?? '');
  const [imageModelId, setImageModelId] = useState(preferences.image_model_id ?? '');
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setMode(
      preferences.image_mode === 'standalone' || (!preferences.image_mode && Boolean(preferences.image_base_url))
        ? 'standalone'
        : 'provider',
    );
    setDriver(preferences.image_driver || 'openrouter_images');
    setBaseUrl(preferences.image_base_url ?? '');
    setApiKey(preferences.image_api_key ?? '');
    setImageProviderId(preferences.image_provider_id ?? '');
    setImageModelId(preferences.image_model_id ?? '');
  }, [
    preferences.image_mode,
    preferences.image_driver,
    preferences.image_base_url,
    preferences.image_api_key,
    preferences.image_provider_id,
    preferences.image_model_id,
  ]);

  const enabledProviders = providers.filter((p) => p.enabled);
  const ph = IMAGE_DRIVER_PLACEHOLDERS[driver] || IMAGE_DRIVER_PLACEHOLDERS.openai_images;

  const save = async () => {
    setSaving(true);
    try {
      const next: UserPreferences = {
        ...preferences,
        image_mode: mode,
        image_driver: driver,
        image_base_url: baseUrl.trim(),
        image_api_key: apiKey.trim(),
        image_provider_id: imageProviderId.trim(),
        image_model_id: imageModelId.trim(),
      };
      await apiFetch<UserPreferences>('/api/v1/preferences', {
        method: 'PUT',
        body: JSON.stringify(next),
      });
      await refreshInitialData();
      onFlash(formatMsg(locale, 'settings.imageSaved'));
    } catch (err) {
      onFlash(formatMsg(locale, 'settings.multimodalSaveFailed', { error: String(err) }));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="mt-5 space-y-3">
      <div>
        <h3 className="text-ui-lg font-semibold text-foreground">
          {formatMsg(locale, 'settings.imageSectionTitle')}
        </h3>
        <p className="mt-0.5 text-ui-sm text-foreground-subtle">
          {formatMsg(locale, 'settings.imageSectionDesc')}
        </p>
      </div>

      <SettingsGroupCard>
        <SettingsRow
          label={formatMsg(locale, 'settings.configMode')}
          description={formatMsg(locale, 'settings.configModeDesc')}
        >
          <div className="inline-flex rounded-lg border border-border bg-surface p-0.5">
            {(
              [
                { id: 'provider', labelKey: 'settings.configModeProvider' },
                { id: 'standalone', labelKey: 'settings.configModeStandalone' },
              ] as const
            ).map((opt) => (
              <button
                key={opt.id}
                type="button"
                onClick={() => setMode(opt.id)}
                className={`h-7 rounded-md px-3 text-ui-xs font-medium transition-colors ${
                  mode === opt.id
                    ? 'bg-card text-foreground shadow-xs'
                    : 'text-foreground-subtle hover:text-foreground'
                }`}
              >
                {formatMsg(locale, opt.labelKey)}
              </button>
            ))}
          </div>
        </SettingsRow>

        {mode === 'provider' ? (
          <SettingsRow
            label={formatMsg(locale, 'settings.imageProvider')}
            description={formatMsg(locale, 'settings.imageProviderDesc')}
          >
            <select
              value={imageProviderId}
              onChange={(e) => setImageProviderId(e.target.value)}
              className={settingsSelectClass}
            >
              <option value="">{formatMsg(locale, 'settings.imageProviderAuto')}</option>
              {enabledProviders.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name || p.id}
                </option>
              ))}
            </select>
          </SettingsRow>
        ) : (
          <>
            <SettingsRow
              label={formatMsg(locale, 'settings.imageDriver')}
              description={formatMsg(locale, 'settings.imageDriverDesc')}
            >
              <select
                value={driver}
                onChange={(e) => setDriver(e.target.value)}
                className={settingsSelectClass}
              >
                <option value="openrouter_images">{formatMsg(locale, 'settings.imageDriverOpenRouter')}</option>
                <option value="openai_images">{formatMsg(locale, 'settings.imageDriverOpenAI')}</option>
                <option value="xai_images">{formatMsg(locale, 'settings.imageDriverXAI')}</option>
                <option value="minimax_images">{formatMsg(locale, 'settings.imageDriverMiniMax')}</option>
              </select>
            </SettingsRow>

            <SettingsRow
              label={formatMsg(locale, 'settings.apiEndpoint')}
              description={formatMsg(locale, 'settings.apiEndpointImageDesc')}
            >
              <input
                value={baseUrl}
                onChange={(e) => setBaseUrl(e.target.value)}
                placeholder={ph.url}
                className={settingsInputClass}
              />
            </SettingsRow>

            <SettingsRow
              label={formatMsg(locale, 'settings.apiKey')}
              description={formatMsg(locale, 'settings.apiKeyImageDesc')}
            >
              <div className="relative flex items-center">
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
                  className="absolute right-2 flex size-6 items-center justify-center rounded text-foreground-subtlest hover:text-foreground"
                >
                  {showKey ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}
                </button>
              </div>
            </SettingsRow>
          </>
        )}

        <SettingsRow
          label={formatMsg(locale, 'settings.imageModelId')}
          description={formatMsg(locale, 'settings.imageModelIdDesc')}
        >
          <input
            value={imageModelId}
            onChange={(e) => setImageModelId(e.target.value)}
            placeholder={ph.model}
            className={settingsMonoInputClass}
          />
        </SettingsRow>

        <div className="flex items-center justify-end gap-2 px-5 py-4">
          <button
            type="button"
            disabled={saving}
            onClick={() => void save()}
            className="h-8 rounded-lg bg-primary px-4 text-ui-sm font-medium text-primary-foreground hover:opacity-90 disabled:opacity-50"
          >
            {saving ? formatMsg(locale, 'settings.saving') : formatMsg(locale, 'settings.saveChanges')}
          </button>
        </div>
      </SettingsGroupCard>
    </div>
  );
}

export function ClassifierSettingsCard({
  onFlash,
}: {
  onFlash: (msg: string) => void;
}) {
  const { locale, providers, preferences, refreshInitialData } = useNexStore();

  const initialMode =
    preferences.classifier_mode === 'standalone' ||
    (!preferences.classifier_mode && Boolean(preferences.classifier_base_url))
      ? 'standalone'
      : 'provider';
  const [mode, setMode] = useState<'provider' | 'standalone'>(initialMode);
  const [driver, setDriver] = useState(preferences.classifier_driver || 'typesafe_system_one');
  const [classifierProviderId, setClassifierProviderId] = useState(preferences.classifier_provider_id ?? '');
  const [classifierModelId, setClassifierModelId] = useState(preferences.classifier_model_id ?? '');
  const [classifierBaseUrl, setClassifierBaseUrl] = useState(preferences.classifier_base_url ?? '');
  const [classifierApiKey, setClassifierApiKey] = useState(preferences.classifier_api_key ?? '');
  const [showKey, setShowKey] = useState(false);
  const [jevAutoApprove, setJevAutoApprove] = useState(Boolean(preferences.jev_auto_approve_enabled));
  const [jevAutoRouter, setJevAutoRouter] = useState(Boolean(preferences.jev_auto_router_enabled));
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setMode(
      preferences.classifier_mode === 'standalone' ||
        (!preferences.classifier_mode && Boolean(preferences.classifier_base_url))
        ? 'standalone'
        : 'provider',
    );
    setDriver(preferences.classifier_driver || 'typesafe_system_one');
    setClassifierProviderId(preferences.classifier_provider_id ?? '');
    setClassifierModelId(preferences.classifier_model_id ?? '');
    setClassifierBaseUrl(preferences.classifier_base_url ?? '');
    setClassifierApiKey(preferences.classifier_api_key ?? '');
    setJevAutoApprove(Boolean(preferences.jev_auto_approve_enabled));
    setJevAutoRouter(Boolean(preferences.jev_auto_router_enabled));
  }, [
    preferences.classifier_mode,
    preferences.classifier_driver,
    preferences.classifier_provider_id,
    preferences.classifier_model_id,
    preferences.classifier_base_url,
    preferences.classifier_api_key,
    preferences.jev_auto_approve_enabled,
    preferences.jev_auto_router_enabled,
  ]);

  const enabledProviders = providers.filter((p) => p.enabled);

  const save = async (overrides?: Partial<UserPreferences>) => {
    setSaving(true);
    try {
      const next: UserPreferences = {
        ...preferences,
        classifier_mode: mode,
        classifier_driver: driver,
        classifier_provider_id: classifierProviderId.trim(),
        classifier_model_id: classifierModelId.trim(),
        classifier_base_url: classifierBaseUrl.trim(),
        classifier_api_key: classifierApiKey.trim(),
        jev_auto_approve_enabled: jevAutoApprove,
        jev_auto_router_enabled: jevAutoRouter,
        ...overrides,
      };
      await apiFetch<UserPreferences>('/api/v1/preferences', {
        method: 'PUT',
        body: JSON.stringify(next),
      });
      await refreshInitialData();
      onFlash(formatMsg(locale, 'settings.classifierSaved'));
    } catch (err) {
      onFlash(formatMsg(locale, 'settings.multimodalSaveFailed', { error: String(err) }));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="mt-5 space-y-3">
      <div>
        <h3 className="text-ui-lg font-semibold text-foreground">
          {formatMsg(locale, 'settings.classifierSectionTitle')}
        </h3>
        <p className="mt-0.5 text-ui-sm text-foreground-subtle">
          {formatMsg(locale, 'settings.classifierSectionDesc')}
        </p>
      </div>

      <SettingsGroupCard>
        <SettingsRow
          label={formatMsg(locale, 'settings.configMode')}
          description={formatMsg(locale, 'settings.configModeDesc')}
        >
          <div className="inline-flex rounded-lg border border-border bg-surface p-0.5">
            {(
              [
                { id: 'provider', labelKey: 'settings.configModeProvider' },
                { id: 'standalone', labelKey: 'settings.configModeStandalone' },
              ] as const
            ).map((opt) => (
              <button
                key={opt.id}
                type="button"
                onClick={() => setMode(opt.id)}
                className={`h-7 rounded-md px-3 text-ui-xs font-medium transition-colors ${
                  mode === opt.id
                    ? 'bg-card text-foreground shadow-xs'
                    : 'text-foreground-subtle hover:text-foreground'
                }`}
              >
                {formatMsg(locale, opt.labelKey)}
              </button>
            ))}
          </div>
        </SettingsRow>

        {mode === 'provider' ? (
          <SettingsRow
            label={formatMsg(locale, 'settings.classifierProvider')}
            description={formatMsg(locale, 'settings.classifierProviderDesc')}
          >
            <select
              value={classifierProviderId}
              onChange={(e) => setClassifierProviderId(e.target.value)}
              className={settingsSelectClass}
            >
              <option value="">{formatMsg(locale, 'settings.classifierProviderAuto')}</option>
              {enabledProviders.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name || p.id}
                </option>
              ))}
            </select>
          </SettingsRow>
        ) : (
          <>
            <SettingsRow
              label={formatMsg(locale, 'settings.classifierDriver')}
              description={formatMsg(locale, 'settings.classifierDriverDesc')}
            >
              <select
                value={driver}
                onChange={(e) => setDriver(e.target.value)}
                className={settingsSelectClass}
              >
                <option value="typesafe_system_one">
                  {formatMsg(locale, 'settings.classifierDriverTypeSafe')}
                </option>
                <option value="openrouter_system_one">
                  {formatMsg(locale, 'settings.classifierDriverOpenRouter')}
                </option>
              </select>
            </SettingsRow>

            <SettingsRow
              label={formatMsg(locale, 'settings.apiEndpoint')}
              description={formatMsg(locale, 'settings.apiEndpointClassifierDesc')}
            >
              <input
                value={classifierBaseUrl}
                onChange={(e) => setClassifierBaseUrl(e.target.value)}
                placeholder={
                  driver === 'openrouter_system_one'
                    ? 'https://openrouter.ai/api/v1'
                    : 'https://api.typesafe.ai/v1'
                }
                className={settingsInputClass}
              />
            </SettingsRow>

            <SettingsRow
              label={formatMsg(locale, 'settings.apiKey')}
              description={formatMsg(locale, 'settings.apiKeyClassifierDesc')}
            >
              <div className="relative flex items-center">
                <input
                  type={showKey ? 'text' : 'password'}
                  value={classifierApiKey}
                  onChange={(e) => setClassifierApiKey(e.target.value)}
                  placeholder="ts-..."
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
            </SettingsRow>
          </>
        )}

        <SettingsRow
          label={formatMsg(locale, 'settings.classifierModelId')}
          description={formatMsg(locale, 'settings.classifierModelIdDesc')}
        >
          <input
            value={classifierModelId}
            onChange={(e) => setClassifierModelId(e.target.value)}
            placeholder={driver === 'openrouter_system_one' ? 'typesafe/jev-1.14' : 'jev-latest'}
            className={settingsMonoInputClass}
          />
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.jevAutoApprove')}
          description={formatMsg(locale, 'settings.jevAutoApproveDesc')}
        >
          <Toggle
            checked={jevAutoApprove}
            onChange={(checked) => {
              setJevAutoApprove(checked);
              void save({ jev_auto_approve_enabled: checked });
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
              void save({ jev_auto_router_enabled: checked });
            }}
            label={formatMsg(locale, 'settings.jevAutoRouter')}
          />
        </SettingsRow>

        <div className="flex items-center justify-end gap-2 px-5 py-4">
          <button
            type="button"
            disabled={saving}
            onClick={() => void save()}
            className="h-8 rounded-lg bg-primary px-4 text-ui-sm font-medium text-primary-foreground hover:opacity-90 disabled:opacity-50"
          >
            {saving ? formatMsg(locale, 'settings.saving') : formatMsg(locale, 'settings.saveChanges')}
          </button>
        </div>
      </SettingsGroupCard>
    </div>
  );
}
