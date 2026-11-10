import { z } from "zod";
import type { CommandAgentSource } from "./command-types.js";
import type { NexProvider } from "./nex-task-types-core.js";

export const NEX_AGENT_PROVIDER = "glm" satisfies NexProvider;
export const NEX_AGENT_PROVIDER_LABEL = "Nex Agent";
export const NEX_COMMAND_AGENT_SOURCE = "nexAgent" satisfies CommandAgentSource;

/**
 * 解析持久化/relay 数据时的 provider 归一 schema：
 * 已移除的第三方 provider 历史值仅作为归一化输入保留，统一收敛为 glm；
 * 其它非法值保持拒绝，与收窄前的校验边界一致。
 */
export const legacyNexProviderSchema = z
  .enum(["claude", "opencode", "gemini", "codex", "glm"])
  .transform((): typeof NEX_AGENT_PROVIDER => NEX_AGENT_PROVIDER);

export const NEX_ENABLED_AGENT_PROVIDERS = [
  NEX_AGENT_PROVIDER,
] as const satisfies readonly NexProvider[];

export const NEX_COMMAND_AGENT_SOURCES = [
  NEX_COMMAND_AGENT_SOURCE,
] as const satisfies readonly CommandAgentSource[];

export function normalizeAgentProviderToNexAgent(
  _provider?: NexProvider | null,
): NexProvider {
  return NEX_AGENT_PROVIDER;
}

export function normalizeAgentProvidersToNexAgent(
  _providers?: ReadonlyArray<NexProvider> | null,
): NexProvider[] {
  return [...NEX_ENABLED_AGENT_PROVIDERS];
}

export function normalizeCommandAgentSourceToNexAgent(
  _agentSource?: CommandAgentSource | null,
): CommandAgentSource {
  return NEX_COMMAND_AGENT_SOURCE;
}

export function isNexAgentProvider(
  provider: NexProvider | null | undefined,
): provider is typeof NEX_AGENT_PROVIDER {
  return provider === NEX_AGENT_PROVIDER;
}
