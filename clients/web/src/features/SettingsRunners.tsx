import { formatMsg } from '../i18n/messages';
import type { LocaleCode } from '../i18n';
import {
  AlertTriangle,
  ArrowUpCircle,
  Check,
  ChevronDown,
  ChevronRight,
  CloudDownload,
  Copy,
  Eye,
  EyeOff,
  FileCode2,
  KeyRound,
  Link2,
  LoaderCircle,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Server,
  Shield,
  Square,
  Terminal,
  Trash2,
  Wifi,
  X,
} from 'lucide-react';
import { type ClipboardEvent, type FormEvent, useEffect, useRef, useState } from 'react';
import { apiFetch } from '../core/protocol';
import { useNexStore } from '../core/store';
import { type RunnerRecord, type SSHConfigHost, type SSHTargetRecord, type StagedBinaryInfo } from '../core/types';
import {
  SectionHeading,
  SettingsGroupCard,
  settingsInputClass,
  settingsMonoInputClass,
} from './SettingsPage';

interface SSHFormState {
  id: string;
  name: string;
  ssh_alias: string;
  host: string;
  port: string;
  username: string;
  auth_method: 'agent' | 'key' | 'password';
  key_path: string;
  password: string;
  has_password: boolean;
  workspace_path: string;
  install_dir: string;
  server_ws_url: string;
  install_mode: 'scp' | 'remote_install';
  download_url: string;
}

const EMPTY_FORM: SSHFormState = {
  id: '',
  name: '',
  ssh_alias: '',
  host: '',
  port: '22',
  username: '',
  auth_method: 'key',
  key_path: '~/.ssh/id_ed25519',
  password: '',
  has_password: false,
  workspace_path: '',
  install_dir: '~/.nex-runner',
  server_ws_url: '',
  install_mode: 'scp',
  download_url: '',
};

/** Parses pasted SSH targets like `user@host`, `user@host:2222`, or `ssh user@host -p 2222`. */
export function parsePastedSSHTarget(raw: string): { host?: string; username?: string; port?: string } | null {
  const s = raw.trim();
  if (!s) return null;
  let rest = s;
  let port: string | undefined;

  if (/^ssh\s+/i.test(rest)) {
    rest = rest.replace(/^ssh\s+/i, '').trim();
    const pMatch = /(?:^|\s)-p\s+(\d{1,5})(?:\s|$)/.exec(rest);
    if (pMatch) {
      port = pMatch[1];
      rest = rest.replace(pMatch[0], ' ').trim();
    }
    // take first non-flag token
    const token = rest.split(/\s+/).find((tok) => tok && !tok.startsWith('-'));
    if (!token) return null;
    rest = token;
  }

  let username: string | undefined;
  let hostPart = rest;
  const atIdx = rest.lastIndexOf('@');
  if (atIdx > 0) {
    username = rest.slice(0, atIdx).trim();
    hostPart = rest.slice(atIdx + 1).trim();
  }

  const ipv6Port = /^\[([^\]]+)\]:(\d{1,5})$/.exec(hostPart);
  if (ipv6Port) {
    hostPart = ipv6Port[1];
    port = port || ipv6Port[2];
  } else if (hostPart.includes(':') && hostPart.indexOf(':') === hostPart.lastIndexOf(':')) {
    const [h, p] = hostPart.split(':');
    if (/^\d{1,5}$/.test(p)) {
      hostPart = h;
      port = port || p;
    }
  }

  if (!hostPart || (!username && !port && !/^ssh\s+/i.test(s))) {
    return null;
  }
  return { host: hostPart, username, port };
}

