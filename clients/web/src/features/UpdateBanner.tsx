import { ArrowUpCircle, CheckCircle2, Download, ExternalLink, LoaderCircle, PackageOpen, RefreshCw, X } from 'lucide-react';
import { useState } from 'react';
import { apiFetch } from '../core/protocol';
import { useNexStore } from '../core/store';
import { hostBridge } from '../host/bridge';
import { formatMsg } from '../i18n/messages';

export function UpdateBanner() {
  const locale = useNexStore((s) => s.locale);
  const preferences = useNexStore((s) => s.preferences);
  const appUpdate = useNexStore((s) => s.appUpdate);
  const updateDownload = useNexStore((s) => s.updateDownload);
  const updateBannerDismissed = useNexStore((s) => s.updateBannerDismissed);
  const downloadAppUpdate = useNexStore((s) => s.downloadAppUpdate);
  const installAppUpdate = useNexStore((s) => s.installAppUpdate);
  const setAutoDownloadUpdates = useNexStore((s) => s.setAutoDownloadUpdates);
  const skipAppUpdateVersion = useNexStore((s) => s.skipAppUpdateVersion);
  const dismissUpdateBanner = useNexStore((s) => s.dismissUpdateBanner);
  const [notesExpanded, setNotesExpanded] = useState(false);
  const [applyingStaged, setApplyingStaged] = useState(false);
  const [stagedError, setStagedError] = useState('');

  if (!appUpdate || updateBannerDismissed) return null;
  const hasStaged = Boolean(appUpdate.staged_server_update);
  if (!hasStaged && !appUpdate.client_has_update && !appUpdate.server_has_update) return null;
  if (
    !hasStaged &&
    appUpdate.latest_version &&
    preferences.skipped_update_version &&
    preferences.skipped_update_version === appUpdate.latest_version
  ) {
    return null;
  }

  const handleApplyStaged = async () => {
    setApplyingStaged(true);
    setStagedError('');
    try {
      await apiFetch('/api/v1/updates/apply-staged', { method: 'POST' });
      window.setTimeout(() => {
        window.location.reload();
      }, 2500);
    } catch (err) {
      setStagedError(err instanceof Error ? err.message : String(err));
      setApplyingStaged(false);
    }
  };

  if (hasStaged) {
    const label = appUpdate.staged_build_sha
      ? `${appUpdate.staged_version || 'new'} (${appUpdate.staged_build_sha})`
      : appUpdate.staged_version || 'new';
    return (
      <div
        data-testid="staged-deploy-banner"
        className="fixed right-4 bottom-4 z-40 w-[340px] max-w-[calc(100vw-2rem)] rounded-2xl border border-border bg-popover p-4 text-foreground shadow-2xl"
      >
        <div className="flex items-start justify-between gap-2.5">
          <div className="flex min-w-0 items-center gap-2">
            <div className="flex size-8 shrink-0 items-center justify-center rounded-xl bg-brand-blue/12 text-brand-blue">
              <CheckCircle2 className="size-4" />
            </div>
            <div className="min-w-0">
              <div className="text-ui-sm font-semibold text-foreground">
                {formatMsg(locale, 'update.stagedTitle')}
              </div>
              <div className="mt-0.5 truncate text-ui-xs text-foreground-subtle">
                {formatMsg(locale, 'update.stagedDesc', { build: label })}
              </div>
            </div>
          </div>
          <button
            type="button"
            data-testid="update-banner-close-btn"
            aria-label={formatMsg(locale, 'common.close')}
            onClick={dismissUpdateBanner}
            className="flex size-6 shrink-0 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
          >
            <X className="size-3.5" />
          </button>
        </div>
        {stagedError && <p className="mt-2 text-ui-xs text-destructive">{stagedError}</p>}
        <div className="mt-3.5 flex items-center justify-end gap-2">
          <button
            type="button"
            onClick={dismissUpdateBanner}
            className="h-7 rounded-lg border border-border bg-card px-2.5 text-ui-xs text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
          >
            {formatMsg(locale, 'update.stagedLater')}
          </button>
          <button
            type="button"
            data-testid="apply-staged-deploy-btn"
            disabled={applyingStaged}
            onClick={() => void handleApplyStaged()}
            className="inline-flex h-7 items-center gap-1 rounded-lg bg-foreground px-2.5 text-ui-xs font-medium text-background transition-opacity hover:opacity-90 disabled:opacity-50"
          >
            {applyingStaged ? (
              <LoaderCircle className="size-3 animate-spin" />
            ) : (
              <RefreshCw className="size-3" />
            )}
            <span>{formatMsg(locale, applyingStaged ? 'update.stagedRestarting' : 'update.stagedApplyNow')}</span>
          </button>
        </div>
      </div>
    );
  }

  const isNative = hostBridge.kind !== 'browser';
  const isDownloading = updateDownload?.status === 'downloading';
  const isReady = updateDownload?.status === 'ready' && Boolean(updateDownload.local_path);
  const pct = Math.max(0, Math.min(100, Math.round(updateDownload?.percent || 0)));

  return (
    <div
      data-testid="app-update-banner"
      className="fixed right-4 bottom-4 z-40 w-[340px] max-w-[calc(100vw-2rem)] rounded-2xl border border-border bg-popover p-4 text-foreground shadow-2xl"
    >
      <div className="flex items-start justify-between gap-2.5">
        <div className="flex min-w-0 items-center gap-2">
          <div className="flex size-8 shrink-0 items-center justify-center rounded-xl bg-brand-blue/12 text-brand-blue">
            {isReady ? <CheckCircle2 className="size-4" /> : <ArrowUpCircle className="size-4" />}
          </div>
          <div className="min-w-0">
            <div className="text-ui-sm font-semibold text-foreground">
              {isReady
                ? formatMsg(locale, 'update.readyTitle', { version: appUpdate.latest_version })
                : formatMsg(locale, 'update.availableTitle', { version: appUpdate.latest_version })}
            </div>
            <div className="mt-0.5 truncate text-ui-xs text-foreground-subtle">
              {appUpdate.client_has_update && appUpdate.client_version
                ? formatMsg(locale, 'about.desktopUpdateAvailable', {
                    current: appUpdate.client_version,
                    latest: appUpdate.latest_version,
                  })
                : formatMsg(locale, 'about.serverUpdateAvailable', {
                    current: appUpdate.server_version,
                    latest: appUpdate.latest_version,
                  })}
            </div>
          </div>
        </div>
        <button
          type="button"
          data-testid="update-banner-close-btn"
          aria-label={formatMsg(locale, 'common.close')}
          onClick={dismissUpdateBanner}
          className="flex size-6 shrink-0 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
        >
          <X className="size-3.5" />
        </button>
      </div>

      {appUpdate.release_notes && (
        <div className="mt-2.5">
          <button
            type="button"
            onClick={() => setNotesExpanded((v) => !v)}
            className="text-ui-xs text-foreground-subtle underline-offset-2 hover:text-foreground hover:underline"
          >
            {formatMsg(locale, notesExpanded ? 'update.hideNotes' : 'update.showNotes')}
          </button>
          {notesExpanded && (
            <pre className="mt-1.5 max-h-32 overflow-y-auto whitespace-pre-wrap rounded-lg border border-border bg-surface p-2 font-mono text-[11px] leading-relaxed text-foreground-subtle">
              {appUpdate.release_notes}
            </pre>
          )}
        </div>
      )}

      {isDownloading && (
        <div data-testid="update-banner-progress" className="mt-3 space-y-1">
          <div className="flex items-center justify-between text-ui-xs text-foreground-subtle">
            <span className="inline-flex items-center gap-1.5">
              <LoaderCircle className="size-3 animate-spin text-brand-blue" />
              <span>{formatMsg(locale, 'update.downloading')}</span>
            </span>
            <span className="font-mono tabular-nums">{pct}%</span>
          </div>
          <div className="h-1.5 w-full overflow-hidden rounded-full bg-surface">
            <div className="h-full rounded-full bg-brand-blue transition-all" style={{ width: `${pct}%` }} />
          </div>
        </div>
      )}

      {updateDownload?.status === 'error' && updateDownload.error && (
        <p className="mt-2 text-ui-xs text-destructive">{updateDownload.error}</p>
      )}

      {isNative && (
        <label className="mt-3 flex cursor-pointer items-center gap-2 text-ui-xs text-foreground-subtle">
          <input
            type="checkbox"
            data-testid="update-banner-auto-download"
            checked={Boolean(preferences.auto_download_updates)}
            onChange={(e) => void setAutoDownloadUpdates(e.target.checked)}
          />
          <span>{formatMsg(locale, 'settings.autoDownloadUpdates')}</span>
        </label>
      )}

      <div className="mt-3.5 flex flex-wrap items-center justify-between gap-2">
        <button
          type="button"
          data-testid="update-banner-skip-btn"
          onClick={() => void skipAppUpdateVersion(appUpdate.latest_version)}
          className="text-ui-xs text-foreground-subtlest transition-colors hover:text-foreground"
        >
          {formatMsg(locale, 'update.skipVersion')}
        </button>

        <div className="flex items-center gap-1.5">
          {appUpdate.release_url && (
            <button
              type="button"
              onClick={() => void hostBridge.openExternal(appUpdate.release_url)}
              className="inline-flex h-7 items-center gap-1 rounded-lg border border-border bg-card px-2.5 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover"
            >
              <ExternalLink className="size-3" />
              <span>{formatMsg(locale, 'about.viewRelease')}</span>
            </button>
          )}
          {isReady ? (
            <button
              type="button"
              data-testid="update-banner-install-btn"
              onClick={() => void installAppUpdate()}
              className="inline-flex h-7 items-center gap-1 rounded-lg bg-foreground px-2.5 text-ui-xs font-medium text-background transition-opacity hover:opacity-90"
            >
              <PackageOpen className="size-3" />
              <span>{formatMsg(locale, 'update.installNow')}</span>
            </button>
          ) : (
            appUpdate.client_has_update &&
            appUpdate.client_download_url && (
              <button
                type="button"
                data-testid="update-banner-download-btn"
                disabled={isDownloading}
                onClick={() => void downloadAppUpdate()}
                className="inline-flex h-7 items-center gap-1 rounded-lg bg-primary px-2.5 text-ui-xs font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-50"
              >
                {isDownloading ? (
                  <LoaderCircle className="size-3 animate-spin" />
                ) : (
                  <Download className="size-3" />
                )}
                <span>{formatMsg(locale, isDownloading ? 'update.downloading' : 'update.downloadNow')}</span>
              </button>
            )
          )}
        </div>
      </div>
    </div>
  );
}
