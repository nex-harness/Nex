import type {
  ExtensionFactory,
  InlineExtension,
  ToolCallEvent,
  ToolCallEventResult,
} from "@earendil-works/pi-coding-agent";
import type { PermissionGate } from "../permissions/gate.js";

export const PERMISSION_GATE_EXTENSION_NAME = "nex-permission-gate";

/**
 * Inline pi extension that routes every `tool_call` through the session's gate.
 *
 * pi emits `tool_execution_start` before this hook runs, so the projection already has a
 * row when the approval card appears. The handler may await indefinitely; the gate
 * settles on a user decision or on the run's abort signal.
 */
export function createPermissionGateExtension(gate: PermissionGate): InlineExtension {
  const factory: ExtensionFactory = (pi) => {
    pi.on(
      "tool_call",
      async (event: ToolCallEvent, ctx): Promise<ToolCallEventResult | undefined> => {
        const decision = await gate.authorize({
          toolCallId: event.toolCallId,
          toolName: event.toolName,
          input: event.input,
          signal: ctx.signal,
        });
        return decision.block
          ? { block: true, ...(decision.reason ? { reason: decision.reason } : {}) }
          : undefined;
      },
    );
  };
  return { name: PERMISSION_GATE_EXTENSION_NAME, factory };
}
