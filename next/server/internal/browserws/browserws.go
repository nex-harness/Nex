// Package browserws implements the browser-facing WebSocket endpoint /ws.
//
// The wire format is the binary Channel RPC protocol from packages/rpc; one
// frame per WebSocket binary message. The server sends Initialize immediately
// and dispatches Promise/EventListen requests to local channels or forwards
// them to the runner that owns the workspace.
package browserws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/nex-harness/nex/server/internal/local"
	"github.com/nex-harness/nex/server/internal/router"
	"github.com/nex-harness/nex/server/internal/rpcwire"
	"github.com/nex-harness/nex/server/internal/runners"
)

// OutboundQueueSize bounds the per-connection writer queue. On overflow the
// connection is closed so the client reconnects and resyncs.
const OutboundQueueSize = 1024

// Outbound byte budgets. A slow browser must not be able to pin an unbounded
// amount of memory: the frame count alone allows 1024 * 64 MiB with the read
// limit, so the queue is also bounded by bytes and oversized frames are
// rejected outright.
const (
	maxOutboundFrameBytes = 32 << 20
	maxOutboundQueueBytes = 64 << 20
)

// Per-connection resource caps. Authenticated clients may otherwise create
// unbounded goroutines or listen registrations.
const (
	maxPendingPromises      = 4096
	maxListensPerConnection = 2048
)

// Timing for the application-level writer and keepalive.
const (
	writeTimeout = 10 * time.Second
	pingInterval = 25 * time.Second
	pingTimeout  = 45 * time.Second
)

// Deps are the collaborators the browser endpoint needs.
type Deps struct {
	Logger   *slog.Logger
	Registry *runners.Registry
	Local    *local.Registry
	// NewNexAgent builds the per-connection local nex-agent table. The v4
	// hello carries a connection-scoped id and binds the clientId on
	// initialize, so it must never be shared between browser connections.
	// Nil selects local.NewNexAgentLocal.
	NewNexAgent func() *local.NexAgentLocal
	// AllowedOrigins adds accepted Origin host patterns for deployments whose
	// reverse proxy rewrites Host. Empty means "same host as the request"
	// only, which the websocket library enforces for us.
	AllowedOrigins []string
	// ToolIDs holds the server-global terminal/watcher id → runner bindings
	// (batch 1 workspace tools). Nil builds an internal store.
	ToolIDs *IDBindings
}

// Handler serves GET /ws.
type Handler struct {
	deps Deps
	seq  atomic.Uint64
}

// NewHandler builds the /ws handler.
func NewHandler(deps Deps) *Handler {
	if deps.ToolIDs == nil {
		deps.ToolIDs = NewIDBindings()
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	// Runner drop clears its tool id bindings: terminals and watchers die with
	// the runner process, and a stale binding would route reattach attempts to
	// an offline machine.
	if deps.Registry != nil {
		deps.Registry.OnRunnerDisconnected(func(runnerID string) {
			deps.ToolIDs.DropRunner(runnerID)
		})
	}
	return &Handler{deps: deps}
}

// ServeHTTP upgrades and runs one browser connection.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Browsers send Origin on every WS handshake, and the session cookie is
	// SameSite=Lax (not a CSRF defence for WS). Leaving origin verification to
	// the browser alone would make any third-party page able to open an
	// authenticated /ws connection (cross-site WebSocket hijacking), so the
	// library's same-host check stays enabled. AllowedOrigins only widens it
	// for deployments behind a Host-rewriting proxy.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns:  h.deps.AllowedOrigins,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		h.deps.Logger.Warn("browser websocket upgrade failed", "error", err)
		return
	}
	ws.SetReadLimit(rpcwire.DefaultMaxPayload)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	newNexAgent := h.deps.NewNexAgent
	if newNexAgent == nil {
		newNexAgent = local.NewNexAgentLocal
	}
	conn := &connection{
		deps:       h.deps,
		nexAgent:   newNexAgent(),
		ws:         ws,
		id:         fmt.Sprintf("ws-%d-%d", time.Now().UnixNano(), h.seq.Add(1)),
		outbound:   make(chan []byte, OutboundQueueSize),
		done:       make(chan struct{}),
		listens:    make(map[uint32]func()),
		pending:    make(map[uint32]*pendingPromise),
		queuedSize: 0,
	}
	defer conn.close()

	go conn.writeLoop(ctx)
	go conn.keepAlive(ctx)

	conn.enqueueResponse(rpcwire.Response{Type: rpcwire.ResponseInitialize, Body: rpcwire.Undefined{}})

	h.deps.Logger.Debug("browser websocket connected", "connectionId", conn.id)

	reader := rpcwire.NewFrameReader(newMessageReader(ctx, ws), rpcwire.DefaultMaxPayload)
	for {
		frame, err := reader.Next()
		if err != nil {
			return
		}
		if frame.Type != rpcwire.MessageRegular {
			continue
		}
		conn.handleFrame(ctx, frame.Payload)
	}
}

