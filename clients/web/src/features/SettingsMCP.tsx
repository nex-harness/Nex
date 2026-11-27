import {
  AlertCircle,
  Cable,
  ChevronRight,
  Folder,
  LoaderCircle,
  PackagePlus,
  Pencil,
  Plus,
  RefreshCw,
  RotateCcw,
  Search,
  Trash2,
  UserRound,
  Wrench,
  X,
} from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { apiFetch } from '../core/protocol';
import { useNexStore } from '../core/store';
import type { MCPExposure, MCPProbeResult, MCPServerRecord, MCPToolEntry } from '../core/types';
import { formatMsg } from '../i18n/messages';
import {
  SectionHeading,
  SettingsGroupCard,
  settingsInputClass,
  settingsMonoInputClass,
  settingsSelectClass,
} from './SettingsPage';

interface MCPPreset {
  id: string;
  title: string;
  name: string;
  description: string;
  transport: 'stdio' | 'http';
  host_mode: 'runner_local' | 'server_hosted';
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  url?: string;
  headers?: Record<string, string>;
  exposure: MCPExposure;
}

const MCP_PRESETS: readonly MCPPreset[] = [
  {
    id: 'filesystem',
    title: 'Filesystem',
    name: 'filesystem',
    description: 'Read, write, and inspect files inside allowed local directories.',
    transport: 'stdio',
    host_mode: 'runner_local',
    command: 'npx',
    args: ['-y', '@modelcontextprotocol/server-filesystem', '.'],
    exposure: 'codemode',
  },
  {
    id: 'playwright',
    title: 'Playwright Browser',
    name: 'playwright',
    description: 'Headless browser automation, DOM snapshots, screenshots, and web testing.',
    transport: 'stdio',
    host_mode: 'runner_local',
    command: 'npx',
    args: ['-y', '@playwright/mcp@latest'],
    exposure: 'codemode',
  },
  {
    id: 'context7',
    title: 'Context7 Docs',
    name: 'context7',
    description: 'Fetch up-to-date library documentation and code examples.',
    transport: 'stdio',
    host_mode: 'runner_local',
    command: 'npx',
    args: ['-y', '@upstash/context7-mcp@latest'],
    exposure: 'deferred',
  },
  {
    id: 'github',
    title: 'GitHub (Streamable HTTP)',
    name: 'github',
    description: 'Search repositories, issues, pull requests, and workflows.',
    transport: 'http',
    host_mode: 'server_hosted',
    url: 'https://api.githubcopilot.com/mcp/',
    headers: { Authorization: 'Bearer ${GITHUB_TOKEN}' },
    exposure: 'deferred',
  },
  {
    id: 'fetch',
    title: 'Web Fetch',
    name: 'fetch',
    description: 'Fetch and convert web pages to clean markdown.',
    transport: 'stdio',
    host_mode: 'runner_local',
    command: 'uvx',
    args: ['mcp-server-fetch'],
    exposure: 'codemode',
  },
  {
    id: 'postgres',
    title: 'PostgreSQL',
    name: 'postgres',
    description: 'Read-only SQL schema inspection and queries against PostgreSQL.',
    transport: 'stdio',
    host_mode: 'runner_local',
    command: 'npx',
    args: ['-y', '@modelcontextprotocol/server-postgres', '${DATABASE_URL}'],
    exposure: 'codemode',
  },
  {
    id: 'sequential-thinking',
    title: 'Sequential Thinking',
    name: 'sequential-thinking',
    description: 'Structured step-by-step reasoning scratchpad tool.',
    transport: 'stdio',
    host_mode: 'runner_local',
    command: 'npx',
    args: ['-y', '@modelcontextprotocol/server-sequential-thinking'],
    exposure: 'direct',
  },
  {
    id: 'sentry',
    title: 'Sentry (Streamable HTTP)',
    name: 'sentry',
    description: 'Inspect production issues, stack traces, and release health.',
    transport: 'http',
    host_mode: 'server_hosted',
    url: 'https://mcp.sentry.dev/mcp',
    headers: { Authorization: 'Bearer ${SENTRY_AUTH_TOKEN}' },
    exposure: 'deferred',
  },
];

interface MCPFormState {
  name: string;
  description: string;
  scope: 'global' | 'workspace';
  workspace_path: string;
  host_mode: 'runner_local' | 'server_hosted';
  transport: 'stdio' | 'http';
  command: string;
  argsText: string;
  envJson: string;
  url: string;
  headersJson: string;
  exposure: MCPExposure;
  idle_ttl_sec: number;
  enabled: boolean;
}

const EMPTY_FORM: MCPFormState = {
  name: '',
  description: '',
  scope: 'global',
  workspace_path: '',
  host_mode: 'runner_local',
  transport: 'stdio',
  command: 'npx',
  argsText: '',
  envJson: '',
  url: '',
  headersJson: '',
  exposure: 'codemode',
  idle_ttl_sec: 300,
  enabled: true,
};

function workspaceLeaf(p: string): string {
  const parts = p.split(/[\\/]/).filter(Boolean);
  return parts[parts.length - 1] || p;
}

function recordToForm(m: MCPServerRecord): MCPFormState {
  return {
    name: m.name,
    description: m.description || '',
    scope: m.scope === 'workspace' ? 'workspace' : 'global',
    workspace_path: m.workspace_path || '',
    host_mode: m.host_mode === 'server_hosted' ? 'server_hosted' : 'runner_local',
    transport: m.transport === 'http' ? 'http' : 'stdio',
    command: m.command || '',
    argsText: (m.args || []).join(' '),
    envJson: m.env && Object.keys(m.env).length > 0 ? JSON.stringify(m.env, null, 2) : '',
    url: m.url || '',
    headersJson:
      m.headers && Object.keys(m.headers).length > 0 ? JSON.stringify(m.headers, null, 2) : '',
    exposure: (m.exposure || 'codemode') as MCPExposure,
    idle_ttl_sec: m.idle_ttl_sec || 300,
    enabled: m.enabled,
  };
}

function formToJsonSnippet(f: MCPFormState): string {
  const entry: Record<string, unknown> = {
    type: f.transport,
    exposure: f.exposure,
    enabled: f.enabled,
  };
  if (f.description.trim()) entry.description = f.description.trim();
  if (f.transport === 'http') {
    entry.url = f.url.trim();
    if (f.headersJson.trim()) {
      try {
        entry.headers = JSON.parse(f.headersJson);
      } catch {
        // ignore invalid intermediate json
      }
    }
  } else {
    entry.command = f.command.trim();
    const args = f.argsText.split(/\s+/).filter(Boolean);
    if (args.length > 0) entry.args = args;
    if (f.envJson.trim()) {
      try {
        entry.env = JSON.parse(f.envJson);
      } catch {
        // ignore
      }
    }
  }
  return JSON.stringify({ mcpServers: { [f.name.trim() || 'my-mcp-server']: entry } }, null, 2);
}

