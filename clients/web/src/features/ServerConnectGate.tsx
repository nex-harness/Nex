import { ArrowLeft, Globe, Laptop, LoaderCircle, QrCode, Server, ShieldCheck, Trash2 } from 'lucide-react';
import { type FormEvent, useEffect, useState } from 'react';
import { useNexStore } from '../core/store';
import { formatMsg } from '../i18n/messages';
import { QRScannerModal } from './QRScannerModal';

export function ServerConnectGate() {
  const locale = useNexStore((s) => s.locale);
  const hostInfo = useNexStore((s) => s.hostInfo);
  const activeServerUrl = useNexStore((s) => s.activeServerUrl);
  const pendingPairInviteURI = useNexStore((s) => s.pendingPairInviteURI);
  const connectToServer = useNexStore((s) => s.connectToServer);
  const useLocalDesktopMode = useNexStore((s) => s.useLocalDesktopMode);
  const pairWithURI = useNexStore((s) => s.pairWithURI);
  const connectPairedHost = useNexStore((s) => s.connectPairedHost);
  const removePairedHost = useNexStore((s) => s.removePairedHost);
  const clearPendingPairInvite = useNexStore((s) => s.clearPendingPairInvite);
  const cancelServerConnect = useNexStore((s) => s.cancelServerConnect);

  const [url, setUrl] = useState(
    () => (hostInfo?.connection_mode === 'remote' ? activeServerUrl : '') || hostInfo?.recent_servers?.[0] || '',
  );
  const [submitting, setSubmitting] = useState(false);
  const [localBusy, setLocalBusy] = useState(false);
  const [pairModalOpen, setPairModalOpen] = useState(() => Boolean(pendingPairInviteURI));
  const [pairBusy, setPairBusy] = useState(false);
  const [pairError, setPairError] = useState('');
  const [error, setError] = useState('');

  useEffect(() => {
    if (pendingPairInviteURI) {
      setPairModalOpen(true);
    }
  }, [pendingPairInviteURI]);

  const supportsLocal = Boolean(hostInfo?.supports_local_mode);
  const supportsPairing = Boolean(hostInfo);
  const pairedHosts = hostInfo?.paired_hosts || [];
  const recentServers = hostInfo?.recent_servers || [];
  const canCancel = Boolean(activeServerUrl);

  const validateAndConnect = async (raw: string) => {
    const trimmed = raw.trim().replace(/\/+$/, '');
    let parsed: URL;
    try {
      parsed = new URL(trimmed);
    } catch {
      setError(formatMsg(locale, 'client.connect.invalidUrl'));
      return;
    }
    if ((parsed.protocol !== 'http:' && parsed.protocol !== 'https:') || !parsed.host) {
      setError(formatMsg(locale, 'client.connect.invalidUrl'));
      return;
    }

    setSubmitting(true);
    setError('');
    try {
      const resp = await fetch(`${parsed.origin}${parsed.pathname.replace(/\/+$/, '')}/api/v1/health`);
      if (!resp.ok) {
        setError(formatMsg(locale, 'client.connect.unreachable'));
        setSubmitting(false);
        return;
      }
      const res = await connectToServer(trimmed);
      if (!res.ok) {
        setError(res.error || formatMsg(locale, 'client.connect.unreachable'));
      }
    } catch {
      setError(formatMsg(locale, 'client.connect.unreachable'));
    } finally {
      setSubmitting(false);
    }
  };

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (submitting || localBusy) return;
    void validateAndConnect(url);
  };

  const handleLocal = async () => {
    if (submitting || localBusy) return;
    setLocalBusy(true);
    setError('');
    try {
      const res = await useLocalDesktopMode();
      if (!res.ok && res.error) {
        setError(res.error);
      }
    } finally {
      setLocalBusy(false);
    }
  };

  const handlePairSubmit = async (uri: string, deviceName: string) => {
    setPairBusy(true);
    setPairError('');
    try {
      const res = await pairWithURI(uri, deviceName);
      if (!res.ok) {
        setPairError(res.error || formatMsg(locale, 'client.pair.invalidUri'));
        return;
      }
      setPairModalOpen(false);
    } finally {
      setPairBusy(false);
    }
  };

  return (
    <div
      data-testid="server-connect-gate"
      className="nex-safe-top nex-safe-bottom flex h-dvh w-screen flex-col items-center justify-center overflow-y-auto bg-background px-4 py-6 text-foreground"
    >
      <div className="nex-drag-region fixed inset-x-0 top-0 h-8" />

      <div className="w-full max-w-[440px] rounded-2xl border border-border bg-card p-6 shadow-xl">
        {canCancel && (
          <button
            type="button"
            data-testid="server-connect-cancel"
            onClick={cancelServerConnect}
            className="nex-no-drag mb-3 inline-flex items-center gap-1.5 rounded-lg px-2 py-1 text-ui-xs text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
          >
            <ArrowLeft className="size-3.5" />
            <span>{formatMsg(locale, 'client.connect.cancel')}</span>
          </button>
        )}

        <div className="flex flex-col items-center text-center">
          <div className="flex size-12 items-center justify-center rounded-2xl border border-border bg-surface text-foreground shadow-xs">
            <Server className="size-5" />
          </div>
          <h1 className="mt-4 text-xl font-semibold tracking-tight text-foreground">
            {formatMsg(locale, 'client.connect.title')}
          </h1>
          <p className="mt-1.5 text-ui-sm leading-relaxed text-foreground-subtle">
            {formatMsg(locale, 'client.connect.subtitle')}
          </p>
        </div>

        {pairedHosts.length > 0 && (
          <div data-testid="paired-hosts-list" className="mt-5 space-y-2">
            <div className="text-ui-xs font-medium text-foreground-subtlest">
              {formatMsg(locale, 'client.connect.pairedHostsHeading')}
            </div>
            <div className="space-y-2">
              {pairedHosts.map((host) => (
                <div
                  key={host.node_id}
                  className="flex items-center justify-between gap-2 rounded-xl border border-border bg-surface px-3 py-2.5"
                >
                  <button
                    type="button"
                    data-testid={`paired-host-connect-${host.node_id}`}
                    onClick={() => void connectPairedHost(host.node_id)}
                    className="flex min-w-0 flex-1 items-center gap-2.5 text-left"
                  >
                    <ShieldCheck className="size-4 shrink-0 text-success-surface-fg" />
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-ui-sm font-medium text-foreground">
                        {host.host_name || host.node_id}
                      </div>
                      <div className="truncate font-mono text-[11px] text-foreground-subtlest">
                        {host.node_id}
                        {host.last_transport ? ` · ${host.last_transport}` : ''}
                      </div>
                    </div>
                  </button>
                  <button
                    type="button"
                    data-testid={`paired-host-remove-${host.node_id}`}
                    aria-label={formatMsg(locale, 'client.connect.removeHost')}
                    onClick={() => void removePairedHost(host.node_id)}
                    className="flex size-7 shrink-0 items-center justify-center rounded-lg text-foreground-subtlest transition-colors hover:bg-hover hover:text-destructive-surface-fg"
                  >
                    <Trash2 className="size-3.5" />
                  </button>
                </div>
              ))}
            </div>
          </div>
        )}

        {supportsPairing && (
          <button
            type="button"
            data-testid="server-pair-qr-btn"
            disabled={submitting || localBusy}
            onClick={() => {
              setPairError('');
              setPairModalOpen(true);
            }}
            className="mt-5 flex h-10 w-full items-center justify-center gap-2 rounded-xl border border-border bg-surface text-ui-sm font-medium text-foreground transition-colors hover:bg-hover"
          >
            <QrCode className="size-4" />
            <span>{formatMsg(locale, 'client.connect.scanPairBtn')}</span>
          </button>
        )}

        <form onSubmit={handleSubmit} className="mt-5 space-y-3.5">
          <div>
            <label className="mb-1.5 block text-ui-xs font-medium text-foreground-subtle">
              {formatMsg(locale, 'client.connect.urlLabel')}
            </label>
            <div className="flex h-10 items-center gap-2 rounded-xl border border-border bg-input px-3 focus-within:border-brand-blue">
              <Globe className="size-4 shrink-0 text-foreground-subtlest" />
              <input
                autoFocus={!supportsPairing}
                type="url"
                inputMode="url"
                autoCapitalize="none"
                autoCorrect="off"
                data-testid="server-url-input"
                value={url}
                onChange={(e) => {
                  setUrl(e.target.value);
                  if (error) setError('');
                }}
                placeholder={formatMsg(locale, 'client.connect.urlPlaceholder')}
                className="min-w-0 flex-1 bg-transparent font-mono text-ui-base text-foreground placeholder-foreground-subtlest outline-none"
              />
            </div>
          </div>

          {error && (
            <div
              data-testid="server-connect-error"
              className="rounded-lg border border-destructive/30 bg-destructive-surface px-3 py-2 text-ui-xs text-destructive-surface-fg"
            >
              {error}
            </div>
          )}

          <button
            type="submit"
            data-testid="server-connect-btn"
            disabled={!url.trim() || submitting || localBusy}
            className="flex h-10 w-full items-center justify-center gap-2 rounded-xl bg-foreground text-ui-base font-medium text-background transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-45"
          >
            {submitting && <LoaderCircle className="size-4 animate-spin" />}
            <span>
              {submitting
                ? formatMsg(locale, 'client.connect.connecting')
                : formatMsg(locale, 'client.connect.connectBtn')}
            </span>
          </button>
        </form>

        {recentServers.length > 0 && (
          <div className="mt-4 space-y-1.5">
            <div className="text-ui-xs font-medium text-foreground-subtlest">
              {formatMsg(locale, 'client.connect.recentServers')}
            </div>
            <div className="flex flex-wrap gap-1.5">
              {recentServers.map((s) => (
                <button
                  key={s}
                  type="button"
                  onClick={() => {
                    setUrl(s);
                    void validateAndConnect(s);
                  }}
                  className="max-w-full truncate rounded-lg border border-border bg-surface px-2.5 py-1 font-mono text-ui-xs text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
                >
                  {s}
                </button>
              ))}
            </div>
          </div>
        )}

        {supportsLocal && (
          <>
            <div className="my-5 flex items-center gap-3 text-ui-xs text-foreground-subtlest">
              <div className="h-px flex-1 bg-border" />
              <span>{formatMsg(locale, 'client.connect.orDivider')}</span>
              <div className="h-px flex-1 bg-border" />
            </div>

            <button
              type="button"
              data-testid="server-local-mode-btn"
              disabled={submitting || localBusy}
              onClick={() => void handleLocal()}
              className="flex h-10 w-full items-center justify-center gap-2 rounded-xl border border-border bg-surface text-ui-base font-medium text-foreground transition-colors hover:bg-hover disabled:opacity-50"
            >
              {localBusy ? <LoaderCircle className="size-4 animate-spin" /> : <Laptop className="size-4" />}
              <span>{formatMsg(locale, 'client.connect.localModeBtn')}</span>
            </button>
            <p className="mt-1.5 text-center text-ui-xs text-foreground-subtlest">
              {formatMsg(locale, 'client.connect.localModeDesc')}
            </p>
          </>
        )}
      </div>

      <QRScannerModal
        open={pairModalOpen}
        initialURI={pendingPairInviteURI}
        busy={pairBusy}
        error={pairError}
        onClose={() => {
          setPairModalOpen(false);
          setPairError('');
          clearPendingPairInvite();
        }}
        onSubmitURI={handlePairSubmit}
      />
    </div>
  );
}
