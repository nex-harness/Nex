/**
 * P2 `/` menu and skill catalog from pi's loaded resources
 * (docs/next/config-mcp-skills.md P2 item 4).
 *
 * - `nex-agent.getSkillReferenceCatalog` → the composer's skill suggestions
 *   (`$` / `/` panels). pi's own discovery decides the list in fallback mode
 *   (`~/.pi/agent/skills`, `.pi/skills`, `.agents/skills`); the server's bundles
 *   are added by `additionalSkillPaths` in server mode.
 * - `nex-session.readWorkspacePresentation.slashCommands` → prompt templates and
 *   `skill:<name>` commands, the same list pi's TUI builds
 *   (`interactive-mode.ts`): templates first, then skill commands when
 *   `enableSkillCommands` is on. pi expands both at prompt time, so nothing else
 *   is needed for `/skill:name` to work.
 */
import type { Engine } from "../engine/types.js";
import type { PiResourceCatalog } from "../engine/pi/piResources.js";

export interface SlashCommandInfo {
  name: string;
  description: string;
  inputHint?: string;
  source: "builtin" | "custom";
}

/** Composer skill catalog entry (packages/shared `nexSkillReferenceCatalogEntrySchema`). */
export interface SkillReferenceEntry {
  id: string;
  name: string;
  description: string;
  path: string;
  scope: "workspace" | "user" | "plugin";
  enabled: true;
}

/** Built-in commands the runner serves itself; always first in the `/` menu. */
export const RUNNER_BUILTIN_SLASH_COMMANDS: readonly SlashCommandInfo[] = [
  {
    name: "compact",
    description: "Compact the current conversation with optional instructions.",
    inputHint: "/compact [instructions]",
    source: "builtin",
  },
];

export function skillCatalogFrom(resources: PiResourceCatalog): SkillReferenceEntry[] {
  const seen = new Set<string>();
  const out: SkillReferenceEntry[] = [];
  for (const skill of resources.skills) {
    const key = skill.name.trim().toLowerCase();
    if (!key || seen.has(key)) continue;
    seen.add(key);
    // 修复：UI 的 `/` 与 `$` 面板只放行 `glm:` 前缀 id、`.nex/skills` 路径或 plugin scope 的技能
    // （packages/ui/src/lib/skillSourceFilter.ts）。pi 的技能在 `~/.pi/agent/skills` 或服务端
    // bundle 目录下，用 `skill:<name>` 作 id 时全部被过滤，面板显示 "No matching skills"。
    // 与旧 CLI 的 toReferenceCatalogEntry 一致：`glm:<scope>:<path>`，路径区分同名不同来源。
    out.push({
      id: `glm:user:${skill.filePath}`,
      name: skill.name,
      description: skill.description,
      path: skill.filePath,
      scope: "user",
      enabled: true,
    });
  }
  return out;
}

export function slashCommandsFrom(
  resources: PiResourceCatalog,
  options: { skillCommands: boolean } = { skillCommands: true },
): SlashCommandInfo[] {
  const commands: SlashCommandInfo[] = resources.prompts.map((prompt) => ({
    name: prompt.name,
    description: prompt.description,
    ...(prompt.argumentHint ? { inputHint: prompt.argumentHint } : {}),
    source: "custom" as const,
  }));
  if (!options.skillCommands) return commands;
  const templateNames = new Set(commands.map((command) => command.name));
  for (const skill of resources.skills) {
    const name = `skill:${skill.name}`;
    if (templateNames.has(name)) continue;
    commands.push({ name, description: skill.description, source: "custom" });
  }
  return commands;
}

/**
 * The workspace's full `/` list: the runner's prompt templates and `/skill:`
 * commands, plus the server doc's skills (P2, forwarded to this runner) so the
 * menu also offers them before any new session materializes them.
 */
export function composeSlashCommands(options: {
  slashCommands?: ReadonlyArray<{
    name: string;
    description?: string;
    source?: "builtin" | "custom";
  }>;
  resources: PiResourceCatalog;
  skillCommands: boolean;
}): SlashCommandInfo[] {
  // 修复：P2 之前 slashCommands 为空，`/compact` 回车时按原文发送并被 UI 解析成 compact 命令。
  // 现在列表里有 pi 的技能/模板却没有内置命令，`/` 面板对 "compact" 模糊匹配会选中某个技能
  // （例如 skill:Reverse-Engineer-Anything）并插入编辑器，compact 从未发出。
  // 与旧 CLI 的 APP_PROTOCOL_VISIBLE_BUILTIN_SLASH_COMMAND_NAMES 一致，内置命令始终排在最前；
  // 只列 runner 真正支持的命令（goal/workflow/init 尚未实现，不列出）。
  const out: SlashCommandInfo[] = RUNNER_BUILTIN_SLASH_COMMANDS.map((command) => ({ ...command }));
  const seen = new Set<string>(out.map((command) => command.name));
  for (const command of options.slashCommands ?? []) {
    const name = command.name.trim();
    if (!name || seen.has(name)) continue;
    seen.add(name);
    out.push({
      name,
      description: command.description ?? "",
      source: command.source ?? "custom",
    });
  }
  for (const command of slashCommandsFrom(options.resources, {
    skillCommands: options.skillCommands,
  })) {
    if (seen.has(command.name)) continue;
    seen.add(command.name);
    out.push(command);
  }
  return out;
}

/** Loads the resources once per call (the engine caches nothing; pi reloads are cheap). */
export async function loadCatalog(
  engine: Engine,
  workspacePath: string,
): Promise<{ resources: PiResourceCatalog; skillCommands: boolean }> {
  const resources = await engine.piResources(workspacePath);
  return { resources, skillCommands: engine.appliedConfig().skillsEnabled };
}
