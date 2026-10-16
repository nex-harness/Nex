// faketransport_test.go is the in-memory agent a fake protocol client talks to.
// It is a real transport with a real frame stream (nothing about the client's
// correlation logic changes when the other end is a buffer), and it can be
// scripted to do the things that matter: answer out of order, answer late, answer
// with an error, ask the host an interaction request, or never answer at all.
package proto

import (
	"encoding/json"
	"sync"

	"github.com/tylinux/nex/go/internal/protocol"
)

// fakeTransport is a scripted agent.
type fakeTransport struct {
	mu sync.Mutex
	// sent records every message the client wrote, in order.
	sent []protocol.Message
	// handlers answer requests by method. A method with no handler is ignored
	// (which is how "never answers" is scripted).
	handlers map[string]func(protocol.Message) []protocol.Message
	// injected are frames pushed at the client without the client asking.
	injected []string

	frames   chan string
	closeCh  chan struct{}
	closed   bool
	reason   string
	sendErr  error
	sendHook func(protocol.Message)
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{
		handlers: map[string]func(protocol.Message) []protocol.Message{},
		frames:   make(chan string, 64),
		closeCh:  make(chan struct{}),
	}
}

// on registers the agent's answer for a method. The fn returns the frames the
// agent writes back, in order; returning nil means "stay silent".
func (f *fakeTransport) on(method string, fn func(protocol.Message) []protocol.Message) *fakeTransport {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[method] = fn
	return f
}

// reply registers a one-shot successful answer.
func (f *fakeTransport) reply(method string, result any) *fakeTransport {
	raw, _ := json.Marshal(result)
	return f.on(method, func(req protocol.Message) []protocol.Message {
		return []protocol.Message{{Kind: protocol.KindResponse, ID: req.ID, Result: raw}}
	})
}

// fail registers a one-shot error answer.
func (f *fakeTransport) fail(method string, code int, message string) *fakeTransport {
	return f.on(method, func(req protocol.Message) []protocol.Message {
		return []protocol.Message{{Kind: protocol.KindError, ID: req.ID, Error: &protocol.RPCError{Code: code, Message: message}}}
	})
}

// inject pushes a frame to the client out of band (an unsolicited notification, or
// a response the client is not waiting for).
func (f *fakeTransport) inject(line string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.injected = append(f.injected, line)
}

// requestFromAgent makes the agent send a request (agent→host) to the client.
func (f *fakeTransport) requestFromAgent(id int64, method string, params any) *fakeTransport {
	raw, _ := json.Marshal(params)
	f.on(method, func(protocol.Message) []protocol.Message {
		return []protocol.Message{{Kind: protocol.KindRequest, ID: protocol.IntID(id), Method: method, Params: raw, HasParams: true}}
	})
	return f
}

// Send satisfies Transport. It records the message, runs the scripted handler and
// delivers whatever frames it produced.
func (f *fakeTransport) Send(msg protocol.Message) error {
	f.mu.Lock()
	f.sent = append(f.sent, msg)
	hook, handler, err := f.sendHook, f.handlers[msg.Method], f.sendErr
	f.mu.Unlock()
	if hook != nil {
		hook(msg)
	}
	if err != nil {
		return err
	}
	if handler == nil {
		return nil
	}
	for _, out := range handler(msg) {
		f.deliver(out)
	}
	return nil
}

// deliver writes one outbound frame to the client, exactly as an agent would.
func (f *fakeTransport) deliver(msg protocol.Message) {
	raw, err := protocol.Encode(msg)
	if err != nil {
		panic(err)
	}
	f.Push(string(raw))
}

// Push injects a raw frame line.
func (f *fakeTransport) Push(line string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.frames <- line
}

// Frames satisfies Transport.
func (f *fakeTransport) Frames() <-chan string { return f.frames }

// CloseReason satisfies Transport.
func (f *fakeTransport) CloseReason() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reason
}

// Close satisfies Transport: the host asked the leg to end. It records no reason
// of its own — like the real process transport, the reason belongs to whatever
// actually ended the leg (a parse error, an exit, an EOF), and the first cause is
// the root cause.
func (f *fakeTransport) Close() { f.End("") }

// End ends the protocol leg from the agent side, which is what a crash or a
// stdout EOF looks like to the host.
func (f *fakeTransport) End(reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.closed = true
	f.reason = reason
	close(f.frames)
}

// EndOfFrames closes the frame channel without setting a reason (an agent that
// closed stdout but is still alive).
func (f *fakeTransport) EndOfFrames() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.closed = true
	close(f.frames)
}

// Sent returns the messages the client wrote.
func (f *fakeTransport) Sent() []protocol.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]protocol.Message(nil), f.sent...)
}

// SentMethods returns the methods the client wrote, in order.
func (f *fakeTransport) SentMethods() []string {
	var out []string
	for _, m := range f.Sent() {
		out = append(out, m.Kind.String()+":"+m.Method)
	}
	return out
}

// SetSendError makes the next and subsequent writes fail (a dead stdin).
func (f *fakeTransport) SetSendError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendErr = err
}

// SetSendHook installs a callback run on every write.
func (f *fakeTransport) SetSendHook(fn func(protocol.Message)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendHook = fn
}
