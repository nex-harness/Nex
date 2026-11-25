import { ArrowUpCircle, Check, Copy, Download, ExternalLink, Keyboard, RefreshCw, X } from 'lucide-react';
import { useCallback, useEffect, useState } from 'react';
import { apiFetch } from '../core/protocol';
import { useNexStore } from '../core/store';
import { hostBridge } from '../host/bridge';
import { formatMsg } from '../i18n/messages';

interface HealthBuildInfo {
  version?: string;
  build_sha?: string;
  is_dev?: boolean;
}

interface AppUpdateCheckResult {
  checked_at: number;
  release_url: string;
  latest_version: string;
  published_at?: string;
  release_notes?: string;
  server_version: string;
  server_is_dev: boolean;
  server_has_update: boolean;
  client_version?: string;
  client_platform?: string;
  client_is_dev?: boolean;
  client_has_update?: boolean;
  client_download_url?: string;
  client_asset_name?: string;
  cached?: boolean;
  error?: string;
}

/** Nex squircle brand mark aligned with clients/app/resources/launch-logo.png. */
export function NexLogo({ className = 'size-12' }: { className?: string }) {
  return (
    <div
      aria-hidden="true"
      className={`flex shrink-0 items-center justify-center rounded-[14px] bg-foreground text-background shadow-sm ${className}`}
    >
      <svg viewBox="0 0 64 64" fill="none" className="size-[62%]">
        <path
          d="M19 47V17L45 47V17"
          stroke="currentColor"
          strokeWidth="7.5"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
    </div>
  );
}