function formatBytesShort(n: number): string {
  if (n >= 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  if (n >= 1024) return `${Math.round(n / 1024)} KB`;
  return `${n} B`;
}

function formatRunnerUpdatePhase(locale: LocaleCode, phase?: string, progress?: number): string {
  if (!phase || phase === 'idle') return '';
  switch (phase) {
    case 'queued':
      return formatMsg(locale, 'runners.phase.queued');
    case 'draining':
      return formatMsg(locale, 'runners.phase.draining');
    case 'downloading':
      return formatMsg(locale, 'runners.phase.downloading', { pct: Math.round(progress ?? 0) });
    case 'verifying':
      return formatMsg(locale, 'runners.phase.verifying');
    case 'installing':
      return formatMsg(locale, 'runners.phase.installing');
    case 'restarting':
      return formatMsg(locale, 'runners.phase.restarting');
    case 'succeeded':
      return formatMsg(locale, 'runners.phase.succeeded');
    case 'failed':
      return formatMsg(locale, 'runners.phase.failed');
    case 'rolled_back':
      return formatMsg(locale, 'runners.phase.rolled_back');
    default:
      return phase;
  }
}

export function RunnersSettingsSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const { locale, runners, sshTargets, refreshInitialData, refreshRegistries, triggerRunnerUpdate, triggerAllRunnersUpdate } = useNexStore();

  const [copied, setCopied] = useState(false);
  const [manualOpen, setManualOpen] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [form, setForm] = useState<SSHFormState>(EMPTY_FORM);
  const [showPwd, setShowPwd] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [pasteNotice, setPasteNotice] = useState('');
  const [formError, setFormError] = useState('');
  const [saving, setSaving] = useState(false);

  const [configHosts, setConfigHosts] = useState<SSHConfigHost[]>([]);
  const [configMenuOpen, setConfigMenuOpen] = useState(false);
  const [configLoading, setConfigLoading] = useState(false);

  const [stagedBinaries, setStagedBinaries] = useState<StagedBinaryInfo[]>([]);

  const [busyById, setBusyById] = useState<Record<string, 'test' | 'deploy' | 'stop'>>({});
  const [expandedLogs, setExpandedLogs] = useState<Record<string, boolean>>({});

  const wsProto = typeof window !== 'undefined' && window.location.protocol === 'https:' ? 'wss' : 'ws';
  const wsHost = typeof window !== 'undefined' ? window.location.host : '127.0.0.1:8080';
  const joinCmd = `./nex-runner -name "My-Linux-Box" -server ${wsProto}://${wsHost}/ws/runner`;

  const loadStagedBinaries = async () => {
    try {
      const list = await apiFetch<StagedBinaryInfo[]>('/api/v1/ssh/binaries');
      if (Array.isArray(list)) setStagedBinaries(list);
    } catch {
      // ignore
    }
  };

  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    void loadStagedBinaries();
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const loadSSHConfigHosts = async () => {
    setConfigLoading(true);
    try {
      const hosts = await apiFetch<SSHConfigHost[]>('/api/v1/ssh/config-hosts');
      if (mountedRef.current) setConfigHosts(Array.isArray(hosts) ? hosts : []);
    } catch {
      if (mountedRef.current) setConfigHosts([]);
    } finally {
      if (mountedRef.current) setConfigLoading(false);
    }
  };

  const openCreateModal = () => {
    setForm(EMPTY_FORM);
    setFormError('');
    setPasteNotice('');
    setAdvancedOpen(false);
    setShowPwd(false);
    setConfigMenuOpen(false);
    setModalOpen(true);
    void loadSSHConfigHosts();
    void loadStagedBinaries();
  };

  const openEditModal = (t: SSHTargetRecord) => {
    setForm({
      id: t.id,
      name: t.name || '',
      ssh_alias: t.ssh_alias || '',
      host: t.host || '',
      port: String(t.port || 22),
      username: t.username || '',
      auth_method: t.auth_method || 'key',
      key_path: t.key_path || '',
      password: '',
      has_password: Boolean(t.has_password),
      workspace_path: t.workspace_path || '',
      install_dir: t.install_dir || '~/.nex-runner',
      server_ws_url: t.server_ws_url || '',
      install_mode: t.install_mode || 'scp',
      download_url: t.download_url || '',
    });
    setFormError('');
    setPasteNotice('');
    setAdvancedOpen(Boolean(t.workspace_path || t.server_ws_url || (t.install_dir && t.install_dir !== '~/.nex-runner')));
    setShowPwd(false);
    setConfigMenuOpen(false);
    setModalOpen(true);
    void loadSSHConfigHosts();
    void loadStagedBinaries();
  };

  const applyConfigHost = (h: SSHConfigHost) => {
    setForm((prev) => ({
      ...prev,
      name: prev.name || h.alias,
      ssh_alias: h.alias,
      host: h.hostname || h.alias,
      port: String(h.port || 22),
      username: h.user || prev.username,
      auth_method: h.identity_file ? 'key' : prev.auth_method,
      key_path: h.identity_file || prev.key_path,
    }));
    setConfigMenuOpen(false);
    setPasteNotice('');
  };

  const handleHostPaste = (e: ClipboardEvent<HTMLInputElement>) => {
    const pasted = e.clipboardData?.getData('text') || '';
    const parsed = parsePastedSSHTarget(pasted);
    if (!parsed) return;
    e.preventDefault();
    setForm((prev) => ({
      ...prev,
      ssh_alias: '',
      host: parsed.host || prev.host,
      username: parsed.username || prev.username,
      port: parsed.port || prev.port,
      name: prev.name || parsed.host || '',
    }));
    setPasteNotice(
      formatMsg(locale, 'runners.parsed', { v: parsed.username ? `${parsed.username}@` : '', host: parsed.host || '', v2: parsed.port ? `:${parsed.port}` : '' }),
    );
  };

  const submitForm = async (e: FormEvent, deployAfterSave = false) => {
    e.preventDefault();
    const host = form.host.trim();
    const portNum = Number(form.port.trim() || '22');
    if (!host) {
      setFormError(formatMsg(locale, 'runners.hostIsRequired'));
      return;
    }
    if (!Number.isInteger(portNum) || portNum < 1 || portNum > 65535) {
      setFormError(formatMsg(locale, 'runners.portMustBeBetween1'));
      return;
    }
    setSaving(true);
    setFormError('');
    try {
      const saved = await apiFetch<SSHTargetRecord>('/api/v1/ssh/targets', {
        method: 'POST',
        body: JSON.stringify({
          id: form.id || undefined,
          name: form.name.trim() || host,
          ssh_alias: form.ssh_alias.trim(),
          host,
          port: portNum,
          username: form.username.trim(),
          auth_method: form.auth_method,
          key_path: form.key_path.trim(),
          password: form.password,
          workspace_path: form.workspace_path.trim(),
          install_dir: form.install_dir.trim() || '~/.nex-runner',
          server_ws_url: form.server_ws_url.trim(),
          install_mode: form.install_mode,
          download_url: form.download_url.trim(),
        }),
      });
      await refreshRegistries();
      setModalOpen(false);
      onFlash(formatMsg(locale, 'runners.savedSshServer', { name: saved.name }));
      if (deployAfterSave && saved?.id) {
        void runTargetAction(saved, 'deploy');
      }
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const runTargetAction = async (target: SSHTargetRecord, action: 'test' | 'deploy' | 'stop') => {
    setBusyById((prev) => ({ ...prev, [target.id]: action }));
    try {
      const res = await apiFetch<{ ok?: boolean; error?: string; log?: string; os?: string; arch?: string }>(
        `/api/v1/ssh/targets/${encodeURIComponent(target.id)}/${action}`,
        { method: 'POST' },
      );
      await refreshRegistries();
      if (action === 'deploy' || action === 'stop') {
        window.setTimeout(() => void refreshInitialData(), 1500);
      }
      setExpandedLogs((prev) => ({ ...prev, [target.id]: true }));
      if (res?.ok) {
        if (action === 'test') {
          onFlash(
            formatMsg(locale, 'runners.sshOk', {
              name: target.name,
              os: res.os || '',
              arch: res.arch || '',
            }),
          );
        } else if (action === 'deploy') {
          onFlash(
            formatMsg(locale, 'runners.deployedStartedRunnerOn', { name: target.name }),
          );
        } else {
          onFlash(formatMsg(locale, 'runners.stoppedRunnerOn', { name: target.name }));
        }
      } else {
        onFlash(
          formatMsg(locale, 'runners.actionFailed', {
            action,
            error: res?.error || formatMsg(locale, 'runners.unknownError'),
          }),
        );
      }
    } catch (err) {
      onFlash(err instanceof Error ? err.message : String(err));
    } finally {
      setBusyById((prev) => {
        const next = { ...prev };
        delete next[target.id];
        return next;
      });
    }
  };

  const deleteTarget = async (id: string) => {
    await apiFetch(`/api/v1/ssh/targets/${encodeURIComponent(id)}`, { method: 'DELETE' });
    await refreshRegistries();
  };

  const [removingRunnerIds, setRemovingRunnerIds] = useState<Record<string, boolean>>({});
  const [upgradingRunnerIds, setUpgradingRunnerIds] = useState<Record<string, boolean>>({});
  const [upgradingAll, setUpgradingAll] = useState(false);

  const upgradableCount = runners.filter(
    (r) =>
      r.status === 'online' &&
      !r.is_dev &&
      (r.incompatible || r.update_status === 'update_required' || r.update_status === 'update_available'),
  ).length;

  const triggerSingleUpgrade = async (r: RunnerRecord, mode: 'when_idle' | 'immediate') => {
    setUpgradingRunnerIds((prev) => ({ ...prev, [r.id]: true }));
    try {
      const res = await triggerRunnerUpdate(r.id, { mode, force: true });
      if (res.ok) {
        onFlash(formatMsg(locale, 'runners.updateTriggered', { name: r.name || r.id }));
      } else if (res.error) {
        onFlash(res.error);
      }
    } finally {
      setUpgradingRunnerIds((prev) => {
        const next = { ...prev };
        delete next[r.id];
        return next;
      });
    }
  };

  const triggerAllUpgrades = async () => {
    setUpgradingAll(true);
    try {
      const res = await triggerAllRunnersUpdate({ mode: 'when_idle', force: false });
      if (res.ok) {
        onFlash(
          formatMsg(locale, 'runners.updateAllTriggered', {
            triggered: res.triggered ?? 0,
            skipped: res.skipped ?? 0,
          }),
        );
      } else if (res.error) {
        onFlash(res.error);
      }
    } finally {
      setUpgradingAll(false);
    }
  };

  const removeRunner = async (r: { id: string; name: string }) => {
    setRemovingRunnerIds((prev) => ({ ...prev, [r.id]: true }));
    try {
      await apiFetch(`/api/v1/runners/${encodeURIComponent(r.id)}`, { method: 'DELETE' });
      await refreshInitialData();
      onFlash(formatMsg(locale, 'runners.removedRunner', { name: r.name || r.id }));
    } catch (err) {
      onFlash(err instanceof Error ? err.message : String(err));
    } finally {
      setRemovingRunnerIds((prev) => {
        const next = { ...prev };
        delete next[r.id];
        return next;
      });
    }
  };

  const livePreview = `${form.username.trim() || 'user'}@${form.host.trim() || 'host'}:${form.port.trim() || '22'}`;

  return (
    <div className="space-y-6">
      {/* Top Action Bar & SSH Targets Section */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <SectionHeading
          title={formatMsg(locale, 'runners.sshRemoteServers', { length: sshTargets.length })}
          description={
            formatMsg(locale, 'runners.configureRemoteHostsViaSsh')
          }
        />
        <button
          type="button"
          data-testid="add-ssh-server-btn"
          onClick={openCreateModal}
          className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-lg bg-foreground px-3 text-ui-sm font-medium text-background transition-opacity hover:opacity-90"
        >
          <Plus className="size-3.5" />
          <span>{formatMsg(locale, 'runners.addSshServer')}</span>
        </button>
      </div>

      <SettingsGroupCard>
        {sshTargets.length === 0 ? (
          <div className="flex flex-col items-center justify-center gap-2 px-5 py-9 text-center">
            <Server className="size-6 text-foreground-subtlest" />
            <p className="text-ui-sm text-foreground-subtle">
              {formatMsg(locale, 'runners.noSshServersConfiguredYet')}
            </p>
          </div>
        ) : (
          sshTargets.map((t) => {
            const busy = busyById[t.id];
            const matchedRunner = runners.find(
              (r) => r.name.toLowerCase() === t.name.toLowerCase() || r.hostname?.toLowerCase() === t.host.toLowerCase(),
            );
            const isOnline = matchedRunner?.status === 'online';
            const logOpen = Boolean(expandedLogs[t.id]);

            return (
              <div key={t.id} data-testid={`ssh-target-${t.id}`} className="px-5 py-4">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <div className="min-w-0 space-y-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span
                        className={`size-2 shrink-0 rounded-full ${
                          isOnline
                            ? 'bg-success'
                            : t.last_status === 'error'
                              ? 'bg-destructive'
                              : 'bg-foreground-subtlest'
                        }`}
                      />
                      <span className="truncate text-ui-base font-medium text-foreground">{t.name}</span>
                      <span className="rounded bg-surface px-1.5 py-0.5 font-mono text-[11px] text-foreground-subtle">
                        {t.username ? `${t.username}@` : ''}
                        {t.host}:{t.port || 22}
                      </span>
                      <span className="rounded-md border border-border bg-surface px-1.5 py-0.5 text-[11px] font-medium text-foreground-subtle">
                        {t.install_mode === 'remote_install'
                          ? formatMsg(locale, 'runners.remoteInstall')
                          : formatMsg(locale, 'runners.scpUpload')}
                      </span>
                      {t.ssh_alias && (
                        <span className="inline-flex items-center gap-1 rounded-md bg-brand-blue/10 px-1.5 py-0.5 font-mono text-[11px] text-brand-blue">
                          <Link2 className="size-3" />
                          {t.ssh_alias}
                        </span>
                      )}
                      {isOnline && (
                        <span className="rounded-md bg-success-surface px-1.5 py-0.5 text-[11px] font-medium text-success-surface-fg">
                          {formatMsg(locale, 'runners.runnerOnline')}
                        </span>
                      )}
                      {matchedRunner?.version && (
                        <span
                          data-testid={`ssh-target-version-${t.id}`}
                          className="rounded bg-surface px-1.5 py-0.5 font-mono text-[11px] text-foreground-subtle"
                        >
                          {matchedRunner.version.startsWith('v')
                            ? matchedRunner.version
                            : formatMsg(locale, 'runners.versionLabel', { version: matchedRunner.version })}
                        </span>
                      )}
                    </div>
                    {t.last_error ? (
                      <p className="truncate text-ui-xs text-destructive">{t.last_error}</p>
                    ) : t.workspace_path ? (
                      <p className="truncate font-mono text-ui-xs text-foreground-subtlest">
                        {t.workspace_path}
                      </p>
                    ) : null}
                  </div>

                  <div className="flex shrink-0 flex-wrap items-center gap-1.5">
                    <button
                      type="button"
                      data-testid={`ssh-test-${t.id}`}
                      disabled={Boolean(busy)}
                      onClick={() => void runTargetAction(t, 'test')}
                      className="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-card px-2.5 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover disabled:opacity-50"
                    >
                      {busy === 'test' ? (
                        <LoaderCircle className="size-3 animate-spin" />
                      ) : (
                        <Wifi className="size-3" />
                      )}
                      <span>{formatMsg(locale, 'runners.test')}</span>
                    </button>
                    <button
                      type="button"
                      data-testid={`ssh-deploy-${t.id}`}
                      disabled={Boolean(busy)}
                      onClick={() => void runTargetAction(t, 'deploy')}
                      className="inline-flex h-7 items-center gap-1 rounded-md bg-foreground px-2.5 text-ui-xs font-medium text-background transition-opacity hover:opacity-90 disabled:opacity-50"
                    >
                      {busy === 'deploy' ? (
                        <LoaderCircle className="size-3 animate-spin" />
                      ) : t.install_mode === 'remote_install' ? (
                        <CloudDownload className="size-3" />
                      ) : (
                        <Play className="size-3" />
                      )}
                      <span>
                        {busy === 'deploy'
                          ? formatMsg(locale, 'runners.deploying')
                          : t.install_mode === 'remote_install'
                            ? formatMsg(locale, 'runners.remoteInstall')
                            : formatMsg(locale, 'runners.scpDeploy')}
                      </span>
                    </button>
                    {isOnline && (
                      <button
                        type="button"
                        disabled={Boolean(busy)}
                        onClick={() => void runTargetAction(t, 'stop')}
                        title={formatMsg(locale, 'runners.stopRemoteRunner')}
                        className="flex size-7 items-center justify-center rounded-md border border-border text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground disabled:opacity-50"
                      >
                        {busy === 'stop' ? <LoaderCircle className="size-3 animate-spin" /> : <Square className="size-3" />}
                      </button>
                    )}
                    {t.last_log && (
                      <button
                        type="button"
                        onClick={() => setExpandedLogs((p) => ({ ...p, [t.id]: !p[t.id] }))}
                        title={formatMsg(locale, 'runners.toggleLogs')}
                        className={`flex size-7 items-center justify-center rounded-md transition-colors hover:bg-hover ${
                          logOpen ? 'bg-selected text-foreground' : 'text-foreground-subtlest hover:text-foreground'
                        }`}
                      >
                        <Terminal className="size-3.5" />
                      </button>
                    )}
                    <button
                      type="button"
                      onClick={() => openEditModal(t)}
                      aria-label={formatMsg(locale, 'runners.editSshServer')}
                      className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
                    >
                      <Pencil className="size-3.5" />
                    </button>
                    <button
                      type="button"
                      onClick={() => void deleteTarget(t.id)}
                      aria-label={formatMsg(locale, 'runners.deleteSshServer')}
                      className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-destructive-surface hover:text-destructive-surface-fg"
                    >
                      <Trash2 className="size-3.5" />
                    </button>
                  </div>
                </div>

                {logOpen && t.last_log && (
                  <pre className="mt-3 max-h-56 overflow-auto rounded-lg border border-border bg-surface p-3 font-mono text-[11px] leading-relaxed text-foreground-subtle whitespace-pre-wrap">
                    {t.last_log}
                  </pre>
                )}
              </div>
            );
          })
        )}
      </SettingsGroupCard>

      {/* Connected Runners */}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <SectionHeading
          title={formatMsg(locale, 'runners.connectedRunners', { length: runners.length })}
          description={
            formatMsg(locale, 'runners.liveReverseConnectedRunnersPlatform')
          }
        />
        <div className="flex items-center gap-2">
          {upgradableCount > 0 && (
            <button
              type="button"
              data-testid="update-all-runners-btn"
              disabled={upgradingAll}
              onClick={() => void triggerAllUpgrades()}
              className="inline-flex h-7 items-center gap-1 rounded-md border border-brand-blue/30 bg-brand-blue/10 px-2.5 text-ui-xs font-medium text-brand-blue transition-colors hover:bg-brand-blue/20 disabled:opacity-50"
            >
              {upgradingAll ? <LoaderCircle className="size-3 animate-spin" /> : <ArrowUpCircle className="size-3" />}
              <span>
                {upgradingAll
                  ? formatMsg(locale, 'runners.updatingAll')
                  : formatMsg(locale, 'runners.updateAllBtn', { count: upgradableCount })}
              </span>
            </button>
          )}
          <button
            type="button"
            onClick={() => void refreshInitialData()}
            className="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-card px-2.5 text-ui-xs text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
          >
            <RefreshCw className="size-3" />
            <span>{formatMsg(locale, 'common.refresh')}</span>
          </button>
        </div>
      </div>

      <SettingsGroupCard>
        {runners.length === 0 ? (
          <div className="px-5 py-8 text-center text-ui-sm text-foreground-subtlest">
            {formatMsg(locale, 'runners.noRunnersConnectedYet')}
          </div>
        ) : (
          runners.map((r) => {
            const removing = Boolean(removingRunnerIds[r.id]);
            const upgrading = Boolean(upgradingRunnerIds[r.id]);
            const formattedVersion = r.version
              ? r.version.startsWith('v')
                ? r.version
                : formatMsg(locale, 'runners.versionLabel', { version: r.version })
              : '';
            const isRequired = Boolean(r.incompatible || r.update_status === 'update_required');
            const isAvailable = !isRequired && r.update_status === 'update_available';
            const phaseLabel = formatRunnerUpdatePhase(locale, r.update_phase, r.update_progress);
            const activeUpgradePhase = Boolean(
              r.update_phase && !['idle', 'succeeded', 'failed', 'rolled_back'].includes(r.update_phase),
            );
            return (
              <div
                key={r.id}
                data-testid={`connected-runner-${r.id}`}
                className="grid grid-cols-1 items-center gap-3 px-5 py-4 sm:grid-cols-[minmax(0,1fr)_auto]"
              >
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <span
                      className={`size-2 shrink-0 rounded-full ${
                        r.status === 'online' ? 'bg-success' : 'bg-foreground-subtlest'
                      }`}
                    />
                    <span className="truncate text-ui-base font-medium text-foreground">{r.name}</span>
                    <span
                      className={`rounded-md px-1.5 py-0.5 text-ui-xs font-medium ${
                        r.status === 'online'
                          ? 'bg-success-surface text-success-surface-fg'
                          : 'bg-tag text-foreground-subtle'
                      }`}
                    >
                      {r.status}
                    </span>
                    {formattedVersion && (
                      <span
                        data-testid={`runner-version-${r.id}`}
                        className="rounded-md border border-border bg-surface px-1.5 py-0.5 font-mono text-ui-xs text-foreground-subtle"
                      >
                        {formattedVersion}
                      </span>
                    )}
                    {r.is_dev && (
                      <span
                        data-testid={`runner-dev-badge-${r.id}`}
                        title={formatMsg(locale, 'runners.devAutoUpdateDisabled')}
                        className="rounded-md border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 font-mono text-ui-xs font-medium text-amber-600 dark:text-amber-400"
                      >
                        {formatMsg(locale, 'runners.devBadge')}
                      </span>
                    )}
                    {r.elevated && (
                      <span
                        data-testid={`runner-admin-badge-${r.id}`}
                        className="inline-flex items-center gap-1 rounded-md border border-brand-blue/30 bg-brand-blue/10 px-1.5 py-0.5 text-ui-xs font-medium text-brand-blue"
                      >
                        <Shield className="size-3" />
                        <span>{formatMsg(locale, 'runners.adminBadge')}</span>
                      </span>
                    )}
                    {phaseLabel && (
                      <span
                        data-testid={`runner-update-phase-${r.id}`}
                        className={`inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-ui-xs font-medium ${
                          r.update_phase === 'failed' || r.update_phase === 'rolled_back'
                            ? 'bg-destructive-surface text-destructive-surface-fg'
                            : r.update_phase === 'succeeded'
                              ? 'bg-success-surface text-success-surface-fg'
                              : 'bg-brand-blue/10 text-brand-blue'
                        }`}
                      >
                        {activeUpgradePhase && <LoaderCircle className="size-3 animate-spin" />}
                        <span>{phaseLabel}</span>
                      </span>
                    )}
                  </div>
                  <div className="mt-1 flex flex-wrap items-center gap-1.5 text-ui-xs text-foreground-subtle">
                    <span className="rounded bg-surface px-1.5 py-0.5 font-mono">{r.os}</span>
                    <span className="rounded bg-surface px-1.5 py-0.5 font-mono">{r.arch}</span>
                    <span className="rounded bg-surface px-1.5 py-0.5 font-mono">{r.shell || 'default'}</span>
                    {r.hostname ? <span className="font-mono">{r.hostname}</span> : null}
                    {r.workspace_default ? <span className="truncate font-mono">{r.workspace_default}</span> : null}
                  </div>
                  {r.status === 'online' && isRequired && (
                    <div
                      data-testid={`runner-update-required-${r.id}`}
                      className="mt-2 flex flex-wrap items-center justify-between gap-2 rounded-lg border border-destructive/30 bg-destructive-surface/60 px-3 py-2 text-ui-xs text-destructive-surface-fg"
                    >
                      <div className="flex min-w-0 items-center gap-1.5">
                        <AlertTriangle className="size-3.5 shrink-0" />
                        <span className="font-medium">
                          {formatMsg(locale, 'runners.updateRequired', { version: r.target_version || 'latest' })}
                        </span>
                        {r.update_reason && <span className="truncate opacity-85">— {r.update_reason}</span>}
                      </div>
                      <button
                        type="button"
                        data-testid={`upgrade-runner-${r.id}`}
                        disabled={upgrading || activeUpgradePhase}
                        onClick={() => void triggerSingleUpgrade(r, 'immediate')}
                        className="inline-flex h-6 shrink-0 items-center gap-1 rounded-md bg-destructive px-2 text-ui-xs font-medium text-white transition-opacity hover:opacity-90 disabled:opacity-50"
                      >
                        {upgrading ? <LoaderCircle className="size-3 animate-spin" /> : <ArrowUpCircle className="size-3" />}
                        <span>{formatMsg(locale, 'runners.updateNowBtn')}</span>
                      </button>
                    </div>
                  )}
                  {r.status === 'online' && isAvailable && (
                    <div
                      data-testid={`runner-update-available-${r.id}`}
                      className="mt-2 flex flex-wrap items-center justify-between gap-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-1.5 text-ui-xs text-amber-700 dark:text-amber-300"
                    >
                      <div className="flex min-w-0 items-center gap-1.5">
                        <ArrowUpCircle className="size-3.5 shrink-0" />
                        <span className="font-medium">
                          {formatMsg(locale, 'runners.updateAvailable', { version: r.target_version || 'latest' })}
                        </span>
                        {r.is_dev && (
                          <span className="truncate opacity-80">({formatMsg(locale, 'runners.devAutoUpdateDisabled')})</span>
                        )}
                      </div>
                      <button
                        type="button"
                        data-testid={`upgrade-runner-${r.id}`}
                        disabled={upgrading || activeUpgradePhase}
                        onClick={() => void triggerSingleUpgrade(r, 'when_idle')}
                        className="inline-flex h-6 shrink-0 items-center gap-1 rounded-md border border-amber-500/40 bg-card px-2 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover disabled:opacity-50"
                      >
                        {upgrading ? <LoaderCircle className="size-3 animate-spin" /> : <ArrowUpCircle className="size-3" />}
                        <span>{formatMsg(locale, 'runners.updateBtn')}</span>
                      </button>
                    </div>
                  )}
                  {r.update_error && (
                    <p className="mt-1 truncate text-ui-xs text-destructive">{r.update_error}</p>
                  )}
                </div>
                <div className="flex items-center justify-end gap-2">
                  <span className="font-mono text-ui-xs tabular-nums text-foreground-subtlest">
                    rev #{r.synced_config_rev}
                  </span>
                  <button
                    type="button"
                    data-testid={`remove-runner-${r.id}`}
                    disabled={removing}
                    title={formatMsg(locale, 'runners.removeRunner')}
                    aria-label={formatMsg(locale, 'runners.removeRunner')}
                    onClick={() => void removeRunner(r)}
                    className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-destructive-surface hover:text-destructive-surface-fg disabled:opacity-50"
                  >
                    {removing ? (
                      <LoaderCircle className="size-3.5 animate-spin" />
                    ) : (
                      <Trash2 className="size-3.5" />
                    )}
                  </button>
                </div>
              </div>
            );
          })
        )}
      </SettingsGroupCard>

      {/* Collapsible Manual Join Command */}
      <SettingsGroupCard>
        <button
          type="button"
          onClick={() => setManualOpen((v) => !v)}
          className="flex w-full items-center justify-between px-5 py-3.5 text-left transition-colors hover:bg-hover/40"
        >
          <span className="flex items-center gap-2 text-ui-sm font-medium text-foreground">
            {manualOpen ? <ChevronDown className="size-4 text-foreground-subtlest" /> : <ChevronRight className="size-4 text-foreground-subtlest" />}
            <span>{formatMsg(locale, 'runners.manualRunnerJoinCommand')}</span>
          </span>
          <span className="text-ui-xs text-foreground-subtlest">
            {formatMsg(locale, 'runners.forNatFirewalledHosts')}
          </span>
        </button>
        {manualOpen && (
          <div className="border-t border-border px-5 py-4">
            <div className="flex items-center justify-between gap-2 rounded-lg border border-border bg-surface px-3 py-2">
              <code className="truncate font-mono text-ui-sm text-foreground">{joinCmd}</code>
              <button
                type="button"
                onClick={() => {
                  void navigator.clipboard?.writeText(joinCmd);
                  setCopied(true);
                  setTimeout(() => setCopied(false), 1500);
                }}
                className="flex shrink-0 items-center gap-1 rounded-md border border-border bg-card px-2.5 py-1 text-ui-xs text-foreground transition-colors hover:bg-hover"
              >
                {copied ? <Check className="size-3 text-success" /> : <Copy className="size-3" />}
                <span>{copied ? (formatMsg(locale, 'common.copied')) : formatMsg(locale, 'runners.copyCommand')}</span>
              </button>
            </div>
          </div>
        )}
      </SettingsGroupCard>

      {/* Nex-Aligned New / Edit SSH Connection Modal */}
      {modalOpen && (
        <div
          data-testid="ssh-server-modal-backdrop"
          onMouseDown={(e) => {
            if (e.target === e.currentTarget) setModalOpen(false);
          }}
          className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 px-4 pt-[8vh] pb-8 backdrop-blur-[2px]"
        >
          <form
            data-testid="ssh-server-modal"
            onSubmit={(e) => void submitForm(e, false)}
            className="w-full max-w-[540px] overflow-hidden rounded-2xl border border-border bg-popover text-foreground shadow-2xl"
          >
            {/* Header + Live Target Preview */}
            <div className="flex items-start justify-between gap-3 border-b border-border px-5 py-4">
              <div className="min-w-0">
                <h2 className="text-ui-lg font-semibold text-foreground">
                  {form.id
                    ? formatMsg(locale, 'runners.editSshConnection')
                    : formatMsg(locale, 'runners.newSshConnection')}
                </h2>
                <div className="mt-0.5 truncate font-mono text-ui-xs text-foreground-subtlest">
                  {livePreview}
                </div>
              </div>
              <button
                type="button"
                onClick={() => setModalOpen(false)}
                className="flex size-7 items-center justify-center rounded-lg text-foreground-subtlest hover:bg-hover hover:text-foreground"
              >
                <X className="size-4" />
              </button>
            </div>

            <div className="max-h-[70vh] space-y-4 overflow-y-auto px-5 py-4">
              {/* Top row: Import from ~/.ssh/config (Nex pattern) */}
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-2">
                  <span className="inline-flex items-center gap-1.5 rounded-lg border border-brand-blue/30 bg-brand-blue/10 px-2.5 py-1 text-ui-xs font-medium text-brand-blue">
                    <Server className="size-3.5" />
                    SSH
                  </span>
                  {form.ssh_alias && (
                    <span className="inline-flex items-center gap-1 rounded-lg border border-brand-blue/25 bg-brand-blue/8 px-2 py-1 font-mono text-ui-xs text-brand-blue">
                      <Link2 className="size-3" />
                      <span>
                        {formatMsg(locale, 'runners.linked', { ssh_alias: form.ssh_alias })}
                      </span>
                      <button
                        type="button"
                        onClick={() => setForm((p) => ({ ...p, ssh_alias: '' }))}
                        className="ml-0.5 rounded hover:bg-brand-blue/20"
                      >
                        <X className="size-3" />
                      </button>
                    </span>
                  )}
                </div>

                <div className="relative">
                  <button
                    type="button"
                    data-testid="ssh-import-config-btn"
                    onClick={() => {
                      setConfigMenuOpen((v) => !v);
                      if (configHosts.length === 0) void loadSSHConfigHosts();
                    }}
                    className="inline-flex h-7 items-center gap-1 rounded-lg border border-border bg-surface px-2.5 text-ui-xs font-medium text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
                  >
                    <FileCode2 className="size-3.5" />
                    <span>{formatMsg(locale, 'runners.importFromSshConfig')}</span>
                    <ChevronDown className="size-3" />
                  </button>

                  {configMenuOpen && (
                    <div
                      data-testid="ssh-config-dropdown"
                      className="absolute right-0 top-8 z-50 max-h-56 w-64 overflow-y-auto rounded-xl border border-border bg-popover p-1 shadow-xl"
                    >
                      {configLoading ? (
                        <div className="flex items-center gap-2 px-3 py-2 text-ui-xs text-foreground-subtlest">
                          <LoaderCircle className="size-3.5 animate-spin" />
                          <span>{formatMsg(locale, 'runners.readingSshConfig')}</span>
                        </div>
                      ) : configHosts.length === 0 ? (
                        <div className="px-3 py-2 text-ui-xs text-foreground-subtlest">
                          {formatMsg(locale, 'runners.noConcreteHostEntriesFound')}
                        </div>
                      ) : (
                        configHosts.map((h) => (
                          <button
                            key={h.alias}
                            type="button"
                            data-testid={`ssh-config-host-${h.alias}`}
                            onClick={() => applyConfigHost(h)}
                            className="flex w-full flex-col rounded-lg px-2.5 py-1.5 text-left transition-colors hover:bg-menu-hover"
                          >
                            <span className="text-ui-sm font-medium text-foreground">{h.alias}</span>
                            <span className="truncate font-mono text-[11px] text-foreground-subtlest">
                              {h.user ? `${h.user}@` : ''}
                              {h.hostname}:{h.port}
                            </span>
                          </button>
                        ))
                      )}
                    </div>
                  )}
                </div>
              </div>

              {formError && (
                <div className="rounded-lg border border-destructive/30 bg-destructive-surface px-3 py-2 text-ui-xs text-destructive-surface-fg">
                  {formError}
                </div>
              )}

              {/* Host + Port side-by-side (Nex RDUX-27 layout) */}
              <div className="grid grid-cols-[minmax(0,1fr)_6rem] gap-3">
                <label className="block space-y-1">
                  <span className="text-ui-sm text-foreground-subtle">{formatMsg(locale, 'runners.host')}</span>
                  <input
                    autoFocus
                    data-testid="ssh-host-input"
                    value={form.host}
                    onPaste={handleHostPaste}
                    onChange={(e) => setForm((p) => ({ ...p, host: e.target.value, ssh_alias: '' }))}
                    placeholder={formatMsg(locale, 'runners.192168110Or')}
                    className={settingsMonoInputClass}
                  />
                  {pasteNotice && <p className="text-ui-xs text-success">{pasteNotice}</p>}
                </label>
                <label className="block space-y-1">
                  <span className="text-ui-sm text-foreground-subtle">{formatMsg(locale, 'runners.port')}</span>
                  <input
                    data-testid="ssh-port-input"
                    value={form.port}
                    onChange={(e) => setForm((p) => ({ ...p, port: e.target.value }))}
                    className={settingsMonoInputClass}
                  />
                </label>
              </div>

              {/* Name + Username */}
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <label className="block space-y-1">
                  <span className="text-ui-sm text-foreground-subtle">{formatMsg(locale, 'runners.username')}</span>
                  <input
                    data-testid="ssh-username-input"
                    value={form.username}
                    onChange={(e) => setForm((p) => ({ ...p, username: e.target.value }))}
                    placeholder="root"
                    className={settingsMonoInputClass}
                  />
                </label>
                <label className="block space-y-1">
                  <span className="text-ui-sm text-foreground-subtle">{formatMsg(locale, 'runners.displayName')}</span>
                  <input
                    data-testid="ssh-name-input"
                    value={form.name}
                    onChange={(e) => setForm((p) => ({ ...p, name: e.target.value }))}
                    placeholder={formatMsg(locale, 'runners.eGBuildServer')}
                    className={settingsInputClass}
                  />
                </label>
              </div>

              {/* Auth method segmented control */}
              <div className="space-y-2">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-ui-sm text-foreground-subtle">{formatMsg(locale, 'runners.authentication')}</span>
                  <div className="inline-flex rounded-lg border border-border bg-surface p-0.5 text-ui-xs">
                    {(
                      [
                        { id: 'key', labelKey: 'runners.auth.key' },
                        { id: 'agent', labelKey: 'runners.auth.agent' },
                        { id: 'password', labelKey: 'runners.auth.password' },
                      ] as const
                    ).map((opt) => (
                      <button
                        key={opt.id}
                        type="button"
                        onClick={() => setForm((p) => ({ ...p, auth_method: opt.id }))}
                        className={`rounded-md px-2.5 py-1 font-medium transition-colors ${
                          form.auth_method === opt.id
                            ? 'bg-popover text-foreground shadow-xs'
                            : 'text-foreground-subtle hover:text-foreground'
                        }`}
                      >
                        {formatMsg(locale, opt.labelKey)}
                      </button>
                    ))}
                  </div>
                </div>

                {form.auth_method === 'key' && (
                  <div className="relative">
                    <KeyRound className="pointer-events-none absolute left-3 top-1/2 size-3.5 -translate-y-1/2 text-foreground-subtlest" />
                    <input
                      value={form.key_path}
                      onChange={(e) => setForm((p) => ({ ...p, key_path: e.target.value }))}
                      placeholder="~/.ssh/id_ed25519"
                      className={`${settingsMonoInputClass} pl-8`}
                    />
                  </div>
                )}

                {form.auth_method === 'password' && (
                  <div className="relative">
                    <input
                      type={showPwd ? 'text' : 'password'}
                      value={form.password}
                      onChange={(e) => setForm((p) => ({ ...p, password: e.target.value }))}
                      placeholder={
                        form.has_password
                          ? formatMsg(locale, 'runners.leaveBlankToKeepSaved')
                          : formatMsg(locale, 'runners.enterSshPassword')
                      }
                      className={`${settingsInputClass} pr-9`}
                    />
                    <button
                      type="button"
                      onClick={() => setShowPwd((v) => !v)}
                      className="absolute right-2 top-1/2 flex size-6 -translate-y-1/2 items-center justify-center rounded text-foreground-subtlest hover:text-foreground"
                    >
                      {showPwd ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}
                    </button>
                  </div>
                )}
              </div>

              {/* Deployment Mode: SCP Upload vs Remote Install */}
              <div className="space-y-2.5 rounded-xl border border-border bg-surface/50 p-3.5">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-ui-sm font-medium text-foreground">
                    {formatMsg(locale, 'runners.runnerDeploymentMode')}
                  </span>
                  <div className="inline-flex rounded-lg border border-border bg-card p-0.5 text-ui-xs">
                    {(
                      [
                        { id: 'scp', labelKey: 'runners.deployMode.scp' },
                        { id: 'remote_install', labelKey: 'runners.deployMode.remoteInstall' },
                      ] as const
                    ).map((m) => (
                      <button
                        key={m.id}
                        type="button"
                        data-testid={`ssh-mode-${m.id}`}
                        onClick={() => setForm((p) => ({ ...p, install_mode: m.id }))}
                        className={`rounded-md px-2.5 py-1 font-medium transition-colors ${
                          form.install_mode === m.id
                            ? 'bg-selected text-foreground shadow-xs'
                            : 'text-foreground-subtle hover:text-foreground'
                        }`}
                      >
                        {formatMsg(locale, m.labelKey)}
                      </button>
                    ))}
                  </div>
                </div>

                {form.install_mode === 'scp' ? (
                  <div className="space-y-2">
                    <p className="text-ui-xs leading-relaxed text-foreground-subtle">
                      {formatMsg(locale, 'runners.probesRemoteOsArchAuto')}
                    </p>
                    {stagedBinaries.length > 0 && (
                      <div className="flex flex-wrap items-center gap-1.5 pt-0.5">
                        <span className="text-[11px] text-foreground-subtlest">
                          {formatMsg(locale, 'runners.bundledRunners')}
                        </span>
                        {stagedBinaries.map((b) => (
                          <span
                            key={`${b.os}/${b.arch}`}
                            title={formatBytesShort(b.size_bytes)}
                            className="inline-flex items-center gap-1 rounded-md border border-border bg-card px-1.5 py-0.5 font-mono text-[11px] text-foreground-subtle"
                          >
                            {b.os}/{b.arch}
                          </span>
                        ))}
                      </div>
                    )}
                  </div>
                ) : (
                  <div className="space-y-2">
                    <p className="text-ui-xs leading-relaxed text-foreground-subtle">
                      {formatMsg(locale, 'runners.runsCurlWgetOnThe')}
                    </p>
                    <input
                      data-testid="ssh-download-url-input"
                      value={form.download_url}
                      onChange={(e) => setForm((p) => ({ ...p, download_url: e.target.value }))}
                      placeholder={
                        formatMsg(locale, 'runners.optionalHttpsReleasesExampleCom')
                      }
                      className={settingsMonoInputClass}
                    />
                  </div>
                )}
              </div>

              {/* Collapsible Advanced Options */}
              <div>
                <button
                  type="button"
                  data-testid="ssh-advanced-toggle"
                  onClick={() => setAdvancedOpen((v) => !v)}
                  className="inline-flex items-center gap-1 text-ui-xs font-medium text-foreground-subtle transition-colors hover:text-foreground"
                >
                  {advancedOpen ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
                  <span>{formatMsg(locale, 'runners.advancedOptions')}</span>
                </button>
                {advancedOpen && (
                  <div className="mt-2.5 grid grid-cols-1 gap-3 sm:grid-cols-2">
                    <label className="block space-y-1">
                      <span className="text-ui-xs text-foreground-subtle">
                        {formatMsg(locale, 'runners.defaultRemoteWorkspace')}
                      </span>
                      <input
                        data-testid="ssh-workspace-input"
                        value={form.workspace_path}
                        onChange={(e) => setForm((p) => ({ ...p, workspace_path: e.target.value }))}
                        placeholder="$HOME"
                        className={settingsMonoInputClass}
                      />
                      <span className="block text-[11px] text-foreground-subtlest">
                        {formatMsg(locale, 'runners.defaultWorkspaceHint')}
                      </span>
                    </label>
                    <label className="block space-y-1">
                      <span className="text-ui-xs text-foreground-subtle">
                        {formatMsg(locale, 'runners.remoteInstallDirectory')}
                      </span>
                      <input
                        value={form.install_dir}
                        onChange={(e) => setForm((p) => ({ ...p, install_dir: e.target.value }))}
                        placeholder="~/.nex-runner"
                        className={settingsMonoInputClass}
                      />
                    </label>
                    <label className="block space-y-1 sm:col-span-2">
                      <span className="text-ui-xs text-foreground-subtle">
                        {formatMsg(locale, 'runners.serverWebsocketUrlOverride')}
                      </span>
                      <input
                        value={form.server_ws_url}
                        onChange={(e) => setForm((p) => ({ ...p, server_ws_url: e.target.value }))}
                        placeholder={`${wsProto}://${wsHost}/ws/runner`}
                        className={settingsMonoInputClass}
                      />
                    </label>
                  </div>
                )}
              </div>
            </div>

            {/* Footer */}
            <div className="flex items-center justify-end gap-2 border-t border-border bg-surface/40 px-5 py-3.5">
              <button
                type="button"
                onClick={() => setModalOpen(false)}
                className="h-8 rounded-lg border border-border bg-card px-3 text-ui-sm text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
              >
                {formatMsg(locale, 'common.cancel')}
              </button>
              <button
                type="submit"
                data-testid="ssh-save-btn"
                disabled={saving}
                className="h-8 rounded-lg border border-border bg-card px-3 text-ui-sm font-medium text-foreground transition-colors hover:bg-hover disabled:opacity-50"
              >
                {formatMsg(locale, 'common.save2')}
              </button>
              <button
                type="button"
                data-testid="ssh-save-deploy-btn"
                disabled={saving}
                onClick={(e) => void submitForm(e, true)}
                className="inline-flex h-8 items-center gap-1.5 rounded-lg bg-foreground px-3.5 text-ui-sm font-medium text-background transition-opacity hover:opacity-90 disabled:opacity-50"
              >
                {saving && <LoaderCircle className="size-3.5 animate-spin" />}
                <span>{formatMsg(locale, 'runners.saveDeploy')}</span>
              </button>
            </div>
          </form>
        </div>
      )}
    </div>
  );
}
