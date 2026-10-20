import type { AgentSession } from "@earendil-works/pi-coding-agent";
import { getSupportedThinkingLevels } from "@earendil-works/pi-ai";
import type { Model } from "@earendil-works/pi-ai";
import type { EngineModelState, EngineUsage } from "../types.js";

export const EMPTY_USAGE: EngineUsage = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };

export function selectionFor(session: AgentSession): EngineModelState | null {
  const model = session.model;
  if (!model) return null;
  const levels = getSupportedThinkingLevels(model);
  return {
    providerId: model.provider,
    modelId: model.id,
    displayName: model.name,
    thought: session.thinkingLevel,
    thoughtLevels: levels.length > 0 ? levels : ["off"],
  };
}

export function defaultThinking(model: Model<never>): string {
  const levels = getSupportedThinkingLevels(model);
  if (levels.length === 0) return "off";
  if (levels.includes("medium")) return "medium";
  return levels[levels.length - 1] ?? "off";
}

export function defaultTitle(text: string): string {
  const trimmed = text.trim().replace(/\s+/gu, " ");
  return trimmed.length > 60 ? `${trimmed.slice(0, 60)}…` : trimmed;
}

export function usageFromSession(session: AgentSession): EngineUsage {
  const stats = session.getSessionStats();
  return {
    input: stats.tokens.input,
    output: stats.tokens.output,
    cacheRead: stats.tokens.cacheRead,
    cacheWrite: stats.tokens.cacheWrite,
  };
}
