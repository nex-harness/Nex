/**
 * Runtime types of one applied config generation (docs/next/config-mcp-skills.md
 * decision 5). Split from `runtimeConfig.ts` for the file-size cap.
 */
import type { McpGeneration } from "./mcpConfig.js";
import type { BundleRefWire, ConfigIssue, WireBundle, WireModel } from "./wireFrame.js";

export interface RuntimeProvider {
  id: string;
  name: string;
  api: string;
  baseUrl: string;
  headers: Record<string, string>;
  /** models.json `apiKey` reference: `$ENV_VAR` (the runner exports the value). */
  envVar?: string;
  modelsJsonModels: ReadonlyArray<Record<string, unknown>>;
  /** The doc's per-model enabled flags (pi has no per-model disable). */
  enabledModels: Set<string>;
  models: WireModel[];
  configured: boolean;
  error?: string;
}

export interface RuntimeConfig {
  readonly version: number;
  readonly owned: boolean;
  /** `fallback`: no server providers — the runner keeps its own ~/.pi/agent. */
  readonly mode: "fallback" | "server";
  readonly providers: readonly RuntimeProvider[];
  readonly settings: Readonly<Record<string, unknown>>;
  /** Secret values in this generation (provider id → key), never persisted. */
  readonly credentials: ReadonlyMap<string, string>;
  readonly issues: readonly ConfigIssue[];
  /** Env var name holding each provider key (exported to tools, never logged). */
  readonly env: Readonly<Record<string, string>>;
  /** MCP generation (P1): doc-owned list or pi's own discovery. */
  readonly mcp: McpGeneration;
  /** Bundles to materialize (P2), in push order (name, kind and files). */
  readonly bundles: readonly WireBundle[];
  /** Skill bundle refs (enabled only). */
  readonly skills: readonly BundleRefWire[];
  /** Prompt template bundle refs (enabled only). */
  readonly prompts: readonly BundleRefWire[];
  /** Doc warnings (informational). */
  readonly warnings: readonly string[];
  /** pi `enableSkillCommands` (default true): list `/skill:<name>` in the menu. */
  readonly skillsEnabled: boolean;
}
