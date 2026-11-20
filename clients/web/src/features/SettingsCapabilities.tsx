import { ChevronRight, Trash2 } from 'lucide-react';
import { type ReactNode, useState } from 'react';
import { apiFetch } from '../core/protocol';
import { useNexStore } from '../core/store';
import { CodeEditorWithHighlight, HighlightedCode, MarkdownRenderer } from './MarkdownRenderer';
import {
  SectionHeading,
  SettingsGroupCard,
  settingsInputClass,
  settingsMonoInputClass,
  settingsSelectClass,
} from './SettingsPage';

const areaClass =
  'w-full rounded-lg border border-border bg-input p-3 text-ui-sm leading-relaxed text-foreground placeholder-foreground-subtlest';

function Field({
  label,
  description,
  children,
}: {
  label: string;
  description?: string;
  children: ReactNode;
}) {
  return (
    <label className="block min-w-0 space-y-1.5">
      <span className="text-ui-xs text-foreground-subtle">{label}</span>
      {description ? <p className="text-ui-xs text-foreground-subtlest">{description}</p> : null}
      {children}
    </label>
  );
}

function SaveButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="h-8 rounded-lg bg-primary px-3.5 text-ui-sm font-medium text-primary-foreground transition-opacity hover:opacity-90"
    >
      {label}
    </button>
  );
}

function EmptyRow({ children }: { children: ReactNode }) {
  return (
    <div className="px-5 py-10 text-center text-ui-sm text-foreground-subtlest">{children}</div>
  );
}

function CardHeading({ title, trailing }: { title: string; trailing?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 px-5 py-3.5">
      <h3 className="text-ui-base font-medium text-foreground">{title}</h3>
      {trailing}
    </div>
  );
}

function MetaBadge({ children, mono = false }: { children: ReactNode; mono?: boolean }) {
  return (
    <span
      className={`shrink-0 rounded-md border border-border bg-surface px-1.5 py-0.5 text-ui-xs text-foreground-subtle ${
        mono ? 'font-mono' : ''
      }`}
    >
      {children}
    </span>
  );
}

/* -------------------------------------------------------------------------- */
/* Subagents                                                                  */
/* -------------------------------------------------------------------------- */

