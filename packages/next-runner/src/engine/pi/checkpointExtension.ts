import type {
  ExtensionFactory,
  InlineExtension,
  ToolCallEvent,
  ToolCallEventResult,
  ToolResultEvent,
} from "@earendil-works/pi-coding-agent";
import type { PiCheckpointHook } from "./piCheckpointHook.js";

/**
 * Inline pi extension forwarding the two hooks the checkpoint service needs (spec B). It
 * loads after the permission gate in the `tool_call` chain, so a denied call never snapshots
 * the workspace, and `tool_result` fires only for calls that actually executed.
 */
export function createCheckpointExtension(hook: PiCheckpointHook): InlineExtension {
  const factory: ExtensionFactory = (pi) => {
    pi.on("tool_call", async (event: ToolCallEvent): Promise<ToolCallEventResult | undefined> => {
      await hook.beforeToolCall(event.toolName, event.input);
      return undefined;
    });
    pi.on("tool_result", async (event: ToolResultEvent) => {
      await hook.afterToolResult(event);
    });
  };
  return { name: "nex-checkpoints", factory };
}
