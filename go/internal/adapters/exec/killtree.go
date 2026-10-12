package exec

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Signal is the platform-level request a tree is asked to die from. On Unix it
// is a real signal; on Windows the distinction collapses to "terminate the job
// object", because Windows has no signals and the first phase is already final.
type Signal int

const (
	// terminateSignal asks politely (SIGTERM on Unix).
	terminateSignal Signal = iota
	// killSignal is the escalation (SIGKILL on Unix).
	killSignal
)

func (s Signal) String() string {
	if s == killSignal {
		return "kill"
	}
	return "terminate"
}

// prepareSpawn sets the process attributes that make a tree addressable: a
// fresh process group on Unix (the child becomes its own group leader, so
// `kill(-pid)` reaches every descendant that did not move itself), and the
// creation flags on Windows.
func prepareSpawn(cmd *execCmd) { prepareSpawnPlatform(cmd) }

// attachProcessTree does the work that needs the running process (Windows job
// assignment). It returns nil on success and a non-nil error when the tree could
// not be tracked, in which case killTree falls back to `taskkill /T /F`, the
// same fallback the TypeScript adapter uses.
func attachProcessTree(p *Process) error { return attachProcessTreePlatform(p) }

// releaseProcessTree frees the per-process resources (the Windows job handle)
// once the child has exited and its output pipes are closed. killed says the
// tree was terminated on purpose.
func releaseProcessTree(pid int, killed bool) { releaseProcessTreePlatform(pid, killed) }

// KillProcessTree terminates pid and every descendant it can reach, using the
// platform convention this package was built with: the negative process group
// on Unix (mirroring process-tree.ts), a Job Object on Windows.
//
// It is the standalone entry point for callers that hold only a pid (a tool
// that timed out, a process the host asked to stop). The signal is escalated
// automatically: a tree that is still alive after DefaultTermGrace is killed.
// Calling it for a pid that no longer exists is not an error.
//
// pid <= 1 is refused: pid 1 is init and 0 addresses the caller's own process
// group, so a signal there would take the agent down with it.
func KillProcessTree(ctx context.Context, pid int) error {
	return killProcessTree(ctx, pid, DefaultTermGrace)
}

// killProcessTree is the shared body; the platform files implement
// signalProcessTree.
func killProcessTree(ctx context.Context, pid int, grace time.Duration) error {
	if pid <= 1 {
		return fmt.Errorf("exec: refusing to signal process tree of pid %d", pid)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		// A cancelled caller must still get its tree killed, so the tree work is
		// detached from the caller's cancellation and bounded by grace instead.
		ctx = context.WithoutCancel(ctx)
	}
	termErr := signalProcessTree(ctx, pid, terminateSignal)
	timer := newGraceTimer(grace)
	defer timer.stop()
	select {
	case <-processTreeGone(pid):
	case <-timer.done():
	case <-ctx.Done():
	}
	return errors.Join(termErr, signalProcessTree(ctx, pid, killSignal))
}
