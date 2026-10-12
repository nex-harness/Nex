// Package exec is the process adapter: the only place in the Go agent that
// spawns a child process, drains its output and terminates a process tree.
//
// Behaviour mirrors apps/nex-cli/packages/adapters/src/exec/:
//   - node-execution-adapter-process.ts spawns with `detached: platform !==
//     "win32"`; here the same boundary is `SysProcAttr.Setpgid` on Unix and a
//     Job Object on Windows (see killtree_unix.go / killtree_windows.go);
//   - output-collector.ts keeps an inline head capped at maxInlineBytes, a tail
//     of maxTailBytes and counts every byte that arrived;
//   - process-tree.ts signals the negative PGID, falls back to the bare pid and
//     then sweeps the `ps` snapshot, because job control can move workers out
//     of the root process group.
//
// Nothing here interprets a shell string: callers pass argv, exactly like the
// argv execution mode of the TypeScript adapter.
package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	osexec "os/exec"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultMaxOutputBytes is the inline cap per stream when
	// Options.MaxOutputBytes is 0.
	DefaultMaxOutputBytes int64 = 1 << 20

	// DefaultMaxTailBytes is the tail cap per stream (0 disables the tail).
	DefaultMaxTailBytes int64 = 4 << 10

	// DefaultTermGrace is how long a signalled tree may take to disappear
	// before SIGKILL is sent to the group. Wait only returns once the last
	// writer of the stdout/stderr pipes is gone, so this sits far below the
	// 500 ms the spec allows for observing a cancellation.
	DefaultTermGrace = 150 * time.Millisecond

	// DescendantLookupTimeout mirrors POSIX_PROCESS_LOOKUP_TIMEOUT_MS in
	// process-tree.ts: the process-table snapshot is best effort and must never
	// hold up a kill.
	DescendantLookupTimeout = 500 * time.Millisecond
)

// Options is one execution request.
type Options struct {
	// Args is the argv to execute; Args[0] is resolved on PATH.
	Args []string
	// Dir is the working directory (empty means the caller's cwd).
	Dir string
	// Env is the child environment; nil inherits the parent environment.
	Env []string
	// Stdin is optional; nil closes the child's stdin immediately.
	Stdin io.Reader

	// MaxOutputBytes caps the inline output kept per stream (0 ->
	// DefaultMaxOutputBytes). Bytes past the cap are counted but dropped, never
	// buffered, so a chatty child can never block on a full pipe.
	MaxOutputBytes int64
	// MaxTailBytes caps the tail kept per stream (0 -> DefaultMaxTailBytes,
	// negative disables it).
	MaxTailBytes int64

	// TermGrace overrides DefaultTermGrace for KillTree.
	TermGrace time.Duration
}

func (o Options) normalized() Options {
	if o.MaxOutputBytes < 0 {
		o.MaxOutputBytes = 0
	}
	if o.MaxOutputBytes == 0 {
		o.MaxOutputBytes = DefaultMaxOutputBytes
	}
	switch {
	case o.MaxTailBytes == 0:
		o.MaxTailBytes = DefaultMaxTailBytes
	case o.MaxTailBytes < 0:
		o.MaxTailBytes = 0
	}
	if o.TermGrace < 0 {
		o.TermGrace = 0
	} else if o.TermGrace == 0 {
		o.TermGrace = DefaultTermGrace
	}
	return o
}

// Result is what Wait reports about a finished process.
type Result struct {
	// PID is the child's process id, which is also its process-group id on Unix.
	PID int
	// ExitCode is the child's exit status (-1 when it was signalled).
	ExitCode int
	// Signal is the signal that ended the child, or "" when it exited normally.
	Signal string
	// Stdout and Stderr carry the collected output of each stream.
	Stdout Output
	Stderr Output
	// WaitErr is the raw error the child reported: non-nil for a non-zero exit
	// and for a signal, nil for a clean exit.
	WaitErr error
}

// Killed reports whether the child ended from a signal (on Windows, from a
// terminate call; the field stays empty there because Windows has no signals).
func (r Result) Killed() bool { return r.Signal != "" }

// Process is a running child.
type Process struct {
	cmd       *osexec.Cmd
	pid       int
	stdout    *OutputCollector
	stderr    *OutputCollector
	termGrace time.Duration

	done     chan struct{}
	result   Result
	watchCtx context.Context

	// terminated records that KillTree ran, so the platform hook can tell a
	// cancelled process (kill the whole job) from one that exited on its own.
	terminated atomic.Bool
	// treeErr is the Windows job-assignment failure kept for the taskkill
	// fallback; it is nil on Unix, where Setpgid cannot fail.
	treeErr error

	killOnce sync.Once
	killDone chan struct{}
	killErr  error
}

