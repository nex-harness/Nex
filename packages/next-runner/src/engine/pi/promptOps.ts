import type { ModelRuntime } from "@earendil-works/pi-coding-agent";
import type { ModelSelection } from "@nex/shared";
import type { Logger } from "../../logger.js";
import type { SessionMetaStore } from "./metaStore.js";
import type { OpenSession } from "./sessionLifecycle.js";
import { applyModelSelection, startPromptRun } from "./piSessionOps.js";

export interface PromptOpsDeps {
  metadata: SessionMetaStore;
  logger: Logger;
  sessionsChanged: (workspacePath: string) => void;
  runtime: () => ModelRuntime;
  configOps: { runModelMutation<T>(sessionId: string, operation: () => Promise<T>): Promise<T> };
}

/**
 * Prompt start + per-submission model selection. Extracted from `PiEngine` for the file
 * budget; ownership is unchanged (the engine is the only caller).
 */
export async function startEnginePrompt(
  deps: PromptOpsDeps,
  open: OpenSession,
  workspacePath: string,
  sessionId: string,
  text: string,
  requestId?: string,
  attachments?: {
    images: import("@earendil-works/pi-ai").ImageContent[];
    streamingBehavior: "followUp" | "steer";
    /** Model prompt text when it differs from the row text (attachment block, spec A). */
    promptText?: string;
    /** 这一轮是否无人值守（automation sendText 进既有会话）。 */
    unattended?: boolean;
    /** Runs right after pi accepted the prompt (attachment input registration). */
    onStart?: (session: import("@earendil-works/pi-coding-agent").AgentSession) => void;
  },
): Promise<string> {
  const inputId = requestId ?? `input_${Date.now()}_${Math.random().toString(36).slice(2, 10)}`;
  // 修复依据（docs/next/automations-views-search.md §A）：无人值守是“轮”的属性，
  // 不是会话属性。每次提示开跑前都按本次提交重设 gate 的单轮标记：automation 那一轮
  // ask 立即 deny；用户随后自己的输入（含 rewind 重新提示）回到有人值守。
  open.gate.setTurnUnattended(attachments?.unattended === true);
  await deps.metadata.upsert(workspacePath, sessionId, {
    lastActivityAt: Date.now(),
    phase: "running",
  });
  startPromptRun(
    {
      metadata: deps.metadata,
      logger: deps.logger,
      sessionsChanged: deps.sessionsChanged,
    },
    open,
    workspacePath,
    sessionId,
    attachments?.promptText ?? text,
    attachments
      ? {
          images: attachments.images,
          streamingBehavior: attachments.streamingBehavior,
          ...(attachments.onStart ? { onStart: attachments.onStart } : {}),
        }
      : {},
  );
  deps.sessionsChanged(workspacePath);
  return inputId;
}

export async function applyEngineSelection(
  deps: PromptOpsDeps,
  open: OpenSession,
  sessionId: string,
  selection: ModelSelection,
): Promise<void> {
  const runtime = deps.runtime();
  await deps.configOps.runModelMutation(sessionId, () =>
    applyModelSelection(open.session, selection, (providerId, modelId) =>
      runtime.getModel(providerId, modelId),
    ),
  );
}
