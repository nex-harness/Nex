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

export function ImageGenerationSettingsCard({
  onFlash,
}: {
  onFlash: (msg: string) => void;
}) {
  const { locale, providers, preferences, refreshInitialData } = useNexStore();

  const [imageProviderId, setImageProviderId] = useState(preferences.image_provider_id ?? '');
  const [imageModelId, setImageModelId] = useState(preferences.image_model_id ?? '');
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setImageProviderId(preferences.image_provider_id ?? '');
    setImageModelId(preferences.image_model_id ?? '');
  }, [preferences.image_provider_id, preferences.image_model_id]);

  const enabledProviders = providers.filter((p) => p.enabled);

  const save = async () => {
    setSaving(true);
    try {
      const next: UserPreferences = {
        ...preferences,
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

        <SettingsRow
          label={formatMsg(locale, 'settings.imageModelId')}
          description={formatMsg(locale, 'settings.imageModelIdDesc')}
        >
          <input
            value={imageModelId}
            onChange={(e) => setImageModelId(e.target.value)}
            placeholder="google/gemini-2.5-flash-image"
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

  const [classifierProviderId, setClassifierProviderId] = useState(preferences.classifier_provider_id ?? '');
  const [classifierModelId, setClassifierModelId] = useState(preferences.classifier_model_id ?? '');
  const [classifierBaseUrl, setClassifierBaseUrl] = useState(preferences.classifier_base_url ?? '');
  const [jevAutoApprove, setJevAutoApprove] = useState(Boolean(preferences.jev_auto_approve_enabled));
  const [jevAutoRouter, setJevAutoRouter] = useState(Boolean(preferences.jev_auto_router_enabled));
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setClassifierProviderId(preferences.classifier_provider_id ?? '');
    setClassifierModelId(preferences.classifier_model_id ?? '');
    setClassifierBaseUrl(preferences.classifier_base_url ?? '');
    setJevAutoApprove(Boolean(preferences.jev_auto_approve_enabled));
    setJevAutoRouter(Boolean(preferences.jev_auto_router_enabled));
  }, [
    preferences.classifier_provider_id,
    preferences.classifier_model_id,
    preferences.classifier_base_url,
    preferences.jev_auto_approve_enabled,
    preferences.jev_auto_router_enabled,
  ]);

  const enabledProviders = providers.filter((p) => p.enabled);

  const save = async (overrides?: Partial<UserPreferences>) => {
    setSaving(true);
    try {
      const next: UserPreferences = {
        ...preferences,
        classifier_provider_id: classifierProviderId.trim(),
        classifier_model_id: classifierModelId.trim(),
        classifier_base_url: classifierBaseUrl.trim(),
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

        <SettingsRow
          label={formatMsg(locale, 'settings.classifierModelId')}
          description={formatMsg(locale, 'settings.classifierModelIdDesc')}
        >
          <input
            value={classifierModelId}
            onChange={(e) => setClassifierModelId(e.target.value)}
            placeholder="typesafe/jev-1.14"
            className={settingsMonoInputClass}
          />
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.classifierBaseUrl')}
          description={formatMsg(locale, 'settings.classifierBaseUrlDesc')}
        >
          <input
            value={classifierBaseUrl}
            onChange={(e) => setClassifierBaseUrl(e.target.value)}
            placeholder="http://127.0.0.1:9931/v1"
            className={settingsInputClass}
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