export function AgentsSettingsSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const { locale, agentProfiles, refreshRegistries } = useNexStore();
  const isEn = locale === 'en-US';

  const [slug, setSlug] = useState('code-reviewer');
  const [name, setName] = useState('Code Reviewer');
  const [prompt, setPrompt] = useState(
    'You are a strict read-only code reviewer focusing on concurrency and edge cases.',
  );
  const [isolation, setIsolation] = useState<'shared' | 'worktree'>('shared');
  const [toolsCsv, setToolsCsv] = useState('read,grep,glob,codemode');

  const saveAgent = async () => {
    await apiFetch('/api/v1/agents', {
      method: 'POST',
      body: JSON.stringify({
        slug: slug.trim(),
        display_name: name.trim() || slug.trim(),
        description: name.trim(),
        system_prompt: prompt,
        tools_allow: toolsCsv
          .split(',')
          .map((s) => s.trim())
          .filter(Boolean),
        max_turns: 25,
        isolation_mode: isolation,
      }),
    });
    await refreshRegistries();
    onFlash(isEn ? `Subagent profile "${slug}" saved` : `已保存并同步子智能体预设 "${slug}"`);
  };

  const removeAgent = async (targetSlug: string) => {
    await apiFetch(`/api/v1/agents/${encodeURIComponent(targetSlug)}`, { method: 'DELETE' });
    await refreshRegistries();
  };

  return (
    <div className="space-y-6">
      <SectionHeading
        title={isEn ? 'New subagent profile' : '新建子智能体'}
        description={
          isEn
            ? 'Spawn read-only or isolated workers with their own tools and prompt.'
            : '为子智能体定义独立工具白名单、系统提示词与 Worktree 隔离策略。'
        }
      />

      <SettingsGroupCard>
        <div className="grid grid-cols-1 gap-4 px-5 py-4 sm:grid-cols-2">
          <Field label={isEn ? 'Slug' : '标识 (Slug)'}>
            <input
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
              placeholder="explore / reviewer"
              className={settingsMonoInputClass}
            />
          </Field>
          <Field label={isEn ? 'Display name' : '显示名称'}>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              className={settingsInputClass}
            />
          </Field>
        </div>

        <div className="grid grid-cols-1 gap-4 px-5 py-4 sm:grid-cols-2">
          <Field
            label={isEn ? 'Allowed tools' : '允许工具'}
            description={isEn ? 'Comma-separated tool allowlist.' : '逗号分隔的工具白名单。'}
          >
            <input
              value={toolsCsv}
              onChange={(e) => setToolsCsv(e.target.value)}
              placeholder="read,grep,glob"
              className={settingsMonoInputClass}
            />
          </Field>
          <Field
            label={isEn ? 'Isolation' : '隔离模式'}
            description={
              isEn
                ? 'Shared workspace or an independent Git worktree.'
                : '共享工作区或独立的 Git Worktree。'
            }
          >
            <select
              value={isolation}
              onChange={(e) => setIsolation(e.target.value as 'shared' | 'worktree')}
              className={settingsSelectClass}
            >
              <option value="shared">{isEn ? 'Shared workspace' : '共享工作区'}</option>
              <option value="worktree">{isEn ? 'Git worktree' : '独立 Git Worktree'}</option>
            </select>
          </Field>
        </div>

        <div className="space-y-3 px-5 py-4">
          <Field
            label={isEn ? 'System prompt' : '系统提示词'}
            description={
              isEn ? 'Appended when this subagent is spawned.' : '派生子智能体时注入的系统提示词。'
            }
          >
            <CodeEditorWithHighlight
              rows={4}
              language="markdown"
              value={prompt}
              onChange={setPrompt}
              placeholder={isEn ? 'Role-specific system prompt...' : '角色专属 System Prompt...'}
            />
          </Field>
          <div className="flex justify-end">
            <SaveButton
              label={isEn ? 'Save subagent' : '保存并同步'}
              onClick={() => void saveAgent()}
            />
          </div>
        </div>
      </SettingsGroupCard>

      <SectionHeading
        title={
          isEn
            ? `Configured profiles (${agentProfiles.length})`
            : `已配置角色 (${agentProfiles.length})`
        }
      />

      <SettingsGroupCard>
        {agentProfiles.length === 0 ? (
          <EmptyRow>
            {isEn
              ? 'Using the built-in explore (read-only) and worker (read-write) subagents.'
              : '当前使用系统内置默认 explore（只读）与 worker（读写）子智能体画像。'}
          </EmptyRow>
        ) : (
          agentProfiles.map((ap) => (
            <div
              key={ap.slug}
              className="flex items-center justify-between gap-3 px-5 py-3.5"
            >
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="text-ui-base font-medium text-foreground">
                    {ap.display_name}
                  </span>
                  <MetaBadge mono>{ap.slug}</MetaBadge>
                  <MetaBadge mono>{ap.isolation_mode}</MetaBadge>
                </div>
                <p className="mt-1 truncate text-ui-sm text-foreground-subtle">{ap.system_prompt}</p>
              </div>
              <button
                type="button"
                onClick={() => void removeAgent(ap.slug)}
                aria-label={isEn ? 'Delete subagent' : '删除子智能体'}
                className="flex size-7 shrink-0 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-destructive-surface hover:text-destructive-surface-fg"
              >
                <Trash2 className="size-4" />
              </button>
            </div>
          ))
        )}
      </SettingsGroupCard>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* MCP servers                                                                */
/* -------------------------------------------------------------------------- */

export function MCPSettingsSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const { locale, mcpServers, refreshRegistries } = useNexStore();
  const isEn = locale === 'en-US';

  const [name, setName] = useState('playwright');
  const [hostMode, setHostMode] = useState<'runner_local' | 'server_hosted'>('runner_local');
  const [command, setCommand] = useState('npx');
  const [argsCsv, setArgsCsv] = useState('@playwright/mcp@latest');
  const [idleTTL, setIdleTTL] = useState(300);

  const saveMCP = async () => {
    await apiFetch('/api/v1/mcp', {
      method: 'POST',
      body: JSON.stringify({
        name: name.trim(),
        host_mode: hostMode,
        transport: hostMode === 'server_hosted' ? 'http' : 'stdio',
        command: hostMode === 'runner_local' ? command.trim() : '',
        args: argsCsv.split(/\s+/).filter(Boolean),
        idle_ttl_sec: idleTTL,
        scope: 'global',
        enabled: true,
      }),
    });
    await refreshRegistries();
    onFlash(
      isEn ? `MCP server "${name}" saved` : `MCP 服务 "${name}" 已保存（懒加载 + codemode 暴露）`,
    );
  };

  const removeMCP = async (mcpName: string) => {
    await apiFetch(`/api/v1/mcp/${encodeURIComponent(mcpName)}`, { method: 'DELETE' });
    await refreshRegistries();
  };

  return (
    <div className="space-y-6">
      <SettingsGroupCard>
        <div className="grid grid-cols-1 gap-4 px-5 py-4 sm:grid-cols-3">
          <Field label={isEn ? 'Namespace' : '命名空间'}>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="github / playwright"
              className={settingsMonoInputClass}
            />
          </Field>
          <Field label={isEn ? 'Host mode' : '托管方式'}>
            <select
              value={hostMode}
              onChange={(e) => setHostMode(e.target.value as 'runner_local' | 'server_hosted')}
              className={settingsSelectClass}
            >
              <option value="runner_local">
                {isEn ? 'Runner local (stdio)' : '节点本地 (stdio)'}
              </option>
              <option value="server_hosted">{isEn ? 'Server hosted' : '中心托管'}</option>
            </select>
          </Field>
          <Field
            label={isEn ? 'Idle TTL (s)' : '空闲回收 (秒)'}
            description={isEn ? 'Reclaim the process after idling.' : '空闲达到该时长后回收进程。'}
          >
            <input
              type="number"
              value={idleTTL}
              onChange={(e) => setIdleTTL(Number(e.target.value))}
              className={settingsMonoInputClass}
            />
          </Field>
        </div>

        {hostMode === 'runner_local' ? (
          <div className="grid grid-cols-1 gap-4 px-5 py-4 sm:grid-cols-3">
            <Field label={isEn ? 'Command' : '命令'}>
              <input
                value={command}
                onChange={(e) => setCommand(e.target.value)}
                placeholder="npx / uvx"
                className={settingsMonoInputClass}
              />
            </Field>
            <div className="sm:col-span-2">
              <Field
                label={isEn ? 'Arguments' : '参数'}
                description={isEn ? 'Whitespace-separated.' : '空格分隔。'}
              >
                <input
                  value={argsCsv}
                  onChange={(e) => setArgsCsv(e.target.value)}
                  className={settingsMonoInputClass}
                />
              </Field>
            </div>
          </div>
        ) : null}

        <div className="flex justify-end px-5 py-4">
          <SaveButton
            label={isEn ? 'Save MCP server' : '保存 MCP 配置'}
            onClick={() => void saveMCP()}
          />
        </div>
      </SettingsGroupCard>

      <SectionHeading
        title={isEn ? `MCP servers (${mcpServers.length})` : `MCP 服务器 (${mcpServers.length})`}
      />

      <SettingsGroupCard>
        {mcpServers.length === 0 ? (
          <EmptyRow>
            {isEn ? 'No external MCP servers registered.' : '暂未注册外部 MCP 服务。'}
          </EmptyRow>
        ) : (
          mcpServers.map((m) => (
            <div key={m.name} className="flex items-center justify-between gap-3 px-5 py-3.5">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="font-mono text-ui-base font-medium text-foreground">
                    {m.name}
                  </span>
                  <MetaBadge mono>{m.host_mode}</MetaBadge>
                  <span className="text-ui-xs text-foreground-subtlest">
                    Idle TTL {m.idle_ttl_sec}s
                  </span>
                </div>
                {m.command ? (
                  <p className="mt-0.5 truncate font-mono text-ui-xs text-foreground-subtle">
                    {m.command} {(m.args || []).join(' ')}
                  </p>
                ) : null}
              </div>
              <button
                type="button"
                onClick={() => void removeMCP(m.name)}
                aria-label={isEn ? 'Delete MCP server' : '删除 MCP 服务'}
                className="flex size-7 shrink-0 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-destructive-surface hover:text-destructive-surface-fg"
              >
                <Trash2 className="size-4" />
              </button>
            </div>
          ))
        )}
      </SettingsGroupCard>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* Skills & plugins                                                           */
/* -------------------------------------------------------------------------- */

export function SkillsAndPluginsSection({
  mode,
  onFlash,
}: {
  mode: 'skills' | 'plugins';
  onFlash: (msg: string) => void;
}) {
  const { locale, skills, plugins, refreshRegistries } = useNexStore();
  const isEn = locale === 'en-US';

  const [skName, setSkName] = useState('code-audit');
  const [skDesc, setSkDesc] = useState('Security & concurrency review checklist');
  const [skBody, setSkBody] = useState(
    '# Code Audit Skill\n\n1. Check goroutine leaks\n2. Verify SQL parameterization',
  );
  const [skPreview, setSkPreview] = useState(false);
  const [expandedSkill, setExpandedSkill] = useState<string | null>(null);

  const [plName, setPlName] = useState('safety-guard');
  const [plTS, setPlTS] = useState(
    `export default function (nex: any) {\n  nex.on("tool_call", (ev: any) => {\n    if (ev.toolName === "bash" && String(ev.input.command).includes("rm -rf /")) {\n      return { block: true, reason: "Blocked destructive root command" };\n    }\n  });\n}`,
  );
  const [expandedPlugin, setExpandedPlugin] = useState<string | null>(null);

  if (mode === 'skills') {
    const saveSkill = async () => {
      await apiFetch('/api/v1/skills', {
        method: 'POST',
        body: JSON.stringify({
          name: skName.trim(),
          description: skDesc.trim(),
          content_md: skBody,
          scope: 'global',
          enabled: true,
        }),
      });
      await refreshRegistries();
      onFlash(
        isEn ? `Skill "${skName}" synced to runners` : `Skill "${skName}" 已同步至各 Runner 本地缓存`,
      );
    };

    return (
      <div className="space-y-6">
        <SettingsGroupCard>
          <div className="grid grid-cols-1 gap-4 px-5 py-4 sm:grid-cols-2">
            <Field
              label={isEn ? 'Skill name' : '技能名称'}
              description={isEn ? 'Invoked as $skill-name.' : '以 $skill-name 形式调用。'}
            >
              <input
                value={skName}
                onChange={(e) => setSkName(e.target.value)}
                placeholder="code-audit"
                className={settingsMonoInputClass}
              />
            </Field>
            <Field
              label={isEn ? 'Description' : '触发描述'}
              description={
                isEn
                  ? 'One line injected into the system prompt index.'
                  : '注入系统提示词目录的一句话描述。'
              }
            >
              <input
                value={skDesc}
                onChange={(e) => setSkDesc(e.target.value)}
                className={settingsInputClass}
              />
            </Field>
          </div>
          <div className="space-y-3 px-5 py-4">
            <div className="flex items-center justify-between gap-2">
              <span className="text-ui-xs text-foreground-subtle">
                {isEn ? 'Markdown body' : 'Markdown 内容'}
              </span>
              <div className="inline-flex rounded-lg border border-border bg-surface p-0.5 text-ui-xs">
                <button
                  type="button"
                  onClick={() => setSkPreview(false)}
                  className={`rounded-md px-2 py-0.5 font-medium transition-colors ${
                    !skPreview ? 'bg-popover text-foreground shadow-xs' : 'text-foreground-subtle hover:text-foreground'
                  }`}
                >
                  {isEn ? 'Edit' : '编辑'}
                </button>
                <button
                  type="button"
                  onClick={() => setSkPreview(true)}
                  className={`rounded-md px-2 py-0.5 font-medium transition-colors ${
                    skPreview ? 'bg-popover text-foreground shadow-xs' : 'text-foreground-subtle hover:text-foreground'
                  }`}
                >
                  {isEn ? 'Preview' : '预览'}
                </button>
              </div>
            </div>
            {skPreview ? (
              <div className="min-h-[150px] rounded-lg border border-border bg-surface/60 px-4 py-3">
                <MarkdownRenderer content={skBody} streaming={false} />
              </div>
            ) : (
              <CodeEditorWithHighlight
                testId="skill-md-editor"
                rows={6}
                language="markdown"
                value={skBody}
                onChange={setSkBody}
                placeholder="# Skill title..."
              />
            )}
            <div className="flex justify-end">
              <SaveButton
                label={isEn ? 'Save skill' : '保存并下发'}
                onClick={() => void saveSkill()}
              />
            </div>
          </div>
        </SettingsGroupCard>

        <SectionHeading title={isEn ? `Skills (${skills.length})` : `技能 (${skills.length})`} />

        <SettingsGroupCard>
          {skills.length === 0 ? (
            <EmptyRow>{isEn ? 'No custom skills yet.' : '暂未添加自定义技能。'}</EmptyRow>
          ) : (
            skills.map((s) => {
              const isOpen = expandedSkill === s.name;
              return (
                <div key={s.name} className="px-5 py-3.5">
                  <div className="flex items-center justify-between gap-3">
                    <button
                      type="button"
                      onClick={() => setExpandedSkill(isOpen ? null : s.name)}
                      className="flex min-w-0 flex-1 items-center gap-2 text-left"
                    >
                      <ChevronRight
                        className={`size-3.5 shrink-0 text-foreground-subtlest transition-transform ${
                          isOpen ? 'rotate-90' : ''
                        }`}
                      />
                      <div className="min-w-0">
                        <span className="font-mono text-ui-base font-medium text-foreground">
                          ${s.name}
                        </span>
                        <p className="truncate text-ui-sm text-foreground-subtle">{s.description}</p>
                      </div>
                    </button>
                    <button
                      type="button"
                      onClick={async () => {
                        await apiFetch(`/api/v1/skills/${encodeURIComponent(s.name)}`, {
                          method: 'DELETE',
                        });
                        await refreshRegistries();
                      }}
                      aria-label={isEn ? 'Delete skill' : '删除技能'}
                      className="flex size-7 shrink-0 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-destructive-surface hover:text-destructive-surface-fg"
                    >
                      <Trash2 className="size-4" />
                    </button>
                  </div>
                  {isOpen && s.content_md && (
                    <div className="mt-3 pl-5">
                      <HighlightedCode code={s.content_md} language="markdown" className="max-h-72 text-ui-xs" />
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

  const savePlugin = async () => {
    await apiFetch('/api/v1/plugins', {
      method: 'POST',
      body: JSON.stringify({
        name: plName.trim(),
        source_ts: plTS,
        scope: 'global',
        enabled: true,
      }),
    });
    await refreshRegistries();
    onFlash(
      isEn
        ? `Plugin "${plName}" compiled and synced`
        : `插件 "${plName}" 已通过 esbuild 转译并同步至 goja 引擎`,
    );
  };

  return (
    <div className="space-y-6">
      <SettingsGroupCard>
        <CardHeading
          title={isEn ? 'TypeScript plugin editor' : 'TypeScript 插件编辑器'}
          trailing={<MetaBadge mono>esbuild + goja</MetaBadge>}
        />
        <div className="space-y-3 border-t border-border px-5 py-4">
          <Field label={isEn ? 'Plugin name' : '插件名称'}>
            <input
              value={plName}
              onChange={(e) => setPlName(e.target.value)}
              className={settingsMonoInputClass}
            />
          </Field>
          <CodeEditorWithHighlight
            testId="plugin-ts-editor"
            rows={8}
            language="ts"
            value={plTS}
            onChange={setPlTS}
            placeholder="export default function (nex: any) { ... }"
          />
          <div className="flex justify-end">
            <SaveButton
              label={isEn ? 'Compile & deploy' : '编译并热下发'}
              onClick={() => void savePlugin()}
            />
          </div>
        </div>
      </SettingsGroupCard>

      <SectionHeading
        title={isEn ? `Plugins (${plugins.length})` : `插件 (${plugins.length})`}
      />

      <SettingsGroupCard>
        {plugins.length === 0 ? (
          <EmptyRow>{isEn ? 'No dynamic plugins installed.' : '暂未安装动态插件。'}</EmptyRow>
        ) : (
          plugins.map((p) => {
            const isOpen = expandedPlugin === p.name;
            return (
              <div key={p.name} className="px-5 py-3.5">
                <div className="flex items-center justify-between gap-3">
                  <button
                    type="button"
                    onClick={() => setExpandedPlugin(isOpen ? null : p.name)}
                    className="flex min-w-0 flex-1 items-center gap-2 text-left"
                  >
                    <ChevronRight
                      className={`size-3.5 shrink-0 text-foreground-subtlest transition-transform ${
                        isOpen ? 'rotate-90' : ''
                      }`}
                    />
                    <div className="flex min-w-0 flex-wrap items-center gap-1.5">
                      <span className="font-mono text-ui-base font-medium text-foreground">{p.name}</span>
                      {p.compiled_js_hash ? (
                        <span className="shrink-0 rounded-md bg-success-surface px-1.5 py-0.5 font-mono text-ui-xs text-success-surface-fg">
                          sha256:{p.compiled_js_hash.slice(0, 10)}
                        </span>
                      ) : null}
                    </div>
                  </button>
                  <button
                    type="button"
                    onClick={async () => {
                      await apiFetch(`/api/v1/plugins/${encodeURIComponent(p.name)}`, {
                        method: 'DELETE',
                      });
                      await refreshRegistries();
                    }}
                    aria-label={isEn ? 'Delete plugin' : '删除插件'}
                    className="flex size-7 shrink-0 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-destructive-surface hover:text-destructive-surface-fg"
                  >
                    <Trash2 className="size-4" />
                  </button>
                </div>
                {isOpen && p.source_ts && (
                  <div className="mt-3 pl-5">
                    <HighlightedCode code={p.source_ts} language="ts" className="max-h-72 text-ui-xs" />
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

/* -------------------------------------------------------------------------- */
/* Automations                                                                */
/* -------------------------------------------------------------------------- */

export function AutomationsSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const { locale, runners, cronJobs, selectedRunnerId, refreshRegistries } = useNexStore();
  const isEn = locale === 'en-US';

  const [runnerId, setRunnerId] = useState(selectedRunnerId || runners[0]?.id || '');
  const [title, setTitle] = useState(
    isEn ? 'Daily workspace health check at 9am' : '每天早上 9 点工作区健康巡检',
  );
  const [cronExpr, setCronExpr] = useState('0 9 * * *');
  const [prompt, setPrompt] = useState(
    isEn ? 'Run the test suite and summarize any failures.' : '运行测试套件并生成简要巡检报告',
  );

  const createCron = async () => {
    const targetRunner = runnerId || runners[0]?.id || 'local';
    await apiFetch('/api/v1/cron', {
      method: 'POST',
      body: JSON.stringify({
        runner_id: targetRunner,
        title: title.trim(),
        cron_expr: cronExpr.trim(),
        timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'Local',
        prompt,
        permission_mode: 'autonomous',
        recurring: true,
        enabled: true,
      }),
    });
    await refreshRegistries();
    onFlash(
      isEn
        ? `Automation "${title}" scheduled on runner`
        : `定时任务 "${title}" 已下发至节点本地 Cron 快照`,
    );
  };

  return (
    <div className="space-y-6">
      <SettingsGroupCard>
        <div className="grid grid-cols-1 gap-4 px-5 py-4 sm:grid-cols-3">
          <Field label={isEn ? 'Runner' : '运行设备'}>
            <select
              value={runnerId}
              onChange={(e) => setRunnerId(e.target.value)}
              className={settingsSelectClass}
            >
              {runners.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name} ({r.os})
                </option>
              ))}
            </select>
          </Field>
          <Field label={isEn ? 'Title' : '任务标题'}>
            <input
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              className={settingsInputClass}
            />
          </Field>
          <Field
            label={isEn ? 'Cron expression' : 'Cron 表达式'}
            description={isEn ? 'Runs in the runner timezone.' : '按设备本地时区触发。'}
          >
            <input
              value={cronExpr}
              onChange={(e) => setCronExpr(e.target.value)}
              placeholder="*/30 * * * *"
              className={settingsMonoInputClass}
            />
          </Field>
        </div>
        <div className="space-y-3 px-5 py-4">
          <Field
            label={isEn ? 'Prompt' : '自治提示词'}
            description={
              isEn
                ? 'Executed autonomously when the schedule fires.'
                : '触发后由节点独立执行的完整提示词。'
            }
          >
            <textarea
              rows={3}
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              className={areaClass}
            />
          </Field>
          <div className="flex justify-end">
            <SaveButton
              label={isEn ? 'Create automation' : '创建自动化'}
              onClick={() => void createCron()}
            />
          </div>
        </div>
      </SettingsGroupCard>

      <SectionHeading
        title={isEn ? `Automations (${cronJobs.length})` : `自动化 (${cronJobs.length})`}
      />

      <SettingsGroupCard>
        {cronJobs.length === 0 ? (
          <EmptyRow>{isEn ? 'No automations yet.' : '暂无定时自动化任务。'}</EmptyRow>
        ) : (
          cronJobs.map((j) => (
            <div key={j.id} className="flex items-center justify-between gap-3 px-5 py-3.5">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="text-ui-base font-medium text-foreground">{j.title}</span>
                  <MetaBadge mono>{j.cron_expr}</MetaBadge>
                  <span className="text-ui-xs text-foreground-subtlest">
                    {isEn
                      ? `${j.runner_id} · ${j.run_count || 0} runs`
                      : `${j.runner_id} · 已运行 ${j.run_count || 0} 次`}
                  </span>
                </div>
                <p className="mt-0.5 truncate text-ui-sm text-foreground-subtle">{j.prompt}</p>
              </div>
              <button
                type="button"
                onClick={async () => {
                  if (j.id) {
                    await apiFetch(`/api/v1/cron/${encodeURIComponent(j.id)}`, {
                      method: 'DELETE',
                    });
                    await refreshRegistries();
                  }
                }}
                aria-label={isEn ? 'Delete automation' : '删除自动化'}
                className="flex size-7 shrink-0 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-destructive-surface hover:text-destructive-surface-fg"
              >
                <Trash2 className="size-4" />
              </button>
            </div>
          ))
        )}
      </SettingsGroupCard>
    </div>
  );
}


/* -------------------------------------------------------------------------- */
/* Long-term Memories                                                         */
/* -------------------------------------------------------------------------- */

export function MemoriesSettingsSection({ onFlash }: { onFlash: (msg: string) => void }) {
  const { locale, memories, selectedWorkspacePath, refreshRegistries } = useNexStore();
  const isEn = locale === 'en-US';

  const [scope, setScope] = useState<'global' | 'project'>('global');
  const [category, setCategory] = useState<'preference' | 'architecture' | 'convention' | 'fact'>('preference');
  const [title, setTitle] = useState('');
  const [content, setContent] = useState('');
  const [filterScope, setFilterScope] = useState<'all' | 'global' | 'project'>('all');

  const saveMemory = async () => {
    if (!title.trim() || !content.trim()) return;
    await apiFetch('/api/v1/memories', {
      method: 'POST',
      body: JSON.stringify({
        scope,
        workspace_path: scope === 'project' ? selectedWorkspacePath : '',
        category,
        title: title.trim(),
        content: content.trim(),
      }),
    });
    setTitle('');
    setContent('');
    await refreshRegistries();
    onFlash(isEn ? 'Memory saved and synced to runners' : '长期记忆已保存并同步至所有节点');
  };

  const removeMemory = async (id: string) => {
    await apiFetch(`/api/v1/memories/${encodeURIComponent(id)}`, { method: 'DELETE' });
    await refreshRegistries();
  };

  const visibleMemories = memories.filter((m) => filterScope === 'all' || m.scope === filterScope);

  return (
    <div className="space-y-6">
      <SettingsGroupCard>
        <div className="grid grid-cols-1 gap-4 px-5 py-4 sm:grid-cols-3">
          <Field label={isEn ? 'Scope' : '作用域'}>
            <select
              value={scope}
              onChange={(e) => setScope(e.target.value as 'global' | 'project')}
              className={settingsSelectClass}
            >
              <option value="global">{isEn ? 'Global (all workspaces)' : '全局（所有项目生效）'}</option>
              <option value="project">{isEn ? 'Current project' : '当前项目工作区'}</option>
            </select>
          </Field>
          <Field label={isEn ? 'Category' : '分类'}>
            <select
              value={category}
              onChange={(e) => setCategory(e.target.value as 'preference' | 'architecture' | 'convention' | 'fact')}
              className={settingsSelectClass}
            >
              <option value="preference">{isEn ? 'Preference' : '个人偏好'}</option>
              <option value="convention">{isEn ? 'Convention' : '编码规范'}</option>
              <option value="architecture">{isEn ? 'Architecture' : '架构决策'}</option>
              <option value="fact">{isEn ? 'Fact' : '项目事实'}</option>
            </select>
          </Field>
          <Field label={isEn ? 'Title' : '标题'}>
            <input
              data-testid="memory-title-input"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder={isEn ? 'e.g. Prefer CGO_ENABLED=0' : '例如：强制纯 Go 静态构建'}
              className={settingsInputClass}
            />
          </Field>
        </div>

        <div className="space-y-3 border-t border-border px-5 py-4">
          <Field label={isEn ? 'Memory content' : '记忆内容'}>
            <CodeEditorWithHighlight
              testId="memory-content-editor"
              rows={4}
              language="markdown"
              value={content}
              onChange={setContent}
              placeholder={
                isEn
                  ? 'Concise fact or preference recalled automatically in future sessions…'
                  : '填写希望在后续对话自动召回的偏好、约定或项目事实…'
              }
            />
          </Field>
          <div className="flex justify-end">
            <SaveButton label={isEn ? 'Save memory' : '保存记忆'} onClick={() => void saveMemory()} />
          </div>
        </div>
      </SettingsGroupCard>

      <div className="flex flex-wrap items-center justify-between gap-2">
        <SectionHeading
          title={isEn ? `Saved memories (${visibleMemories.length})` : `已保存记忆 (${visibleMemories.length})`}
        />
        <div className="inline-flex rounded-lg border border-border bg-surface p-0.5 text-ui-xs">
          {(['all', 'global', 'project'] as const).map((sc) => (
            <button
              key={sc}
              type="button"
              onClick={() => setFilterScope(sc)}
              className={`rounded-md px-2.5 py-1 font-medium transition-colors ${
                filterScope === sc ? 'bg-popover text-foreground shadow-xs' : 'text-foreground-subtle hover:text-foreground'
              }`}
            >
              {sc === 'all'
                ? isEn
                  ? 'All'
                  : '全部'
                : sc === 'global'
                  ? isEn
                    ? 'Global'
                    : '全局'
                  : isEn
                    ? 'Project'
                    : '项目'}
            </button>
          ))}
        </div>
      </div>

      <SettingsGroupCard>
        {visibleMemories.length === 0 ? (
          <EmptyRow>
            {isEn
              ? 'No memories saved yet. Add one above or ask Nex to remember something via memory_save.'
              : '暂无长期记忆。可在上方添加，或在对话中直接让 Nex 记住你的偏好（调用 memory_save）。'}
          </EmptyRow>
        ) : (
          visibleMemories.map((m) => (
            <div key={m.id} className="flex items-start justify-between gap-3 px-5 py-3.5">
              <div className="min-w-0 space-y-1">
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="text-ui-base font-medium text-foreground">{m.title}</span>
                  <MetaBadge mono>{m.scope}</MetaBadge>
                  <MetaBadge mono>{m.category}</MetaBadge>
                </div>
                <p className="whitespace-pre-wrap text-ui-sm text-foreground-subtle">{m.content}</p>
                {m.workspace_path ? (
                  <p className="font-mono text-ui-xs text-foreground-subtlest">{m.workspace_path}</p>
                ) : null}
              </div>
              <button
                type="button"
                onClick={() => void removeMemory(m.id)}
                aria-label={isEn ? 'Delete memory' : '删除记忆'}
                className="flex size-7 shrink-0 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-destructive-surface hover:text-destructive-surface-fg"
              >
                <Trash2 className="size-4" />
              </button>
            </div>
          ))
        )}
      </SettingsGroupCard>
    </div>
  );
}