// messageReader adapts WebSocket binary messages into an io.Reader so the
// FrameReader tolerates split or concatenated frames.
type messageReader struct {
	ctx     context.Context
	ws      *websocket.Conn
	current []byte
}

func newMessageReader(ctx context.Context, ws *websocket.Conn) *messageReader {
	return &messageReader{ctx: ctx, ws: ws}
}

func (r *messageReader) Read(p []byte) (int, error) {
	for len(r.current) == 0 {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		typ, data, err := r.ws.Read(r.ctx)
		if err != nil {
			return 0, err
		}
		if typ != websocket.MessageBinary {
			continue
		}
		r.current = data
	}
	n := copy(p, r.current)
	r.current = r.current[n:]
	return n, nil
}

type connection struct {
	deps     Deps
	nexAgent *local.NexAgentLocal
	ws       *websocket.Conn
	id       string

	outbound chan []byte

	mu      sync.Mutex
	closed  bool
	listens map[uint32]func()
	pending map[uint32]*pendingPromise
	// queuedSize is the sum of frame lengths currently buffered in outbound.
	// It is guarded by mu instead of being updated in the writer so the byte
	// budget is exact even while a write is in flight.
	queuedSize int

	done chan struct{}
	once sync.Once
}

// pendingPromise keeps the cancel func together with its identity: a finished
// handler must only delete its own entry, never one a client registered later
// under the same request id.
type pendingPromise struct {
	cancel context.CancelFunc
}

func (c *connection) close() {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		listens := c.listens
		pending := c.pending
		c.listens = make(map[uint32]func())
		c.pending = make(map[uint32]*pendingPromise)
		c.mu.Unlock()

		// Wake the writer/keepalive goroutines. closed is already visible to
		// enqueue under mu, so no producer can add to outbound from here on.
		close(c.done)

		for _, stop := range listens {
			if stop != nil {
				stop()
			}
		}
		for _, entry := range pending {
			entry.cancel()
		}
		c.deps.Registry.UnregisterOwner(c.id)
		if c.ws != nil {
			_ = c.ws.CloseNow()
		}
	})
}

func (c *connection) writeLoop(ctx context.Context) {
	for {
		select {
		case <-c.done:
			return
		case <-ctx.Done():
			c.close()
			return
		case data := <-c.outbound:
			c.mu.Lock()
			c.queuedSize -= len(data)
			c.mu.Unlock()
			writeCtx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			err := c.ws.Write(writeCtx, websocket.MessageBinary, data)
			cancel()
			if err != nil {
				c.close()
				return
			}
		}
	}
}

func (c *connection) keepAlive(ctx context.Context) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
			err := c.ws.Ping(pingCtx)
			cancel()
			if err != nil {
				c.close()
				return
			}
		}
	}
}

// enqueue queues one already-encoded frame. Overflow (frames or bytes) closes
// the connection so a slow browser can never block the caller or grow memory
// without bound; the client reconnects and resyncs.
func (c *connection) enqueue(data []byte) {
	if len(data) > maxOutboundFrameBytes {
		c.deps.Logger.Warn("browser outbound frame too large, closing connection",
			"connectionId", c.id, "bytes", len(data))
		c.closeAsync()
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if c.queuedSize+len(data) > maxOutboundQueueBytes {
		c.mu.Unlock()
		c.deps.Logger.Warn("browser outbound queue byte budget exceeded, closing connection", "connectionId", c.id)
		c.closeAsync()
		return
	}
	c.queuedSize += len(data)
	select {
	case c.outbound <- data:
		c.mu.Unlock()
	default:
		c.queuedSize -= len(data)
		c.mu.Unlock()
		c.deps.Logger.Warn("browser outbound queue overflow, closing connection", "connectionId", c.id)
		c.closeAsync()
	}
}

// closeAsync marks the connection closed immediately (so further enqueues are
// dropped) and performs the teardown in its own goroutine. Teardown sends
// `unlisten` frames to runners and can block on a slow runner socket; doing
// that inline would stall whichever goroutine produced the overflowed event —
// in particular a runner's event pump, which must keep serving other browsers.
func (c *connection) closeAsync() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	go c.close()
}

