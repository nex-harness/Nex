// storagegate.go ports NexStorageStartupGate (nexStorageStartupGate.ts): the
// per-process-generation gate that holds the client's first turn until SQLite is
// ready, and fails every later turn when it never becomes ready.
//
// The rules it keeps, each from the TS:
//   - State comes only from this connection's legal control frames. An attempt
//     that restarts (new attemptId) or replays (sequence not increasing) does not
//     overwrite the accepted state.
//   - ready and failed are terminal: the first one wins for the process.
//   - A failure synthesises a snapshot even if no frame arrived, with an
//     "unresolved:<uuid>" database id — an unknown database must not pretend to be
//     a resolved path.
//   - Dispose while still waiting fails with transport_closed rather than leaving
//     waiters parked forever.
package proto

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

// StoragePhase is the phase field of nexStorageStartupStateSchema.
type StoragePhase string

const (
	StorageChecking       StoragePhase = "checking"
	StorageWaitingForLock StoragePhase = "waiting_for_lock"
	StorageMigrating      StoragePhase = "migrating"
	StorageCommitting     StoragePhase = "committing"
	StorageReady          StoragePhase = "ready"
	StorageFailed         StoragePhase = "failed"
)

// StorageState is the subset of the startup frame the gate reasons about. The
// frame is carried whole on the notification stream; this is the gate's view.
type StorageState struct {
	SchemaVersion int          `json:"schemaVersion"`
	AttemptID     string       `json:"attemptId"`
	DatabaseID    string       `json:"databaseId"`
	DatabaseKind  string       `json:"databaseKind"`
	Sequence      int64        `json:"sequence"`
	Phase         StoragePhase `json:"phase"`
	ElapsedMs     float64      `json:"elapsedMs"`
	ErrorCode     string       `json:"errorCode,omitempty"`
}

// FirstStatusTimeout is FIRST_STATUS_TIMEOUT_MS: the deadline for the first
// storage frame when startup gating is required.
const FirstStatusTimeout = 30 * time.Second

// ErrTransportClosed is the error a waiter gets when the gate is disposed while
// still waiting, and what the client reports when its transport closes.
var ErrTransportClosed = errors.New("Nex agent transport closed")

// StorageStartupError is the terminal failure. The code is the classified
// database startup error (sql_failed, startup_status_timeout, transport_closed…).
type StorageStartupError struct{ Code string }

func (e *StorageStartupError) Error() string { return "SQLite startup failed: " + e.Code }

// ErrStorageFailed returns the terminal error for a code, or nil.
func ErrStorageFailed(code string) error { return &StorageStartupError{Code: code} }

// StorageStartupGate holds the first turn until storage is ready.
//
// One gate belongs to one process generation: it is constructed with the client
// and dropped when the transport closes, so a restarted attempt never inherits a
// previous generation's "ready".
type StorageStartupGate struct {
	required bool

	mu        sync.Mutex
	current   *StorageState
	terminal  error
	ready     chan struct{}
	readyOnce bool
	timer     *time.Timer
	timerStop sync.Once
	disposed  bool

	// changed is the notification stream (startup/storageState is also delivered
	// to the client subscribers; this channel exists for the gate's own observers).
	changedMu sync.Mutex
	changed   []chan StorageState
}

// NewStorageStartupGate constructs a gate. When required is false the gate never
// blocks: an agent that cannot report storage state at all (an old CLI) must
// still serve requests.
func NewStorageStartupGate(required bool, firstStatusTimeout time.Duration) *StorageStartupGate {
	g := &StorageStartupGate{required: required, ready: make(chan struct{})}
	if required {
		if firstStatusTimeout <= 0 {
			firstStatusTimeout = FirstStatusTimeout
		}
		g.timer = time.AfterFunc(firstStatusTimeout, func() { g.fail("startup_status_timeout") })
	}
	return g
}

// OnChange registers an observer of accepted storage states. The returned
// function removes it.
func (g *StorageStartupGate) OnChange() (<-chan StorageState, func()) {
	ch := make(chan StorageState, 1)
	g.changedMu.Lock()
	g.changed = append(g.changed, ch)
	g.changedMu.Unlock()
	return ch, func() {
		g.changedMu.Lock()
		defer g.changedMu.Unlock()
		for i, c := range g.changed {
			if c == ch {
				g.changed = append(g.changed[:i], g.changed[i+1:]...)
				return
			}
		}
	}
}

// Required reports whether this gate gates at all. A client whose agent command
// did not declare SupportsStorageStartup builds a non-required gate, and the
// client must then leave its request watchdogs running: nothing would ever resume
// a suspended one.
func (g *StorageStartupGate) Required() bool { return g.required }

// IsWaiting reports whether a request must wait before being written.
//
// It mirrors the TS `isWaiting`, which is driven by the presence of a PENDING
// promise: a gate that was not required never creates one, so an agent that
// cannot report storage state at all never blocks a request. Once a gate IS
// required and a frame has failed it, waiting is permanent — that is how a failed
// startup reaches the caller instead of being silently ignored.
func (g *StorageStartupGate) IsWaiting() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.terminal != nil {
		return true
	}
	if !g.required {
		return false
	}
	return g.current == nil || g.current.Phase != StorageReady
}

// Snapshot returns the accepted state, or nil when no legal frame arrived yet.
func (g *StorageStartupGate) Snapshot() (StorageState, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.current == nil {
		return StorageState{}, false
	}
	return *g.current, true
}

