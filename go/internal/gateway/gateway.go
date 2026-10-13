package gateway

// gateway.go is the entry point: the thing internal/server holds, and the two seams it exposes —
// HandleConnection for the WebSocket endpoints, Handler for the HTTP routes Go does not own.
//
// Both seams share ONE router, loaded once at construction. The plan makes the routing table the
// state owner of "who handles channel X" and says it is read at connection start; loading it once
// here and handing each connection an immutable snapshot satisfies that, and it also means a
// routing table edited while the server is running does not half-apply to live connections.
//
// Failure semantics, verbatim from the plan:
//   - upstream down at connection time → close the client with 1013 ("try again later")
//   - upstream drops mid-connection      → close the client socket (the UI reconnects)
//   - never fabricate a response

import (
	"context"
	"fmt"
	"net/http"

	"github.com/coder/websocket"
	"github.com/tylinux/nex/go/internal/rpc"
	server "github.com/tylinux/nex/go/internal/server"
)

// Gateway is the router, the upstream dialer and the HTTP proxy, built once at server start.
type Gateway struct {
	router *Router
	dialer *dialer
	proxy  *HTTPProxy
	log    Logger
	// goChannels are the channels the Go process handles itself. Registering one makes the router
	// route it to Go even when the table says otherwise — the table can only move a channel ONTO
	// Go, never take one OFF, because a handler that exists but is unrouted is the state a
	// half-finished migration step leaves behind and silently ignoring it would serve it from Node.
	goChannels map[string]rpc.Channel
}

// New builds a Gateway from opts, loading the routing table from disk.
//
// A routing table that cannot be understood is a startup error, not a warning: continuing with a
// table we ignored would move channels between processes without anyone deciding to.
func New(opts Options) (*Gateway, error) {
	router, err := LoadRouter(opts.RoutesPath)
	if err != nil {
		return nil, err
	}
	dialer, err := newDialer(opts.Upstream, opts.Log, opts.DialTimeout, opts.HostCapabilityHeader)
	if err != nil {
		return nil, err
	}
	proxy, err := NewHTTPProxy(opts.Upstream.BaseURL, opts.Upstream.AuthToken, router)
	if err != nil {
		return nil, err
	}
	proxy.log = opts.Log
	gateway := &Gateway{
		router:     router,
		dialer:     dialer,
		proxy:      proxy,
		log:        opts.Log,
		goChannels: map[string]rpc.Channel{},
	}
	for _, name := range opts.GoChannels {
		gateway.goChannels[name] = nil
	}
	return gateway, nil
}

// Router exposes the routing table, for the HTTP shell's decision on a path.
func (g *Gateway) Router() *Router { return g.router }

// HTTPHandler returns the reverse proxy for the routes Go does not own, or nil when there is no
// upstream. A nil handler is the right answer for "nothing to forward", so the caller can mount it
// unconditionally and let a nil be a pass-through.
func (g *Gateway) HTTPHandler() http.Handler {
	if g == nil || g.proxy == nil {
		return nil
	}
	return g.proxy
}

// RegisterChannel records a Go-implemented channel. Registering it routes the name to Go for
// every future connection.
func (g *Gateway) RegisterChannel(name string, channel rpc.Channel) {
	if channel == nil {
		return
	}
	g.goChannels[name] = channel
}

// RegisterChannels records every Go-implemented channel a source provides. It is
// the bulk form a service Registry feeds (step 3.1): the registry owns the name →
// channel set, the gateway owns routing, and this is the single handoff point
// between them.
func (g *Gateway) RegisterChannels(names []string, channelFor func(name string) rpc.Channel) {
	for _, name := range names {
		if channel := channelFor(name); channel != nil {
			g.RegisterChannel(name, channel)
		}
	}
}

// Handle owns one accepted client connection and returns when it has ended.
//
// It implements server.ConnectionOwner. client is the already-upgraded socket; this method never
// closes it, because the caller owns it and only the caller can close it with a status code.
//
// The order is the plan's: the routing table is frozen for this connection, then the upstream leg
// opens, then the Go rpc.Server exists and Initialize goes out, then forwarding runs. An upstream
// that is not there is reported as ErrUpstreamUnavailable so the caller can close with 1013.
//
// log is the connection's logger, minted by internal/server for THIS connection (step 2.10). It
// arrives here rather than being built here so that the connectionId on a forwarding line is the
// same one on the open/close pair the endpoint logged, and so the traceId in the error payloads
// below is the same id. A caller that hands over a zero ConnectionLog gets the discards, which is
// why nothing in the forwarding path has to test the logger for nil.
func (g *Gateway) Handle(ctx context.Context, client rpc.Socket, mode string, log server.ConnectionLog) error {
	connectionMode := ConnectionMode(mode)
	upstream, err := g.dialer.Open(ctx, mode)
	if err != nil {
		// error, not warn: this connection is over. The client is closed with 1013 and has to
		// reconnect, so nothing on it can be recovered. The traceId on the line is the one in the
		// connection's error payloads.
		log.UpstreamRefused(err)
		return fmt.Errorf("%w: %w", server.ErrUpstreamUnavailable, err)
	}

	socket := newLockedSocket(client)
	// rpc.Server is constructed over the SAME socket, so its responses and the gateway's forwarded
	// frames share one write path (lockedSocket) and one writer — the property that makes frames
	// for one id keep their order across both directions.
	//
	// TraceID comes from the connection log, so every PromiseError the Go side sends carries the
	// id a client can quote and an operator can grep.
	server := rpc.NewServer(socket, ctx, log.RPCOptions())

	connection, err := newConnection(connectionOptions{
		router: g.router,
		up:     upstream,
		client: socket,
		log:    g.log,
		conn:   log,
		mode:   connectionMode,
	})
	if err != nil {
		_ = upstream.Close()
		server.Close()
		return err
	}
	connection.server = server

	// Go-owned channels are registered BEFORE the read loop starts. A request for one that arrived
	// earlier would otherwise sit in the 1 s pending queue and be answered with "timed out" — the
	// symptom of a registration that lost a race with the first frame.
	for name, channel := range g.goChannels {
		if channel != nil && g.router.ChannelOwnedByGo(name) {
			server.RegisterChannel(name, channel)
		}
	}

	runErr := connection.Run(ctx)
	// The upstream dropped (or never came up) mid-connection: the client socket closes with it.
	// The UI reconnects, and the web client already handles a close. warn, because the next
	// connection gets a fresh leg and nothing durable was lost.
	if runErr != nil {
		log.UpstreamLost(runErr)
	}
	return runErr
}

// CloseTryAgain closes a client socket with WebSocket 1013 ("try again later"), which is what the
// plan prescribes when the upstream was unavailable at connection time.
//
// It takes the raw rpc.Socket rather than a ws.Conn so the method satisfies server.ConnectionOwner
// without either package naming the other's type, and it degrades to a plain Close on a socket
// that cannot carry a status code (an in-memory pair in a test).
func (g *Gateway) CloseTryAgain(socket rpc.Socket, reason string) error {
	if closer, ok := socket.(interface {
		CloseStatus(code websocket.StatusCode, reason string) error
	}); ok {
		return closer.CloseStatus(websocket.StatusTryAgainLater, reason)
	}
	return socket.Close()
}

// Compile-time proof that the gateway is the strangler server expects.
var _ interface {
	Handle(ctx context.Context, socket rpc.Socket, mode string, log server.ConnectionLog) error
	CloseTryAgain(socket rpc.Socket, reason string) error
} = (*Gateway)(nil)
