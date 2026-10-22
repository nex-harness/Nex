import type { AgentSessionEvent } from "@earendil-works/pi-coding-agent";
import type { EngineEvent } from "../types.js";
import { findMessageEntryId } from "./events.js";

/**
 * Reports the persisted session entry id of a user/assistant message
 * (`EngineEvent.message_entry`), which is what turns row identity into `pi:<entryId>`.
 *
 * pi emits `message_end` to subscribers *before* it calls `appendMessage`, and it emits
 * `entry_appended` only for custom/boundary/compaction entries. The append happens
 * synchronously right after our listener returns, so a microtask sees the entry while
 * ordering against later events is preserved (no macrotask interleaving).
 */
export function reportMessageEntry(
  event: AgentSessionEvent,
  resolve: () =>
    | { session: { sessionManager: Parameters<typeof findMessageEntryId>[0] } }
    | undefined,
  emit: (event: EngineEvent) => void,
): void {
  if (event.type !== "message_end") return;
  const role = event.message.role;
  if (role !== "user" && role !== "assistant") return;
  const message = event.message;
  queueMicrotask(() => {
    const open = resolve();
    if (!open) return;
    const entryId = findMessageEntryId(open.session.sessionManager, message);
    if (!entryId) return;
    emit({ type: "message_entry", at: Date.now(), entryId, role });
  });
}
