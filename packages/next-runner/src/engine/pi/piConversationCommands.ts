import type { PiCompactOps } from "./piCompactOps.js";
import {
  compactStateOf,
  resolveRewindTarget,
  runFork,
  runManualCompact,
  type ConversationOpsDeps,
} from "./piConversationOps.js";
import type {
  EngineCompactOutcome,
  EngineCompactState,
  EngineForkOutcome,
  EngineForkRequest,
  EngineRewindOutcome,
  EngineRewindRequest,
  EngineRewindTarget,
} from "../types.js";

/**
 * Conversation commands of the engine (rewind / compact / fork), bound to the engine's
 * conversation ops. Kept out of `piEngine.ts`; the rewind body (attachment mapping + branch
 * cut + re-prompt) is supplied by the engine because it owns the attachment host.
 */
export type ConversationCommands = ReturnType<typeof createConversationCommands>;

export function createConversationCommands(deps: {
  ops: () => ConversationOpsDeps;
  compactionEntries: Map<string, string>;
  compactOps: PiCompactOps;
  rewindPrompt: (sessionId: string, request: EngineRewindRequest) => Promise<EngineRewindOutcome>;
}) {
  return {
    compactState(sessionId: string): EngineCompactState {
      return compactStateOf(deps.ops(), sessionId);
    },
    takeCompactOutcomes(sessionId: string): EngineCompactOutcome[] {
      return deps.compactOps.takeOutcomes(sessionId);
    },
    compact(sessionId: string, sourceCommandId: string): Promise<EngineCompactOutcome> {
      deps.compactionEntries.delete(sessionId);
      return runManualCompact(deps.ops(), sessionId, sourceCommandId).finally(() =>
        deps.compactionEntries.delete(sessionId),
      );
    },
    rewind(sessionId: string, request: EngineRewindRequest): Promise<EngineRewindOutcome> {
      return deps.rewindPrompt(sessionId, request);
    },
    userMessageBefore(sessionId: string, entryId: string): Promise<EngineRewindTarget | null> {
      return Promise.resolve(resolveRewindTarget(deps.ops(), sessionId, entryId));
    },
    forkAssistant(
      workspacePath: string,
      sessionId: string,
      request: EngineForkRequest,
    ): Promise<EngineForkOutcome> {
      return runFork(deps.ops(), workspacePath, sessionId, request);
    },
  };
}