// Start launches the command and returns immediately. Cancelling ctx
// terminates the whole process tree; Wait then reports the signal.
func Start(ctx context.Context, opts Options) (*Process, error) {
	o := opts.normalized()
	if len(o.Args) == 0 {
		return nil, errors.New("exec: Options.Args must not be empty")
	}
	if ctx == nil {
		return nil, errors.New("exec: nil context")
	}
	cmd := osexec.Command(o.Args[0], o.Args[1:]...)
	cmd.Dir = o.Dir
	cmd.Env = o.Env
	cmd.Stdin = o.Stdin
	p := &Process{
		cmd:       cmd,
		stdout:    NewOutputCollector(o.MaxOutputBytes, o.MaxTailBytes),
		stderr:    NewOutputCollector(o.MaxOutputBytes, o.MaxTailBytes),
		termGrace: o.TermGrace,
		done:      make(chan struct{}),
		killDone:  make(chan struct{}),
		watchCtx:  ctx,
	}
	cmd.Stdout = p.stdout
	cmd.Stderr = p.stderr
	// Platform hook 1: the attributes that make the tree addressable.
	prepareSpawn(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("exec: start %q: %w", o.Args[0], err)
	}
	p.pid = cmd.Process.Pid
	// Platform hook 2: Windows puts the child in a Job Object here. A failure
	// is kept, not fatal: killTree falls back to `taskkill /T /F`, which is what
	// the TypeScript adapter uses on Windows.
	p.treeErr = attachProcessTree(p)
	go p.waitLoop()
	go p.watchCancellation(ctx)
	return p, nil
}

// PID is the child's process id (its process-group id on Unix).
func (p *Process) PID() int { return p.pid }

// Stdout is the live collector of the child's standard output.
func (p *Process) Stdout() *OutputCollector { return p.stdout }

// Stderr is the live collector of the child's standard error.
func (p *Process) Stderr() *OutputCollector { return p.stderr }

// Wait blocks until the child has exited and its output pipes have closed, and
// returns the outcome. It is safe to call more than once and from several
// goroutines.
func (p *Process) Wait() Result {
	<-p.done
	return p.result
}

// KillTree terminates the child and every descendant it can reach, first with
// SIGTERM and then, once TermGrace has elapsed, with SIGKILL. It is idempotent:
// later calls join the first one. A tree that is already gone is not an error.
func (p *Process) KillTree(ctx context.Context) error {
	// The cancellation path calls this with an already-cancelled context, so the
	// kill itself runs detached from that cancellation and is bounded by
	// TermGrace instead.
	if ctx != nil && ctx.Err() != nil {
		ctx = context.WithoutCancel(ctx)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.killOnce.Do(func() {
		defer close(p.killDone)
		p.killErr = p.killTree(ctx)
	})
	<-p.killDone
	return p.killErr
}

func (p *Process) killTree(ctx context.Context) error {
	p.terminated.Store(true)
	termErr := p.signalTree(ctx, terminateSignal)
	// Return as soon as the leader is reaped, but never wait longer than the
	// grace period: a descendant that ignored SIGTERM (or that job control moved
	// into another process group) still has to be cleaned up.
	timer := time.NewTimer(p.termGrace)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
	case <-ctx.Done():
	}
	return errors.Join(termErr, p.signalTree(ctx, killSignal))
}

// watchCancellation turns the caller's context cancellation into a kill of the
// whole tree.
//
// It must be armed before the child runs, not from waitLoop: waitLoop only gets
// control after cmd.Wait has returned, which is by definition too late to
// cancel anything. KillTree detaches itself from the cancelled context and
// bounds itself by TermGrace instead, so the cancellation is never dropped.
func (p *Process) watchCancellation(ctx context.Context) {
	select {
	case <-ctx.Done():
		_ = p.KillTree(ctx)
	case <-p.done:
	}
}

// waitLoop reaps the child, publishes the Result and releases the platform
// resources the tree was holding.
func (p *Process) waitLoop() {
	waitErr := p.cmd.Wait()
	state := p.cmd.ProcessState
	res := Result{PID: p.pid, Stdout: p.stdout.Result(), Stderr: p.stderr.Result(), WaitErr: waitErr}
	if state != nil {
		res.ExitCode = state.ExitCode()
		res.Signal = exitedSignal(state)
	}
	p.result = res
	close(p.done)
	// The platform hook releases the Job Object here: closing a job handle
	// created with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE terminates everything still
	// in it, which is only wanted when the tree was cancelled (releaseProcessTree
	// drops the limit flag first on a normal exit).
	releaseProcessTree(p.pid, p.terminated.Load())
}
