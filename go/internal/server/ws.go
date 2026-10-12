package server

// ws.go is step 2.7 of docs/specs/go-rewrite/phase-2-go-server.md: the one-shot host capability
// and the two WebSocket endpoints. It is the Go port of the WebSocket half of
// packages/server/src/http.ts (createNodeWebSocket, the /ws and /ws/host registrations,
// POST /api/rpc-host-capability and setupChannelServer).
//
// Ordering is load-bearing and reproduces the TypeScript route table exactly:
//
//	auth middleware ("*")           ->  401 before anything else on a protected path
//	  /ws                           ->  web-remote-replayable
//	  POST /api/rpc-host-capability ->  mint a capability (an ordinary /api/* route)
//	  /ws/host                      ->  consume the capability; 401
//	                                    {"error":"Invalid or expired host capability"} on failure,
//	                                    else desktop-continuous
//
// so the capability check runs AFTER auth, exactly as `app.use("/ws/host", …)` sits behind
// `app.use("*", webAuth.middleware())` in the source. A caller with no session gets 401
// "Unauthorized"; a caller WITH a session but no capability gets 401 "Invalid or expired host
// capability". The two bodies are what the desktop client branches on.
//
// The per-connection channel set is a stub here: step 2.8 replaces StubChannel with the gateway
// router while keeping this file's accept/dispose wiring. Everything above it — upgrade, mode
// selection, per-connection rpc.Server construction, disposal on close — is already final.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/tylinux/nex/go/internal/adapters/ws"
	"github.com/tylinux/nex/go/internal/rpc"
	"github.com/tylinux/nex/go/internal/server/hostcap"
	"github.com/tylinux/nex/go/internal/server/webauth"
)

// HostCapabilityHeader is NEX_RPC_HOST_CAPABILITY_HEADER from packages/shared/src/channels.ts.
// The desktop client sends the ticket here and the server consumes it on the /ws/host upgrade.
const HostCapabilityHeader = "x-nex-rpc-host-capability"

// The client modes of the two endpoints, verbatim from setupChannelServer's `clientMode` argument
// in packages/server/src/http.ts. The value decides the role of the per-connection
// INexAgentService scope (trusted-host-relay vs terminal-client), which step 2.8 wires; the mode
// is fixed now so that routing decision has exactly one owner.
const (
	// ModeWebRemoteReplayable is /ws. Always a terminal client: a browser cannot become a trusted
	// host by setting the legacy mode header, and the capability store is not consulted at all.
	ModeWebRemoteReplayable = "web-remote-replayable"
	// ModeDesktopContinuous is /ws/host, reachable only with a fresh capability.
	ModeDesktopContinuous = "desktop-continuous"
)

// Paths registered here. capabilityPath is an /api/* route, so auth guards it exactly as it guards
// the Node one; wsPath and wsHostPath are protected by the same isTokenProtectedPath rule.
const (
	capabilityPath = "/api/rpc-host-capability"
	wsPath         = "/ws"
	wsHostPath     = "/ws/host"
)

// invalidCapabilityBody is the exact body of the 401 the TypeScript returns from the /ws/host
// middleware. The desktop client matches on it, so the bytes are fixed.
const invalidCapabilityBody = `{"error":"Invalid or expired host capability"}`

// WSConfig configures the WebSocket endpoints and the capability route. The zero value is the
// production configuration: a 30 s TTL store with the real clock and RNG, rpc.Server defaults and
// no logging.
type WSConfig struct {
	// Capabilities is the one-shot store. Nil builds the default in-memory store, matching
	// `createHostCapabilityStore()` called with no options.
	Capabilities *hostcap.Store
	// RPCOptions configures each per-connection rpc.Server (pending timeout, concurrency,
	// back-pressure). The zero value uses internal/rpc's defaults, which match the TS ones.
	RPCOptions rpc.Options
	// Log receives per-connection lifecycle events. Nil discards them.
	Log *slog.Logger
}

