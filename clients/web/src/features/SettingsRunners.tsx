import { formatMsg } from '../i18n/messages';
import type { LocaleCode } from '../i18n';
import {
  AlertTriangle,
  ArrowRight,
  ArrowUpCircle,
  Check,
  ChevronDown,
  ChevronRight,
  CloudDownload,
  Copy,
  Ellipsis,
  ExternalLink,
  Eye,
  EyeOff,
  FileCode2,
  FolderOpen,
  HardDrive,
  KeyRound,
  Link2,
  LoaderCircle,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Server,
  Shield,
  ShieldCheck,
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

/** Mirrors Go's `sshTargetRunnerID(name)` so renamed runners stay linked to their SSH target. */
export function sshTargetRunnerId(name: string): string {
  const slug = name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9_-]/g, '_')
    .replace(/^_+|_+$/g, '');
  return `run_${slug || 'remote'}`;
}

/** Formats a runner or target version cleanly as `v0.4.0(49019b2)` when build metadata or SHA is present. */
export function formatRunnerVersion(version?: string, buildSha?: string): string {
  const rawVer = (version || '').trim();
  const rawSha = (buildSha || '').trim();
  const shortSha = rawSha && rawSha.toLowerCase() !== 'unknown' ? rawSha.slice(0, 7) : '';

  if (!rawVer) {
    return shortSha;
  }

  const plusIdx = rawVer.indexOf('+');
  if (plusIdx > 0) {
    const baseRaw = rawVer.slice(0, plusIdx).trim();
    const metaSha = rawVer.slice(plusIdx + 1).trim().slice(0, 7);
    const base = baseRaw.toLowerCase() === 'dev'
      ? 'dev'
      : baseRaw.startsWith('v') || baseRaw.startsWith('V')
        ? `v${baseRaw.slice(1)}`
        : `v${baseRaw}`;
    return metaSha ? `${base}(${metaSha})` : base;
  }

  if (rawVer.toLowerCase() === 'dev') {
    return shortSha ? `dev(${shortSha})` : 'dev';
  }

  if (/^[0-9a-f]{7,40}$/i.test(rawVer)) {
    return rawVer.slice(0, 7);
  }

  const base = rawVer.startsWith('v') || rawVer.startsWith('V') ? `v${rawVer.slice(1)}` : `v${rawVer}`;
  if (shortSha && !base.includes(shortSha)) {
    return `${base}(${shortSha})`;
  }
  return base;
}

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
  const { locale, runners, sshTargets, refreshInitialData, refreshRegistries, renameRunner, triggerRunnerUpdate, triggerAllRunnersUpdate } = useNexStore();

  const updateRunnerPolicy = async (
    runnerId: string,
    patch: { auto_update?: boolean; update_mode?: 'self_update' | 'scp' },
  ) => {
    const updated = await apiFetch<RunnerRecord>(`/api/v1/runners/${encodeURIComponent(runnerId)}`, {
      method: 'PATCH',
      body: JSON.stringify(patch),
    });
    useNexStore.setState((prev) => ({
      runners: prev.runners.map((item) => (item.id === runnerId ? { ...item, ...patch, ...updated } : item)),
    }));
  };

  const [copied, setCopied] = useState(false);
  const [manualModalOpen, setManualModalOpen] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [sshModalMode, setSshModalMode] = useState<'ssh' | 'runner_only'>('ssh');
  const [editingRunner, setEditingRunner] = useState<RunnerRecord | null>(null);
  const [autoUpdateDraft, setAutoUpdateDraft] = useState(true);
  const [form, setForm] = useState<SSHFormState>(EMPTY_FORM);
  const [showPwd, setShowPwd] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [pasteNotice, setPasteNotice] = useState('');
  const [formError, setFormError] = useState('');
  const [deployLog, setDeployLog] = useState('');
  const [saving, setSaving] = useState(false);

  const [configHosts, setConfigHosts] = useState<SSHConfigHost[]>([]);
  const [configMenuOpen, setConfigMenuOpen] = useState(false);
  const [configLoading, setConfigLoading] = useState(false);

  const [stagedBinaries, setStagedBinaries] = useState<StagedBinaryInfo[]>([]);

  const [busyById, setBusyById] = useState<Record<string, 'test' | 'deploy' | 'stop'>>({});
  const [expandedLogs, setExpandedLogs] = useState<Record<string, boolean>>({});

  const [runnerAuth, setRunnerAuth] = useState<{
    pairing_secret: string;
    require_auth: boolean;
    env_managed: boolean;
    rotated_at?: number;
  } | null>(null);
  const [showPairingSecret, setShowPairingSecret] = useState(false);
  const [authBusy, setAuthBusy] = useState(false);
  const [relayPairCmd, setRelayPairCmd] = useState('');
  const [relayPairLoading, setRelayPairLoading] = useState(false);
  const [relayCopied, setRelayCopied] = useState(false);
  const [fdaRunnerId, setFdaRunnerId] = useState<string | null>(null);
  const [fdaBusyAction, setFdaBusyAction] = useState<string | null>(null);
  const fdaRunner = fdaRunnerId ? runners.find((item) => item.id === fdaRunnerId) || null : null;

  const triggerRunnerPermission = async (
    runner: RunnerRecord,
    action: 'check' | 'open_settings' | 'reveal_app' | 'open_guide',
  ) => {
    setFdaBusyAction(action);
    try {
      const res = await apiFetch<{
        ok?: boolean;
        full_disk_access?: boolean;
        app_path?: string;
        error?: string;
      }>(`/api/v1/runners/${encodeURIComponent(runner.id)}/permissions`, {
        method: 'POST',
        body: JSON.stringify({ action }),
      });
      if (mountedRef.current && res) {
        useNexStore.setState((prev) => ({
          runners: prev.runners.map((item) =>
            item.id === runner.id
              ? {
                  ...item,
                  full_disk_access:
                    typeof res.full_disk_access === 'boolean' ? res.full_disk_access : item.full_disk_access,
                  app_path: res.app_path || item.app_path,
                }
              : item,
          ),
        }));
        if (res.error) {
          onFlash(formatMsg(locale, 'runners.fdaActionFailed', { error: res.error }));
        } else if (action === 'check') {
          if (res.full_disk_access) {
            onFlash(formatMsg(locale, 'runners.fdaGrantedToast', { name: runner.name }));
          } else {
            onFlash(formatMsg(locale, 'runners.fdaStillMissingToast', { name: runner.name }));
          }
        } else if (action === 'open_guide') {
          onFlash(formatMsg(locale, 'runners.fdaGuideOpened', { name: runner.name }));
        }
      }
    } catch (err) {
      if (mountedRef.current) {
        onFlash(formatMsg(locale, 'runners.fdaActionFailed', { error: err instanceof Error ? err.message : String(err) }));
      }
    } finally {
      if (mountedRef.current) setFdaBusyAction(null);
    }
  };

  const httpOrigin =
    typeof window !== 'undefined' && window.location.origin ? window.location.origin : 'http://127.0.0.1:8080';
  const wsProto = typeof window !== 'undefined' && window.location.protocol === 'https:' ? 'wss' : 'ws';
  const wsHost = typeof window !== 'undefined' ? window.location.host : '127.0.0.1:8080';
  const tokenSnippet =
    runnerAuth?.require_auth && runnerAuth?.pairing_secret ? ` -token "${runnerAuth.pairing_secret}"` : '';
  const tokenQuery =
    runnerAuth?.require_auth && runnerAuth?.pairing_secret
      ? `?token=${encodeURIComponent(runnerAuth.pairing_secret)}`
      : '';
  const joinCmd = `./nex-runner -name "My-Linux-Box" -server ${wsProto}://${wsHost}/ws/runner${tokenSnippet}`;
  const winInstallCmd = `irm "${httpOrigin}/api/v1/runners/install.ps1${tokenQuery}" | iex`;
  const unixInstallCmd = `curl -fsSL "${httpOrigin}/api/v1/runners/install.sh${tokenQuery}" | sh`;
  const [copiedKey, setCopiedKey] = useState<'win' | 'unix' | null>(null);

  const loadRunnerAuth = async () => {
    try {
      const data = await apiFetch<{
        pairing_secret: string;
        require_auth: boolean;
        env_managed: boolean;
        rotated_at?: number;
      }>('/api/v1/runners/auth');
      if (mountedRef.current && data && typeof data.pairing_secret === 'string') {
        setRunnerAuth(data);
      }
    } catch {
      // ignore
    }
  };

  const toggleRequireAuth = async (next: boolean) => {
    setAuthBusy(true);
    try {
      const data = await apiFetch<{
        pairing_secret: string;
        require_auth: boolean;
        env_managed: boolean;
        rotated_at?: number;
      }>('/api/v1/runners/auth', {
        method: 'PUT',
        body: JSON.stringify({ require_auth: next }),
      });
      if (mountedRef.current && data) setRunnerAuth(data);
    } catch (err) {
      onFlash(err instanceof Error ? err.message : String(err));
    } finally {
      if (mountedRef.current) setAuthBusy(false);
    }
  };

  const rotatePairingSecret = async () => {
    setAuthBusy(true);
    try {
      const data = await apiFetch<{
        pairing_secret: string;
        require_auth: boolean;
        env_managed: boolean;
        rotated_at?: number;
      }>('/api/v1/runners/auth/rotate', { method: 'POST' });
      if (mountedRef.current && data) {
        setRunnerAuth(data);
        onFlash(formatMsg(locale, 'runners.secretRotated'));
      }
    } catch (err) {
      onFlash(err instanceof Error ? err.message : String(err));
    } finally {
      if (mountedRef.current) setAuthBusy(false);
    }
  };

  const generateRelayPairCommand = async () => {
    setRelayPairLoading(true);
    try {
      const res = await apiFetch<{ uri?: string }>('/api/v1/remote/pair/invite', { method: 'POST' });
      if (mountedRef.current && res?.uri) {
        setRelayPairCmd(`./nex-runner -name "My-Remote-Box" -pair "${res.uri}"`);
      }
    } catch (err) {
      onFlash(err instanceof Error ? err.message : String(err));
    } finally {
      if (mountedRef.current) setRelayPairLoading(false);
    }
  };

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
    void loadRunnerAuth();
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
    setSshModalMode('ssh');
    setEditingRunner(null);
    setAutoUpdateDraft(true);
    setForm(EMPTY_FORM);
    setFormError('');
    setDeployLog('');
    setPasteNotice('');
    setAdvancedOpen(false);
    setShowPwd(false);
    setConfigMenuOpen(false);
    setModalOpen(true);
    void loadSSHConfigHosts();
    void loadStagedBinaries();
  };

  const openEditModal = (t: SSHTargetRecord, linkedRunner?: RunnerRecord) => {
    const matched =
      linkedRunner ||
      runners.find(
        (r) =>
          sshTargetRunnerId(t.name) === r.id ||
          t.name.toLowerCase() === r.name.toLowerCase() ||
          (Boolean(r.hostname) && t.host.toLowerCase() === r.hostname!.toLowerCase()),
      ) ||
      null;
    setSshModalMode('ssh');
    setEditingRunner(matched);
    setAutoUpdateDraft(matched ? matched.auto_update !== false : true);
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
    setDeployLog('');
    setPasteNotice('');
    setAdvancedOpen(Boolean(t.workspace_path || t.server_ws_url || (t.install_dir && t.install_dir !== '~/.nex-runner')));
    setShowPwd(false);
    setConfigMenuOpen(false);
    setModalOpen(true);
    void loadSSHConfigHosts();
    void loadStagedBinaries();
  };

  const openRunnerSettingsModal = (r: RunnerRecord) => {
    setSshModalMode('runner_only');
    setEditingRunner(r);
    setAutoUpdateDraft(r.auto_update !== false);
    setForm({ ...EMPTY_FORM, name: r.name || r.id });
    setFormError('');
    setDeployLog('');
    setPasteNotice('');
    setAdvancedOpen(false);
    setShowPwd(false);
    setConfigMenuOpen(false);
    setModalOpen(true);
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
    if (sshModalMode === 'runner_only') {
      if (!editingRunner) return;
      setSaving(true);
      setFormError('');
      try {
        await updateRunnerPolicy(editingRunner.id, { auto_update: autoUpdateDraft });
        setModalOpen(false);
        onFlash(formatMsg(locale, 'runners.updatePolicySaved'));
      } catch (err) {
        setFormError(err instanceof Error ? err.message : String(err));
      } finally {
        setSaving(false);
      }
      return;
    }
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
    setDeployLog('');
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
      if (saved?.id) {
        setForm((prev) => ({ ...prev, id: saved.id }));
      }
      const linkedRunnerId = editingRunner?.id || sshTargetRunnerId(saved?.name || form.name.trim() || host);
      if (linkedRunnerId && runners.some((item) => item.id === linkedRunnerId)) {
        try {
          await updateRunnerPolicy(linkedRunnerId, {
            auto_update: autoUpdateDraft,
            update_mode: form.install_mode === 'scp' ? 'scp' : 'self_update',
          });
        } catch {
          // ignore if runner policy patch fails (SSH save succeeded)
        }
      }

      if (!deployAfterSave || !saved?.id) {
        await refreshRegistries();
        setModalOpen(false);
        onFlash(formatMsg(locale, 'runners.savedSshServer', { name: saved.name }));
        return;
      }

      const res = await apiFetch<{ ok?: boolean; error?: string; log?: string }>(
        `/api/v1/ssh/targets/${encodeURIComponent(saved.id)}/deploy`,
        { method: 'POST' },
      );
      if (res?.ok) {
        await Promise.all([refreshRegistries(), refreshInitialData()]);
        window.setTimeout(() => void refreshInitialData(), 1500);
        setModalOpen(false);
        onFlash(formatMsg(locale, 'runners.deployedStartedRunnerOn', { name: saved.name }));
      } else {
        setFormError(res?.error || formatMsg(locale, 'runners.unknownError'));
        setDeployLog(res?.log || '');
        void refreshRegistries();
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

  const [removingRunnerIds, setRemovingRunnerIds] = useState<Record<string, boolean>>({});
  const [pendingDeleteRunner, setPendingDeleteRunner] = useState<{ id: string; name: string } | null>(null);
  const [pendingRestartRunner, setPendingRestartRunner] = useState<{ id: string; name: string } | null>(null);
  const [restartingRunnerId, setRestartingRunnerId] = useState<string | null>(null);

  const restartRunner = async (target: { id: string; name: string }) => {
    setRestartingRunnerId(target.id);
    try {
      await apiFetch(`/api/v1/runners/${encodeURIComponent(target.id)}/restart`, { method: 'POST' });
      onFlash(formatMsg(locale, 'runners.restartedToast', { name: target.name }));
      setTimeout(() => {
        void refreshInitialData();
      }, 800);
    } catch (err) {
      onFlash(
        formatMsg(locale, 'runners.restartFailedToast', {
          name: target.name,
          error: err instanceof Error ? err.message : String(err),
        }),
      );
    } finally {
      if (mountedRef.current) setRestartingRunnerId(null);
    }
  };
  const [upgradingRunnerIds, setUpgradingRunnerIds] = useState<Record<string, boolean>>({});
  const [upgradingAll, setUpgradingAll] = useState(false);
  const [renamingRunnerId, setRenamingRunnerId] = useState<string | null>(null);
  const [renameDraft, setRenameDraft] = useState('');
  const [renameBusyId, setRenameBusyId] = useState<string | null>(null);
  const [openMenuKey, setOpenMenuKey] = useState<string | null>(null);
  const menuContainerRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!openMenuKey) return;
    const onDown = (event: MouseEvent) => {
      if (menuContainerRef.current && !menuContainerRef.current.contains(event.target as Node)) {
        setOpenMenuKey(null);
      }
    };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  }, [openMenuKey]);

  const upgradableCount = runners.filter(
    (r) =>
      r.status === 'online' &&
      !r.is_dev &&
      (r.incompatible || r.update_status === 'update_required' || r.update_status === 'update_available'),
  ).length;

  const triggerSingleUpgrade = async (r: RunnerRecord, mode: 'when_idle' | 'immediate') => {
    const matched = matchSshTargetForRunner(r);
    const useScp = Boolean(matched && (r.update_mode ? r.update_mode === 'scp' : matched.install_mode === 'scp'));
    if (useScp && matched) {
      setUpgradingRunnerIds((prev) => ({ ...prev, [r.id]: true }));
      try {
        await runTargetAction(matched, 'deploy');
      } finally {
        setUpgradingRunnerIds((prev) => {
          const next = { ...prev };
          delete next[r.id];
          return next;
        });
      }
      return;
    }
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

  const submitRunnerRename = async (r: RunnerRecord) => {
    const trimmed = renameDraft.trim();
    if (!trimmed || trimmed === r.name) {
      setRenamingRunnerId(null);
      return;
    }
    setRenameBusyId(r.id);
    try {
      await renameRunner(r.id, trimmed);
      setRenamingRunnerId(null);
      onFlash(formatMsg(locale, 'runners.renamedSuccess', { name: trimmed }));
    } catch (err) {
      onFlash(
        formatMsg(locale, 'runners.renameFailed', {
          error: err instanceof Error ? err.message : String(err),
        }),
      );
    } finally {
      setRenameBusyId(null);
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

  // Map each connected runner to its matching SSH target (by deterministic runner ID, name, or hostname).
  const matchSshTargetForRunner = (r: (typeof runners)[number]): (typeof sshTargets)[number] | undefined =>
    sshTargets.find(
      (t) =>
        sshTargetRunnerId(t.name) === r.id ||
        t.name.toLowerCase() === r.name.toLowerCase() ||
        (Boolean(r.hostname) && t.host.toLowerCase() === r.hostname!.toLowerCase()),
    );

  const renderSshActionButtons = (t: (typeof sshTargets)[number], isOnline: boolean, linkedRunner: RunnerRecord) => {
    const busy = busyById[t.id];
    const logOpen = Boolean(expandedLogs[t.id]);
    return (
      <>
        <button
          type="button"
          data-testid={`ssh-test-${t.id}`}
          disabled={Boolean(busy)}
          onClick={() => void runTargetAction(t, 'test')}
          className="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-card px-2.5 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover disabled:opacity-50"
        >
          {busy === 'test' ? <LoaderCircle className="size-3 animate-spin" /> : <Wifi className="size-3" />}
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
          data-testid={`ssh-edit-${t.id}`}
          onClick={() => openEditModal(t, linkedRunner)}
          aria-label={formatMsg(locale, 'runners.editSshServer')}
          title={formatMsg(locale, 'runners.editSshServer')}
          className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
        >
          <Pencil className="size-3.5" />
        </button>
      </>
    );
  };

  return (
    <div className="space-y-6">
      {/* Connected Runners + Unified Actions Toolbar */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <SectionHeading
          title={formatMsg(locale, 'runners.connectedRunners', { length: runners.length })}
          description={
            formatMsg(locale, 'runners.liveReverseConnectedRunnersPlatform')
          }
        />
        <div className="flex flex-wrap items-center gap-2">
          {upgradableCount > 0 && (
            <button
              type="button"
              data-testid="update-all-runners-btn"
              disabled={upgradingAll}
              onClick={() => void triggerAllUpgrades()}
              className="inline-flex h-8 items-center gap-1 rounded-lg border border-brand-blue/30 bg-brand-blue/10 px-2.5 text-ui-xs font-medium text-brand-blue transition-colors hover:bg-brand-blue/20 disabled:opacity-50"
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
            data-testid="add-ssh-server-btn"
            onClick={openCreateModal}
            className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-lg bg-foreground px-3 text-ui-xs font-medium text-background transition-opacity hover:opacity-90"
          >
            <Plus className="size-3.5" />
            <span>{formatMsg(locale, 'runners.connectViaSsh')}</span>
          </button>
          <button
            type="button"
            data-testid="manual-join-btn"
            onClick={() => setManualModalOpen(true)}
            className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-lg border border-border bg-card px-2.5 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover"
          >
            <Terminal className="size-3.5" />
            <span>{formatMsg(locale, 'runners.manualJoin')}</span>
          </button>
          <button
            type="button"
            onClick={() => void refreshInitialData()}
            className="inline-flex h-8 items-center gap-1 rounded-lg border border-border bg-card px-2.5 text-ui-xs text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
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
            const matchedSsh = matchSshTargetForRunner(r);
            const sshLogOpen = matchedSsh ? Boolean(expandedLogs[matchedSsh.id]) : false;
            const formattedVersion = formatRunnerVersion(r.version, r.build_sha);
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
                className="px-5 py-4"
              >
                <div className="grid grid-cols-1 items-center gap-3 sm:grid-cols-[minmax(0,1fr)_auto]">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <span
                      className={`size-2 shrink-0 rounded-full ${
                        r.status === 'online' ? 'bg-success' : 'bg-foreground-subtlest'
                      }`}
                    />
                    {renamingRunnerId === r.id ? (
                      <form
                        onSubmit={(e) => {
                          e.preventDefault();
                          void submitRunnerRename(r);
                        }}
                        className="inline-flex items-center gap-1"
                      >
                        <input
                          type="text"
                          autoFocus
                          data-testid={`rename-runner-input-${r.id}`}
                          value={renameDraft}
                          disabled={renameBusyId === r.id}
                          maxLength={80}
                          placeholder={formatMsg(locale, 'runners.renamePlaceholder')}
                          onChange={(e) => setRenameDraft(e.target.value)}
                          onKeyDown={(e) => {
                            if (e.key === 'Escape') {
                              e.preventDefault();
                              setRenamingRunnerId(null);
                            }
                          }}
                          className={`${settingsInputClass} h-7 w-44 px-2 text-ui-sm`}
                        />
                        <button
                          type="submit"
                          data-testid={`save-runner-name-${r.id}`}
                          disabled={renameBusyId === r.id || !renameDraft.trim()}
                          aria-label={formatMsg(locale, 'common.save')}
                          className="flex size-7 items-center justify-center rounded-md bg-foreground text-background transition-opacity hover:opacity-90 disabled:opacity-40"
                        >
                          {renameBusyId === r.id ? <LoaderCircle className="size-3.5 animate-spin" /> : <Check className="size-3.5" />}
                        </button>
                        <button
                          type="button"
                          onClick={() => setRenamingRunnerId(null)}
                          aria-label={formatMsg(locale, 'common.cancel')}
                          className="flex size-7 items-center justify-center rounded-md border border-border text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
                        >
                          <X className="size-3.5" />
                        </button>
                      </form>
                    ) : (
                      <>
                        <span className="truncate text-ui-base font-medium text-foreground">{r.name}</span>
                        <button
                          type="button"
                          data-testid={`rename-runner-${r.id}`}
                          title={formatMsg(locale, 'runners.rename')}
                          aria-label={formatMsg(locale, 'runners.rename')}
                          onClick={() => {
                            setRenamingRunnerId(r.id);
                            setRenameDraft(r.name);
                          }}
                          className="flex size-6 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
                        >
                          <Pencil className="size-3" />
                        </button>
                      </>
                    )}
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
                    {r.status === 'online' && r.os === 'darwin' && r.full_disk_access === false && (
                      <button
                        type="button"
                        data-testid={`runner-fda-btn-${r.id}`}
                        onClick={() => setFdaRunnerId(r.id)}
                        className="inline-flex items-center gap-1 rounded-md border border-amber-500/40 bg-amber-500/15 px-1.5 py-0.5 text-ui-xs font-medium text-amber-700 transition-colors hover:bg-amber-500/25 dark:text-amber-300"
                      >
                        <HardDrive className="size-3" />
                        <span>{formatMsg(locale, 'runners.fdaNeeded')}</span>
                      </button>
                    )}
                    {r.status === 'online' && r.os === 'darwin' && r.full_disk_access === true && (
                      <button
                        type="button"
                        data-testid={`runner-fda-granted-${r.id}`}
                        onClick={() => setFdaRunnerId(r.id)}
                        className="inline-flex items-center gap-1 rounded-md border border-success/30 bg-success-surface px-1.5 py-0.5 text-ui-xs font-medium text-success-surface-fg transition-opacity hover:opacity-85"
                      >
                        <ShieldCheck className="size-3" />
                        <span>{formatMsg(locale, 'runners.fdaGranted')}</span>
                      </button>
                    )}
                    {matchedSsh?.ssh_alias && (
                      <span className="inline-flex items-center gap-1 rounded-md bg-brand-blue/10 px-1.5 py-0.5 font-mono text-[11px] text-brand-blue">
                        <Link2 className="size-3" />
                        {matchedSsh.ssh_alias}
                      </span>
                    )}
                    {phaseLabel && (
                      <span
                        data-testid={`runner-update-phase-${r.id}`}
                        title={r.update_message || undefined}
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
                    {matchedSsh && (
                      <span className="rounded bg-surface px-1.5 py-0.5 font-mono text-[11px]">
                        {matchedSsh.username ? `${matchedSsh.username}@` : ''}
                        {matchedSsh.host}:{matchedSsh.port || 22}
                      </span>
                    )}
                    {r.hostname ? <span className="font-mono">{r.hostname}</span> : null}
                    {r.workspace_default ? <span className="truncate font-mono">{r.workspace_default}</span> : null}
                  </div>
                  {matchedSsh?.last_error && (
                    <p className="mt-1 truncate text-ui-xs text-destructive">{matchedSsh.last_error}</p>
                  )}
                  {r.status === 'online' && isRequired && (
                    <div
                      data-testid={`runner-update-required-${r.id}`}
                      className="mt-2 flex flex-wrap items-center justify-between gap-2 rounded-lg border border-destructive/30 bg-destructive-surface/60 px-3 py-2 text-ui-xs text-destructive-surface-fg"
                    >
                      <div className="flex min-w-0 items-center gap-1.5">
                        <AlertTriangle className="size-3.5 shrink-0" />
                        <span className="font-medium">
                          {formatMsg(locale, 'runners.updateRequired', {
                            version: formatRunnerVersion(r.target_version, r.target_build_sha) || 'latest',
                          })}
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
                        <span>
                          {Boolean(matchedSsh && (r.update_mode ? r.update_mode === 'scp' : matchedSsh.install_mode === 'scp'))
                            ? formatMsg(locale, 'runners.updateViaScpBtn')
                            : formatMsg(locale, 'runners.updateNowBtn')}
                        </span>
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
                          {formatMsg(locale, 'runners.updateAvailable', {
                            version: formatRunnerVersion(r.target_version, r.target_build_sha) || 'latest',
                          })}
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
                        <span>
                          {Boolean(matchedSsh && (r.update_mode ? r.update_mode === 'scp' : matchedSsh.install_mode === 'scp'))
                            ? formatMsg(locale, 'runners.updateViaScpBtn')
                            : formatMsg(locale, 'runners.updateBtn')}
                        </span>
                      </button>
                    </div>
                  )}
                  {r.status !== 'online' && (isRequired || isAvailable) && (
                    <div
                      data-testid={`runner-update-offline-${r.id}`}
                      className="mt-2 flex flex-wrap items-center justify-between gap-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-1.5 text-ui-xs text-amber-700 dark:text-amber-300"
                    >
                      <div className="flex min-w-0 items-center gap-1.5">
                        <AlertTriangle className="size-3.5 shrink-0" />
                        <span className="truncate font-medium">
                          {formatMsg(locale, 'runners.offlineOutdatedHint', { version: r.target_version || 'latest' })}
                        </span>
                      </div>
                      <button
                        type="button"
                        onClick={() => {
                          setManualModalOpen(true);
                          void navigator.clipboard?.writeText(joinCmd);
                          onFlash(formatMsg(locale, 'common.copied'));
                        }}
                        className="inline-flex h-6 shrink-0 items-center gap-1 rounded-md border border-amber-500/40 bg-card px-2 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover"
                      >
                        <Copy className="size-3" />
                        <span>{formatMsg(locale, 'runners.copyJoinCommandBtn')}</span>
                      </button>
                    </div>
                  )}
                  {r.status === 'online' && r.update_phase === 'draining' && (
                    <div
                      data-testid={`runner-draining-hint-${r.id}`}
                      className="mt-2 flex flex-wrap items-center justify-between gap-2 rounded-lg border border-brand-blue/30 bg-brand-blue/10 px-3 py-1.5 text-ui-xs text-brand-blue"
                    >
                      <div className="flex min-w-0 items-center gap-1.5">
                        <LoaderCircle className="size-3.5 shrink-0 animate-spin" />
                        <span className="truncate font-medium">
                          {r.update_message || formatMsg(locale, 'runners.phase.draining')}
                        </span>
                      </div>
                      <button
                        type="button"
                        data-testid={`force-upgrade-runner-${r.id}`}
                        disabled={upgrading}
                        onClick={() => void triggerSingleUpgrade(r, 'immediate')}
                        className="inline-flex h-6 shrink-0 items-center gap-1 rounded-md border border-brand-blue/40 bg-card px-2 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover disabled:opacity-50"
                      >
                        <ArrowUpCircle className="size-3" />
                        <span>{formatMsg(locale, 'runners.updateNowBtn')}</span>
                      </button>
                    </div>
                  )}
                  {r.update_error && (
                    <p className="mt-1 truncate text-ui-xs text-destructive">{r.update_error}</p>
                  )}
                </div>
                <div className="flex flex-wrap items-center justify-end gap-1.5">
                  {matchedSsh ? (
                    renderSshActionButtons(matchedSsh, r.status === 'online', r)
                  ) : (
                    <button
                      type="button"
                      data-testid={`edit-runner-settings-${r.id}`}
                      onClick={() => openRunnerSettingsModal(r)}
                      aria-label={formatMsg(locale, 'runners.editRunnerSettings')}
                      title={formatMsg(locale, 'runners.editRunnerSettings')}
                      className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
                    >
                      <Pencil className="size-3.5" />
                    </button>
                  )}
                  {(r.id === 'run_desktop_embedded' || r.status === 'online') && (
                    <button
                      type="button"
                      data-testid={`restart-runner-${r.id}`}
                      disabled={restartingRunnerId === r.id}
                      title={formatMsg(locale, 'runners.restartRunner')}
                      aria-label={formatMsg(locale, 'runners.restartRunner')}
                      onClick={() => setPendingRestartRunner({ id: r.id, name: r.name || r.id })}
                      className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground disabled:opacity-50"
                    >
                      {restartingRunnerId === r.id ? (
                        <LoaderCircle className="size-3.5 animate-spin" />
                      ) : (
                        <RefreshCw className="size-3.5" />
                      )}
                    </button>
                  )}
                  {r.id !== 'run_desktop_embedded' && (
                    <button
                      type="button"
                      data-testid={`remove-runner-${r.id}`}
                      disabled={removing}
                      title={formatMsg(locale, 'runners.removeRunner')}
                      aria-label={formatMsg(locale, 'runners.removeRunner')}
                      onClick={() => setPendingDeleteRunner({ id: r.id, name: r.name || r.id })}
                      className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-destructive-surface hover:text-destructive-surface-fg disabled:opacity-50"
                    >
                      {removing ? (
                        <LoaderCircle className="size-3.5 animate-spin" />
                      ) : (
                        <Trash2 className="size-3.5" />
                      )}
                    </button>
                  )}
                </div>
                </div>
                {matchedSsh && sshLogOpen && matchedSsh.last_log && (
                  <pre className="mt-3 max-h-56 overflow-auto rounded-lg border border-border bg-surface p-3 font-mono text-[11px] leading-relaxed text-foreground-subtle whitespace-pre-wrap">
                    {matchedSsh.last_log}
                  </pre>
                )}
              </div>
            );
          })
        )}
      </SettingsGroupCard>

      {/* Manual Join Steps Modal */}
      {manualModalOpen && (
        <div
          data-testid="manual-join-modal-backdrop"
          onMouseDown={(e) => {
            if (e.target === e.currentTarget) setManualModalOpen(false);
          }}
          className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 px-4 pt-[8vh] pb-8 backdrop-blur-[2px]"
        >
          <div
            data-testid="manual-join-modal"
            className="w-full max-w-[600px] overflow-hidden rounded-2xl border border-border bg-popover text-foreground shadow-2xl"
          >
            <div className="flex items-start justify-between gap-3 border-b border-border px-5 py-4">
              <div className="min-w-0">
                <h2 className="text-ui-lg font-semibold text-foreground">
                  {formatMsg(locale, 'runners.manualRunnerJoinCommand')}
                </h2>
                <p className="mt-0.5 text-ui-xs text-foreground-subtlest">
                  {formatMsg(locale, 'runners.forNatFirewalledHosts')}
                </p>
              </div>
              <button
                type="button"
                onClick={() => setManualModalOpen(false)}
                className="flex size-7 items-center justify-center rounded-lg text-foreground-subtlest hover:bg-hover hover:text-foreground"
              >
                <X className="size-4" />
              </button>
            </div>

            <div className="max-h-[75vh] space-y-4 overflow-y-auto px-5 py-4">
              {runnerAuth && (
                <div
                  data-testid="runner-auth-panel"
                  className="space-y-2.5 rounded-lg border border-border bg-surface/60 p-3.5"
                >
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div>
                      <div className="text-ui-xs font-medium text-foreground">
                        {formatMsg(locale, 'runners.authSectionTitle')}
                      </div>
                      <div className="text-ui-xs text-foreground-subtlest">
                        {runnerAuth.env_managed
                          ? formatMsg(locale, 'runners.requireAuthEnvManaged')
                          : formatMsg(locale, 'runners.authSectionDesc')}
                      </div>
                    </div>
                    <label className="inline-flex items-center gap-2 text-ui-xs text-foreground">
                      <input
                        type="checkbox"
                        data-testid="runner-require-auth-toggle"
                        checked={runnerAuth.require_auth}
                        disabled={authBusy || runnerAuth.env_managed}
                        onChange={(e) => void toggleRequireAuth(e.target.checked)}
                        className="accent-brand-blue"
                      />
                      <span>{formatMsg(locale, 'runners.requireAuthLabel')}</span>
                    </label>
                  </div>
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-ui-xs text-foreground-subtle">
                      {formatMsg(locale, 'runners.pairingSecretLabel')}:
                    </span>
                    <code
                      data-testid="runner-pairing-secret-val"
                      className="flex-1 truncate rounded border border-border bg-card px-2 py-1 font-mono text-ui-xs text-foreground"
                    >
                      {showPairingSecret
                        ? runnerAuth.pairing_secret
                        : `${runnerAuth.pairing_secret.slice(0, 6)}••••••••${runnerAuth.pairing_secret.slice(-4)}`}
                    </code>
                    <button
                      type="button"
                      onClick={() => setShowPairingSecret((v) => !v)}
                      className="flex h-7 items-center gap-1 rounded-md border border-border bg-card px-2 text-ui-xs text-foreground-subtle hover:bg-hover hover:text-foreground"
                    >
                      {showPairingSecret ? <EyeOff className="size-3" /> : <Eye className="size-3" />}
                    </button>
                    {!runnerAuth.env_managed && (
                      <button
                        type="button"
                        data-testid="runner-rotate-secret-btn"
                        disabled={authBusy}
                        onClick={() => void rotatePairingSecret()}
                        className="flex h-7 items-center gap-1 rounded-md border border-border bg-card px-2.5 text-ui-xs text-foreground transition-colors hover:bg-hover disabled:opacity-50"
                      >
                        <KeyRound className="size-3" />
                        <span>{formatMsg(locale, 'runners.rotateSecretBtn')}</span>
                      </button>
                    )}
                  </div>
                </div>
              )}

              {/* Zero-SSH One-Liner Installers (Windows PowerShell & Linux/macOS) */}
              <div className="space-y-2.5 rounded-lg border border-border bg-surface/40 p-3.5">
                <div>
                  <div className="text-ui-xs font-medium text-foreground">
                    {formatMsg(locale, 'runners.oneLinerTitle')}
                  </div>
                  <div className="text-ui-xs text-foreground-subtlest">
                    {formatMsg(locale, 'runners.oneLinerDesc')}
                  </div>
                </div>

                <div className="space-y-1">
                  <div className="text-ui-xs text-foreground-subtle">
                    {formatMsg(locale, 'runners.oneLinerWindows')}
                  </div>
                  <div className="flex items-center justify-between gap-2 rounded-lg border border-border bg-surface px-3 py-2">
                    <code data-testid="runner-win-install-cmd" className="truncate font-mono text-ui-xs text-foreground">
                      {winInstallCmd}
                    </code>
                    <button
                      type="button"
                      onClick={() => {
                        void navigator.clipboard?.writeText(winInstallCmd);
                        setCopiedKey('win');
                        setTimeout(() => setCopiedKey((cur) => (cur === 'win' ? null : cur)), 1500);
                      }}
                      className="flex shrink-0 items-center gap-1 rounded-md border border-border bg-card px-2.5 py-1 text-ui-xs text-foreground transition-colors hover:bg-hover"
                    >
                      {copiedKey === 'win' ? <Check className="size-3 text-success" /> : <Copy className="size-3" />}
                      <span>{copiedKey === 'win' ? formatMsg(locale, 'common.copied') : formatMsg(locale, 'runners.copyCommand')}</span>
                    </button>
                  </div>
                </div>

                <div className="space-y-1">
                  <div className="text-ui-xs text-foreground-subtle">
                    {formatMsg(locale, 'runners.oneLinerUnix')}
                  </div>
                  <div className="flex items-center justify-between gap-2 rounded-lg border border-border bg-surface px-3 py-2">
                    <code data-testid="runner-unix-install-cmd" className="truncate font-mono text-ui-xs text-foreground">
                      {unixInstallCmd}
                    </code>
                    <button
                      type="button"
                      onClick={() => {
                        void navigator.clipboard?.writeText(unixInstallCmd);
                        setCopiedKey('unix');
                        setTimeout(() => setCopiedKey((cur) => (cur === 'unix' ? null : cur)), 1500);
                      }}
                      className="flex shrink-0 items-center gap-1 rounded-md border border-border bg-card px-2.5 py-1 text-ui-xs text-foreground transition-colors hover:bg-hover"
                    >
                      {copiedKey === 'unix' ? <Check className="size-3 text-success" /> : <Copy className="size-3" />}
                      <span>{copiedKey === 'unix' ? formatMsg(locale, 'common.copied') : formatMsg(locale, 'runners.copyCommand')}</span>
                    </button>
                  </div>
                </div>
              </div>

              <div className="flex items-center justify-between gap-2 rounded-lg border border-border bg-surface px-3 py-2">
                <code data-testid="runner-manual-join-cmd" className="truncate font-mono text-ui-sm text-foreground">
                  {joinCmd}
                </code>
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

              <div className="space-y-2 rounded-lg border border-border bg-surface/40 p-3.5">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div>
                    <div className="text-ui-xs font-medium text-foreground">
                      {formatMsg(locale, 'runners.relayPairTitle')}
                    </div>
                    <div className="text-ui-xs text-foreground-subtlest">
                      {formatMsg(locale, 'runners.relayPairDesc')}
                    </div>
                  </div>
                  <button
                    type="button"
                    data-testid="generate-relay-pair-cmd-btn"
                    disabled={relayPairLoading}
                    onClick={() => void generateRelayPairCommand()}
                    className="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-card px-2.5 text-ui-xs text-foreground transition-colors hover:bg-hover disabled:opacity-50"
                  >
                    {relayPairLoading ? <LoaderCircle className="size-3 animate-spin" /> : <Link2 className="size-3" />}
                    <span>
                      {relayPairLoading
                        ? formatMsg(locale, 'runners.generatingRelayPair')
                        : formatMsg(locale, 'runners.generateRelayPairBtn')}
                    </span>
                  </button>
                </div>
                {relayPairCmd && (
                  <div className="flex items-center justify-between gap-2 rounded-lg border border-border bg-card px-3 py-2">
                    <code data-testid="runner-relay-pair-cmd" className="truncate font-mono text-ui-xs text-foreground">
                      {relayPairCmd}
                    </code>
                    <button
                      type="button"
                      onClick={() => {
                        void navigator.clipboard?.writeText(relayPairCmd);
                        setRelayCopied(true);
                        setTimeout(() => setRelayCopied(false), 1500);
                      }}
                      className="flex shrink-0 items-center gap-1 rounded-md border border-border bg-surface px-2.5 py-1 text-ui-xs text-foreground transition-colors hover:bg-hover"
                    >
                      {relayCopied ? <Check className="size-3 text-success" /> : <Copy className="size-3" />}
                      <span>{relayCopied ? formatMsg(locale, 'common.copied') : formatMsg(locale, 'runners.copyCommand')}</span>
                    </button>
                  </div>
                )}
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Nex-Aligned New / Edit SSH Connection Modal */}
      {modalOpen && (
        <div
          data-testid="ssh-server-modal-backdrop"
          onMouseDown={(e) => {
            if (e.target === e.currentTarget && !saving) setModalOpen(false);
          }}
          className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 px-4 pt-[8vh] pb-8 backdrop-blur-[2px]"
        >
          <form
            data-testid="ssh-server-modal"
            onSubmit={(e) => void submitForm(e, !form.id)}
            className="w-full max-w-[540px] overflow-hidden rounded-2xl border border-border bg-popover text-foreground shadow-2xl"
          >
            {/* Header + Live Target Preview */}
            <div className="flex items-start justify-between gap-3 border-b border-border px-5 py-4">
              <div className="min-w-0">
                <h2 className="text-ui-lg font-semibold text-foreground">
                  {sshModalMode === 'runner_only'
                    ? formatMsg(locale, 'runners.runnerSettingsTitle')
                    : form.id
                      ? formatMsg(locale, 'runners.editSshConnection')
                      : formatMsg(locale, 'runners.newSshConnection')}
                </h2>
                <div className="mt-0.5 truncate font-mono text-ui-xs text-foreground-subtlest">
                  {sshModalMode === 'runner_only' ? editingRunner?.name || editingRunner?.id : livePreview}
                </div>
              </div>
              <button
                type="button"
                disabled={saving}
                onClick={() => setModalOpen(false)}
                className="flex size-7 items-center justify-center rounded-lg text-foreground-subtlest hover:bg-hover hover:text-foreground disabled:opacity-50"
              >
                <X className="size-4" />
              </button>
            </div>

            <div className="max-h-[70vh] space-y-4 overflow-y-auto px-5 py-4">
              {sshModalMode === 'ssh' && (
                <>
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

              {(formError || deployLog) && (
                <div
                  data-testid="ssh-modal-error"
                  className="space-y-2 rounded-lg border border-destructive/30 bg-destructive-surface px-3 py-2 text-ui-xs text-destructive-surface-fg"
                >
                  {formError && <div className="font-medium break-words">{formError}</div>}
                  {deployLog && (
                    <pre className="max-h-40 overflow-auto rounded border border-destructive/20 bg-background/60 p-2 font-mono text-[11px] leading-relaxed text-foreground-subtle whitespace-pre-wrap">
                      {deployLog}
                    </pre>
                  )}
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

                <p className="text-ui-xs leading-relaxed text-foreground-subtle">
                  {formatMsg(
                    locale,
                    form.install_mode === 'scp' ? 'runners.probesRemoteOsArchAuto' : 'runners.runsCurlWgetOnThe',
                  )}
                </p>

                <div className="flex items-center justify-between gap-3 border-t border-border/70 pt-2.5">
                  <div className="min-w-0">
                    <div className="text-ui-xs font-medium text-foreground">
                      {formatMsg(locale, 'runners.autoUpdateLabel')}
                    </div>
                    <p className="mt-0.5 text-[11px] leading-relaxed text-foreground-subtlest">
                      {formatMsg(locale, 'runners.autoUpdateDesc')}
                    </p>
                  </div>
                  <input
                    type="checkbox"
                    data-testid="modal-runner-auto-update-toggle"
                    checked={autoUpdateDraft}
                    onChange={(e) => setAutoUpdateDraft(e.target.checked)}
                    className="size-4 shrink-0 accent-brand-blue"
                  />
                </div>
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
                </>
              )}

              {sshModalMode === 'runner_only' && (
                <>
                  {formError && (
                    <div
                      data-testid="ssh-modal-error"
                      className="rounded-lg border border-destructive/30 bg-destructive-surface px-3 py-2 text-ui-xs text-destructive-surface-fg"
                    >
                      <div className="font-medium break-words">{formError}</div>
                    </div>
                  )}
                  <div className="flex items-center justify-between gap-3 rounded-xl border border-border bg-surface/50 p-3.5">
                    <div className="min-w-0">
                      <div className="text-ui-sm font-medium text-foreground">
                        {formatMsg(locale, 'runners.autoUpdateLabel')}
                      </div>
                      <p className="mt-0.5 text-ui-xs leading-relaxed text-foreground-subtlest">
                        {formatMsg(locale, 'runners.autoUpdateDesc')}
                      </p>
                    </div>
                    <input
                      type="checkbox"
                      data-testid="modal-runner-auto-update-toggle"
                      checked={autoUpdateDraft}
                      onChange={(e) => setAutoUpdateDraft(e.target.checked)}
                      className="size-4 shrink-0 accent-brand-blue"
                    />
                  </div>
                </>
              )}
            </div>

            {/* Footer */}
            <div className="flex items-center justify-end gap-2 border-t border-border bg-surface/40 px-5 py-3.5">
              <button
                type="button"
                disabled={saving}
                onClick={() => setModalOpen(false)}
                className="h-8 rounded-lg border border-border bg-card px-3 text-ui-sm text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground disabled:opacity-50"
              >
                {formatMsg(locale, 'common.cancel')}
              </button>
              {sshModalMode === 'runner_only' ? (
                <button
                  type="submit"
                  data-testid="runner-settings-save-btn"
                  disabled={saving}
                  className="inline-flex h-8 items-center gap-1.5 rounded-lg bg-foreground px-3.5 text-ui-sm font-medium text-background transition-opacity hover:opacity-90 disabled:opacity-50"
                >
                  {saving && <LoaderCircle className="size-3.5 animate-spin" />}
                  <span>{formatMsg(locale, 'common.save2')}</span>
                </button>
              ) : (
                <>
                  {form.id && (
                    <button
                      type="submit"
                      data-testid="ssh-save-btn"
                      disabled={saving}
                      className="h-8 rounded-lg border border-border bg-card px-3 text-ui-sm font-medium text-foreground transition-colors hover:bg-hover disabled:opacity-50"
                    >
                      {formatMsg(locale, 'common.save2')}
                    </button>
                  )}
                  <button
                    type="button"
                    data-testid="ssh-save-deploy-btn"
                    disabled={saving}
                    onClick={(e) => void submitForm(e, true)}
                    className="inline-flex h-8 items-center gap-1.5 rounded-lg bg-foreground px-3.5 text-ui-sm font-medium text-background transition-opacity hover:opacity-90 disabled:opacity-50"
                  >
                    {saving && <LoaderCircle className="size-3.5 animate-spin" />}
                    <span>
                      {saving
                        ? formatMsg(locale, 'runners.connectingAndDeploying')
                        : form.id
                          ? formatMsg(locale, 'runners.saveDeploy')
                          : formatMsg(locale, 'runners.connectAndJoin')}
                    </span>
                  </button>
                </>
              )}
            </div>
          </form>
        </div>
      )}

      {pendingRestartRunner && (
        <div
          data-testid="confirm-restart-runner-modal"
          onMouseDown={(e) => {
            if (e.target === e.currentTarget) setPendingRestartRunner(null);
          }}
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/45 p-4 backdrop-blur-[2px]"
        >
          <div className="w-full max-w-sm rounded-2xl border border-border bg-popover p-5 text-foreground shadow-2xl">
            <h3 className="text-ui-lg font-semibold text-foreground">
              {formatMsg(locale, 'runners.confirmRestartTitle')}
            </h3>
            <p className="mt-2 text-ui-sm leading-relaxed text-foreground-subtle">
              {formatMsg(locale, 'runners.confirmRestartDesc', { name: pendingRestartRunner.name })}
            </p>
            <div className="mt-5 flex items-center justify-end gap-2">
              <button
                type="button"
                onClick={() => setPendingRestartRunner(null)}
                className="h-8 rounded-lg border border-border bg-card px-3 text-ui-sm text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
              >
                {formatMsg(locale, 'common.cancel')}
              </button>
              <button
                type="button"
                data-testid="confirm-restart-runner-btn"
                onClick={() => {
                  const target = pendingRestartRunner;
                  setPendingRestartRunner(null);
                  if (target) void restartRunner(target);
                }}
                className="h-8 rounded-lg bg-destructive px-3.5 text-ui-sm font-medium text-white transition-opacity hover:opacity-90"
              >
                {formatMsg(locale, 'runners.restartRunner')}
              </button>
            </div>
          </div>
        </div>
      )}

      {pendingDeleteRunner && (
        <div
          data-testid="confirm-delete-runner-modal"
          onMouseDown={(e) => {
            if (e.target === e.currentTarget) setPendingDeleteRunner(null);
          }}
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/45 p-4 backdrop-blur-[2px]"
        >
          <div className="w-full max-w-sm rounded-2xl border border-border bg-popover p-5 text-foreground shadow-2xl">
            <h3 className="text-ui-lg font-semibold text-foreground">
              {formatMsg(locale, 'runners.confirmRemoveTitle')}
            </h3>
            <p className="mt-2 text-ui-sm leading-relaxed text-foreground-subtle">
              {formatMsg(locale, 'runners.confirmRemoveDesc', { name: pendingDeleteRunner.name })}
            </p>
            <div className="mt-5 flex items-center justify-end gap-2">
              <button
                type="button"
                onClick={() => setPendingDeleteRunner(null)}
                className="h-8 rounded-lg border border-border bg-card px-3 text-ui-sm text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
              >
                {formatMsg(locale, 'common.cancel')}
              </button>
              <button
                type="button"
                data-testid="confirm-delete-runner-btn"
                onClick={() => {
                  const target = pendingDeleteRunner;
                  setPendingDeleteRunner(null);
                  if (target) void removeRunner(target);
                }}
                className="h-8 rounded-lg bg-destructive px-3.5 text-ui-sm font-medium text-white transition-opacity hover:opacity-90"
              >
                {formatMsg(locale, 'runners.removeRunner')}
              </button>
            </div>
          </div>
        </div>
      )}

      {fdaRunner && (() => {
        const displayPath = fdaRunner.app_path || '~/.nex-runner/NexRunner.app';
        const appName = displayPath.split('/').filter(Boolean).pop() || 'NexRunner.app';
        const granted = fdaRunner.full_disk_access === true;
        return (
          <div
            data-testid="runner-fda-modal"
            onMouseDown={(e) => {
              if (e.target === e.currentTarget) setFdaRunnerId(null);
            }}
            className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4 backdrop-blur-[2px]"
          >
            <div className="flex w-full max-w-lg flex-col overflow-hidden rounded-2xl border border-border bg-popover text-foreground shadow-2xl">
              {/* Header */}
              <div className="flex items-start justify-between gap-3 border-b border-border px-5 py-4">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <HardDrive className="size-4 shrink-0 text-amber-500" />
                    <h3 className="text-ui-lg font-semibold text-foreground">
                      {formatMsg(locale, 'runners.fdaModalTitle')}
                    </h3>
                  </div>
                  <p className="mt-1 text-ui-xs leading-relaxed text-foreground-subtle">
                    {formatMsg(locale, 'runners.fdaModalSubtitle', { name: fdaRunner.name })}
                  </p>
                </div>
                <button
                  type="button"
                  onClick={() => setFdaRunnerId(null)}
                  className="flex size-7 shrink-0 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
                >
                  <X className="size-4" />
                </button>
              </div>

              {/* Body */}
              <div className="space-y-4 px-5 py-4">
                {/* Live status bar */}
                <div
                  data-testid="fda-status-pill"
                  className={`flex items-center justify-between gap-2 rounded-xl border px-3 py-2 text-ui-xs font-medium ${
                    granted
                      ? 'border-success/30 bg-success-surface text-success-surface-fg'
                      : 'border-amber-500/35 bg-amber-500/10 text-amber-700 dark:text-amber-300'
                  }`}
                >
                  <span className="inline-flex items-center gap-1.5">
                    {granted ? <ShieldCheck className="size-3.5" /> : <AlertTriangle className="size-3.5" />}
                    <span>
                      {granted
                        ? formatMsg(locale, 'runners.fdaStatusGranted')
                        : formatMsg(locale, 'runners.fdaStatusMissing')}
                    </span>
                  </span>
                  <button
                    type="button"
                    data-testid="fda-recheck-btn"
                    disabled={fdaBusyAction !== null}
                    onClick={() => void triggerRunnerPermission(fdaRunner, 'check')}
                    className="inline-flex h-6 items-center gap-1 rounded-md border border-current/25 bg-card/80 px-2 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover disabled:opacity-50"
                  >
                    {fdaBusyAction === 'check' ? (
                      <LoaderCircle className="size-3 animate-spin" />
                    ) : (
                      <RefreshCw className="size-3" />
                    )}
                    <span>{formatMsg(locale, 'runners.fdaRecheckBtn')}</span>
                  </button>
                </div>

                {/* Visual drag-to-grant illustration */}
                <div className="grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-center gap-2 rounded-xl border border-dashed border-border bg-surface/60 p-3.5">
                  <div className="flex flex-col items-center gap-1.5 rounded-lg border border-border bg-card px-2.5 py-3 text-center shadow-sm">
                    <FolderOpen className="size-5 text-brand-blue" />
                    <span className="text-[11px] text-foreground-subtlest">
                      {formatMsg(locale, 'runners.fdaDragVisualApp')}
                    </span>
                    <span className="max-w-full truncate rounded bg-surface px-1.5 py-0.5 font-mono text-[11px] font-medium text-foreground">
                      {appName}
                    </span>
                  </div>
                  <div className="flex flex-col items-center gap-1 px-1 text-foreground-subtlest">
                    <ArrowRight className="size-4 animate-pulse text-brand-blue" />
                    <span className="whitespace-nowrap text-[10px] font-medium text-brand-blue">
                      {formatMsg(locale, 'runners.fdaDragVisualArrow')}
                    </span>
                  </div>
                  <div className="flex flex-col items-center gap-1.5 rounded-lg border border-border bg-card px-2.5 py-3 text-center shadow-sm">
                    <ShieldCheck className="size-5 text-success" />
                    <span className="text-[11px] font-medium leading-tight text-foreground">
                      {formatMsg(locale, 'runners.fdaDragVisualSettings')}
                    </span>
                  </div>
                </div>

                {/* Numbered steps */}
                <ol className="space-y-2 text-ui-xs leading-relaxed text-foreground-subtle">
                  <li className="flex gap-2">
                    <span className="flex size-4 shrink-0 items-center justify-center rounded-full bg-surface font-mono text-[10px] font-semibold text-foreground">
                      1
                    </span>
                    <span>{formatMsg(locale, 'runners.fdaStep1')}</span>
                  </li>
                  <li className="flex gap-2">
                    <span className="flex size-4 shrink-0 items-center justify-center rounded-full bg-surface font-mono text-[10px] font-semibold text-foreground">
                      2
                    </span>
                    <span>{formatMsg(locale, 'runners.fdaStep2', { appName })}</span>
                  </li>
                  <li className="flex gap-2">
                    <span className="flex size-4 shrink-0 items-center justify-center rounded-full bg-surface font-mono text-[10px] font-semibold text-foreground">
                      3
                    </span>
                    <span>{formatMsg(locale, 'runners.fdaStep3')}</span>
                  </li>
                </ol>

                {/* Copyable app bundle path */}
                <div className="space-y-1">
                  <div className="text-[11px] font-medium text-foreground-subtle">
                    {formatMsg(locale, 'runners.fdaAppPathLabel')}
                  </div>
                  <div className="flex items-center gap-2 rounded-lg border border-border bg-surface px-2.5 py-1.5">
                    <code className="min-w-0 flex-1 truncate font-mono text-[11px] text-foreground">
                      {displayPath}
                    </code>
                    <button
                      type="button"
                      onClick={() => {
                        void navigator.clipboard?.writeText(displayPath);
                        onFlash(formatMsg(locale, 'common.copied'));
                      }}
                      className="inline-flex h-6 shrink-0 items-center gap-1 rounded border border-border bg-card px-2 text-[11px] text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
                    >
                      <Copy className="size-3" />
                      <span>{formatMsg(locale, 'common.copy')}</span>
                    </button>
                  </div>
                </div>

                <p className="text-[11px] leading-relaxed text-foreground-subtlest">
                  {formatMsg(locale, 'runners.fdaSignNote')}
                </p>
              </div>

              {/* Footer */}
              <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border bg-surface/40 px-5 py-3.5">
                <div className="flex items-center gap-1.5">
                  <button
                    type="button"
                    disabled={fdaBusyAction !== null}
                    onClick={() => void triggerRunnerPermission(fdaRunner, 'open_settings')}
                    className="inline-flex h-8 items-center gap-1 rounded-lg border border-border bg-card px-2.5 text-ui-xs text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground disabled:opacity-50"
                  >
                    <ExternalLink className="size-3" />
                    <span>{formatMsg(locale, 'runners.fdaOpenSettingsBtn')}</span>
                  </button>
                  <button
                    type="button"
                    disabled={fdaBusyAction !== null}
                    onClick={() => void triggerRunnerPermission(fdaRunner, 'reveal_app')}
                    className="inline-flex h-8 items-center gap-1 rounded-lg border border-border bg-card px-2.5 text-ui-xs text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground disabled:opacity-50"
                  >
                    <FolderOpen className="size-3" />
                    <span>{formatMsg(locale, 'runners.fdaRevealAppBtn')}</span>
                  </button>
                </div>
                <button
                  type="button"
                  data-testid="fda-open-guide-btn"
                  disabled={fdaBusyAction !== null}
                  onClick={() => void triggerRunnerPermission(fdaRunner, 'open_guide')}
                  className="inline-flex h-8 items-center gap-1.5 rounded-lg bg-foreground px-3.5 text-ui-xs font-medium text-background transition-opacity hover:opacity-90 disabled:opacity-50"
                >
                  {fdaBusyAction === 'open_guide' ? (
                    <LoaderCircle className="size-3.5 animate-spin" />
                  ) : (
                    <HardDrive className="size-3.5" />
                  )}
                  <span>{formatMsg(locale, 'runners.fdaOpenGuideBtn')}</span>
                </button>
              </div>
            </div>
          </div>
        );
      })()}
    </div>
  );
}
