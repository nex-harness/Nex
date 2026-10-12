// status.go is the read side of the supervisor: what the Go front, an operator or a
// test can observe about the child without reaching into its internals.
//
// It is separate from supervisor.go because everything here is a pure read of state
// someone else owns. The gateway (step 2.8) is the important consumer: it needs the
// current child's origin and the secret that child was started with, and it must take
// BOTH from one snapshot — reading the URL and the token in two separate locked calls
// could straddle a restart and pair a fresh URL with a stale secret, which the upstream
// would answer with 401 and the operator would read as a broken gateway.

package node

// Status is a point-in-time view for diagnostics and the (future) status route.
type Status struct {
	State          string
	PID            int
	Port           int
	Secret         string
	Generation     int
	StartedAt      int64
	LastExitReason string
	CrashBudget    BudgetSnapshot
}

// Status snapshots the supervisor. Secret is included because it is the token the
// gateway must present as ?token= to the current child; it is never logged (callers
// that serialize Status must strip it).
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	pid, port := 0, 0
	if s.child != nil {
		pid = s.child.PID()
		port = s.child.port
	}
	return Status{
		State:          s.state,
		PID:            pid,
		Port:           port,
		Secret:         s.secret,
		Generation:     s.generation,
		StartedAt:      s.startedAt,
		LastExitReason: s.lastExitReason,
		CrashBudget:    s.budget.Snapshot(),
	}
}

// UpstreamBaseURL is the current child's origin, or "" when there is none. This is
// what the gateway (step 2.8) dials.
func (s *Supervisor) UpstreamBaseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.child == nil {
		return ""
	}
	return s.child.BaseURL()
}

// UpstreamToken is the current generation's shared secret, or "".
func (s *Supervisor) UpstreamToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.secret
}

// State reports the lifecycle state.
func (s *Supervisor) State() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// currentPID is the child pid for tests and diagnostics.
func (s *Supervisor) currentPID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.child == nil {
		return 0
	}
	return s.child.PID()
}
