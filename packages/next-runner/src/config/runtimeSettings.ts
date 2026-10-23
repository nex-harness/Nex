/**
 * Settings and applied-cache projections of a RuntimeConfig (split out of
 * `runtimeConfig.ts` for the file budget).
 */
import type { RuntimeConfig } from "./runtimeConfig.js";

/** Settings keys pi understands as a `Partial<Settings>` (kept narrow on purpose). */
export const SETTINGS_KEYS = new Set([
  "defaultProvider",
  "defaultModel",
  "defaultThinkingLevel",
  "enabledModels",
  "enableSkillCommands",
  "defaultTools",
]);

export function pickSettings(settings: Readonly<Record<string, unknown>>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(settings)) {
    if (SETTINGS_KEYS.has(key)) out[key] = value;
  }
  return out;
}

/** The persisted applied-cache (no secrets). */
export interface AppliedCache {
  version: number;
  appliedAt: number;
  mode: "fallback" | "server";
  providers: Array<{ id: string; api: string; baseUrl: string; models: string[] }>;
}

export function appliedCacheFor(config: RuntimeConfig): AppliedCache {
  return {
    version: config.version,
    appliedAt: Date.now(),
    mode: config.mode,
    providers: config.providers.map((provider) => ({
      id: provider.id,
      api: provider.api,
      baseUrl: provider.baseUrl,
      models: provider.models.map((model) => model.id),
    })),
  };
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function asString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

export function asStringRecord(value: Record<string, unknown>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [key, entry] of Object.entries(value)) {
    if (typeof entry === "string") out[key] = entry;
  }
  return out;
}

export function positive(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) && value > 0
    ? Math.floor(value)
    : undefined;
}
