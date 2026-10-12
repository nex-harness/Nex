// Package gateway is step 2.8 of docs/specs/go-rewrite/phase-2-go-server.md: the strangler seam
// between the Go server and the Node upstream it has not ported yet.
//
// Architecture (the plan's diagram — state owners and event order):
//
//	Browser/Desktop ──ws──► Go server ──► rpc.Server (per connection)
//	                                        │ header[2] = channelName
//	                                        ├─ channel owned by Go?  ──► Go handler
//	                                        └─ otherwise ───────────► Upstream leg (1 per client connection)
//	                                                                   Go raw-forwards the SAME Regular frame bytes to
//	                                                                   Node's /ws (or /ws/host) and returns Node's frames
//
// Order guarantees this package keeps:
//   - frames for one request id keep their order — one forwarder goroutine per direction, and the
//     upstream write side is serialized behind the same queue that carries the client leg;
//   - Initialize (200) is produced by Go ONCE. The upstream's own Initialize is swallowed on the
//     way back, so a client sees exactly one handshake no matter how many legs exist;
//   - 101 (cancel) and 103 (dispose) are forwarded iff the request they name was forwarded, decided
//     by the per-connection id→route map, never by re-inspecting the channel.
//
// The state owner of "who handles channel X" is the routing table, loaded once at connection start
// and immutable for that connection's lifetime. There is no hot reload in Phase 2.
package gateway

import (
	"errors"
	"path/filepath"
	"time"

	server "github.com/tylinux/nex/go/internal/server"
)

// Route names a handler owner. They are the only two values the file accepts; anything else is a
// typo the operator must fix, not a silent default.
const (
	// RouteGo is served by the Go process itself.
	RouteGo = "go"
	// RouteNode is forwarded to the Node upstream.
	RouteNode = "node"
)

// RoutesFileName is the fixed file name under the app config dir. It is not configurable, for the
// same reason webauth.SessionsFileName is not: Node derives its own paths from getAppConfigDir(),
// and a second name would split the two implementations' state. D7 forbids a new environment
// variable for this, so a file in the config dir is the configuration surface.
const RoutesFileName = "gateway-routes.json"

// TableVersion is the only accepted `"version"` in the file. It is bumped when the shape changes;
// an unknown version is a startup error rather than a best-effort read, because a half-understood
// routing table silently moves channels between processes.
const TableVersion = 1

// DefaultRoutesFilePath reproduces `join(getAppConfigDir(), "gateway-routes.json")`, where
// getAppConfigDir() is <data base>/.nex/v2 (packages/services/src/paths.ts). The data base is
// NEX_DATA_BASE_DIR or $HOME, exactly as webauth.DefaultSessionsFilePath resolves it.
func DefaultRoutesFilePath() string {
	return filepath.Join(appConfigDir(), RoutesFileName)
}

// Options configures the gateway. The zero value is unusable (it has no upstream), so callers must
// set Upstream; everything else has a production default.
type Options struct {
	// RoutesPath is the routing table file. Empty selects DefaultRoutesFilePath.
	RoutesPath string
	// Upstream addresses the Node process. Required.
	Upstream UpstreamConfig
	// GoChannels are the channels the Go process handles itself, consulted before the file. This
	// is how step 2.7's structural ownership (and any later Go service) is expressed without
	// putting it in a user-editable file.
	GoChannels []string
	// Log receives per-connection and forwarding lifecycle events. Nil discards them.
	Log Logger
	// DialTimeout bounds the upstream handshake. Zero selects DefaultDialTimeout.
	DialTimeout time.Duration
	// HostCapabilityHeader is the header Node consumes on /ws/host. Empty selects the shared
	// constant, duplicated here so this package does not import internal/server (which imports it).
	HostCapabilityHeader string
}

// Logger is the slog-shaped sink the gateway logs through. Declaring the two methods it needs
// instead of importing log/slog keeps the package's surface small and makes assertions in tests
// trivial; *slog.Logger satisfies it.
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// ConnectionLog is one client connection's logging scope, built by internal/server when the socket
// is accepted (step 2.10). The gateway does not construct one: the ids have to be the same on the
// endpoint's open/close lines, on the forwarding lines and inside the error payloads, and the only
// way to guarantee that is for one place to mint all three.
//
// It is aliased rather than imported as a named type so the rest of this package reads as
// `conn.Forwarded(...)` instead of `conn server.ConnectionLog.Forwarded(...)`. The dependency
// direction is unchanged: gateway already imports internal/server, and internal/server does not
// import gateway.
type ConnectionLog = server.ConnectionLog

// DefaultDialTimeout bounds one upstream handshake. It is short enough that a Node that is still
// starting makes the CLIENT fail fast (and retry), rather than parking the connection goroutine
// for the OS TCP timeout. Step 2.9's supervision owns the "wait for Node to be ready" policy; this
// timeout is only about one attempt.
const DefaultDialTimeout = 5 * time.Second

// CloseTryAgainLater is the WebSocket close code for "upstream unavailable, try again later".
// RFC 6455 reserves 1013 for exactly this, and it is what a client should distinguish from a
// normal teardown: the UI already reconnects on any close, and 1013 additionally says the retry
// is worth making. 1013 is application-defined, so it is legal to send from an application.
const CloseTryAgainLater = 1013

// ErrUnknownChannel collects every channel name in the routing table that is not a real service
// channel. Typo protection: a table that silently ignored "fil" would leave that channel on Node
// forever while the operator believed it had moved.
type ErrUnknownChannel struct {
	Names []string
}

func (e *ErrUnknownChannel) Error() string {
	return "gateway-routes.json lists channels that do not exist: " + joinNames(e.Names)
}

// ErrBadRoutes reports a structurally invalid table: a wrong version, an unknown target, a
// duplicate key, or trailing content after the object.
type ErrBadRoutes struct {
	Path string
	Why  string
}

func (e *ErrBadRoutes) Error() string {
	if e.Path == "" {
		return "gateway-routes.json: " + e.Why
	}
	return e.Path + ": " + e.Why
}

// ErrNoUpstream is returned by New when no upstream address is configured. Forwarding is the
// default for every channel, so a gateway without an upstream could only ever serve Go channels —
// which is never a useful deployment and is almost always a wiring mistake.
var ErrNoUpstream = errors.New("gateway: no upstream configured")

func joinNames(names []string) string {
	out := ""
	for i, name := range names {
		if i > 0 {
			out += ", "
		}
		out += name
	}
	return out
}
