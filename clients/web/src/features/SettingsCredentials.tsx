import {
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  FileCode2,
  KeyRound,
  LoaderCircle,
  Pencil,
  Play,
  Plus,
  Shield,
  Sparkles,
  Trash2,
  X,
  XCircle,
} from 'lucide-react';
import { useMemo, useState } from 'react';
import { apiFetch } from '../core/protocol';
import { useNexStore } from '../core/store';
import type {
  CredentialEntry,
  CredentialProbeResult,
  CredentialRecord,
  CredentialScope,
  CredentialStorageMode,
  CredentialUsagePolicy,
} from '../core/types';
import {
  SectionHeading,
  SettingsGroupCard,
  Toggle,
  settingsInputClass,
  settingsMonoInputClass,
  settingsSelectClass,
} from './SettingsPage';

interface CLIPreset {
  id: string;
  title: string;
  description: string;
  probeCommand: string;
  keys: Array<{ key: string; note: string }>;
}

const CLI_PRESETS: readonly CLIPreset[] = [
  {
    id: 'github',
    title: 'GitHub CLI (gh)',
    description: 'Issues, PRs, Releases, and GitHub Actions via gh CLI.',
    probeCommand: 'gh auth status',
    keys: [
      { key: 'GH_TOKEN', note: 'Fine-grained PAT or classic token' },
      { key: 'GH_HOST', note: 'Optional enterprise host (default github.com)' },
    ],
  },
  {
    id: 'aws',
    title: 'AWS CLI',
    description: 'AWS STS, S3, CloudWatch, and infrastructure commands.',
    probeCommand: 'aws sts get-caller-identity',
    keys: [
      { key: 'AWS_ACCESS_KEY_ID', note: 'IAM access key ID' },
      { key: 'AWS_SECRET_ACCESS_KEY', note: 'IAM secret access key' },
      { key: 'AWS_REGION', note: 'Default region (e.g. us-east-1)' },
    ],
  },
  {
    id: 'cloudflare',
    title: 'Cloudflare (wrangler)',
    description: 'Workers, Pages, R2, and DNS management.',
    probeCommand: 'wrangler whoami',
    keys: [
      { key: 'CLOUDFLARE_API_TOKEN', note: 'Scoped API token' },
      { key: 'CLOUDFLARE_ACCOUNT_ID', note: 'Account ID' },
    ],
  },
  {
    id: 'kubectl',
    title: 'Kubernetes (kubectl)',
    description: 'Cluster inspection and deployments (supports per-runner override).',
    probeCommand: 'kubectl cluster-info',
    keys: [{ key: 'KUBECONFIG', note: 'Path to kubeconfig on target runner' }],
  },
  {
    id: 'npm',
    title: 'npm Registry',
    description: 'Publish or install private npm packages.',
    probeCommand: 'npm whoami',
    keys: [{ key: 'NPM_TOKEN', note: 'Automation or granular access token' }],
  },
  {
    id: 'sentry',
    title: 'Sentry CLI',
    description: 'Inspect issues, releases, and upload sourcemaps.',
    probeCommand: 'sentry-cli info',
    keys: [
      { key: 'SENTRY_AUTH_TOKEN', note: 'Auth token' },
      { key: 'SENTRY_ORG', note: 'Organization slug' },
    ],
  },
];

interface DraftEntry {
  key: string;
  value: string;
  value_hint?: string;
  has_value?: boolean;
  clear_value?: boolean;
  note: string;
}

interface CredentialFormState {
  id: string;
  name: string;
  description: string;
  preset: string;
  storage_mode: CredentialStorageMode;
  usage_policy: CredentialUsagePolicy;
  scope: CredentialScope;
  workspace_path: string;
  target_runners: string[];
  entries: DraftEntry[];
  probe_command: string;
  enabled: boolean;
}

const EMPTY_FORM: CredentialFormState = {
  id: '',
  name: '',
  description: '',
  preset: '',
  storage_mode: 'server_synced',
  usage_policy: 'all',
  scope: 'global',
  workspace_path: '',
  target_runners: [],
  entries: [{ key: '', value: '', note: '' }],
  probe_command: '',
  enabled: true,
};