// ChannelRegistrar builds the per-connection channel set and returns the disposer that tears it
// down when the socket closes. It is the seam step 2.8 fills with the gateway router; today it
// installs the stub channel. The map-per-connection shape is already the router's shape, so wiring
// it later touches this function alone.
type ChannelRegistrar func(server *rpc.Server) (dispose func())

// WebSocket owns the host capability store and the two WebSocket endpoints. It is created once per
// HTTP server and is safe for concurrent use; every accepted connection gets its own rpc.Server.
type WebSocket struct {
	caps     *hostcap.Store
	rpcOpts  rpc.Options
	log      *slog.Logger
	mw       *webauth.Middleware
	routes   *webauth.Routes
	register ChannelRegistrar
}

// NewWebSocket builds the endpoint set. authToken is the same NEX_SERVER_AUTH_TOKEN the auth
// middleware and the /api/auth/* routes read.
//
// The middleware and the routes must share ONE session store and ONE rate limiter, because
// `createWebAuth()` builds a single store and hands the same instance to both `middleware()` and
// `mountRoutes()`. Two stores would mean a session created by POST /api/auth/login is unknown to
// the middleware that guards /ws, so every logged-in browser would still get 401 on the socket.
// The store is in-memory here, matching the Node server when sessionsFilePath is absent; the file
// path is wired in step 2.6's caller (internal/server.Options).
func NewWebSocket(authToken string, cfg WSConfig) *WebSocket {
	caps := cfg.Capabilities
	if caps == nil {
		caps = hostcap.New(hostcap.Options{})
	}
	store := webauth.NewSessionStore(authToken, "", nil, nil)
	return &WebSocket{
		caps:     caps,
		rpcOpts:  cfg.RPCOptions,
		log:      cfg.Log,
		mw:       webauth.NewMiddleware(authToken, store, nil, nil),
		routes:   webauth.NewRoutes(authToken, store, nil, nil),
		register: defaultRegistrar,
	}
}

// SetRegistrar replaces the per-connection channel builder. Step 2.8 calls this once at
// construction to install the gateway router; tests use it to install their own channels.
func (s *WebSocket) SetRegistrar(register ChannelRegistrar) {
	if register != nil {
		s.register = register
	}
}

// AuthMiddleware returns the auth middleware this endpoint set uses, so the HTTP shell can mount
// it in front of every route exactly as `app.use("*", webAuth.middleware())` does. Mounting it in
// the caller keeps the ordering — auth before capability — visible at the one place it matters.
func (s *WebSocket) AuthMiddleware() *webauth.Middleware { return s.mw }

// MountRoutes dispatches /api/auth/* before falling through to next. The shell calls it at the
// head of its /api dispatcher, mirroring webAuth.mountRoutes(app) being registered before
// /api/server-info and the capability route.
func (s *WebSocket) MountRoutes(w http.ResponseWriter, r *http.Request, next http.Handler) {
	s.routes.Serve(w, r, next)
}

// HandledRoute reports whether the auth routes own this path.
func (s *WebSocket) HandledRoute(pathname string) bool { return s.routes.Handled(pathname) }

// Capabilities exposes the store: the issuing route writes through it and tests issue directly.
func (s *WebSocket) Capabilities() *hostcap.Store { return s.caps }

