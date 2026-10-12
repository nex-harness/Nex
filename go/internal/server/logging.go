package server

// logging.go is step 2.10 of docs/specs/go-rewrite/phase-2-go-server.md: observability and
// tracing for the Go front. It is the port of two TypeScript things plus one repository rule:
//
//   - LoggingChannelServer (packages/rpc/src/logging-middleware.ts), which decorates the channel
//     server so every call is recorded without the core RPC code knowing;
//   - formatLogPrefix (packages/shared/src/log-format.ts), which fixes the line prefix every
//     process in this repository writes — see internal/adapters/logio for the exact port;
//   - the AGENTS.md logging section, which fixes the level for each class of event.
//
// What this file owns, and why it is not a middleware in internal/rpc:
//
//	internal/rpc must stay transport-agnostic (gate G2.11) and knows nothing about connections,
//	so "give me the trace id" and "which level is this event" cannot live there. internal/server
//	is the only place that sees both a socket (so it has a connection, a mode and a lifetime) and
//	the gateway (which forwards the per-request lines). So the identifiers and the level policy
//	live here, and the gateway — which is what actually sees forwarded frames — receives a
//	ConnectionLog built by this file and emits through it.
//
// The four identifiers, and which is which:
//
//	connectionId  one accepted WebSocket. Stable for the connection's lifetime; the thing every
//	              line of one client's session shares.
//	traceId       the same thing on the wire. It goes into every PromiseError payload, which is a
//	              passthrough key the TS client already copies onto the Error it rebuilds, so a
//	              user who reports a failure can quote an id that appears in both the server log
//	              and their own error object.
//	mode          /ws or /ws/host. Recorded on every line so a log can be filtered per endpoint.
//	spanId        per forwarded request. Not on the wire — a line can carry it, an error payload
//	              cannot, because adding a key the client does not copy would be lost work.
//
// Levels, straight from AGENTS.md and pinned by tests in logging_test.go:
//
//	info   connection open and close — the process/session lifecycle class, one pair per client.
//	debug  every forwarded request, with its channel.method and id. High-frequency protocol
//	       diagnostics, which is exactly what AGENTS.md says must not reach a production disk.
//	warn   an upstream failure the connection survives: a drop mid-flight, a failed HTTP route.
//	error  an upstream failure the connection does not survive: 1013 at connect time, a refused
//	       upgrade, a crash budget spent (that one is emitted by internal/adapters/node).
//
// The rule the levels exist to protect: NO REQUEST BODY IS EVER LOGGED AT INFO. Not truncated, not
// redacted, not behind a flag — bodies are arguments, and arguments are user content (prompts,
// file contents, credentials typed into a provider settings form). A body at debug is a deliberate,
// opt-in choice a deployment makes by turning debug on; a body at info would be in every
// production log forever with no way to opt out. The only body-adjacent thing on a line is the
// channel and method NAMES, which are the service vocabulary and are already known to anyone
// reading a log.

import (
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/logio"
	"github.com/tylinux/nex/go/internal/rpc"
	"github.com/tylinux/nex/go/internal/trace"
)

// LogSource is the prefix token for a Go server log line. It matches the source the Node server
// uses for the same process (formatLogPrefix("nex-server:http", process.pid) in
// packages/server/src/http.ts), so a log tail that groups by source sees one group rather than
// two that happen to be the same process.
const LogSource = "nex-server:http"

// DefaultLogLevel is the production minimum. Debug is off, which is what keeps a forwarded
// request line out of a production disk (see the level table above); AGENTS.md reserves debug for
// exactly these high-frequency diagnostics.
const DefaultLogLevel = slog.LevelInfo

// LogOptions configures the process logger. It is not `Options`: internal/server already has one
// of those, and it configures the HTTP shell.
type LogOptions struct {
	// Level is the minimum level written. Zero selects DefaultLogLevel.
	Level slog.Level
	// Now and PID exist so the prefix is assertable; production leaves them nil and gets the real
	// clock and the real pid.
	Now func() time.Time
	PID int
}

// NewLogger builds the process logger: prefix in the shared format, one line per record, to w.
//
// This is the logger every other seam in the phase takes. internal/gateway and
// internal/adapters/node declare the four methods they need (*slog.Logger satisfies both) and
// receive THIS value, so there is exactly one logger in the process and one place where the
// format and the level default live.
func NewLogger(w io.Writer, opts LogOptions) *slog.Logger {
	return logio.New(w, logio.Options{
		Source: LogSource,
		Level:  opts.Level,
		Now:    opts.Now,
		PID:    opts.PID,
	})
}

// ConnectionLog is one connection's view of the logger: a base logger carrying the connection's
// identifiers, plus the two things that can only be said per request.
//
// The embedded *slog.Logger carries connectionId, traceId and mode, so a call site that logs
// something this file does not name still gets them on the line for free. The methods below cover
// the events the plan enumerates; anything else is log.Info/Debug with those attributes.
//
// A ConnectionLog is safe for concurrent use — the per-request span is built by a method that does
// not mutate the base, and *slog.Logger itself is.
type ConnectionLog struct {
	// Logger is the connection-scoped base. Prefer the methods below; reach for it directly only
	// for an event no method covers.
	*slog.Logger
	connectionID string
	traceID      string
	mode         string
}

