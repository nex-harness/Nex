//go:build !windows

// Unix half of the process-tree hooks declared in killtree.go. The boundary is
// the same one node-execution-adapter-process.ts draws with
// `detached: platform !== "win32"` plus a negative-pgid kill: the child is put
// in its own process group and a tree is signalled by its group id.
package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"sync"
	"syscall"
	"time"
)

// execCmd is the name killtree.go uses for the command it prepares; it is an
// alias, so *osexec.Cmd values stay interchangeable across the platform files.
type execCmd = osexec.Cmd

const (
	// processGonePollInterval is how often signal 0 asks the kernel whether the
	// group leader is still there. It has to be short: it is the tail of the
	// latency budget between "the tree is gone" and "kill returns".
	processGonePollInterval = 25 * time.Millisecond
	// processGoneMaxPoll bounds the watcher so a pid that outlives the tree (an
	// unreaped zombie owned by another process, or a kill that lost its race
	// with an exec) cannot keep a goroutine polling forever.
	processGoneMaxPoll = 30 * time.Second
)

// prepareSpawnPlatform puts the child in a fresh process group, which makes it
// the group leader: kill(-pid) then reaches every descendant that did not move
// itself into another group.
func prepareSpawnPlatform(cmd *execCmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// attachProcessTreePlatform is a no-op on Unix: Setpgid already made the tree
// addressable at spawn time and it cannot fail. Windows assigns a Job Object
// here instead.
func attachProcessTreePlatform(*Process) error { return nil }

// releaseProcessTreePlatform is a no-op on Unix: the process group is released
// by the kernel when its last member exits.
func releaseProcessTreePlatform(int, bool) {}

// signalTree terminates the tree this Process owns.
func (p *Process) signalTree(ctx context.Context, sig Signal) error {
	return signalProcessTree(ctx, p.pid, sig)
}

// signalProcessTree sends sig to the process group led by pid, falling back to
// the bare pid when the group does not exist.
//
// The fallback is not defensive padding: Start and a caller that only knows the
// pid can race, and a child that never reached setpgid has no group to signal.
// It mirrors the group-then-pid cascade of killPosixProcessTree in
// apps/nex-cli/packages/adapters/src/exec/process-tree.ts.
func signalProcessTree(_ context.Context, pid int, sig Signal) error {
	if pid <= 1 {
		return fmt.Errorf("exec: refusing to signal process tree of pid %d", pid)
	}
	if err := syscall.Kill(-pid, sig.sysSignal()); err == nil {
		return nil
	} else if !errors.Is(err, syscall.ESRCH) {
		// EPERM and friends are real failures: the group exists but is not ours.
		return fmt.Errorf("exec: %s process group -%d: %w", sig, pid, err)
	}
	if err := syscall.Kill(pid, sig.sysSignal()); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("exec: %s pid %d: %w", sig, pid, err)
	}
	// ESRCH on both: the tree is already gone, which is not an error.
	return nil
}

// processTreeGone reports when the leader pid has left the process table.
//
// It only answers for the leader, which is what the callers need (they use it
// to decide whether to escalate); descendants that moved to another process
// group are reaped by their own parent or by init.
//
// ctx cancellation is not an argument: killProcessTree already selects on
// ctx.Done() next to this channel, which bounds the caller's wait without
// bounding this watcher.
func processTreeGone(pid int) <-chan struct{} {
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		ticker := time.NewTicker(processGonePollInterval)
		defer ticker.Stop()
		deadline := time.NewTimer(processGoneMaxPoll)
		defer deadline.Stop()
		for {
			// Signal 0 performs the permission and existence checks without
			// delivering anything: ESRCH means the leader is gone, EPERM means it
			// exists and belongs to somebody else.
			if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
				return
			}
			select {
			case <-ticker.C:
			case <-deadline.C:
				return
			}
		}
	}()
	return gone
}

// graceTimer is the deadline killProcessTree waits on between the two signal
// phases. It exists so the call site reads as `timer.done()` / `defer
// timer.stop()` and so stop() stays safe when the timer already fired.
type graceTimer struct {
	timer *time.Timer
	once  sync.Once
}

func newGraceTimer(grace time.Duration) *graceTimer {
	return &graceTimer{timer: time.NewTimer(time.Duration(grace))}
}

// done is the channel the caller selects on; a fired timer delivers once.
func (g *graceTimer) done() <-chan time.Time { return g.timer.C }

// stop releases the timer. Calling it twice, or after the timer fired, is safe.
func (g *graceTimer) stop() {
	g.once.Do(func() {
		if !g.timer.Stop() {
			// Drain a pending tick so the runtime can release the timer.
			select {
			case <-g.timer.C:
			default:
			}
		}
	})
}

// signalNames maps the signals this package can produce (and the neighbours a
// caller may observe) to their conventional names.
//
// It is an explicit table rather than a transformation of
// syscall.Signal.String(), which reports the signal as a past participle
// ("terminated", "killed", "interrupt") and would yield "SIGTERMINATED".
var signalNames = map[syscall.Signal]string{
	syscall.SIGHUP:  "SIGHUP",
	syscall.SIGINT:  "SIGINT",
	syscall.SIGQUIT: "SIGQUIT",
	syscall.SIGKILL: "SIGKILL",
	syscall.SIGTERM: "SIGTERM",
	syscall.SIGABRT: "SIGABRT",
	syscall.SIGSEGV: "SIGSEGV",
	syscall.SIGPIPE: "SIGPIPE",
	syscall.SIGALRM: "SIGALRM",
	syscall.SIGBUS:  "SIGBUS",
	syscall.SIGFPE:  "SIGFPE",
	syscall.SIGUSR1: "SIGUSR1",
	syscall.SIGUSR2: "SIGUSR2",
	syscall.SIGCHLD: "SIGCHLD",
	syscall.SIGCONT: "SIGCONT",
	syscall.SIGSTOP: "SIGSTOP",
	syscall.SIGTSTP: "SIGTSTP",
	syscall.SIGTTIN: "SIGTTIN",
	syscall.SIGTTOU: "SIGTTOU",
}

// exitedSignal names the signal that ended the process, or "" when it exited
// normally. Result.Killed reads that name, and the bash tool turns it into the
// `killed` stop state of node-execution-adapter-process.ts.
//
// os.ProcessState exposes the wait status only through Sys(), which is a
// syscall.WaitStatus on Unix; a state the platform does not describe that way
// yields "" instead of a panic, because Wait may be called on a Process whose
// cmd never started.
func exitedSignal(state *os.ProcessState) string {
	if state == nil {
		return ""
	}
	ws, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return ""
	}
	if name, known := signalNames[ws.Signal()]; known {
		return name
	}
	// The real-time range has no table entry above; a number still tells the
	// reader more than an empty Result.Signal would.
	return fmt.Sprintf("signal %d", int(ws.Signal()))
}

// sysSignal is the platform signal for each phase. Unix has a real escalation
// (SIGTERM then SIGKILL); on Windows both phases collapse to one terminate.
func (s Signal) sysSignal() syscall.Signal {
	if s == killSignal {
		return syscall.SIGKILL
	}
	return syscall.SIGTERM
}