function isEffectiveEnabled(m: MCPServerRecord, workspacePath: string): { enabled: boolean; overridden: boolean } {
  const ws = workspacePath.trim().replace(/\/+$/, '');
  if (ws && m.workspace_overrides && Object.prototype.hasOwnProperty.call(m.workspace_overrides, ws)) {
    return { enabled: Boolean(m.workspace_overrides[ws]), overridden: true };
  }
  if (m.scope === 'workspace' && m.workspace_path) {
    const bound = m.workspace_path.trim().replace(/\/+$/, '');
    if (ws && bound !== ws) return { enabled: false, overridden: false };
  }
  return { enabled: m.enabled, overridden: false };
}

function ToggleSwitch({
  checked,
  disabled,
  label,
  onChange,
}: {
  checked: boolean;
  disabled?: boolean;
  label: string;
  onChange: (next: boolean) => void;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={(e) => {
        e.stopPropagation();
        onChange(!checked);
      }}
      className={`relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition-colors ${
        checked
          ? 'border-primary bg-primary'
          : 'border-border bg-surface hover:border-foreground-subtlest'
      } ${disabled ? 'cursor-not-allowed opacity-50' : 'cursor-pointer'}`}
    >
      <span
        className={`inline-block size-3.5 rounded-full bg-white shadow-xs transition-transform ${
          checked ? 'translate-x-4' : 'translate-x-0.5'
        }`}
      />
    </button>
  );
}