func (c *connection) enqueueResponse(resp rpcwire.Response) {
	payload, err := rpcwire.EncodeResponse(resp)
	if err != nil {
		c.deps.Logger.Warn("encoding response failed", "error", err)
		return
	}
	c.enqueue(rpcwire.EncodeFrame(rpcwire.Frame{Type: rpcwire.MessageRegular, Payload: payload}))
}

func (c *connection) enqueuePromiseError(id uint32, message, name string) {
	payload, err := rpcwire.EncodePromiseError(id, message, name, "")
	if err != nil {
		c.deps.Logger.Warn("encoding error response failed", "error", err)
		return
	}
	c.enqueue(rpcwire.EncodeFrame(rpcwire.Frame{Type: rpcwire.MessageRegular, Payload: payload}))
}

func (c *connection) enqueuePromiseErrorObj(id uint32, value rpcwire.Value) {
	payload, err := rpcwire.EncodePromiseErrorObj(id, value)
	if err != nil {
		c.deps.Logger.Warn("encoding error object failed", "error", err)
		return
	}
	c.enqueue(rpcwire.EncodeFrame(rpcwire.Frame{Type: rpcwire.MessageRegular, Payload: payload}))
}

func (c *connection) enqueueEvent(id uint32, value rpcwire.Value) {
	payload, err := rpcwire.EncodeEventFire(id, value)
	if err != nil {
		c.deps.Logger.Warn("encoding event fire failed", "error", err)
		return
	}
	c.enqueue(rpcwire.EncodeFrame(rpcwire.Frame{Type: rpcwire.MessageRegular, Payload: payload}))
}

func (c *connection) handleFrame(parent context.Context, payload []byte) {
	request, err := rpcwire.DecodeRequest(payload)
	if err != nil {
		c.deps.Logger.Warn("dropping malformed channel payload", "error", err)
		return
	}
	switch request.Type {
	case rpcwire.RequestPromise:
		// Registration must happen on the read goroutine, before the next frame
		// is read: otherwise a PromiseCancel that the client sends immediately
		// after the promise (e.g. component unmount) can be processed before
		// the handler goroutine records the cancel func and is silently lost.
		if ctx, entry, ok := c.beginPromise(parent, request.ID); ok {
			go c.runPromise(ctx, entry, request)
		}
	case rpcwire.RequestPromiseCancel:
		c.cancelPending(request.ID)
	case rpcwire.RequestEventListen:
		// Listens are registered synchronously in read order: clients subscribe
		// (listen) before issuing the call that triggers the first frame, and
		// spawning a goroutine here would let the call overtake the listen and
		// lose the initial snapshot notification.
		c.handleListen(parent, request)
	case rpcwire.RequestEventDispose:
		c.disposeListen(request.ID)
	}
}

func (c *connection) cancelPending(id uint32) {
	c.mu.Lock()
	entry := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if entry != nil {
		entry.cancel()
	}
}

func (c *connection) disposeListen(id uint32) {
	c.mu.Lock()
	stop := c.listens[id]
	delete(c.listens, id)
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
	c.deps.Registry.UnregisterListen(c.id, id)
}

