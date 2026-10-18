package taskadapter

import (
	"sync"
)

// queue.go — the host command queue and the drain. Ports runtimeCommands +
// drainRuntimeCommands + scheduleRuntimeCommandDrain (guards D1–D14).
//
// The queue is the FACT SOURCE for a phone refresh. The renderer keeps a pending
// optimistic list, but that list is a projection: `getTaskSnapshot` reads
// `runtimeCommands.get(taskKey(meta))` (E2), so a renderer-only cancel would be undone
// by the next snapshot. Every mutation therefore goes through this queue.

// queue is runtimeCommands: taskKey → the ordered command list.
type queue struct {
	mu sync.Mutex
	// commands is runtimeCommands. An empty list DELETES the key rather than storing
	// an empty slice: handleTaskIndexTerminalEvent's "no local state" check tests
	// presence, so a lingering empty slice would make a foreign task look owned.
	commands map[string][]Command
}

func newQueue() *queue { return &queue{commands: map[string][]Command{}} }

// setLocked is the single write path. A re-locking `set` sibling would deadlock here:
// the mutating operations (Promote / Cancel / CompleteByInputID) hold the lock across
// their read-modify-write, because releasing it between the read and the write would
// let two callers both decide they own the same command.
func (q *queue) setLocked(key string, commands []Command) {
	if len(commands) == 0 {
		delete(q.commands, key)
		return
	}
	q.commands[key] = commands
}

func (q *queue) list(key string) []Command {
	q.mu.Lock()
	defer q.mu.Unlock()
	existing := q.commands[key]
	if len(existing) == 0 {
		return nil
	}
	out := make([]Command, len(existing))
	copy(out, existing)
	return out
}

// present ports `runtimeCommands.has(key)`.
func (q *queue) present(key string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.commands[key]
	return ok
}

// size ports the memoryDiagnostics `runtimeCommands` counter.
func (q *queue) size() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.commands)
}

// Append ports enqueueTaskCommand's append (D13): FIFO within a task, and a retried
// commandId must not become a second entry (D14).
//
// The dedupe is by commandId across the WHOLE list, including failed entries. A phone
// that retries after a timeout re-sends the same commandId with fresh content; without
// the dedupe the queue would hold two entries that drain as two turns of the same
// input.
//
// The type is forced to send_prompt here because that is the only member of the
// discriminated union, and the drain keys on it — a command with no type would be
// accepted into the queue and then never be drainable, which is a silent drop. The status
// is forced to `accepted` for the same reason: the only statuses an APPEND may produce
// are accepted (a new or retried entry); running and failed are set by the drain.
func (q *queue) Append(target Target, command Command) Command {
	key := target.TaskKey()
	command.Type = CommandTypeSendPrompt
	command.WorkspaceKey = target.WorkspaceKey()
	command.TaskID = target.TaskID
	command.Status = CommandAccepted
	q.mu.Lock()
	defer q.mu.Unlock()
	for index, existing := range q.commands[key] {
		if existing.CommandID == command.CommandID {
			// A retry refreshes the entry in place (FIFO position preserved) and
			// re-arms a failed command, but never appends a second copy.
			command.Status = CommandAccepted
			command.CreatedAt = existing.CreatedAt
			q.commands[key][index] = command
			return command
		}
	}
	q.commands[key] = append(append([]Command{}, q.commands[key]...), command)
	return command
}

