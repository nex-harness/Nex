// supervisor.go is the orchestrator: one state owner for "is the Node upstream up,
// and what do we do about it".
//
// State owners and event order (AGENTS.md requires this to be written down for
// anything with state, timing and async sync):
//
//	Go server start
//	  └─ Start() ─► generate secret ─► spawn child (PORT=0) ─► scan stdout for port
//	                                                  └─► wait GET /api/auth/session 200
//	                                                        └─► state=Ready → gateway may dial
//	child crash
//	  └─ recordCrash() ─► delayMs ─► state=Starting ─► spawn again (new secret)
//	       └─ crash count walks off CRASH_BACKOFF_MS ─► state=CrashLoopStopped
//	                                                          (no automatic restart; manual only)
//	Stop() / SIGTERM / SIGINT / parent disconnect
//	  └─ state=Stopping ─► SIGTERM the tree ─► grace ─► SIGKILL ─► wait terminal ─► state=Stopped
//
// Transitions are owned here and nowhere else. The gateway (step 2.8) takes an
// UpstreamConfig and never spawns or observes the child; it only reads Upstream(),
// so "which process is on the other side" and "who restarts it" cannot disagree.
//
// Lifecycle states are the TypeScript enum from contracts.ts
// (lifecycleStateSchema). Only the ones a child-process supervisor can reach are
// defined: starting, ready, stopping, crashed, crash-loop-stopped, stopped.
// stop-failed exists because the Go front must NOT silently leave a tree alive; the
// update/uninstall states do not, because this supervisor owns no update.
package node

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/exec"
)

const (
	// StateStarting: a child has been asked for and is not ready yet.
	StateStarting = "starting"
	// StateReady: the child answered the health probe.
	StateReady = "ready"
	// StateStopping: a stop is in progress.
	StateStopping = "stopping"
	// StateStopped: no child.
	StateStopped = "stopped"
	// StateCrashed: the child died and a restart is scheduled or the loop is stopping.
	StateCrashed = "crashed"
	// StateCrashLoopStopped: the crash budget is exhausted; no automatic restart.
	StateCrashLoopStopped = "crash-loop-stopped"
	// StateStopFailed: the tree did not reach a terminal state. Manual intervention.
	StateStopFailed = "stop-failed"

	// DefaultStopGrace is the supervisor's graceful stop budget. The TypeScript uses
	// coreStopGraceTimeoutMs ?? 5_000.
	DefaultStopGrace = 5 * time.Second
	// DefaultRestartBaseTimeout bounds Start overall.
	DefaultRestartBaseTimeout = 30 * time.Second
)

// ErrCrashLoopStopped is returned by Restart when the crash budget is exhausted.
var ErrCrashLoopStopped = errors.New("node: crash budget exhausted; upstream is not restarted automatically")

// Logger is the sink the supervisor logs through; *slog.Logger satisfies it. The
// levels follow the AGENTS.md logging section: process lifecycle at info, crashes at
// warn, unrecoverable stop failure at error.
//
// The production value is the process logger internal/server builds (server.NewLogger), so the
// supervisor's lines carry the same `[timestamp] [pid] [source]` prefix every other line in the
// process does and land in the same stream. This interface is what keeps that a wiring decision
// made once, in the entry point, instead of a hard dependency from this adapter to the HTTP shell
// (which would invert the dependency direction internal/archcheck enforces).
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// Options configures a Supervisor. The zero value is unusable (no spawn config),
// so Spawn must be set; everything else has a production default.
type Options struct {
	// Spawn describes the child to run. Required.
	Spawn SpawnConfig
	// PinPort, when non-zero, is the port the child is told to bind. The default (0)
	// lets the OS assign one and the port is discovered from the child's listen line,
	// which is race-free; pinning is for a deployment that published a port.
	PinPort int
	// ReadyTimeout bounds the first health probe after a spawn. Zero selects
	// DefaultReadyTimeout.
	ReadyTimeout time.Duration
	// HealthInterval is the live-probe period. Zero selects DefaultHealthInterval.
	HealthInterval time.Duration
	// StopGrace is the graceful stop budget. Zero selects DefaultStopGrace.
	StopGrace time.Duration
	// Now is the crash-budget clock. Zero selects time.Now. Tests inject a fake.
	Now func() time.Time
	// Poll is the sleep used between readiness attempts. Zero selects a real sleep;
	// tests pass a fake-clock sleep so the backoff timing is instant.
	Poll func(ctx context.Context, d time.Duration) error
	// Log receives lifecycle events. Nil discards them.
	Log Logger
	// WatchHealth enables the periodic liveness loop after Start returns. Tests leave
	// it off and drive probes themselves.
	WatchHealth bool
}