// Handler returns the handler for the capability and WebSocket routes. Any request it does not own
// falls through to next, so the caller keeps control of the ordering (auth first) and of the
// 404/static behavior for everything else.
//
// pathName is the already dot-segment-resolved, still percent-encoded request path — the same
// string the Hono router sees. A false result routes the request to next, which is Hono's
// treatment of a target it could not parse.
func (s *WebSocket) Handler(pathName func(*http.Request) (string, bool), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, ok := pathName(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		// /ws/host's capability check lives in the Hono middleware, which runs for ANY request to
		// that path — including a plain GET, which the TypeScript answers 401 and never upgrades.
		// Keeping the capability check ahead of the upgrade guard reproduces that order exactly.
		switch path {
		case wsHostPath:
			if !s.caps.Consume(r.Header.Get(HostCapabilityHeader)) {
				writeRawJSON(w, http.StatusUnauthorized, invalidCapabilityBody)
				return
			}
			if isUpgradeRequest(r) {
				s.handleWebSocket(w, r, ModeDesktopContinuous)
				return
			}
			next.ServeHTTP(w, r)
			return
		case wsPath:
			if isUpgradeRequest(r) {
				s.handleWebSocket(w, r, ModeWebRemoteReplayable)
				return
			}
			next.ServeHTTP(w, r)
			return
		case capabilityPath:
			s.issueCapability(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isUpgradeRequest ports the guard at the top of Hono's upgradeWebSocket helper:
// `if (c.req.header("upgrade")?.toLowerCase() !== "websocket") return;`. A plain GET /ws is not a
// WebSocket handshake, so it must fall through to whatever comes next — for /ws and /ws/* that is
// the static handler, which answers 404 because isStaticFallbackAllowed refuses a protected path.
// Dropping this guard would make a plain GET /ws answer with a WebSocket handshake error instead
// of the recorded 404, which is an observable difference the static test already pins.
func isUpgradeRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// issueCapability ports `app.post("/api/rpc-host-capability", c => c.json(caps.issue()))`. Only
// POST is registered, as in Hono; any other method is not this route and falls through to the 404
// the shell produces.
func (s *WebSocket) issueCapability(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeNotFound(w)
		return
	}
	writeJSONResponse(w, http.StatusOK, s.caps.Issue())
}

// handleWebSocket performs the upgrade and then owns the connection.
//
// It serves the per-connection rpc.Server on THIS goroutine. That is deliberate: net/http has
// already handed the connection to the library at this point, so the handler goroutine is free,
// and blocking it here means the deferred disposal runs exactly when the socket ends — the same
// single place the TypeScript registers socket.onClose.
//
// Two observable behaviors are reproduced:
//   - Initialize is produced once, by the Go server. The upstream's Initialize (step 2.8) is
//     swallowed rather than forwarded.
//   - On a rejected upgrade the WebSocket library has already written its own HTTP error, so this
//     function returns without touching the ResponseWriter. The Node stack refuses the upgrade the
//     same way (@hono/node-ws ends the socket during its app.request pass), which is why a client
//     sees the status code, not a body.
func (s *WebSocket) handleWebSocket(w http.ResponseWriter, r *http.Request, mode string) {
	conn, err := ws.Accept(w, r, ws.Options{})
	if err != nil {
		s.logf("websocket upgrade rejected", "path", r.URL.Path, "mode", mode, "error", err)
		return
	}

	// The connection context is derived from the request context but NOT cancelled when the
	// handler returns: net/http cancels r.Context() as soon as ServeHTTP returns, which would kill
	// a live connection immediately. context.Background() is the correct parent for a hijacked
	// connection; the socket's own error ends the loop.
	ctx, cancel := context.WithCancel(context.Background())
	server := rpc.NewServer(conn, ctx, s.rpcOpts)
	dispose := s.register(server)
	defer func() {
		// Same teardown as socket.onClose in setupChannelServer: the connection scope and the
		// window attachment (both step 2.8) first, then the channel server, then the socket.
		if dispose != nil {
			dispose()
		}
		cancel()
		server.Close()
		_ = conn.Close()
		s.logf("websocket closed", "path", r.URL.Path, "mode", mode)
	}()
	if err := server.Serve(ctx); err != nil {
		s.logf("websocket ended", "path", r.URL.Path, "mode", mode, "error", err)
	}
}

// logf emits one lifecycle line at info through the configured logger, or discards it. Step 2.10
// replaces this with the connectionId/traceId logger; the level (info for open and close) is the
// one the plan fixes.
func (s *WebSocket) logf(msg string, args ...any) {
	if s.log != nil {
		s.log.Info(msg, args...)
	}
}

// writeJSONResponse marshals a payload with the JSON content type Hono's c.json sets.
func writeJSONResponse(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	writeRawJSON(w, status, string(body))
}

// writeRawJSON writes an already-marshaled body, used for the capability 401 whose exact bytes are
// part of the contract the desktop client matches on.
func writeRawJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