// TakeDrainable ports the drain's two blocking guards plus its pick, under ONE lock
// (D1–D3, D6).
//
// It refuses (and says why) when:
//   - the task has an active prompt input (D1): sending now would interleave two turns;
//   - any command is already `running` (D2): one turn at a time;
//   - no command is an `accepted` send_prompt (D3).
//
// The pick and the flip to `running` happen under a single lock because the TS gets the
// same guarantee from serializing the drains on a per-key promise (D4): two concurrent
// drains can never take the same command.
func (q *queue) TakeDrainable(target Target, hasActiveRun bool, updatedAt int64) (Command, string) {
	key := target.TaskKey()
	if hasActiveRun {
		return Command{}, "active_input"
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	commands := q.commands[key]
	for _, existing := range commands {
		if existing.Status == CommandRunning {
			return Command{}, "already_running"
		}
	}
	for index, existing := range commands {
		if existing.Type != CommandTypeSendPrompt || existing.Status != CommandAccepted {
			continue
		}
		commands[index].Status = CommandRunning
		commands[index].UpdatedAt = updatedAt
		commands[index].Error = ""
		return commands[index], ""
	}
	return Command{}, "no_accepted_command"
}

// MarkRunning ports markRuntimeCommandRunning (D6): it flips ONLY the matching
// commandId, because two queued commands must not both enter `running`.
func (q *queue) MarkRunning(target Target, commandID string, updatedAt int64) (Command, bool) {
	key := target.TaskKey()
	q.mu.Lock()
	defer q.mu.Unlock()
	commands := q.commands[key]
	for index, existing := range commands {
		if existing.CommandID != commandID {
			continue
		}
		commands[index].Status = CommandRunning
		commands[index].UpdatedAt = updatedAt
		commands[index].Error = ""
		return commands[index], true
	}
	return Command{}, false
}

// MarkFailed ports markRuntimeCommandFailed (D5): the failure is recorded on the
// entry, which keeps it out of the drain (only `accepted` is drainable) and visible to
// the phone's pending list.
func (q *queue) MarkFailed(target Target, commandID string, updatedAt int64, message string) {
	key := target.TaskKey()
	q.mu.Lock()
	defer q.mu.Unlock()
	commands := q.commands[key]
	for index := range commands {
		if commands[index].CommandID == commandID {
			commands[index].Status = CommandFailed
			commands[index].UpdatedAt = updatedAt
			commands[index].Error = message
			return
		}
	}
}

// Promote ports promoteTaskCommand (D7–D9): it moves the command to the HEAD and
// re-arms it. Promoting a `running` command is a no-op success (D8) — resetting it
// would let a retried promote start the same input twice — and an unknown commandId is
// an OWNER_COMMAND_FAILED refusal (D9).
func (q *queue) Promote(target Target, commandID string, updatedAt int64) (Command, error) {
	key := target.TaskKey()
	q.mu.Lock()
	defer q.mu.Unlock()
	commands := q.commands[key]
	index := -1
	for position := range commands {
		if commands[position].CommandID == commandID {
			index = position
			break
		}
	}
	if index < 0 {
		return Command{}, &OwnerError{Code: CodeOwnerCommandFailed, Message: "Task command not found."}
	}
	promoted := commands[index]
	if promoted.Status == CommandRunning {
		return promoted, nil
	}
	promoted.Status = CommandAccepted
	promoted.UpdatedAt = updatedAt
	promoted.Error = ""
	next := make([]Command, 0, len(commands))
	next = append(next, promoted)
	for position, existing := range commands {
		if position != index {
			next = append(next, existing)
		}
	}
	q.setLocked(key, next)
	return promoted, nil
}

// CancelOutcome is NexCancelTaskCommandResult's discriminated union.
type CancelOutcome struct {
	Canceled  bool   `json:"canceled"`
	CommandID string `json:"commandId"`
	// Reason is "" (removed), "not_found" or "already_running".
	Reason string `json:"reason,omitempty"`
	// Status is only set for already_running, where the command survives.
	Status string `json:"status,omitempty"`
}

// Cancel ports cancelTaskCommand (D10–D12).
//
// Three outcomes, and each one is a different promise to the caller:
//
//   - not found → canceled:true. Cancel is idempotent: the phone's delete must not
//     error on a command the host already dropped (D10).
//   - running → canceled:false + already_running. A command that already started must
//     not be pulled back out of the send path (D11), and nothing changed, so no
//     snapshot update is emitted (E8).
//   - otherwise → the entry is deleted from the HOST queue (D12) and the caller emits
//     a snapshot update (E7). Deleting only the renderer's copy would let the next
//     snapshot restore the command.
func (q *queue) Cancel(target Target, commandID string, updatedAt int64) (CancelOutcome, bool) {
	key := target.TaskKey()
	q.mu.Lock()
	commands := q.commands[key]
	index := -1
	for position := range commands {
		if commands[position].CommandID == commandID {
			index = position
			break
		}
	}
	if index < 0 {
		q.mu.Unlock()
		return CancelOutcome{Canceled: true, CommandID: commandID, Reason: "not_found"}, false
	}
	if commands[index].Status == CommandRunning {
		outcome := CancelOutcome{
			Canceled:  false,
			CommandID: commands[index].CommandID,
			Reason:    "already_running",
			Status:    commands[index].Status,
		}
		q.mu.Unlock()
		return outcome, false
	}
	kept := make([]Command, 0, len(commands)-1)
	for position, existing := range commands {
		if position != index {
			kept = append(kept, existing)
		}
	}
	q.setLocked(key, kept)
	q.mu.Unlock()
	return CancelOutcome{Canceled: true, CommandID: commands[index].CommandID, Status: commands[index].Status}, true
}

// MarkRunning ports markRuntimeCommandRunning (D6): it flips ONLY the matching
// commandId, because two queued commands must not both enter `running`.

// CompleteByInputID ports completeRuntimeCommandByInputId (D15, D16): it removes the
// command whose traceId equals the terminal input id AND whose status is `running`.
//
// Both conditions matter. The traceId match is what stops a terminal event for one
// input from clearing another; the `running` condition is what keeps a still-`accepted`
// entry queued. A missing terminal input id is a no-op (D16): the v4 sessions-index
// terminal event carries no inputId, and only the locally recorded active run may
// close the queue.
func (q *queue) CompleteByInputID(target Target, terminalInputID string) (Command, bool) {
	if terminalInputID == "" {
		return Command{}, false
	}
	key := target.TaskKey()
	q.mu.Lock()
	defer q.mu.Unlock()
	commands := q.commands[key]
	index := -1
	for position, existing := range commands {
		if existing.Type == CommandTypeSendPrompt &&
			existing.Status == CommandRunning &&
			existing.TraceID == terminalInputID {
			index = position
			break
		}
	}
	if index < 0 {
		return Command{}, false
	}
	completed := commands[index]
	kept := make([]Command, 0, len(commands)-1)
	for position, existing := range commands {
		if position != index {
			kept = append(kept, existing)
		}
	}
	q.setLocked(key, kept)
	return completed, true
}
