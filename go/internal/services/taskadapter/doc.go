// Package taskadapter is the Go port of nexTaskServiceAdapter.ts (step 3.10.5), plus
// the cross-host half of taskRealtimeBus.ts.
//
// The adapter is the legacy task facade: it owns the per-task host command queue the
// phone replayable leg submits into, the "which input run owns this task right now"
// bookkeeping, and the pending optimistic boundaries around a turn. The bus is the
// cross-host half: which Host may publish a task run's stream, and which Host must
// answer an owner command.
//
// Both halves are here on purpose. They are ONE decision seen from two ends: the bus
// refuses a stale cross-host command (STALE_TASK_OWNER_COMMAND), and the adapter
// refuses a stale local owner command with the same code. Splitting them across two
// packages would recreate the duplicate-state / two-write-paths arrangement AGENTS.md
// forbids.
//
// Every early-return that exists to protect ownership, staleness, routing or queue
// integrity is inventoried in docs/specs/go-rewrite/phase-3-guards.md, and every row
// there names the test in this package that pins it. A guard may not be dropped without
// a recorded decision in that file.
//
// The rules this package preserves, each with the TS comment that documents it:
//
//   - ownerRunId is the remote-control staleness boundary. A phone command carrying an
//     ownerRunId that is not the task's current active input run is refused
//     (STALE_TASK_OWNER_COMMAND); with no active run at all it is refused
//     NO_ACTIVE_TASK_OWNER. A command with NO ownerRunId is the desktop-continuous leg
//     and is never owner-checked.
//   - a task run lease names the Host that owns a run. Another Host (or the same Host
//     on a different run) cannot acquire it, and a late release from a non-owner must
//     not free the current owner's lease.
//   - an owner command is routed to the lease's host, never answered locally, and its
//     result is accepted only from that host.
//   - the host command queue is the fact source for a phone refresh: the renderer's
//     local pending list is a projection of it, never a second writer.
//   - a turn terminal does NOT release the CLI's active lock; only the ready boundary
//     does. Draining on terminal would race the lock.
package taskadapter
