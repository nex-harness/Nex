// Command nex-agent is the Go agent. The host spawns it as `app-server --stdio`
// (NEX_AGENT_RUNTIME.spawnArgs); stdout carries protocol frames only and every
// log line goes to stderr.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"

	"github.com/tylinux/nex/go/internal/protocol"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "0.0.0-dev"

const usage = "usage: nex-agent app-server --stdio | nex-agent --version"

// claimStdout returns the stream that carries protocol frames and redirects the
// process-wide os.Stdout to stderr, so a stray fmt.Println can never corrupt the
// frame channel. It only covers writes that go through os.Stdout; it does not
// reroute file descriptor 1 for native code, which this binary does not link.
func claimStdout() io.Writer {
	frames := os.Stdout
	os.Stdout = os.Stderr
	return frames
}

func run(args []string, stdin io.Reader, frames io.Writer, stderr io.Writer) int {
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	switch {
	case len(args) == 1 && args[0] == "--version":
		fmt.Fprintln(stderr, "nex-agent", version)
		fmt.Fprintln(frames, version)
		return 0
	// `agent-server` is accepted as an alias: the TypeScript CLI treats both
	// commands identically (cli/src/run.ts).
	case len(args) == 2 && (args[0] == "app-server" || args[0] == "agent-server") && args[1] == "--stdio":
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	log.Info("agent starting", "version", version)
	writer := protocol.NewFrameWriter(frames)
	db, err := openStorage(ctx, writer)
	if err != nil {
		log.Error("storage startup failed", "err", err)
		return 1
	}
	defer db.Close()
	rt, err := buildRuntime(ctx, writer, db)
	if err != nil {
		log.Error("runtime startup failed", "err", err)
		return 1
	}
	rt.StartFlushTimer(ctx, rt.FlushWindowMs())
	err = protocol.Serve(ctx, stdin, writer, rt, log)
	if err != nil {
		log.Error("protocol session ended with error", "err", err)
		return 1
	}
	log.Info("stdin closed, exiting")
	return 0
}

func main() {
	frames := claimStdout()
	os.Exit(run(os.Args[1:], os.Stdin, frames, os.Stderr))
}
