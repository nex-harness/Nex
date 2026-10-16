// agenttransport.go binds the protocol client to one spawned agent process: it
// turns agentproc.Client's raw stdin/stdout byte boundary into the framed message
// boundary the client speaks, and it is the only place in this package that knows
// a process exists.
//
// Nothing here parses a protocol message — internal/protocol owns that, and the
// client owns the message dispatch. What lives here is the wiring and the two
// failure edges the client must learn about: a write failure, and the protocol leg
// ending (process exit or stdout EOF) with a reason.
package proto

import (
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/tylinux/nex/go/internal/agentproc"
	"github.com/tylinux/nex/go/internal/protocol"
)

// AgentProcess is the slice of agentproc.Client this transport needs. Keeping it
// an interface is what lets the same client run against the real binary and
// against a fake process in tests without either being a special case.
type AgentProcess interface {
	// Send writes raw bytes to the agent's stdin.
	Send(data []byte) error
	// StdoutLines delivers every complete stdout line while the agent runs. It is
	// closed when the process exits.
	StdoutLines() <-chan string
	// Exited is closed when the process has been reaped.
	Exited() <-chan struct{}
	// ExitInfo returns the terminal state once Exited is closed.
	ExitInfo() agentproc.ExitInfo
	// IsAlive reports whether the process is still running.
	IsAlive() bool
	// Dispose requests the graceful close (stdin EOF, bounded wait, then kill).
	Dispose()
}

// ProcessTransport frames protocol messages over one agent process.
type ProcessTransport struct {
	proc AgentProcess

	writer *protocol.FrameWriter

	frames  chan string
	closeCh chan struct{}

	mu         sync.Mutex
	closeOnce  sync.Once
	reason     string
	exitedDone chan struct{}
}

// NewProcessTransport starts the frame pump for a spawned agent. It returns
// immediately; the transport is usable as soon as it is returned.
func NewProcessTransport(proc AgentProcess) *ProcessTransport {
	t := &ProcessTransport{
		proc:       proc,
		writer:     protocol.NewFrameWriter(writerFunc(func(p []byte) error { return proc.Send(p) })),
		frames:     make(chan string, 64),
		closeCh:    make(chan struct{}),
		exitedDone: make(chan struct{}),
	}
	go t.pump()
	return t
}

// writerFunc adapts a write function to io.Writer for the frame writer.
type writerFunc func(p []byte) error

func (f writerFunc) Write(p []byte) (int, error) {
	if err := f(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Send encodes one message and writes it as one LF-terminated frame.
func (t *ProcessTransport) Send(msg protocol.Message) error {
	select {
	case <-t.closeCh:
		return ErrTransportClosed
	default:
	}
	if err := t.writer.WriteMessage(msg); err != nil {
		if errors.Is(err, agentproc.ErrTransportClosed) {
			return ErrTransportClosed
		}
		return err
	}
	return nil
}

// Frames delivers the agent's stdout lines. The channel closes when the protocol
// leg ends.
func (t *ProcessTransport) Frames() <-chan string { return t.frames }

// CloseReason reports why the leg ended.
func (t *ProcessTransport) CloseReason() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.reason
}

// Close asks the agent to stop. It does not wait: the pump goroutine ends when
// the process actually exits, which is what the client observes as the leg ending.
func (t *ProcessTransport) Close() {
	t.proc.Dispose()
}

// WaitProcess blocks until the transport's pump goroutine has finished (used by
// tests asserting no goroutine leak after a close).
func (t *ProcessTransport) WaitProcess() { <-t.exitedDone }

// pump is the single reader of stdout. It ends on the first of: the process
// exiting, stdout ending, or the client asking for a close. The reason it records
// is what the pending waiters see.
func (t *ProcessTransport) pump() {
	defer close(t.exitedDone)
	defer close(t.frames)

	lines := t.proc.StdoutLines()
	if lines == nil {
		t.end("agent has no stdout stream")
		return
	}
	exited := t.proc.Exited()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				// stdoutLines is closed by the manager when the child is reaped,
				// so this is the process ending. The terminal state is published
				// before that close, so reading it here cannot block.
				t.end(t.exitReason())
				return
			}
			select {
			case t.frames <- line:
			case <-t.closeCh:
				return
			default:
				// A burst larger than the buffer would otherwise block the agent's
				// stdout reader. Frames are dropped rather than stalling the
				// process; the assembler's ordinal rules make a dropped fragment
				// a typed fault on the next frame, never a silently wrong frame.
				t.frames <- line
			}
		case <-exited:
			// The process is reaped, so the terminal state is observable. Give the
			// line tap a moment to deliver whatever the agent wrote before dying:
			// a final response is a real answer, and dropping it would turn a
			// successful turn into a spurious transport error.
			t.drainLines(lines)
			t.end(t.exitReason())
			return
		case <-t.closeCh:
			t.end("")
			return
		}
	}
}

// drainLines moves whatever the line tap still holds into the frame channel, so a
// response written just before the process exited is still delivered. It gives up
// rather than block: a tap that is already empty ends the drain immediately, and
// a producer that is stuck must not keep the leg open.
func (t *ProcessTransport) drainLines(lines <-chan string) {
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return
			}
			select {
			case t.frames <- line:
			case <-t.closeCh:
				return
			}
		case <-time.After(drainGrace):
			return
		}
	}
}

// drainGrace bounds the post-exit drain. It is short on purpose: the protocol has
// already failed, and a wedged writer must not keep the leg alive.
const drainGrace = 50 * time.Millisecond

// end records the close reason once.
func (t *ProcessTransport) end(reason string) {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.reason = reason
		t.mu.Unlock()
		close(t.closeCh)
	})
}

// exitReason renders the agent's terminal state as the TS transport close reason
// carries it, without inventing a signal on Windows.
func (t *ProcessTransport) exitReason() string {
	info := t.proc.ExitInfo()
	if info.Signal != "" {
		return "agent_exited: signal=" + info.Signal
	}
	if info.Code != 0 {
		return "agent_exited: code=" + strconv.Itoa(info.Code)
	}
	return "agent_exited"
}

// NewAgentClient builds the full stack for one spawned agent: the process
// transport plus the protocol client on top of it. This is the only constructor
// production code needs.
func NewAgentClient(proc *agentproc.Client, opts Options) *Client {
	return New(NewProcessTransport(proc), opts)
}
