import { Copy, QrCode, RefreshCw, ShieldCheck, Trash2 } from 'lucide-react';
import QRCode from 'qrcode';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { apiFetch } from '../core/protocol';
import { useNexStore } from '../core/store';
import {
  type RemoteHostConfig,
  type RemoteHostStatus,
  type RemotePairedDevice,
  type RemotePairingSession,
  type SystemTailscaleStatus,
  type TSNetSidecarStatus,
} from '../core/types';
import { formatMsg, formatRelativeTimeShort } from '../i18n/messages';
import {
  SectionHeading,
  SettingsGroupCard,
  SettingsRow,
  settingsInputClass,
  settingsMonoInputClass,
  Toggle,
} from './SettingsPage';

function QRCodeSVG({
  value,
  size = 196,
  testId = 'remote-pair-qr-svg',
}: {
  value: string;
  size?: number;
  testId?: string;
}) {
  const pathData = useMemo(() => {
    const qr = QRCode.create(value, { errorCorrectionLevel: 'M' });
    const count = qr.modules.size;
    const cmds: string[] = [];
    for (let y = 0; y < count; y++) {
      for (let x = 0; x < count; x++) {
        if (qr.modules.get(x, y)) {
          cmds.push(`M${x + 1},${y + 1}h1v1h-1z`);
        }
      }
    }
    return { viewBox: `0 0 ${count + 2} ${count + 2}`, d: cmds.join('') };
  }, [value]);

  return (
    <svg
      data-testid={testId}
      width={size}
      height={size}
      viewBox={pathData.viewBox}
      shapeRendering="crispEdges"
      className="rounded-xl border border-border bg-white p-2 shadow-xs"
    >
      <path d={pathData.d} fill="#111111" />
    </svg>
  );
}