// OpenConnection mints a ConnectionLog for an accepted connection. It is the single place a
// connectionId and a traceId come into being, so there is no way for two connections to agree on
// one.
//
// base may be nil, which is the zero configuration: every call below then discards, and no
// connection pays for a nil check. The connection's PATH is deliberately not a parameter: a query
// string can carry a ?token= (the auth path does), and a credential must not reach a log
// (AGENTS.md). mode is the endpoint, which is what a reader actually filters on.
func OpenConnection(base *slog.Logger, mode string) ConnectionLog {
	id := "conn_" + trace.NewTraceID()
	traceID := trace.NewTraceID()
	conn := ConnectionLog{connectionID: id, traceID: traceID, mode: mode}
	if base == nil {
		return conn
	}
	conn.Logger = base.With(
		slog.String("connectionId", id),
		slog.String("traceId", traceID),
		slog.String("mode", mode),
	)
	// info, not debug: AGENTS.md puts process and session lifecycle at info, and this pair
	// (open, close) is the whole lifecycle of a connection. It is two lines per client, ever.
	conn.info("websocket connection opened")
	return conn
}

// ConnectionID is the id shared by every line of one connection.
func (c ConnectionLog) ConnectionID() string { return c.connectionID }

// TraceID is the value stamped into this connection's error payloads. It is the same string the
// connection's log lines carry, which is the whole point: an id in a user-reported error resolves
// to a grep over the server log.
func (c ConnectionLog) TraceID() string { return c.traceID }

// RPCOptions returns the per-connection rpc.Server configuration that stamps c's traceId into
// every PromiseError. It is passed to rpc.NewServer by whoever owns the connection — the gateway
// for production, registrarOwner for the no-upstream path — so both get it from one place.
func (c ConnectionLog) RPCOptions() rpc.Options { return rpc.Options{TraceID: c.traceID} }

// Forwarded records one request the connection handed to the Node upstream: `channel.method id`,
// at debug, with a fresh span id and NO body.
//
// The message shape is the plan's, and the order (channel, method, id) is the order the frame
// header itself stores them in — a reader comparing a Go log with a Node one sees them in the
// same sequence. The span id is an attribute rather than part of the message because it varies
// per line and the message is what someone greps for.
//
// The duration is omitted rather than logged: it is only meaningful when a response comes back,
// which for a forwarded frame happens on another leg this call site does not observe. A half
// measurement is worse than none.
func (c ConnectionLog) Forwarded(channelName, methodName string, requestID int) {
	if c.Logger == nil {
		return
	}
	c.Logger.Debug("rpc forward "+channelName+"."+methodName+" "+itoa(requestID),
		slog.String("spanId", trace.NewSpanID()),
		slog.String("channel", channelName),
		slog.String("method", methodName),
		slog.Int("requestId", requestID))
}

// ForwardFailed records a forwarded request that could not be written upstream, at warn: the
// connection may still be alive, so this is the recoverable class. It repeats the channel.method id
// because "which request failed" is the question an operator asks first, and the span id from
// Forwarded is not necessarily still on screen.
func (c ConnectionLog) ForwardFailed(channelName, methodName string, requestID int, err error) {
	if c.Logger == nil {
		return
	}
	c.Logger.Warn("rpc forward failed "+channelName+"."+methodName+" "+itoa(requestID),
		slog.String("spanId", trace.NewSpanID()),
		slog.String("channel", channelName),
		slog.String("method", methodName),
		slog.Int("requestId", requestID),
		slog.String("error", err.Error()))
}

// UpstreamLost records an upstream that ended mid-connection. warn: recoverable — the client
// reconnects and the next connection gets a fresh upstream leg, so nothing is lost but the
// in-flight requests on this one.
func (c ConnectionLog) UpstreamLost(err error) { c.warn("upstream connection ended", err) }

// UpstreamRefused records an upstream that was not there at connect time. error: this connection
// is closed with 1013 and the client has to reconnect on its own schedule, so it is the
// unrecoverable class for this request even though it is transient for the deployment.
func (c ConnectionLog) UpstreamRefused(err error) { c.error("upstream unavailable", err) }

// Closed ends the lifecycle pair opened by OpenConnection. It is called once, from the same
// deferred teardown that closes the socket, so an open always has exactly one close.
func (c ConnectionLog) Closed(err error) {
	if err == nil {
		c.info("websocket connection closed")
		return
	}
	// warn, not error: a client that navigates away or whose network drops ends a connection
	// with an error every time, and that is not a failure of anything. An error here would train
	// an operator to ignore error lines, which is how the real ones get missed.
	c.warn("websocket connection closed", err)
}

// Accepted records a refused upgrade (bad capability shape, malformed handshake). It is the one
// event here with no connection behind it, so it takes the process logger rather than a
// connection-scoped one.
func Accepted(base *slog.Logger, mode, path string, err error) {
	if base == nil {
		return
	}
	base.Error("websocket upgrade rejected",
		slog.String("mode", mode),
		slog.String("path", path),
		slog.String("error", err.Error()))
}

func (c ConnectionLog) info(msg string) {
	if c.Logger != nil {
		c.Logger.Info(msg)
	}
}

func (c ConnectionLog) warn(msg string, err error) {
	if c.Logger == nil {
		return
	}
	c.Logger.Warn(msg, slog.String("error", err.Error()))
}

func (c ConnectionLog) error(msg string, err error) {
	if c.Logger == nil {
		return
	}
	c.Logger.Error(msg, slog.String("error", err.Error()))
}

// itoa keeps the two id-in-message call sites from pulling strconv in for one call each. It is
// the same shape the gateway's test helpers use.
func itoa(n int) string { return strconv.Itoa(n) }

// Compile-time proof the ConnectionLog satisfies what the gateway needs from a per-connection
// sink. The gateway declares its own four-method interface (so it does not import log/slog); this
// is the assertion that the two agree.
var _ interface {
	Forwarded(channelName, methodName string, requestID int)
	ForwardFailed(channelName, methodName string, requestID int, err error)
	UpstreamLost(err error)
	UpstreamRefused(err error)
	RPCOptions() rpc.Options
} = ConnectionLog{}
