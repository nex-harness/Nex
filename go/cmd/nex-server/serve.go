package main

// serve.go is the wiring: the one place where the environment, the Node upstream, the gateway and
// the HTTP shell are assembled into a process. Everything else in the Go tree is a library with a
// single owner of its state; this file is where the owners are told about each other, and where
// the ORDER of that happens is load-bearing.
//
//	serve
//	  ├─ resolveConfig      env -> host, port, static root, auth token
//	  ├─ newSupervisor     the Node upstream: argv, data base, per-start secret
//	  ├─ Start + WaitReady GET /api/auth/session 200
//	  ├─ gateway.New        routing table + upstream address + proxy
//	  ├─ server.NewHandler  static, /api/server-info, auth, /ws, /ws/host, proxy tail
//	  └─ http.Server.ListenAndServe, then on SIGTERM/SIGINT: shutdown the listener, Stop the tree
//
// Why the Node upstream is REQUIRED rather than optional: the gateway forwards by default, so a
// gateway without one could only ever serve Go-owned channels — which today is none, because
// every service channel still lives in Node. Starting without an upstream would produce a server
// that answers /api/server-info and 404s on everything the UI actually calls. That is a wiring
// mistake, not a deployment, so it fails at startup (the same decision gateway.ErrNoUpstream makes).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/node"
	"github.com/tylinux/nex/go/internal/adapters/serverenv"
	"github.com/tylinux/nex/go/internal/gateway"
	"github.com/tylinux/nex/go/internal/server"
	"github.com/tylinux/nex/go/internal/services"
	"github.com/tylinux/nex/go/internal/services/broadcast"
)

// config is the resolved deployment. Every field comes from an environment variable the
// TypeScript server already reads (D7: no new variables), so a deployment written for the Node
// server runs unchanged against this one.
type config struct {
	host string
	port int
	// staticRoot is NEX_WEB_STATIC_ROOT. Empty disables static serving entirely, exactly as
	// `staticRoot?.trim()` does in http.ts — there is no default web/ directory to fall back to,
	// because a Go binary has no idea where a web build might be.
	staticRoot string
	// authToken is NEX_SERVER_AUTH_TOKEN. Empty disables web authentication. It is read here with
	// the same trim-and-test rule entry-http.ts uses, so a whitespace-only token means "off" on
	// both sides rather than "on, with an empty password".
	authToken string
	// nodeEntry is the Node upstream to spawn. There is no environment variable for it and none is
	// added: it is resolved from the release layout, which is a FILE (see resolveNodeEntry).
	nodeEntry string
}

// resolveConfig reads the environment. Host resolution is entry-http.ts's chain verbatim —
// NEX_SERVER_HOST, then HOST, then the empty string, which net.Listen reads as "all interfaces" —
// and PortFromEnv keeps the `Number(PORT) || 3030` rule including the "0 and non-numeric fall back
// to the default" part.
func resolveConfig() config {
	host := serverenv.Trimmed("NEX_SERVER_HOST")
	if host == "" {
		host = serverenv.Trimmed("HOST")
	}
	return config{
		host:       host,
		port:       server.PortFromEnv(),
		staticRoot: serverenv.Trimmed("NEX_WEB_STATIC_ROOT"),
		authToken:  serverenv.Trimmed("NEX_SERVER_AUTH_TOKEN"),
		nodeEntry:  resolveNodeEntry(),
	}
}

// listenAddr is host:port. net.JoinHostPort is used rather than string concatenation so an IPv6
// host produces the bracketed form, and a "localhost" host (which resolves to ::1 on a
// dual-stack machine) still works — Node's listen(port, host) has the same requirement.
func (c config) listenAddr() string {
	return net.JoinHostPort(c.host, strconv.Itoa(c.port))
}

