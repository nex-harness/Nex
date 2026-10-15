// transport.go owns the *process* boundary of one agent: the three stdio pipes,
// the stderr side channel, the stdin-EOF close handshake and the process-tree
// termination. It is the step 3.10.1 half of nexStdioTransport.ts.
//
// What is here and what is not:
//   - Here: spawn wiring through internal/adapters/exec, the stderr line tap with
//     the exit drain, the observed child exit (code + signal), the graceful
//     close (stdin EOF, bounded wait for a natural exit, then whole-tree kill)
//     and the disposal that proves the tree is gone. These are the parts step
//     3.10.1 owns and the parts the crash/hang/zombie tests assert.
//   - Step 3.10.2: request/response correlation, startup and interaction routing
//     on top of the stdout frames. The frame bytes are surfaced here (retained
//     output plus the live line tap) so that client lands on a boundary that
//     already exists; this file deliberately does NOT parse protocol messages.
//
// The reuse boundary is the exec adapter, exactly like internal/adapters/node:
// this file never calls os/exec or signals a pid itself. KillTree is the only
// way a tree dies, and it is the same call that works for the CLI's MCP
// descendants and for the Windows Job Object.
package agentproc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/exec"
)

// Timing constants ported from nexStdioTransport.ts.
const (
	// StdioEOFWait is STDIO_EOF_EXIT_WAIT_MS: how long a stdin EOF is given to
	// end the agent before the tree is killed. `app-server --stdio` exits on
	// stdin EOF by contract, so this is the graceful window; skipping it turns
	// every shutdown into a SIGKILL and strands MCP descendants.
	StdioEOFWait = 1_800 * time.Millisecond
	// DisposeStderrDrain is DISPOSE_STDERR_DRAIN_MS.
	DisposeStderrDrain = 3_250 * time.Millisecond
	// ExitStderrDrain is EXIT_STDERR_DRAIN_MS: at exit the protocol has already
	// failed, so the stderr tail gets a short window and no more.
	ExitStderrDrain = 250 * time.Millisecond
)

// ErrTransportClosed is "Nex agent stdio transport is closed".
var ErrTransportClosed = errors.New("Nex agent stdio transport is closed")

// ExitInfo is the observed child exit (code + signal), i.e. what the TS child
// "exit" event carries. On Windows the Signal is empty because Windows has no
// signals; the code still distinguishes a normal 0 from a crash.
type ExitInfo struct {
	Code   int
	Signal string
}

// child wraps an *exec.Process with the pipes and the observation channels the
// manager drives. It is not safe for concurrent use of the exit channel (each is
// received once by the single manager goroutine that owns the child).
type child struct {
	proc *exec.Process

	// stdin is the READ side of the pipe handed to the child. The exec adapter
	// closes it once the child is gone (it owns the copier goroutine that reads
	// it), so nothing here has to.
	stdin *io.PipeReader
	// stdinWriter is the host's write side. Closing it is the graceful "please stop"
	// the agent contract defines.
	stdinWriter *io.PipeWriter

	// stdoutLines is the exec adapter's live line tap on stdout. It is where the
	// protocol client (3.10.2) reads frames from; the manager ignores it.
	stdoutLines chan string

	// exited delivers the child's terminal state exactly once.
	exited chan ExitInfo

	// stderrLines carries every complete stderr line to the manager, which parses
	// diagnostics and appends to the bounded tail.
	stderrLines chan string

	// done is closed exactly once, when the child has been reaped. It is the
	// broadcast signal ("has it exited?") and is safe to select on from any number
	// of goroutines, unlike exited, which carries the value.
	done chan struct{}

	// exitInfo is the terminal state, published under exitMu before done is closed.
	exitMu   sync.Mutex
	exitInfo ExitInfo
	hasExit  bool

	// observeOnce guards the single reaper.
	observeOnce sync.Once
}

// startChild launches one agent through the exec adapter and wires the pipes.
//
// stdin is an *io.Pipe*: the host keeps the writer, and the reader is what the
// exec adapter hands to the child. Both ends are kept so the teardown can
// unblock the adapter's stdin copier (see abandonStdin).
//
// stdout/stderr are collected by the adapter, with both also tapped line-by-line:
// stdout for the protocol client (3.10.2), stderr for the diagnostics/tail
// collector that has to see a crash line while the process is still alive.
func startChild(ctx context.Context, argv []string, dir string, env []string) (*child, error) {
	stdoutLines := make(chan string, stdoutLineBuffer)
	stderrLines := make(chan string, stdoutLineBuffer)

	// The exec adapter reads the child's stdin from an io.Reader; a pipe gives the
	// host a writer and lets closing that writer deliver a real EOF to the child,
	// which is exactly how `app-server --stdio` is asked to stop.
	stdinReader, stdinWriter := io.Pipe()

	proc, err := exec.Start(ctx, exec.Options{
		Args:        argv,
		Dir:         dir,
		Env:         env,
		Stdin:       stdinReader,
		StdoutLines: stdoutLines,
		StderrLines: stderrLines,
	})
	if err != nil {
		_ = stdinWriter.Close()
		return nil, err
	}
	return &child{
		proc:        proc,
		stdin:       stdinReader,
		stdinWriter: stdinWriter,
		stdoutLines: stdoutLines,
		stderrLines: stderrLines,
		done:        make(chan struct{}),
	}, nil
}

