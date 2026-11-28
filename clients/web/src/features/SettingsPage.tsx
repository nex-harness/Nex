import {
  AlarmClock,
  Archive,
  ArrowLeft,
  BarChart3,
  Bell,
  Blocks,
  Bot,
  Brain,
  Cable,
  Check,
  Copy,
  Eye,
  EyeOff,
  Globe,
  GripVertical,
  HardDrive,
  KeyRound,
  Lock,
  LogOut,
  Monitor,
  Moon,
  Package,
  Palette,
  Plus,
  Radio,
  RefreshCw,
  Search,
  Settings2,
  SlidersHorizontal,
  Sun,
  Trash2,
  WandSparkles,
  type LucideIcon,
} from 'lucide-react';
import { type ReactNode, useEffect, useState } from 'react';
import { apiFetch } from '../core/protocol';
import { useNexStore } from '../core/store';
import { ModelMeta, ProviderRecord, SettingsSectionId, UserPreferences } from '../core/types';
import { hostBridge } from '../host/bridge';
import { formatMsg, type LocaleCode, type MessageKey, SUPPORTED_LOCALES } from '../i18n/messages';
import { HighlightedCode } from './MarkdownRenderer';
import { ArchivesSettingsSection, UsageAnalyticsSection } from './SettingsAnalyticsArchive';
import {
  AgentsSettingsSection,
  AutomationsSection,
  MCPSettingsSection,
  MemoriesSettingsSection,
  SkillsAndPluginsSection,
} from './SettingsCapabilities';
import { SettingsBrowserSection } from './SettingsBrowser';
import { SettingsCredentialsSection } from './SettingsCredentials';
import { ClassifierSettingsCard, ImageGenerationSettingsCard } from './SettingsMultimodal';
import { SettingsRemoteAccessSection } from './SettingsRemoteAccess';
import { RunnersSettingsSection } from './SettingsRunners';

/* -------------------------------------------------------------------------- */
/* Shared settings primitives (also consumed by the sibling settings files)    */
/* -------------------------------------------------------------------------- */

export const settingsInputClass =
  'h-9 w-full min-w-0 rounded-lg border border-border bg-input px-3 text-ui-base text-foreground placeholder-foreground-subtlest';
export const settingsMonoInputClass = `${settingsInputClass} font-mono`;
export const settingsSelectClass =
  'h-9 w-full min-w-0 rounded-lg border border-border bg-input px-3 text-ui-base text-foreground';

export function SettingsGroupCard({
  children,
  className = '',
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={`divide-y divide-border overflow-hidden rounded-2xl border border-border bg-card ${className}`}
    >
      {children}
    </div>
  );
}

export function SettingsRow({
  label,
  description,
  children,
}: {
  label: ReactNode;
  description?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="grid grid-cols-1 items-center gap-4 px-5 py-4 sm:grid-cols-[minmax(0,1fr)_260px]">
      <div className="min-w-0">
        <div className="text-ui-base font-medium text-foreground">{label}</div>
        {description ? (
          <p className="mt-1 text-ui-sm leading-relaxed text-foreground-subtle">{description}</p>
        ) : null}
      </div>
      <div className="flex items-center justify-end gap-2">{children}</div>
    </div>
  );
}

export type ToggleProps = {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label?: string;
  disabled?: boolean;
  testId?: string;
};

export function Toggle({ checked, onChange, label, disabled, testId }: ToggleProps) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      data-testid={testId}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={`relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors disabled:opacity-50 ${
        checked ? 'bg-primary' : 'bg-tag'
      }`}
    >
      <span
        className={`inline-block size-4 rounded-full bg-background shadow-sm transition-transform ${
          checked ? 'translate-x-[18px]' : 'translate-x-0.5'
        }`}
      />
    </button>
  );
}