// beginPromise reserves a promise id and returns a cancellable context. It runs
// synchronously in the read loop so PromiseCancel frames are never lost.
func (c *connection) beginPromise(parent context.Context, id uint32) (context.Context, *pendingPromise, bool) {
	ctx, cancel := context.WithCancel(parent)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cancel()
		return nil, nil, false
	}
	// Bound concurrent in-flight calls: each one owns a goroutine that may wait
	// up to the runner call timeout, so an authenticated client must not be
	// able to grow them without limit.
	if len(c.pending) >= maxPendingPromises {
		c.mu.Unlock()
		cancel()
		c.enqueuePromiseError(id, "too many pending requests", "Error")
		return nil, nil, false
	}
	// Re-using a live promise id would orphan the older request: its reply
	// would carry the same id and its cancel func would be overwritten.
	if _, duplicate := c.pending[id]; duplicate {
		c.mu.Unlock()
		cancel()
		c.enqueuePromiseError(id, "duplicate request id", "Error")
		return nil, nil, false
	}
	entry := &pendingPromise{cancel: cancel}
	c.pending[id] = entry
	c.mu.Unlock()
	return ctx, entry, true
}

func (c *connection) runPromise(ctx context.Context, entry *pendingPromise, request rpcwire.Request) {
	defer func() {
		// Only remove our own entry: the id may already have been reused by a
		// newer request, and deleting that one would make it uncancellable.
		c.mu.Lock()
		if c.pending[request.ID] == entry {
			delete(c.pending, request.ID)
		}
		c.mu.Unlock()
		entry.cancel()
	}()

	env := &local.Env{ConnectionID: c.id}
	route := router.Resolve(request.Channel, request.Name, request.Arg, func(name string) bool {
		_, ok := c.deps.Local.Lookup(name)
		return ok
	})

	switch route.Kind {
	case router.TargetLocal:
		channel, _ := c.deps.Local.Lookup(request.Channel)
		value, err := channel.Call(ctx, env, request.Name, request.Arg)
		c.respondPromise(ctx, request.ID, value, err)

	case router.TargetLocalUnsupported:
		if c.nexAgent.HandlesCall(request.Name) && request.Channel == router.ChannelNexAgent {
			value, err := c.nexAgent.Call(ctx, env, request.Name, request.Arg)
			c.respondPromise(ctx, request.ID, value, err)
			return
		}
		c.enqueuePromiseError(request.ID, "Method not found: "+request.Name, "Error")

	case router.TargetWorkspace:
		c.forwardCall(ctx, request, route.Target())

	case router.TargetFirstRunner:
		arg, err := rpcwire.ToJSON(request.Arg)
		if err != nil {
			c.enqueuePromiseError(request.ID, err.Error(), "Error")
			return
		}
		value, err := c.deps.Registry.CallFirst(ctx, request.Channel, request.Name, arg)
		c.respondForwarded(ctx, request.ID, value, err)

	case router.TargetRunner:
		arg, err := rpcwire.ToJSON(request.Arg)
		if err != nil {
			c.enqueuePromiseError(request.ID, err.Error(), "Error")
			return
		}
		link, ok := c.deps.Registry.Runner(route.RunnerID)
		if !ok {
			c.enqueuePromiseError(request.ID, "runner offline", "Error")
			return
		}
		value, err := link.Call(ctx, request.Channel, request.Name, arg)
		c.respondForwarded(ctx, request.ID, value, err)

	case router.TargetTerminalID:
		c.forwardIdRouted(ctx, request, c.deps.ToolIDs.Terminal, "terminal")

	case router.TargetWatchID:
		c.forwardIdRouted(ctx, request, c.deps.ToolIDs.Watcher, "watcher")

	default:
		c.enqueuePromiseError(request.ID, fmt.Sprintf("Channel name '%s' not found", request.Channel), "Unknown channel")
	}
}

func (c *connection) respondPromise(ctx context.Context, id uint32, value rpcwire.Value, err error) {
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		c.enqueuePromiseError(id, err.Error(), "Error")
		return
	}
	c.enqueueResponse(rpcwire.Response{Type: rpcwire.ResponsePromiseSuccess, ID: id, Body: value})
}

// forwardCall forwards the Channel RPC body verbatim: the browser sends the
// method arguments as an array (ProxyChannel.toService → channel.call(name,
// args)), and the runner applies that array exactly like ProxyChannel.
func (c *connection) forwardCall(ctx context.Context, request rpcwire.Request, target runners.Target) {
	arg, err := rpcwire.ToJSON(request.Arg)
	if err != nil {
		c.enqueuePromiseError(request.ID, err.Error(), "Error")
		return
	}
	// terminal.create / file-watcher.watch mint an id on the runner: record
	// the id → runner binding from the result so the later id-only calls
	// (write/resize/dispose, unwatch, the dynamic listens) route back.
	record := c.bindingRecorderFor(request)
	value, err := c.deps.Registry.CallWorkspace(ctx, target, request.Channel, request.Name, arg)
	if record != nil && err == nil {
		record(value)
	}
	c.respondForwarded(ctx, request.ID, value, err)
}

