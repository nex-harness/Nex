package runnerupdate

import (
	"strings"
	"sync"
	"time"
)

// Update states reported by the runner's `updateStatus` frame
// (docs/next/runner-update.md decision 4).
const (
	StateIdle        = "idle"
	StateDownloading = "downloading"
	StateRestarting  = "restarting"
	StateFailed      = "failed"
)

// Status is the last known self-update state of one runner.
type Status struct {
	State string `json:"state"`
	// BuildID is the target build of the last update attempt, when the runner
	// reported one.
	BuildID string `json:"buildId,omitempty"`
	Error   string `json:"error,omitempty"`
	// At is when the state was recorded (zero for the implicit idle state).
	At time.Time `json:"at,omitempty"`
}

// Tracker keeps the per-runner update state the UI reads through
// GET /api/config/runners. It is in-memory only: after a server restart a
// runner reconnects and re-reports its build, which is the authoritative state.
type Tracker struct {
	mu     sync.Mutex
	status map[string]Status
	now    func() time.Time
}

// NewTracker returns an empty tracker.
func NewTracker() *Tracker {
	return &Tracker{status: map[string]Status{}, now: time.Now}
}

// Record stores one updateStatus frame. Unknown states are ignored so a newer
// runner cannot push an arbitrary string into the API.
func (t *Tracker) Record(runnerID, state, buildID, errMessage string) {
	runnerID = strings.TrimSpace(runnerID)
	if t == nil || runnerID == "" {
		return
	}
	switch state {
	case StateIdle, StateDownloading, StateRestarting, StateFailed:
	default:
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status == nil {
		t.status = map[string]Status{}
	}
	next := Status{State: state, BuildID: strings.TrimSpace(buildID), At: t.now()}
	if state == StateFailed {
		next.Error = strings.TrimSpace(errMessage)
	}
	t.status[runnerID] = next
}

// Get returns the last recorded status; the zero value means idle.
func (t *Tracker) Get(runnerID string) Status {
	runnerID = strings.TrimSpace(runnerID)
	if t == nil || runnerID == "" {
		return Status{State: StateIdle}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	status, ok := t.status[runnerID]
	if !ok {
		return Status{State: StateIdle}
	}
	return status
}

// Reset returns the runner to idle. A reconnect whose hello buildId already
// equals the staged build proves the update landed (or was never needed), so a
// stale `restarting`/`failed` state must not survive it.
func (t *Tracker) Reset(runnerID string) {
	runnerID = strings.TrimSpace(runnerID)
	if t == nil || runnerID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status == nil {
		t.status = map[string]Status{}
	}
	t.status[runnerID] = Status{State: StateIdle, At: t.now()}
}
