import { AlertTriangle, CheckCircle2, Globe, LoaderCircle, RefreshCw, Trash2 } from 'lucide-react';
import { useEffect, useState } from 'react';
import { apiFetch } from '../core/protocol';
import { useNexStore } from '../core/store';
import { UserPreferences } from '../core/types';
import { formatMsg } from '../i18n/messages';
import {
  SectionHeading,
  SettingsGroupCard,
  SettingsRow,
  settingsInputClass,
  settingsMonoInputClass,
  settingsSelectClass,
  Toggle,
} from './SettingsPage';

const OFFICIAL_CFT_MIRROR = '';
const NPMMIRROR_CFT = 'https://cdn.npmmirror.com/binaries/chrome-for-testing';

export function SettingsBrowserSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const locale = useNexStore((s) => s.locale);
  const preferences = useNexStore((s) => s.preferences);
  const runners = useNexStore((s) => s.runners);
  const browserProbes = useNexStore((s) => s.browserProbes);
  const browserInstalling = useNexStore((s) => s.browserInstalling);
  const refreshInitialData = useNexStore((s) => s.refreshInitialData);
  const probeRunnerBrowser = useNexStore((s) => s.probeRunnerBrowser);
  const installRunnerBrowser = useNexStore((s) => s.installRunnerBrowser);
  const clearRunnerBrowserData = useNexStore((s) => s.clearRunnerBrowserData);

  const [prefs, setPrefs] = useState<UserPreferences>({
    ...preferences,
    browser_enabled: preferences.browser_enabled ?? true,
    browser_mode: preferences.browser_mode || 'managed_cdp',
    browser_headless: preferences.browser_headless ?? true,
    browser_profile_mode: preferences.browser_profile_mode || 'persistent',
    browser_attach_url: preferences.browser_attach_url || '',
    browser_executable_path: preferences.browser_executable_path || '',
    browser_download_mirror: preferences.browser_download_mirror || '',
  });
  const [mirrorPreset, setMirrorPreset] = useState<'official' | 'npmmirror' | 'custom'>(() => {
    const cur = (preferences.browser_download_mirror || '').trim();
    if (!cur) return 'official';
    if (cur === NPMMIRROR_CFT) return 'npmmirror';
    return 'custom';
  });

  useEffect(() => {
    setPrefs((prev) => ({
      ...prev,
      ...preferences,
      browser_enabled: preferences.browser_enabled ?? true,
      browser_mode: preferences.browser_mode || 'managed_cdp',
      browser_headless: preferences.browser_headless ?? true,
      browser_profile_mode: preferences.browser_profile_mode || 'persistent',
    }));
  }, [preferences]);

  const savePrefs = async (next: UserPreferences) => {
    setPrefs(next);
    await apiFetch('/api/v1/preferences', {
      method: 'PUT',
      body: JSON.stringify(next),
    });
    await refreshInitialData();
    onFlash(formatMsg(locale, 'settings.browser.saved'));
  };

  const onlineRunners = runners.filter((r) => r.status === 'online');

  const statusBadge = (status: string) => {
    switch (status) {
      case 'ready':
        return (
          <span className="inline-flex items-center gap-1 rounded-md bg-emerald-500/12 px-2 py-0.5 text-ui-xs font-medium text-emerald-700 dark:text-emerald-300">
            <CheckCircle2 className="size-3" />
            {formatMsg(locale, 'settings.browser.statusReady')}
          </span>
        );
      case 'missing_binary':
        return (
          <span className="inline-flex items-center gap-1 rounded-md bg-amber-500/12 px-2 py-0.5 text-ui-xs font-medium text-amber-700 dark:text-amber-300">
            <AlertTriangle className="size-3" />
            {formatMsg(locale, 'settings.browser.statusMissing')}
          </span>
        );
      case 'missing_deps':
        return (
          <span className="inline-flex items-center gap-1 rounded-md bg-amber-500/12 px-2 py-0.5 text-ui-xs font-medium text-amber-700 dark:text-amber-300">
            <AlertTriangle className="size-3" />
            {formatMsg(locale, 'settings.browser.statusMissingDeps')}
          </span>
        );
      case 'unsupported_arch':
        return (
          <span className="inline-flex items-center gap-1 rounded-md bg-amber-500/12 px-2 py-0.5 text-ui-xs font-medium text-amber-700 dark:text-amber-300">
            {formatMsg(locale, 'settings.browser.statusUnsupportedArch')}
          </span>
        );
      default:
        return (
          <span className="inline-flex items-center gap-1 rounded-md bg-destructive/12 px-2 py-0.5 text-ui-xs font-medium text-destructive">
            {formatMsg(locale, 'settings.browser.statusError')}
          </span>
        );
    }
  };

  return (
    <div data-testid="settings-browser-section" className="space-y-6">
      <SectionHeading
        title={formatMsg(locale, 'settings.browser.title')}
        description={formatMsg(locale, 'settings.browser.desc')}
      />

      <SettingsGroupCard>
        <SettingsRow
          label={formatMsg(locale, 'settings.browser.enabled')}
          description={formatMsg(locale, 'settings.browser.enabledDesc')}
        >
          <div data-testid="browser-enabled-toggle">
            <Toggle
              checked={prefs.browser_enabled ?? true}
              label={formatMsg(locale, 'settings.browser.enabled')}
              onChange={(checked) => void savePrefs({ ...prefs, browser_enabled: checked })}
            />
          </div>
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.browser.mode')}
          description={formatMsg(locale, 'settings.browser.modeDesc')}
        >
          <select
            data-testid="browser-mode-select"
            value={prefs.browser_mode || 'managed_cdp'}
            onChange={(e) => void savePrefs({ ...prefs, browser_mode: e.target.value })}
            className={settingsSelectClass}
          >
            <option value="managed_cdp">{formatMsg(locale, 'settings.browser.modeManaged')}</option>
            <option value="attach_cdp">{formatMsg(locale, 'settings.browser.modeAttach')}</option>
          </select>
        </SettingsRow>

        {(prefs.browser_mode || 'managed_cdp') === 'managed_cdp' ? (
          <>
            <SettingsRow
              label={formatMsg(locale, 'settings.browser.headless')}
              description={formatMsg(locale, 'settings.browser.headlessDesc')}
            >
              <Toggle
                checked={prefs.browser_headless ?? true}
                label={formatMsg(locale, 'settings.browser.headless')}
                onChange={(checked) => void savePrefs({ ...prefs, browser_headless: checked })}
              />
            </SettingsRow>

            <SettingsRow label={formatMsg(locale, 'settings.browser.executablePath')}>
              <input
                value={prefs.browser_executable_path || ''}
                onChange={(e) => setPrefs({ ...prefs, browser_executable_path: e.target.value })}
                onBlur={() => void savePrefs(prefs)}
                placeholder={formatMsg(locale, 'settings.browser.executablePlaceholder')}
                className={settingsMonoInputClass}
              />
            </SettingsRow>
          </>
        ) : (
          <SettingsRow label={formatMsg(locale, 'settings.browser.attachUrl')}>
            <input
              data-testid="browser-attach-url-input"
              value={prefs.browser_attach_url || ''}
              onChange={(e) => setPrefs({ ...prefs, browser_attach_url: e.target.value })}
              onBlur={() => void savePrefs(prefs)}
              placeholder={formatMsg(locale, 'settings.browser.attachUrlPlaceholder')}
              className={settingsMonoInputClass}
            />
          </SettingsRow>
        )}

        <SettingsRow label={formatMsg(locale, 'settings.browser.downloadMirror')}>
          <div className="flex w-full flex-col gap-2">
            <select
              value={mirrorPreset}
              onChange={(e) => {
                const nextPreset = e.target.value as 'official' | 'npmmirror' | 'custom';
                setMirrorPreset(nextPreset);
                const nextMirror =
                  nextPreset === 'official'
                    ? OFFICIAL_CFT_MIRROR
                    : nextPreset === 'npmmirror'
                      ? NPMMIRROR_CFT
                      : prefs.browser_download_mirror || '';
                void savePrefs({ ...prefs, browser_download_mirror: nextMirror });
              }}
              className={settingsSelectClass}
            >
              <option value="official">{formatMsg(locale, 'settings.browser.mirrorOfficial')}</option>
              <option value="npmmirror">{formatMsg(locale, 'settings.browser.mirrorNpmmirror')}</option>
              <option value="custom">{formatMsg(locale, 'settings.browser.mirrorCustom')}</option>
            </select>
            {mirrorPreset === 'custom' && (
              <input
                value={prefs.browser_download_mirror || ''}
                onChange={(e) => setPrefs({ ...prefs, browser_download_mirror: e.target.value })}
                onBlur={() => void savePrefs(prefs)}
                placeholder="https://mirror.example.com/chrome-for-testing"
                className={settingsInputClass}
              />
            )}
          </div>
        </SettingsRow>

        <SettingsRow label={formatMsg(locale, 'settings.browser.profileMode')}>
          <select
            value={prefs.browser_profile_mode || 'persistent'}
            onChange={(e) => void savePrefs({ ...prefs, browser_profile_mode: e.target.value })}
            className={settingsSelectClass}
          >
            <option value="persistent">{formatMsg(locale, 'settings.browser.profilePersistent')}</option>
            <option value="ephemeral">{formatMsg(locale, 'settings.browser.profileEphemeral')}</option>
          </select>
        </SettingsRow>
      </SettingsGroupCard>

      <SectionHeading
        title={formatMsg(locale, 'settings.browser.runnersHeading')}
        description={formatMsg(locale, 'settings.browser.runnersDesc')}
      />

      <SettingsGroupCard>
        {onlineRunners.length === 0 ? (
          <div className="px-5 py-8 text-center text-ui-sm text-foreground-subtlest">
            {formatMsg(locale, 'settings.browser.noRunners')}
          </div>
        ) : (
          onlineRunners.map((r) => {
            const probe = browserProbes[r.id];
            const installing = Boolean(browserInstalling[r.id]);
            return (
              <div key={r.id} className="space-y-2 px-5 py-4">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="flex min-w-0 items-center gap-2">
                    <Globe className="size-4 shrink-0 text-foreground-subtle" />
                    <span className="truncate text-ui-base font-medium text-foreground">{r.name || r.id}</span>
                    <span className="rounded bg-surface px-1.5 py-0.5 font-mono text-ui-xs text-foreground-subtlest">
                      {r.os}/{r.arch}
                    </span>
                    {probe ? statusBadge(probe.status) : null}
                  </div>
                  <div className="flex flex-wrap items-center gap-1.5">
                    <button
                      type="button"
                      data-testid={`browser-runner-probe-${r.id}`}
                      onClick={() => void probeRunnerBrowser(r.id)}
                      className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border bg-card px-3 text-ui-xs font-medium text-foreground hover:bg-hover"
                    >
                      <RefreshCw className="size-3.5" />
                      <span>{formatMsg(locale, 'settings.browser.probe')}</span>
                    </button>
                    {(!probe || probe.status === 'missing_binary') && (
                      <button
                        type="button"
                        data-testid={`browser-runner-install-${r.id}`}
                        disabled={installing}
                        onClick={() =>
                          void installRunnerBrowser(r.id, prefs.browser_download_mirror).then((res) => {
                            if (res?.status === 'ready') {
                              onFlash(
                                formatMsg(locale, 'settings.browser.installSuccess', {
                                  runner: r.name || r.id,
                                  version: res.version || 'Chromium',
                                }),
                              );
                            }
                          })
                        }
                        className="inline-flex h-8 items-center gap-1.5 rounded-lg bg-primary px-3 text-ui-xs font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-50"
                      >
                        {installing && <LoaderCircle className="size-3.5 animate-spin" />}
                        <span>
                          {formatMsg(
                            locale,
                            installing ? 'settings.browser.installing' : 'settings.browser.install',
                          )}
                        </span>
                      </button>
                    )}
                    <button
                      type="button"
                      onClick={() =>
                        void clearRunnerBrowserData(r.id, 'cache').then((ok) => {
                          if (ok) {
                            onFlash(
                              formatMsg(locale, 'settings.browser.dataCleared', {
                                runner: r.name || r.id,
                                mode: 'cache',
                              }),
                            );
                          }
                        })
                      }
                      className="inline-flex h-8 items-center gap-1 rounded-lg border border-border bg-card px-2.5 text-ui-xs text-foreground-subtle hover:bg-hover hover:text-foreground"
                    >
                      <Trash2 className="size-3" />
                      <span>{formatMsg(locale, 'settings.browser.clearCache')}</span>
                    </button>
                    <button
                      type="button"
                      onClick={() =>
                        void clearRunnerBrowserData(r.id, 'all').then((ok) => {
                          if (ok) {
                            onFlash(
                              formatMsg(locale, 'settings.browser.dataCleared', {
                                runner: r.name || r.id,
                                mode: 'all',
                              }),
                            );
                          }
                        })
                      }
                      className="inline-flex h-8 items-center gap-1 rounded-lg border border-border bg-card px-2.5 text-ui-xs text-destructive hover:bg-destructive/10"
                    >
                      <Trash2 className="size-3" />
                      <span>{formatMsg(locale, 'settings.browser.resetData')}</span>
                    </button>
                  </div>
                </div>
                {probe?.version || probe?.executable_path ? (
                  <p className="truncate font-mono text-ui-xs text-foreground-subtlest">
                    {probe.version} {probe.executable_path ? `(${probe.executable_path})` : ''}
                  </p>
                ) : null}
                {probe?.suggested_command && probe.status !== 'ready' ? (
                  <pre className="overflow-x-auto rounded-lg border border-border bg-surface px-3 py-2 font-mono text-ui-xs text-foreground-subtle">
                    {probe.suggested_command}
                  </pre>
                ) : null}
                {probe?.error && probe.status !== 'ready' ? (
                  <p className="text-ui-xs text-destructive">{probe.error}</p>
                ) : null}
              </div>
            );
          })
        )}
      </SettingsGroupCard>
    </div>
  );
}