// bindingRecorderFor returns a result hook when this request mints a tool id.
func (c *connection) bindingRecorderFor(request rpcwire.Request) func(json.RawMessage) {
	kind := ""
	switch request.Channel {
	case router.ChannelTerminal:
		if request.Name != "create" {
			return nil
		}
		kind = "terminal"
	case router.ChannelFileWatcher:
		if request.Name != "watch" {
			return nil
		}
		kind = "watcher"
	default:
		return nil
	}
	return func(result json.RawMessage) {
		// Result shape: {"id":"..."} (terminal.create also carries shell/font
		// fields; watch returns {"id"}).
		var payload struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(result, &payload); err != nil || payload.ID == "" {
			return
		}
		// Resolve the owning runner from the request's original target: the
		// registry's identity/path tables answered the create; ask it again
		// for the same target.
		target := routerTargetOf(request)
		runnerID, ok := c.deps.Registry.RunnerForTarget(target)
		if !ok {
			return
		}
		if kind == "terminal" {
			c.deps.ToolIDs.RecordTerminal(payload.ID, runnerID)
		} else {
			c.deps.ToolIDs.RecordWatcher(payload.ID, runnerID)
		}
	}
}

// forwardIdRouted forwards one id-only call to the runner that owns the id.
func (c *connection) forwardIdRouted(
	ctx context.Context,
	request rpcwire.Request,
	resolveID func(string) (string, bool),
	kind string,
) {
	id := idFieldOf(request.Arg)
	if id == "" {
		c.deps.Logger.Warn("id-routed call without id",
			"kind", kind, "channel", request.Channel, "method", request.Name)
		c.enqueuePromiseError(request.ID, kind+" id is required", "Error")
		return
	}
	runnerID, ok := resolveID(id)
	if !ok {
		c.deps.Logger.Debug("id-routed call without binding",
			"kind", kind, "id", id, "channel", request.Channel, "method", request.Name)
		c.enqueuePromiseError(request.ID, "unknown "+kind+" id: "+id, "Error")
		return
	}
	link, ok := c.deps.Registry.Runner(runnerID)
	if !ok {
		c.enqueuePromiseError(request.ID, "runner offline", "Error")
		return
	}
	arg, err := rpcwire.ToJSON(request.Arg)
	if err != nil {
		c.enqueuePromiseError(request.ID, err.Error(), "Error")
		return
	}
	value, err := link.Call(ctx, request.Channel, request.Name, arg)
	c.respondForwarded(ctx, request.ID, value, err)
}

// routerTargetOf extracts the workspace target of one request's argument.
func routerTargetOf(request rpcwire.Request) runners.Target {
	fields := map[string]string{}
	for _, entry := range argumentObjects(request.Arg) {
		if path, ok := entry["workspacePath"].(string); ok {
			fields["workspacePath"] = path
		}
		if identity, ok := entry["workspaceIdentity"].(string); ok {
			fields["workspaceIdentity"] = identity
		}
	}
	return runners.Target{
		WorkspacePath:     fields["workspacePath"],
		WorkspaceIdentity: fields["workspaceIdentity"],
	}
}