// stdoutLineBuffer is the depth of both line taps. A burst is dropped rather than
// blocking the child (the adapter's SetLineSink never blocks); stderr diagnostics
// are additionally retained as a bounded redacted tail, and stdout is retained by
// the adapter's collector, so a drop degrades live streaming, not correctness.
const stdoutLineBuffer = 64

// PID is the child's process id (its process-group id on Unix).
func (c *child) PID() int { return c.proc.PID() }

// Write sends raw bytes to the child's stdin. The protocol client (3.10.2) frames
// on top of this.
func (c *child) Write(data []byte) error {
	_, err := c.stdinWriter.Write(data)
	return err
}

// CloseStdin delivers the graceful EOF that ends an `app-server --stdio` agent.
func (c *child) CloseStdin() { _ = c.stdinWriter.Close() }

// WaitExit blocks until the child exits and reports its terminal state.
func (c *child) WaitExit() ExitInfo {
	<-c.done
	c.exitMu.Lock()
	defer c.exitMu.Unlock()
	return c.exitInfo
}

// observeExit reaps the child once and publishes its terminal state. It is the
// single reader of the adapter's Wait, started as soon as the child starts, so
// every exit path (crash, kill, natural) is observed exactly once.
//
// It deliberately does NOT close stdin: the protocol keeps stdin open for the
// whole session, and closing it here would stop the agent on the way in. The
// case that worried about it — a child that dies while the host still holds its
// stdin pipe, which would leave os/exec's stdin copier blocked forever — is
// handled inside the adapter by Options.WaitDelay.
func (c *child) observeExit() {
	c.observeOnce.Do(func() {
		result := c.proc.Wait()
		c.exitMu.Lock()
		c.exitInfo = ExitInfo{Code: result.ExitCode, Signal: result.Signal}
		c.exitMu.Unlock()
		// 修复依据：两个 line channel 是 exec adapter 的一次性 tap，进程退出后
		// 不会再有新行。不关闭它们，pumpStderr 的 for-range 会永久阻塞在空
		// channel 上，observeExit 里的 drainStderr 也等不到静默。每次 spawn 泄漏
		// 两个 goroutine（pump + drain），长期运行的 host 会积累到不可忽略。
		// 在 done 之前关闭，使 <-done 的等待者看到的是已经安静的 channel 状态。
		c.proc.Stdout().SetLineSink(nil)
		c.proc.Stderr().SetLineSink(nil)
		close(c.stdoutLines)
		close(c.stderrLines)
		// Closing done after publishing the value is what makes "read the value after
		// <-done" safe without a further lock.
		close(c.done)
	})
}

// terminateAndReap performs the graceful close and proves the tree is gone. It
// ports disposeAndWaitOnce: request stdin EOF, wait up to StdioEOFWait for a
// natural exit, then kill the whole tree regardless (descendants such as MCP
// servers may outlive the root) and wait for the terminal state.
//
// It returns an error only when the process tree could not be reaped, so a caller
// can report real residue instead of silently dropping ownership.
func (c *child) terminateAndReap(ctx context.Context, alreadyExited bool) error {
	if !alreadyExited {
		c.CloseStdin()
		c.waitForExit(StdioEOFWait)
	}
	// KillTree escalates SIGTERM→SIGKILL across the whole group (Unix) / Job Object
	// (Windows) and only returns once the leader is reaped. A descendant that
	// ignored SIGTERM is force-killed inside it.
	if err := c.proc.KillTree(ctx); err != nil {
		return err
	}
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return errors.Join(ctx.Err(), errTreeNotReaped(c.PID()))
	}
}

// errTreeNotReaped is the residue error: the tree did not reach a terminal state
// within the caller's budget after SIGKILL. The TS reports remaining pids the same
// way; here the pid is enough to make the residue traceable.
func errTreeNotReaped(pid int) error {
	return fmt.Errorf("nex agent: process tree cleanup incomplete; remaining pid=%d", pid)
}

// waitForExit waits up to timeout for the child to exit on its own. It only
// observes c.done, which is broadcast, so several waiters are fine.
func (c *child) waitForExit(timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-c.done:
	case <-timer.C:
	}
}

// exitedSignal is the broadcast "has exited" signal, safe to select on from any
// number of goroutines.
func (c *child) exitedSignal() <-chan struct{} { return c.done }

// IsAlive reports whether the child has already been observed to exit. It is the
// best-effort check the "is this still the active instance" guards use.
func (c *child) IsAlive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}
