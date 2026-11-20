import {
  AlarmClock,
  Archive,
  ArrowLeft,
  BarChart3,
  Blocks,
  Bot,
  Cable,
  Check,
  Copy,
  Eye,
  EyeOff,
  Globe,
  GripVertical,
  HardDrive,
  Monitor,
  Moon,
  Package,
  Palette,
  Pencil,
  Plus,
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
import { formatMsg, type MessageKey } from '../i18n/messages';
import { HighlightedCode } from './MarkdownRenderer';
import { ArchivesSettingsSection, UsageAnalyticsSection } from './SettingsAnalyticsArchive';
import {
  AgentsSettingsSection,
  AutomationsSection,
  MCPSettingsSection,
  SkillsAndPluginsSection,
} from './SettingsCapabilities';

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
};

export function Toggle({ checked, onChange, label, disabled }: ToggleProps) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
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

  { id: 'agents', icon: Bot, titleKey: 'settings.subagents.title', descKey: 'settings.subagents.desc', groupKey: 'settings.group.agentCapabilities' },
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
      {/* Left settings sidebar */}
      <nav className="flex w-[248px] shrink-0 flex-col border-r border-border bg-sidebar px-3 py-3.5 select-none">
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
            placeholder={locale === 'en-US' ? 'Search settings' : '搜索设置'}
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
                      onClick={() => openSettings(item.id)}
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
      <div className="min-h-0 min-w-0 flex-1 overflow-y-auto bg-background px-8 py-10">
        <div className="mx-auto w-full max-w-4xl">
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
            <div className="zcode-toast-in fixed right-6 top-6 z-50 rounded-xl border border-border bg-card px-4 py-2.5 text-ui-sm font-medium text-foreground shadow-lg">
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
          {activeMeta.id === 'runners' && <RunnersSection />}
          {activeMeta.id === 'agents' && <AgentsSettingsSection onFlash={showFlash} />}
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
  { id: 'system' as const, icon: Monitor, zh: '跟随系统', en: 'System' },
  { id: 'light' as const, icon: Sun, zh: '浅色', en: 'Light' },
  { id: 'dark' as const, icon: Moon, zh: '深色', en: 'Dark' },
];

function GeneralSettingsSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const {
    locale,
    setLocale,
    permissionMode,
    setPermissionMode,
    preferences,
    refreshInitialData,
    updateThemeQuick,
  } = useNexStore();
  const isEn = locale === 'en-US';

  const [autoArchiveEnabled, setAutoArchiveEnabled] = useState(
    preferences.auto_archive_after_days > 0,
  );
  const [archiveDays, setArchiveDays] = useState(
    preferences.auto_archive_after_days > 0 ? preferences.auto_archive_after_days : 30,
  );

  const savePreferences = async (patch: Partial<UserPreferences>) => {
    await apiFetch('/api/v1/preferences', {
      method: 'PUT',
      body: JSON.stringify({ ...preferences, ...patch }),
    }).catch(() => {});
    await refreshInitialData();
  };

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
      <SettingsGroupCard>
        <SettingsRow
          label={isEn ? 'Language' : '界面语言'}
          description={
            isEn ? 'Display language for the app interface.' : '应用界面所使用的显示语言。'
          }
        >
          <select
            value={locale}
            onChange={(e) => setLocale(e.target.value as 'zh-CN' | 'en-US')}
            className={settingsSelectClass}
          >
            <option value="zh-CN">中文简体</option>
            <option value="en-US">English</option>
          </select>
        </SettingsRow>

        <SettingsRow
          label={isEn ? 'App theme' : '界面主题'}
          description={
            isEn ? 'Use light, dark, or match the system setting.' : '使用浅色、深色，或跟随系统设置。'
          }
        >
          <div className="inline-flex w-full items-center gap-0.5 rounded-lg border border-border bg-surface p-0.5">
            {THEME_OPTIONS.map(({ id, icon: Icon, zh, en }) => {
              const active = preferences.theme === id;
              return (
                <button
                  key={id}
                  type="button"
                  onClick={() => {
                    void updateThemeQuick(id);
                    onFlash(isEn ? 'Theme updated' : '界面主题已切换');
                  }}
                  className={`flex h-8 flex-1 items-center justify-center gap-1.5 rounded-md text-ui-sm transition-colors ${
                    active
                      ? 'bg-card font-medium text-foreground shadow-2xs'
                      : 'text-foreground-subtle hover:text-foreground'
                  }`}
                >
                  <Icon className="size-3.5 shrink-0" />
                  <span className="truncate">{isEn ? en : zh}</span>
                </button>
              );
            })}
          </div>
        </SettingsRow>

        <SettingsRow
          label={isEn ? 'Default permission mode' : '默认权限模式'}
          description={
            isEn
              ? 'Applied to new sessions until you change it in the composer.'
              : '应用于新会话，可在对话输入框中随时切换。'
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
      </SettingsGroupCard>

      <SectionHeading
        title={isEn ? 'Session storage' : '会话存储'}
        description={
          isEn
            ? 'Keep the active workspace lean by moving idle sessions to cold storage.'
            : '将长期闲置的会话迁出热库，保持活跃工作区轻量高效。'
        }
      />

      <SettingsGroupCard>
        <SettingsRow
          label={isEn ? 'Auto-archive old sessions' : '自动归档旧会话'}
          description={
            isEn
              ? 'Idle sessions move to archive.db automatically.'
              : '闲置会话将自动迁移至冷库 archive.db，不影响历史统计。'
          }
        >
          <Toggle
            checked={autoArchiveEnabled}
            onChange={toggleAutoArchive}
            label={isEn ? 'Auto-archive old sessions' : '自动归档旧会话'}
          />
        </SettingsRow>

        {autoArchiveEnabled ? (
          <SettingsRow
            label={isEn ? 'Archive after' : '归档保留时长'}
            description={
              isEn
                ? 'Sessions older than this threshold become archive candidates.'
                : '会话最后更新时间早于该时长后，才会进入自动归档候选。'
            }
          >
            <select
              value={archiveDays}
              onChange={(e) => changeArchiveDays(Number(e.target.value))}
              className={settingsSelectClass}
            >
              {[3, 7, 14, 30].map((d) => (
                <option key={d} value={d}>
                  {isEn ? `${d} days` : `${d} 天后归档`}
                </option>
              ))}
            </select>
          </SettingsRow>
        ) : null}
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
    zh: '跟随系统',
    en: 'System',
    preview: 'bg-gradient-to-r from-[#f8f8f8] to-[#161616]',
    side: 'bg-black/10',
    line: 'bg-black/25',
    lineSoft: 'bg-black/10',
  },
  {
    id: 'light' as const,
    icon: Sun,
    zh: '浅色',
    en: 'Light',
    preview: 'bg-[#f8f8f8]',
    side: 'bg-[#ececee]',
    line: 'bg-black/25',
    lineSoft: 'bg-black/10',
  },
  {
    id: 'dark' as const,
    icon: Moon,
    zh: '深色',
    en: 'Dark',
    preview: 'bg-[#161616]',
    side: 'bg-[#202020]',
    line: 'bg-white/30',
    lineSoft: 'bg-white/12',
  },
];

function AppearanceSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const { locale, preferences, refreshInitialData, updateThemeQuick } = useNexStore();
  const isEn = locale === 'en-US';

  const [uiFontPx, setUiFontPx] = useState(() => hostBridge.getDeviceFontScale());
  const [sansFont, setSansFont] = useState(preferences.font_family);
  const [monoFont, setMonoFont] = useState(preferences.mono_font_family);
  const [expandThinking, setExpandThinking] = useState(preferences.expand_thinking);
  const [saving, setSaving] = useState(false);

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
        }),
      }).catch(() => {});
      await refreshInitialData();
      onFlash(isEn ? 'Appearance settings saved' : '界面设置已保存并生效');
    } finally {
      setSaving(false);
    }
  };

  const chooseTheme = async (theme: UserPreferences['theme']) => {
    await updateThemeQuick(theme);
    onFlash(isEn ? 'Theme updated' : '界面主题已切换');
  };

  return (
    <div className="space-y-6">
      <SettingsGroupCard>
        <div className="space-y-3 px-5 py-4">
          <div>
            <div className="text-ui-base font-medium text-foreground">
              {isEn ? 'Theme' : '界面主题'}
            </div>
            <p className="mt-1 text-ui-sm leading-relaxed text-foreground-subtle">
              {isEn ? 'Use light, dark, or match the system setting.' : '使用浅色、深色，或跟随系统设置。'}
            </p>
          </div>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            {THEME_CARDS.map(({ id, icon: Icon, zh, en, preview, side, line, lineSoft }) => {
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
                        {isEn ? en : zh}
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
          label={isEn ? 'Interface font size' : '界面字号'}
          description={
            isEn
              ? 'Adjust interface text between 12px and 18px. Icons and layout stay intact.'
              : '在 12px ~ 18px 间调整界面文字大小，图标与布局尺寸不受影响。'
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
          label={isEn ? 'Expand thinking by default' : '默认展开思考过程'}
          description={
            isEn
              ? 'Reasoning blocks are expanded when they arrive instead of collapsed.'
              : '对话流收到推理块时默认展开而非折叠。'
          }
        >
          <Toggle
            checked={expandThinking}
            onChange={setExpandThinking}
            label={isEn ? 'Expand thinking by default' : '默认展开思考过程'}
          />
        </SettingsRow>
      </SettingsGroupCard>

      <SectionHeading
        title={isEn ? 'Code' : '代码设置'}
        description={
          isEn
            ? 'Configure code typography. These settings do not depend on interface size.'
            : '设置代码内容的字体与预览效果，不受界面字号影响。'
        }
      />

      <SettingsGroupCard>
        <SettingsRow
          label={isEn ? 'Interface font (Sans)' : '界面字体 (Sans)'}
          description={isEn ? 'Sans-serif stack used across the interface.' : '界面全局使用的无衬线字体栈。'}
        >
          <input
            value={sansFont}
            onChange={(e) => setSansFont(e.target.value)}
            placeholder="Inter, system-ui, sans-serif"
            className={settingsInputClass}
          />
        </SettingsRow>

        <SettingsRow
          label={isEn ? 'Code & terminal font (Mono)' : '代码与终端字体 (Mono)'}
          description={
            isEn ? 'Monospace stack used by code blocks and terminals.' : '代码块与终端使用的等宽字体栈。'
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
            {isEn ? 'Live preview' : '实时预览'}
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
            {isEn ? 'Save changes' : '保存设置'}
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
  const isEn = locale === 'en-US';

  const [draft, setDraft] = useState<ProviderDraft | null>(null);
  const [showKey, setShowKey] = useState(false);
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [showAddModel, setShowAddModel] = useState(false);
  const [newModelId, setNewModelId] = useState('');
  const [newModelName, setNewModelName] = useState('');
  const [newCtxWin, setNewCtxWin] = useState(128_000);
  const [newMaxOut, setNewMaxOut] = useState(16_384);
  const [newSupportsVision, setNewSupportsVision] = useState(true);
  const [newSupportsReasoning, setNewSupportsReasoning] = useState(true);
  const [newReasoningLevels, setNewReasoningLevels] = useState('');
  const [newReasoningMap, setNewReasoningMap] = useState('');
  const [editingModel, setEditingModel] = useState<ModelMeta | null>(null);
  const [fetchingInfo, setFetchingInfo] = useState(false);
  const [fetchRemoteOpen, setFetchRemoteOpen] = useState(false);
  const [remoteModels, setRemoteModels] = useState<string[]>([]);
  const [remoteFilter, setRemoteFilter] = useState('');
  const [remoteSelected, setRemoteSelected] = useState<Record<string, boolean>>({});
  const [fetchingRemote, setFetchingRemote] = useState(false);
  const [addingRemote, setAddingRemote] = useState(false);
  const [testingModelId, setTestingModelId] = useState<string | null>(null);
  const [testResults, setTestResults] = useState<Record<string, { ok: boolean; msg: string }>>({});
  const [syncing, setSyncing] = useState(false);
  const [hiddenModelIds, setHiddenModelIds] = useState<Record<string, boolean>>({});
  const [draggingModelId, setDraggingModelId] = useState<string | null>(null);
  const [renamingModelId, setRenamingModelId] = useState<string | null>(null);
  const [renameModelDraft, setRenameModelDraft] = useState('');

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
      name: isEn ? 'New provider' : '新供应商',
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
    onFlash(isEn ? `Provider "${name}" saved` : `供应商 "${name}" 配置已保存并同步`);
  };

  const removeProvider = async (id: string) => {
    await apiFetch(`/api/v1/providers/${encodeURIComponent(id)}`, { method: 'DELETE' }).catch(
      () => {},
    );
    await refreshInitialData();
    setDraft(null);
    onFlash(isEn ? 'Provider deleted' : '供应商已删除');
  };

  const syncModelsDev = async () => {
    setSyncing(true);
    try {
      const res = await apiFetch<{ synced_count: number }>('/api/v1/models/sync', {
        method: 'POST',
        body: JSON.stringify({}),
      });
      await refreshInitialData();
      onFlash(
        isEn
          ? `Synced ${res.synced_count} models from models.dev`
          : `成功从 models.dev 同步更新了 ${res.synced_count} 个模型的窗口与计费单价`,
      );
    } catch (e) {
      onFlash(
        isEn
          ? `models.dev sync failed: ${(e as Error).message}`
          : `models.dev 在线同步失败: ${(e as Error).message}`,
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
        body: JSON.stringify({ model_id: id }),
      });
      if (res.found && res.meta) {
        if (!newModelName.trim()) setNewModelName(res.meta.display_name || id);
        if (res.meta.context_window > 0) setNewCtxWin(res.meta.context_window);
        if (res.meta.max_output_tokens > 0) setNewMaxOut(res.meta.max_output_tokens);
        setNewSupportsVision(Boolean(res.meta.supports_vision));
        setNewSupportsReasoning(Boolean(res.meta.supports_reasoning));
        onFlash(isEn ? `Loaded metadata for "${res.meta.id}"` : `已从 models.dev 匹配 "${res.meta.id}"`);
      } else {
        onFlash(isEn ? 'No matching model found in models.dev' : '未在 models.dev 找到匹配模型');
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
    setRemoteSelected({});
    setRemoteFilter('');
    try {
      const res = await apiFetch<{ models: string[] }>('/api/v1/models/fetch-remote', {
        method: 'POST',
        body: JSON.stringify({ base_url: draft.baseUrl.trim(), api_key: draft.apiKey }),
      });
      const existingIds = new Set(
        models.filter((m) => m.provider_id === draft?.id).map((m) => m.id),
      );
      setRemoteModels((res.models || []).filter((id) => !existingIds.has(id)));
    } catch (e) {
      onFlash(isEn ? `Fetch /v1/models failed: ${(e as Error).message}` : `获取 /v1/models 失败: ${(e as Error).message}`);
      setFetchRemoteOpen(false);
    } finally {
      setFetchingRemote(false);
    }
  };

  const addSelectedRemoteModels = async () => {
    if (!draft?.id) {
      onFlash(isEn ? 'Save the provider before adding models' : '请先保存供应商，再添加模型');
      return;
    }
    const chosen = Object.entries(remoteSelected)
      .filter(([, v]) => v)
      .map(([k]) => k);
    if (chosen.length === 0) return;
    setAddingRemote(true);
    try {
      for (const id of chosen) {
        let meta: Partial<ModelMeta> = {};
        try {
          const lookup = await apiFetch<{ found: boolean; meta?: ModelMeta }>('/api/v1/models/lookup', {
            method: 'POST',
            body: JSON.stringify({ model_id: id }),
          });
          if (lookup.found && lookup.meta) meta = lookup.meta;
        } catch {
          // fallback to defaults
        }
        await apiFetch('/api/v1/models', {
          method: 'POST',
          body: JSON.stringify({
            id,
            provider_id: draft.id,
            display_name: meta.display_name || id,
            source: meta.source || 'custom',
            context_window: meta.context_window || 128000,
            max_output_tokens: meta.max_output_tokens || 16384,
            supports_vision: meta.supports_vision ?? false,
            supports_tools: meta.supports_tools ?? true,
            supports_reasoning: meta.supports_reasoning ?? false,
            supports_caching: meta.supports_caching ?? false,
            pricing: meta.pricing,
          }),
        });
      }
      setFetchRemoteOpen(false);
      await refreshInitialData();
      onFlash(isEn ? `Added ${chosen.length} models` : `已添加 ${chosen.length} 个模型`);
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
        onFlash(isEn ? `Model "${m.id}" OK (${res.latency_ms ?? 0}ms)` : `模型 "${m.id}" 测试通过 (${res.latency_ms ?? 0}ms)`);
      } else {
        setTestResults((prev) => ({ ...prev, [m.id]: { ok: false, msg: res.error || 'Error' } }));
        onFlash(isEn ? `Test failed: ${res.error}` : `模型测试失败: ${res.error}`);
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
    setNewModelId('');
    setNewModelName('');
    setNewCtxWin(128_000);
    setNewMaxOut(16_384);
    setNewSupportsVision(true);
    setNewSupportsReasoning(true);
    setNewReasoningLevels('');
    setNewReasoningMap('');
    setShowAddModel(true);
  };

  const openEditModelDialog = (m: ModelMeta) => {
    setEditingModel(m);
    setNewModelId(m.id);
    setNewModelName(m.display_name || '');
    setNewCtxWin(m.context_window || 128_000);
    setNewMaxOut(m.max_output_tokens || 16_384);
    setNewSupportsVision(Boolean(m.supports_vision));
    setNewSupportsReasoning(Boolean(m.supports_reasoning));
    setNewReasoningLevels((m.reasoning_levels || []).join(', '));
    setNewReasoningMap(formatReasoningMapInput(m.reasoning_map));
    setShowAddModel(true);
  };

  const addCustomModel = async () => {
    if (!draft || !newModelId.trim()) return;
    if (!draft.id) {
      onFlash(isEn ? 'Save the provider before adding models' : '请先保存供应商，再添加模型');
      return;
    }
    const modelId = newModelId.trim();
    const parsedLevels = parseReasoningLevelsInput(newReasoningLevels);
    const parsedMap = parseReasoningMapInput(newReasoningMap);
    await apiFetch('/api/v1/models', {
      method: 'POST',
      body: JSON.stringify({
        ...(editingModel || {}),
        id: modelId,
        provider_id: draft.id,
        display_name: newModelName.trim() || modelId,
        source: editingModel?.source || 'custom',
        context_window: newCtxWin,
        max_output_tokens: newMaxOut,
        supports_vision: newSupportsVision,
        supports_tools: editingModel?.supports_tools ?? true,
        supports_reasoning: newSupportsReasoning || Boolean(parsedLevels && parsedLevels.length >= 2),
        supports_caching: editingModel?.supports_caching ?? true,
        reasoning_levels: parsedLevels,
        reasoning_map: parsedMap,
      }),
    }).catch(() => {});
    setEditingModel(null);
    setNewModelId('');
    setNewModelName('');
    setNewReasoningLevels('');
    setNewReasoningMap('');
    setShowAddModel(false);
    await refreshInitialData();
    onFlash(
      editingModel
        ? isEn
          ? `Model "${modelId}" updated`
          : `已更新模型 "${modelId}"`
        : isEn
          ? `Model "${modelId}" added`
          : `已向 ${draft.id} 添加模型 "${modelId}"`,
    );
  };

  const removeModel = async (m: ModelMeta) => {
    setHiddenModelIds((prev) => ({ ...prev, [m.id]: true }));
    await apiFetch(`/api/v1/models/${encodeURIComponent(m.id)}?provider_id=${encodeURIComponent(m.provider_id)}`, {
      method: 'DELETE',
    }).catch(() => {});
    await refreshInitialData();
    onFlash(isEn ? `Deleted "${m.id}"` : `已删除模型 "${m.id}"`);
  };

  const renameModel = async (m: ModelMeta, nextName: string) => {
    const clean = nextName.trim();
    setRenamingModelId(null);
    if (!clean || clean === (m.display_name || m.id)) return;
    await apiFetch('/api/v1/models', {
      method: 'POST',
      body: JSON.stringify({ ...m, display_name: clean }),
    }).catch(() => {});
    await refreshInitialData();
    onFlash(isEn ? `Renamed model to "${clean}"` : `已重命名为 "${clean}"`);
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
        <div className="flex shrink-0 items-center gap-2">
          <button
            type="button"
            onClick={startNewProvider}
            className="flex h-8 items-center gap-1 rounded-lg bg-primary px-3.5 text-ui-sm font-medium text-primary-foreground transition-opacity hover:opacity-90"
          >
            <Plus className="size-3.5" />
            <span>{isEn ? 'Add provider' : '添加供应商'}</span>
          </button>
        </div>
      </div>

      <div className="mt-5 grid grid-cols-1 overflow-hidden rounded-2xl border border-border bg-card md:grid-cols-[220px_minmax(0,1fr)]">
        {/* Left: custom providers */}
        <div className="space-y-1 border-b border-border p-3 md:border-r md:border-b-0">
          <div className="px-2.5 py-1 text-ui-xs font-medium text-foreground-subtlest">
            {isEn ? 'Custom providers' : '自定义供应商'}
          </div>
          {providers.length === 0 ? (
            <p className="px-2.5 py-2 text-ui-sm text-foreground-subtlest">
              {isEn ? 'No providers yet.' : '暂无供应商。'}
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
                {isEn
                  ? 'Select a provider on the left, or add a new one.'
                  : '在左侧选择供应商，或添加一个新的供应商。'}
              </p>
              <button
                type="button"
                onClick={startNewProvider}
                className="flex h-8 items-center gap-1 rounded-lg bg-primary px-3.5 text-ui-sm font-medium text-primary-foreground hover:opacity-90"
              >
                <Plus className="size-3.5" />
                <span>{isEn ? 'Add provider' : '添加供应商'}</span>
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
                    placeholder={isEn ? 'Provider name (click to rename)' : '供应商名称（点击重命名）'}
                    aria-label={isEn ? 'Provider name' : '供应商名称'}
                    className="h-8 min-w-0 flex-1 rounded-lg border border-border/70 bg-input/60 px-2.5 text-ui-lg font-semibold text-foreground transition-colors hover:border-border-hover hover:bg-input focus:border-primary focus:bg-input focus:outline-none"
                  />
                  <span className="shrink-0 text-ui-xs text-foreground-subtlest opacity-0 transition-opacity group-hover:opacity-100">
                    {isEn ? 'Rename' : '可重命名'}
                  </span>
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  <Toggle
                    checked={draft.enabled}
                    onChange={(v) => {
                      setDraft({ ...draft, enabled: v });
                      void persistDraft({ enabled: v });
                    }}
                    label={isEn ? 'Provider enabled' : '启用供应商'}
                  />
                  {isSavedDraft ? (
                    <button
                      type="button"
                      onClick={() => void removeProvider(draft.id)}
                      aria-label={isEn ? 'Delete provider' : '删除供应商'}
                      title={isEn ? 'Delete provider' : '删除供应商'}
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
                    {isEn ? 'API format' : 'API 格式'}
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
                      aria-label={showKey ? (isEn ? 'Hide API key' : '隐藏密钥') : isEn ? 'Show API key' : '显示密钥'}
                      className="absolute top-1/2 right-1 flex size-7 -translate-y-1/2 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
                    >
                      {showKey ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                    </button>
                  </span>
                </label>

                {showAdvanced ? (
                  <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                    <label className="block space-y-1.5">
                      <span className="text-ui-xs text-foreground-subtle">
                        {isEn ? 'Provider ID' : '供应商 ID'}
                      </span>
                      <input
                        value={draft.id}
                        onChange={(e) => setDraft({ ...draft, id: e.target.value })}
                        onBlur={() => void persistDraft()}
                        placeholder="custom-gateway"
                        className={settingsMonoInputClass}
                      />
                    </label>
                    <label className="block space-y-1.5">
                      <span className="text-ui-xs text-foreground-subtle">
                        {isEn ? 'Credential routing' : '密钥路由'}
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
                        <option value="direct">{isEn ? 'Direct' : 'Direct（直接下发）'}</option>
                        <option value="server_proxy">
                          {isEn ? 'Server proxy' : 'Server Proxy（中心代理）'}
                        </option>
                      </select>
                    </label>
                  </div>
                ) : null}

                <button
                  type="button"
                  onClick={() => setShowAdvanced((v) => !v)}
                  className="text-ui-xs text-foreground-subtlest transition-colors hover:text-foreground"
                >
                  {showAdvanced
                    ? isEn
                      ? 'Hide advanced'
                      : '收起高级选项'
                    : isEn
                      ? 'Advanced'
                      : '高级选项'}
                </button>
              </div>

              {/* Model list */}
              <div className="space-y-2 pt-2">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="text-ui-sm font-medium text-foreground">
                    {isEn ? 'Model list' : '模型列表'}
                  </span>
                  <div className="flex items-center gap-1.5">
                    <button
                      type="button"
                      onClick={() => void fetchFromV1Models()}
                      className="flex h-7 items-center gap-1 rounded-lg border border-border bg-card px-2.5 text-ui-xs text-foreground transition-colors hover:bg-hover"
                    >
                      <Globe className="size-3" />
                      <span>{isEn ? 'Fetch from /v1/models' : '从 /v1/models 获取'}</span>
                    </button>
                    <button
                      type="button"
                      onClick={openAddModelDialog}
                      className="flex h-7 items-center gap-1 rounded-lg border border-border bg-card px-2.5 text-ui-xs text-foreground transition-colors hover:bg-hover"
                    >
                      <Plus className="size-3" />
                      <span>{isEn ? 'Add model manually' : '手动添加模型'}</span>
                    </button>
                  </div>
                </div>

                {fetchRemoteOpen && (
                  <div className="zcode-dialog-overlay fixed inset-0 z-50 flex items-center justify-center bg-black/45 p-4 backdrop-blur-[2px]">
                    <div className="zcode-dialog-content flex max-h-[82vh] w-full max-w-lg flex-col rounded-2xl border border-border bg-card p-5 shadow-xl">
                      <div className="flex items-start justify-between gap-2">
                        <div>
                          <h3 className="text-ui-lg font-semibold text-foreground">
                            {isEn ? 'Select models from /v1/models' : '从 /v1/models 选择模型'}
                          </h3>
                          <p className="mt-1 text-ui-xs text-foreground-subtle">
                            {isEn
                              ? 'Reads the model list using the provider base URL and API key above.'
                              : '使用上方配置的 Base URL 与 API Key 读取模型列表。'}
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
                          placeholder={isEn ? 'Filter by name' : '按名称筛选'}
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
                            {isEn ? 'Select all listed' : '全选'}
                          </button>
                          <button
                            type="button"
                            onClick={() => setRemoteSelected({})}
                            className="px-2 py-1 text-foreground-subtle hover:text-foreground"
                          >
                            {isEn ? 'Clear selection' : '清空'}
                          </button>
                        </div>
                        <span className="text-foreground-subtle">
                          {Object.values(remoteSelected).filter(Boolean).length} {isEn ? 'selected' : '已选'}
                        </span>
                      </div>

                      <div className="mt-2.5 flex-1 space-y-1.5 overflow-y-auto pr-1">
                        {fetchingRemote ? (
                          <div className="py-8 text-center text-ui-sm text-foreground-subtle">
                            {isEn ? 'Loading…' : '加载中…'}
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
                          {isEn ? 'Cancel' : '取消'}
                        </button>
                        <button
                          type="button"
                          disabled={addingRemote || Object.values(remoteSelected).filter(Boolean).length === 0}
                          onClick={() => void addSelectedRemoteModels()}
                          className="h-8 rounded-lg bg-primary px-3 text-ui-sm font-medium text-primary-foreground hover:opacity-90 disabled:opacity-50"
                        >
                          {addingRemote
                            ? isEn
                              ? 'Adding…'
                              : '添加中…'
                            : isEn
                              ? 'Add selected models'
                              : '添加所选模型'}
                        </button>
                      </div>
                    </div>
                  </div>
                )}

                {showAddModel && (
                  <div className="zcode-dialog-overlay fixed inset-0 z-50 flex items-center justify-center bg-black/45 p-4 backdrop-blur-[2px]">
                    <div className="zcode-dialog-content w-full max-w-md rounded-2xl border border-border bg-card p-5 shadow-xl">
                      <div className="flex items-center justify-between">
                        <h3 className="text-ui-lg font-semibold text-foreground">
                          {isEn ? 'Add model manually' : '手动添加模型'}
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
                            onChange={(e) => setNewModelId(e.target.value)}
                            placeholder="e.g. cli/glm-5.3"
                            className={settingsMonoInputClass}
                          />
                        </label>
                        <label className="block space-y-1">
                          <span className="text-ui-xs text-foreground-subtle">
                            {isEn ? 'Model name' : '显示名称'}
                          </span>
                          <input
                            value={newModelName}
                            onChange={(e) => setNewModelName(e.target.value)}
                            placeholder={isEn ? 'Optional short name' : '可选短名称'}
                            className={settingsInputClass}
                          />
                        </label>
                        <div className="grid grid-cols-2 gap-3">
                          <label className="block space-y-1">
                            <span className="text-ui-xs text-foreground-subtle">
                              {isEn ? 'Context window' : '上下文窗口'}
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
                              {isEn ? 'Max output tokens' : '最大输出'}
                            </span>
                            <input
                              type="number"
                              value={newMaxOut}
                              onChange={(e) => setNewMaxOut(Number(e.target.value))}
                              className={settingsMonoInputClass}
                            />
                          </label>
                        </div>
                        <div className="flex items-center gap-4 pt-1 text-ui-xs text-foreground">
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
                              checked={newSupportsReasoning}
                              onChange={(e) => setNewSupportsReasoning(e.target.checked)}
                            />
                            <span>Reasoning</span>
                          </label>
                        </div>
                        {newSupportsReasoning && (
                          <div className="space-y-2.5 rounded-xl border border-border/70 bg-surface/50 p-3">
                            <label className="block space-y-1">
                              <span className="text-ui-xs text-foreground-subtle">
                                {isEn
                                  ? 'Reasoning levels (low → high, comma-separated)'
                                  : '推理强度档位（按低→高逗号分隔，留空则按模型族自动推断）'}
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
                                {isEn
                                  ? 'Wire effort mapping (optional, e.g. max=xhigh, off=none)'
                                  : '协议参数映射（可选，如 max=xhigh, off=none 或 high=12000）'}
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
                            ? isEn
                              ? 'Fetching…'
                              : '获取中…'
                            : isEn
                              ? 'Fetch model info'
                              : '从 models.dev 获取信息'}
                        </button>
                        <div className="flex items-center gap-2">
                          <button
                            type="button"
                            onClick={() => setShowAddModel(false)}
                            className="h-8 rounded-lg px-3 text-ui-sm text-foreground-subtle hover:bg-hover"
                          >
                            {isEn ? 'Cancel' : '取消'}
                          </button>
                          <button
                            type="button"
                            onClick={() => void addCustomModel()}
                            className="h-8 rounded-lg bg-primary px-3 text-ui-sm font-medium text-primary-foreground hover:opacity-90"
                          >
                            {isEn ? 'Save' : '保存'}
                          </button>
                        </div>
                      </div>
                    </div>
                  </div>
                )}

                {providerModels.length === 0 ? (
                  <p className="py-6 text-center text-ui-sm text-foreground-subtlest">
                    {isEn
                      ? 'No models registered for this provider yet.'
                      : '该供应商暂未注册模型。'}
                  </p>
                ) : (
                  <div className="divide-y divide-border">
                    {providerModels.map((m) => {
                      const visible = !hiddenModelKeys[`${m.provider_id}::${m.id}`];
                      const testRes = testResults[m.id];
                      const isRenaming = renamingModelId === m.id;
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
                            isEn
                              ? `${m.id} · In $${m.pricing.input_per_1m}/1M · Out $${m.pricing.output_per_1m}/1M`
                              : `${m.id} · 输入 $${m.pricing.input_per_1m}/1M · 输出 $${m.pricing.output_per_1m}/1M`
                          }
                        >
                          <div className="flex min-w-0 items-center gap-1.5">
                            <GripVertical className="size-3.5 shrink-0 cursor-grab text-foreground-subtlest" />
                            {isRenaming ? (
                              <input
                                autoFocus
                                value={renameModelDraft}
                                onChange={(e) => setRenameModelDraft(e.target.value)}
                                onBlur={() => void renameModel(m, renameModelDraft)}
                                onKeyDown={(e) => {
                                  if (e.key === 'Enter') void renameModel(m, renameModelDraft);
                                  if (e.key === 'Escape') setRenamingModelId(null);
                                }}
                                className="h-7 w-44 rounded-md border border-border bg-input px-2 text-ui-sm text-foreground focus:border-primary focus:outline-none"
                              />
                            ) : (
                              <span className="truncate font-mono text-ui-base text-foreground">
                                {m.display_name && m.display_name !== m.id ? m.display_name : m.id}
                              </span>
                            )}
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
                              onClick={() => openEditModelDialog(m)}
                              title={isEn ? 'Configure model & reasoning levels' : '配置模型与推理强度档位'}
                              className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
                            >
                              <SlidersHorizontal className="size-3.5" />
                            </button>
                            <button
                              type="button"
                              onClick={() => {
                                setRenamingModelId(m.id);
                                setRenameModelDraft(m.display_name || m.id);
                              }}
                              title={isEn ? 'Rename display name' : '重命名显示名称'}
                              className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
                            >
                              <Pencil className="size-3.5" />
                            </button>
                            <button
                              type="button"
                              disabled={testingModelId === m.id}
                              onClick={() => void testModelConnection(m)}
                              title={isEn ? 'Test model connection' : '测试模型连通性'}
                              aria-label={isEn ? 'Test model connection' : '测试模型连通性'}
                              className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground disabled:opacity-40"
                            >
                              <Cable className={`size-3.5 ${testingModelId === m.id ? 'animate-pulse text-primary' : ''}`} />
                            </button>
                            <button
                              type="button"
                              onClick={() => {
                                void navigator.clipboard?.writeText(m.id);
                                onFlash(isEn ? `Copied "${m.id}"` : `已复制模型 ID "${m.id}"`);
                              }}
                              aria-label={isEn ? 'Copy model ID' : '复制模型 ID'}
                              className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
                            >
                              <Copy className="size-3.5" />
                            </button>
                            <button
                              type="button"
                              onClick={() => void removeModel(m)}
                              aria-label={isEn ? 'Delete model' : '删除模型'}
                              className="flex size-7 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-destructive-surface hover:text-destructive-surface-fg"
                            >
                              <Trash2 className="size-3.5" />
                            </button>
                            <Toggle
                              checked={visible}
                              onChange={() => toggleModelVisibility(m.provider_id, m.id)}
                              label={isEn ? 'Show in model picker' : '在模型选择框中显示'}
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
    </>
  );
}

/* -------------------------------------------------------------------------- */
/* Remote devices (runners)                                                   */
/* -------------------------------------------------------------------------- */

function RunnersSection() {
  const { locale, runners } = useNexStore();
  const isEn = locale === 'en-US';
  const [copied, setCopied] = useState(false);
  const wsHost = typeof window !== 'undefined' ? window.location.host : '127.0.0.1:8080';
  const joinCmd = `./nex-runner -name "My-Linux-Box" -server ws://${wsHost}/ws/runner`;

  return (
    <div className="space-y-6">
      <SettingsGroupCard>
        <div className="space-y-3 px-5 py-4">
          <div>
            <div className="text-ui-base font-medium text-foreground">
              {isEn ? 'Join a new runner' : '接入新的远程设备'}
            </div>
            <p className="mt-1 text-ui-sm leading-relaxed text-foreground-subtle">
              {isEn
                ? 'Run the single static binary on macOS, Linux, or Windows. It dials back over a long-lived connection and traverses NAT automatically.'
                : '在目标 macOS、Linux 或 Windows 机器上运行单文件二进制，即可通过反向长连接穿透内网接入控制中心。'}
            </p>
          </div>
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
              <span>
                {copied ? (isEn ? 'Copied' : '已复制') : isEn ? 'Copy command' : '复制命令'}
              </span>
            </button>
          </div>
        </div>
      </SettingsGroupCard>

      <SectionHeading
        title={isEn ? `Connected runners (${runners.length})` : `已连接设备 (${runners.length})`}
        description={
          isEn
            ? 'Status, platform, workspace, and the config revision each runner has synced.'
            : '状态、平台、默认工作区与已同步配置修订号。'
        }
      />

      <SettingsGroupCard>
        {runners.length === 0 ? (
          <div className="px-5 py-10 text-center text-ui-sm text-foreground-subtlest">
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
                  <span className="truncate text-ui-base font-medium text-foreground">
                    {r.name}
                  </span>
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
                  <span className="rounded bg-surface px-1.5 py-0.5 font-mono">
                    {r.shell || 'default'}
                  </span>
                  {r.hostname ? <span className="font-mono">{r.hostname}</span> : null}
                  {r.workspace_default ? (
                    <span className="truncate font-mono">{r.workspace_default}</span>
                  ) : null}
                </div>
              </div>
              <div className="font-mono text-ui-xs tabular-nums text-foreground-subtlest sm:text-right">
                rev #{r.synced_config_rev}
              </div>
            </div>
          ))
        )}
      </SettingsGroupCard>
    </div>
  );
}
