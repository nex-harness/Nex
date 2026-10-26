import type { ModelRuntime, SettingsManager } from "@earendil-works/pi-coding-agent";
import type { AttachmentRegistry } from "../../attachments/registry.js";
import type { Logger } from "../../logger.js";
import type { TitleCompletionRequest, TitleGenerationSettings } from "./titleGenerator.js";

export interface PiEngineOptions {
  dataDir: string;
  logger: Logger;
  /**
   * Server-owned runtime config (docs/next/config-mcp-skills.md). When set, new
   * sessions use the latest applied generation's ModelRuntime and
   * SettingsManager; `createModelRuntime` then only serves the fallback mode.
   */
  runtimeConfig?: import("../../config/applyRuntimeConfig.js").RuntimeConfigManager;
  /** Test seam: a faux-backed ModelRuntime instead of `~/.pi/agent` credentials. */
  createModelRuntime?: () => Promise<ModelRuntime>;
  /** Test seams: in-memory settings, title settings and a fake completion. */
  createSettingsManager?: (workspacePath: string) => SettingsManager;
  titleSettings?: TitleGenerationSettings;
  completeTitle?: (request: TitleCompletionRequest) => Promise<string>;
  /** Test seam: a `git` binary other than PATH's (or a missing-path probe). */
  gitExecutable?: string;
  /** Test seam: threshold above which the full-tree turn snapshot is skipped. */
  maxCheckpointFiles?: number;
  /** Shared attachment authority (the v4 runtime owns refs; defaults to a private one). */
  attachments?: AttachmentRegistry;
  /** Test seam: per-session attachment disk quota. */
  attachmentSessionQuotaBytes?: number;
  /**
   * Workspaces this runner serves (P1). The hidden MCP status session opens in the
   * first one; absent (tests without workspaces), status queries read sessions only.
   */
  workspacePaths?: ReadonlyArray<string>;
}
