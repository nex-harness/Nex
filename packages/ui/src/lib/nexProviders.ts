import {
  NEX_AGENT_PROVIDER,
  NEX_AGENT_PROVIDER_LABEL,
  type NexProvider,
} from "@nex/shared";

export const NEX_AGENT_PROVIDERS: NexProvider[] = [NEX_AGENT_PROVIDER];

export const NEX_AGENT_PROVIDER_LABELS: Record<NexProvider, string> = {
  glm: NEX_AGENT_PROVIDER_LABEL,
};

export function supportsNexSessionFork(provider: NexProvider): boolean {
  void provider;
  // 功能开关：fork 入口恢复对所有 Nex provider 开放；实际可操作性仍由 task 状态、
  // assistant 目标和运行中保护 guard 控制，避免把 active/failed 分支暴露为可 fork。
  return true;
}

export function shouldHideNexSessionForkAction(provider: NexProvider): boolean {
  return !supportsNexSessionFork(provider);
}
