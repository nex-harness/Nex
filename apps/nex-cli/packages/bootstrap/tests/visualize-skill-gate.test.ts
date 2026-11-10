import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createConfig } from "@nex/adapters/config";
import { createNodeSkillAdapter } from "@nex/adapters/skills";
import type { Logger } from "@nex/contracts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createWorkflowChildSkillPort } from "../src/app/script-workflow-child-runtime.js";
import { resolveStartupPlugins } from "../src/app/startup-marks.js";
import type { NexApp, NexAppOptions } from "../src/app/types.js";
import { inspectNexSkill, listNexSkills } from "../src/skills.js";
import { StartupTimer } from "../src/startup-logging.js";
import { getSkillReferenceCatalog } from "../src/nex-protocol/skill-reference-catalog.js";
import type { NexProtocolAgentServerContext } from "../src/nex-protocol/server-types.js";
import { createWorkspaceNexApp } from "../src/nex-protocol/workspace-model-runtime.js";

const VISUALIZE_PLUGIN_ID = "visualize@zcode-plugins-official";
const VISUALIZE_SKILL = "visualize:visualize";
const logger = {
  debug() {},
  info() {},
  warn() {},
  error() {},
} as unknown as Logger;

describe("visualize protocol-only skill discovery", () => {
  let root: string;
  let env: NodeJS.ProcessEnv;

  beforeEach(async () => {
    root = await mkdtemp(join(tmpdir(), "nex-visualize-gate-"));
    env = { NEX_STORAGE_DIR: join(root, "storage") };
  });

  afterEach(async () => {
    await rm(root, { recursive: true, force: true });
  });

  it.each([undefined, false, true])(
    "list and inspect agree with includeVisualize=%s",
    async (includeVisualize) => {
      const options = { env, includeVisualize, skipUserConfig: true, workingDirectory: root };
      const outcome = await listNexSkills(options);
      expect(outcome.skills.some((skill) => skill.pluginId === VISUALIZE_PLUGIN_ID)).toBe(
        includeVisualize === true,
      );
      expect(outcome.skills.some((skill) => skill.name === "pdf")).toBe(true);
      for (const name of ["visualize", VISUALIZE_SKILL]) {
        if (includeVisualize) {
          const result = await inspectNexSkill({ ...options, name });
          expect(result.skill.metadata.pluginId).toBe(VISUALIZE_PLUGIN_ID);
          expect(result.skill.content).toContain("Inline HTML output contract");
        } else {
          await expect(inspectNexSkill({ ...options, name })).rejects.toThrow("Skill not found:");
        }
      }
    },
  );

  it.each([undefined, false, true])(
    "startup and workflow child agree with includeVisualize=%s",
    async (includeVisualize) => {
      const configResult = createConfig({ env, skipUserConfig: true, workingDirectory: root });
      const appOptions = { includeVisualize } as NexAppOptions;
      const plugins = resolveStartupPlugins({
        cliStorageRoot: join(root, "storage", "cli"),
        configResult,
        env,
        options: appOptions,
        startupTimer: new StartupTimer(logger, {}),
        workingDirectory: root,
      });
      const parentPort = createNodeSkillAdapter({ extraResolvedRoots: plugins.skillRoots });
      const childPort = createWorkflowChildSkillPort({
        appOptions,
        configResult,
        pluginSkillRoots: plugins.skillRoots,
      });
      for (const port of [parentPort, childPort!]) {
        const outcome = await port.discoverSkills({ workingDirectory: root });
        expect(outcome.skills.some((skill) => skill.pluginId === VISUALIZE_PLUGIN_ID)).toBe(
          includeVisualize === true,
        );
        expect(outcome.skills.some((skill) => skill.name === "pdf")).toBe(true);
      }
    },
  );

  it("keeps project skills with the same name available in CLI", async () => {
    const skillDir = join(root, ".agents", "skills", "visualize");
    await mkdir(skillDir, { recursive: true });
    await writeFile(
      join(skillDir, "SKILL.md"),
      "---\nname: visualize\ndescription: Project visualization.\n---\nProject skill.",
    );
    const result = await inspectNexSkill({
      env,
      skipUserConfig: true,
      workingDirectory: root,
      name: "visualize",
    });
    expect(result.skill.metadata.source).toBe("agents");
    expect(result.skill.content).toBe("Project skill.");
  });

  it("does not override explicit plugin disablement in protocol mode", async () => {
    const configPath = join(root, "config.json");
    await writeFile(
      configPath,
      JSON.stringify({ plugins: { enabledPlugins: { [VISUALIZE_PLUGIN_ID]: false } } }),
    );
    const options = {
      env,
      includeVisualize: true,
      userConfigPath: configPath,
      workingDirectory: root,
    };
    const outcome = await listNexSkills(options);
    expect(outcome.skills.some((skill) => skill.pluginId === VISUALIZE_PLUGIN_ID)).toBe(false);
    await expect(inspectNexSkill({ ...options, name: VISUALIZE_SKILL })).rejects.toThrow(
      "Skill not found:",
    );
  });

  it("opts protocol apps in and keeps their draft catalog consistent", async () => {
    const createNexApp = vi.fn(() => ({}) as NexApp);
    const context = {
      deps: { createNexApp, env },
      sessions: new Map(),
    } as unknown as NexProtocolAgentServerContext;
    const workspace = { workspacePath: root, workspaceKey: root };
    await createWorkspaceNexApp(context, workspace, {
      providerRuntimeHeadersPort: {} as NexAppOptions["providerRuntimeHeadersPort"],
    });
    expect(createNexApp).toHaveBeenCalledWith(
      expect.objectContaining({ includeVisualize: true }),
    );
    const catalog = await getSkillReferenceCatalog(context, { workspace });
    expect(catalog.skills).toContainEqual(
      expect.objectContaining({ name: "visualize", pluginName: "visualize" }),
    );
  });
});
