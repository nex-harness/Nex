// Package windowcontroller is the Go port of IWindowControllerService (step 3.10.6):
// the window-scoped sidebar list controller.
//
// The controller is NOT the task store. It owns no rows: every online query
// rebuilds its projection from the resolved sources' task indexes, and a
// disconnected source keeps only its last successful in-memory snapshot (the
// TS calls this a frozen projection). The rules this port preserves, each with
// the TS comment that documents it:
//
//   - Two topics only. `controller/tasks-index` carries task rows and
//     `controller/workspaces` carries per-source availability facts. They have
//     SEPARATE logEpoch/seq cursors, because a workspace reconnect invalidates
//     the task rows while the workspace fact converges separately.
//   - One log epoch per topic, one monotonic seq. A delta frame advances the
//     cursor by one; an empty delta batch publishes NOTHING (publishTaskDeltas
//     early-returns), so a no-op refresh cannot open a gap for the consumer.
//   - A source's snapshot is replaced, never merged: the reader cannot see a
//     half-replaced projection. The reconnect replacement publishes the old
//     rows' removals AND the new rows' upserts in ONE delta frame, so a consumer
//     never observes an intermediate empty projection (the remoteSessionId
//     changes on reconnect, which is exactly when this matters).
//   - Live session facts (the sessions-index overlay) live only in Host memory.
//     An overlay's absence means "no provable runtime": a running/interaction
//     status persisted in SQLite may be from the previous process exit and must
//     NOT be re-interpreted as a current running/waiting fact. defaultLiveStatus
//     therefore reduces a meta to completed/error/idle, never running.
//   - A remote task address REQUIRES workspaceIdentity, and an offline source's
//     connectionState must be disconnected/failed. Both are schema refinements
//     in controller.ts and are reproduced here at the boundary that can violate
//     them, not silently emitted.
//   - Membership is the projection's own state (pinned/archived/active), not a
//     re-derivation: matchesTaskListMembershipKind is the single shared
//     predicate between the query and the projection.
//
// Per-connection attachment is the whole reason this service is not a plain
// singleton: the runtime is shared process-wide, but every accepted connection
// gets its OWN attachment with its OWN subscription set and frame emitter. Two
// connections therefore never share a subscription id, a cursor, or a delivery
// buffer, and disposing one connection's attachment cannot remove another's rows
// or frames. This mirrors packages/server/src/windowController.ts, where the
// process holds one runtime and each WebSocket calls runtime.createAttachmentService().
package windowcontroller