// Supervisor supervises one Node upstream.
type Supervisor struct {
	opts    Options
	budget  *CrashBudget
	log     Logger
	stopDur time.Duration

	mu sync.Mutex
	// state is the lifecycle state. Guarded by mu.
	state string
	// child is the current child, or nil between generations.
	child *child
	// secret is the shared secret for the CURRENT generation. It rotates on every
	// restart so a process that somehow outlives its generation cannot be reached
	// with a stale token, and it is the token the gateway must present as ?token=.
	secret string
	// lastExitReason mirrors the TypeScript field for diagnostics.
	lastExitReason string
	// generation counts spawns; it is the "which child am I watching" discriminator,
	// mirroring the TypeScript launchCore generation counter.
	generation int
	// startedAt is when the current child became ready (Unix ms).
	startedAt int64

	// cancelContext is cancelled by Stop; it stops the health loop and any in-flight
	// restart timer.
	cancelContext context.CancelFunc
	// rootCtx is the supervisor's lifetime context, created in Start.
	rootCtx context.Context
	// wait blocks a concurrent Stop until the first one finished.
	wait sync.WaitGroup
	// readySignal is closed when a child became ready and re-created on each spawn, so
	// Start can wait without polling twice.
	readySignal chan struct{}
}

// New builds a supervisor for opts.
func New(opts Options) (*Supervisor, error) {
	if err := opts.Spawn.Validate(); err != nil {
		return nil, err
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = DefaultReadyTimeout
	}
	if opts.HealthInterval <= 0 {
		opts.HealthInterval = DefaultHealthInterval
	}
	if opts.StopGrace <= 0 {
		opts.StopGrace = DefaultStopGrace
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Supervisor{
		opts:        opts,
		budget:      NewCrashBudget(opts.Now),
		log:         loggerOrDiscard(opts.Log),
		stopDur:     opts.StopGrace,
		state:       StateStopped,
		readySignal: make(chan struct{}),
	}, nil
}

// loggerOrDiscard returns a sink that drops everything, so every call site can use
// s.log unconditionally instead of re-checking a nil that a caller may leave unset.
func loggerOrDiscard(log Logger) Logger {
	if log == nil {
		return discardLogger{}
	}
	return log
}

// LoggerOrDiscard is loggerOrDiscard exported for the process entry point, which builds one
// logger and hands the same value to the supervisor, the gateway and the HTTP shell (step 2.10).
// A caller that has no logger can pass nil to any of them; this exists so "I have none" is one
// named decision rather than three separate nil checks.
func LoggerOrDiscard(log Logger) Logger { return loggerOrDiscard(log) }

type discardLogger struct{}

func (discardLogger) Debug(string, ...any) {}
func (discardLogger) Info(string, ...any)  {}
func (discardLogger) Warn(string, ...any)  {}
func (discardLogger) Error(string, ...any) {}

// Start launches the first child and blocks until it is ready (or the ready timeout
// or ctx expires). It is idempotent while a child is running: calling it when a child
// is ready returns immediately, which is what lets a request-path caller ensure the
// upstream without coordinating with Start.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.state == StateReady || s.state == StateStarting {
		signal := s.readySignal
		s.mu.Unlock()
		select {
		case <-signal:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.state == StateCrashLoopStopped {
		s.mu.Unlock()
		return ErrCrashLoopStopped
	}
	s.rootCtx, s.cancelContext = context.WithCancel(context.WithoutCancel(ctx))
	s.readySignal = make(chan struct{})
	signal := s.readySignal
	s.mu.Unlock()

	s.wait.Add(1)
	go func() {
		defer s.wait.Done()
		s.run(ctx, signal)
	}()
	return nil
}

// WaitReady blocks until the current child answered the health probe.
func (s *Supervisor) WaitReady(ctx context.Context) error {
	s.mu.Lock()
	signal := s.readySignal
	s.mu.Unlock()
	select {
	case <-signal:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.state == StateReady {
			return nil
		}
		return fmt.Errorf("node: upstream not ready: state=%s", s.state)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// run is the supervisor's single long-lived goroutine for the current generation
// cycle: it spawns, waits for ready, watches for a crash, and applies the crash
// budget. Everything that mutates state funnels through it, which is what keeps the
// state transitions ordered.
//
// signal is closed exactly once when a child became ready, so the caller of Start
// observes the ready transition. After the first ready it is replaced (Start made a
// fresh channel) so a later generation has its own ready edge.
//
// The loop deliberately ignores the context Start was called with and runs on the
// supervisor's own rootCtx: a child belongs to the SERVER, not to the request that
// asked for it. The caller's context only ever bounds how long that caller waits.
func (s *Supervisor) run(_ context.Context, readySignal chan struct{}) {
	s.mu.Lock()
	rootCtx := s.rootCtx
	s.mu.Unlock()

	for {
		if rootCtx.Err() != nil {
			return
		}
		if err := s.spawnAndWaitReady(rootCtx, readySignal); err != nil {
			if errors.Is(err, ErrCrashLoopStopped) {
				return
			}
			// A failed spawn/ready is a crash as far as the budget is concerned: a child
			// that dies during startup must not get unlimited retries. recordCrash and
			// the delay below mirror handleTerminal in supervisor.ts.
			decision := s.budget.RecordCrash()
			if !decision.ShouldRestart {
				s.transition(StateCrashLoopStopped, fmt.Sprintf("crash budget exhausted during startup: %v", err))
				s.log.Error("node upstream crash budget exhausted during startup", "error", err)
				return
			}
			s.transition(StateCrashed, err.Error())
			s.log.Warn("node upstream failed to become ready, scheduling restart", "error", err, "delayMs", decision.DelayMs)
			if !s.sleepCtx(rootCtx, time.Duration(decision.DelayMs)*time.Millisecond) {
				return
			}
			s.transition(StateStarting, "")
			continue
		}

		// The child is ready. Now block until it dies (ctx cancelled) or it crashes.
		select {
		case <-rootCtx.Done():
			return
		case result := <-s.waitChild(rootCtx):
			if rootCtx.Err() != nil {
				return
			}
			decision := s.budget.RecordCrash()
			s.transition(StateCrashed, result)
			if !decision.ShouldRestart {
				s.transition(StateCrashLoopStopped, result)
				s.log.Error("node upstream crash budget exhausted, entering crash-loop-stopped", "reason", result)
				return
			}
			s.log.Warn("node upstream crashed, scheduling restart", "reason", result, "delayMs", decision.DelayMs)
			if !s.sleepCtx(rootCtx, time.Duration(decision.DelayMs)*time.Millisecond) {
				return
			}
			s.transition(StateStarting, "")
			readySignal = s.freshReadySignal()
		}
	}
}

// waitChild blocks until the current child reaches a terminal state, returning a
// reason string.
//
// It CLEARS s.child when the process is observed dead. That is not bookkeeping: a
// supervisor in crash-loop-stopped must report no child, because a stale reference
// would make the gateway keep a dead origin in its UpstreamConfig and the next Start
// would believe something is running. The reference is only dropped once Wait
// returned, so there is no window where a live process has no owner.
func (s *Supervisor) waitChild(ctx context.Context) <-chan string {
	out := make(chan string, 1)
	s.mu.Lock()
	child := s.child
	s.mu.Unlock()
	if child == nil {
		out <- "no child"
		close(out)
		return out
	}
	go func() {
		result := child.proc.Wait()
		reason := describeExit(result)
		s.mu.Lock()
		// Only clear OUR child: a later generation may already own the reference.
		if s.child == child {
			s.child = nil
		}
		s.mu.Unlock()
		out <- reason
		close(out)
	}()
	return out
}

// describeExit is the TypeScript `core exited code=… signal=…` reason, for parity in
// the last-exit diagnostic.
func describeExit(result exec.Result) string {
	signal := result.Signal
	if signal == "" {
		signal = "none"
	}
	return fmt.Sprintf("core exited code=%d signal=%s", result.ExitCode, signal)
}

// freshReadySignal returns a new ready channel for the next generation and stores it,
// so WaitReady observes a fresh generation's ready edge.
func (s *Supervisor) freshReadySignal() chan struct{} {
	ch := make(chan struct{})
	s.mu.Lock()
	s.readySignal = ch
	s.mu.Unlock()
	return ch
}

// transition sets the state and the last-exit reason under the lock.
func (s *Supervisor) transition(state, reason string) {
	s.mu.Lock()
	s.state = state
	if reason != "" {
		s.lastExitReason = reason
	}
	s.mu.Unlock()
}

// sleepCtx waits d unless ctx is done; returns false when it was interrupted by ctx.
func (s *Supervisor) sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	if s.opts.Poll != nil {
		return s.opts.Poll(ctx, d) == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