export function AboutModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const locale = useNexStore((s) => s.locale);
  const [health, setHealth] = useState<HealthBuildInfo | null>(null);
  const [hostVersion, setHostVersion] = useState<string>('');
  const [hostPlatform, setHostPlatform] = useState<string>('');
  const [checkingUpdate, setCheckingUpdate] = useState(false);
  const [updateInfo, setUpdateInfo] = useState<AppUpdateCheckResult | null>(null);
  const [updateErr, setUpdateErr] = useState<string>('');
  const [copiedSha, setCopiedSha] = useState(false);

  const runUpdateCheck = useCallback(
    async (force: boolean, clientVer?: string, clientPlat?: string) => {
      setCheckingUpdate(true);
      setUpdateErr('');
      try {
        const params = new URLSearchParams();
        if (force) params.set('force', '1');
        const cv = (clientVer ?? hostVersion).trim();
        const cp = (clientPlat ?? hostPlatform).trim();
        if (cv) params.set('client_version', cv);
        if (cp) params.set('client_platform', cp);
        const qs = params.toString();
        const res = await apiFetch<AppUpdateCheckResult>(`/api/v1/updates/check${qs ? `?${qs}` : ''}`);
        if (res && typeof res === 'object') {
          setUpdateInfo(res);
          if (res.error) setUpdateErr(res.error);
        }
      } catch (err) {
        setUpdateErr(err instanceof Error ? err.message : String(err));
      } finally {
        setCheckingUpdate(false);
      }
    },
    [hostPlatform, hostVersion],
  );

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    apiFetch<HealthBuildInfo>('/api/v1/health')
      .then((res) => {
        if (!cancelled && res && typeof res === 'object') {
          setHealth(res);
        }
      })
      .catch(() => {
        if (!cancelled) setHealth(null);
      });

    if (hostBridge.kind !== 'browser') {
      hostBridge
        .getHostInfo()
        .then((info) => {
          if (!cancelled && info) {
            setHostVersion((info.version || '').trim());
            setHostPlatform((info.platform || hostBridge.platform || '').trim());
          }
        })
        .catch(() => {});
    }

    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        onClose();
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => {
      cancelled = true;
      window.removeEventListener('keydown', onKeyDown);
    };
  }, [open, onClose]);

  if (!open) return null;

  const fallbackVersion = typeof __NEX_APP_VERSION__ !== 'undefined' ? __NEX_APP_VERSION__ : 'dev';
  const fallbackSha = typeof __NEX_BUILD_SHA__ !== 'undefined' ? __NEX_BUILD_SHA__ : '';
  const fallbackIsDev = typeof __NEX_IS_DEV__ !== 'undefined' ? __NEX_IS_DEV__ : false;

  const displayVersion = (health?.version || '').trim() || fallbackVersion;
  const displaySha = (health?.build_sha || '').trim() || fallbackSha;
  const lowerVer = displayVersion.toLowerCase();
  const isDebug =
    Boolean(health?.is_dev ?? fallbackIsDev) ||
    lowerVer === 'dev' ||
    lowerVer.includes('-dev') ||
    lowerVer.includes('-dirty') ||
    lowerVer.includes('debug');

  const shortcuts: Array<{ label: string; keys: string }> = [
    { label: formatMsg(locale, 'chat.sendPrompt'), keys: 'Enter' },
    { label: formatMsg(locale, 'chat.newLine'), keys: 'Shift+Enter' },
    { label: formatMsg(locale, 'chat.cycleReasoningEffort'), keys: '^ T' },
    { label: formatMsg(locale, 'chat.referenceFile'), keys: '@' },
    { label: formatMsg(locale, 'about.slashCommands'), keys: '/' },
    { label: formatMsg(locale, 'chat.steerRunningAgent'), keys: 'Enter' },
    { label: formatMsg(locale, 'about.commandPalette'), keys: '⌘ K' },
  ];

  const handleCopySha = () => {
    if (!displaySha) return;
    void navigator.clipboard?.writeText(displaySha);
    setCopiedSha(true);
    window.setTimeout(() => setCopiedSha(false), 1400);
  };

  return (
    <div
      data-testid="about-modal-backdrop"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/45 px-4 backdrop-blur-[2px]"
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={formatMsg(locale, 'about.title')}
        data-testid="about-modal"
        className="w-full max-w-[420px] overflow-hidden rounded-2xl border border-border bg-popover text-foreground shadow-2xl"
      >
        <div className="flex items-start justify-between gap-3 border-b border-border px-5 py-4">
          <div className="flex items-center gap-3.5">
            <NexLogo className="size-11" />
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <h2 data-testid="about-app-name" className="text-ui-lg font-semibold tracking-tight text-foreground">
                  {formatMsg(locale, 'about.appName')}
                </h2>
                {isDebug && (
                  <span
                    data-testid="about-debug-badge"
                    className="rounded-md border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 font-mono text-[11px] font-medium text-amber-600 dark:text-amber-400"
                  >
                    {formatMsg(locale, 'about.debugBadge')}
                  </span>
                )}
              </div>
              <p className="mt-0.5 text-ui-xs text-foreground-subtle">
                {formatMsg(locale, 'about.subtitle')}
              </p>
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label={formatMsg(locale, 'common.close')}
            className="flex size-7 shrink-0 items-center justify-center rounded-lg text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
          >
            <X className="size-4" />
          </button>
        </div>

        <div className="space-y-4 px-5 py-4">
          <div className="divide-y divide-border rounded-xl border border-border bg-surface/60">
            <div className="flex items-center justify-between gap-3 px-3.5 py-2.5 text-ui-sm">
              <span className="text-foreground-subtle">{formatMsg(locale, 'about.version')}</span>
              <span data-testid="about-version" className="font-mono font-medium text-foreground">
                {displayVersion}
              </span>
            </div>
            {hostVersion && (
              <div className="flex items-center justify-between gap-3 px-3.5 py-2.5 text-ui-sm">
                <span className="text-foreground-subtle">{formatMsg(locale, 'about.desktopVersion')}</span>
                <span data-testid="about-desktop-version" className="font-mono font-medium text-foreground">
                  {hostVersion}
                </span>
              </div>
            )}
            {isDebug && displaySha && (
              <div className="flex items-center justify-between gap-3 px-3.5 py-2.5 text-ui-sm">
                <span className="text-foreground-subtle">{formatMsg(locale, 'about.buildHash')}</span>
                <div className="flex items-center gap-1.5">
                  <code data-testid="about-build-sha" className="rounded bg-tag px-1.5 py-0.5 font-mono text-ui-xs text-foreground">
                    {displaySha}
                  </code>
                  <button
                    type="button"
                    onClick={handleCopySha}
                    title={copiedSha ? formatMsg(locale, 'about.copiedHash') : formatMsg(locale, 'about.copyHash')}
                    className="flex size-6 items-center justify-center rounded-md text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
                  >
                    {copiedSha ? <Check className="size-3.5 text-success" /> : <Copy className="size-3.5" />}
                  </button>
                </div>
              </div>
            )}
            <div className="flex flex-col gap-2 px-3.5 py-2.5">
              <div className="flex items-center justify-between gap-2">
                <div className="min-w-0 text-ui-xs text-foreground-subtle">
                  {updateErr ? (
                    <span data-testid="about-update-error" className="text-destructive">
                      {formatMsg(locale, 'about.checkFailed', { error: updateErr })}
                    </span>
                  ) : updateInfo?.latest_version ? (
                    updateInfo.server_has_update || updateInfo.client_has_update ? (
                      <span
                        data-testid="about-update-available"
                        className="inline-flex items-center gap-1 font-medium text-primary"
                      >
                        <ArrowUpCircle className="size-3.5 shrink-0" />
                        <span>{formatMsg(locale, 'about.updateAvailable', { version: updateInfo.latest_version })}</span>
                      </span>
                    ) : (
                      <span data-testid="about-update-uptodate" className="text-emerald-600 dark:text-emerald-400">
                        {formatMsg(locale, 'about.upToDate', { version: updateInfo.latest_version })}
                      </span>
                    )
                  ) : null}
                </div>
                <button
                  type="button"
                  data-testid="about-check-updates-btn"
                  disabled={checkingUpdate}
                  onClick={() => void runUpdateCheck(true)}
                  className="inline-flex h-7 shrink-0 items-center gap-1.5 rounded-lg border border-border bg-background px-2.5 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover disabled:opacity-50"
                >
                  <RefreshCw className={`size-3.5 ${checkingUpdate ? 'animate-spin' : ''}`} />
                  <span>
                    {formatMsg(locale, checkingUpdate ? 'about.checkingUpdates' : 'about.checkUpdates')}
                  </span>
                </button>
              </div>
              {updateInfo && (updateInfo.server_has_update || updateInfo.client_has_update) && (
                <div className="space-y-1.5 rounded-lg border border-border/80 bg-background/70 p-2 text-ui-xs">
                  {updateInfo.server_has_update && (
                    <div className="text-foreground-subtle">
                      {formatMsg(locale, 'about.serverUpdateAvailable', {
                        current: updateInfo.server_version,
                        latest: updateInfo.latest_version,
                      })}
                    </div>
                  )}
                  {updateInfo.client_has_update && updateInfo.client_version && (
                    <div className="text-foreground-subtle">
                      {formatMsg(locale, 'about.desktopUpdateAvailable', {
                        current: updateInfo.client_version,
                        latest: updateInfo.latest_version,
                      })}
                    </div>
                  )}
                  <div className="flex flex-wrap items-center gap-2 pt-0.5">
                    {updateInfo.client_download_url && (
                      <button
                        type="button"
                        data-testid="about-download-desktop-btn"
                        onClick={() => void hostBridge.openExternal(updateInfo.client_download_url!)}
                        className="inline-flex h-7 items-center gap-1 rounded-md bg-primary px-2.5 text-ui-xs font-medium text-primary-foreground transition-opacity hover:opacity-90"
                      >
                        <Download className="size-3.5" />
                        <span>{formatMsg(locale, 'about.downloadDesktop')}</span>
                      </button>
                    )}
                    {updateInfo.release_url && (
                      <button
                        type="button"
                        data-testid="about-view-release-btn"
                        onClick={() => void hostBridge.openExternal(updateInfo.release_url)}
                        className="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-surface px-2.5 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover"
                      >
                        <ExternalLink className="size-3.5" />
                        <span>{formatMsg(locale, 'about.viewRelease')}</span>
                      </button>
                    )}
                  </div>
                </div>
              )}
            </div>
          </div>

          <div data-testid="about-shortcuts" className="space-y-2">
            <div className="flex items-center gap-1.5 text-ui-xs font-medium uppercase tracking-wider text-foreground-subtlest">
              <Keyboard className="size-3.5" />
              <span>{formatMsg(locale, 'about.shortcutsTitle')}</span>
            </div>
            <div className="divide-y divide-border/60 rounded-xl border border-border bg-card px-3.5 py-1">
              {shortcuts.map((sc, idx) => (
                <div key={idx} className="flex items-center justify-between gap-3 py-1.5 text-ui-sm">
                  <span className="text-foreground-subtle">{sc.label}</span>
                  <kbd className="rounded bg-tag px-1.5 py-0.5 font-mono text-ui-xs text-foreground">
                    {sc.keys}
                  </kbd>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