export function SettingsRemoteAccessSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const { locale } = useNexStore();

  const [status, setStatus] = useState<RemoteHostStatus | null>(null);
  const [devices, setDevices] = useState<RemotePairedDevice[]>([]);
  const [pairSession, setPairSession] = useState<RemotePairingSession | null>(null);
  const [busy, setBusy] = useState(false);

  const [enabled, setEnabled] = useState(false);
  const [hostName, setHostName] = useState('');
  const [relayUrl, setRelayUrl] = useState('');
  const [relayToken, setRelayToken] = useState('');
  const [directUrlsText, setDirectUrlsText] = useState('');
  const [preferLan, setPreferLan] = useState(true);
  const [tsnetEnabled, setTsnetEnabled] = useState(false);
  const [tsnetControlUrl, setTsnetControlUrl] = useState('');
  const [tsnetHostname, setTsnetHostname] = useState('');
  const [tsnetMirror, setTsnetMirror] = useState('');
  const [installingTsnet, setInstallingTsnet] = useState(false);

  const loadAll = useCallback(async () => {
    try {
      const [stRes, devRes] = await Promise.all([
        apiFetch<{ status: RemoteHostStatus; paired_device_count: number }>('/api/v1/remote/status'),
        apiFetch<{ devices: RemotePairedDevice[] }>('/api/v1/remote/devices'),
      ]);
      if (stRes?.status) {
        setStatus(stRes.status);
        const cfg = stRes.status.config;
        setEnabled(Boolean(cfg.enabled));
        setHostName(cfg.host_name || stRes.status.host_name || '');
        setRelayUrl(cfg.relay_url || '');
        setRelayToken(cfg.relay_token || '');
        setDirectUrlsText((cfg.direct_urls || []).join('\n'));
        setPreferLan(cfg.prefer_lan !== false);
        setTsnetEnabled(Boolean(cfg.tsnet_enabled));
        setTsnetControlUrl(cfg.tsnet_control_url || '');
        setTsnetHostname(cfg.tsnet_hostname || '');
        setTsnetMirror(cfg.tsnet_download_mirror || '');
      }
      setDevices(devRes?.devices || []);
    } catch {
      // ignore transient API failures while detached
    }
  }, []);

  useEffect(() => {
    void loadAll();
  }, [loadAll]);

  const handleSave = async () => {
    setBusy(true);
    try {
      const payload: RemoteHostConfig = {
        enabled,
        host_name: hostName.trim(),
        relay_url: relayUrl.trim(),
        relay_token: relayToken.trim(),
        direct_urls: directUrlsText
          .split(/\r?\n/)
          .map((line) => line.trim())
          .filter(Boolean),
        prefer_lan: preferLan,
        tsnet_enabled: tsnetEnabled,
        tsnet_control_url: tsnetControlUrl.trim(),
        tsnet_hostname: tsnetHostname.trim(),
        tsnet_download_mirror: tsnetMirror.trim(),
      };
      const updated = await apiFetch<RemoteHostStatus>('/api/v1/remote/config', {
        method: 'PUT',
        body: JSON.stringify(payload),
      });
      setStatus(updated);
      onFlash(formatMsg(locale, 'settings.remote.saved'));
    } finally {
      setBusy(false);
    }
  };

  const handleProbeTsnet = async () => {
    setBusy(true);
    try {
      const res = await apiFetch<{ system_tailscale: SystemTailscaleStatus; tsnet: TSNetSidecarStatus }>(
        '/api/v1/remote/tsnet/probe',
        { method: 'POST' },
      );
      setStatus((prev) =>
        prev
          ? {
              ...prev,
              system_tailscale: res.system_tailscale,
              tsnet: res.tsnet,
            }
          : prev,
      );
    } finally {
      setBusy(false);
    }
  };

  const handleInstallTsnet = async () => {
    if (!tsnetMirror.trim()) {
      onFlash(formatMsg(locale, 'settings.remote.tsnetNeedMirror'));
      return;
    }
    setInstallingTsnet(true);
    try {
      const res = await apiFetch<{ ok: boolean; tsnet: TSNetSidecarStatus }>('/api/v1/remote/tsnet/install', {
        method: 'POST',
        body: JSON.stringify({ download_url: tsnetMirror.trim() }),
      });
      setStatus((prev) => (prev ? { ...prev, tsnet: res.tsnet } : prev));
      onFlash(formatMsg(locale, 'settings.remote.tsnetInstalledMsg'));
    } finally {
      setInstallingTsnet(false);
    }
  };

  const handleUninstallTsnet = async () => {
    setBusy(true);
    try {
      const res = await apiFetch<{ system_tailscale: SystemTailscaleStatus; tsnet: TSNetSidecarStatus }>(
        '/api/v1/remote/tsnet',
        { method: 'DELETE' },
      );
      setStatus((prev) =>
        prev
          ? {
              ...prev,
              system_tailscale: res.system_tailscale,
              tsnet: res.tsnet,
            }
          : prev,
      );
      onFlash(formatMsg(locale, 'settings.remote.tsnetUninstalledMsg'));
    } finally {
      setBusy(false);
    }
  };

  const handleGenerateQR = async () => {
    setBusy(true);
    try {
      const session = await apiFetch<RemotePairingSession>('/api/v1/remote/pair/start', {
        method: 'POST',
        body: JSON.stringify({ ttl_seconds: 300 }),
      });
      setPairSession(session);
    } finally {
      setBusy(false);
    }
  };

  const handleCopyURI = async () => {
    if (!pairSession?.pair_uri) return;
    try {
      await navigator.clipboard.writeText(pairSession.pair_uri);
      onFlash(formatMsg(locale, 'settings.remote.copiedUri'));
    } catch {
      // ignore clipboard errors
    }
  };

  const handleRevoke = async (id: string) => {
    await apiFetch(`/api/v1/remote/devices/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    });
    setDevices((prev) => prev.filter((d) => d.id !== id));
    onFlash(formatMsg(locale, 'settings.remote.revokedDevice'));
  };

  const relayBadgeClass = !status?.enabled
    ? 'bg-surface text-foreground-subtle'
    : status.relay_connected
      ? 'bg-success-surface text-success-surface-fg'
      : 'bg-destructive-surface text-destructive-surface-fg';

  const relayStatusLabel = !status?.enabled
    ? formatMsg(locale, 'settings.remote.statusDisabled')
    : status.relay_connected
      ? formatMsg(locale, 'settings.remote.statusConnected')
      : formatMsg(locale, 'settings.remote.statusOffline');

  return (
    <div data-testid="settings-remote-access" className="space-y-6">
      <div className="flex items-center justify-between gap-3">
        <SectionHeading title={formatMsg(locale, 'settings.remote.identityHeading')} />
        <button
          type="button"
          onClick={() => void loadAll()}
          className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border bg-card px-3 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover"
        >
          <RefreshCw className="size-3.5" />
          <span>{formatMsg(locale, 'settings.remote.refreshStatus')}</span>
        </button>
      </div>

      <SettingsGroupCard>
        <SettingsRow label={formatMsg(locale, 'settings.remote.nodeId')}>
          <span data-testid="remote-node-id" className="font-mono text-ui-xs text-foreground">
            {status?.node_id || '—'}
          </span>
        </SettingsRow>
        <SettingsRow label={formatMsg(locale, 'settings.remote.fingerprint')}>
          <span className="font-mono text-ui-xs text-foreground-subtle">
            {status?.short_fingerprint || '—'}
          </span>
        </SettingsRow>
        <SettingsRow
          label={formatMsg(locale, 'settings.remote.relayStatus')}
          description={status?.last_relay_error || undefined}
        >
          <div className="flex items-center gap-2">
            <span className={`rounded-md px-2.5 py-1 text-ui-xs font-medium ${relayBadgeClass}`}>
              {relayStatusLabel}
            </span>
            <span className="text-ui-xs text-foreground-subtle">
              {formatMsg(locale, 'settings.remote.activeTunnels', {
                count: status?.active_tunnels ?? 0,
              })}
            </span>
          </div>
        </SettingsRow>
        {(status?.lan_urls || []).length > 0 && (
          <SettingsRow label={formatMsg(locale, 'settings.remote.lanEndpoints')}>
            <div className="flex flex-wrap justify-end gap-1.5">
              {status!.lan_urls.map((u) => (
                <span
                  key={u}
                  className="rounded-md border border-border bg-surface px-2 py-0.5 font-mono text-ui-xs text-foreground-subtle"
                >
                  {u}
                </span>
              ))}
            </div>
          </SettingsRow>
        )}
      </SettingsGroupCard>

      <SectionHeading title={formatMsg(locale, 'settings.remote.configHeading')} />

      <SettingsGroupCard>
        <SettingsRow
          label={formatMsg(locale, 'settings.remote.enabled')}
          description={formatMsg(locale, 'settings.remote.enabledDesc')}
        >
          <Toggle
            testId="remote-enable-toggle"
            checked={enabled}
            onChange={setEnabled}
            label={formatMsg(locale, 'settings.remote.enabled')}
          />
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.remote.hostName')}
          description={formatMsg(locale, 'settings.remote.hostNameDesc')}
        >
          <input
            data-testid="remote-hostname-input"
            value={hostName}
            onChange={(e) => setHostName(e.target.value)}
            className={settingsInputClass}
          />
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.remote.relayUrl')}
          description={formatMsg(locale, 'settings.remote.relayUrlDesc')}
        >
          <input
            data-testid="remote-relay-url-input"
            value={relayUrl}
            onChange={(e) => setRelayUrl(e.target.value)}
            placeholder={formatMsg(locale, 'settings.remote.relayUrlPlaceholder')}
            className={settingsMonoInputClass}
          />
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.remote.relayToken')}
          description={formatMsg(locale, 'settings.remote.relayTokenDesc')}
        >
          <input
            data-testid="remote-relay-token-input"
            type="password"
            value={relayToken}
            onChange={(e) => setRelayToken(e.target.value)}
            className={settingsMonoInputClass}
          />
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.remote.directUrls')}
          description={formatMsg(locale, 'settings.remote.directUrlsDesc')}
        >
          <textarea
            data-testid="remote-direct-urls-input"
            rows={2}
            value={directUrlsText}
            onChange={(e) => setDirectUrlsText(e.target.value)}
            placeholder={formatMsg(locale, 'settings.remote.directUrlsPlaceholder')}
            className="w-full rounded-lg border border-border bg-input px-3 py-2 font-mono text-ui-xs text-foreground placeholder-foreground-subtlest"
          />
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.remote.preferLan')}
          description={formatMsg(locale, 'settings.remote.preferLanDesc')}
        >
          <Toggle
            testId="remote-prefer-lan-toggle"
            checked={preferLan}
            onChange={setPreferLan}
            label={formatMsg(locale, 'settings.remote.preferLan')}
          />
        </SettingsRow>

        <div className="flex justify-end px-5 py-3.5">
          <button
            type="button"
            data-testid="remote-save-btn"
            disabled={busy}
            onClick={() => void handleSave()}
            className="inline-flex h-9 items-center rounded-lg bg-foreground px-4 text-ui-sm font-medium text-background transition-opacity hover:opacity-90 disabled:opacity-50"
          >
            {formatMsg(locale, 'settings.remote.saveConfig')}
          </button>
        </div>
      </SettingsGroupCard>

      <div data-testid="remote-tailscale-card" className="space-y-3">
        <SectionHeading
          title={formatMsg(locale, 'settings.remote.tailscaleHeading')}
          description={formatMsg(locale, 'settings.remote.tailscaleDesc')}
        />

        <SettingsGroupCard>
          <SettingsRow label={formatMsg(locale, 'settings.remote.systemTailscale')}>
            <div className="flex flex-wrap items-center justify-end gap-2">
              <span
                data-testid="remote-system-tailscale-badge"
                className={`rounded-md px-2.5 py-1 text-ui-xs font-medium ${
                  status?.system_tailscale?.running
                    ? 'bg-success-surface text-success-surface-fg'
                    : 'bg-surface text-foreground-subtle'
                }`}
              >
                {status?.system_tailscale?.backend_state === 'mobile_os_managed'
                  ? formatMsg(locale, 'settings.remote.systemTailscaleMobile')
                  : status?.system_tailscale?.running
                    ? formatMsg(locale, 'settings.remote.systemTailscaleRunning', {
                        dns:
                          status.system_tailscale.dns_name ||
                          status.system_tailscale.tailscale_ips?.[0] ||
                          'ok',
                      })
                    : status?.system_tailscale?.available
                      ? formatMsg(locale, 'settings.remote.systemTailscaleStopped')
                      : formatMsg(locale, 'settings.remote.systemTailscaleMissing')}
              </span>
              <button
                type="button"
                data-testid="remote-tsnet-probe-btn"
                disabled={busy}
                onClick={() => void handleProbeTsnet()}
                className="inline-flex h-8 items-center rounded-lg border border-border bg-card px-2.5 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover"
              >
                {formatMsg(locale, 'settings.remote.tsnetProbe')}
              </button>
            </div>
          </SettingsRow>

          {status?.tsnet?.platform_supported !== false && (
            <>
              <SettingsRow
                label={formatMsg(locale, 'settings.remote.tsnetToggle')}
                description={formatMsg(locale, 'settings.remote.tsnetToggleDesc')}
              >
                <Toggle
                  testId="remote-tsnet-toggle"
                  checked={tsnetEnabled}
                  onChange={setTsnetEnabled}
                  label={formatMsg(locale, 'settings.remote.tsnetToggle')}
                />
              </SettingsRow>

              <SettingsRow label={formatMsg(locale, 'settings.remote.tsnetHostname')}>
                <input
                  data-testid="remote-tsnet-hostname"
                  value={tsnetHostname}
                  onChange={(e) => setTsnetHostname(e.target.value)}
                  placeholder={formatMsg(locale, 'settings.remote.tsnetHostnamePlaceholder')}
                  className={settingsInputClass}
                />
              </SettingsRow>

              <SettingsRow label={formatMsg(locale, 'settings.remote.tsnetControlUrl')}>
                <input
                  data-testid="remote-tsnet-control-url"
                  value={tsnetControlUrl}
                  onChange={(e) => setTsnetControlUrl(e.target.value)}
                  placeholder={formatMsg(locale, 'settings.remote.tsnetControlUrlPlaceholder')}
                  className={settingsMonoInputClass}
                />
              </SettingsRow>

              <SettingsRow label={formatMsg(locale, 'settings.remote.tsnetMirror')}>
                <div className="flex w-full items-center gap-2">
                  <input
                    data-testid="remote-tsnet-mirror"
                    value={tsnetMirror}
                    onChange={(e) => setTsnetMirror(e.target.value)}
                    placeholder={formatMsg(locale, 'settings.remote.tsnetMirrorPlaceholder')}
                    className={settingsMonoInputClass}
                  />
                </div>
              </SettingsRow>

              <div className="flex flex-wrap items-center justify-between gap-3 px-5 py-3.5">
                <span className="text-ui-xs text-foreground-subtle">
                  {formatMsg(locale, 'settings.remote.tsnetStatePrefix', {
                    state: status?.tsnet?.state || 'not_installed',
                  })}
                </span>
                <div className="flex items-center gap-2">
                  {status?.tsnet?.installed && (
                    <button
                      type="button"
                      data-testid="remote-tsnet-uninstall-btn"
                      disabled={busy}
                      onClick={() => void handleUninstallTsnet()}
                      className="inline-flex h-8 items-center rounded-lg border border-border bg-card px-3 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover"
                    >
                      {formatMsg(locale, 'settings.remote.tsnetUninstall')}
                    </button>
                  )}
                  <button
                    type="button"
                    data-testid="remote-tsnet-install-btn"
                    disabled={installingTsnet || busy}
                    onClick={() => void handleInstallTsnet()}
                    className="inline-flex h-8 items-center rounded-lg border border-border bg-card px-3 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover disabled:opacity-50"
                  >
                    {installingTsnet
                      ? formatMsg(locale, 'settings.remote.tsnetInstalling')
                      : formatMsg(locale, 'settings.remote.tsnetInstall')}
                  </button>
                </div>
              </div>

              {status?.tsnet?.auth_url && (
                <div className="flex flex-col items-center gap-3 border-t border-border px-5 py-4 sm:flex-row">
                  <QRCodeSVG value={status.tsnet.auth_url} size={136} testId="remote-tsnet-auth-qr" />
                  <div className="min-w-0 flex-1 space-y-1.5">
                    <div className="text-ui-xs font-medium text-foreground">
                      {formatMsg(locale, 'settings.remote.tsnetLoginPrompt')}
                    </div>
                    <a
                      href={status.tsnet.auth_url}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="block break-all font-mono text-ui-xs text-brand-blue underline"
                    >
                      {status.tsnet.auth_url}
                    </a>
                  </div>
                </div>
              )}
            </>
          )}
        </SettingsGroupCard>
      </div>

      <SectionHeading
        title={formatMsg(locale, 'settings.remote.pairHeading')}
        description={formatMsg(locale, 'settings.remote.pairDesc')}
      />

      <SettingsGroupCard>
        <div className="flex flex-col items-center gap-4 px-5 py-5 sm:flex-row sm:items-start">
          {pairSession?.pair_uri ? (
            <div className="shrink-0">
              <QRCodeSVG value={pairSession.pair_uri} />
            </div>
          ) : (
            <div className="flex size-[196px] shrink-0 items-center justify-center rounded-xl border border-dashed border-border bg-surface text-foreground-subtlest">
              <QrCode className="size-10" />
            </div>
          )}

          <div className="min-w-0 flex-1 space-y-3">
            <div className="flex flex-wrap gap-2">
              <button
                type="button"
                data-testid="remote-pair-start-btn"
                disabled={busy}
                onClick={() => void handleGenerateQR()}
                className="inline-flex h-9 items-center gap-1.5 rounded-lg bg-foreground px-4 text-ui-sm font-medium text-background transition-opacity hover:opacity-90 disabled:opacity-50"
              >
                <QrCode className="size-4" />
                <span>
                  {pairSession
                    ? formatMsg(locale, 'settings.remote.regenerateQr')
                    : formatMsg(locale, 'settings.remote.generateQr')}
                </span>
              </button>

              {pairSession?.pair_uri && (
                <button
                  type="button"
                  data-testid="remote-pair-copy-btn"
                  onClick={() => void handleCopyURI()}
                  className="inline-flex h-9 items-center gap-1.5 rounded-lg border border-border bg-card px-3 text-ui-sm font-medium text-foreground transition-colors hover:bg-hover"
                >
                  <Copy className="size-3.5" />
                  <span>{formatMsg(locale, 'settings.remote.copyPairUri')}</span>
                </button>
              )}
            </div>

            {pairSession?.pair_uri && (
              <div className="space-y-1.5">
                <p className="text-ui-xs text-foreground-subtle">
                  {formatMsg(locale, 'settings.remote.pairExpires', {
                    fp: pairSession.short_fingerprint,
                  })}
                </p>
                <div className="break-all rounded-lg border border-border bg-surface px-3 py-2 font-mono text-[11px] text-foreground-subtle">
                  {pairSession.pair_uri}
                </div>
              </div>
            )}
          </div>
        </div>
      </SettingsGroupCard>

      <SectionHeading
        title={formatMsg(locale, 'settings.remote.devicesHeading')}
        description={formatMsg(locale, 'settings.remote.devicesDesc')}
      />

      <SettingsGroupCard>
        <div data-testid="remote-paired-devices" className="divide-y divide-border">
          {devices.length === 0 ? (
            <div className="px-5 py-6 text-center text-ui-sm text-foreground-subtle">
              {formatMsg(locale, 'settings.remote.noDevices')}
            </div>
          ) : (
            devices.map((dev) => (
              <div key={dev.id} className="flex flex-wrap items-center justify-between gap-3 px-5 py-3.5">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <ShieldCheck className="size-4 shrink-0 text-success-surface-fg" />
                    <span className="truncate text-ui-base font-medium text-foreground">{dev.name}</span>
                    <span className="rounded-md bg-tag px-2 py-0.5 text-ui-xs font-medium text-foreground-subtle">
                      {dev.platform || 'client'}
                    </span>
                    {dev.last_transport && (
                      <span className="rounded-md bg-surface px-2 py-0.5 text-ui-xs text-foreground-subtle">
                        {dev.last_transport}
                      </span>
                    )}
                  </div>
                  <div className="mt-1 flex items-center gap-3 text-ui-xs text-foreground-subtlest">
                    <span className="font-mono">{dev.pubkey_fp.slice(0, 16)}</span>
                    <span>
                      {dev.last_seen_at > 0
                        ? formatRelativeTimeShort(dev.last_seen_at, locale)
                        : formatMsg(locale, 'settings.remote.neverSeen')}
                    </span>
                  </div>
                </div>
                <button
                  type="button"
                  data-testid={`remote-revoke-device-${dev.id}`}
                  onClick={() => void handleRevoke(dev.id)}
                  className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-destructive/30 bg-destructive-surface px-3 text-ui-xs font-medium text-destructive-surface-fg transition-opacity hover:opacity-90"
                >
                  <Trash2 className="size-3.5" />
                  <span>{formatMsg(locale, 'settings.remote.revokeDevice')}</span>
                </button>
              </div>
            ))
          )}
        </div>
      </SettingsGroupCard>
    </div>
  );
}
