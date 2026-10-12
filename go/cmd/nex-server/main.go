// Command nex-server is the Go front of the strangler rewrite
// (docs/specs/go-rewrite/phase-2-go-server.md). It is the only process a browser or the Desktop
// app talks to: it serves the Web UI's static files, the HTTP routes Go owns, the WebSocket
// endpoints and web authentication, and it forwards every channel and HTTP route it has not ported
// yet to a Node @nex/server child process.
//
// It is a DROP-IN for `packages/server`'s HTTP entry (packages/server/src/entry-http.ts): it
// reads the same environment variables, resolves the same paths and answers the same routes, so
// NEX_WEB_STATIC_ROOT, PORT, NEX_SERVER_HOST and NEX_SERVER_AUTH_TOKEN mean the same thing to both.
// What it adds is the gateway. D7 forbids new environment variables, so everything the extra
// behaviour needs comes from a FILE (gateway-routes.json) or from the variables the TypeScript
// server already reads — see docs/specs/go-rewrite/phase-2-usage.md.
//
// The wiring below is the one place the pieces meet, so it is written down here rather than spread
// across constructors:
//
//	start  ->  resolve env  ->  supervise the Node upstream  ->  wait for its health
//	      ->  build the gateway over it  ->  build the HTTP shell around the gateway
//	      ->  listen  ->  on SIGTERM/SIGINT: stop the listener, then stop the Node tree
//
// The ORDER matters twice. The gateway is constructed BEFORE the HTTP shell, because the shell's
// WebSocket endpoints take the gateway as their connection owner — internal/server builds the
// single one-shot capability store either way, and two stores would mint two incompatible host
// capabilities. And the Node upstream is started BEFORE the gateway exists, because the gateway
// needs an address to dial and has no legitimate "no upstream yet" state: forwarding is the default
// for every channel.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/tylinux/nex/go/internal/adapters/logio"
	"github.com/tylinux/nex/go/internal/server"
)

// usage is what `--help` and an unrecognised argument print. The flag set is deliberately tiny:
// everything configurable comes from the environment the TypeScript server already reads, so a
// drop-in swap cannot be broken by passing the wrong flag.
const usage = `usage: nex-server [--version] [--help]

Serves the Web UI, /api/* and /ws (plus /ws/host). Environment:
  PORT                     listen port (default 3030)
  NEX_SERVER_HOST, HOST    listen host
  NEX_WEB_STATIC_ROOT      built web client directory; static serving is off when unset
  NEX_SERVER_AUTH_TOKEN    web login token; unset means authentication is disabled
  NEX_DATA_BASE_DIR        data base for web-sessions.json and gateway-routes.json (default $HOME)
  NEX_SERVER_WORKSPACE     workspace advertised by GET /api/server-info
  NEX_SERVER_ID            override the derived server id
  NEX_SERVER_NAME          display name
  NEX_SERVER_ROOT          release root; the Node upstream is looked up under its runtime/ dir`

// logSource is the prefix token of this process's lines. It is internal/server.LogSource, used
// rather than a repeated literal: the entry point already imports internal/server for the version
// it prints and the shell it builds, so naming the constant here keeps the one place that could
// disagree with internal/server's own log lines empty.
const logSource = server.LogSource

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run is main's body with its inputs as parameters, so every test drives it without a subprocess.
func run(args []string, stderr io.Writer) int {
	// Level info, prefixed in the shared format. Debug is where the per-forwarded-request protocol
	// lines live, which AGENTS.md says must not reach a production disk, and there is no
	// environment variable to raise the level (D7) — so debug is off in production by
	// construction rather than by configuration.
	log := logio.New(stderr, logio.Options{Source: logSource, Level: logio.LevelInfo})
	for _, arg := range args {
		switch arg {
		case "--version", "-v":
			// The single line the release tooling and build-server.sh's smoke test read. It is the
			// same value the Node bundle reports, because both come from the same root
			// package.json — tsup's __NEX_VERSION__ define here, and -ldflags -X
			// internal/server.Version on the Go side.
			fmt.Fprintln(stderr, "nex-server", server.Version)
			return 0
		case "--help", "-h":
			fmt.Fprintln(stderr, usage)
			return 0
		default:
			fmt.Fprintf(stderr, "nex-server: unknown argument %q\n\n%s", arg, usage)
			return 2
		}
	}
	return serve(log)
}

// fatalExit is the exit code for a startup failure, matching the TypeScript entry's
// `process.exitCode = 1`.
const fatalExit = 1

// signalContext returns a context cancelled on SIGINT/SIGTERM — a terminal's interrupt and what a
// service manager or `docker stop` sends. Both are the ordinary way this process is asked to stop,
// and the Node supervisor arms the same two on its own run loop.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// isCancellation reports whether err is the ordinary end of a signal-driven shutdown, so a stop
// that worked is not reported as a failure.
func isCancellation(err error) bool {
	return errors.Is(err, context.Canceled)
}

// logFatal writes a startup or shutdown failure in the shape the rest of the repository uses for a
// process that cannot boot: one error line carrying the cause, never a bare panic. slog.Logger is
// the parameter type because *slog.Logger is what logio.New returns and what every package in this
// tree takes.
func logFatal(log *slog.Logger, message string, err error) {
	log.Error(message, "err", err)
}
