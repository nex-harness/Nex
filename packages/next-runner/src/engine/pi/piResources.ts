/**
 * The pi resources a workspace's next session would see (P2 `/` menu and skill
 * catalog, docs/next/config-mcp-skills.md P2 item 4).
 *
 * A probe `DefaultResourceLoader` is reloaded with the same cwd, agent dir,
 * settings and bundle paths a new session gets, so pi's own discovery rules
 * decide what is visible: in fallback mode `~/.pi/agent/skills`,
 * `<cwd>/.pi/skills` and `.agents/skills`; in server mode those plus the
 * materialized bundles. Nothing here executes skill content.
 */
import {
  DefaultResourceLoader,
  getAgentDir,
  type SettingsManager,
} from "@earendil-works/pi-coding-agent";

export interface PiSkillInfo {
  name: string;
  description: string;
  filePath: string;
}

export interface PiPromptInfo {
  name: string;
  description: string;
  argumentHint?: string;
}

export interface PiResourceCatalog {
  skills: PiSkillInfo[];
  prompts: PiPromptInfo[];
}

export interface PiResourceCatalogOptions {
  workspacePath: string;
  settingsManager: SettingsManager;
  additionalSkillPaths?: ReadonlyArray<string>;
  additionalPromptTemplatePaths?: ReadonlyArray<string>;
}

/** Loads pi's skills and prompt templates with the session's resource rules. */
export async function loadPiResources(
  options: PiResourceCatalogOptions,
): Promise<PiResourceCatalog> {
  const loader = new DefaultResourceLoader({
    cwd: options.workspacePath,
    agentDir: getAgentDir(),
    settingsManager: options.settingsManager,
    ...(options.additionalSkillPaths?.length
      ? { additionalSkillPaths: [...options.additionalSkillPaths] }
      : {}),
    ...(options.additionalPromptTemplatePaths?.length
      ? { additionalPromptTemplatePaths: [...options.additionalPromptTemplatePaths] }
      : {}),
  });
  await loader.reload();
  const skills = loader.getSkills().skills.map((skill) => ({
    name: skill.name,
    description: skill.description,
    filePath: skill.filePath,
  }));
  const prompts = loader.getPrompts().prompts.map((prompt) => ({
    name: prompt.name,
    description: prompt.description ?? "",
    ...(prompt.argumentHint ? { argumentHint: prompt.argumentHint } : {}),
  }));
  return { skills, prompts };
}
