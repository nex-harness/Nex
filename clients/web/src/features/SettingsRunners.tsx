import {
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
  Square,
  Terminal,
  Trash2,
  Upload,
  Wifi,
  X,
} from 'lucide-react';
import { type ClipboardEvent, type FormEvent, useEffect, useState } from 'react';
import { apiFetch, resolveApiBase, safeStorageGet } from '../core/protocol';
import { useNexStore } from '../core/store';
import { SSHConfigHost, SSHTargetRecord, StagedBinaryInfo } from '../core/types';
import {
  SectionHeading,
  SettingsGroupCard,
  settingsInputClass,
  settingsMonoInputClass,
  settingsSelectClass,
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

export function RunnersSettingsSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const { locale, runners, sshTargets, refreshInitialData, refreshRegistries } = useNexStore();
  const isEn = locale === 'en-US';

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
  const [uploadPlatform, setUploadPlatform] = useState('linux/amd64');
  const [uploadingBin, setUploadingBin] = useState(false);

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

  useEffect(() => {
    void loadStagedBinaries();
  }, []);

  const loadSSHConfigHosts = async () => {
    setConfigLoading(true);
    try {
      const hosts = await apiFetch<SSHConfigHost[]>('/api/v1/ssh/config-hosts');
      setConfigHosts(Array.isArray(hosts) ? hosts : []);
    } catch {
      setConfigHosts([]);
    } finally {
      setConfigLoading(false);
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
      isEn
        ? `Parsed ${parsed.username ? `${parsed.username}@` : ''}${parsed.host}${parsed.port ? `:${parsed.port}` : ''}`
        : `已自动识别 ${parsed.username ? `${parsed.username}@` : ''}${parsed.host}${parsed.port ? `:${parsed.port}` : ''}`,
    );
  };

  const handleUploadBinary = async (file: File | null) => {
    if (!file) return;
    const [os, arch] = uploadPlatform.split('/');
    setUploadingBin(true);
    try {
      const headers: Record<string, string> = {};
      const tok = safeStorageGet('nex.auth.token');
      if (tok) headers.Authorization = `Bearer ${tok}`;
      const res = await fetch(
        `${resolveApiBase()}/api/v1/ssh/binaries?os=${encodeURIComponent(os)}&arch=${encodeURIComponent(arch)}`,
        {
          method: 'POST',
          credentials: 'same-origin',
          headers,
          body: file,
        },
      );
      if (!res.ok) throw new Error(await res.text());
      await loadStagedBinaries();
      onFlash(isEn ? `Uploaded nex-runner (${os}/${arch})` : `已上传 nex-runner (${os}/${arch}) 二进制`);
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err));
    } finally {
      setUploadingBin(false);
    }
  };

  const submitForm = async (e: FormEvent, deployAfterSave = false) => {
    e.preventDefault();
    const host = form.host.trim();
    const portNum = Number(form.port.trim() || '22');
    if (!host) {
      setFormError(isEn ? 'Host is required.' : '请填写远程主机地址。');
      return;
    }
    if (!Number.isInteger(portNum) || portNum < 1 || portNum > 65535) {
      setFormError(isEn ? 'Port must be between 1 and 65535.' : '端口必须为 1–65535 的整数。');
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
      onFlash(isEn ? `Saved SSH server "${saved.name}"` : `已保存远程服务器 "${saved.name}"`);
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
            isEn
              ? `SSH OK: ${target.name} (${res.os}/${res.arch})`
              : `SSH 连接成功：${target.name} (${res.os}/${res.arch})`,
          );
        } else if (action === 'deploy') {
          onFlash(
            isEn
              ? `Deployed & started runner on ${target.name}`
              : `已部署并启动远程 Runner：${target.name}`,
          );
        } else {
          onFlash(isEn ? `Stopped runner on ${target.name}` : `已停止远程 Runner：${target.name}`);
        }
      } else {
        onFlash(
          isEn
            ? `${action} failed: ${res?.error || 'unknown error'}`
            : `操作失败：${res?.error || '请查看部署日志'}`,
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

  const livePreview = `${form.username.trim() || 'user'}@${form.host.trim() || 'host'}:${form.port.trim() || '22'}`;

  return (
    <div className="space-y-6">
      {/* Top Action Bar & SSH Targets Section */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <SectionHeading
          title={isEn ? `SSH remote servers (${sshTargets.length})` : `SSH 远程服务器 (${sshTargets.length})`}
          description={
            isEn
              ? 'Configure remote hosts via SSH config or credentials, then deploy nex-runner via SCP or Remote Install.'
              : '支持从 ~/.ssh/config 导入或手动配置远程服务器，一键通过 SCP 上传或远程下载安装并启动 nex-runner。'
          }
        />
        <button
          type="button"
          data-testid="add-ssh-server-btn"
          onClick={openCreateModal}
          className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-lg bg-foreground px-3 text-ui-sm font-medium text-background transition-opacity hover:opacity-90"
        >
          <Plus className="size-3.5" />
          <span>{isEn ? 'Add SSH server' : '添加 SSH 服务器'}</span>
        </button>
      </div>

      <SettingsGroupCard>
        {sshTargets.length === 0 ? (
          <div className="flex flex-col items-center justify-center gap-2 px-5 py-9 text-center">
            <Server className="size-6 text-foreground-subtlest" />
            <p className="text-ui-sm text-foreground-subtle">
              {isEn
                ? 'No SSH servers configured yet. Add one to deploy nex-runner via SCP or Remote Install.'
                : '尚未配置 SSH 远程服务器。点击右上角「添加 SSH 服务器」，即可导入 ~/.ssh/config 并一键 SCP 上传或远程安装。'}
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
                          ? isEn
                            ? 'Remote Install'
                            : '远程安装'
                          : isEn
                            ? 'SCP Upload'
                            : 'SCP 上传'}
                      </span>
                      {t.ssh_alias && (
                        <span className="inline-flex items-center gap-1 rounded-md bg-brand-blue/10 px-1.5 py-0.5 font-mono text-[11px] text-brand-blue">
                          <Link2 className="size-3" />
                          {t.ssh_alias}
                        </span>
                      )}
                      {isOnline && (
                        <span className="rounded-md bg-success-surface px-1.5 py-0.5 text-[11px] font-medium text-success-surface-fg">
                          {isEn ? 'Runner Online' : '节点在线'}
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
                      <span>{isEn ? 'Test' : '测试连接'}</span>
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
                          ? isEn
                            ? 'Deploying…'
                            : '部署中…'
                          : t.install_mode === 'remote_install'
                            ? isEn
                              ? 'Remote Install'
                              : '远程安装'
                            : isEn
                              ? 'SCP Deploy'
                              : 'SCP 部署'}
                      </span>
                    </button>
                    {isOnline && (
                      <button
                        type="button"
                        disabled={Boolean(busy)}
                        onClick={() => void runTargetAction(t, 'stop')}
                        title={isEn ? 'Stop remote runner' : '停止远程 Runner'}
                        className="flex size-7 items-center justify-center rounded-md border border-border text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground disabled:opacity-50"
                      >
                        {busy === 'stop' ? <LoaderCircle className="size-3 animate-spin" /> : <Square className="size-3" />}
                      </button>
                    )}
                    {t.last_log && (
                      <button
                        type="button"
                        onClick={() => setExpandedLogs((p) => ({ ...p, [t.id]: !p[t.id] }))}
                        title={isEn ? 'Toggle logs' : '查看部署日志'}
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
                      aria-label={isEn ? 'Edit SSH server' : '编辑服务器'}
                      className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
                    >
                      <Pencil className="size-3.5" />
                    </button>
                    <button
                      type="button"
                      onClick={() => void deleteTarget(t.id)}
                      aria-label={isEn ? 'Delete SSH server' : '删除服务器'}
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
      <div className="flex items-center justify-between gap-2">
        <SectionHeading
          title={isEn ? `Connected runners (${runners.length})` : `已接入设备 (${runners.length})`}
          description={
            isEn
              ? 'Live reverse-connected runners, platform info, workspace, and synced config revision.'
              : '实时在线的反向连接节点、系统架构、默认工作区与配置同步版本。'
          }
        />
        <button
          type="button"
          onClick={() => void refreshInitialData()}
          className="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-card px-2.5 text-ui-xs text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
        >
          <RefreshCw className="size-3" />
          <span>{isEn ? 'Refresh' : '刷新'}</span>
        </button>
      </div>

      <SettingsGroupCard>
        {runners.length === 0 ? (
          <div className="px-5 py-8 text-center text-ui-sm text-foreground-subtlest">
            {isEn ? 'No runners connected yet.' : '暂无已连接的远程设备。'}
          </div>
        ) : (
          runners.map((r) => (
            <div
              key={r.id}
              className="grid grid-cols-1 items-center gap-3 px-5 py-4 sm:grid-cols-[minmax(0,1fr)_auto]"
            >
              <div className="min-w-0">
                <div className="flex items-center gap-2">
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
                </div>
                <div className="mt-1 flex flex-wrap items-center gap-1.5 text-ui-xs text-foreground-subtle">
                  <span className="rounded bg-surface px-1.5 py-0.5 font-mono">{r.os}</span>
                  <span className="rounded bg-surface px-1.5 py-0.5 font-mono">{r.arch}</span>
                  <span className="rounded bg-surface px-1.5 py-0.5 font-mono">{r.shell || 'default'}</span>
                  {r.hostname ? <span className="font-mono">{r.hostname}</span> : null}
                  {r.workspace_default ? <span className="truncate font-mono">{r.workspace_default}</span> : null}
                </div>
              </div>
              <div className="font-mono text-ui-xs tabular-nums text-foreground-subtlest sm:text-right">
                rev #{r.synced_config_rev}
              </div>
            </div>
          ))
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
            <span>{isEn ? 'Manual runner join command' : '手动运行接入命令'}</span>
          </span>
          <span className="text-ui-xs text-foreground-subtlest">
            {isEn ? 'For NAT / firewalled hosts' : '适用于仅能出站访问的内网机器'}
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
                <span>{copied ? (isEn ? 'Copied' : '已复制') : isEn ? 'Copy command' : '复制命令'}</span>
              </button>
            </div>
          </div>
        )}
      </SettingsGroupCard>

      {/* ZCode-Aligned New / Edit SSH Connection Modal */}
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
                    ? isEn
                      ? 'Edit SSH connection'
                      : '编辑 SSH 远程连接'
                    : isEn
                      ? 'New SSH connection'
                      : '新建 SSH 远程连接'}
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
              {/* Top row: Import from ~/.ssh/config (ZCode pattern) */}
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
                        {isEn ? `Linked: ${form.ssh_alias}` : `已关联 Host ${form.ssh_alias}`}
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
                    <span>{isEn ? 'Import from SSH config' : '从 SSH config 导入'}</span>
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
                          <span>{isEn ? 'Reading ~/.ssh/config…' : '正在读取 ~/.ssh/config…'}</span>
                        </div>
                      ) : configHosts.length === 0 ? (
                        <div className="px-3 py-2 text-ui-xs text-foreground-subtlest">
                          {isEn ? 'No concrete Host entries found in ~/.ssh/config' : '~/.ssh/config 中未找到可用 Host'}
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

              {/* Host + Port side-by-side (ZCode RDUX-27 layout) */}
              <div className="grid grid-cols-[minmax(0,1fr)_6rem] gap-3">
                <label className="block space-y-1">
                  <span className="text-ui-sm text-foreground-subtle">{isEn ? 'Host' : '主机地址'}</span>
                  <input
                    autoFocus
                    data-testid="ssh-host-input"
                    value={form.host}
                    onPaste={handleHostPaste}
                    onChange={(e) => setForm((p) => ({ ...p, host: e.target.value, ssh_alias: '' }))}
                    placeholder={isEn ? '192.168.1.10 or paste user@host:22' : '192.168.1.10（支持粘贴 user@host:22）'}
                    className={settingsMonoInputClass}
                  />
                  {pasteNotice && <p className="text-ui-xs text-success">{pasteNotice}</p>}
                </label>
                <label className="block space-y-1">
                  <span className="text-ui-sm text-foreground-subtle">{isEn ? 'Port' : '端口'}</span>
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
                  <span className="text-ui-sm text-foreground-subtle">{isEn ? 'Username' : '用户名'}</span>
                  <input
                    data-testid="ssh-username-input"
                    value={form.username}
                    onChange={(e) => setForm((p) => ({ ...p, username: e.target.value }))}
                    placeholder="root / ubuntu / tylinux"
                    className={settingsMonoInputClass}
                  />
                </label>
                <label className="block space-y-1">
                  <span className="text-ui-sm text-foreground-subtle">{isEn ? 'Display name' : '设备名称'}</span>
                  <input
                    data-testid="ssh-name-input"
                    value={form.name}
                    onChange={(e) => setForm((p) => ({ ...p, name: e.target.value }))}
                    placeholder={isEn ? 'e.g. Build Server' : '例如：编译服务器'}
                    className={settingsInputClass}
                  />
                </label>
              </div>

              {/* Auth method segmented control */}
              <div className="space-y-2">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-ui-sm text-foreground-subtle">{isEn ? 'Authentication' : '认证方式'}</span>
                  <div className="inline-flex rounded-lg border border-border bg-surface p-0.5 text-ui-xs">
                    {(
                      [
                        { id: 'key', zh: '私钥文件', en: 'Private Key' },
                        { id: 'agent', zh: 'SSH Agent', en: 'SSH Agent' },
                        { id: 'password', zh: '密码', en: 'Password' },
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
                        {isEn ? opt.en : opt.zh}
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
                          ? isEn
                            ? 'Leave blank to keep saved password'
                            : '留空则保留已保存的密码'
                          : isEn
                            ? 'Enter SSH password'
                            : '输入 SSH 登录密码'
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
                    {isEn ? 'Runner deployment mode' : 'Runner 部署方式'}
                  </span>
                  <div className="inline-flex rounded-lg border border-border bg-card p-0.5 text-ui-xs">
                    {(
                      [
                        { id: 'scp', zh: 'SCP 上传部署', en: 'SCP Upload' },
                        { id: 'remote_install', zh: '远程下载安装', en: 'Remote Install' },
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
                        {isEn ? m.en : m.zh}
                      </button>
                    ))}
                  </div>
                </div>

                {form.install_mode === 'scp' ? (
                  <div className="space-y-2">
                    <p className="text-ui-xs leading-relaxed text-foreground-subtle">
                      {isEn
                        ? 'Probes remote OS/Arch, auto-matches (or cross-compiles in dev workspace) the nex-runner binary, uploads via SCP, and launches it in background.'
                        : '自动探测远端 OS/Arch，优先匹配已上传或本机二进制（开发环境可自动交叉编译），通过 SCP 上传至远端并后台启动。'}
                    </p>
                    <div className="flex flex-wrap items-center gap-2 pt-1">
                      <select
                        value={uploadPlatform}
                        onChange={(e) => setUploadPlatform(e.target.value)}
                        className={`${settingsSelectClass} h-7 w-auto text-ui-xs`}
                      >
                        <option value="linux/amd64">linux/amd64</option>
                        <option value="linux/arm64">linux/arm64</option>
                        <option value="darwin/arm64">darwin/arm64</option>
                        <option value="darwin/amd64">darwin/amd64</option>
                      </select>
                      <label className="inline-flex h-7 cursor-pointer items-center gap-1.5 rounded-md border border-border bg-card px-2.5 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover">
                        {uploadingBin ? (
                          <LoaderCircle className="size-3 animate-spin" />
                        ) : (
                          <Upload className="size-3" />
                        )}
                        <span>{isEn ? 'Upload binary (optional)' : '上传预编译二进制（可选）'}</span>
                        <input
                          type="file"
                          className="hidden"
                          disabled={uploadingBin}
                          onChange={(e) => {
                            void handleUploadBinary(e.target.files?.[0] || null);
                            e.currentTarget.value = '';
                          }}
                        />
                      </label>
                      {stagedBinaries.length > 0 && (
                        <span className="text-[11px] text-foreground-subtlest">
                          {isEn ? 'Ready: ' : '已就绪：'}
                          {stagedBinaries.map((b) => `${b.os}/${b.arch} (${formatBytesShort(b.size_bytes)})`).join(', ')}
                        </span>
                      )}
                    </div>
                  </div>
                ) : (
                  <div className="space-y-2">
                    <p className="text-ui-xs leading-relaxed text-foreground-subtle">
                      {isEn
                        ? 'Runs curl/wget on the remote server to download nex-runner. Supports {os} and {arch} placeholders, or leave blank to pull directly from this Nex server.'
                        : '在远端通过 curl/wget 拉取安装 nex-runner。支持 {os} / {arch} 占位符；留空则自动生成一次性令牌从当前 Nex Server 拉取。'}
                    </p>
                    <input
                      data-testid="ssh-download-url-input"
                      value={form.download_url}
                      onChange={(e) => setForm((p) => ({ ...p, download_url: e.target.value }))}
                      placeholder={
                        isEn
                          ? 'Optional: https://releases.example.com/nex-runner-{os}-{arch}'
                          : '可选：https://example.com/releases/nex-runner-{os}-{arch}'
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
                  onClick={() => setAdvancedOpen((v) => !v)}
                  className="inline-flex items-center gap-1 text-ui-xs font-medium text-foreground-subtle transition-colors hover:text-foreground"
                >
                  {advancedOpen ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
                  <span>{isEn ? 'Advanced options' : '高级选项'}</span>
                </button>
                {advancedOpen && (
                  <div className="mt-2.5 grid grid-cols-1 gap-3 sm:grid-cols-2">
                    <label className="block space-y-1">
                      <span className="text-ui-xs text-foreground-subtle">
                        {isEn ? 'Default remote workspace' : '远端默认工作目录'}
                      </span>
                      <input
                        value={form.workspace_path}
                        onChange={(e) => setForm((p) => ({ ...p, workspace_path: e.target.value }))}
                        placeholder="/home/user/workspace"
                        className={settingsMonoInputClass}
                      />
                    </label>
                    <label className="block space-y-1">
                      <span className="text-ui-xs text-foreground-subtle">
                        {isEn ? 'Remote install directory' : '远端安装目录'}
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
                        {isEn ? 'Server WebSocket URL override' : 'Server WebSocket 回连地址覆盖（可选）'}
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
                {isEn ? 'Cancel' : '取消'}
              </button>
              <button
                type="submit"
                data-testid="ssh-save-btn"
                disabled={saving}
                className="h-8 rounded-lg border border-border bg-card px-3 text-ui-sm font-medium text-foreground transition-colors hover:bg-hover disabled:opacity-50"
              >
                {isEn ? 'Save' : '仅保存'}
              </button>
              <button
                type="button"
                data-testid="ssh-save-deploy-btn"
                disabled={saving}
                onClick={(e) => void submitForm(e, true)}
                className="inline-flex h-8 items-center gap-1.5 rounded-lg bg-foreground px-3.5 text-ui-sm font-medium text-background transition-opacity hover:opacity-90 disabled:opacity-50"
              >
                {saving && <LoaderCircle className="size-3.5 animate-spin" />}
                <span>{isEn ? 'Save & deploy' : '保存并部署'}</span>
              </button>
            </div>
          </form>
        </div>
      )}
    </div>
  );
}
