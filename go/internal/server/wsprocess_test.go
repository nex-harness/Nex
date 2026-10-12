package server

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
)

// TestReplayServerProcess is not a test: it is the harness scripts/go-rewrite/replay-rpc-go.mjs
// spawns so the Node client half of the replay (the real @nex/rpc codec and the `ws` package
// talking to the Go server over a socket) can run against the Go handler.
//
// It is skipped unless NEX_REPLAY_LISTEN is set, so `go test ./internal/server` is unaffected. With
// the variable set it listens on 127.0.0.1:0, prints `NEX_REPLAY_URL=ws://host:port` on stdout,
// and serves until the process is killed. NEX_SERVER_AUTH_TOKEN is honoured so the R10 half of the
// replay can exercise the auth-enabled deployment.
//
// The Go-only half of the same comparison (R01 and R11 fed straight into rpc.Server) lives in
// wsreplay_test.go; between them, no byte of the recording is only ever checked by Go against Go.
func TestReplayServerProcess(t *testing.T) {
	if os.Getenv("NEX_REPLAY_LISTEN") == "" {
		t.Skip("set NEX_REPLAY_LISTEN=1 to serve a replay target for replay-rpc-go.mjs")
	}
	handler, _, err := NewHandler(Options{AuthToken: os.Getenv("NEX_SERVER_AUTH_TOKEN")})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	fmt.Printf("NEX_REPLAY_URL=ws://%s\n", listener.Addr().String())
	// Serve until the parent kills the process; a failed accept is not interesting here.
	_ = (&http.Server{Handler: handler}).Serve(listener)
}
