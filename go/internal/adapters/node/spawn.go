// spawn.go is one generation of the upstream: spawn it, learn its port, prove it is
// healthy, and hand the caller the ready edge.
//
// It is the part of the cycle that can fail for reasons unrelated to the crash budget
// (the executable is missing, the child dies during startup, the health endpoint never
// answers). Those failures are counted as crashes on purpose: a child that cannot boot
// is the exact failure mode CRASH_BACKOFF_MS exists to throttle, and treating it as
// anything else turns a broken deployment into an unbounded respawn loop.

package node

import (
	"context"
	"fmt"
)

// spawnAndWaitReady starts one child and blocks until it passes the health probe. On
// success it closes readySignal (the first ready of this Start call) and records the
// host/port/pid.
//
// ctx is the SUPERVISOR'S lifetime context, not the caller's. That is deliberate and
// load-bearing: internal/adapters/exec arms a cancellation watcher on the context it
// is given, so a child's life is bound to that context. Handing it the caller's
// request context would make the upstream die the moment Start's caller returned —
// and it would do so silently, because the crash path then just reports a SIGTERM and
// schedules a restart, which looks exactly like a crash-looping upstream.
//
// The per-attempt budgets (ReadyTimeout) are applied on top with WithTimeout, so one
// stuck generation still cannot hold the supervisor forever.
func (s *Supervisor) spawnAndWaitReady(ctx context.Context, readySignal chan struct{}) error {
	secret, err := GenerateAuthToken()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.generation++
	generation := s.generation
	s.secret = secret
	s.state = StateStarting
	s.child = nil
	s.startedAt = 0
	s.mu.Unlock()

	spawn := s.opts.Spawn
	child, err := s.start(ctx, spawn, secret, s.opts.PinPort)
	if err != nil {
		return err
	}

	// Discover the real port from the listen line (PORT=0 case), unless it was pinned.
	port := child.port
	if port == 0 {
		scanCtx, cancel := context.WithTimeout(ctx, s.opts.ReadyTimeout)
		discovered, scanErr := scanPort(scanCtx, child.lines, child.proc)
		cancel()
		if scanErr != nil {
			// The child never announced a port, so nothing can be probed and nothing
			// would ever stop it except Stop. Kill it here rather than leaving a
			// process with no port and no owner.
			_ = stopChild(context.WithoutCancel(ctx), child, s.stopDur)
			return fmt.Errorf("node: upstream generation %d did not announce a port: %w", generation, scanErr)
		}
		port = discovered
	}
	child.port = port

	s.mu.Lock()
	s.child = child
	s.mu.Unlock()

	checker := NewHealthChecker(child.BaseURL(), secret)
	checker.Interval = s.opts.HealthInterval
	checker.Token = secret
	probeCtx, cancelProbe := context.WithTimeout(ctx, s.opts.ReadyTimeout)
	err = checker.WaitReady(probeCtx, s.opts.ReadyTimeout, s.opts.Poll)
	cancelProbe()
	if err != nil {
		// A child that never became ready is torn down here, not left to linger: the
		// crash-budget path below would otherwise spawn a second child on the same port.
		_ = stopChild(context.WithoutCancel(ctx), child, s.stopDur)
		return err
	}

	s.mu.Lock()
	s.state = StateReady
	s.startedAt = s.opts.Now().UnixMilli()
	s.child = child
	s.mu.Unlock()
	close(readySignal)

	s.log.Info("node upstream ready", "pid", child.PID(), "port", port, "generation", generation)

	if s.opts.WatchHealth {
		s.wait.Add(1)
		go func() {
			defer s.wait.Done()
			_ = checker.WatchHealth(ctx, func(r Ready) {
				if !r.OK {
					s.log.Warn("node upstream health degraded", "status", r.StatusCode, "error", r.Err)
				} else {
					s.log.Debug("node upstream healthy", "status", r.StatusCode)
				}
			})
		}()
	}
	return nil
}