export function SettingsCredentialsSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const { locale, credentials, runners, selectedWorkspacePath, refreshRegistries } = useNexStore();
  const isZh = locale === 'zh-CN';

  const [scopeFilter, setScopeFilter] = useState<'all' | 'global' | 'workspace' | 'runner'>('all');
  const [runnerFilter, setRunnerFilter] = useState<string>('');
  const [expandedId, setExpandedId] = useState<string | null>(null);

  const [editorOpen, setEditorOpen] = useState(false);
  const [form, setForm] = useState<CredentialFormState>(EMPTY_FORM);
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState('');

  // Bulk .env import modal state
  const [importOpen, setImportOpen] = useState(false);
  const [importName, setImportName] = useState('');
  const [importScope, setImportScope] = useState<CredentialScope>('global');
  const [importPolicy, setImportPolicy] = useState<CredentialUsagePolicy>('all');
  const [importWorkspace, setImportWorkspace] = useState(selectedWorkspacePath || '');
  const [importRaw, setImportRaw] = useState('');
  const [importMerge, setImportMerge] = useState(true);
  const [importing, setImporting] = useState(false);
  const [importError, setImportError] = useState('');

  // Runner-local push & probe state
  const [localValuesDraft, setLocalValuesDraft] = useState<Record<string, Record<string, string>>>({});
  const [localBusyKey, setLocalBusyKey] = useState<string | null>(null);
  const [probeBusyKey, setProbeBusyKey] = useState<string | null>(null);
  const [probeResults, setProbeResults] = useState<Record<string, CredentialProbeResult>>({});

  const visibleCredentials = useMemo(() => {
    return credentials.filter((c) => {
      if (scopeFilter !== 'all' && c.scope !== scopeFilter) return false;
      if (runnerFilter) {
        if (c.target_runners && c.target_runners.length > 0 && !c.target_runners.includes(runnerFilter)) {
          return false;
        }
      }
      return true;
    });
  }, [credentials, scopeFilter, runnerFilter]);

  const startCreate = (preset?: CLIPreset) => {
    setFormError('');
    if (preset) {
      setForm({
        ...EMPTY_FORM,
        name: preset.title,
        description: preset.description,
        preset: preset.id,
        probe_command: preset.probeCommand,
        entries: preset.keys.map((k) => ({ key: k.key, value: '', note: k.note })),
      });
    } else {
      setForm({
        ...EMPTY_FORM,
        workspace_path: selectedWorkspacePath || '',
      });
    }
    setEditorOpen(true);
  };

  const startEdit = (c: CredentialRecord) => {
    setFormError('');
    setForm({
      id: c.id,
      name: c.name,
      description: c.description || '',
      preset: c.preset || '',
      storage_mode: c.storage_mode || 'server_synced',
      usage_policy: c.usage_policy || 'all',
      scope: c.scope || 'global',
      workspace_path: c.workspace_path || '',
      target_runners: c.target_runners || [],
      entries:
        c.entries && c.entries.length > 0
          ? c.entries.map((e) => ({
              key: e.key,
              value: '',
              value_hint: e.value_hint,
              has_value: e.has_value,
              clear_value: false,
              note: e.note || '',
            }))
          : [{ key: '', value: '', note: '' }],
      probe_command: c.probe_command || '',
      enabled: c.enabled,
    });
    setEditorOpen(true);
  };

  const toggleTargetRunner = (rid: string) => {
    setForm((prev) => {
      const exists = prev.target_runners.includes(rid);
      return {
        ...prev,
        target_runners: exists
          ? prev.target_runners.filter((id) => id !== rid)
          : [...prev.target_runners, rid],
      };
    });
  };

  const saveForm = async () => {
    setFormError('');
    if (!form.name.trim()) {
      setFormError(isZh ? '请填写凭证分组名称' : 'Credential group name is required');
      return;
    }
    const entries: CredentialEntry[] = form.entries
      .map((e) => ({
        key: e.key.trim(),
        value: e.value,
        clear_value: e.clear_value,
        note: e.note.trim(),
      }))
      .filter((e) => e.key !== '');
    if (entries.length === 0) {
      setFormError(isZh ? '请至少添加一个环境变量 Key' : 'Add at least one environment variable key');
      return;
    }

    setSaving(true);
    try {
      await apiFetch('/api/v1/credentials', {
        method: 'POST',
        body: JSON.stringify({
          id: form.id || undefined,
          name: form.name.trim(),
          description: form.description.trim(),
          preset: form.preset,
          storage_mode: form.storage_mode,
          usage_policy: form.usage_policy,
          scope: form.scope,
          workspace_path: form.scope === 'workspace' ? form.workspace_path.trim() : '',
          target_runners: form.target_runners,
          entries,
          probe_command: form.probe_command.trim(),
          enabled: form.enabled,
        }),
      });
      await refreshRegistries();
      setEditorOpen(false);
      onFlash(isZh ? '凭证已加密保存并同步至目标 Runner' : 'Credentials encrypted and synced to target runners');
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const toggleEnabled = async (c: CredentialRecord, nextEnabled: boolean) => {
    await apiFetch('/api/v1/credentials', {
      method: 'POST',
      body: JSON.stringify({ ...c, enabled: nextEnabled }),
    });
    await refreshRegistries();
    onFlash(
      nextEnabled
        ? isZh
          ? `已启用「${c.name}」`
          : `Enabled "${c.name}"`
        : isZh
          ? `已停用「${c.name}」`
          : `Disabled "${c.name}"`,
    );
  };

  const removeCredential = async (c: CredentialRecord) => {
    await apiFetch(`/api/v1/credentials/${encodeURIComponent(c.id)}`, { method: 'DELETE' });
    await refreshRegistries();
    onFlash(isZh ? `已删除「${c.name}」` : `Deleted "${c.name}"`);
  };

  const runImportEnv = async () => {
    setImportError('');
    if (!importRaw.trim()) {
      setImportError(isZh ? '请粘贴 .env 内容' : 'Paste .env content first');
      return;
    }
    setImporting(true);
    try {
      const res = await apiFetch<{ imported_count: number }>('/api/v1/credentials/import-env', {
        method: 'POST',
        body: JSON.stringify({
          name: importName.trim() || 'Imported .env',
          storage_mode: 'server_synced',
          usage_policy: importPolicy,
          scope: importScope,
          workspace_path: importScope === 'workspace' ? importWorkspace.trim() : '',
          raw_env: importRaw,
          merge: importMerge,
        }),
      });
      await refreshRegistries();
      setImportOpen(false);
      setImportRaw('');
      setImportName('');
      onFlash(
        isZh
          ? `已加密导入 ${res.imported_count} 个环境变量`
          : `Encrypted & imported ${res.imported_count} variables`,
      );
    } catch (err) {
      setImportError(err instanceof Error ? err.message : String(err));
    } finally {
      setImporting(false);
    }
  };

  const pushLocalValues = async (credId: string, runnerId: string) => {
    const key = `${credId}::${runnerId}`;
    const values = localValuesDraft[key] || {};
    const filtered: Record<string, string> = {};
    for (const [k, v] of Object.entries(values)) {
      if (v.trim() !== '') filtered[k] = v;
    }
    if (Object.keys(filtered).length === 0) return;
    setLocalBusyKey(key);
    try {
      await apiFetch(`/api/v1/credentials/${encodeURIComponent(credId)}/local-values`, {
        method: 'POST',
        body: JSON.stringify({ runner_id: runnerId, values: filtered }),
      });
      setLocalValuesDraft((prev) => ({ ...prev, [key]: {} }));
      onFlash(
        isZh
          ? `已直接写入 ${runnerId} 本地加密库（未落 Server 磁盘）`
          : `Written directly to ${runnerId} local encrypted vault`,
      );
    } catch (err) {
      onFlash(err instanceof Error ? err.message : String(err));
    } finally {
      setLocalBusyKey(null);
    }
  };

  const runProbe = async (c: CredentialRecord, runnerId: string) => {
    const key = `${c.id}::${runnerId}`;
    setProbeBusyKey(key);
    try {
      const res = await apiFetch<CredentialProbeResult>(
        `/api/v1/credentials/${encodeURIComponent(c.id)}/probe`,
        {
          method: 'POST',
          body: JSON.stringify({
            runner_id: runnerId,
            workspace_path: c.workspace_path || selectedWorkspacePath || '',
            command: c.probe_command,
          }),
        },
      );
      setProbeResults((prev) => ({ ...prev, [key]: res }));
    } catch (err) {
      setProbeResults((prev) => ({
        ...prev,
        [key]: {
          ok: false,
          exit_code: -1,
          error: err instanceof Error ? err.message : String(err),
          latency_ms: 0,
        },
      }));
    } finally {
      setProbeBusyKey(null);
    }
  };

  const policyLabel = (p: CredentialUsagePolicy) => {
    if (p === 'chat_only') return isZh ? '仅交互对话' : 'Chat only';
    if (p === 'cron_explicit') return isZh ? '仅定时任务显式授权' : 'Cron explicit';
    return isZh ? '对话 + 定时任务' : 'Chat + Cron';
  };

  const storageLabel = (m: CredentialStorageMode) =>
    m === 'runner_local'
      ? isZh
        ? 'Runner 本地直存'
        : 'Runner-local only'
      : isZh
        ? 'Server 加密同步'
        : 'Server encrypted sync';

  return (
    <div className="space-y-6" data-testid="settings-credentials-section">
      {/* Top toolbar */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-1.5">
          {(['all', 'global', 'workspace', 'runner'] as const).map((sc) => (
            <button
              key={sc}
              type="button"
              onClick={() => setScopeFilter(sc)}
              className={`h-8 rounded-lg px-3 text-ui-xs font-medium transition-colors ${
                scopeFilter === sc
                  ? 'bg-foreground text-background'
                  : 'border border-border bg-card text-foreground-subtle hover:bg-hover hover:text-foreground'
              }`}
            >
              {sc === 'all'
                ? isZh
                  ? '全部'
                  : 'All'
                : sc === 'global'
                  ? isZh
                    ? '全局'
                    : 'Global'
                  : sc === 'workspace'
                    ? isZh
                      ? '工作区'
                      : 'Workspace'
                    : isZh
                      ? '指定 Runner'
                      : 'Per-Runner'}
            </button>
          ))}

          {runners.length > 0 && (
            <select
              value={runnerFilter}
              onChange={(e) => setRunnerFilter(e.target.value)}
              className="h-8 rounded-lg border border-border bg-input px-2.5 text-ui-xs text-foreground"
            >
              <option value="">{isZh ? '所有 Runner 节点' : 'All Runners'}</option>
              {runners.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name || r.id} ({r.status})
                </option>
              ))}
            </select>
          )}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            data-testid="credentials-import-env-btn"
            onClick={() => {
              setImportError('');
              setImportOpen(true);
            }}
            className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border bg-card px-3 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover"
          >
            <FileCode2 className="size-3.5" />
            <span>{isZh ? '粘贴 .env 导入' : 'Paste .env'}</span>
          </button>
          <button
            type="button"
            data-testid="credentials-new-btn"
            onClick={() => startCreate()}
            className="inline-flex h-8 items-center gap-1.5 rounded-lg bg-primary px-3 text-ui-xs font-medium text-primary-foreground transition-opacity hover:opacity-90"
          >
            <Plus className="size-3.5" />
            <span>{isZh ? '新建凭证分组' : 'New Credential'}</span>
          </button>
        </div>
      </div>

      {/* Quick CLI Presets strip */}
      <SettingsGroupCard>
        <div className="px-5 py-3.5">
          <div className="flex items-center gap-1.5 text-ui-xs font-medium text-foreground-subtle">
            <Sparkles className="size-3.5 text-brand-blue" />
            <span>{isZh ? '常用 CLI 预设模板（含只读鉴权验活探针）' : 'CLI Presets (with read-only auth probe)'}</span>
          </div>
          <div className="mt-2.5 flex flex-wrap gap-2">
            {CLI_PRESETS.map((preset) => (
              <button
                key={preset.id}
                type="button"
                onClick={() => startCreate(preset)}
                className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-surface px-2.5 py-1.5 text-ui-xs font-medium text-foreground transition-colors hover:border-border-hover hover:bg-hover"
              >
                <KeyRound className="size-3 text-foreground-subtlest" />
                <span>{preset.title}</span>
              </button>
            ))}
          </div>
        </div>
      </SettingsGroupCard>

      {/* Bulk .env import panel */}
      {importOpen && (
        <SettingsGroupCard>
          <div className="space-y-4 px-5 py-4">
            <div className="flex items-center justify-between">
              <div className="text-ui-base font-medium text-foreground">
                {isZh ? '批量粘贴 .env 导入' : 'Bulk Paste .env Import'}
              </div>
              <button
                type="button"
                onClick={() => setImportOpen(false)}
                className="rounded-md p-1 text-foreground-subtlest hover:bg-hover hover:text-foreground"
              >
                <X className="size-4" />
              </button>
            </div>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              <label className="space-y-1">
                <span className="text-ui-xs text-foreground-subtle">{isZh ? '分组名称' : 'Group Name'}</span>
                <input
                  value={importName}
                  onChange={(e) => setImportName(e.target.value)}
                  placeholder="e.g. Project Staging Env"
                  className={settingsInputClass}
                />
              </label>
              <label className="space-y-1">
                <span className="text-ui-xs text-foreground-subtle">{isZh ? '作用域' : 'Scope'}</span>
                <select
                  value={importScope}
                  onChange={(e) => setImportScope(e.target.value as CredentialScope)}
                  className={settingsSelectClass}
                >
                  <option value="global">{isZh ? '全局' : 'Global'}</option>
                  <option value="workspace">{isZh ? '指定工作区' : 'Workspace'}</option>
                </select>
              </label>
              <label className="space-y-1">
                <span className="text-ui-xs text-foreground-subtle">{isZh ? '使用策略' : 'Usage Policy'}</span>
                <select
                  value={importPolicy}
                  onChange={(e) => setImportPolicy(e.target.value as CredentialUsagePolicy)}
                  className={settingsSelectClass}
                >
                  <option value="all">{policyLabel('all')}</option>
                  <option value="chat_only">{policyLabel('chat_only')}</option>
                  <option value="cron_explicit">{policyLabel('cron_explicit')}</option>
                </select>
              </label>
            </div>
            {importScope === 'workspace' && (
              <label className="block space-y-1">
                <span className="text-ui-xs text-foreground-subtle">{isZh ? '工作区路径' : 'Workspace Path'}</span>
                <input
                  value={importWorkspace}
                  onChange={(e) => setImportWorkspace(e.target.value)}
                  placeholder="/path/to/workspace"
                  className={settingsMonoInputClass}
                />
              </label>
            )}
            <textarea
              rows={6}
              value={importRaw}
              onChange={(e) => setImportRaw(e.target.value)}
              placeholder={'# Paste .env lines\nGH_TOKEN=ghp_...\nAWS_REGION=us-east-1'}
              className="w-full rounded-lg border border-border bg-input p-3 font-mono text-ui-xs text-foreground placeholder-foreground-subtlest"
            />
            {importError && <p className="text-ui-xs text-destructive">{importError}</p>}
            <div className="flex flex-wrap items-center justify-between gap-2">
              <label className="inline-flex items-center gap-2 text-ui-xs text-foreground-subtle">
                <input
                  type="checkbox"
                  checked={importMerge}
                  onChange={(e) => setImportMerge(e.target.checked)}
                />
                <span>{isZh ? '若同名分组已存在则合并覆盖' : 'Merge into existing group with same name'}</span>
              </label>
              <button
                type="button"
                disabled={importing}
                onClick={() => void runImportEnv()}
                className="h-8 rounded-lg bg-primary px-3.5 text-ui-xs font-medium text-primary-foreground hover:opacity-90 disabled:opacity-50"
              >
                {importing ? (isZh ? '导入中...' : 'Importing...') : isZh ? '加密导入' : 'Encrypt & Import'}
              </button>
            </div>
          </div>
        </SettingsGroupCard>
      )}

      {/* Create / Edit form */}
      {editorOpen && (
        <SettingsGroupCard>
          <div className="space-y-4 px-5 py-4">
            <div className="flex items-center justify-between">
              <div className="text-ui-base font-medium text-foreground">
                {form.id
                  ? isZh
                    ? `编辑凭证分组：${form.name}`
                    : `Edit Credential Group: ${form.name}`
                  : isZh
                    ? '新建凭证分组'
                    : 'New Credential Group'}
              </div>
              <button
                type="button"
                onClick={() => setEditorOpen(false)}
                className="rounded-md p-1 text-foreground-subtlest hover:bg-hover hover:text-foreground"
              >
                <X className="size-4" />
              </button>
            </div>

            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <label className="space-y-1">
                <span className="text-ui-xs text-foreground-subtle">{isZh ? '分组名称' : 'Name'}</span>
                <input
                  data-testid="cred-form-name"
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="e.g. GitHub CLI"
                  className={settingsInputClass}
                />
              </label>
              <label className="space-y-1">
                <span className="text-ui-xs text-foreground-subtle">{isZh ? '备注说明' : 'Description'}</span>
                <input
                  value={form.description}
                  onChange={(e) => setForm({ ...form, description: e.target.value })}
                  placeholder={isZh ? '可选用途说明' : 'Optional description'}
                  className={settingsInputClass}
                />
              </label>
            </div>

            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              <label className="space-y-1">
                <span className="text-ui-xs text-foreground-subtle">{isZh ? '存储与同步模式' : 'Storage Mode'}</span>
                <select
                  value={form.storage_mode}
                  onChange={(e) => setForm({ ...form, storage_mode: e.target.value as CredentialStorageMode })}
                  className={settingsSelectClass}
                >
                  <option value="server_synced">{storageLabel('server_synced')}</option>
                  <option value="runner_local">{storageLabel('runner_local')}</option>
                </select>
              </label>
              <label className="space-y-1">
                <span className="text-ui-xs text-foreground-subtle">{isZh ? '执行场景策略' : 'Usage Policy'}</span>
                <select
                  value={form.usage_policy}
                  onChange={(e) => setForm({ ...form, usage_policy: e.target.value as CredentialUsagePolicy })}
                  className={settingsSelectClass}
                >
                  <option value="all">{policyLabel('all')}</option>
                  <option value="chat_only">{policyLabel('chat_only')}</option>
                  <option value="cron_explicit">{policyLabel('cron_explicit')}</option>
                </select>
              </label>
              <label className="space-y-1">
                <span className="text-ui-xs text-foreground-subtle">{isZh ? '作用域' : 'Scope'}</span>
                <select
                  value={form.scope}
                  onChange={(e) => setForm({ ...form, scope: e.target.value as CredentialScope })}
                  className={settingsSelectClass}
                >
                  <option value="global">{isZh ? '全局' : 'Global'}</option>
                  <option value="workspace">{isZh ? '指定工作区' : 'Workspace'}</option>
                  <option value="runner">{isZh ? '指定 Runner' : 'Specific Runners'}</option>
                </select>
              </label>
            </div>

            {form.scope === 'workspace' && (
              <label className="block space-y-1">
                <span className="text-ui-xs text-foreground-subtle">{isZh ? '绑定工作区路径' : 'Workspace Path'}</span>
                <input
                  value={form.workspace_path}
                  onChange={(e) => setForm({ ...form, workspace_path: e.target.value })}
                  placeholder="/Users/you/Projects/app"
                  className={settingsMonoInputClass}
                />
              </label>
            )}

            {runners.length > 0 && (
              <div className="space-y-1.5">
                <span className="text-ui-xs text-foreground-subtle">
                  {isZh
                    ? '目标 Runner（留空则对全部节点生效；指定节点时仅定向下发给选中节点）'
                    : 'Target Runners (leave empty for all runners)'}
                </span>
                <div className="flex flex-wrap gap-2">
                  {runners.map((r) => {
                    const selected = form.target_runners.includes(r.id);
                    return (
                      <button
                        key={r.id}
                        type="button"
                        onClick={() => toggleTargetRunner(r.id)}
                        className={`rounded-lg border px-2.5 py-1 text-ui-xs transition-colors ${
                          selected
                            ? 'border-brand-blue bg-brand-blue/10 font-medium text-foreground'
                            : 'border-border bg-surface text-foreground-subtle hover:bg-hover'
                        }`}
                      >
                        {r.name || r.id} ({r.status})
                      </button>
                    );
                  })}
                </div>
              </div>
            )}

            {/* Variable rows */}
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <span className="text-ui-xs font-medium text-foreground-subtle">
                  {isZh
                    ? '环境变量列表（保存后不可回读明文；编辑时留空则保留原值）'
                    : 'Environment Variables (write-only; leave blank on edit to keep existing secret)'}
                </span>
                <button
                  type="button"
                  onClick={() =>
                    setForm((prev) => ({
                      ...prev,
                      entries: [...prev.entries, { key: '', value: '', note: '' }],
                    }))
                  }
                  className="inline-flex items-center gap-1 text-ui-xs font-medium text-brand-blue hover:underline"
                >
                  <Plus className="size-3" />
                  <span>{isZh ? '添加变量' : 'Add Variable'}</span>
                </button>
              </div>
              {form.entries.map((entry, idx) => (
                <div key={idx} className="grid grid-cols-1 gap-2 sm:grid-cols-[1fr_1.2fr_1fr_auto]">
                  <input
                    value={entry.key}
                    onChange={(e) => {
                      const next = [...form.entries];
                      next[idx] = { ...next[idx], key: e.target.value };
                      setForm({ ...form, entries: next });
                    }}
                    placeholder="ENV_KEY"
                    className={settingsMonoInputClass}
                  />
                  <input
                    type="password"
                    disabled={form.storage_mode === 'runner_local'}
                    value={entry.value}
                    onChange={(e) => {
                      const next = [...form.entries];
                      next[idx] = { ...next[idx], value: e.target.value, clear_value: false };
                      setForm({ ...form, entries: next });
                    }}
                    placeholder={
                      form.storage_mode === 'runner_local'
                        ? isZh
                          ? '保存后在下方按 Runner 直写'
                          : 'Set per-runner below after saving'
                        : entry.has_value
                          ? `${entry.value_hint || '••••••••'} (${isZh ? '留空保留' : 'keep'})`
                          : isZh
                            ? '输入密钥值'
                            : 'Secret value'
                    }
                    className={settingsMonoInputClass}
                  />
                  <input
                    value={entry.note}
                    onChange={(e) => {
                      const next = [...form.entries];
                      next[idx] = { ...next[idx], note: e.target.value };
                      setForm({ ...form, entries: next });
                    }}
                    placeholder={isZh ? '备注（可选）' : 'Note (optional)'}
                    className={settingsInputClass}
                  />
                  <button
                    type="button"
                    onClick={() =>
                      setForm((prev) => ({
                        ...prev,
                        entries: prev.entries.filter((_, i) => i !== idx),
                      }))
                    }
                    className="flex size-9 items-center justify-center rounded-lg border border-border text-foreground-subtlest hover:bg-destructive-surface hover:text-destructive-surface-fg"
                  >
                    <Trash2 className="size-3.5" />
                  </button>
                </div>
              ))}
            </div>

            <label className="block space-y-1">
              <span className="text-ui-xs text-foreground-subtle">
                {isZh ? '只读验活探针命令（可选，用于跨 Runner 一键测试鉴权）' : 'Read-only Probe Command (optional)'}
              </span>
              <input
                value={form.probe_command}
                onChange={(e) => setForm({ ...form, probe_command: e.target.value })}
                placeholder="e.g. gh auth status"
                className={settingsMonoInputClass}
              />
            </label>

            {formError && <p className="text-ui-xs text-destructive">{formError}</p>}

            <div className="flex justify-end gap-2">
              <button
                type="button"
                onClick={() => setEditorOpen(false)}
                className="h-8 rounded-lg border border-border px-3 text-ui-xs font-medium text-foreground-subtle hover:bg-hover"
              >
                {isZh ? '取消' : 'Cancel'}
              </button>
              <button
                type="button"
                data-testid="cred-form-save"
                disabled={saving}
                onClick={() => void saveForm()}
                className="h-8 rounded-lg bg-primary px-3.5 text-ui-xs font-medium text-primary-foreground hover:opacity-90 disabled:opacity-50"
              >
                {saving ? (isZh ? '保存中...' : 'Saving...') : isZh ? '加密保存' : 'Encrypt & Save'}
              </button>
            </div>
          </div>
        </SettingsGroupCard>
      )}

      {/* Credential groups list */}
      <SectionHeading
        title={isZh ? `已配置凭证分组 (${visibleCredentials.length})` : `Configured Credentials (${visibleCredentials.length})`}
        description={
          isZh
            ? '所有注入的密钥值都会自动从工具输出、流式日志与落盘文件中脱敏，LLM 仅可见变量名。'
            : 'All injected secret values are automatically redacted from tool outputs, streams, and logs.'
        }
      />

      <SettingsGroupCard>
        {visibleCredentials.length === 0 ? (
          <div className="px-5 py-10 text-center text-ui-sm text-foreground-subtlest">
            {isZh
              ? '暂无凭证分组。可点击上方 CLI 预设快速创建，或直接粘贴现有 .env 文件批量导入。'
              : 'No credential groups yet. Use a CLI preset or paste an existing .env file to get started.'}
          </div>
        ) : (
          visibleCredentials.map((c) => {
            const isOpen = expandedId === c.id;
            const targetRunners =
              c.target_runners && c.target_runners.length > 0
                ? runners.filter((r) => c.target_runners?.includes(r.id))
                : runners;
            return (
              <div key={c.id} className="px-5 py-3.5">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <button
                    type="button"
                    onClick={() => setExpandedId(isOpen ? null : c.id)}
                    className="flex min-w-0 flex-1 items-center gap-2 text-left"
                  >
                    {isOpen ? (
                      <ChevronDown className="size-4 shrink-0 text-foreground-subtlest" />
                    ) : (
                      <ChevronRight className="size-4 shrink-0 text-foreground-subtlest" />
                    )}
                    <div className="min-w-0">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span className="text-ui-base font-medium text-foreground">{c.name}</span>
                        <span className="rounded-md border border-border bg-surface px-1.5 py-0.5 text-ui-xs text-foreground-subtle">
                          {storageLabel(c.storage_mode)}
                        </span>
                        <span className="rounded-md border border-border bg-surface px-1.5 py-0.5 text-ui-xs text-foreground-subtle">
                          {policyLabel(c.usage_policy)}
                        </span>
                        <span className="rounded-md border border-border bg-surface px-1.5 py-0.5 font-mono text-ui-xs text-foreground-subtle">
                          {c.entries.length} {isZh ? '项' : 'vars'}
                        </span>
                      </div>
                      {c.description && (
                        <p className="mt-0.5 truncate text-ui-xs text-foreground-subtlest">{c.description}</p>
                      )}
                    </div>
                  </button>

                  <div className="flex shrink-0 items-center gap-2">
                    <Toggle
                      checked={c.enabled}
                      onChange={(next) => void toggleEnabled(c, next)}
                      label={c.name}
                    />
                    <button
                      type="button"
                      onClick={() => startEdit(c)}
                      className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest hover:bg-hover hover:text-foreground"
                    >
                      <Pencil className="size-3.5" />
                    </button>
                    <button
                      type="button"
                      onClick={() => void removeCredential(c)}
                      className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest hover:bg-destructive-surface hover:text-destructive-surface-fg"
                    >
                      <Trash2 className="size-3.5" />
                    </button>
                  </div>
                </div>

                {isOpen && (
                  <div className="mt-3 space-y-3 border-t border-border pt-3">
                    {/* Entries summary */}
                    <div className="space-y-1.5">
                      {c.entries.map((e) => (
                        <div
                          key={e.key}
                          className="flex flex-wrap items-center justify-between gap-2 rounded-lg bg-surface px-3 py-2 text-ui-xs"
                        >
                          <div className="flex items-center gap-2">
                            <span className="font-mono font-medium text-foreground">{e.key}</span>
                            {e.note && <span className="text-foreground-subtlest">· {e.note}</span>}
                          </div>
                          <span className="font-mono text-foreground-subtle">
                            {c.storage_mode === 'runner_local'
                              ? isZh
                                ? '仅保存在目标 Runner 本地'
                                : 'Stored on Runner'
                              : e.has_value
                                ? e.value_hint || '••••••••'
                                : isZh
                                  ? '未设置值'
                                  : 'Unset'}
                          </span>
                        </div>
                      ))}
                    </div>

                    {/* Multi-runner status, local direct-write & probe */}
                    {targetRunners.length > 0 && (
                      <div className="space-y-2">
                        <div className="flex items-center gap-1.5 text-ui-xs font-medium text-foreground-subtle">
                          <Shield className="size-3.5" />
                          <span>{isZh ? '多 Runner 节点状态与验证' : 'Runner Node Status & Probe'}</span>
                        </div>
                        {targetRunners.map((r) => {
                          const rk = `${c.id}::${r.id}`;
                          const probe = probeResults[rk];
                          const isOnline = r.status === 'online';
                          return (
                            <div key={r.id} className="space-y-2 rounded-lg border border-border bg-background/40 p-3">
                              <div className="flex flex-wrap items-center justify-between gap-2">
                                <div className="flex items-center gap-2 text-ui-xs">
                                  <span
                                    className={`size-2 rounded-full ${isOnline ? 'bg-emerald-500' : 'bg-zinc-500'}`}
                                  />
                                  <span className="font-medium text-foreground">{r.name || r.id}</span>
                                  <span className="font-mono text-foreground-subtlest">
                                    {r.os}/{r.arch}
                                  </span>
                                </div>
                                {c.probe_command && (
                                  <button
                                    type="button"
                                    disabled={!isOnline || probeBusyKey === rk}
                                    onClick={() => void runProbe(c, r.id)}
                                    className="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-card px-2.5 text-ui-xs font-medium text-foreground hover:bg-hover disabled:opacity-45"
                                  >
                                    {probeBusyKey === rk ? (
                                      <LoaderCircle className="size-3 animate-spin" />
                                    ) : (
                                      <Play className="size-3" />
                                    )}
                                    <span>{isZh ? '验证鉴权' : 'Probe'}</span>
                                  </button>
                                )}
                              </div>

                              {/* Direct write to Runner local vault when storage_mode == runner_local */}
                              {c.storage_mode === 'runner_local' && isOnline && (
                                <div className="space-y-1.5 pt-1">
                                  {c.entries.map((e) => (
                                    <div key={e.key} className="flex items-center gap-2">
                                      <span className="w-40 shrink-0 truncate font-mono text-ui-xs text-foreground-subtle">
                                        {e.key}
                                      </span>
                                      <input
                                        type="password"
                                        value={localValuesDraft[rk]?.[e.key] || ''}
                                        onChange={(ev) =>
                                          setLocalValuesDraft((prev) => ({
                                            ...prev,
                                            [rk]: { ...(prev[rk] || {}), [e.key]: ev.target.value },
                                          }))
                                        }
                                        placeholder={isZh ? '直接写入此 Runner 本地加密库' : 'Write directly to runner vault'}
                                        className="h-7 min-w-0 flex-1 rounded-md border border-border bg-input px-2 font-mono text-ui-xs text-foreground"
                                      />
                                    </div>
                                  ))}
                                  <div className="flex justify-end">
                                    <button
                                      type="button"
                                      disabled={localBusyKey === rk}
                                      onClick={() => void pushLocalValues(c.id, r.id)}
                                      className="h-7 rounded-md bg-foreground px-2.5 text-ui-xs font-medium text-background hover:opacity-90 disabled:opacity-45"
                                    >
                                      {localBusyKey === rk
                                        ? isZh
                                          ? '写入中...'
                                          : 'Writing...'
                                        : isZh
                                          ? '直写到该 Runner'
                                          : 'Push to Runner'}
                                    </button>
                                  </div>
                                </div>
                              )}

                              {probe && (
                                <div
                                  className={`rounded-md px-2.5 py-2 font-mono text-ui-xs ${
                                    probe.ok
                                      ? 'bg-emerald-500/10 text-emerald-400'
                                      : 'bg-destructive-surface text-destructive-surface-fg'
                                  }`}
                                >
                                  <div className="flex items-center gap-1.5 font-sans font-medium">
                                    {probe.ok ? (
                                      <CheckCircle2 className="size-3.5" />
                                    ) : (
                                      <XCircle className="size-3.5" />
                                    )}
                                    <span>
                                      {probe.ok
                                        ? isZh
                                          ? `鉴权通过 (${probe.latency_ms}ms)`
                                          : `Auth OK (${probe.latency_ms}ms)`
                                        : isZh
                                          ? `鉴权失败 (exit ${probe.exit_code})`
                                          : `Auth failed (exit ${probe.exit_code})`}
                                    </span>
                                  </div>
                                  {(probe.output || probe.error) && (
                                    <pre className="mt-1 max-h-28 overflow-auto whitespace-pre-wrap text-[11px] opacity-90">
                                      {probe.output || probe.error}
                                    </pre>
                                  )}
                                </div>
                              )}
                            </div>
                          );
                        })}
                      </div>
                    )}
                  </div>
                )}
              </div>
            );
          })
        )}
      </SettingsGroupCard>
    </div>
  );
}