// serve is main's body for the no-argument case.
func serve(log *slog.Logger) int {
	cfg := resolveConfig()
	ctx, stopSignal := signalContext()
	defer stopSignal()

	upstream, err := startUpstream(ctx, cfg, log)
	if err != nil {
		// A SIGTERM that lands while the upstream is still coming up is a stop the operator asked
		// for, not a failure: the signal context is already cancelled, so WaitReady returns
		// ctx.Err() and reporting it at error level would put "error" lines in the log of every
		// clean shutdown. Exit 0 and let the deferred cleanup run.
		if isCancellation(err) {
			log.Info("nex-server stopped during startup")
			return 0
		}
		logFatal(log, "node upstream did not become ready", err)
		return fatalExit
	}
	defer stopUpstream(upstream, log)

	handler, resolved, err := buildHandler(cfg, upstream, log)
	if err != nil {
		logFatal(log, "http shell startup failed", err)
		return fatalExit
	}

	listener, err := net.Listen("tcp", cfg.listenAddr())
	if err != nil {
		logFatal(log, "listen failed", fmt.Errorf("%s: %w", cfg.listenAddr(), err))
		return fatalExit
	}
	// A single log line that answers "what is this deployment" from the log alone: the address the
	// UI should be pointed at, the version it will report through /api/server-info, and whether
	// auth is on. It is written AFTER the listener exists, so the address in it is real even when
	// the port was 0 — which is not reachable here (PortFromEnv falls back to 3030 for 0) but is
	// reachable through the upstream's own port discovery, and the same care applies.
	log.Info("nex-server listening",
		"addr", listener.Addr().String(),
		"version", server.Version,
		"authRequired", resolved.AuthRequired,
		"staticRoot", orNone(resolved.StaticRoot),
		"workspace", orNone(resolved.Workspace),
		"dataBaseDir", node.DataBaseDir(),
		"routes", upstream.routerSource,
		"upstream", upstream.supervisor.UpstreamBaseURL(),
	)

	srv := &http.Server{
		Handler: handler,
		// No WriteTimeout: /ws is a long-lived upgrade and a server-wide write deadline would cut
		// every WebSocket connection at the deadline. ReadHeaderTimeout still applies — it is what
		// bounds a slow-header attack, and it is not inherited by an upgraded connection the way a
		// WriteTimeout is.
		ReadHeaderTimeout: 30 * time.Second,
	}

	// The signal has to reach the LISTENER, not just the contexts. Cancelling ctx stops the Node
	// supervisor's run loop and every per-request context, but an http.Server blocked in Accept
	// does not observe a context: without this Shutdown the process would log its stop lines and
	// then sit there forever holding the port, which is the orphan Phase 1's process-tree work
	// exists to prevent — one level up, on ourselves.
	//
	// The order below is deliberate. Shutdown first, because it closes the listener and drains
	// in-flight requests, so nothing new arrives while the upstream is being torn down. The
	// upstream stop is the deferred call, so it runs on the way out of this function.
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(listener) }()
	select {
	case err := <-serveDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logFatal(log, "http server failed", err)
			return fatalExit
		}
	case <-ctx.Done():
		log.Info("shutting down")
		// A context NOT derived from the signal one, for the same reason the upstream stop uses a
		// fresh one: ctx is already cancelled, and a shutdown bounded by it would skip the drain
		// and kill live requests. The timeout is what keeps a stuck connection from blocking the
		// exit forever — after it, Close forces the listener and every connection down.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		err := srv.Shutdown(shutdownCtx)
		cancel()
		if err != nil {
			log.Warn("graceful shutdown did not finish, closing", "err", err)
			_ = srv.Close()
		}
		// Wait for Serve to return so the listener is provably closed before the Node tree is torn
		// down: a request still arriving on a live listener after the upstream died would be
		// answered with a 502 for something the client did right.
		serveErr := <-serveDone
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logFatal(log, "http server failed", serveErr)
			return fatalExit
		}
	}
	log.Info("nex-server stopped")
	return 0
}

// shutdownGrace bounds the graceful HTTP drain on SIGTERM/SIGINT. It is generous relative to a
// normal request and short relative to a service manager's StopTimeout, so a stuck long-lived
// connection cannot turn a stop into a SIGKILL of the whole process.
const shutdownGrace = 5 * time.Second

// buildHandler assembles the gateway and the HTTP shell.
//
// The gateway is built FIRST and handed to the shell as Options.Gateway. That is not a style
// preference: internal/server.NewWebSocket owns the single one-shot host-capability store, and the
// shell takes a pre-built endpoint set through Options.Endpoints when the caller owns the
// connection owner itself. Passing the gateway as the owner — rather than building the endpoints
// twice and setting the owner afterwards — is what guarantees one store and therefore one ticket
// per POST /api/rpc-host-capability.
func buildHandler(cfg config, upstream *upstreamProcess, log *slog.Logger) (http.Handler, *server.Config, error) {
	// The service registry (step 3.1) is the Go-side ServiceCollection. The
	// broadcast channel is the first real service ported into it; every later
	// Phase-3 tier registers here the same way. The gateway still owns ROUTING,
	// so a registered channel only serves traffic once gateway-routes.json
	// marks it "go" — registering early can never flip a channel by accident.
	registry := services.NewRegistry()
	broadcastHub := broadcast.NewHub()
	registry.MustRegister(broadcast.ChannelName, &broadcast.Channel{Service: broadcast.NewService(broadcastHub)})

	gw, err := gateway.New(gateway.Options{
		// Empty selects gateway.DefaultRoutesFilePath(): <data base>/.nex/v2/gateway-routes.json.
		// A missing file is not an error — LoadRouter falls back to "everything to Node", which is
		// the correct default while no service has moved yet.
		Upstream: gateway.UpstreamConfig{
			BaseURL:   upstream.supervisor.UpstreamBaseURL(),
			AuthToken: upstream.supervisor.UpstreamToken(),
		},
		Log: log,
	})
	if err != nil {
		return nil, nil, err
	}
	upstream.routerSource = gw.Router().Source()

	// Registry → gateway handoff: the registry owns the name → channel set,
	// the gateway owns routing. Names the routing table still sends to Node
	// are simply never dispatched here.
	gw.RegisterChannels(registry.ChannelNames(), registry.LookupOptional)

	handler, resolved, err := server.NewHandler(server.Options{
		Host:          cfg.host,
		Port:          cfg.port,
		StaticRoot:    cfg.staticRoot,
		SPAFallback:   cfg.staticRoot != "",
		AuthToken:     cfg.authToken,
		AuthRequired:  cfg.authToken != "",
		WorkspacePath: serverenv.Trimmed("NEX_SERVER_WORKSPACE"),
		Gateway:       gw,
		WSConfig:      &server.WSConfig{Log: log},
	})
	if err != nil {
		return nil, nil, err
	}
	return handler, resolved, nil
}

// orNone renders an empty string as "none" for a log line, so a log reader does not have to tell
// an unset field from a field that failed to print.
func orNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}
