// stop.go is the supervisor's way down: Stop, and the signal handler that feeds it.
//
// Both exist for the same reason. server-core/parentDisconnect.ts installs a cleanup
// hook on the Core so that a Core whose parent was SIGKILLed still closes its own
// HTTP/WebSocket/agent resources instead of surviving as an orphan holding the port.
// This is that contract seen from the other side: the Go front is the parent, so when
// the front is asked to stop — explicitly, by context cancellation, or by the
// operator's SIGTERM/Ctrl-C — it must do for the Node child exactly what the Core does
// for its supervisor, and prove the tree is gone before returning.

package node

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// Stop tears down the current child and stops the supervisor.
//
// It is the parent-disconnect analogue of server-core/parentDisconnect.ts from the
// OTHER side: the TS makes the Core clean up when its parent IPC drops, so the Core
// does not become an orphan holding the port. Here the Go front is the parent; when
// it is asked to stop (or its context is cancelled, or it receives SIGTERM/SIGINT),
// it must do for the Node child what the Core does for its supervisor — close the
// HTTP/WebSocket/agent resources deterministically and prove the tree is gone before
// returning, so no orphan survives the Go process.
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel := s.cancelContext
	s.cancelContext = nil
	child := s.child
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	var stopErr error
	if child != nil {
		s.transition(StateStopping, "requested")
		if err := stopChild(ctx, child, s.stopDur); err != nil {
			// The tree did not reach a terminal state after SIGKILL. Preserve the child
			// reference and surface stop-failed; the Go front must NOT pretend the
			// upstream is gone while a process may still hold the port.
			s.transition(StateStopFailed, err.Error())
			s.log.Error("node upstream did not terminate; state is stop-failed", "pid", child.PID(), "error", err)
			stopErr = err
		} else {
			s.mu.Lock()
			s.child = nil
			s.mu.Unlock()
			s.log.Info("node upstream stopped", "pid", child.PID())
		}
	}
	s.wait.Wait()
	if stopErr == nil {
		s.transition(StateStopped, "")
	}
	return stopErr
}

// ShutdownOnSignal stops the supervisor when the process receives SIGTERM/SIGINT, and
// returns a function that releases the handler.
//
// It is the Go front's side of "forward SIGTERM/Ctrl-C" from the plan: the parent Go
// server receives the operator's SIGTERM/SIGINT and must deliver the stop to the Node
// child (SIGTERM the tree) and wait, rather than exiting immediately and orphaning it.
//
// ctx bounds the wait. Signal delivery is only wired on platforms that have SIGTERM
// (Unix); on Windows Interrupt is the analogue and is enough. Returns a no-op when
// the platform has neither.
func (s *Supervisor) ShutdownOnSignal(ctx context.Context) (stop func()) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case sig := <-signals:
			s.log.Info("termination signal received, stopping node upstream", "signal", sig.String())
			_ = s.Stop(ctx)
		case <-ctx.Done():
		case <-done:
		}
	}()
	return func() {
		signal.Stop(signals)
		close(done)
	}
}