export function SectionHeading({ title, description }: { title: string; description?: string }) {
  return (
    <div className="space-y-1">
      <h2 className="text-ui-lg font-semibold text-foreground">{title}</h2>
      {description ? (
        <p className="text-ui-sm leading-relaxed text-foreground-subtle">{description}</p>
      ) : null}
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* Navigation config                                                          */
/* -------------------------------------------------------------------------- */

interface NavItem {
  id: SettingsSectionId;
  icon: LucideIcon;
  titleKey: MessageKey;
  descKey: MessageKey;
  groupKey: MessageKey;
}

const NAV_ITEMS: NavItem[] = [
  { id: 'general', icon: Settings2, titleKey: 'settings.general.title', descKey: 'settings.general.desc', groupKey: 'settings.group.basics' },
  { id: 'appearance', icon: Palette, titleKey: 'settings.appearance.title', descKey: 'settings.appearance.desc', groupKey: 'settings.group.basics' },
  { id: 'providers', icon: Package, titleKey: 'settings.modelProvider.title', descKey: 'settings.modelProvider.desc', groupKey: 'settings.group.basics' },
  { id: 'runners', icon: HardDrive, titleKey: 'settings.remoteDevices.title', descKey: 'settings.remoteDevices.desc', groupKey: 'settings.group.basics' },
  { id: 'credentials', icon: KeyRound, titleKey: 'settings.credentials.title', descKey: 'settings.credentials.desc', groupKey: 'settings.group.basics' },
  { id: 'remote', icon: Radio, titleKey: 'settings.remote.title', descKey: 'settings.remote.desc', groupKey: 'settings.group.basics' },

  { id: 'browser', icon: Globe, titleKey: 'settings.browser.title', descKey: 'settings.browser.desc', groupKey: 'settings.group.agentCapabilities' },
  { id: 'agents', icon: Bot, titleKey: 'settings.subagents.title', descKey: 'settings.subagents.desc', groupKey: 'settings.group.agentCapabilities' },
  { id: 'memories', icon: Brain, titleKey: 'settings.memories.title', descKey: 'settings.memories.desc', groupKey: 'settings.group.agentCapabilities' },
  { id: 'plugins', icon: Blocks, titleKey: 'settings.plugins.title', descKey: 'settings.plugins.desc', groupKey: 'settings.group.agentCapabilities' },
  { id: 'mcp', icon: Cable, titleKey: 'settings.mcp.title', descKey: 'settings.mcp.desc', groupKey: 'settings.group.agentCapabilities' },
  { id: 'skills', icon: WandSparkles, titleKey: 'settings.skills.title', descKey: 'settings.skills.desc', groupKey: 'settings.group.agentCapabilities' },
  { id: 'automations', icon: AlarmClock, titleKey: 'settings.automations.title', descKey: 'settings.automations.desc', groupKey: 'settings.group.agentCapabilities' },

  { id: 'usage', icon: BarChart3, titleKey: 'settings.usage.title', descKey: 'settings.usage.desc', groupKey: 'settings.group.dataAndStats' },
  { id: 'archives', icon: Archive, titleKey: 'settings.archives.title', descKey: 'settings.archives.desc', groupKey: 'settings.group.dataAndStats' },
];

const GROUP_KEYS: MessageKey[] = [
  'settings.group.basics',
  'settings.group.agentCapabilities',
  'settings.group.dataAndStats',
];

/* -------------------------------------------------------------------------- */
/* Settings shell                                                             */
/* -------------------------------------------------------------------------- */

export function SettingsPage() {
  const { settingsSection, openSettings, closeSettings, locale } = useNexStore();
  const [navFilter, setNavFilter] = useState('');
  const [flashMsg, setFlashMsg] = useState('');
  const [viewportWidth, setViewportWidth] = useState(() =>
    typeof window !== 'undefined' ? window.innerWidth : 1440,
  );
  const [mobileShowNav, setMobileShowNav] = useState(false);

  useEffect(() => {
    if (typeof window === 'undefined') return;
    const onResize = () => setViewportWidth(window.innerWidth);
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }, []);

  const isCompact = viewportWidth < 768;

  const showFlash = (msg: string) => {
    setFlashMsg(msg);
    setTimeout(() => setFlashMsg(''), 3000);
  };

  const activeMeta = NAV_ITEMS.find((n) => n.id === settingsSection) || NAV_ITEMS[0];
  const query = navFilter.trim().toLowerCase();
  const filteredNav = NAV_ITEMS.filter((n) => {
    if (!query) return true;
    const title = formatMsg(locale, n.titleKey).toLowerCase();
    const desc = formatMsg(locale, n.descKey).toLowerCase();
    return title.includes(query) || desc.includes(query);
  });
  const providerHeaderRow = activeMeta.id === 'providers';

  return (
    <div
      data-testid="settings-page"
      className="fixed inset-0 z-40 flex h-dvh w-screen overflow-hidden bg-background text-foreground"
    >
      <div
        className="nex-drag-region fixed inset-x-0 top-0 z-10"
        style={{ height: 'var(--mygo-titlebar-controls-bottom, 0px)' }}
      />

      {/* Left settings sidebar */}
      <nav
        className={
          isCompact
            ? `${mobileShowNav ? 'flex w-full' : 'hidden'} nex-titlebar-top flex-col bg-sidebar px-3 pb-3.5 select-none`
            : 'nex-titlebar-top flex w-[248px] shrink-0 flex-col border-r border-border bg-sidebar px-3 pb-3.5 select-none'
        }
      >
        <button
          type="button"
          data-testid="settings-back-btn"
          onClick={closeSettings}
          className="flex h-8 w-full items-center gap-2 rounded-lg bg-black/[0.05] px-3 text-ui-sm font-medium text-foreground transition-colors hover:bg-hover dark:bg-white/[0.07]"
        >
          <ArrowLeft className="size-3.5 shrink-0" />
          <span className="truncate">{formatMsg(locale, 'workspace.backToWorkspace')}</span>
        </button>

        <div className="mt-2.5 flex h-8 items-center gap-2 rounded-lg border border-border bg-input px-2.5">
          <Search className="size-3.5 shrink-0 text-foreground-subtlest" />
          <input
            value={navFilter}
            onChange={(e) => setNavFilter(e.target.value)}
            placeholder={formatMsg(locale, 'settings.searchSettings')}
            className="min-w-0 flex-1 bg-transparent text-ui-sm text-foreground placeholder-foreground-subtlest"
          />
        </div>

        <div className="mt-4 min-h-0 flex-1 space-y-4 overflow-y-auto">
          {GROUP_KEYS.map((groupKey) => {
            const items = filteredNav.filter((n) => n.groupKey === groupKey);
            if (items.length === 0) return null;
            return (
              <div key={groupKey} className="space-y-0.5">
                <div className="px-2.5 pb-1 text-ui-xs font-medium text-foreground-subtlest">
                  {formatMsg(locale, groupKey)}
                </div>
                {items.map((item) => {
                  const Icon = item.icon;
                  const active = item.id === activeMeta.id;
                  return (
                    <button
                      key={item.id}
                      type="button"
                      data-testid={`settings-nav-${item.id}`}
                      aria-current={active ? 'page' : undefined}
                      onClick={() => {
                        openSettings(item.id);
                        setMobileShowNav(false);
                      }}
                      className={`flex h-8 w-full items-center gap-2.5 rounded-lg px-2.5 text-ui-base transition-colors ${
                        active
                          ? 'bg-black/[0.06] font-medium text-foreground dark:bg-white/[0.1]'
                          : 'text-foreground-subtle hover:bg-hover hover:text-foreground'
                      }`}
                    >
                      <Icon className="size-4 shrink-0" />
                      <span className="truncate">{formatMsg(locale, item.titleKey)}</span>
                    </button>
                  );
                })}
              </div>
            );
          })}
        </div>
      </nav>

      {/* Right content viewport */}
      <div
        className={`nex-titlebar-top min-h-0 min-w-0 flex-1 overflow-y-auto bg-background px-4 pb-6 sm:px-8 sm:pb-10 ${
          isCompact && mobileShowNav ? 'hidden' : 'block'
        }`}
      >
        <div className="mx-auto w-full max-w-4xl">
          {isCompact && (
            <div className="mb-4 flex items-center justify-between gap-2">
              <button
                type="button"
                data-testid="settings-mobile-nav-back"
                onClick={() => setMobileShowNav(true)}
                className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border bg-card px-2.5 text-ui-xs font-medium text-foreground-subtle hover:bg-hover hover:text-foreground"
              >
                <ArrowLeft className="size-3.5" />
                <span>{formatMsg(locale, 'settings.mobile.allSections')}</span>
              </button>
              <button
                type="button"
                onClick={closeSettings}
                className="inline-flex h-8 items-center gap-1.5 rounded-lg bg-black/[0.05] px-2.5 text-ui-xs font-medium text-foreground hover:bg-hover dark:bg-white/[0.07]"
              >
                <span>{formatMsg(locale, 'workspace.backToWorkspace')}</span>
              </button>
            </div>
          )}
          <header className={providerHeaderRow ? '' : 'mb-6'}>
            <h1 className="text-[28px] font-bold tracking-tight text-foreground">
              {formatMsg(locale, activeMeta.titleKey)}
            </h1>
            {!providerHeaderRow ? (
              <p className="mt-2 text-ui-base text-foreground-subtle">
                {formatMsg(locale, activeMeta.descKey)}
              </p>
            ) : null}
          </header>

          {flashMsg ? (
            <div className="nex-toast-in fixed right-6 top-6 z-50 rounded-xl border border-border bg-card px-4 py-2.5 text-ui-sm font-medium text-foreground shadow-lg">
              {flashMsg}
            </div>
          ) : null}

          {activeMeta.id === 'general' && <GeneralSettingsSection onFlash={showFlash} />}
          {activeMeta.id === 'appearance' && <AppearanceSection onFlash={showFlash} />}
          {activeMeta.id === 'providers' && (
            <ProvidersAndModelsSection
              onFlash={showFlash}
              description={formatMsg(locale, activeMeta.descKey)}
            />
          )}
          {activeMeta.id === 'runners' && <RunnersSettingsSection onFlash={showFlash} />}
          {activeMeta.id === 'credentials' && <SettingsCredentialsSection onFlash={showFlash} />}
          {activeMeta.id === 'remote' && <SettingsRemoteAccessSection onFlash={showFlash} />}
          {activeMeta.id === 'browser' && <SettingsBrowserSection onFlash={showFlash} />}
          {activeMeta.id === 'agents' && <AgentsSettingsSection onFlash={showFlash} />}
          {activeMeta.id === 'memories' && <MemoriesSettingsSection onFlash={showFlash} />}
          {activeMeta.id === 'plugins' && (
            <SkillsAndPluginsSection mode="plugins" onFlash={showFlash} />
          )}
          {activeMeta.id === 'mcp' && <MCPSettingsSection onFlash={showFlash} />}
          {activeMeta.id === 'skills' && (
            <SkillsAndPluginsSection mode="skills" onFlash={showFlash} />
          )}
          {activeMeta.id === 'automations' && <AutomationsSection onFlash={showFlash} />}
          {activeMeta.id === 'usage' && <UsageAnalyticsSection />}
          {activeMeta.id === 'archives' && <ArchivesSettingsSection onFlash={showFlash} />}
        </div>
      </div>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* General                                                                    */
/* -------------------------------------------------------------------------- */

const THEME_OPTIONS = [
  { id: 'system' as const, icon: Monitor, labelKey: 'settings.theme.system' as MessageKey },
  { id: 'light' as const, icon: Sun, labelKey: 'settings.theme.light' as MessageKey },
  { id: 'dark' as const, icon: Moon, labelKey: 'settings.theme.dark' as MessageKey },
];

function GeneralSettingsSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const {
    locale,
    setLocale,
    permissionMode,
    setPermissionMode,
    preferences,
    hostInfo,
    remoteTransportStatus,
    reconnectRemote,
    activeServerUrl,
    openServerConnect,
    authEnabled,
    updateWebPassword,
    logoutWeb,
    refreshInitialData,
    updateThemeQuick,
    providers,
    models,
  } = useNexStore();

  const [autoArchiveEnabled, setAutoArchiveEnabled] = useState(
    preferences.auto_archive_after_days > 0,
  );
  const [archiveDays, setArchiveDays] = useState(
    preferences.auto_archive_after_days > 0 ? preferences.auto_archive_after_days : 30,
  );
  const [currentPwd, setCurrentPwd] = useState('');
  const [newPwd, setNewPwd] = useState('');
  const [confirmPwd, setConfirmPwd] = useState('');
  const [pwdError, setPwdError] = useState('');
  const [pwdBusy, setPwdBusy] = useState(false);
  const [notifyPerm, setNotifyPerm] = useState(() => hostBridge.getNotificationPermission());
  const [globalInstr, setGlobalInstr] = useState('');
  const [globalInstrPath, setGlobalInstrPath] = useState('~/.nex/AGENTS.md');
  const [globalInstrLoading, setGlobalInstrLoading] = useState(true);
  const [globalInstrSaving, setGlobalInstrSaving] = useState(false);

  useEffect(() => {
    let cancelled = false;
    apiFetch<{ content: string; path: string }>('/api/v1/instructions/global')
      .then((res) => {
        if (cancelled) return;
        setGlobalInstr(res?.content ?? '');
        if (res?.path) setGlobalInstrPath(res.path);
      })
      .catch(() => {})
      .finally(() => {
        if (!cancelled) setGlobalInstrLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const saveGlobalInstructions = async () => {
    setGlobalInstrSaving(true);
    try {
      const res = await apiFetch<{ content: string; path: string }>('/api/v1/instructions/global', {
        method: 'PUT',
        body: JSON.stringify({ content: globalInstr }),
      });
      if (res?.path) setGlobalInstrPath(res.path);
      onFlash(formatMsg(locale, 'settings.instructions.saved'));
    } catch (err) {
      onFlash(formatMsg(locale, 'settings.multimodalSaveFailed', { error: String(err) }));
    } finally {
      setGlobalInstrSaving(false);
    }
  };

  const notifyEnabled = preferences.notify_enabled !== false;
  const notifyOnComplete = preferences.notify_on_complete !== false;
  const notifyOnInput = preferences.notify_on_input !== false;
  const notifyOnlyBackground = preferences.notify_only_background !== false;

  const handleRequestNotifyPermission = async () => {
    const perm = await hostBridge.requestNotificationPermission();
    setNotifyPerm(perm);
  };

  const handleTestNotification = async () => {
    let perm = hostBridge.getNotificationPermission();
    if (hostBridge.kind === 'browser' && perm === 'default') {
      perm = await hostBridge.requestNotificationPermission();
    }
    setNotifyPerm(perm);
    const res = await hostBridge.notify({
      title: formatMsg(locale, 'notify.testTitle'),
      body: formatMsg(locale, 'notify.testBody'),
      sessionId: 'test:ping',
    });
    if (res.delivered) {
      onFlash(formatMsg(locale, 'settings.notifications.testSent'));
    } else if (res.reason === 'permission_denied' || res.reason === 'permission_required') {
      onFlash(formatMsg(locale, 'settings.notifications.testBlocked'));
    } else {
      onFlash(formatMsg(locale, 'settings.notifications.testUnsupported'));
    }
  };

  const handleSavePassword = async () => {
    setPwdError('');
    if (newPwd.length < 4) {
      setPwdError(formatMsg(locale, 'settings.passwordMustBeAtLeast'));
      return;
    }
    if (newPwd !== confirmPwd) {
      setPwdError(formatMsg(locale, 'settings.passwordsDoNotMatch'));
      return;
    }
    setPwdBusy(true);
    const res = await updateWebPassword(currentPwd, newPwd);
    setPwdBusy(false);
    if (!res.ok) {
      setPwdError(res.error || (formatMsg(locale, 'settings.failedToSavePassword')));
      return;
    }
    setCurrentPwd('');
    setNewPwd('');
    setConfirmPwd('');
    onFlash(formatMsg(locale, 'settings.webAccessPasswordUpdated'));
  };

  const handleClearPassword = async () => {
    setPwdError('');
    if (!currentPwd) {
      setPwdError(formatMsg(locale, 'settings.enterCurrentPasswordToDisable'));
      return;
    }
    setPwdBusy(true);
    const res = await updateWebPassword(currentPwd, '');
    setPwdBusy(false);
    if (!res.ok) {
      setPwdError(res.error || (formatMsg(locale, 'settings.failedToRemovePassword')));
      return;
    }
    setCurrentPwd('');
    setNewPwd('');
    setConfirmPwd('');
    onFlash(formatMsg(locale, 'settings.webAccessPasswordDisabled'));
  };

  const savePreferences = async (patch: Partial<UserPreferences>) => {
    const next = { ...preferences, ...patch };
    useNexStore.setState({ preferences: next });
    await apiFetch('/api/v1/preferences', {
      method: 'PUT',
      body: JSON.stringify(next),
    }).catch(() => {});
    await refreshInitialData();
  };

  const enabledProviderIds = new Set(providers.filter((p) => p.enabled).map((p) => p.id));
  const selectableGoalVerifierModels = models.filter(
    (m) => enabledProviderIds.size === 0 || enabledProviderIds.has(m.provider_id),
  );
  const goalMaxRounds = preferences.goal_max_rounds ?? 8;
  const goalMaxTurnsPerRound = preferences.goal_max_turns_per_round ?? 40;
  const goalMaxNoProgress = preferences.goal_max_consecutive_no_progress ?? 2;
  const goalVerifierValue =
    preferences.goal_verifier_provider_id && preferences.goal_verifier_model_id
      ? `${preferences.goal_verifier_provider_id}::${preferences.goal_verifier_model_id}`
      : '';

  const toggleAutoArchive = (enabled: boolean) => {
    setAutoArchiveEnabled(enabled);
    void savePreferences({ auto_archive_after_days: enabled ? archiveDays : 0 });
  };

  const changeArchiveDays = (days: number) => {
    setArchiveDays(days);
    if (autoArchiveEnabled) void savePreferences({ auto_archive_after_days: days });
  };

  return (
    <div className="space-y-6">
      {(hostInfo || activeServerUrl) && (
        <div data-testid="settings-server-connection" className="space-y-3">
          <SectionHeading title={formatMsg(locale, 'settings.connection.title')} />
          <SettingsGroupCard>
            <SettingsRow
              label={formatMsg(locale, 'settings.connection.modeLabel')}
              description={
                hostInfo?.connection_mode === 'paired'
                  ? `${hostInfo.active_host_name || hostInfo.active_node_id || ''}${activeServerUrl ? ` (${activeServerUrl})` : ''}`
                  : activeServerUrl || undefined
              }
            >
              <div className="flex flex-wrap items-center justify-end gap-2">
                <span className="rounded-md bg-tag px-2.5 py-1 text-ui-xs font-medium text-foreground">
                  {hostInfo?.connection_mode === 'local'
                    ? formatMsg(locale, 'settings.connection.modeLocal')
                    : hostInfo?.connection_mode === 'paired'
                      ? formatMsg(locale, 'settings.connection.modePaired')
                      : formatMsg(locale, 'settings.connection.modeRemote')}
                </span>
                {hostInfo?.connection_mode === 'paired' && (
                  <>
                    <span
                      data-testid="settings-remote-transport-badge"
                      className="rounded-md bg-success-surface px-2.5 py-1 text-ui-xs font-medium text-success-surface-fg"
                    >
                      {(() => {
                        const tr = remoteTransportStatus?.transport || hostInfo.active_transport || 'disconnected';
                        if (tr === 'lan') return formatMsg(locale, 'settings.connection.transportLan');
                        if (tr === 'direct') return formatMsg(locale, 'settings.connection.transportDirect');
                        if (tr === 'tsnet') return formatMsg(locale, 'settings.connection.transportTsnet');
                        if (tr === 'relay') return formatMsg(locale, 'settings.connection.transportRelay');
                        return formatMsg(locale, 'settings.connection.transportDisconnected');
                      })()}
                    </span>
                    <button
                      type="button"
                      onClick={() => void reconnectRemote()}
                      className="inline-flex h-8 items-center rounded-lg border border-border bg-card px-2.5 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover"
                    >
                      {formatMsg(locale, 'settings.connection.reconnectBtn')}
                    </button>
                  </>
                )}
                <button
                  type="button"
                  data-testid="settings-switch-server-btn"
                  onClick={openServerConnect}
                  className="inline-flex h-8 items-center rounded-lg border border-border bg-card px-3 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover"
                >
                  {formatMsg(locale, 'settings.connection.switchServer')}
                </button>
              </div>
            </SettingsRow>
          </SettingsGroupCard>
        </div>
      )}

      <SettingsGroupCard>
        <SettingsRow
          label={formatMsg(locale, 'settings.locale')}
          description={
            formatMsg(locale, 'settings.displayLanguageForTheApp')
          }
        >
          <select
            value={locale}
            onChange={(e) => setLocale(e.target.value as LocaleCode)}
            className={settingsSelectClass}
          >
            {SUPPORTED_LOCALES.map((loc) => (
              <option key={loc.code} value={loc.code}>
                {loc.nativeName}
              </option>
            ))}
          </select>
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.themeMode')}
          description={
            formatMsg(locale, 'settings.useLightDarkOrMatch')
          }
        >
          <div className="inline-flex w-full items-center gap-0.5 rounded-lg border border-border bg-surface p-0.5">
            {THEME_OPTIONS.map(({ id, icon: Icon, labelKey }) => {
              const active = preferences.theme === id;
              return (
                <button
                  key={id}
                  type="button"
                  onClick={() => {
                    void updateThemeQuick(id);
                    onFlash(formatMsg(locale, 'settings.themeUpdated'));
                  }}
                  className={`flex h-8 flex-1 items-center justify-center gap-1.5 rounded-md text-ui-sm transition-colors ${
                    active
                      ? 'bg-card font-medium text-foreground shadow-2xs'
                      : 'text-foreground-subtle hover:text-foreground'
                  }`}
                >
                  <Icon className="size-3.5 shrink-0" />
                  <span className="truncate">{formatMsg(locale, labelKey)}</span>
                </button>
              );
            })}
          </div>
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.defaultPermissionMode')}
          description={
            formatMsg(locale, 'settings.appliedToNewSessionsUntil')
          }
        >
          <select
            value={permissionMode}
            onChange={(e) =>
              setPermissionMode(e.target.value as 'full_access' | 'interactive' | 'read_only')
            }
            className={settingsSelectClass}
          >
            <option value="interactive">{formatMsg(locale, 'mode.default')}</option>
            <option value="full_access">{formatMsg(locale, 'mode.fullAccess')}</option>
            <option value="read_only">{formatMsg(locale, 'mode.readOnly')}</option>
          </select>
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.autoCheckUpdates')}
          description={formatMsg(locale, 'settings.autoCheckUpdatesDesc')}
        >
          <Toggle
            checked={preferences.auto_check_updates !== false}
            onChange={(checked) => void useNexStore.getState().setAutoCheckUpdates(checked)}
            label={formatMsg(locale, 'settings.autoCheckUpdates')}
          />
        </SettingsRow>

        {hostBridge.kind !== 'browser' && (
          <SettingsRow
            label={formatMsg(locale, 'settings.autoDownloadUpdates')}
            description={formatMsg(locale, 'settings.autoDownloadUpdatesDesc')}
          >
            <Toggle
              checked={Boolean(preferences.auto_download_updates)}
              onChange={(checked) => void useNexStore.getState().setAutoDownloadUpdates(checked)}
              label={formatMsg(locale, 'settings.autoDownloadUpdates')}
            />
          </SettingsRow>
        )}
      </SettingsGroupCard>

      <div data-testid="settings-global-instructions" className="space-y-3">
        <SectionHeading
          title={formatMsg(locale, 'settings.instructions.title')}
          description={formatMsg(locale, 'settings.instructions.desc')}
        />
        <SettingsGroupCard>
          <div className="space-y-3 p-5">
            <div className="flex flex-wrap items-center justify-between gap-2 text-ui-xs text-foreground-subtle">
              <span className="font-mono text-foreground-subtlest">
                {globalInstrPath || formatMsg(locale, 'settings.instructions.path')}
              </span>
              <span>{formatMsg(locale, 'settings.instructions.hint')}</span>
            </div>
            <textarea
              rows={8}
              value={globalInstr}
              disabled={globalInstrLoading}
              onChange={(e) => setGlobalInstr(e.target.value)}
              placeholder={
                globalInstrLoading
                  ? formatMsg(locale, 'settings.instructions.loading')
                  : formatMsg(locale, 'settings.instructions.placeholder')
              }
              className="w-full resize-y rounded-lg border border-border bg-input p-3 font-mono text-ui-sm text-foreground placeholder-foreground-subtlest outline-none focus:border-primary"
            />
            <div className="flex items-center justify-end">
              <button
                type="button"
                disabled={globalInstrLoading || globalInstrSaving}
                onClick={() => void saveGlobalInstructions()}
                className="h-8 rounded-lg bg-primary px-4 text-ui-sm font-medium text-primary-foreground hover:opacity-90 disabled:opacity-50"
              >
                {globalInstrSaving
                  ? formatMsg(locale, 'settings.saving')
                  : formatMsg(locale, 'settings.saveChanges')}
              </button>
            </div>
          </div>
        </SettingsGroupCard>
      </div>

      <div data-testid="settings-notifications" className="space-y-3">
        <SectionHeading
          title={formatMsg(locale, 'settings.notifications.title')}
          description={formatMsg(locale, 'settings.notifications.description')}
        />

        <SettingsGroupCard>
          {hostBridge.kind === 'browser' ? (
            <SettingsRow
              label={
                <span className="inline-flex items-center gap-2">
                  <Bell className="size-4 text-foreground-subtle" />
                  <span>{formatMsg(locale, 'settings.notifications.browserPermission')}</span>
                </span>
              }
              description={formatMsg(locale, 'settings.notifications.browserPermissionDesc')}
            >
              <div className="flex flex-wrap items-center justify-end gap-2">
                <span
                  data-testid="settings-notify-permission-badge"
                  className={`rounded-md px-2.5 py-1 text-ui-xs font-medium ${
                    notifyPerm === 'granted'
                      ? 'bg-success-surface text-success-surface-fg'
                      : 'bg-surface text-foreground-subtle'
                  }`}
                >
                  {notifyPerm === 'granted'
                    ? formatMsg(locale, 'settings.notifications.statusGranted')
                    : notifyPerm === 'denied'
                      ? formatMsg(locale, 'settings.notifications.statusDenied')
                      : notifyPerm === 'unsupported'
                        ? formatMsg(locale, 'settings.notifications.statusUnsupported')
                        : formatMsg(locale, 'settings.notifications.statusDefault')}
                </span>
                {notifyPerm === 'default' && (
                  <button
                    type="button"
                    data-testid="settings-notify-request-btn"
                    onClick={() => void handleRequestNotifyPermission()}
                    className="inline-flex h-8 items-center rounded-lg border border-border bg-card px-3 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover"
                  >
                    {formatMsg(locale, 'settings.notifications.enableBrowserBtn')}
                  </button>
                )}
              </div>
            </SettingsRow>
          ) : (
            <SettingsRow
              label={
                <span className="inline-flex items-center gap-2">
                  <Bell className="size-4 text-foreground-subtle" />
                  <span>{formatMsg(locale, 'settings.notifications.nativeStatus')}</span>
                </span>
              }
              description={formatMsg(locale, 'settings.notifications.nativeStatusDesc')}
            >
              <span
                data-testid="settings-notify-permission-badge"
                className={`rounded-md px-2.5 py-1 text-ui-xs font-medium ${
                  hostInfo?.notifications_supported !== false
                    ? 'bg-success-surface text-success-surface-fg'
                    : 'bg-surface text-foreground-subtle'
                }`}
              >
                {hostInfo?.notifications_supported !== false
                  ? formatMsg(locale, 'settings.notifications.statusNativeSupported')
                  : formatMsg(locale, 'settings.notifications.statusNativeUnpackaged')}
              </span>
            </SettingsRow>
          )}

          <SettingsRow
            label={formatMsg(locale, 'settings.notifications.enabled')}
            description={formatMsg(locale, 'settings.notifications.enabledDesc')}
          >
            <Toggle
              testId="settings-notify-enabled"
              checked={notifyEnabled}
              onChange={(checked) => void savePreferences({ notify_enabled: checked })}
              label={formatMsg(locale, 'settings.notifications.enabled')}
            />
          </SettingsRow>

          {notifyEnabled && (
            <>
              <SettingsRow
                label={formatMsg(locale, 'settings.notifications.onComplete')}
                description={formatMsg(locale, 'settings.notifications.onCompleteDesc')}
              >
                <Toggle
                  testId="settings-notify-complete"
                  checked={notifyOnComplete}
                  onChange={(checked) => void savePreferences({ notify_on_complete: checked })}
                  label={formatMsg(locale, 'settings.notifications.onComplete')}
                />
              </SettingsRow>

              <SettingsRow
                label={formatMsg(locale, 'settings.notifications.onInput')}
                description={formatMsg(locale, 'settings.notifications.onInputDesc')}
              >
                <Toggle
                  testId="settings-notify-input"
                  checked={notifyOnInput}
                  onChange={(checked) => void savePreferences({ notify_on_input: checked })}
                  label={formatMsg(locale, 'settings.notifications.onInput')}
                />
              </SettingsRow>

              <SettingsRow
                label={formatMsg(locale, 'settings.notifications.onlyBackground')}
                description={formatMsg(locale, 'settings.notifications.onlyBackgroundDesc')}
              >
                <Toggle
                  testId="settings-notify-background"
                  checked={notifyOnlyBackground}
                  onChange={(checked) => void savePreferences({ notify_only_background: checked })}
                  label={formatMsg(locale, 'settings.notifications.onlyBackground')}
                />
              </SettingsRow>
            </>
          )}

          <SettingsRow
            label={formatMsg(locale, 'settings.notifications.testLabel')}
            description={formatMsg(locale, 'settings.notifications.testDesc')}
          >
            <button
              type="button"
              data-testid="settings-notify-test-btn"
              onClick={() => void handleTestNotification()}
              className="inline-flex h-8 items-center rounded-lg border border-border bg-card px-3 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover"
            >
              {formatMsg(locale, 'settings.notifications.testBtn')}
            </button>
          </SettingsRow>
        </SettingsGroupCard>
      </div>

      <SectionHeading
        title={formatMsg(locale, 'settings.sessionStorage')}
        description={
          formatMsg(locale, 'settings.keepTheActiveWorkspaceLean')
        }
      />

      <SettingsGroupCard>
        <SettingsRow
          label={formatMsg(locale, 'settings.autoArchiveOldSessions')}
          description={
            formatMsg(locale, 'settings.idleSessionsMoveToArchive')
          }
        >
          <Toggle
            checked={autoArchiveEnabled}
            onChange={toggleAutoArchive}
            label={formatMsg(locale, 'settings.autoArchiveOldSessions')}
          />
        </SettingsRow>

        {autoArchiveEnabled ? (
          <SettingsRow
            label={formatMsg(locale, 'settings.archiveAfter')}
            description={
              formatMsg(locale, 'settings.sessionsOlderThanThisThreshold')
            }
          >
            <select
              value={archiveDays}
              onChange={(e) => changeArchiveDays(Number(e.target.value))}
              className={settingsSelectClass}
            >
              {[3, 7, 14, 30].map((d) => (
                <option key={d} value={d}>
                  {formatMsg(locale, 'settings.days', { d })}
                </option>
              ))}
            </select>
          </SettingsRow>
        ) : null}
      </SettingsGroupCard>

      <SectionHeading
        title={formatMsg(locale, 'settings.goal.title')}
        description={formatMsg(locale, 'settings.goal.desc')}
      />

      <SettingsGroupCard>
        <SettingsRow
          label={formatMsg(locale, 'settings.goal.maxRounds')}
          description={formatMsg(locale, 'settings.goal.maxRoundsDesc')}
        >
          <input
            type="number"
            min={1}
            max={30}
            data-testid="settings-goal-max-rounds"
            value={goalMaxRounds}
            onChange={(e) => {
              const raw = Number(e.target.value);
              const clamped = Number.isFinite(raw) ? Math.max(1, Math.min(30, Math.round(raw))) : 8;
              void savePreferences({ goal_max_rounds: clamped });
            }}
            className={`${settingsInputClass} w-24 text-right tabular-nums`}
          />
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.goal.maxTurnsPerRound')}
          description={formatMsg(locale, 'settings.goal.maxTurnsPerRoundDesc')}
        >
          <input
            type="number"
            min={10}
            max={100}
            data-testid="settings-goal-max-turns"
            value={goalMaxTurnsPerRound}
            onChange={(e) => {
              const raw = Number(e.target.value);
              const clamped = Number.isFinite(raw) ? Math.max(10, Math.min(100, Math.round(raw))) : 40;
              void savePreferences({ goal_max_turns_per_round: clamped });
            }}
            className={`${settingsInputClass} w-24 text-right tabular-nums`}
          />
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.goal.maxNoProgress')}
          description={formatMsg(locale, 'settings.goal.maxNoProgressDesc')}
        >
          <input
            type="number"
            min={1}
            max={5}
            data-testid="settings-goal-max-no-progress"
            value={goalMaxNoProgress}
            onChange={(e) => {
              const raw = Number(e.target.value);
              const clamped = Number.isFinite(raw) ? Math.max(1, Math.min(5, Math.round(raw))) : 2;
              void savePreferences({ goal_max_consecutive_no_progress: clamped });
            }}
            className={`${settingsInputClass} w-24 text-right tabular-nums`}
          />
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.goal.verifierModel')}
          description={formatMsg(locale, 'settings.goal.verifierModelDesc')}
        >
          <select
            data-testid="settings-goal-verifier-model"
            value={goalVerifierValue}
            onChange={(e) => {
              const val = e.target.value;
              if (!val) {
                void savePreferences({ goal_verifier_provider_id: '', goal_verifier_model_id: '' });
              } else {
                const [pid, mid] = val.split('::');
                void savePreferences({ goal_verifier_provider_id: pid, goal_verifier_model_id: mid });
              }
            }}
            className={settingsSelectClass}
          >
            <option value="">{formatMsg(locale, 'capabilities.followActiveSessionModel')}</option>
            {selectableGoalVerifierModels.map((m) => (
              <option key={`${m.provider_id}::${m.id}`} value={`${m.provider_id}::${m.id}`}>
                {m.display_name || m.id} ({m.provider_id})
              </option>
            ))}
          </select>
        </SettingsRow>
      </SettingsGroupCard>

      <SectionHeading
        title={formatMsg(locale, 'settings.webAccessPassword')}
        description={
          formatMsg(locale, 'settings.requireAPasswordWhenOpening')
        }
      />

      <SettingsGroupCard>
        <div className="flex flex-wrap items-center justify-between gap-3 px-5 py-4">
          <div className="flex items-center gap-2.5">
            <Lock className="size-4 text-foreground-subtle" />
            <span className="text-ui-base font-medium text-foreground">
              {formatMsg(locale, 'settings.passwordProtection')}
            </span>
            <span
              data-testid="web-auth-status-badge"
              className={`rounded-md px-2 py-0.5 text-ui-xs font-medium ${
                authEnabled
                  ? 'bg-success-surface text-success-surface-fg'
                  : 'bg-surface text-foreground-subtle'
              }`}
            >
              {authEnabled
                ? formatMsg(locale, 'common.enabled2')
                : formatMsg(locale, 'common.disabled2')}
            </span>
          </div>
          {authEnabled && (
            <button
              type="button"
              data-testid="web-logout-btn"
              onClick={() => void logoutWeb()}
              className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border bg-surface px-3 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover"
            >
              <LogOut className="size-3.5" />
              <span>{formatMsg(locale, 'settings.lockLogOut')}</span>
            </button>
          )}
        </div>

        <div className="space-y-3 border-t border-border px-5 py-4">
          <div className={`grid grid-cols-1 gap-3 ${authEnabled ? 'sm:grid-cols-3' : 'sm:grid-cols-2'}`}>
            {authEnabled && (
              <label className="block space-y-1">
                <span className="text-ui-xs text-foreground-subtle">
                  {formatMsg(locale, 'settings.currentPassword')}
                </span>
                <input
                  type="password"
                  data-testid="web-password-current"
                  value={currentPwd}
                  onChange={(e) => setCurrentPwd(e.target.value)}
                  placeholder={formatMsg(locale, 'settings.requiredToChangeRemove')}
                  className={settingsInputClass}
                />
              </label>
            )}
            <label className="block space-y-1">
              <span className="text-ui-xs text-foreground-subtle">
                {authEnabled
                  ? formatMsg(locale, 'settings.newPassword')
                  : formatMsg(locale, 'settings.setPassword')}
              </span>
              <input
                type="password"
                data-testid="web-password-new"
                value={newPwd}
                onChange={(e) => setNewPwd(e.target.value)}
                placeholder={formatMsg(locale, 'settings.atLeast4Characters')}
                className={settingsInputClass}
              />
            </label>
            <label className="block space-y-1">
              <span className="text-ui-xs text-foreground-subtle">
                {formatMsg(locale, 'settings.confirmPassword')}
              </span>
              <input
                type="password"
                data-testid="web-password-confirm"
                value={confirmPwd}
                onChange={(e) => setConfirmPwd(e.target.value)}
                placeholder={formatMsg(locale, 'settings.repeatNewPassword')}
                className={settingsInputClass}
              />
            </label>
          </div>

          {pwdError && (
            <p className="text-ui-xs text-destructive">{pwdError}</p>
          )}

          <div className="flex flex-wrap items-center justify-end gap-2">
            {authEnabled && (
              <button
                type="button"
                data-testid="web-password-clear-btn"
                disabled={pwdBusy}
                onClick={() => void handleClearPassword()}
                className="h-8 rounded-lg border border-destructive/30 px-3 text-ui-xs font-medium text-destructive transition-colors hover:bg-destructive-surface disabled:opacity-50"
              >
                {formatMsg(locale, 'settings.disablePassword')}
              </button>
            )}
            <button
              type="button"
              data-testid="web-password-save-btn"
              disabled={pwdBusy || !newPwd}
              onClick={() => void handleSavePassword()}
              className="h-8 rounded-lg bg-foreground px-3.5 text-ui-xs font-medium text-background transition-opacity hover:opacity-90 disabled:opacity-45"
            >
              {authEnabled
                ? formatMsg(locale, 'settings.updatePassword')
                : formatMsg(locale, 'settings.enablePassword')}
            </button>
          </div>
        </div>
      </SettingsGroupCard>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* Appearance                                                                 */
/* -------------------------------------------------------------------------- */

const THEME_CARDS = [
  {
    id: 'system' as const,
    icon: Monitor,
    labelKey: 'settings.theme.system' as MessageKey,
    preview: 'bg-gradient-to-r from-[#f8f8f8] to-[#161616]',
    side: 'bg-black/10',
    line: 'bg-black/25',
    lineSoft: 'bg-black/10',
  },
  {
    id: 'light' as const,
    icon: Sun,
    labelKey: 'settings.theme.light' as MessageKey,
    preview: 'bg-[#f8f8f8]',
    side: 'bg-[#ececee]',
    line: 'bg-black/25',
    lineSoft: 'bg-black/10',
  },
  {
    id: 'dark' as const,
    icon: Moon,
    labelKey: 'settings.theme.dark' as MessageKey,
    preview: 'bg-[#161616]',
    side: 'bg-[#202020]',
    line: 'bg-white/30',
    lineSoft: 'bg-white/12',
  },
];

function AppearanceSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const { locale, preferences, refreshInitialData, updateThemeQuick } = useNexStore();

  const [uiFontPx, setUiFontPx] = useState(() => hostBridge.getDeviceFontScale());
  const [sansFont, setSansFont] = useState(preferences.font_family);
  const [monoFont, setMonoFont] = useState(preferences.mono_font_family);
  const [expandThinking, setExpandThinking] = useState(preferences.expand_thinking);
  const [runnerBadgeStyle, setRunnerBadgeStyle] = useState<'tinted' | 'neutral'>(
    preferences.runner_badge_style || 'tinted',
  );
  const [saving, setSaving] = useState(false);

  const chooseRunnerBadgeStyle = async (style: 'tinted' | 'neutral') => {
    setRunnerBadgeStyle(style);
    const nextPrefs: UserPreferences = { ...preferences, runner_badge_style: style };
    useNexStore.setState({ preferences: nextPrefs });
    await apiFetch('/api/v1/preferences', {
      method: 'PUT',
      body: JSON.stringify(nextPrefs),
    }).catch(() => {});
  };

  const saveAppearance = async () => {
    setSaving(true);
    try {
      hostBridge.setDeviceFontScale(uiFontPx);
      await apiFetch('/api/v1/preferences', {
        method: 'PUT',
        body: JSON.stringify({
          ...preferences,
          font_family: sansFont.trim() || preferences.font_family,
          mono_font_family: monoFont.trim() || preferences.mono_font_family,
          expand_thinking: expandThinking,
          runner_badge_style: runnerBadgeStyle,
        }),
      }).catch(() => {});
      await refreshInitialData();
      onFlash(formatMsg(locale, 'settings.appearanceSettingsSaved'));
    } finally {
      setSaving(false);
    }
  };

  const chooseTheme = async (theme: UserPreferences['theme']) => {
    await updateThemeQuick(theme);
    onFlash(formatMsg(locale, 'settings.themeUpdated'));
  };

  return (
    <div className="space-y-6">
      <SettingsGroupCard>
        <div className="space-y-3 px-5 py-4">
          <div>
            <div className="text-ui-base font-medium text-foreground">
              {formatMsg(locale, 'settings.theme')}
            </div>
            <p className="mt-1 text-ui-sm leading-relaxed text-foreground-subtle">
              {formatMsg(locale, 'settings.useLightDarkOrMatch')}
            </p>
          </div>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            {THEME_CARDS.map(({ id, icon: Icon, labelKey, preview, side, line, lineSoft }) => {
              const active = preferences.theme === id;
              return (
                <button
                  key={id}
                  type="button"
                  onClick={() => void chooseTheme(id)}
                  className={`overflow-hidden rounded-xl border text-left transition-colors ${
                    active
                      ? 'border-brand-blue bg-card-selected'
                      : 'border-border bg-card hover:border-border-hover'
                  }`}
                >
                  <div className={`flex h-16 gap-1.5 p-2 ${preview}`}>
                    <div className={`w-1/4 rounded ${side}`} />
                    <div className="flex-1 space-y-1 py-0.5">
                      <div className={`h-2 w-2/3 rounded ${line}`} />
                      <div className={`h-1.5 w-full rounded ${lineSoft}`} />
                      <div className={`h-1.5 w-4/5 rounded ${lineSoft}`} />
                    </div>
                  </div>
                  <div className="flex items-center justify-between gap-2 px-3 py-2">
                    <span className="flex min-w-0 items-center gap-1.5">
                      <Icon className="size-3.5 shrink-0 text-foreground-subtle" />
                      <span className="truncate text-ui-sm font-medium text-foreground">
                        {formatMsg(locale, labelKey)}
                      </span>
                    </span>
                    {active ? (
                      <Check className="size-3.5 shrink-0 text-brand-blue" />
                    ) : null}
                  </div>
                </button>
              );
            })}
          </div>
        </div>

        <SettingsRow
          label={formatMsg(locale, 'settings.interfaceFontSize')}
          description={
            formatMsg(locale, 'settings.adjustInterfaceTextBetween12px')
          }
        >
          <input
            type="range"
            min={12}
            max={18}
            value={uiFontPx}
            onChange={(e) => {
              const v = Number(e.target.value);
              setUiFontPx(v);
              hostBridge.setDeviceFontScale(v);
            }}
            className="min-w-0 flex-1 cursor-pointer accent-brand-blue"
          />
          <span className="w-10 shrink-0 text-right font-mono text-ui-sm tabular-nums text-foreground">
            {uiFontPx}px
          </span>
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.runnerBadgeStyle')}
          description={formatMsg(locale, 'settings.runnerBadgeStyleDesc')}
        >
          <div className="inline-flex items-center rounded-lg bg-selected/80 p-0.5 text-ui-xs">
            {(
              [
                { id: 'tinted', labelKey: 'settings.runnerBadgeStyleTinted' },
                { id: 'neutral', labelKey: 'settings.runnerBadgeStyleNeutral' },
              ] as const
            ).map((opt) => (
              <button
                key={opt.id}
                type="button"
                data-testid={`appearance-runner-badge-${opt.id}`}
                onClick={() => void chooseRunnerBadgeStyle(opt.id)}
                className={`rounded-md px-2.5 py-1 font-medium transition-all ${
                  runnerBadgeStyle === opt.id
                    ? 'bg-background text-foreground shadow-[0_1px_2px_rgba(0,0,0,0.08)]'
                    : 'text-foreground-subtle hover:text-foreground'
                }`}
              >
                {formatMsg(locale, opt.labelKey)}
              </button>
            ))}
          </div>
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.expandThinkingByDefault')}
          description={
            formatMsg(locale, 'settings.reasoningBlocksAreExpandedWhen')
          }
        >
          <Toggle
            checked={expandThinking}
            onChange={setExpandThinking}
            label={formatMsg(locale, 'settings.expandThinkingByDefault')}
          />
        </SettingsRow>
      </SettingsGroupCard>

      <SectionHeading
        title={formatMsg(locale, 'settings.code')}
        description={
          formatMsg(locale, 'settings.configureCodeTypographyTheseSettings')
        }
      />

      <SettingsGroupCard>
        <SettingsRow
          label={formatMsg(locale, 'settings.interfaceFontSans')}
          description={formatMsg(locale, 'settings.sansSerifStackUsedAcross')}
        >
          <input
            value={sansFont}
            onChange={(e) => setSansFont(e.target.value)}
            placeholder="Inter, system-ui, sans-serif"
            className={settingsInputClass}
          />
        </SettingsRow>

        <SettingsRow
          label={formatMsg(locale, 'settings.codeTerminalFontMono')}
          description={
            formatMsg(locale, 'settings.monospaceStackUsedByCode')
          }
        >
          <input
            value={monoFont}
            onChange={(e) => setMonoFont(e.target.value)}
            placeholder="JetBrains Mono, Menlo, monospace"
            className={settingsMonoInputClass}
          />
        </SettingsRow>

        <div className="space-y-2 px-5 py-4">
          <div className="text-ui-base font-medium text-foreground">
            {formatMsg(locale, 'settings.livePreview')}
          </div>
          <HighlightedCode
            language="ts"
            style={{ fontFamily: monoFont || undefined, fontSize: `${Math.max(11, uiFontPx - 1)}px` }}
            code={`export async function reviewPatch(patch: string) {\n  const diff = await readDiff(patch);\n  return diff.hunks.map((hunk) => hunk.header);\n}`}
          />
        </div>

        <div className="flex items-center justify-end gap-2 px-5 py-4">
          <button
            type="button"
            disabled={saving}
            onClick={() => void saveAppearance()}
            className="h-8 rounded-lg bg-primary px-4 text-ui-sm font-medium text-primary-foreground hover:opacity-90 disabled:opacity-50"
          >
            {formatMsg(locale, 'settings.saveChanges')}
          </button>
        </div>
      </SettingsGroupCard>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* Model providers                                                            */
/* -------------------------------------------------------------------------- */

interface ProviderDraft {
  id: string;
  name: string;
  driver: ProviderRecord['driver'];
  baseUrl: string;
  apiKey: string;
  proxyMode: ProviderRecord['proxy_mode'];
  enabled: boolean;
}

const toDraft = (p: ProviderRecord): ProviderDraft => ({
  id: p.id,
  name: p.name,
  driver: p.driver,
  baseUrl: p.base_url,
  apiKey: p.api_key,
  proxyMode: p.proxy_mode,
  enabled: p.enabled,
});

const contextLabel = (n: number): string => {
  if (n >= 1_000_000) return `${Number((n / 1_000_000).toFixed(1))}M`;
  if (n >= 1_000) return `${Math.round(n / 1_000)}K`;
  return String(n);
};

const slugify = (value: string): string =>
  value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');

function ModelBadge({ children }: { children: ReactNode }) {
  return (
    <span className="shrink-0 rounded-md border border-border bg-surface px-1.5 py-0.5 font-mono text-ui-xs text-foreground-subtle">
      {children}
    </span>
  );
}

function ProvidersAndModelsSection({
  onFlash,
  description,
}: {
  onFlash: (msg: string) => void;
  description: string;
}) {
  const {
    locale,
    providers,
    models,
    refreshInitialData,
    hiddenModelKeys,
    toggleModelVisibility,
    modelOrder,
    reorderProviderModels,
  } = useNexStore();

  const [providersTab, setProvidersTab] = useState<'chat' | 'image' | 'classifier'>('chat');
  const [draft, setDraft] = useState<ProviderDraft | null>(null);
  const [showKey, setShowKey] = useState(false);
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [showAddModel, setShowAddModel] = useState(false);
  const [newModelId, setNewModelId] = useState('');
  const [newModelName, setNewModelName] = useState('');
  const [newCtxWin, setNewCtxWin] = useState(128_000);
  const [newMaxOut, setNewMaxOut] = useState(16_384);
  const [newSupportsVision, setNewSupportsVision] = useState(true);
  const [newSupportsPDF, setNewSupportsPDF] = useState(false);
  const [newSupportsTools, setNewSupportsTools] = useState(true);
  const [newSupportsReasoning, setNewSupportsReasoning] = useState(true);
  const [newSupportsCaching, setNewSupportsCaching] = useState(true);
  const [newReasoningLevels, setNewReasoningLevels] = useState('');
  const [newReasoningMap, setNewReasoningMap] = useState('');
  const [editingModel, setEditingModel] = useState<ModelMeta | null>(null);
  const [lookedUpMeta, setLookedUpMeta] = useState<ModelMeta | null>(null);
  const [fetchingInfo, setFetchingInfo] = useState(false);
  const [fetchRemoteOpen, setFetchRemoteOpen] = useState(false);
  const [remoteModels, setRemoteModels] = useState<string[]>([]);
  const [remoteMatchedMeta, setRemoteMatchedMeta] = useState<Record<string, ModelMeta>>({});
  const [remoteFilter, setRemoteFilter] = useState('');
  const [remoteSelected, setRemoteSelected] = useState<Record<string, boolean>>({});
  const [fetchingRemote, setFetchingRemote] = useState(false);
  const [addingRemote, setAddingRemote] = useState(false);
  const [testingModelId, setTestingModelId] = useState<string | null>(null);
  const [testResults, setTestResults] = useState<Record<string, { ok: boolean; msg: string }>>({});
  const [syncing, setSyncing] = useState(false);
  const [hiddenModelIds, setHiddenModelIds] = useState<Record<string, boolean>>({});
  const [draggingModelId, setDraggingModelId] = useState<string | null>(null);

  useEffect(() => {
    if (draft) return;
    const first = providers[0];
    if (first) setDraft(toDraft(first));
  }, [draft, providers]);

  const startNewProvider = () => {
    setShowKey(false);
    setShowAdvanced(false);
    setDraft({
      id: '',
      name: formatMsg(locale, 'settings.newProvider'),
      driver: 'openai_responses',
      baseUrl: 'https://api.openai.com/v1',
      apiKey: '',
      proxyMode: 'direct',
      enabled: true,
    });
  };

  const persistDraft = async (override?: Partial<ProviderDraft>) => {
    if (!draft) return;
    const next = { ...draft, ...override };
    const name = next.name.trim();
    if (!name) return;
    const existing = providers.find((p) => p.id === draft.id);
    const unchanged =
      existing &&
      existing.name === name &&
      existing.driver === next.driver &&
      existing.base_url === next.baseUrl.trim() &&
      existing.api_key === next.apiKey &&
      existing.proxy_mode === next.proxyMode &&
      existing.enabled === next.enabled;
    if (unchanged && !override) return;

    const id = next.id.trim() || slugify(name) || `custom-${providers.length + 1}`;
    await apiFetch('/api/v1/providers', {
      method: 'POST',
      body: JSON.stringify({
        id,
        name,
        driver: next.driver,
        base_url: next.baseUrl.trim(),
        api_key: next.apiKey,
        proxy_mode: next.proxyMode,
        enabled: next.enabled,
      }),
    }).catch(() => {});
    await refreshInitialData();
    setDraft({ ...next, id });
    onFlash(formatMsg(locale, 'settings.providerSaved', { name }));
  };

  const removeProvider = async (id: string) => {
    await apiFetch(`/api/v1/providers/${encodeURIComponent(id)}`, { method: 'DELETE' }).catch(
      () => {},
    );
    await refreshInitialData();
    setDraft(null);
    onFlash(formatMsg(locale, 'settings.providerDeleted'));
  };

  const syncModelsDev = async () => {
    setSyncing(true);
    try {
      const res = await apiFetch<{ synced?: number; synced_count?: number }>('/api/v1/models/sync', {
        method: 'POST',
        body: JSON.stringify({}),
      });
      await refreshInitialData();
      const syncedCount = res.synced_count ?? res.synced ?? 0;
      onFlash(
        formatMsg(locale, 'settings.syncedModelsFromModelsDev', { synced_count: syncedCount }),
      );
    } catch (e) {
      onFlash(
        formatMsg(locale, 'settings.modelsDevSyncFailed', { message: (e as Error).message }),
      );
    } finally {
      setSyncing(false);
    }
  };

  const lookupModelInfo = async () => {
    const id = newModelId.trim();
    if (!id) return;
    setFetchingInfo(true);
    try {
      const res = await apiFetch<{ found: boolean; meta?: ModelMeta }>('/api/v1/models/lookup', {
        method: 'POST',
        body: JSON.stringify({ model_id: id, provider_id: draft?.id || '' }),
      });
      if (res.found && res.meta) {
        setLookedUpMeta(res.meta);
        if (!newModelName.trim()) setNewModelName(res.meta.display_name || id);
        if (res.meta.context_window > 0) setNewCtxWin(res.meta.context_window);
        if (res.meta.max_output_tokens > 0) setNewMaxOut(res.meta.max_output_tokens);
        setNewSupportsVision(Boolean(res.meta.supports_vision));
        setNewSupportsPDF(Boolean(res.meta.supports_pdf));
        setNewSupportsTools(res.meta.supports_tools ?? true);
        setNewSupportsReasoning(Boolean(res.meta.supports_reasoning));
        setNewSupportsCaching(Boolean(res.meta.supports_caching));
        setNewReasoningLevels((res.meta.reasoning_levels || []).join(', '));
        onFlash(formatMsg(locale, 'settings.loadedMetadataFor', { id: res.meta.id }));
      } else {
        onFlash(formatMsg(locale, 'settings.noMatchingModelFoundIn'));
      }
    } catch (e) {
      onFlash((e as Error).message);
    } finally {
      setFetchingInfo(false);
    }
  };

  const fetchFromV1Models = async () => {
    if (!draft?.baseUrl.trim()) return;
    setFetchingRemote(true);
    setFetchRemoteOpen(true);
    setRemoteModels([]);
    setRemoteMatchedMeta({});
    setRemoteSelected({});
    setRemoteFilter('');
    try {
      const res = await apiFetch<{ models: string[]; matched_meta?: Record<string, ModelMeta> }>(
        '/api/v1/models/fetch-remote',
        {
          method: 'POST',
          body: JSON.stringify({
            base_url: draft.baseUrl.trim(),
            api_key: draft.apiKey,
            provider_id: draft.id,
          }),
        },
      );
      const existingIds = new Set(
        models.filter((m) => m.provider_id === draft?.id).map((m) => m.id),
      );
      setRemoteModels((res.models || []).filter((id) => !existingIds.has(id)));
      setRemoteMatchedMeta(res.matched_meta || {});
    } catch (e) {
      onFlash(formatMsg(locale, 'settings.fetchV1ModelsFailed', { message: (e as Error).message }));
      setFetchRemoteOpen(false);
    } finally {
      setFetchingRemote(false);
    }
  };

  const addSelectedRemoteModels = async () => {
    if (!draft?.id) {
      onFlash(formatMsg(locale, 'settings.saveTheProviderBeforeAdding'));
      return;
    }
    const chosen = Object.entries(remoteSelected)
      .filter(([, v]) => v)
      .map(([k]) => k);
    if (chosen.length === 0) return;
    setAddingRemote(true);
    try {
      let metaById: Record<string, ModelMeta> = { ...remoteMatchedMeta };
      const missingIds = chosen.filter((id) => !metaById[id]);
      if (missingIds.length > 0) {
        try {
          const lookup = await apiFetch<{ results?: Record<string, ModelMeta> }>('/api/v1/models/lookup', {
            method: 'POST',
            body: JSON.stringify({ model_ids: missingIds, provider_id: draft.id }),
          });
          if (lookup.results) {
            metaById = { ...metaById, ...lookup.results };
          }
        } catch {
          // fallback to defaults
        }
      }
      const batchPayload = chosen.map((id) => {
        const meta: Partial<ModelMeta> = metaById[id] || {};
        return {
          id,
          provider_id: draft.id,
          display_name: meta.display_name || id,
          source: meta.source || 'models.dev',
          context_window: meta.context_window || 128000,
          max_output_tokens: meta.max_output_tokens || 16384,
          supports_vision: meta.supports_vision ?? false,
          supports_pdf: meta.supports_pdf ?? false,
          supports_audio: meta.supports_audio ?? false,
          supports_video: meta.supports_video ?? false,
          supports_tools: meta.supports_tools ?? true,
          supports_reasoning: meta.supports_reasoning ?? false,
          supports_caching: meta.supports_caching ?? false,
          reasoning_levels: meta.reasoning_levels,
          pricing: meta.pricing,
        };
      });
      await apiFetch('/api/v1/models', {
        method: 'POST',
        body: JSON.stringify(batchPayload),
      });
      setFetchRemoteOpen(false);
      await refreshInitialData();
      onFlash(formatMsg(locale, 'settings.addedModels', { length: chosen.length }));
    } finally {
      setAddingRemote(false);
    }
  };

  const testModelConnection = async (m: ModelMeta) => {
    if (!draft) return;
    setTestingModelId(m.id);
    try {
      const res = await apiFetch<{ ok: boolean; reply?: string; error?: string; latency_ms?: number }>(
        '/api/v1/models/test',
        {
          method: 'POST',
          body: JSON.stringify({
            driver: draft.driver,
            base_url: draft.baseUrl.trim(),
            api_key: draft.apiKey,
            model_id: m.id,
          }),
        },
      );
      if (res.ok) {
        setTestResults((prev) => ({ ...prev, [m.id]: { ok: true, msg: `${res.latency_ms ?? 0}ms` } }));
        onFlash(formatMsg(locale, 'settings.modelOkMs', { id: m.id, v: res.latency_ms ?? 0 }));
      } else {
        setTestResults((prev) => ({ ...prev, [m.id]: { ok: false, msg: res.error || 'Error' } }));
        onFlash(formatMsg(locale, 'settings.testFailed', { error: res.error || '' }));
      }
    } catch (e) {
      setTestResults((prev) => ({ ...prev, [m.id]: { ok: false, msg: (e as Error).message } }));
      onFlash((e as Error).message);
    } finally {
      setTestingModelId(null);
    }
  };

  const parseReasoningLevelsInput = (raw: string): string[] | undefined => {
    const parts = raw
      .split(/[,，\s]+/)
      .map((s) => s.trim())
      .filter(Boolean);
    return parts.length > 0 ? parts : undefined;
  };

  const parseReasoningMapInput = (raw: string): Record<string, string> | undefined => {
    const out: Record<string, string> = {};
    for (const pair of raw.split(/[,，;\n]+/)) {
      const idx = pair.indexOf('=');
      if (idx <= 0) continue;
      const k = pair.slice(0, idx).trim();
      const v = pair.slice(idx + 1).trim();
      if (k) out[k] = v;
    }
    return Object.keys(out).length > 0 ? out : undefined;
  };

  const formatReasoningMapInput = (m?: Record<string, string>): string => {
    if (!m) return '';
    return Object.entries(m)
      .map(([k, v]) => `${k}=${v}`)
      .join(', ');
  };

  const openAddModelDialog = () => {
    setEditingModel(null);
    setLookedUpMeta(null);
    setNewModelId('');
    setNewModelName('');
    setNewCtxWin(128_000);
    setNewMaxOut(16_384);
    setNewSupportsVision(true);
    setNewSupportsPDF(false);
    setNewSupportsTools(true);
    setNewSupportsReasoning(true);
    setNewSupportsCaching(true);
    setNewReasoningLevels('');
    setNewReasoningMap('');
    setShowAddModel(true);
  };

  const openEditModelDialog = (m: ModelMeta) => {
    setEditingModel(m);
    setLookedUpMeta(null);
    setNewModelId(m.id);
    setNewModelName(m.display_name || '');
    setNewCtxWin(m.context_window || 128_000);
    setNewMaxOut(m.max_output_tokens || 16_384);
    setNewSupportsVision(Boolean(m.supports_vision));
    setNewSupportsPDF(Boolean(m.supports_pdf));
    setNewSupportsTools(m.supports_tools ?? true);
    setNewSupportsReasoning(Boolean(m.supports_reasoning));
    setNewSupportsCaching(Boolean(m.supports_caching));
    setNewReasoningLevels((m.reasoning_levels || []).join(', '));
    setNewReasoningMap(formatReasoningMapInput(m.reasoning_map));
    setShowAddModel(true);
  };

  const addCustomModel = async () => {
    if (!draft || !newModelId.trim()) return;
    if (!draft.id) {
      onFlash(formatMsg(locale, 'settings.saveTheProviderBeforeAdding'));
      return;
    }
    const modelId = newModelId.trim();
    const parsedLevels = parseReasoningLevelsInput(newReasoningLevels);
    const parsedMap = parseReasoningMapInput(newReasoningMap);
    const baseMeta = lookedUpMeta || editingModel;
    await apiFetch('/api/v1/models', {
      method: 'POST',
      body: JSON.stringify({
        ...(baseMeta || {}),
        id: modelId,
        provider_id: draft.id,
        display_name: newModelName.trim() || modelId,
        source: 'custom',
        context_window: newCtxWin,
        max_output_tokens: newMaxOut,
        supports_vision: newSupportsVision,
        supports_pdf: newSupportsPDF,
        supports_tools: newSupportsTools,
        supports_reasoning: newSupportsReasoning || Boolean(parsedLevels && parsedLevels.length >= 2),
        supports_caching: newSupportsCaching,
        reasoning_levels: parsedLevels,
        reasoning_map: parsedMap,
      }),
    }).catch(() => {});
    setEditingModel(null);
    setLookedUpMeta(null);
    setNewModelId('');
    setNewModelName('');
    setNewReasoningLevels('');
    setNewReasoningMap('');
    setShowAddModel(false);
    await refreshInitialData();
    onFlash(
      editingModel
        ? formatMsg(locale, 'settings.modelUpdated', { modelId })
        : formatMsg(locale, 'settings.modelAddedToProvider', { modelId, providerId: draft.id }),
    );
  };

  const removeModel = async (m: ModelMeta) => {
    setHiddenModelIds((prev) => ({ ...prev, [m.id]: true }));
    await apiFetch(`/api/v1/models/${encodeURIComponent(m.id)}?provider_id=${encodeURIComponent(m.provider_id)}`, {
      method: 'DELETE',
    }).catch(() => {});
    await refreshInitialData();
    onFlash(formatMsg(locale, 'settings.deleted', { id: m.id }));
  };

  const providerModels = (() => {
    if (!draft) return [];
    const base = models.filter((m) => m.provider_id === draft.id && !hiddenModelIds[m.id]);
    const customOrder = modelOrder[draft.id];
    if (!customOrder || customOrder.length === 0) return base;
    const rank = new Map(customOrder.map((id, i) => [id, i]));
    return [...base].sort((a, b) => {
      const ra = rank.has(a.id) ? rank.get(a.id)! : Number.MAX_SAFE_INTEGER;
      const rb = rank.has(b.id) ? rank.get(b.id)! : Number.MAX_SAFE_INTEGER;
      return ra - rb;
    });
  })();
  const isSavedDraft = Boolean(draft?.id && providers.some((p) => p.id === draft.id));

  return (
    <>
      <div className="mt-2 flex flex-wrap items-center justify-between gap-4 text-ui-base text-foreground-subtle">
        <p className="min-w-0 flex-1">{description}</p>
        {providersTab === 'chat' ? (
          <div className="flex shrink-0 items-center gap-2">
            <button
              type="button"
              onClick={startNewProvider}
              className="flex h-8 items-center gap-1 rounded-lg bg-primary px-3.5 text-ui-sm font-medium text-primary-foreground transition-opacity hover:opacity-90"
            >
              <Plus className="size-3.5" />
              <span>{formatMsg(locale, 'settings.addProvider')}</span>
            </button>
          </div>
        ) : null}
      </div>

      {/* Top segmented tabs so Image Generation & Classifier (Jev) are always immediately visible */}
      <div
        role="tablist"
        aria-label={formatMsg(locale, 'settings.providersTabChat')}
        className="mt-4 inline-flex flex-wrap items-center gap-1 rounded-xl border border-border bg-surface p-1"
      >
        {(
          [
            { id: 'chat', labelKey: 'settings.providersTabChat' },
            { id: 'image', labelKey: 'settings.providersTabImage' },
            { id: 'classifier', labelKey: 'settings.providersTabClassifier' },
          ] as const
        ).map((tab) => {
          const active = providersTab === tab.id;
          return (
            <button
              key={tab.id}
              type="button"
              role="tab"
              aria-selected={active}
              onClick={() => setProvidersTab(tab.id)}
              className={`h-8 rounded-lg px-3.5 text-ui-sm font-medium transition-colors ${
                active
                  ? 'bg-card text-foreground shadow-xs'
                  : 'text-foreground-subtle hover:text-foreground'
              }`}
            >
              {formatMsg(locale, tab.labelKey)}
            </button>
          );
        })}
      </div>

      {providersTab === 'image' ? (
        <ImageGenerationSettingsCard onFlash={onFlash} />
      ) : providersTab === 'classifier' ? (
        <ClassifierSettingsCard onFlash={onFlash} />
      ) : (
      <div className="mt-5 grid grid-cols-1 overflow-hidden rounded-2xl border border-border bg-card md:grid-cols-[220px_minmax(0,1fr)]">
        {/* Left: custom providers */}
        <div className="space-y-1 border-b border-border p-3 md:border-r md:border-b-0">
          <div className="px-2.5 py-1 text-ui-xs font-medium text-foreground-subtlest">
            {formatMsg(locale, 'settings.customProviders')}
          </div>
          {providers.length === 0 ? (
            <p className="px-2.5 py-2 text-ui-sm text-foreground-subtlest">
              {formatMsg(locale, 'settings.noProvidersYet')}
            </p>
          ) : (
            providers.map((p) => {
              const active = draft?.id === p.id;
              return (
                <button
                  key={p.id}
                  type="button"
                  onClick={() => {
                    setDraft(toDraft(p));
                    setShowKey(false);
                    setShowAdvanced(false);
                  }}
                  className={`flex h-9 w-full items-center justify-between gap-2 rounded-lg px-2.5 text-ui-base transition-colors ${
                    active
                      ? 'bg-selected font-medium text-foreground'
                      : 'text-foreground-subtle hover:bg-hover hover:text-foreground'
                  }`}
                >
                  <span className="flex min-w-0 items-center gap-2">
                    <Package className="size-4 shrink-0" />
                    <span className="truncate">{p.name}</span>
                  </span>
                  {p.enabled ? (
                    <span className="size-2 shrink-0 rounded-full bg-success" />
                  ) : null}
                </button>
              );
            })
          )}
        </div>

        {/* Right: provider detail */}
        <div className="min-w-0 p-5">
          {!draft ? (
            <div className="flex min-h-[280px] flex-col items-center justify-center gap-3 text-center">
              <Package className="size-6 text-foreground-subtlest" />
              <p className="text-ui-sm text-foreground-subtle">
                {formatMsg(locale, 'settings.selectAProviderOnThe')}
              </p>
              <button
                type="button"
                onClick={startNewProvider}
                className="flex h-8 items-center gap-1 rounded-lg bg-primary px-3.5 text-ui-sm font-medium text-primary-foreground hover:opacity-90"
              >
                <Plus className="size-3.5" />
                <span>{formatMsg(locale, 'settings.addProvider')}</span>
              </button>
            </div>
          ) : (
            <div className="space-y-4">
              <div className="flex items-center justify-between gap-3">
                <div className="group flex min-w-0 flex-1 items-center gap-2">
                  <Package className="size-[18px] shrink-0 text-foreground" />
                  <input
                    value={draft.name}
                    onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                    onBlur={() => void persistDraft()}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') {
                        (e.target as HTMLInputElement).blur();
                      }
                    }}
                    placeholder={formatMsg(locale, 'settings.providerNameClickToRename')}
                    aria-label={formatMsg(locale, 'settings.providerName')}
                    className="h-8 min-w-0 flex-1 rounded-lg border border-border/70 bg-input/60 px-2.5 text-ui-lg font-semibold text-foreground transition-colors hover:border-border-hover hover:bg-input focus:border-primary focus:bg-input focus:outline-none"
                  />
                  <span className="shrink-0 text-ui-xs text-foreground-subtlest opacity-0 transition-opacity group-hover:opacity-100">
                    {formatMsg(locale, 'settings.rename')}
                  </span>
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  <Toggle
                    checked={draft.enabled}
                    onChange={(v) => {
                      setDraft({ ...draft, enabled: v });
                      void persistDraft({ enabled: v });
                    }}
                    label={formatMsg(locale, 'settings.providerEnabled')}
                  />
                  {isSavedDraft ? (
                    <button
                      type="button"
                      onClick={() => void removeProvider(draft.id)}
                      aria-label={formatMsg(locale, 'settings.deleteProvider')}
                      title={formatMsg(locale, 'settings.deleteProvider')}
                      className="ml-1 flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-destructive-surface hover:text-destructive-surface-fg"
                    >
                      <Trash2 className="size-4" />
                    </button>
                  ) : null}
                </div>
              </div>

              <div className="space-y-3.5">
                <label className="block space-y-1.5">
                  <span className="text-ui-xs text-foreground-subtle">Base URL</span>
                  <input
                    value={draft.baseUrl}
                    onChange={(e) => setDraft({ ...draft, baseUrl: e.target.value })}
                    onBlur={() => void persistDraft()}
                    placeholder="https://api.openai.com/v1"
                    className={settingsMonoInputClass}
                  />
                </label>

                <label className="block space-y-1.5">
                  <span className="text-ui-xs text-foreground-subtle">
                    {formatMsg(locale, 'settings.apiFormat')}
                  </span>
                  <select
                    value={draft.driver}
                    onChange={(e) => {
                      const driver = e.target.value as ProviderRecord['driver'];
                      setDraft({ ...draft, driver });
                      void persistDraft({ driver });
                    }}
                    className={settingsSelectClass}
                  >
                    <option value="openai_responses">OpenAI Responses (/responses)</option>
                    <option value="openai_chat">OpenAI Chat Completions (/chat/completions)</option>
                    <option value="anthropic">Anthropic Messages (/v1/messages)</option>
                  </select>
                </label>

                <label className="block space-y-1.5">
                  <span className="text-ui-xs text-foreground-subtle">API key</span>
                  <span className="relative block">
                    <input
                      type={showKey ? 'text' : 'password'}
                      value={draft.apiKey}
                      onChange={(e) => setDraft({ ...draft, apiKey: e.target.value })}
                      onBlur={() => void persistDraft()}
                      placeholder="sk-..."
                      autoComplete="off"
                      className={`${settingsMonoInputClass} pr-10`}
                    />
                    <button
                      type="button"
                      onClick={() => setShowKey((v) => !v)}
                      aria-label={showKey ? (formatMsg(locale, 'settings.hideApiKey')) : formatMsg(locale, 'settings.showApiKey')}
                      className="absolute top-1/2 right-1 flex size-7 -translate-y-1/2 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
                    >
                      {showKey ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                    </button>
                  </span>
                </label>

                {showAdvanced ? (
                  <label className="block space-y-1.5">
                    <span className="text-ui-xs text-foreground-subtle">
                      {formatMsg(locale, 'settings.credentialRouting')}
                    </span>
                    <select
                      value={draft.proxyMode}
                      onChange={(e) => {
                        const proxyMode = e.target.value as ProviderRecord['proxy_mode'];
                        setDraft({ ...draft, proxyMode });
                        void persistDraft({ proxyMode });
                      }}
                      className={settingsSelectClass}
                    >
                      <option value="direct">{formatMsg(locale, 'settings.direct')}</option>
                      <option value="server_proxy">
                        {formatMsg(locale, 'settings.serverProxy')}
                      </option>
                    </select>
                  </label>
                ) : null}

                <button
                  type="button"
                  onClick={() => setShowAdvanced((v) => !v)}
                  className="text-ui-xs text-foreground-subtlest transition-colors hover:text-foreground"
                >
                  {showAdvanced
                    ? formatMsg(locale, 'settings.hideAdvanced')
                    : formatMsg(locale, 'settings.advanced')}
                </button>
              </div>

              {/* Model list */}
              <div className="space-y-2 pt-2">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="text-ui-sm font-medium text-foreground">
                    {formatMsg(locale, 'settings.modelList')}
                  </span>
                  <div className="flex items-center gap-1.5">
                    <button
                      type="button"
                      onClick={() => void fetchFromV1Models()}
                      className="flex h-7 items-center gap-1 rounded-lg border border-border bg-card px-2.5 text-ui-xs text-foreground transition-colors hover:bg-hover"
                    >
                      <Globe className="size-3" />
                      <span>{formatMsg(locale, 'settings.fetchFromV1Models')}</span>
                    </button>
                    <button
                      type="button"
                      onClick={openAddModelDialog}
                      className="flex h-7 items-center gap-1 rounded-lg border border-border bg-card px-2.5 text-ui-xs text-foreground transition-colors hover:bg-hover"
                    >
                      <Plus className="size-3" />
                      <span>{formatMsg(locale, 'settings.addModelManually')}</span>
                    </button>
                  </div>
                </div>

                {fetchRemoteOpen && (
                  <div className="nex-dialog-overlay fixed inset-0 z-50 flex items-center justify-center bg-black/45 p-4 backdrop-blur-[2px]">
                    <div className="nex-dialog-content flex max-h-[82vh] w-full max-w-lg flex-col rounded-2xl border border-border bg-card p-5 shadow-xl">
                      <div className="flex items-start justify-between gap-2">
                        <div>
                          <h3 className="text-ui-lg font-semibold text-foreground">
                            {formatMsg(locale, 'settings.selectModelsFromV1Models')}
                          </h3>
                          <p className="mt-1 text-ui-xs text-foreground-subtle">
                            {formatMsg(locale, 'settings.readsTheModelListUsing')}
                          </p>
                        </div>
                        <button
                          type="button"
                          onClick={() => setFetchRemoteOpen(false)}
                          className="rounded-md px-2 py-1 text-ui-sm text-foreground-subtle hover:bg-hover"
                        >
                          ✕
                        </button>
                      </div>

                      <div className="mt-3 flex items-center gap-2 rounded-lg border border-border bg-input px-3">
                        <Search className="size-3.5 text-foreground-subtlest" />
                        <input
                          value={remoteFilter}
                          onChange={(e) => setRemoteFilter(e.target.value)}
                          placeholder={formatMsg(locale, 'settings.filterByName')}
                          className="h-8 w-full bg-transparent text-ui-sm text-foreground outline-none placeholder-foreground-subtlest"
                        />
                      </div>

                      <div className="mt-2.5 flex items-center justify-between text-ui-xs">
                        <div className="flex items-center gap-2">
                          <button
                            type="button"
                            onClick={() => {
                              const next: Record<string, boolean> = {};
                              for (const id of remoteModels.filter((m) =>
                                m.toLowerCase().includes(remoteFilter.toLowerCase()),
                              )) {
                                next[id] = true;
                              }
                              setRemoteSelected(next);
                            }}
                            className="rounded-md bg-secondary px-2 py-1 text-foreground hover:bg-hover"
                          >
                            {formatMsg(locale, 'settings.selectAllListed')}
                          </button>
                          <button
                            type="button"
                            onClick={() => setRemoteSelected({})}
                            className="px-2 py-1 text-foreground-subtle hover:text-foreground"
                          >
                            {formatMsg(locale, 'settings.clearSelection')}
                          </button>
                        </div>
                        <span className="text-foreground-subtle">
                          {Object.values(remoteSelected).filter(Boolean).length} {formatMsg(locale, 'settings.selected')}
                        </span>
                      </div>

                      <div className="mt-2.5 flex-1 space-y-1.5 overflow-y-auto pr-1">
                        {fetchingRemote ? (
                          <div className="py-8 text-center text-ui-sm text-foreground-subtle">
                            {formatMsg(locale, 'common.loading')}
                          </div>
                        ) : (
                          remoteModels
                            .filter((m) => m.toLowerCase().includes(remoteFilter.toLowerCase()))
                            .map((id) => (
                              <label
                                key={id}
                                className="flex cursor-pointer items-center gap-2.5 rounded-lg border border-border px-3 py-2 hover:bg-hover"
                              >
                                <input
                                  type="checkbox"
                                  checked={Boolean(remoteSelected[id])}
                                  onChange={(e) =>
                                    setRemoteSelected((prev) => ({ ...prev, [id]: e.target.checked }))
                                  }
                                />
                                <span className="truncate font-mono text-ui-sm text-foreground">{id}</span>
                              </label>
                            ))
                        )}
                      </div>

                      <div className="mt-4 flex items-center justify-end gap-2">
                        <button
                          type="button"
                          onClick={() => setFetchRemoteOpen(false)}
                          className="h-8 rounded-lg px-3 text-ui-sm text-foreground-subtle hover:bg-hover"
                        >
                          {formatMsg(locale, 'common.cancel')}
                        </button>
                        <button
                          type="button"
                          disabled={addingRemote || Object.values(remoteSelected).filter(Boolean).length === 0}
                          onClick={() => void addSelectedRemoteModels()}
                          className="h-8 rounded-lg bg-primary px-3 text-ui-sm font-medium text-primary-foreground hover:opacity-90 disabled:opacity-50"
                        >
                          {addingRemote
                            ? formatMsg(locale, 'settings.adding')
                            : formatMsg(locale, 'settings.addSelectedModels')}
                        </button>
                      </div>
                    </div>
                  </div>
                )}

                {showAddModel && (
                  <div className="nex-dialog-overlay fixed inset-0 z-50 flex items-center justify-center bg-black/45 p-4 backdrop-blur-[2px]">
                    <div className="nex-dialog-content w-full max-w-md rounded-2xl border border-border bg-card p-5 shadow-xl">
                      <div className="flex items-center justify-between">
                        <h3 data-testid="model-modal-title" className="text-ui-lg font-semibold text-foreground">
                          {editingModel
                            ? formatMsg(locale, 'settings.editModel')
                            : formatMsg(locale, 'settings.addModelManually')}
                        </h3>
                        <button
                          type="button"
                          onClick={() => setShowAddModel(false)}
                          className="rounded-md px-2 py-1 text-ui-sm text-foreground-subtle hover:bg-hover"
                        >
                          ✕
                        </button>
                      </div>

                      <div className="mt-4 space-y-3">
                        <label className="block space-y-1">
                          <span className="text-ui-xs text-foreground-subtle">Model ID</span>
                          <input
                            value={newModelId}
                            disabled={Boolean(editingModel)}
                            onChange={(e) => setNewModelId(e.target.value)}
                            placeholder="e.g. cli/glm-5.3"
                            className={`${settingsMonoInputClass} disabled:opacity-60`}
                          />
                        </label>
                        <label className="block space-y-1">
                          <span className="text-ui-xs text-foreground-subtle">
                            {formatMsg(locale, 'settings.modelName')}
                          </span>
                          <input
                            value={newModelName}
                            onChange={(e) => setNewModelName(e.target.value)}
                            placeholder={formatMsg(locale, 'settings.optionalShortName')}
                            className={settingsInputClass}
                          />
                        </label>
                        <div className="grid grid-cols-2 gap-3">
                          <label className="block space-y-1">
                            <span className="text-ui-xs text-foreground-subtle">
                              {formatMsg(locale, 'settings.contextWindow')}
                            </span>
                            <input
                              type="number"
                              value={newCtxWin}
                              onChange={(e) => setNewCtxWin(Number(e.target.value))}
                              className={settingsMonoInputClass}
                            />
                          </label>
                          <label className="block space-y-1">
                            <span className="text-ui-xs text-foreground-subtle">
                              {formatMsg(locale, 'settings.maxOutputTokens')}
                            </span>
                            <input
                              type="number"
                              value={newMaxOut}
                              onChange={(e) => setNewMaxOut(Number(e.target.value))}
                              className={settingsMonoInputClass}
                            />
                          </label>
                        </div>
                        <div className="flex flex-wrap items-center gap-x-4 gap-y-2 pt-1 text-ui-xs text-foreground">
                          <label className="flex items-center gap-1.5">
                            <input
                              type="checkbox"
                              checked={newSupportsVision}
                              onChange={(e) => setNewSupportsVision(e.target.checked)}
                            />
                            <span>Vision</span>
                          </label>
                          <label className="flex items-center gap-1.5">
                            <input
                              type="checkbox"
                              checked={newSupportsPDF}
                              onChange={(e) => setNewSupportsPDF(e.target.checked)}
                            />
                            <span>PDF</span>
                          </label>
                          <label className="flex items-center gap-1.5">
                            <input
                              type="checkbox"
                              checked={newSupportsTools}
                              onChange={(e) => setNewSupportsTools(e.target.checked)}
                            />
                            <span>Tools</span>
                          </label>
                          <label className="flex items-center gap-1.5">
                            <input
                              type="checkbox"
                              checked={newSupportsReasoning}
                              onChange={(e) => setNewSupportsReasoning(e.target.checked)}
                            />
                            <span>Reasoning</span>
                          </label>
                          <label className="flex items-center gap-1.5">
                            <input
                              type="checkbox"
                              checked={newSupportsCaching}
                              onChange={(e) => setNewSupportsCaching(e.target.checked)}
                            />
                            <span>Prompt Cache</span>
                          </label>
                        </div>
                        {newSupportsReasoning && (
                          <div className="space-y-2.5 rounded-xl border border-border/70 bg-surface/50 p-3">
                            <label className="block space-y-1">
                              <span className="text-ui-xs text-foreground-subtle">
                                {formatMsg(locale, 'settings.reasoningLevelsLowHighComma')}
                              </span>
                              <input
                                value={newReasoningLevels}
                                onChange={(e) => setNewReasoningLevels(e.target.value)}
                                placeholder="off, low, medium, high, xhigh  (or off, on)"
                                className={settingsMonoInputClass}
                              />
                            </label>
                            <label className="block space-y-1">
                              <span className="text-ui-xs text-foreground-subtle">
                                {formatMsg(locale, 'settings.wireEffortMappingOptionalE')}
                              </span>
                              <input
                                value={newReasoningMap}
                                onChange={(e) => setNewReasoningMap(e.target.value)}
                                placeholder="max=xhigh, off=none"
                                className={settingsMonoInputClass}
                              />
                            </label>
                          </div>
                        )}
                      </div>

                      <div className="mt-5 flex items-center justify-between">
                        <button
                          type="button"
                          disabled={fetchingInfo || !newModelId.trim()}
                          onClick={() => void lookupModelInfo()}
                          className="text-ui-xs text-foreground-subtle underline hover:text-foreground disabled:opacity-40"
                        >
                          {fetchingInfo
                            ? formatMsg(locale, 'settings.fetching')
                            : formatMsg(locale, 'settings.fetchModelInfo')}
                        </button>
                        <div className="flex items-center gap-2">
                          <button
                            type="button"
                            onClick={() => setShowAddModel(false)}
                            className="h-8 rounded-lg px-3 text-ui-sm text-foreground-subtle hover:bg-hover"
                          >
                            {formatMsg(locale, 'common.cancel')}
                          </button>
                          <button
                            type="button"
                            onClick={() => void addCustomModel()}
                            className="h-8 rounded-lg bg-primary px-3 text-ui-sm font-medium text-primary-foreground hover:opacity-90"
                          >
                            {formatMsg(locale, 'common.save')}
                          </button>
                        </div>
                      </div>
                    </div>
                  </div>
                )}

                {providerModels.length === 0 ? (
                  <p className="py-6 text-center text-ui-sm text-foreground-subtlest">
                    {formatMsg(locale, 'settings.noModelsRegisteredForThis')}
                  </p>
                ) : (
                  <div className="divide-y divide-border">
                    {providerModels.map((m) => {
                      const visible = !hiddenModelKeys[`${m.provider_id}::${m.id}`];
                      const testRes = testResults[m.id];
                      return (
                        <div
                          key={m.id}
                          draggable
                          onDragStart={() => setDraggingModelId(m.id)}
                          onDragOver={(e) => {
                            if (draggingModelId && draggingModelId !== m.id) e.preventDefault();
                          }}
                          onDrop={(e) => {
                            e.preventDefault();
                            if (!draft || !draggingModelId || draggingModelId === m.id) return;
                            const ids = providerModels.map((x) => x.id);
                            const from = ids.indexOf(draggingModelId);
                            const to = ids.indexOf(m.id);
                            if (from >= 0 && to >= 0) {
                              const next = [...ids];
                              const [moved] = next.splice(from, 1);
                              next.splice(to, 0, moved);
                              reorderProviderModels(draft.id, next);
                            }
                            setDraggingModelId(null);
                          }}
                          onDragEnd={() => setDraggingModelId(null)}
                          className={`flex h-11 items-center justify-between gap-2 transition-opacity ${
                            draggingModelId === m.id ? 'opacity-45' : ''
                          }`}
                          title={
                            formatMsg(locale, 'settings.in1mOut1m', { id: m.id, input_per_1m: m.pricing.input_per_1m, output_per_1m: m.pricing.output_per_1m })
                          }
                        >
                          <div className="flex min-w-0 items-center gap-1.5">
                            <GripVertical className="size-3.5 shrink-0 cursor-grab text-foreground-subtlest" />
                            <span className="truncate font-mono text-ui-base text-foreground">
                              {m.display_name && m.display_name !== m.id ? m.display_name : m.id}
                            </span>
                            <ModelBadge>{contextLabel(m.context_window)}</ModelBadge>
                            {m.supports_vision ? <ModelBadge>Vision</ModelBadge> : null}
                            {m.supports_reasoning ? <ModelBadge>Reasoning</ModelBadge> : null}
                          </div>
                          <div className="flex shrink-0 items-center gap-0.5">
                            {testRes && (
                              <span
                                className={`mr-1 text-ui-xs ${
                                  testRes.ok ? 'text-success' : 'text-destructive-surface-fg'
                                }`}
                                title={testRes.msg}
                              >
                                {testRes.ok ? testRes.msg : 'ERR'}
                              </span>
                            )}
                            <button
                              type="button"
                              data-testid={`edit-model-${m.id}`}
                              onClick={() => openEditModelDialog(m)}
                              title={formatMsg(locale, 'settings.editModel')}
                              className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
                            >
                              <SlidersHorizontal className="size-3.5" />
                            </button>
                            <button
                              type="button"
                              disabled={testingModelId === m.id}
                              onClick={() => void testModelConnection(m)}
                              title={formatMsg(locale, 'settings.testModelConnection')}
                              aria-label={formatMsg(locale, 'settings.testModelConnection')}
                              className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground disabled:opacity-40"
                            >
                              <Cable className={`size-3.5 ${testingModelId === m.id ? 'animate-pulse text-primary' : ''}`} />
                            </button>
                            <button
                              type="button"
                              onClick={() => {
                                void navigator.clipboard?.writeText(m.id);
                                onFlash(formatMsg(locale, 'settings.copied', { id: m.id }));
                              }}
                              aria-label={formatMsg(locale, 'settings.copyModelId')}
                              className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
                            >
                              <Copy className="size-3.5" />
                            </button>
                            <button
                              type="button"
                              onClick={() => void removeModel(m)}
                              aria-label={formatMsg(locale, 'settings.deleteModel')}
                              className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-destructive-surface hover:text-destructive-surface-fg"
                            >
                              <Trash2 className="size-3.5" />
                            </button>
                            <Toggle
                              checked={visible}
                              onChange={() => toggleModelVisibility(m.provider_id, m.id)}
                              label={formatMsg(locale, 'settings.showInModelPicker')}
                            />
                          </div>
                        </div>
                      );
                    })}
                  </div>
                )}
              </div>
            </div>
          )}
        </div>
      </div>
      )}
    </>
  );
}
