import { clampThinkingLevel } from "@earendil-works/pi-ai";
import type { Model } from "@earendil-works/pi-ai";
import type { AgentSession } from "@earendil-works/pi-coding-agent";
import type {
  EngineFollowupMode,
  EngineModelState,
  SwitchModelConfigInput,
  SwitchModelConfigOutcome,
} from "../types.js";
import { selectionFor } from "./sessionHelpers.js";

/**
 * Mid-session model / follow-up mode changes
 * (docs/next/conversation-features.md decisions 9; research §7).
 *
 * Ported semantics from the old CLI:
 * - `model-config.ts:76-140`: identical values → `noop config.unchanged`; unknown provider
 *   → `failed provider.notInRegistry`; a same-model thought change is still a real change.
 * - `steering.ts` / `queue.ts`: `setFollowupMode` only flips the config value; pi's own
 *   `setFollowUpMode` (drain granularity) is deliberately untouched.
 */

export const CONFIG_UNCHANGED = "config.unchanged";
export const PROVIDER_NOT_IN_REGISTRY = "provider.notInRegistry";

export interface ModelConfigOpsDeps {
  sessionFor: (sessionId: string) => AgentSession | undefined;
  getModel: (providerId: string, modelId: string) => Model<never> | undefined;
}

/**
 * `switchModelConfig` core. Returns the outcome without touching projection or metadata;
 * the caller (engine) persists the selection and appends the marker row.
 *
 * Serialization: pi's `setModel` is async and appends `model_change`; callers must not run
 * two switches concurrently for one session. `PiEngine` serializes per session.
 */
export async function applySwitchModelConfig(
  deps: ModelConfigOpsDeps,
  sessionId: string,
  input: SwitchModelConfigInput,
): Promise<SwitchModelConfigOutcome> {
  const session = deps.sessionFor(sessionId);
  if (!session) {
    return { status: "failed", reasonCode: "session.notOpen" };
  }
  const providerId = input.provider.trim();
  const modelId = input.model.trim();
  const requestedThought = input.thought.trim();

  const currentModel = session.model as Model<never> | undefined;
  const previous = currentModel
    ? { provider: currentModel.provider, model: currentModel.id }
    : undefined;
  const previousThought = session.thinkingLevel;
  const modelIdentityChanged =
    !currentModel || currentModel.provider !== providerId || currentModel.id !== modelId;
  const thoughtChanged = requestedThought.length > 0 && requestedThought !== previousThought;

  if (!modelIdentityChanged && !thoughtChanged) {
    const model = modelStateOf(session);
    return {
      status: "unchanged",
      reasonCode: CONFIG_UNCHANGED,
      ...(previous ? { previous } : {}),
      ...(model ? { model } : {}),
      modelIdentityChanged: false,
    };
  }

  if (modelIdentityChanged) {
    const target = deps.getModel(providerId, modelId);
    if (!target) {
      return {
        status: "failed",
        reasonCode: PROVIDER_NOT_IN_REGISTRY,
        ...(previous ? { previous } : {}),
      };
    }
    try {
      await session.setModel(target);
    } catch (error) {
      return {
        status: "failed",
        reasonCode: "model.setFailed",
        message: error instanceof Error ? error.message : String(error),
        ...(previous ? { previous } : {}),
      };
    }
    if (requestedThought.length > 0) {
      // pi clamps to the model's supported set; the requested level is the intent, the
      // effective level is what the projection reports.
      session.setThinkingLevel(clampThinkingLevel(target, requestedThought as never) as never);
    }
  } else if (requestedThought.length > 0) {
    // provider/model unchanged: the command is an explicit thought-level pin.
    const current = currentModel;
    if (!current) {
      return {
        status: "failed",
        reasonCode: PROVIDER_NOT_IN_REGISTRY,
        ...(previous ? { previous } : {}),
      };
    }
    session.setThinkingLevel(clampThinkingLevel(current, requestedThought as never) as never);
  }

  const model = modelStateOf(session);
  return {
    status: "changed",
    ...(model ? { model } : {}),
    modelIdentityChanged,
    ...(previous ? { previous } : {}),
  };
}

function modelStateOf(session: AgentSession): EngineModelState | null {
  return selectionFor(session);
}

/** `setFollowupMode` merely stores the value; nothing about pi changes. */
export function followupModePatch(mode: EngineFollowupMode): { followupMode: EngineFollowupMode } {
  return { followupMode: mode };
}