export function MCPManagerSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const {
    locale,
    mcpServers,
    runners,
    sessions,
    recentWorkspaces,
    selectedRunnerId,
    selectedWorkspacePath,
    refreshRegistries,
  } = useNexStore();

  const knownWorkspaces = useMemo(() => {
    const set = new Set<string>();
    const push = (p?: string) => {
      const clean = (p || '').trim().replace(/\/+$/, '');
      if (clean) set.add(clean);
    };
    push(selectedWorkspacePath);
    for (const rw of recentWorkspaces) push(rw.path);
    for (const s of sessions) push(s.workspace_path);
    for (const r of runners) push(r.workspace_default);
    for (const m of mcpServers) push(m.workspace_path);
    return Array.from(set).sort((a, b) => a.localeCompare(b));
  }, [mcpServers, recentWorkspaces, runners, selectedWorkspacePath, sessions]);

  const [targetWorkspace, setTargetWorkspace] = useState<string>('');
  const [searchQuery, setSearchQuery] = useState('');
  const [expandedServer, setExpandedServer] = useState<string | null>(null);
  const [probingByName, setProbingByName] = useState<Record<string, boolean>>({});
  const [probingAll, setProbingAll] = useState(false);
  const [discoveredServers, setDiscoveredServers] = useState<
    Array<MCPServerRecord & { source: 'workspace' | 'user'; source_path: string; source_root: string }>
  >([]);
  const [discovering, setDiscovering] = useState(false);

  const loadDiscovered = async (wsPath: string) => {
    setDiscovering(true);
    try {
      const params = new URLSearchParams();
      if (selectedRunnerId) params.set('runner_id', selectedRunnerId);
      if (wsPath) params.set('workspace_path', wsPath);
      const qs = params.toString();
      const res = await apiFetch<{
        servers?: Array<MCPServerRecord & { source: 'workspace' | 'user'; source_path: string; source_root: string }>;
      }>(`/api/v1/mcp/discover${qs ? `?${qs}` : ''}`);
      if (mountedRef.current) {
        setDiscoveredServers(Array.isArray(res?.servers) ? res.servers : []);
      }
    } catch {
      if (mountedRef.current) setDiscoveredServers([]);
    } finally {
      if (mountedRef.current) setDiscovering(false);
    }
  };

  useEffect(() => {
    void loadDiscovered(targetWorkspace || selectedWorkspacePath || '');
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [targetWorkspace, selectedRunnerId, selectedWorkspacePath]);

  // Install / Edit modal state
  const [modalOpen, setModalOpen] = useState(false);
  const [editingName, setEditingName] = useState<string | null>(null);
  const [modalTab, setModalTab] = useState<'catalog' | 'form' | 'json'>('catalog');
  const [form, setForm] = useState<MCPFormState>(EMPTY_FORM);
  const [jsonDraft, setJsonDraft] = useState('');
  const [formError, setFormError] = useState('');
  const [saving, setSaving] = useState(false);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const openInstallModal = () => {
    setEditingName(null);
    const nextForm: MCPFormState = {
      ...EMPTY_FORM,
      scope: targetWorkspace ? 'workspace' : 'global',
      workspace_path: targetWorkspace || '',
    };
    setForm(nextForm);
    setJsonDraft(
      '{\n  "mcpServers": {\n    "playwright": {\n      "command": "npx",\n      "args": ["-y", "@playwright/mcp@latest"],\n      "exposure": "codemode"\n    }\n  }\n}',
    );
    setFormError('');
    setModalTab('catalog');
    setModalOpen(true);
  };

  const openEditModal = (m: MCPServerRecord) => {
    const f = recordToForm(m);
    setEditingName(m.name);
    setForm(f);
    setJsonDraft(formToJsonSnippet(f));
    setFormError('');
    setModalTab('form');
    setModalOpen(true);
  };

  const applyPreset = (preset: MCPPreset) => {
    const nextForm: MCPFormState = {
      ...EMPTY_FORM,
      name: preset.name,
      description: preset.description,
      scope: targetWorkspace ? 'workspace' : 'global',
      workspace_path: targetWorkspace || '',
      host_mode: preset.host_mode,
      transport: preset.transport,
      command: preset.command || '',
      argsText: (preset.args || []).join(' '),
      envJson: preset.env ? JSON.stringify(preset.env, null, 2) : '',
      url: preset.url || '',
      headersJson: preset.headers ? JSON.stringify(preset.headers, null, 2) : '',
      exposure: preset.exposure,
      idle_ttl_sec: 300,
      enabled: true,
    };
    setForm(nextForm);
    setJsonDraft(formToJsonSnippet(nextForm));
    setFormError('');
    setModalTab('form');
  };

  const submitForm = async () => {
    setFormError('');
    const cleanName = form.name.trim();
    if (!cleanName) {
      setFormError('Server name is required');
      return;
    }
    let parsedEnv: Record<string, string> = {};
    if (form.transport === 'stdio' && form.envJson.trim()) {
      try {
        parsedEnv = JSON.parse(form.envJson);
      } catch (err) {
        setFormError(
          formatMsg(locale, 'mcp.modal.invalidJson', {
            error: err instanceof Error ? err.message : String(err),
          }),
        );
        return;
      }
    }
    let parsedHeaders: Record<string, string> = {};
    if (form.transport === 'http' && form.headersJson.trim()) {
      try {
        parsedHeaders = JSON.parse(form.headersJson);
      } catch (err) {
        setFormError(
          formatMsg(locale, 'mcp.modal.invalidJson', {
            error: err instanceof Error ? err.message : String(err),
          }),
        );
        return;
      }
    }

    setSaving(true);
    try {
      await apiFetch('/api/v1/mcp', {
        method: 'POST',
        body: JSON.stringify({
          name: cleanName,
          description: form.description.trim(),
          host_mode: form.host_mode,
          transport: form.transport,
          command: form.transport === 'stdio' ? form.command.trim() : '',
          args: form.transport === 'stdio' ? form.argsText.split(/\s+/).filter(Boolean) : [],
          env: form.transport === 'stdio' ? parsedEnv : {},
          url: form.transport === 'http' ? form.url.trim() : '',
          headers: form.transport === 'http' ? parsedHeaders : {},
          exposure: form.exposure,
          scope: form.scope,
          workspace_path: form.scope === 'workspace' ? form.workspace_path.trim() : '',
          idle_ttl_sec: Number(form.idle_ttl_sec) || 300,
          enabled: form.enabled,
        }),
      });
      await refreshRegistries();
      if (mountedRef.current) {
        setModalOpen(false);
      }
      onFlash(formatMsg(locale, 'capabilities.mcpServerSaved', { name: cleanName }));
    } catch (err) {
      if (mountedRef.current) {
        setFormError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      if (mountedRef.current) setSaving(false);
    }
  };

  const submitJsonImport = async () => {
    setFormError('');
    try {
      JSON.parse(jsonDraft);
    } catch (err) {
      setFormError(
        formatMsg(locale, 'mcp.modal.invalidJson', {
          error: err instanceof Error ? err.message : String(err),
        }),
      );
      return;
    }

    setSaving(true);
    try {
      const res = await apiFetch<{ count?: number }>('/api/v1/mcp/import', {
        method: 'POST',
        body: JSON.stringify({
          raw_json: jsonDraft,
          scope: form.scope,
          workspace_path: form.scope === 'workspace' ? form.workspace_path.trim() : '',
          host_mode: form.host_mode,
        }),
      });
      await refreshRegistries();
      if (mountedRef.current) {
        setModalOpen(false);
      }
      onFlash(formatMsg(locale, 'mcp.modal.importedFlash', { count: res?.count || 1 }));
    } catch (err) {
      if (mountedRef.current) {
        setFormError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      if (mountedRef.current) setSaving(false);
    }
  };

  const probeServer = async (name: string) => {
    setProbingByName((p) => ({ ...p, [name]: true }));
    try {
      const res = await apiFetch<{ probe?: MCPProbeResult }>(
        `/api/v1/mcp/${encodeURIComponent(name)}/test`,
        {
          method: 'POST',
          body: JSON.stringify({ runner_id: selectedRunnerId || '' }),
        },
      );
      await refreshRegistries();
      const pr = res?.probe;
      if (pr?.status === 'connected') {
        onFlash(
          formatMsg(locale, 'mcp.probeSuccess', {
            name,
            count: pr.tool_count,
            ms: pr.latency_ms,
          }),
        );
      } else if (pr?.error) {
        onFlash(formatMsg(locale, 'mcp.probeFailed', { name, error: pr.error }));
      }
    } catch (err) {
      onFlash(
        formatMsg(locale, 'mcp.probeFailed', {
          name,
          error: err instanceof Error ? err.message : String(err),
        }),
      );
    } finally {
      if (mountedRef.current) {
        setProbingByName((p) => ({ ...p, [name]: false }));
      }
    }
  };

  const probeAll = async () => {
    if (probingAll || mcpServers.length === 0) return;
    setProbingAll(true);
    try {
      for (const m of mcpServers) {
        if (!m.enabled) continue;
        await probeServer(m.name);
      }
    } finally {
      if (mountedRef.current) setProbingAll(false);
    }
  };

  const updateVisibility = async (
    name: string,
    patch: {
      scope_target?: 'global' | 'workspace' | '';
      workspace_path?: string;
      enabled?: boolean | null;
      exposure?: MCPExposure;
    },
  ) => {
    await apiFetch(`/api/v1/mcp/${encodeURIComponent(name)}/visibility`, {
      method: 'PUT',
      body: JSON.stringify(patch),
    });
    await refreshRegistries();
    if (patch.exposure) {
      onFlash(formatMsg(locale, 'mcp.exposureUpdated', { name, exposure: patch.exposure }));
    } else {
      onFlash(formatMsg(locale, 'mcp.visibilityUpdated', { name }));
    }
  };

  const removeServer = async (name: string) => {
    await apiFetch(`/api/v1/mcp/${encodeURIComponent(name)}`, { method: 'DELETE' });
    await refreshRegistries();
  };

  const filteredServers = useMemo(() => {
    const q = searchQuery.trim().toLowerCase();
    return mcpServers.filter((m) => {
      if (targetWorkspace && m.scope === 'workspace' && m.workspace_path) {
        const bound = m.workspace_path.trim().replace(/\/+$/, '');
        if (bound !== targetWorkspace.trim().replace(/\/+$/, '')) return false;
      }
      if (!q) return true;
      return (
        m.name.toLowerCase().includes(q) ||
        (m.description || '').toLowerCase().includes(q) ||
        (m.command || '').toLowerCase().includes(q) ||
        (m.url || '').toLowerCase().includes(q)
      );
    });
  }, [mcpServers, searchQuery, targetWorkspace]);

  const globalServers = useMemo(
    () => filteredServers.filter((m) => m.scope !== 'workspace'),
    [filteredServers],
  );
  const workspaceServers = useMemo(
    () => filteredServers.filter((m) => m.scope === 'workspace'),
    [filteredServers],
  );

  const renderStatusDot = (m: MCPServerRecord, effectiveEnabled: boolean) => {
    const exp = (m.exposure || 'codemode') as MCPExposure;
    let colorClass = 'bg-foreground-subtlest/50';
    let title = formatMsg(locale, 'mcp.status.disconnected');
    if (!effectiveEnabled) {
      colorClass = 'bg-foreground-subtlest/35';
      title = formatMsg(locale, 'mcp.status.disabled');
    } else if (exp === 'hidden') {
      colorClass = 'bg-foreground-subtlest/45';
      title = formatMsg(locale, 'mcp.status.hidden');
    } else if (m.last_status === 'connected') {
      colorClass = 'bg-emerald-500';
      title = formatMsg(locale, 'mcp.status.connected');
    } else if (m.last_status === 'error') {
      colorClass = 'bg-rose-500';
      title = m.last_error || formatMsg(locale, 'mcp.status.error');
    }
    return <span title={title} className={`mt-1.5 inline-block size-2 shrink-0 rounded-full ${colorClass}`} />;
  };

  const renderServerRow = (m: MCPServerRecord) => {
    const isOpen = expandedServer === m.name;
    const isWorkspaceView = Boolean(targetWorkspace);
    const { enabled: effectiveEnabled, overridden } = isEffectiveEnabled(m, targetWorkspace);
    const exposure = (m.exposure || 'codemode') as MCPExposure;
    const cachedTools: MCPToolEntry[] = Array.isArray(m.catalog_cache) ? m.catalog_cache : [];
    const probing = Boolean(probingByName[m.name]);

    return (
      <div key={m.name} data-testid={`mcp-row-${m.name}`} className="px-5 py-3.5">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <button
            type="button"
            onClick={() => setExpandedServer(isOpen ? null : m.name)}
            className="flex min-w-0 flex-1 items-start gap-2 text-left"
          >
            <ChevronRight
              className={`mt-1 size-3.5 shrink-0 text-foreground-subtlest transition-transform ${
                isOpen ? 'rotate-90' : ''
              }`}
            />
            {renderStatusDot(m, effectiveEnabled)}
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-1.5">
                <span className="font-mono text-ui-base font-medium text-foreground">{m.name}</span>
                {m.scope === 'workspace' ? (
                  <span className="inline-flex shrink-0 items-center gap-1 rounded-full border border-border bg-surface px-2 py-0.5 text-ui-xs text-foreground-subtle">
                    <Folder className="size-3" />
                    <span>
                      {m.workspace_path
                        ? workspaceLeaf(m.workspace_path)
                        : formatMsg(locale, 'skills.scope.workspace')}
                    </span>
                  </span>
                ) : (
                  <span className="inline-flex shrink-0 items-center gap-1 rounded-full border border-border bg-surface px-2 py-0.5 text-ui-xs text-foreground-subtle">
                    <UserRound className="size-3" />
                    <span>{formatMsg(locale, 'skills.scope.user')}</span>
                  </span>
                )}
                <span className="rounded-md border border-border bg-background px-1.5 py-0.5 font-mono text-[11px] text-foreground-subtle">
                  {m.transport || 'stdio'}
                </span>
                <span className="rounded-md border border-border bg-background px-1.5 py-0.5 font-mono text-[11px] text-foreground-subtlest">
                  {m.host_mode}
                </span>
                {cachedTools.length > 0 ? (
                  <span className="text-ui-xs text-foreground-subtlest">
                    {formatMsg(locale, 'mcp.toolCount', { count: cachedTools.length })}
                  </span>
                ) : null}
              </div>
              {m.description ? (
                <p className="mt-1 truncate text-ui-sm text-foreground-subtle">{m.description}</p>
              ) : m.command ? (
                <p className="mt-0.5 truncate font-mono text-ui-xs text-foreground-subtlest">
                  {m.command} {(m.args || []).join(' ')}
                </p>
              ) : m.url ? (
                <p className="mt-0.5 truncate font-mono text-ui-xs text-foreground-subtlest">
                  {m.url}
                </p>
              ) : null}
              {m.last_status === 'error' && m.last_error ? (
                <p className="mt-1 flex items-center gap-1 truncate text-ui-xs text-foreground-subtle">
                  <AlertCircle className="size-3 shrink-0 text-rose-500" />
                  <span className="truncate">{m.last_error}</span>
                </p>
              ) : null}
            </div>
          </button>

          {/* Right controls: Exposure select + Override reset + Test + Edit + Delete + Visibility switch */}
          <div className="flex shrink-0 flex-wrap items-center gap-1.5">
            <select
              aria-label={`${m.name} exposure`}
              data-testid={`mcp-exposure-${m.name}`}
              value={exposure}
              onClick={(e) => e.stopPropagation()}
              onChange={(e) =>
                void updateVisibility(m.name, { exposure: e.target.value as MCPExposure })
              }
              className="h-7 rounded-md border border-border bg-surface px-2 text-ui-xs text-foreground focus:outline-none"
            >
              <option value="codemode">{formatMsg(locale, 'mcp.exposure.codemode')}</option>
              <option value="deferred">{formatMsg(locale, 'mcp.exposure.deferred')}</option>
              <option value="direct">{formatMsg(locale, 'mcp.exposure.direct')}</option>
              <option value="hidden">{formatMsg(locale, 'mcp.exposure.hidden')}</option>
            </select>

            {overridden ? (
              <button
                type="button"
                title={formatMsg(locale, 'skills.resetOverride')}
                onClick={() =>
                  void updateVisibility(m.name, {
                    scope_target: 'workspace',
                    workspace_path: targetWorkspace,
                    enabled: null,
                  })
                }
                className="inline-flex h-7 items-center gap-1 rounded-md border border-brand-blue/30 bg-brand-blue/10 px-1.5 text-[11px] font-medium text-brand-blue hover:opacity-80"
              >
                <span>{formatMsg(locale, 'skills.overrideBadge')}</span>
                <RotateCcw className="size-3" />
              </button>
            ) : null}

            <button
              type="button"
              onClick={() => void probeServer(m.name)}
              disabled={probing}
              title={formatMsg(locale, 'mcp.testBtn')}
              className="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-surface px-2 text-ui-xs text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground disabled:opacity-50"
            >
              {probing ? (
                <LoaderCircle className="size-3 animate-spin" />
              ) : (
                <RefreshCw className="size-3" />
              )}
              <span className="hidden sm:inline">{formatMsg(locale, 'mcp.testBtn')}</span>
            </button>

            <button
              type="button"
              onClick={() => openEditModal(m)}
              aria-label={formatMsg(locale, 'common.edit')}
              className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
            >
              <Pencil className="size-3.5" />
            </button>

            <button
              type="button"
              onClick={() => void removeServer(m.name)}
              aria-label={formatMsg(locale, 'capabilities.deleteMcpServer')}
              className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-destructive-surface hover:text-destructive-surface-fg"
            >
              <Trash2 className="size-3.5" />
            </button>

            <ToggleSwitch
              checked={effectiveEnabled}
              label={`${m.name} visibility`}
              onChange={(next) =>
                void updateVisibility(m.name, {
                  scope_target: isWorkspaceView ? 'workspace' : 'global',
                  workspace_path: targetWorkspace,
                  enabled: next,
                })
              }
            />
          </div>
        </div>

        {/* Expanded inspector: Workspace Visibility Matrix + Tool Catalog + Diagnostics */}
        {isOpen ? (
          <div className="mt-3.5 space-y-4 border-t border-border/60 pt-3.5 pl-5">
            {/* Per-workspace visibility matrix */}
            <div className="space-y-2 rounded-xl border border-border bg-background/60 p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div>
                  <div className="text-ui-xs font-medium text-foreground">
                    {formatMsg(locale, 'mcp.workspaceMatrix')}
                  </div>
                  <p className="text-[11px] text-foreground-subtlest">
                    {formatMsg(locale, 'mcp.workspaceMatrixHint')}
                  </p>
                </div>
                <div className="flex items-center gap-2 text-ui-xs text-foreground-subtle">
                  <span>{formatMsg(locale, 'skills.globalDefault')}</span>
                  <ToggleSwitch
                    checked={m.enabled}
                    label={`${m.name} global enabled`}
                    onChange={(next) =>
                      void updateVisibility(m.name, { scope_target: 'global', enabled: next })
                    }
                  />
                </div>
              </div>

              {knownWorkspaces.length === 0 ? (
                <p className="pt-1 text-ui-xs text-foreground-subtlest">
                  {formatMsg(locale, 'skills.noKnownWorkspaces')}
                </p>
              ) : (
                <div className="divide-y divide-border/50 rounded-lg border border-border/70 bg-surface/50">
                  {knownWorkspaces.map((ws) => {
                    const st = isEffectiveEnabled(m, ws);
                    return (
                      <div
                        key={ws}
                        className="flex items-center justify-between gap-3 px-3 py-2 text-ui-xs"
                      >
                        <div className="flex min-w-0 items-center gap-2">
                          <Folder className="size-3.5 shrink-0 text-foreground-subtlest" />
                          <span className="truncate font-medium text-foreground">
                            {workspaceLeaf(ws)}
                          </span>
                          <span className="hidden truncate font-mono text-[11px] text-foreground-subtlest md:inline">
                            {ws}
                          </span>
                        </div>
                        <div className="flex shrink-0 items-center gap-2">
                          {st.overridden ? (
                            <button
                              type="button"
                              onClick={() =>
                                void updateVisibility(m.name, {
                                  scope_target: 'workspace',
                                  workspace_path: ws,
                                  enabled: null,
                                })
                              }
                              title={formatMsg(locale, 'skills.resetOverride')}
                              className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] text-brand-blue hover:bg-brand-blue/10"
                            >
                              <span>{formatMsg(locale, 'skills.overrideBadge')}</span>
                              <RotateCcw className="size-2.5" />
                            </button>
                          ) : (
                            <span className="text-[11px] text-foreground-subtlest">
                              {formatMsg(locale, 'skills.inheritedBadge')}
                            </span>
                          )}
                          <ToggleSwitch
                            checked={st.enabled}
                            label={`${m.name} in ${ws}`}
                            onChange={(next) =>
                              void updateVisibility(m.name, {
                                scope_target: 'workspace',
                                workspace_path: ws,
                                enabled: next,
                              })
                            }
                          />
                        </div>
                      </div>
                    );
                  })}
                </div>
              )}
            </div>

            {/* Discovered Tools Catalog */}
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-1.5 text-ui-xs font-medium text-foreground">
                  <Wrench className="size-3.5 text-foreground-subtlest" />
                  <span>{formatMsg(locale, 'mcp.discoveredTools', { count: cachedTools.length })}</span>
                </div>
                <button
                  type="button"
                  onClick={() => void probeServer(m.name)}
                  disabled={probing}
                  className="text-ui-xs text-brand-blue hover:underline disabled:opacity-50"
                >
                  {probing ? formatMsg(locale, 'mcp.testing') : formatMsg(locale, 'mcp.testBtn')}
                </button>
              </div>
              {cachedTools.length === 0 ? (
                <p className="rounded-lg border border-dashed border-border px-3 py-2.5 text-ui-xs text-foreground-subtlest">
                  {formatMsg(locale, 'mcp.noToolsCached')}
                </p>
              ) : (
                <div className="max-h-56 divide-y divide-border/50 overflow-y-auto rounded-lg border border-border bg-surface/50">
                  {cachedTools.map((t) => (
                    <div key={t.full_name || t.name} className="px-3 py-2">
                      <div className="flex items-center gap-2">
                        <span className="font-mono text-ui-xs font-medium text-foreground">
                          {t.name}
                        </span>
                        {t.read_only ? (
                          <span className="rounded bg-background px-1.5 py-0.5 text-[10px] text-foreground-subtlest">
                            {formatMsg(locale, 'mcp.readOnly')}
                          </span>
                        ) : null}
                      </div>
                      {t.description ? (
                        <p className="mt-0.5 text-[11px] text-foreground-subtle">{t.description}</p>
                      ) : null}
                    </div>
                  ))}
                </div>
              )}
            </div>

            {/* Diagnostics on error */}
            {m.last_error ? (
              <div className="space-y-1 rounded-lg border border-rose-500/30 bg-rose-500/5 p-3">
                <div className="text-ui-xs font-medium text-rose-500">
                  {formatMsg(locale, 'mcp.diagnostics')}
                </div>
                <pre className="whitespace-pre-wrap break-all font-mono text-[11px] text-foreground-subtle">
                  {m.last_error}
                </pre>
              </div>
            ) : null}
          </div>
        ) : null}
      </div>
    );
  };

  return (
    <div data-testid="mcp-manager-section" className="space-y-6">
      {/* Management Top Control Bar */}
      <SettingsGroupCard>
        <div className="space-y-3.5 px-5 py-4">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="min-w-0">
              <div className="text-ui-base font-medium text-foreground">
                {formatMsg(locale, 'mcp.managerTitle')}
              </div>
              <p className="mt-0.5 text-ui-xs text-foreground-subtle">
                {formatMsg(locale, 'mcp.managerDesc')}
              </p>
            </div>
            <div className="flex items-center gap-2">
              {mcpServers.length > 0 ? (
                <button
                  type="button"
                  onClick={() => void probeAll()}
                  disabled={probingAll}
                  className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border bg-surface px-3 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover disabled:opacity-50"
                >
                  {probingAll ? (
                    <LoaderCircle className="size-3.5 animate-spin" />
                  ) : (
                    <RefreshCw className="size-3.5" />
                  )}
                  <span>{formatMsg(locale, 'mcp.probeAll')}</span>
                </button>
              ) : null}
              <button
                type="button"
                data-testid="mcp-install-btn"
                onClick={openInstallModal}
                className="inline-flex h-8 items-center gap-1.5 rounded-lg bg-primary px-3.5 text-ui-xs font-medium text-primary-foreground transition-opacity hover:opacity-90"
              >
                <Plus className="size-3.5" />
                <span>{formatMsg(locale, 'mcp.installBtn')}</span>
              </button>
            </div>
          </div>

          <div className="grid grid-cols-1 gap-3 sm:grid-cols-12">
            <div className="sm:col-span-6">
              <label className="block space-y-1">
                <span className="text-ui-xs text-foreground-subtle">
                  {formatMsg(locale, 'skills.targetWorkspace')}
                </span>
                <select
                  data-testid="mcp-workspace-select"
                  value={targetWorkspace}
                  onChange={(e) => setTargetWorkspace(e.target.value)}
                  className={settingsSelectClass}
                >
                  <option value="">{formatMsg(locale, 'skills.globalDefault')}</option>
                  {knownWorkspaces.map((ws) => (
                    <option key={ws} value={ws}>
                      {workspaceLeaf(ws)} — {ws}
                    </option>
                  ))}
                </select>
              </label>
            </div>
            <div className="sm:col-span-6">
              <label className="block space-y-1">
                <span className="text-ui-xs text-foreground-subtle">&nbsp;</span>
                <div className="relative">
                  <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-foreground-subtlest" />
                  <input
                    value={searchQuery}
                    onChange={(e) => setSearchQuery(e.target.value)}
                    placeholder={formatMsg(locale, 'mcp.searchPlaceholder')}
                    className={`${settingsInputClass} pl-8`}
                  />
                </div>
              </label>
            </div>
          </div>
        </div>
      </SettingsGroupCard>

      {/* Installed Servers List */}
      {filteredServers.length === 0 ? (
        <SettingsGroupCard>
          <div className="flex flex-col items-center gap-2.5 px-6 py-10 text-center">
            <Cable className="size-6 text-foreground-subtlest" />
            <div className="text-ui-base font-medium text-foreground">
              {formatMsg(locale, 'mcp.emptyTitle')}
            </div>
            <p className="max-w-lg text-ui-xs leading-relaxed text-foreground-subtlest">
              {formatMsg(locale, 'mcp.emptyHint')}
            </p>
            <button
              type="button"
              onClick={openInstallModal}
              className="mt-1 inline-flex h-8 items-center gap-1.5 rounded-lg bg-primary px-3.5 text-ui-xs font-medium text-primary-foreground transition-opacity hover:opacity-90"
            >
              <PackagePlus className="size-3.5" />
              <span>{formatMsg(locale, 'mcp.installBtn')}</span>
            </button>
          </div>
        </SettingsGroupCard>
      ) : (
        <div className="space-y-6">
          {globalServers.length > 0 ? (
            <div className="space-y-3">
              <SectionHeading
                title={`${formatMsg(locale, 'mcp.group.global')} (${globalServers.length})`}
              />
              <SettingsGroupCard>{globalServers.map(renderServerRow)}</SettingsGroupCard>
            </div>
          ) : null}

          {workspaceServers.length > 0 ? (
            <div className="space-y-3">
              <SectionHeading
                title={`${formatMsg(locale, 'mcp.group.workspace')} (${workspaceServers.length})`}
              />
              <SettingsGroupCard>{workspaceServers.map(renderServerRow)}</SettingsGroupCard>
            </div>
          ) : null}
        </div>
      )}

      {/* Auto-discovered local MCP servers (.mcp.json / ~/.nex/mcp.json) */}
      {discoveredServers.length > 0 ? (
        <div className="space-y-3" data-testid="mcp-discovered-section">
          <SectionHeading
            title={`${formatMsg(locale, 'mcp.group.discovered')} (${discoveredServers.length})`}
          />
          <SettingsGroupCard>
            {discoveredServers
              .filter((d) => {
                const q = searchQuery.trim().toLowerCase();
                if (!q) return true;
                return (
                  d.name.toLowerCase().includes(q) ||
                  (d.description || '').toLowerCase().includes(q) ||
                  (d.source_root || '').toLowerCase().includes(q)
                );
              })
              .map((d) => (
                <div
                  key={`${d.source}:${d.name}`}
                  data-testid={`mcp-discovered-${d.name}`}
                  className="flex flex-wrap items-center justify-between gap-3 px-4 py-3"
                >
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span
                        className={`inline-block size-2 shrink-0 rounded-full ${
                          d.enabled ? 'bg-emerald-500' : 'bg-foreground-subtlest/40'
                        }`}
                      />
                      <span className="font-mono text-ui-base font-medium text-foreground">{d.name}</span>
                      <span className="inline-flex shrink-0 items-center gap-1 rounded-full border border-border bg-surface px-2 py-0.5 text-ui-xs text-foreground-subtle">
                        {d.source === 'workspace' ? (
                          <Folder className="size-3" />
                        ) : (
                          <UserRound className="size-3" />
                        )}
                        <span>
                          {formatMsg(
                            locale,
                            d.source === 'workspace' ? 'skills.scope.workspace' : 'skills.scope.user',
                          )}
                        </span>
                      </span>
                      <span
                        title={d.source_path}
                        className="rounded-md border border-border bg-background px-1.5 py-0.5 font-mono text-[11px] text-foreground-subtle"
                      >
                        {d.source_root}
                      </span>
                      <span className="rounded-md border border-border bg-background px-1.5 py-0.5 font-mono text-[11px] text-foreground-subtlest">
                        {d.transport || 'stdio'}
                      </span>
                    </div>
                    {d.description ? (
                      <p className="mt-1 truncate text-ui-sm text-foreground-subtle">{d.description}</p>
                    ) : d.command ? (
                      <p className="mt-0.5 truncate font-mono text-ui-xs text-foreground-subtlest">
                        {d.command} {(d.args || []).join(' ')}
                      </p>
                    ) : d.url ? (
                      <p className="mt-0.5 truncate font-mono text-ui-xs text-foreground-subtlest">{d.url}</p>
                    ) : null}
                  </div>
                  <span className="text-ui-xs text-foreground-subtlest">
                    {discovering ? '...' : formatMsg(locale, 'mcp.discoveredReadOnly')}
                  </span>
                </div>
              ))}
          </SettingsGroupCard>
        </div>
      ) : null}

      {/* Install / Edit MCP Modal */}
      {modalOpen ? (
        <div
          data-testid="mcp-install-modal"
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4 backdrop-blur-[2px]"
          onClick={() => setModalOpen(false)}
        >
          <div
            className="flex max-h-[88vh] w-full max-w-2xl flex-col overflow-hidden rounded-2xl border border-border bg-card shadow-2xl"
            onClick={(e) => e.stopPropagation()}
          >
            {/* Modal Header */}
            <div className="flex items-center justify-between border-b border-border px-5 py-3.5">
              <div className="text-ui-base font-semibold text-foreground">
                {editingName
                  ? formatMsg(locale, 'mcp.modal.editTitle', { name: editingName })
                  : formatMsg(locale, 'mcp.modal.installTitle')}
              </div>
              <button
                type="button"
                onClick={() => setModalOpen(false)}
                className="flex size-7 items-center justify-center rounded-lg text-foreground-subtlest hover:bg-hover hover:text-foreground"
              >
                <X className="size-4" />
              </button>
            </div>

            {/* Mode Segmented Tabs */}
            <div className="border-b border-border/70 px-5 py-2.5">
              <div className="inline-flex rounded-lg border border-border bg-surface p-0.5 text-ui-xs">
                {!editingName ? (
                  <button
                    type="button"
                    data-testid="mcp-tab-catalog"
                    onClick={() => setModalTab('catalog')}
                    className={`rounded-md px-3 py-1 font-medium transition-colors ${
                      modalTab === 'catalog'
                        ? 'bg-popover text-foreground shadow-xs'
                        : 'text-foreground-subtle hover:text-foreground'
                    }`}
                  >
                    {formatMsg(locale, 'mcp.modal.tabCatalog')}
                  </button>
                ) : null}
                <button
                  type="button"
                  data-testid="mcp-tab-form"
                  onClick={() => setModalTab('form')}
                  className={`rounded-md px-3 py-1 font-medium transition-colors ${
                    modalTab === 'form'
                      ? 'bg-popover text-foreground shadow-xs'
                      : 'text-foreground-subtle hover:text-foreground'
                  }`}
                >
                  {formatMsg(locale, 'mcp.modal.tabForm')}
                </button>
                <button
                  type="button"
                  data-testid="mcp-tab-json"
                  onClick={() => {
                    if (modalTab === 'form' && form.name.trim()) {
                      setJsonDraft(formToJsonSnippet(form));
                    }
                    setModalTab('json');
                  }}
                  className={`rounded-md px-3 py-1 font-medium transition-colors ${
                    modalTab === 'json'
                      ? 'bg-popover text-foreground shadow-xs'
                      : 'text-foreground-subtle hover:text-foreground'
                  }`}
                >
                  {formatMsg(locale, 'mcp.modal.tabJson')}
                </button>
              </div>
            </div>

            {/* Modal Body */}
            <div className="flex-1 space-y-4 overflow-y-auto px-5 py-4">
              {modalTab === 'catalog' ? (
                <div className="space-y-3">
                  <p className="text-ui-xs text-foreground-subtle">
                    {formatMsg(locale, 'mcp.modal.catalogHint')}
                  </p>
                  <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2">
                    {MCP_PRESETS.map((preset) => (
                      <div
                        key={preset.id}
                        className="flex flex-col justify-between gap-2.5 rounded-xl border border-border bg-surface/60 p-3.5 transition-colors hover:border-foreground/25"
                      >
                        <div className="space-y-1">
                          <div className="flex items-center justify-between gap-2">
                            <span className="text-ui-sm font-medium text-foreground">
                              {preset.title}
                            </span>
                            <span className="rounded bg-background px-1.5 py-0.5 font-mono text-[10px] text-foreground-subtlest">
                              {preset.transport}
                            </span>
                          </div>
                          <p className="text-ui-xs leading-relaxed text-foreground-subtle">
                            {preset.description}
                          </p>
                        </div>
                        <div className="flex items-center justify-between pt-1">
                          <span className="font-mono text-[11px] text-foreground-subtlest">
                            {preset.exposure}
                          </span>
                          <button
                            type="button"
                            data-testid={`mcp-preset-${preset.id}`}
                            onClick={() => applyPreset(preset)}
                            className="inline-flex h-7 items-center rounded-md border border-border bg-popover px-2.5 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover"
                          >
                            {formatMsg(locale, 'mcp.modal.usePreset')}
                          </button>
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
              ) : null}

              {modalTab === 'form' ? (
                <div className="space-y-3.5">
                  <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                    <label className="block space-y-1">
                      <span className="text-ui-xs text-foreground-subtle">
                        {formatMsg(locale, 'mcp.modal.name')}
                      </span>
                      <input
                        data-testid="mcp-form-name"
                        value={form.name}
                        disabled={Boolean(editingName)}
                        onChange={(e) => setForm((p) => ({ ...p, name: e.target.value }))}
                        placeholder="playwright"
                        className={`${settingsMonoInputClass} disabled:opacity-60`}
                      />
                      <span className="block text-[11px] text-foreground-subtlest">
                        {formatMsg(locale, 'mcp.modal.nameHint')}
                      </span>
                    </label>

                    <label className="block space-y-1">
                      <span className="text-ui-xs text-foreground-subtle">
                        {formatMsg(locale, 'mcp.modal.scope')}
                      </span>
                      <select
                        value={form.scope === 'workspace' ? form.workspace_path : '__global__'}
                        onChange={(e) => {
                          const v = e.target.value;
                          if (v === '__global__') {
                            setForm((p) => ({ ...p, scope: 'global', workspace_path: '' }));
                          } else {
                            setForm((p) => ({ ...p, scope: 'workspace', workspace_path: v }));
                          }
                        }}
                        className={settingsSelectClass}
                      >
                        <option value="__global__">
                          {formatMsg(locale, 'mcp.modal.scopeGlobal')}
                        </option>
                        {knownWorkspaces.map((ws) => (
                          <option key={ws} value={ws}>
                            {formatMsg(locale, 'mcp.modal.scopeWorkspace', {
                              workspace: workspaceLeaf(ws),
                            })}
                          </option>
                        ))}
                      </select>
                    </label>
                  </div>

                  <label className="block space-y-1">
                    <span className="text-ui-xs text-foreground-subtle">
                      {formatMsg(locale, 'mcp.modal.description')}
                    </span>
                    <input
                      value={form.description}
                      onChange={(e) => setForm((p) => ({ ...p, description: e.target.value }))}
                      placeholder={formatMsg(locale, 'mcp.modal.descriptionPlaceholder')}
                      className={settingsInputClass}
                    />
                  </label>

                  <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
                    <label className="block space-y-1">
                      <span className="text-ui-xs text-foreground-subtle">
                        {formatMsg(locale, 'mcp.modal.transport')}
                      </span>
                      <select
                        value={form.transport}
                        onChange={(e) =>
                          setForm((p) => ({
                            ...p,
                            transport: e.target.value as 'stdio' | 'http',
                          }))
                        }
                        className={settingsSelectClass}
                      >
                        <option value="stdio">
                          {formatMsg(locale, 'mcp.modal.transportStdio')}
                        </option>
                        <option value="http">
                          {formatMsg(locale, 'mcp.modal.transportHttp')}
                        </option>
                      </select>
                    </label>

                    <label className="block space-y-1">
                      <span className="text-ui-xs text-foreground-subtle">
                        {formatMsg(locale, 'capabilities.hostMode')}
                      </span>
                      <select
                        value={form.host_mode}
                        onChange={(e) =>
                          setForm((p) => ({
                            ...p,
                            host_mode: e.target.value as 'runner_local' | 'server_hosted',
                          }))
                        }
                        className={settingsSelectClass}
                      >
                        <option value="runner_local">
                          {formatMsg(locale, 'capabilities.runnerLocalStdio')}
                        </option>
                        <option value="server_hosted">
                          {formatMsg(locale, 'capabilities.serverHosted')}
                        </option>
                      </select>
                    </label>

                    <label className="block space-y-1">
                      <span className="text-ui-xs text-foreground-subtle">
                        {formatMsg(locale, 'mcp.exposure.label')}
                      </span>
                      <select
                        value={form.exposure}
                        onChange={(e) =>
                          setForm((p) => ({ ...p, exposure: e.target.value as MCPExposure }))
                        }
                        className={settingsSelectClass}
                      >
                        <option value="codemode">{formatMsg(locale, 'mcp.exposure.codemode')}</option>
                        <option value="deferred">{formatMsg(locale, 'mcp.exposure.deferred')}</option>
                        <option value="direct">{formatMsg(locale, 'mcp.exposure.direct')}</option>
                        <option value="hidden">{formatMsg(locale, 'mcp.exposure.hidden')}</option>
                      </select>
                    </label>
                  </div>

                  <p className="rounded-lg border border-border/70 bg-surface/60 px-3 py-2 text-[11px] text-foreground-subtle">
                    {formatMsg(locale, 'mcp.exposure.hint')}
                  </p>

                  {form.transport === 'stdio' ? (
                    <>
                      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
                        <label className="block space-y-1">
                          <span className="text-ui-xs text-foreground-subtle">
                            {formatMsg(locale, 'capabilities.command')}
                          </span>
                          <input
                            value={form.command}
                            onChange={(e) => setForm((p) => ({ ...p, command: e.target.value }))}
                            placeholder="npx / uvx"
                            className={settingsMonoInputClass}
                          />
                        </label>
                        <label className="block space-y-1 sm:col-span-2">
                          <span className="text-ui-xs text-foreground-subtle">
                            {formatMsg(locale, 'capabilities.arguments')}
                          </span>
                          <input
                            value={form.argsText}
                            onChange={(e) => setForm((p) => ({ ...p, argsText: e.target.value }))}
                            placeholder="-y @playwright/mcp@latest"
                            className={settingsMonoInputClass}
                          />
                        </label>
                      </div>

                      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
                        <label className="block space-y-1 sm:col-span-2">
                          <span className="text-ui-xs text-foreground-subtle">
                            {formatMsg(locale, 'mcp.modal.envJson')}
                          </span>
                          <textarea
                            rows={3}
                            value={form.envJson}
                            onChange={(e) => setForm((p) => ({ ...p, envJson: e.target.value }))}
                            placeholder='{"API_KEY": "${MY_API_KEY}"}'
                            className="w-full rounded-lg border border-border bg-input p-2.5 font-mono text-ui-xs text-foreground placeholder-foreground-subtlest"
                          />
                          <span className="block text-[11px] text-foreground-subtlest">
                            {formatMsg(locale, 'mcp.modal.envHint')}
                          </span>
                        </label>
                        <label className="block space-y-1">
                          <span className="text-ui-xs text-foreground-subtle">
                            {formatMsg(locale, 'capabilities.idleTtlS')}
                          </span>
                          <input
                            type="number"
                            value={form.idle_ttl_sec}
                            onChange={(e) =>
                              setForm((p) => ({ ...p, idle_ttl_sec: Number(e.target.value) }))
                            }
                            className={settingsMonoInputClass}
                          />
                        </label>
                      </div>
                    </>
                  ) : (
                    <>
                      <label className="block space-y-1">
                        <span className="text-ui-xs text-foreground-subtle">
                          {formatMsg(locale, 'mcp.modal.url')}
                        </span>
                        <input
                          value={form.url}
                          onChange={(e) => setForm((p) => ({ ...p, url: e.target.value }))}
                          placeholder="https://example.com/mcp"
                          className={settingsMonoInputClass}
                        />
                      </label>
                      <label className="block space-y-1">
                        <span className="text-ui-xs text-foreground-subtle">
                          {formatMsg(locale, 'mcp.modal.headersJson')}
                        </span>
                        <textarea
                          rows={3}
                          value={form.headersJson}
                          onChange={(e) => setForm((p) => ({ ...p, headersJson: e.target.value }))}
                          placeholder='{"Authorization": "Bearer ${TOKEN}"}'
                          className="w-full rounded-lg border border-border bg-input p-2.5 font-mono text-ui-xs text-foreground placeholder-foreground-subtlest"
                        />
                        <span className="block text-[11px] text-foreground-subtlest">
                          {formatMsg(locale, 'mcp.modal.envHint')}
                        </span>
                      </label>
                    </>
                  )}
                </div>
              ) : null}

              {modalTab === 'json' ? (
                <div className="space-y-3">
                  <p className="text-ui-xs text-foreground-subtle">
                    {formatMsg(locale, 'mcp.modal.jsonHint')}
                  </p>
                  <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                    <label className="block space-y-1">
                      <span className="text-ui-xs text-foreground-subtle">
                        {formatMsg(locale, 'mcp.modal.scope')}
                      </span>
                      <select
                        value={form.scope === 'workspace' ? form.workspace_path : '__global__'}
                        onChange={(e) => {
                          const v = e.target.value;
                          if (v === '__global__') {
                            setForm((p) => ({ ...p, scope: 'global', workspace_path: '' }));
                          } else {
                            setForm((p) => ({ ...p, scope: 'workspace', workspace_path: v }));
                          }
                        }}
                        className={settingsSelectClass}
                      >
                        <option value="__global__">
                          {formatMsg(locale, 'mcp.modal.scopeGlobal')}
                        </option>
                        {knownWorkspaces.map((ws) => (
                          <option key={ws} value={ws}>
                            {formatMsg(locale, 'mcp.modal.scopeWorkspace', {
                              workspace: workspaceLeaf(ws),
                            })}
                          </option>
                        ))}
                      </select>
                    </label>
                    <label className="block space-y-1">
                      <span className="text-ui-xs text-foreground-subtle">
                        {formatMsg(locale, 'capabilities.hostMode')}
                      </span>
                      <select
                        value={form.host_mode}
                        onChange={(e) =>
                          setForm((p) => ({
                            ...p,
                            host_mode: e.target.value as 'runner_local' | 'server_hosted',
                          }))
                        }
                        className={settingsSelectClass}
                      >
                        <option value="runner_local">
                          {formatMsg(locale, 'capabilities.runnerLocalStdio')}
                        </option>
                        <option value="server_hosted">
                          {formatMsg(locale, 'capabilities.serverHosted')}
                        </option>
                      </select>
                    </label>
                  </div>
                  <textarea
                    data-testid="mcp-json-textarea"
                    rows={12}
                    value={jsonDraft}
                    onChange={(e) => {
                      setJsonDraft(e.target.value);
                      if (formError) setFormError('');
                    }}
                    className="w-full rounded-lg border border-border bg-input p-3 font-mono text-ui-xs leading-relaxed text-foreground placeholder-foreground-subtlest"
                  />
                </div>
              ) : null}

              {formError ? (
                <div className="rounded-lg border border-rose-500/30 bg-rose-500/10 px-3 py-2 text-ui-xs text-rose-500">
                  {formError}
                </div>
              ) : null}
            </div>

            {/* Modal Footer */}
            {modalTab !== 'catalog' ? (
              <div className="flex items-center justify-end gap-2 border-t border-border px-5 py-3">
                <button
                  type="button"
                  onClick={() => setModalOpen(false)}
                  className="h-8 rounded-lg border border-border bg-surface px-3 text-ui-xs font-medium text-foreground-subtle hover:bg-hover hover:text-foreground"
                >
                  {formatMsg(locale, 'common.cancel')}
                </button>
                <button
                  type="button"
                  data-testid="mcp-modal-submit"
                  disabled={saving}
                  onClick={() =>
                    void (modalTab === 'json' ? submitJsonImport() : submitForm())
                  }
                  className="inline-flex h-8 items-center gap-1.5 rounded-lg bg-primary px-3.5 text-ui-xs font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-50"
                >
                  {saving ? <LoaderCircle className="size-3.5 animate-spin" /> : null}
                  <span>
                    {modalTab === 'json'
                      ? formatMsg(locale, 'mcp.modal.importBtn')
                      : editingName
                        ? formatMsg(locale, 'mcp.modal.saveSubmit')
                        : formatMsg(locale, 'mcp.modal.installSubmit')}
                  </span>
                </button>
              </div>
            ) : null}
          </div>
        </div>
      ) : null}
    </div>
  );
}
