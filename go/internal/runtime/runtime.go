package runtime

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/tylinux/nex/go/internal/emitters"
	"github.com/tylinux/nex/go/internal/eventstore"
	"github.com/tylinux/nex/go/internal/projection"
	"github.com/tylinux/nex/go/internal/protocol"
	"github.com/tylinux/nex/go/internal/tools"
	"github.com/tylinux/nex/go/internal/v4pub"
)

// Methods the runtime answers.
const (
	MethodV4Command           = "v4/command"
	MethodConversationSub     = "v4/conversation/subscribe"
	MethodConversationUnsub   = "v4/conversation/unsubscribe"
	MethodConversationResync  = "v4/conversation/resync"
	MethodRuntimeCapabilities = "runtime/capabilities"
)

// Runtime owns the sessions of one agent process. It is the protocol handler:
// the frame reader calls HandleRequest/HandleNotification and routes answers to
// agent-initiated requests back through Deliver.
//
// Concurrency: all session state (event store, projection, publisher, telemetry)
// is mutated only while holding runMu, so the single-threaded state machines are
// safe. Agent→host requests block the calling handler goroutine but not the
// reader, which keeps reading and resolves them via Deliver.
type Runtime struct {
	deps      Deps
	rootTrace string
	// ctx is the runtime's base context (used by tool execution).
	ctx context.Context
	// version is the agent version written into the session row.
	version string

	// runMu serialises all session-state mutation and frame emission.
	runMu sync.Mutex

	// mu guards the maps and the pending table.
	mu       sync.Mutex
	sessions map[string]*session
	pending  map[string]chan hostReply
	prefsSeq int

	// postResponseQueue holds callbacks that run after the current response is
	// written (the request-scoped post-response outbox).
	postResponseQueue []func()

	// turnStarter runs a queued turn; replaced in tests.
	turnStarter func(ctx context.Context, s *session, inputID string)
}

type hostReply struct {
	result json.RawMessage
	err    error
}

// New builds a runtime. rootTraceID is stamped on every event.
func New(deps Deps, rootTraceID, version string) *Runtime {
	r := &Runtime{
		deps:      deps,
		rootTrace: rootTraceID,
		version:   version,
		sessions:  map[string]*session{},
		pending:   map[string]chan hostReply{},
	}
	r.turnStarter = func(ctx context.Context, s *session, inputID string) { r.runTurn(ctx, s, inputID) }
	return r
}

// Lock/Unlock implement protocol.RequestSerializer: the frame reader holds runMu
// around each request handler and its response write, so responses are written
// in handler-completion order and the turn goroutine cannot interleave.
func (r *Runtime) Lock()   { r.runMu.Lock() }
func (r *Runtime) Unlock() { r.runMu.Unlock() }

// HandleRequest implements protocol.Handler. The reader calls it while holding
// runMu (via RequestSerializer). A handler may block on a host answer; the
// reader runs handlers on their own goroutine, so the answer still arrives.
func (r *Runtime) HandleRequest(ctx context.Context, req protocol.Message) ([]byte, error) {
	if r.ctx == nil {
		r.ctx = ctx
	}
	switch req.Method {
	case MethodV4Command:
		return r.handleV4Command(ctx, req)
	case MethodConversationSub:
		return r.handleSubscribe(ctx, req)
	case MethodConversationUnsub:
		return r.handleUnsubscribe(ctx, req)
	case MethodConversationResync:
		return r.handleResync(ctx, req)
	case MethodRuntimeCapabilities:
		return json.Marshal(map[string]bool{"independentPlanState": true})
	}
	return nil, &protocol.RPCError{Code: protocol.CodeMethodNotFound, Message: "Method not found: " + req.Method}
}

// HandleNotification implements protocol.Handler.
func (r *Runtime) HandleNotification(context.Context, protocol.Message) {}

// HandleResponse routes a host answer to the waiting request. The reader calls
// it for KindResponse/KindError frames.
func (r *Runtime) HandleResponse(resp protocol.Message) {
	key := resp.ID.String()
	r.mu.Lock()
	ch := r.pending[key]
	if ch != nil {
		delete(r.pending, key)
	}
	r.mu.Unlock()
	if ch == nil {
		return
	}
	if resp.Kind == protocol.KindError && resp.Error != nil {
		ch <- hostReply{err: &protocol.RPCError{Code: resp.Error.Code, Message: resp.Error.Message, Data: resp.Error.Data}}
		return
	}
	ch <- hostReply{result: resp.Result}
}

// request sends an agent-initiated request and waits for the answer.
func (r *Runtime) Request(ctx context.Context, msg protocol.Message) (json.RawMessage, error) {
	key := msg.ID.String()
	ch := make(chan hostReply, 1)
	r.mu.Lock()
	r.pending[key] = ch
	r.mu.Unlock()
	r.write(msg)
	select {
	case <-ctx.Done():
		r.mu.Lock()
		delete(r.pending, key)
		r.mu.Unlock()
		return nil, ctx.Err()
	case reply := <-ch:
		if reply.err != nil {
			return nil, reply.err
		}
		return reply.result, nil
	}
}

func (r *Runtime) session(id string) (*session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		return nil, &protocol.RPCError{Code: protocol.CodeInvalidParams, Message: "unknown session: " + id}
	}
	return s, nil
}

// newSession builds and registers a session.
func (r *Runtime) newSession(id, logEpoch string, ws Workspace) (*session, error) {
	proj := projection.New(id, logEpoch)
	pub, err := v4pub.NewPublisher(id, logEpoch, v4pub.Options{
		Now:    r.deps.Clock.NowMs,
		Source: newProjectionSource(proj),
	})
	if err != nil {
		return nil, err
	}
	s := &session{
		rt: r, id: id, ws: ws,
		events:  eventstore.NewInMemory(eventstore.RetentionTurnWindow, r.deps.Clock.NowMs),
		project: proj,
		pub:     pub,
		tele: emitters.New(id, emitters.Metadata{
			ModelName:     r.deps.Provider.ModelName,
			ModelProvider: r.deps.Provider.ProviderID,
		}),
		readState: tools.NewReadState(),
		firstSub:  make(chan struct{}),
	}
	r.mu.Lock()
	r.sessions[id] = s
	r.mu.Unlock()
	return s, nil
}

func (r *Runtime) logEpoch() string { return r.deps.IDs.LogEpoch() }