// Accept applies one startup/storageState frame. It returns false when the frame
// is not a legal continuation, which the caller uses to decide whether to resume
// suspended request timers.
func (g *StorageStartupGate) Accept(params json.RawMessage) bool {
	next, ok := parseStorageState(params)
	if !ok {
		return false
	}
	g.mu.Lock()
	if g.terminal != nil || !g.acceptedLocked(next) {
		g.mu.Unlock()
		return false
	}
	g.stopTimerLocked()
	g.current = &next
	phase := next.Phase
	g.mu.Unlock()

	switch phase {
	case StorageReady:
		g.markReady()
	case StorageFailed:
		g.fail(orDefault(next.ErrorCode, "sql_failed"))
	}
	g.publish(next)
	return true
}

// acceptedLocked is the monotonic-acceptance rule: same attempt and database,
// strictly increasing sequence, and no already-terminal phase.
func (g *StorageStartupGate) acceptedLocked(next StorageState) bool {
	if g.current == nil {
		return true
	}
	cur := g.current
	return next.AttemptID == cur.AttemptID &&
		next.DatabaseID == cur.DatabaseID &&
		next.Sequence > cur.Sequence &&
		cur.Phase != StorageReady &&
		cur.Phase != StorageFailed
}

// Wait blocks until the gate is ready, fails, or ctx is done. A gate that is not
// required, or already ready, returns immediately.
func (g *StorageStartupGate) Wait(ctx context.Context) error {
	g.mu.Lock()
	switch {
	case g.terminal != nil:
		err := g.terminal
		g.mu.Unlock()
		return err
	case !g.required || (g.current != nil && g.current.Phase == StorageReady):
		g.mu.Unlock()
		return nil
	}
	ready := g.ready
	g.mu.Unlock()

	// The ready channel means "the gate finished", not "storage is ready": a
	// failure closes it too. The terminal error is therefore re-read AFTER the
	// channel fires, never from the value captured before the wait.
	select {
	case <-ready:
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.terminal
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Dispose fails a still-waiting gate with transport_closed. Disposing twice is a
// no-op, so a transport close followed by a client dispose cannot overwrite the
// original cause.
func (g *StorageStartupGate) Dispose() {
	g.mu.Lock()
	if g.disposed {
		g.mu.Unlock()
		return
	}
	g.disposed = true
	g.stopTimerLocked()
	g.mu.Unlock()
	g.fail("transport_closed")
}

// Terminal returns the terminal error, if the gate failed.
func (g *StorageStartupGate) Terminal() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.terminal
}

func (g *StorageStartupGate) markReady() {
	g.mu.Lock()
	if g.readyOnce {
		g.mu.Unlock()
		return
	}
	g.readyOnce = true
	g.mu.Unlock()
	close(g.ready)
}

// fail records the terminal error once. The first cause is the root cause: a
// later dispose must not rewrite an sql_failed into a transport_closed.
func (g *StorageStartupGate) fail(code string) {
	g.mu.Lock()
	if g.terminal != nil {
		g.mu.Unlock()
		return
	}
	g.stopTimerLocked()
	if g.current == nil {
		// Synthesise a snapshot: a failure before the first frame is still a
		// fact, and its database identity must stay unresolved rather than
		// borrow some other database's id.
		g.current = &StorageState{
			SchemaVersion: 1,
			AttemptID:     uuid.NewString(),
			DatabaseID:    "unresolved:" + uuid.NewString(),
			DatabaseKind:  "session",
			Sequence:      1,
			Phase:         StorageFailed,
			ErrorCode:     code,
		}
	} else if g.current.Phase != StorageFailed {
		next := *g.current
		next.Phase = StorageFailed
		next.ErrorCode = code
		next.Sequence++
		g.current = &next
	}
	g.terminal = ErrStorageFailed(code)
	if !g.readyOnce {
		g.readyOnce = true
		close(g.ready)
	}
	state := *g.current
	g.mu.Unlock()
	g.publish(state)
}

func (g *StorageStartupGate) stopTimerLocked() {
	if g.timer != nil {
		g.timerStop.Do(func() { g.timer.Stop() })
	}
}

func (g *StorageStartupGate) publish(state StorageState) {
	g.changedMu.Lock()
	subs := append([]chan StorageState(nil), g.changed...)
	g.changedMu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- state:
		default: // a slow observer must not stall the frame pump
		}
	}
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// parseStorageState is the runtime validation of nexStorageStartupStateSchema for
// the fields the gate depends on. A frame that fails it is not a legal
// continuation and must not move the state — the gate does not care about the
// optional migration/telemetry fields, which the notification subscribers keep.
func parseStorageState(raw json.RawMessage) (StorageState, bool) {
	var state StorageState
	if err := json.Unmarshal(raw, &state); err != nil {
		return StorageState{}, false
	}
	if state.SchemaVersion != 1 || state.AttemptID == "" || len(state.AttemptID) > 128 ||
		state.DatabaseID == "" || len(state.DatabaseID) > 128 ||
		state.Sequence < 1 || state.ElapsedMs < 0 {
		return StorageState{}, false
	}
	switch state.DatabaseKind {
	case "session", "tasks-index":
	default:
		return StorageState{}, false
	}
	switch state.Phase {
	case StorageChecking, StorageWaitingForLock, StorageMigrating, StorageCommitting, StorageReady:
	case StorageFailed:
		if state.ErrorCode == "" {
			return StorageState{}, false
		}
	default:
		return StorageState{}, false
	}
	return state, true
}