// idFieldOf reads the `id` of an argument: an object field (`{id}`) or a bare
// string (the dynamic-event listens pass the id directly as the listen arg).
func idFieldOf(arg rpcwire.Value) string {
	if id, ok := directString(arg); ok {
		return id
	}
	for _, entry := range argumentObjects(arg) {
		if id, ok := entry["id"].(string); ok && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

// directString unwraps a bare string argument (the dynamic-event listen arg).
func directString(arg rpcwire.Value) (string, bool) {
	switch typed := arg.(type) {
	case string:
		if strings.TrimSpace(typed) != "" {
			return strings.TrimSpace(typed), true
		}
	case json.RawMessage:
		var decoded string
		if err := json.Unmarshal(typed, &decoded); err == nil && strings.TrimSpace(decoded) != "" {
			return strings.TrimSpace(decoded), true
		}
	}
	return "", false
}

// argumentObjects lists the object elements of a positional argument envelope
// (browser calls always wrap args in an array). rpcwire decodes array elements
// as json.RawMessage (tag 5 objects), so both shapes are accepted.
func argumentObjects(arg rpcwire.Value) []map[string]any {
	var elements []any
	switch typed := arg.(type) {
	case []any:
		elements = typed
	case json.RawMessage, []byte:
		var decoded []any
		if err := json.Unmarshal(rawBytes(typed), &decoded); err == nil {
			elements = decoded
		}
	}
	out := make([]map[string]any, 0, len(elements))
	for _, element := range elements {
		switch typed := element.(type) {
		case map[string]any:
			out = append(out, typed)
		case json.RawMessage:
			var decoded map[string]any
			if err := json.Unmarshal(typed, &decoded); err == nil && decoded != nil {
				out = append(out, decoded)
			}
		}
	}
	return out
}

// rawBytes converts a json.RawMessage or []byte to bytes.
func rawBytes(value rpcwire.Value) []byte {
	switch typed := value.(type) {
	case json.RawMessage:
		return typed
	case []byte:
		return typed
	}
	return nil
}

func (c *connection) respondForwarded(ctx context.Context, id uint32, value json.RawMessage, err error) {
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		var remote *runners.RemoteError
		switch {
		case errors.As(err, &remote):
			stack := ""
			name := remote.Name
			if name == "" {
				name = "Error"
			}
			// rpcwire.EncodePromiseError builds JSON itself, so the remote name
			// and message are passed as plain strings.
			payload, encodeErr := rpcwire.EncodePromiseError(id, remote.Message, name, stack)
			if encodeErr == nil {
				c.enqueue(rpcwire.EncodeFrame(rpcwire.Frame{Type: rpcwire.MessageRegular, Payload: payload}))
				return
			}
			c.enqueuePromiseError(id, remote.Message, name)
			return
		case errors.Is(err, runners.ErrNoRunnerConnected):
			c.enqueuePromiseError(id, "no runner connected", "Error")
			return
		case errors.Is(err, runners.ErrAmbiguousWorkspacePath):
			c.enqueuePromiseError(id, "ambiguous workspace path; workspaceIdentity required", "Error")
			return
		case errors.Is(err, runners.ErrNoRunnerForWorkspace):
			c.enqueuePromiseError(id, "no runner connected for workspace", "Error")
			return
		default:
			c.enqueuePromiseError(id, err.Error(), "Error")
			return
		}
	}
	// A runner `result` may omit `value` (void methods): map that to the wire
	// `undefined` value. The runner link uses the rpcwire JSON bridge, so
	// buffers/undefined sentinels must go back through FromJSON.
	body, decodeErr := rpcwire.FromJSON(value)
	if decodeErr != nil {
		c.enqueuePromiseError(id, "invalid runner result: "+decodeErr.Error(), "Error")
		return
	}
	c.enqueueResponse(rpcwire.Response{
		Type: rpcwire.ResponsePromiseSuccess,
		ID:   id,
		Body: body,
	})
}

func (c *connection) handleListen(parent context.Context, request rpcwire.Request) {
	ctx, cancel := context.WithCancel(parent)
	env := &local.Env{ConnectionID: c.id}
	route := router.ResolveEvent(request.Channel, request.Name, request.Arg, func(name string) bool {
		_, ok := c.deps.Local.Lookup(name)
		return ok
	})

	register := func(stop func()) bool {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			cancel()
			if stop != nil {
				stop()
			}
			return false
		}
		if len(c.listens) >= maxListensPerConnection {
			c.mu.Unlock()
			cancel()
			if stop != nil {
				stop()
			}
			c.enqueueEvent(request.ID, rpcwire.Undefined{})
			return false
		}
		previous := c.listens[request.ID]
		c.listens[request.ID] = func() {
			cancel()
			if stop != nil {
				stop()
			}
		}
		c.mu.Unlock()
		// Re-using a listen id must release the previous local subscription
		// (broadcast/window-controller listeners are keyed by channel, not by
		// request id), otherwise it leaks for the lifetime of the connection.
		if previous != nil {
			previous()
		}
		return true
	}

	switch route.Kind {
	case router.TargetLocal:
		channel, _ := c.deps.Local.Lookup(request.Channel)
		emit := func(value rpcwire.Value) { c.enqueueEvent(request.ID, value) }
		stop, err := channel.Listen(ctx, env, request.Name, request.Arg, emit)
		if err != nil {
			c.enqueueEvent(request.ID, rpcwire.Undefined{})
			cancel()
			return
		}
		register(stop)

	case router.TargetLocalUnsupported:
		// Unknown events on a known channel stay silent (never fire), matching
		// the MVP contract in docs/next/README.md.
		register(nil)

	case router.TargetWorkspace, router.TargetFirstRunner:
		arg, err := rpcwire.ToJSON(request.Arg)
		if err != nil {
			c.enqueueEvent(request.ID, rpcwire.Undefined{})
			cancel()
			return
		}
		target := runners.Target{}
		if route.Kind == router.TargetWorkspace {
			target = route.Target()
		}
		emit := func(data json.RawMessage) {
			// Runner event values use the rpcwire JSON bridge.
			value, decodeErr := rpcwire.FromJSON(data)
			if decodeErr != nil {
				c.deps.Logger.Warn("invalid runner event value", "error", decodeErr)
				return
			}
			c.enqueueEvent(request.ID, value)
		}
		if err := c.deps.Registry.RegisterListen(c.id, request.ID, target, request.Channel, request.Name, arg, emit); err != nil {
			// Identity listens are stored even while the runner is offline so a
			// reconnect can re-issue them; only a rejected spec (ambiguous
			// legacy path) reaches this branch.
			c.deps.Logger.Warn("browser listen rejected",
				"channel", request.Channel, "event", request.Name, "error", err)
			c.enqueueEvent(request.ID, rpcwire.Undefined{})
			cancel()
			return
		}
		if !register(nil) {
			// The connection closed between storing the binding and recording
			// the local listen id; UnregisterOwner may have already run before
			// the binding existed, so drop it explicitly (idempotent).
			c.deps.Registry.UnregisterListen(c.id, request.ID)
		}

	case router.TargetTerminalID, router.TargetWatchID:
		// The id-only dynamic listens (terminal data/exit, watcher change):
		// resolve the owning runner from the id binding, then register the
		// listen against that runner's Target{RunnerID} so the registry's
		// runner binding (not path resolution) keeps it alive.
		id := idFieldOf(request.Arg)
		kind := "watcher"
		resolveID := c.deps.ToolIDs.Watcher
		if route.Kind == router.TargetTerminalID {
			kind = "terminal"
			resolveID = c.deps.ToolIDs.Terminal
		}
		if id == "" {
			c.enqueueEvent(request.ID, rpcwire.Undefined{})
			cancel()
			return
		}
		runnerID, ok := resolveID(id)
		if !ok {
			// Unknown id (runner restarted, or a listen racing the create
			// result): stay silent like an unknown event, never fire.
			c.deps.Logger.Debug("id-routed listen without binding",
				"kind", kind, "id", id, "channel", request.Channel)
			register(nil)
			return
		}
		arg, err := rpcwire.ToJSON(request.Arg)
		if err != nil {
			c.enqueueEvent(request.ID, rpcwire.Undefined{})
			cancel()
			return
		}
		emit := func(data json.RawMessage) {
			value, decodeErr := rpcwire.FromJSON(data)
			if decodeErr != nil {
				c.deps.Logger.Warn("invalid runner event value", "error", decodeErr)
				return
			}
			c.enqueueEvent(request.ID, value)
		}
		target := runners.Target{RunnerID: runnerID}
		if err := c.deps.Registry.RegisterListen(c.id, request.ID, target, request.Channel, request.Name, arg, emit); err != nil {
			c.enqueueEvent(request.ID, rpcwire.Undefined{})
			cancel()
			return
		}
		if !register(nil) {
			c.deps.Registry.UnregisterListen(c.id, request.ID)
		}

	default:
		// Unknown channel listens are dropped silently in the TypeScript server
		// too (ChannelServer keeps them pending forever).
		register(nil)
	}
}
